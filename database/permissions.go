package database

import (
	"database/sql"
	"errors"
	"strings"

	"access-terminal-cloud-api/models"
)

// Permission and schedule stores (migrations/014_authorization_engine.sql).
//
// The write half of the authorization engine. database/authorization.go
// evaluates; this is where the rules it evaluates come from.
//
// EVERY FUNCTION TAKES A companyID AND EVERY QUERY FILTERS ON IT, which is the
// contract the rest of this package keeps and the reason the tenancy boundary is
// checkable at all. A permission names a person, a site and a terminal, and all
// three are resolved INSIDE the caller's company -- so a rule cannot be written
// that points at another tenant's person or another tenant's door, and the
// attempt is a 404 rather than a silent cross-tenant grant.

// ---------------------------------------------------------------------------
// Permissions
// ---------------------------------------------------------------------------

// ListPersonPermissions returns every live rule for one person.
func ListPersonPermissions(companyID int64, externalID string) ([]models.Permission, error) {
	rows, err := DB.Query(`
		SELECT pm.public_id, p.public_id, pm.scope_type,
		       s.public_id, s.site_name, d.serial_number, d.device_name,
		       pm.application, pm.effect,
		       sc.public_id, sc.name,
		       pm.starts_at, pm.ends_at, pm.active, pm.created_at, pm.updated_at
		  FROM permissions pm
		  JOIN people p ON p.id = pm.person_id
		  LEFT JOIN sites s ON s.id = pm.site_id
		  LEFT JOIN devices d ON d.id = pm.device_id
		  LEFT JOIN schedules sc ON sc.id = pm.schedule_id
		 WHERE pm.company_id = $1
		   AND p.external_id = $2
		   AND p.deleted_at IS NULL
		   AND pm.deleted_at IS NULL
		 ORDER BY pm.effect DESC, pm.scope_type, pm.created_at`,
		companyID, externalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	permissions := make([]models.Permission, 0)
	for rows.Next() {
		var (
			p            models.Permission
			siteID       sql.NullString
			siteName     sql.NullString
			serial       sql.NullString
			deviceName   sql.NullString
			application  sql.NullString
			scheduleID   sql.NullString
			scheduleName sql.NullString
			startsAt     sql.NullTime
			endsAt       sql.NullTime
		)
		if err := rows.Scan(&p.ID, &p.PersonID, &p.ScopeType,
			&siteID, &siteName, &serial, &deviceName,
			&application, &p.Effect, &scheduleID, &scheduleName,
			&startsAt, &endsAt, &p.Active, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}

		p.SiteID = siteID.String
		p.SiteName = siteName.String
		p.DeviceSerial = serial.String
		p.DeviceName = deviceName.String
		p.Application = application.String
		p.ScheduleID = scheduleID.String
		p.ScheduleName = scheduleName.String
		if startsAt.Valid {
			t := startsAt.Time
			p.StartsAt = &t
		}
		if endsAt.Valid {
			t := endsAt.Time
			p.EndsAt = &t
		}

		permissions = append(permissions, p)
	}
	return permissions, rows.Err()
}

// permissionTargets is the resolved row ids behind a request's public ids.
type permissionTargets struct {
	PersonID   int64
	SiteID     sql.NullInt64
	DeviceID   sql.NullInt64
	ScheduleID sql.NullInt64
}

// resolvePermissionTargets turns the public ids in a request into row ids,
// refusing anything outside the caller's company.
//
// The refusal is what makes this an authorization boundary rather than a lookup:
// a request naming another tenant's site gets ErrSiteNotFound, which the handler
// answers 404 to, and no rule is written.
func resolvePermissionTargets(companyID int64, externalID string,
	req models.PermissionRequest) (*permissionTargets, error) {

	var targets permissionTargets

	err := DB.QueryRow(`
		SELECT id FROM people
		 WHERE company_id = $1 AND external_id = $2 AND deleted_at IS NULL`,
		companyID, externalID).Scan(&targets.PersonID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, models.ErrPersonNotFound
	}
	if err != nil {
		return nil, err
	}

	if req.SiteID != "" {
		if !looksLikeUUID(req.SiteID) {
			return nil, models.ErrSiteNotFound
		}
		var id int64
		err := DB.QueryRow(`
			SELECT id FROM sites
			 WHERE company_id = $1 AND public_id = $2::uuid AND deleted_at IS NULL`,
			companyID, req.SiteID).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, models.ErrSiteNotFound
		}
		if err != nil {
			return nil, err
		}
		targets.SiteID = sql.NullInt64{Int64: id, Valid: true}
	}

	if req.DeviceSerial != "" {
		id, err := resolveTerminal(DB, companyID, req.DeviceSerial)
		if err != nil {
			return nil, err
		}
		targets.DeviceID = sql.NullInt64{Int64: id, Valid: true}
	}

	if req.ScheduleID != "" {
		if !looksLikeUUID(req.ScheduleID) {
			return nil, models.ErrScheduleNotFound
		}
		var id int64
		err := DB.QueryRow(`
			SELECT id FROM schedules
			 WHERE company_id = $1 AND public_id = $2::uuid AND deleted_at IS NULL`,
			companyID, req.ScheduleID).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, models.ErrScheduleNotFound
		}
		if err != nil {
			return nil, err
		}
		targets.ScheduleID = sql.NullInt64{Int64: id, Valid: true}
	}

	return &targets, nil
}

