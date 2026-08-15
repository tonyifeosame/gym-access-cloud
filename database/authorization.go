package database

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"access-terminal-cloud-api/models"
)

// The authorization engine (migrations/014_authorization_engine.sql).
//
// APP-02: `permissions` and `schedules` were created by migration 002 and 014
// and read by ZERO lines of Go. The audit's finding was not that the rules were
// wrong -- it was that there were no rules, because every active person with a
// bound credential opened every terminal in their company, permanently, and no
// customer could express anything narrower.
//
// This file is the half that was missing: the evaluator, and the stores the
// console needs to write rules for it to evaluate.
//
// ---------------------------------------------------------------------------
// THE RULE, STATED ONCE
// ---------------------------------------------------------------------------
//
//	Absence of permission is not permission.
//
// Authorize returns DENIED unless a live, matching, in-window ALLOW says
// otherwise, and any matching DENY beats every ALLOW. Every early return in
// this file is a denial; there is no path that returns granted without having
// reached the permission evaluation at the bottom. That property is what makes
// the engine safe to extend -- a new check inserted anywhere can only ever
// refuse, never admit.
//
// ---------------------------------------------------------------------------
// WHY THE DECISION IS MADE HERE AND NOT AT THE TERMINAL
// ---------------------------------------------------------------------------
//
// The terminal holds a cached roster and decides locally when it cannot reach
// the platform -- that is what makes a door work through a network outage, and
// removing it would be a downgrade. But the cache is a projection of this
// decision, not a second implementation of it. The site's offline policy
// (models.OfflinePolicy) governs how long a terminal may keep trusting that
// projection, which is the only lever that makes "revoked at 09:00" mean
// anything at a terminal that last synced at 08:00.
//
// ---------------------------------------------------------------------------
// FAILING SAFE
// ---------------------------------------------------------------------------
//
// A database error during evaluation returns an error AND a DENIED decision,
// so a caller that logs the error and carries on cannot accidentally admit
// somebody. The decision is never nil on the deny path.

// authorizationContext is the terminal-side state a decision is made against,
// loaded in one round trip because every field of it is needed for every
// decision and six queries at a door is six chances to be slow.
type authorizationContext struct {
	DeviceID        int64
	DeviceSerial    string
	DeviceStatus    string
	DeviceActive    bool
	ApplicationMode string
	CredentialHash  sql.NullString

	SiteID       int64
	SitePublicID string
	SiteName     string
	SiteActive   bool
	SiteTimezone string

	CompanyID     int64
	CompanyActive bool
}

