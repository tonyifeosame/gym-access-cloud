package database

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"access-terminal-cloud-api/models"
)

// The settings object a terminal actually receives.
//
// THE FINDING (firmware-protocol-requirements.md §1, blocking). `sites` has
// carried `offline_policy` and `offline_grace_minutes` since
// 016_terminal_lifecycle.sql, and 016 argued -- correctly -- that what a
// terminal does during an outage is a safety decision with no default the
// platform can pick. But nothing ever put either column into the `settings`
// object a device is sent, so the decision was made in a browser and stopped
// there.
//
// The consequence is the one that matters: EVERY TERMINAL IN THE FIELD BEHAVES
// AS CACHED_INDEFINITE regardless of what the site chose. A site that selected
// DENY_ALL got a door that kept admitting whoever it last heard about, for ever,
// while the console reported a policy it was not enforcing. That is worse than
// not offering the setting at all, because somebody made a safety decision and
// was told it had taken effect.
//
// ---------------------------------------------------------------------------
// WHY THE COLUMNS WIN OVER THE JSON
// ---------------------------------------------------------------------------
//
// `sites.settings` is an open JSONB object an operator can write anything into
// (GP-04 is the finding that says it should not be). `offline_policy` is a
// validated column with a CHECK constraint behind it.
//
// So the column is layered OVER the stored object rather than under it. If the
// two disagree -- because somebody wrote `offline_policy` into the free-form
// blob -- the column wins. The alternative would let an operator bypass the
// validated setting by writing raw JSON, which is precisely how a DENY_ALL site
// would end up running something else.
//
// ---------------------------------------------------------------------------
// COMPATIBILITY
// ---------------------------------------------------------------------------
//
// Purely additive: two new keys inside an object that already exists. Firmware
// that does not know them ignores them, and firmware that does treats absent
// keys as "leave the stored policy alone" -- so old servers and new terminals,
// and new servers and old terminals, both behave exactly as they did.
// SyncProtocolVersion is unchanged, as the requirements document says it should
// be.

// DeviceSettings is a site's configuration as a terminal receives it, with the
// version that gates it.
type DeviceSettings struct {
	Version int
	// Settings is the merged object: the site's stored JSON with the validated
	// policy columns layered over it.
	Settings json.RawMessage
}

// deviceSettingsRow is what the site actually holds.
type deviceSettingsRow struct {
	Version      int
	Settings     []byte
	Policy       string
	GraceMinutes int
}

// loadDeviceSettingsRow reads one site's settings and policy columns.
func loadDeviceSettingsRow(q rowQuerier, siteID int64) (*deviceSettingsRow, error) {
	var row deviceSettingsRow
	err := q.QueryRow(`
		SELECT settings, settings_version, offline_policy, offline_grace_minutes
		  FROM sites
		 WHERE id = $1 AND deleted_at IS NULL`, siteID).
		Scan(&row.Settings, &row.Version, &row.Policy, &row.GraceMinutes)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// mergeDeviceSettings layers the validated policy columns over the stored blob.
//
// Returns the object a terminal is sent. A stored blob that is absent, null, or
// not a JSON object is treated as empty rather than refused: the policy keys are
// a safety control and must reach the terminal even when the free-form settings
// are unusable. Refusing here would mean one malformed operator edit silently
// stopped the offline policy being delivered, which is the failure this whole
// file exists to prevent.
func mergeDeviceSettings(row *deviceSettingsRow) (json.RawMessage, error) {
	merged := map[string]any{}

	if len(row.Settings) > 0 {
		if err := json.Unmarshal(row.Settings, &merged); err != nil || merged == nil {
			merged = map[string]any{}
		}
	}

	// Layered OVER, deliberately. See the note at the top of the file.
	merged[models.SettingsKeyOfflinePolicy] = row.Policy
	merged[models.SettingsKeyOfflineGraceMinutes] = row.GraceMinutes

	encoded, err := json.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("encoding device settings: %w", err)
	}
	return json.RawMessage(encoded), nil
}