// GrantPermission writes one rule, or updates the one that already says the same
// thing at the same scope.
//
// UPSERT RATHER THAN INSERT, matching the partial unique indexes 014 created:
// one rule per (person, scope, application, effect). A second identical grant is
// the caller restating an intention, not a conflict to reject -- and rejecting
// it would make an idempotent console retry into an error.
func GrantPermission(companyID int64, externalID string,
	req models.PermissionRequest) (*models.Permission, error) {

	if err := req.Validate(); err != nil {
		return nil, err
	}

	targets, err := resolvePermissionTargets(companyID, externalID, req)
	if err != nil {
		return nil, err
	}

	effect := req.Effect
	if effect == "" {
		effect = models.EffectAllow
	}
	active := true
	if req.Active != nil {
		active = *req.Active
	}

	// The conflict target has to match the partial index for the scope, and the
	// three indexes differ, so the statement does too. One query with a
	// COALESCE'd target would not match any of them.
	var conflict string
	switch req.ScopeType {
	case models.ScopeCompany:
		conflict = `(person_id, effect, COALESCE(application, '')) WHERE scope_type = 'COMPANY' AND deleted_at IS NULL`
	case models.ScopeSite:
		conflict = `(person_id, site_id, effect, COALESCE(application, '')) WHERE scope_type = 'SITE' AND deleted_at IS NULL`
	default:
		conflict = `(person_id, device_id, effect, COALESCE(application, '')) WHERE scope_type = 'TERMINAL' AND deleted_at IS NULL`
	}

	var publicID string
	err = DB.QueryRow(`
		INSERT INTO permissions
		    (company_id, person_id, scope_type, site_id, device_id,
		     application, effect, schedule_id, starts_at, ends_at, active)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), $7, $8, $9, $10, $11)
		ON CONFLICT `+conflict+` DO UPDATE
		   SET schedule_id = EXCLUDED.schedule_id,
		       starts_at   = EXCLUDED.starts_at,
		       ends_at     = EXCLUDED.ends_at,
		       active      = EXCLUDED.active,
		       updated_at  = CURRENT_TIMESTAMP
		RETURNING public_id`,
		companyID, targets.PersonID, req.ScopeType, targets.SiteID, targets.DeviceID,
		strings.ToUpper(strings.TrimSpace(req.Application)), effect,
		targets.ScheduleID, req.StartsAt, req.EndsAt, active).Scan(&publicID)
	if err != nil {
		return nil, err
	}

	return GetPermission(companyID, publicID)
}