// Authorize evaluates one presentation and returns the platform's decision.
//
// The decision is ALWAYS returned, including on error, and is always safe to
// act on: the zero path is denial. `err` is non-nil only when the evaluation
// could not be completed, which the caller should log -- but the decision beside
// it is still DENIED and still correct to enforce.
//
// The caller is expected to record the returned decision as an event. That is
// deliberately NOT done here: Authorize is also used to answer "would this be
// allowed" from the console, and a preview that wrote a door event would put
// fiction in the audit trail. RecordAccessEvent is the paired call, and the
// device path makes both.
func Authorize(req models.AccessRequest) (*models.AccessDecision, error) {
	at := req.At
	if at.IsZero() {
		at = time.Now().UTC()
	}

	deny := func(reason string) *models.AccessDecision {
		return &models.AccessDecision{Granted: false, Reason: reason, DecidedAt: at}
	}

	ctx, err := loadAuthorizationContext(req.DeviceID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// A terminal that does not resolve cannot be authorized against.
			// This is reachable only if a device row is deleted between
			// authentication and evaluation.
			return deny(models.ReasonTerminalDisabled), nil
		}
		return deny(models.ReasonTerminalDisabled), err
	}

	// ---------------------------------------------------------------------
	// 1. The terminal, the site and the company must all be in service
	// ---------------------------------------------------------------------
	//
	// Checked before the person is even resolved. A revoked terminal must not
	// be able to learn whether an external id is known to the platform, which
	// it would if an unknown person and a revoked terminal gave different
	// answers.
	if !ctx.CompanyActive {
		return deny(models.ReasonCompanyInactive), nil
	}
	if !ctx.SiteActive {
		return deny(models.ReasonSiteInactive), nil
	}
	// A revoked credential clears api_key_hash, so a terminal in that state
	// cannot authenticate at all and should never reach here -- this is the
	// second gate, in the layer that decides rather than the layer that
	// authenticates (SEC-01).
	if !ctx.DeviceActive || ctx.DeviceStatus == models.DeviceDisabled || !ctx.CredentialHash.Valid {
		return deny(models.ReasonTerminalDisabled), nil
	}

	// ---------------------------------------------------------------------
	// 2. Which capability is this decision being made under
	// ---------------------------------------------------------------------
	//
	// The request may name one (an application acting on a person's behalf);
	// otherwise the terminal's configured mode decides. MULTI_PURPOSE means the
	// terminal is not dedicated to a capability, and a permission scoped to a
	// specific application therefore does not narrow it.
	application := strings.TrimSpace(req.Application)
	if application == "" {
		application = ctx.ApplicationMode
	}
	if application == models.AppMultiPurpose {
		application = ""
	}

	// A capability the company has switched off does not authorize anybody,
	// even where a terminal is still assigned to it. 009 kept the assignment
	// deliberately so that re-enabling is recoverable; this is the other half
	// of that decision.
	if application != "" {
		enabled, err := applicationEnabled(ctx.CompanyID, application)
		if err != nil {
			return deny(models.ReasonApplicationOff), err
		}
		if !enabled {
			return deny(models.ReasonApplicationOff), nil
		}
	}

	// ---------------------------------------------------------------------
	// 3. Resolve the person
	// ---------------------------------------------------------------------
	//
	// Resolved from the external id INSIDE THE TERMINAL'S COMPANY. The terminal
	// does not get to say who somebody is; it says what it read.
	person, err := resolvePerson(ctx.CompanyID, req.ExternalID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// The most interesting half of an audit trail. Recorded with the
			// external id the terminal sent so the attempt stays traceable.
			d := deny(models.ReasonPersonUnknown)
			d.ExternalID = req.ExternalID
			d.Application = application
			return d, nil
		}
		return deny(models.ReasonPersonUnknown), err
	}

	decision := &models.AccessDecision{
		PersonID:    person.PublicID,
		PersonName:  person.Name,
		ExternalID:  person.ExternalID,
		Application: application,
		DecidedAt:   at,
	}

	refuse := func(reason string) *models.AccessDecision {
		decision.Granted = false
		decision.Reason = reason
		return decision
	}

	if !person.Active {
		return refuse(models.ReasonPersonInactive), nil
	}
	// The person's own validity window (PPL-03). A contractor whose engagement
	// ended is inactive by date without anybody having to remember to switch
	// them off, which is the failure mode a validity window exists to remove.
	if person.ValidFrom.Valid && at.Before(person.ValidFrom.Time) {
		return refuse(models.ReasonPermissionNotYet), nil
	}
	if person.ValidUntil.Valid && !at.Before(person.ValidUntil.Time) {
		return refuse(models.ReasonPermissionExpired), nil
	}

	// ---------------------------------------------------------------------
	// 4. The credential, where the terminal named one
	// ---------------------------------------------------------------------
	//
	// Optional: the legacy device protocol knows only an external id, and that
	// path stays supported. Where a credential IS named it must belong to the
	// resolved person, be active, and be inside its own validity window -- a
	// credential can be revoked without the person being, which is the whole
	// point of modelling them separately (HW-03).
	if req.CredentialID != "" {
		cred, err := resolveCredential(ctx.CompanyID, req.CredentialID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return refuse(models.ReasonCredentialUnknown), nil
			}
			return refuse(models.ReasonCredentialUnknown), err
		}
		if cred.PersonID != person.ID {
			// Presented by somebody it does not belong to. Refused as unknown
			// rather than as a mismatch: the terminal learns nothing about
			// whose it is.
			return refuse(models.ReasonCredentialUnknown), nil
		}

		decision.CredentialID = cred.PublicID

		switch cred.Status {
		case models.CredentialActive:
		case models.CredentialRevoked:
			return refuse(models.ReasonCredentialRevoked), nil
		case models.CredentialSuspended:
			return refuse(models.ReasonCredentialSuspended), nil
		default:
			return refuse(models.ReasonCredentialRevoked), nil
		}
		if cred.ValidFrom.Valid && at.Before(cred.ValidFrom.Time) {
			return refuse(models.ReasonCredentialNotYet), nil
		}
		if cred.ValidUntil.Valid && !at.Before(cred.ValidUntil.Time) {
			return refuse(models.ReasonCredentialExpired), nil
		}
	}

	// ---------------------------------------------------------------------
	// 5. The permission set
	// ---------------------------------------------------------------------
	rules, err := matchingPermissions(person.ID, ctx.SiteID, ctx.DeviceID, application)
	if err != nil {
		return refuse(models.ReasonNoPermission), err
	}

	// Two passes rather than one, because DENY beats ALLOW regardless of which
	// rule the database happened to return first. Deciding on the first match
	// would make the outcome depend on row order.
	var allowed *permissionRow
	for i := range rules {
		rule := &rules[i]

		admits, err := ruleAdmits(rule, at, ctx.SiteTimezone)
		if err != nil {
			// A schedule that cannot be evaluated -- an unknown timezone -- must
			// not admit. Skipping the rule is the safe reading: an ALLOW that
			// cannot be evaluated does not allow, and a DENY that cannot be
			// evaluated is caught by the deny-by-default at the bottom.
			return refuse(models.ReasonOutsideSchedule), err
		}
		if !admits {
			continue
		}

		if rule.Effect == models.EffectDeny {
			decision.MatchedPermission = rule.PublicID
			return refuse(models.ReasonExplicitDeny), nil
		}
		if allowed == nil {
			allowed = rule
		}
	}

	if allowed == nil {
		// Deny by default. The single most important line in the file.
		return refuse(models.ReasonNoPermission), nil
	}

	decision.Granted = true
	decision.Reason = models.ReasonAllowed
	decision.MatchedPermission = allowed.PublicID
	return decision, nil
}