// GetDeviceSettings returns the settings a terminal at this site should hold.
//
// Used by GET /api/v1/devices/settings and by every SETTINGS job payload, so the
// pull and the push cannot describe different configurations -- which is the
// bug that would otherwise appear only on a terminal that had missed a job.
func GetDeviceSettings(siteID int64) (*DeviceSettings, error) {
	row, err := loadDeviceSettingsRow(DB, siteID)
	if err != nil {
		return nil, err
	}

	merged, err := mergeDeviceSettings(row)
	if err != nil {
		return nil, err
	}

	return &DeviceSettings{Version: row.Version, Settings: merged}, nil
}

// deviceSettingsPayloadTx builds the SETTINGS job payload inside a transaction.
func deviceSettingsPayloadTx(tx *sql.Tx, siteID int64) ([]byte, int, error) {
	row, err := loadDeviceSettingsRow(tx, siteID)
	if err != nil {
		return nil, 0, err
	}

	merged, err := mergeDeviceSettings(row)
	if err != nil {
		return nil, 0, err
	}

	payload, err := json.Marshal(models.SettingsSyncPayload{
		SettingsVersion: row.Version,
		Settings:        merged,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("building settings payload: %w", err)
	}
	return payload, row.Version, nil
}

// ---------------------------------------------------------------------------
// Changing the policy
// ---------------------------------------------------------------------------

// OfflinePolicyUpdate is a change to a site's outage behaviour.
type OfflinePolicyUpdate struct {
	Policy       *string
	GraceMinutes *int
}

// Empty reports whether the update asks for nothing.
func (u OfflinePolicyUpdate) Empty() bool {
	return u.Policy == nil && u.GraceMinutes == nil
}

// Validate checks the values against what the schema and the firmware accept.
func (u OfflinePolicyUpdate) Validate() error {
	if u.Policy != nil && !models.IsOfflinePolicy(*u.Policy) {
		return models.ErrInvalidOfflinePolicy
	}
	if u.GraceMinutes != nil {
		// The same bound the firmware enforces (offlineGraceMinutesAreValid)
		// and the same one 016's CHECK constraint carries. Validated here so a
		// bad value is a 400 naming the field rather than a constraint
		// violation the handler cannot tell from an outage.
		if *u.GraceMinutes < 0 || *u.GraceMinutes > models.MaxOfflineGraceMinutes {
			return models.ErrInvalidOfflineGrace
		}
	}
	return nil
}

// SetSiteOfflinePolicy changes a site's outage behaviour and pushes it out.
//
// THE VERSION BUMP AND THE FAN-OUT ARE THE POINT, and the requirements document
// asks for them by name. The terminal gates every settings push behind a
// STRICTLY GREATER version check -- an equal version is a redelivery of what it
// already holds and is discarded as a replay. So a policy change written without
// incrementing settings_version is not a slow change; it is a silent no-op that
// the console reports as applied.
//
// One transaction: the column, the version and the jobs commit together or not
// at all. A committed policy with no queued jobs would be a site whose terminals
// never hear about it until something unrelated bumps the version.
func SetSiteOfflinePolicy(companyID, siteID int64, in OfflinePolicyUpdate) (*DeviceSettings, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}

	tx, err := DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// COALESCE so an update naming only the policy cannot blank a grace window
	// the caller never mentioned.
	var changed bool
	err = tx.QueryRow(`
		UPDATE sites
		   SET offline_policy        = COALESCE($3, offline_policy),
		       offline_grace_minutes = COALESCE($4, offline_grace_minutes),
		       settings_version      = settings_version + 1,
		       settings_updated_at   = CURRENT_TIMESTAMP,
		       updated_at            = CURRENT_TIMESTAMP
		 WHERE id = $1 AND company_id = $2 AND deleted_at IS NULL
		RETURNING TRUE`,
		siteID, companyID, in.Policy, in.GraceMinutes).Scan(&changed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, models.ErrSiteNotFound
	}
	if err != nil {
		return nil, err
	}

	payload, version, err := deviceSettingsPayloadTx(tx, siteID)
	if err != nil {
		return nil, err
	}

	if err := enqueueSettingsFanoutTx(tx, siteID, payload); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	settings, err := GetDeviceSettings(siteID)
	if err != nil {
		return nil, err
	}
	settings.Version = version
	return settings, nil
}