// GetPermission reads one rule by public id, inside a company.
func GetPermission(companyID int64, publicID string) (*models.Permission, error) {
	if !looksLikeUUID(publicID) {
		return nil, models.ErrPermissionNotFound
	}

	var (
		p            models.Permission
		siteID       sql.NullString
		siteName     sql.NullString
		serial       sql.NullString
		deviceName   sql.NullString
		application  sql.NullString
		scheduleID   sql.NullString
		scheduleName sql.NullString
		startsAt     sql.NullTime
		endsAt       sql.NullTime
	)
	err := DB.QueryRow(`
		SELECT pm.public_id, p.public_id, pm.scope_type,
		       s.public_id, s.site_name, d.serial_number, d.device_name,
		       pm.application, pm.effect, sc.public_id, sc.name,
		       pm.starts_at, pm.ends_at, pm.active, pm.created_at, pm.updated_at
		  FROM permissions pm
		  JOIN people p ON p.id = pm.person_id
		  LEFT JOIN sites s ON s.id = pm.site_id
		  LEFT JOIN devices d ON d.id = pm.device_id
		  LEFT JOIN schedules sc ON sc.id = pm.schedule_id
		 WHERE pm.company_id = $1 AND pm.public_id = $2::uuid AND pm.deleted_at IS NULL`,
		companyID, publicID).
		Scan(&p.ID, &p.PersonID, &p.ScopeType, &siteID, &siteName, &serial, &deviceName,
			&application, &p.Effect, &scheduleID, &scheduleName,
			&startsAt, &endsAt, &p.Active, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, models.ErrPermissionNotFound
	}
	if err != nil {
		return nil, err
	}

	p.SiteID = siteID.String
	p.SiteName = siteName.String
	p.DeviceSerial = serial.String
	p.DeviceName = deviceName.String
	p.Application = application.String
	p.ScheduleID = scheduleID.String
	p.ScheduleName = scheduleName.String
	if startsAt.Valid {
		t := startsAt.Time
		p.StartsAt = &t
	}
	if endsAt.Valid {
		t := endsAt.Time
		p.EndsAt = &t
	}
	return &p, nil
}