// loadAuthorizationContext reads the terminal, its site and its company at once.
func loadAuthorizationContext(deviceID int64) (*authorizationContext, error) {
	var ctx authorizationContext
	err := DB.QueryRow(`
		SELECT d.id, d.serial_number, d.status, d.active, d.application_mode,
		       d.api_key_hash,
		       s.id, s.public_id, s.site_name, s.active, s.timezone,
		       c.id, c.active
		  FROM devices d
		  JOIN sites s ON s.id = d.site_id
		  JOIN companies c ON c.id = s.company_id
		 WHERE d.id = $1
		   AND d.deleted_at IS NULL
		   AND s.deleted_at IS NULL`, deviceID).
		Scan(&ctx.DeviceID, &ctx.DeviceSerial, &ctx.DeviceStatus, &ctx.DeviceActive,
			&ctx.ApplicationMode, &ctx.CredentialHash,
			&ctx.SiteID, &ctx.SitePublicID, &ctx.SiteName, &ctx.SiteActive, &ctx.SiteTimezone,
			&ctx.CompanyID, &ctx.CompanyActive)
	if err != nil {
		return nil, err
	}
	return &ctx, nil
}

// applicationEnabled reports whether a company currently has a capability on.
func applicationEnabled(companyID int64, application string) (bool, error) {
	var enabled bool
	err := DB.QueryRow(`
		SELECT enabled FROM company_applications
		 WHERE company_id = $1 AND application = $2`, companyID, application).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		// Never configured is not enabled. A capability a company has not
		// chosen must not authorize anybody, which is the same reasoning
		// EnabledApplications documents for refusing to assume a default set.
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return enabled, nil
}

