package database

import (
	"database/sql"
	"encoding/json"
	"errors"

	"access-terminal-cloud-api/models"
)

// Application modules (migrations/009_applications.sql).
//
// Which capabilities a company has enabled, and which one a terminal is
// assigned to. Everything here is company-scoped like the rest of the data
// layer: a company configures its own modules and nobody else's.
//
// Nothing in this file is read by any device-facing endpoint. Terminals are not
// told about application modes yet -- delivering one to firmware is an additive
// change to the settings payload that belongs with the module that first needs
// it, not here.

// ListCompanyApplications returns every configured capability for a company,
// enabled or not, in a stable order.
//
// Capabilities the company has never configured are absent rather than listed
// as disabled. "Never configured" and "switched off" are different answers, and
// flattening them would lose the settings a company had chosen before it turned
// a module off.
func ListCompanyApplications(companyID int64) ([]models.CompanyApplication, error) {
	rows, err := DB.Query(`
		SELECT public_id, application, enabled, settings, created_at, updated_at
		  FROM company_applications
		 WHERE company_id = $1
		 ORDER BY application`, companyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var applications []models.CompanyApplication
	for rows.Next() {
		var app models.CompanyApplication
		var settings []byte
		if err := rows.Scan(&app.PublicID, &app.Code, &app.Enabled, &settings,
			&app.CreatedAt, &app.UpdatedAt); err != nil {
			return nil, err
		}
		app.Settings = json.RawMessage(settings)
		applications = append(applications, app)
	}
	return applications, rows.Err()
}

// EnabledApplications returns the capabilities a company currently has on.
//
// An EMPTY result is a legitimate answer and the state every company is in until
// somebody enables something. A caller must render that rather than fall back to
// a default set -- assuming a default is the exact thing this model exists to
// prevent.
//
// Ordered by models.ApplicationOrder so a dashboard's navigation does not
// reshuffle between requests.
func EnabledApplications(companyID int64) ([]models.EnabledApplication, error) {
	rows, err := DB.Query(`
		SELECT application, settings
		  FROM company_applications
		 WHERE company_id = $1 AND enabled`, companyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	found := map[string]json.RawMessage{}
	for rows.Next() {
		var code string
		var settings []byte
		if err := rows.Scan(&code, &settings); err != nil {
			return nil, err
		}
		found[code] = json.RawMessage(settings)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	enabled := make([]models.EnabledApplication, 0, len(found))
	for _, code := range models.ApplicationOrder {
		if settings, ok := found[code]; ok {
			enabled = append(enabled, models.EnabledApplication{Code: code, Settings: settings})
		}
	}
	return enabled, nil
}

// SetCompanyApplication turns a capability on or off for a company and records
// its configuration.
//
// An upsert: enabling something already enabled is not an error, which makes the
// console's toggle idempotent. Settings are replaced wholesale when supplied and
// left alone when nil, so a caller flipping `enabled` does not have to resend a
// configuration it did not intend to change.
//
// Disabling PRESERVES the row and its settings. Turning a module back on then
// restores what the company had configured, instead of silently starting over.
func SetCompanyApplication(companyID int64, application string, enabled bool,
	settings json.RawMessage) (*models.CompanyApplication, error) {

	if !models.IsApplication(application) {
		return nil, models.ErrInvalidApplication
	}

	// The column is constrained to a JSON object; reject anything else here so
	// the caller gets a clear message rather than a constraint violation.
	if settings != nil {
		var probe map[string]any
		if err := json.Unmarshal(settings, &probe); err != nil {
			return nil, errors.New("application settings must be a JSON object")
		}
	}

	var app models.CompanyApplication
	var stored []byte
	err := DB.QueryRow(`
		INSERT INTO company_applications (company_id, application, enabled, settings)
		VALUES ($1, $2, $3, COALESCE($4::jsonb, '{}'::jsonb))
		ON CONFLICT (company_id, application)
		DO UPDATE SET enabled  = EXCLUDED.enabled,
		              settings = COALESCE($4::jsonb, company_applications.settings)
		RETURNING public_id, application, enabled, settings, created_at, updated_at`,
		companyID, application, enabled, []byte(settings)).
		Scan(&app.PublicID, &app.Code, &app.Enabled, &stored, &app.CreatedAt, &app.UpdatedAt)
	if err != nil {
		return nil, err
	}

	app.Settings = json.RawMessage(stored)
	return &app, nil
}

// SetDeviceApplicationMode assigns what a terminal is for.
//
// The mode must be a capability the company has ENABLED, or MULTI_PURPOSE.
// Pointing a terminal at a module the company does not have would be a
// configuration mistake nobody discovers until somebody presents a finger at the
// door, so it is refused at the point of assignment instead.
//
// Company-scoped by join, not by trusting the caller: a device in another tenant
// is reported as not found, the same as every other cross-tenant read.
func SetDeviceApplicationMode(companyID int64, serialNumber, mode string) error {
	if !models.IsDeviceApplicationMode(mode) {
		return models.ErrInvalidApplication
	}

	if mode != models.AppMultiPurpose {
		var enabled bool
		err := DB.QueryRow(`
			SELECT EXISTS (SELECT 1 FROM company_applications
			                WHERE company_id = $1 AND application = $2 AND enabled)`,
			companyID, mode).Scan(&enabled)
		if err != nil {
			return err
		}
		if !enabled {
			return models.ErrApplicationNotEnabled
		}
	}

	result, err := DB.Exec(`
		UPDATE devices d
		   SET application_mode = $1
		  FROM sites s
		 WHERE s.id = d.site_id
		   AND s.company_id = $2
		   AND d.serial_number = $3
		   AND d.deleted_at IS NULL
		   AND s.deleted_at IS NULL`,
		mode, companyID, serialNumber)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return models.ErrDeviceNotFound
	}
	return nil
}

// GetDeviceApplication reports what a terminal is assigned to and what that
// currently resolves to.
//
// MULTI_PURPOSE resolves to every capability the company has enabled -- which is
// an empty list for a company that has enabled none, and that is the honest
// answer rather than a failure.
//
// A specific mode resolves to itself ONLY while the company still has that
// capability enabled. If the company later turns it off, the assignment is
// retained and Effective goes empty: recoverable and visible, rather than the
// database silently rewriting a terminal's configuration.
func GetDeviceApplication(companyID int64, serialNumber string) (*models.DeviceApplication, error) {
	var device models.DeviceApplication
	err := DB.QueryRow(`
		SELECT d.serial_number, d.application_mode
		  FROM devices d
		  JOIN sites s ON s.id = d.site_id
		 WHERE s.company_id = $1
		   AND d.serial_number = $2
		   AND d.deleted_at IS NULL
		   AND s.deleted_at IS NULL`,
		companyID, serialNumber).Scan(&device.SerialNumber, &device.Mode)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, models.ErrDeviceNotFound
	}
	if err != nil {
		return nil, err
	}

	enabled, err := EnabledApplications(companyID)
	if err != nil {
		return nil, err
	}

	device.Effective = []string{}
	for _, app := range enabled {
		if device.Mode == models.AppMultiPurpose || device.Mode == app.Code {
			device.Effective = append(device.Effective, app.Code)
		}
	}
	return &device, nil
}