// RevokePermission soft-deletes one rule.
//
// SOFT, matching every other entity here, so the audit trail's target still
// resolves to something readable after the rule is gone. The partial unique
// indexes all exclude deleted rows, so the same grant can be written again.
func RevokePermission(companyID int64, publicID string) error {
	if !looksLikeUUID(publicID) {
		return models.ErrPermissionNotFound
	}

	result, err := DB.Exec(`
		UPDATE permissions
		   SET deleted_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		 WHERE company_id = $1 AND public_id = $2::uuid AND deleted_at IS NULL`,
		companyID, publicID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return models.ErrPermissionNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// Schedules
// ---------------------------------------------------------------------------

// ListSchedules returns a company's schedules with their windows.
func ListSchedules(companyID int64) ([]models.Schedule, error) {
	rows, err := DB.Query(`
		SELECT sc.public_id, sc.name, sc.description, sc.timezone, sc.active,
		       sc.created_at, sc.updated_at,
		       (SELECT count(*) FROM permissions pm
		         WHERE pm.schedule_id = sc.id AND pm.deleted_at IS NULL),
		       sw.days_of_week, sw.start_time::text, sw.end_time::text
		  FROM schedules sc
		  LEFT JOIN schedule_windows sw ON sw.schedule_id = sc.id
		 WHERE sc.company_id = $1 AND sc.deleted_at IS NULL
		 ORDER BY sc.name, sw.start_time`, companyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// One row per (schedule, window); gathered back here rather than with a
	// query per schedule.
	var ordered []string
	byID := make(map[string]*models.Schedule)

	for rows.Next() {
		var (
			publicID    string
			name        string
			description sql.NullString
			timezone    sql.NullString
			active      bool
			s           models.Schedule
			permCount   int
			days        sql.NullInt64
			startTime   sql.NullString
			endTime     sql.NullString
		)
		if err := rows.Scan(&publicID, &name, &description, &timezone, &active,
			&s.CreatedAt, &s.UpdatedAt, &permCount,
			&days, &startTime, &endTime); err != nil {
			return nil, err
		}

		schedule, seen := byID[publicID]
		if !seen {
			schedule = &models.Schedule{
				ID:              publicID,
				Name:            name,
				Description:     description.String,
				Timezone:        timezone.String,
				Active:          active,
				PermissionCount: permCount,
				CreatedAt:       s.CreatedAt,
				UpdatedAt:       s.UpdatedAt,
				Windows:         []models.ScheduleWindow{},
			}
			byID[publicID] = schedule
			ordered = append(ordered, publicID)
		}

		if days.Valid && startTime.Valid && endTime.Valid {
			schedule.Windows = append(schedule.Windows, models.ScheduleWindow{
				DaysOfWeek: int(days.Int64),
				StartTime:  startTime.String,
				EndTime:    endTime.String,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	schedules := make([]models.Schedule, 0, len(ordered))
	for _, id := range ordered {
		schedules = append(schedules, *byID[id])
	}
	return schedules, nil
}

// validateWindows checks a window set before it reaches the database.
func validateWindows(windows []models.ScheduleWindow) error {
	if len(windows) == 0 {
		return models.ErrScheduleNoWindows
	}
	for _, w := range windows {
		if w.DaysOfWeek < 1 || w.DaysOfWeek > models.DayEveryDay {
			return models.ErrInvalidWindow
		}
		start, err := parseWindowTime(w.StartTime)
		if err != nil {
			return models.ErrInvalidWindow
		}
		end, err := parseWindowTime(w.EndTime)
		if err != nil {
			return models.ErrInvalidWindow
		}
		// Equal times are a zero-length window that admits nobody, which is
		// never what somebody meant to configure. The schema refuses it too;
		// refusing here makes it a 400 with a message rather than a 500.
		if start == end {
			return models.ErrInvalidWindow
		}
	}
	return nil
}

// CreateSchedule writes a named schedule and its windows in one transaction.
func CreateSchedule(companyID int64, req models.ScheduleRequest) (*models.Schedule, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, models.ErrInvalidWindow
	}
	if err := validateWindows(req.Windows); err != nil {
		return nil, err
	}

	tx, err := DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var (
		scheduleID int64
		publicID   string
	)
	err = tx.QueryRow(`
		INSERT INTO schedules (company_id, name, description, timezone, active)
		VALUES ($1, $2, $3, NULLIF($4, ''), COALESCE($5, TRUE))
		RETURNING id, public_id`,
		companyID, name, stringOrNil(req.Description), stringValue(req.Timezone),
		req.Active).Scan(&scheduleID, &publicID)
	if err != nil {
		if IsUniqueViolation(err) {
			return nil, models.ErrScheduleNameTaken
		}
		return nil, err
	}

	if err := replaceScheduleWindows(tx, scheduleID, req.Windows); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return GetSchedule(companyID, publicID)
}

// UpdateSchedule replaces a schedule's metadata and its whole window set.
//
// WINDOWS ARE REPLACED, NOT MERGED. A schedule is a set of windows and the
// caller sends the set it wants; merging would leave a removed window in place
// with no way to express its removal.
func UpdateSchedule(companyID int64, publicID string,
	req models.ScheduleRequest) (*models.Schedule, error) {

	if !looksLikeUUID(publicID) {
		return nil, models.ErrScheduleNotFound
	}
	if req.Windows != nil {
		if err := validateWindows(req.Windows); err != nil {
			return nil, err
		}
	}

	tx, err := DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var scheduleID int64
	err = tx.QueryRow(`
		UPDATE schedules
		   SET name        = COALESCE(NULLIF($3, ''), name),
		       description = COALESCE($4, description),
		       timezone    = COALESCE(NULLIF($5, ''), timezone),
		       active      = COALESCE($6, active),
		       updated_at  = CURRENT_TIMESTAMP
		 WHERE company_id = $1 AND public_id = $2::uuid AND deleted_at IS NULL
		RETURNING id`,
		companyID, publicID, strings.TrimSpace(req.Name),
		stringOrNil(req.Description), stringValue(req.Timezone), req.Active).Scan(&scheduleID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, models.ErrScheduleNotFound
	}
	if err != nil {
		if IsUniqueViolation(err) {
			return nil, models.ErrScheduleNameTaken
		}
		return nil, err
	}

	if req.Windows != nil {
		if err := replaceScheduleWindows(tx, scheduleID, req.Windows); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return GetSchedule(companyID, publicID)
}

// replaceScheduleWindows swaps a schedule's whole window set inside a tx.
func replaceScheduleWindows(tx *sql.Tx, scheduleID int64, windows []models.ScheduleWindow) error {
	if _, err := tx.Exec(`DELETE FROM schedule_windows WHERE schedule_id = $1`, scheduleID); err != nil {
		return err
	}
	for _, w := range windows {
		if _, err := tx.Exec(`
			INSERT INTO schedule_windows (schedule_id, days_of_week, start_time, end_time)
			VALUES ($1, $2, $3::time, $4::time)`,
			scheduleID, w.DaysOfWeek, w.StartTime, w.EndTime); err != nil {
			return err
		}
	}
	return nil
}

// GetSchedule reads one schedule and its windows.
func GetSchedule(companyID int64, publicID string) (*models.Schedule, error) {
	if !looksLikeUUID(publicID) {
		return nil, models.ErrScheduleNotFound
	}

	schedules, err := ListSchedules(companyID)
	if err != nil {
		return nil, err
	}
	for i := range schedules {
		if schedules[i].ID == publicID {
			return &schedules[i], nil
		}
	}
	return nil, models.ErrScheduleNotFound
}

// DeleteSchedule soft-deletes a schedule.
//
// REFUSED WHILE PERMISSIONS REFERENCE IT. The foreign key is ON DELETE SET NULL,
// which for a soft delete would silently WIDEN every rule that used it -- a
// permission restricted to office hours would become a permission with no time
// restriction at all. Refusing, and telling the caller how many rules are in the
// way, is the only answer that cannot quietly grant access.
func DeleteSchedule(companyID int64, publicID string) error {
	if !looksLikeUUID(publicID) {
		return models.ErrScheduleNotFound
	}

	var scheduleID int64
	err := DB.QueryRow(`
		SELECT id FROM schedules
		 WHERE company_id = $1 AND public_id = $2::uuid AND deleted_at IS NULL`,
		companyID, publicID).Scan(&scheduleID)
	if errors.Is(err, sql.ErrNoRows) {
		return models.ErrScheduleNotFound
	}
	if err != nil {
		return err
	}

	var inUse int
	if err := DB.QueryRow(`
		SELECT count(*) FROM permissions
		 WHERE schedule_id = $1 AND deleted_at IS NULL`, scheduleID).Scan(&inUse); err != nil {
		return err
	}
	if inUse > 0 {
		return models.ErrScheduleInUse
	}

	_, err = DB.Exec(`
		UPDATE schedules
		   SET deleted_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		 WHERE id = $1`, scheduleID)
	return err
}

// stringOrNil maps an unset optional string to a SQL NULL, so COALESCE keeps the
// stored value rather than overwriting it with an empty one.
func stringOrNil(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

// stringValue dereferences an optional string to "" when unset.
func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