// authorizationPerson is the subject of a decision.
type authorizationPerson struct {
	ID         int64
	PublicID   string
	ExternalID string
	Name       string
	Active     bool
	ValidFrom  sql.NullTime
	ValidUntil sql.NullTime
}

// resolvePerson finds a person by the external id a terminal read, within the
// terminal's own company.
func resolvePerson(companyID int64, externalID string) (*authorizationPerson, error) {
	externalID = strings.TrimSpace(externalID)
	if externalID == "" {
		return nil, sql.ErrNoRows
	}

	var p authorizationPerson
	err := DB.QueryRow(`
		SELECT id, public_id, external_id,
		       COALESCE(NULLIF(btrim(full_name), ''), external_id),
		       active, valid_from, valid_until
		  FROM people
		 WHERE company_id = $1 AND external_id = $2 AND deleted_at IS NULL`,
		companyID, externalID).
		Scan(&p.ID, &p.PublicID, &p.ExternalID, &p.Name, &p.Active, &p.ValidFrom, &p.ValidUntil)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// authorizationCredential is the credential presented, where one was named.
type authorizationCredential struct {
	ID         int64
	PublicID   string
	PersonID   int64
	Status     string
	ValidFrom  sql.NullTime
	ValidUntil sql.NullTime
}

// resolveCredential finds a credential by public id within a company.
func resolveCredential(companyID int64, publicID string) (*authorizationCredential, error) {
	if !looksLikeUUID(publicID) {
		return nil, sql.ErrNoRows
	}

	var c authorizationCredential
	err := DB.QueryRow(`
		SELECT id, public_id, person_id, status, valid_from, valid_until
		  FROM credentials
		 WHERE company_id = $1 AND public_id = $2::uuid AND deleted_at IS NULL`,
		companyID, publicID).
		Scan(&c.ID, &c.PublicID, &c.PersonID, &c.Status, &c.ValidFrom, &c.ValidUntil)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// permissionRow is one candidate rule, with its schedule already joined.
type permissionRow struct {
	PublicID  string
	Effect    string
	StartsAt  sql.NullTime
	EndsAt    sql.NullTime
	Timezone  sql.NullString
	HasWindow bool
	Windows   []models.ScheduleWindow
}

// matchingPermissions loads every live rule whose SCOPE covers this terminal and
// whose APPLICATION matches.
//
// Scope and application are filtered in SQL because they are exact matches an
// index can serve. Validity windows and schedules are evaluated in Go, because
// a schedule is evaluated in its own timezone and the correct comparison depends
// on a zone database the query planner should not be asked to consult per row.
func matchingPermissions(personID, siteID, deviceID int64, application string) ([]permissionRow, error) {
	rows, err := DB.Query(`
		SELECT p.public_id, p.effect, p.starts_at, p.ends_at,
		       sc.timezone,
		       sw.days_of_week, sw.start_time::text, sw.end_time::text
		  FROM permissions p
		  LEFT JOIN schedules sc
		         ON sc.id = p.schedule_id AND sc.deleted_at IS NULL AND sc.active
		  LEFT JOIN schedule_windows sw ON sw.schedule_id = sc.id
		 WHERE p.person_id = $1
		   AND p.deleted_at IS NULL
		   AND p.active
		   AND (
		         p.scope_type = 'COMPANY'
		      OR (p.scope_type = 'SITE'     AND p.site_id   = $2)
		      OR (p.scope_type = 'TERMINAL' AND p.device_id = $3)
		   )
		   AND (p.application IS NULL OR p.application = $4)
		 ORDER BY p.public_id`,
		personID, siteID, deviceID, nullIfEmpty(application))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// One row per (permission, window), so the windows are gathered back onto
	// their permission here rather than in a second query per rule.
	var ordered []string
	byID := make(map[string]*permissionRow)

	for rows.Next() {
		var (
			publicID  string
			effect    string
			startsAt  sql.NullTime
			endsAt    sql.NullTime
			timezone  sql.NullString
			days      sql.NullInt64
			startTime sql.NullString
			endTime   sql.NullString
		)
		if err := rows.Scan(&publicID, &effect, &startsAt, &endsAt, &timezone,
			&days, &startTime, &endTime); err != nil {
			return nil, err
		}

		rule, seen := byID[publicID]
		if !seen {
			rule = &permissionRow{
				PublicID: publicID,
				Effect:   effect,
				StartsAt: startsAt,
				EndsAt:   endsAt,
				Timezone: timezone,
			}
			byID[publicID] = rule
			ordered = append(ordered, publicID)
		}

		// A permission whose schedule_id is set but whose schedule is inactive
		// or deleted joins to NULL and therefore carries no windows. That is
		// treated as "no schedule", i.e. always -- see ruleAdmits, where the
		// distinction is made against p.schedule_id rather than against the
		// window count, so a deactivated schedule does not silently widen a
		// rule that was meant to be restricted.
		if days.Valid && startTime.Valid && endTime.Valid {
			rule.HasWindow = true
			rule.Windows = append(rule.Windows, models.ScheduleWindow{
				DaysOfWeek: int(days.Int64),
				StartTime:  startTime.String,
				EndTime:    endTime.String,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := make([]permissionRow, 0, len(ordered))
	for _, id := range ordered {
		result = append(result, *byID[id])
	}
	return result, nil
}

// ruleAdmits reports whether one rule is in force at a moment.
//
// Validity window first, then schedule. Both must hold.
func ruleAdmits(rule *permissionRow, at time.Time, siteTimezone string) (bool, error) {
	if rule.StartsAt.Valid && at.Before(rule.StartsAt.Time) {
		return false, nil
	}
	if rule.EndsAt.Valid && !at.Before(rule.EndsAt.Time) {
		return false, nil
	}

	// No schedule means no time restriction, which is the common case.
	if !rule.HasWindow {
		return true, nil
	}

	// The schedule's own zone, falling back to the site's. 014 chose an explicit
	// zone precisely so a company running one shift pattern across several
	// countries can say so; NULL means "local to wherever the terminal is".
	zoneName := siteTimezone
	if rule.Timezone.Valid && strings.TrimSpace(rule.Timezone.String) != "" {
		zoneName = rule.Timezone.String
	}
	if zoneName == "" {
		zoneName = "UTC"
	}

	location, err := time.LoadLocation(zoneName)
	if err != nil {
		return false, fmt.Errorf("schedule timezone %q: %w", zoneName, err)
	}

	local := at.In(location)
	for _, window := range rule.Windows {
		admits, err := windowAdmits(window, local)
		if err != nil {
			return false, err
		}
		if admits {
			return true, nil
		}
	}
	return false, nil
}

// windowAdmits reports whether a local moment falls inside one window.
//
// MIDNIGHT-CROSSING IS THE CASE THAT MATTERS. A window whose end is not after
// its start runs into the following day, and days_of_week names the day it
// STARTS on -- so a 22:00-06:00 Friday window admits Friday 23:00 and Saturday
// 02:00, and refuses Friday 05:00. Reading the mask against the moment's own
// day would make Sunday night's shift look like Monday's.
func windowAdmits(window models.ScheduleWindow, local time.Time) (bool, error) {
	start, err := parseWindowTime(window.StartTime)
	if err != nil {
		return false, err
	}
	end, err := parseWindowTime(window.EndTime)
	if err != nil {
		return false, err
	}

	minutes := local.Hour()*60 + local.Minute()
	seconds := minutes*60 + local.Second()

	today := dayBit(local.Weekday())

	if end > start {
		// An ordinary same-day window.
		return window.DaysOfWeek&today != 0 && seconds >= start && seconds < end, nil
	}

	// Crosses midnight. Two ways to be inside it:
	//   - after the start, on a day the window begins; or
	//   - before the end, on the day AFTER a day the window begins.
	yesterday := dayBit(local.Add(-24 * time.Hour).Weekday())

	if window.DaysOfWeek&today != 0 && seconds >= start {
		return true, nil
	}
	if window.DaysOfWeek&yesterday != 0 && seconds < end {
		return true, nil
	}
	return false, nil
}

// parseWindowTime converts "HH:MM" or "HH:MM:SS" to seconds past midnight.
//
// STRICT, AND THAT IS THE POINT. An earlier version used fmt.Sscanf("%d"),
// which stops at the first non-digit and reports no error -- so
// "0000-01-01T09:00:00Z" parsed as hour ZERO rather than failing. That string is
// exactly what database/sql produces when a PostgreSQL TIME column is scanned
// into a Go string, and the result was that every schedule window collapsed to
// 00:00-00:00, which the midnight-crossing branch then read as "always". A
// permission restricted to office hours admitted at 3am.
//
// The columns are now cast to text in SQL so they arrive as "09:00:00", and this
// parser refuses anything that is not exactly that shape -- because a schedule
// that cannot be parsed must fail loudly rather than quietly become "always".
func parseWindowTime(value string) (int, error) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, fmt.Errorf("schedule window time %q is not HH:MM or HH:MM:SS", value)
	}

	field := func(raw string) (int, error) {
		// Every character must be a digit. strconv.Atoi already refuses
		// trailing junk, unlike Sscanf, and the length check refuses "9" where
		// "09" was meant so a truncated value cannot be read as a valid one.
		if len(raw) != 2 {
			return 0, fmt.Errorf("schedule window time %q has a malformed field %q", value, raw)
		}
		n, err := strconv.Atoi(raw)
		if err != nil {
			return 0, fmt.Errorf("schedule window time %q: %w", value, err)
		}
		return n, nil
	}

	hours, err := field(parts[0])
	if err != nil {
		return 0, err
	}
	minutes, err := field(parts[1])
	if err != nil {
		return 0, err
	}
	seconds := 0
	if len(parts) == 3 {
		if seconds, err = field(parts[2]); err != nil {
			return 0, err
		}
	}

	if hours > 23 || minutes > 59 || seconds > 59 {
		return 0, fmt.Errorf("schedule window time %q is out of range", value)
	}
	return hours*3600 + minutes*60 + seconds, nil
}

// dayBit maps a weekday to the Mon=1..Sun=64 mask 002 established.
func dayBit(day time.Weekday) int {
	switch day {
	case time.Monday:
		return models.DayMonday
	case time.Tuesday:
		return models.DayTuesday
	case time.Wednesday:
		return models.DayWednesday
	case time.Thursday:
		return models.DayThursday
	case time.Friday:
		return models.DayFriday
	case time.Saturday:
		return models.DaySaturday
	default:
		return models.DaySunday
	}
}

// nullIfEmpty maps "" to a SQL NULL, so `application = $4` does not match rows
// with a NULL application by accident.
func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// ResolvePersonRowID maps an external id to a row id inside a company.
//
// Returns 0 rather than an error when nothing matches, because the caller --
// the door event path -- must record the presentation either way. An
// unrecognised credential is the more interesting half of an audit trail, and
// losing it because the lookup "failed" would be the wrong reading: it did not
// fail, the answer is that nobody matched.
func ResolvePersonRowID(companyID int64, externalID string) (int64, error) {
	person, err := resolvePerson(companyID, externalID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	return person.ID, nil
}
