package database

import (
	"database/sql"

	"access-terminal-cloud-api/models"

	"github.com/lib/pq"
)

// Firmware catalog and device inventory (migrations/006_device_firmware_state.sql).
//
// This is inventory and reporting only. Nothing here downloads, schedules, or
// applies firmware -- OTA is a later sprint.
//
// "Outdated" is measured against the build explicitly marked current for a
// device's type and release channel, not against the newest published build.
// The newest build is not necessarily the one a fleet is supposed to be running.

// deviceInventoryColumns is the shared projection for device inventory reads,
// including the outdated determination.
const deviceInventoryColumns = `
	d.id, d.public_id, d.site_id, s.public_id AS site_public_id,
	s.site_name, d.serial_number, d.device_name,
	d.device_type, d.status, d.active, d.release_channel,
	COALESCE(d.firmware_version, ''), COALESCE(d.hardware_revision, ''),
	COALESCE(d.build_number, ''), d.boot_count,
	d.last_seen_at, d.last_sync_at, d.last_heartbeat_at,
	COALESCE(fv.version, '') AS current_firmware_version,
	CASE
	    WHEN fv.version IS NULL THEN FALSE
	    WHEN d.firmware_version IS NULL THEN TRUE
	    ELSE d.firmware_version <> fv.version
	END AS firmware_outdated`

// The firmware join carries company_id as well as type and channel: "current"
// is a per-tenant target, so a device must only ever be measured against its own
// company's build.
const deviceInventoryFrom = `
	FROM devices d
	JOIN sites s ON s.id = d.site_id
	LEFT JOIN firmware_versions fv
	       ON fv.company_id = s.company_id
	      AND fv.device_type = d.device_type
	      AND fv.release_channel = d.release_channel
	      AND fv.is_current
	      AND fv.deleted_at IS NULL
	WHERE s.company_id = $1
	  AND d.deleted_at IS NULL
	  AND s.deleted_at IS NULL`

// GetDeviceInventoryBySerial returns one terminal's inventory row.
//
// The SAME projection the list uses, filtered to one serial, so a detail view
// cannot drift from the list it was opened from -- and so the outdated
// determination is made the one way it is made everywhere else, against the
// company's own current build.
//
// Company-scoped through the join on sites, like every other console read: a
// terminal in another tenant is reported as not found.
func GetDeviceInventoryBySerial(companyID int64, serialNumber string) (*models.DeviceInventory, error) {
	rows, err := DB.Query(`
		SELECT `+deviceInventoryColumns+deviceInventoryFrom+`
		  AND d.serial_number = $2`,
		companyID, serialNumber)
	if err != nil {
		return nil, err
	}

	devices, err := scanDeviceInventory(rows)
	if err != nil {
		return nil, err
	}
	if len(devices) == 0 {
		return nil, models.ErrDeviceNotFound
	}
	return &devices[0], nil
}

func scanDeviceInventory(rows *sql.Rows) ([]models.DeviceInventory, error) {
	defer rows.Close()

	var devices []models.DeviceInventory
	for rows.Next() {
		var d models.DeviceInventory
		err := rows.Scan(
			&d.ID, &d.PublicID, &d.SiteID, &d.SitePublicID,
			&d.SiteName, &d.SerialNumber, &d.DeviceName,
			&d.DeviceType, &d.Status, &d.Active, &d.ReleaseChannel,
			&d.FirmwareVersion, &d.HardwareRevision, &d.BuildNumber, &d.BootCount,
			&d.LastSeenAt, &d.LastSyncAt, &d.LastHeartbeatAt,
			&d.CurrentFirmwareVersion, &d.FirmwareOutdated,
		)
		if err != nil {
			return nil, err
		}
		devices = append(devices, d)
	}
	return devices, rows.Err()
}

// siteScopeClause narrows a device read to a set of sites.
//
// nil means "every site in the company" -- what an unscoped operator, every
// ADMIN and OWNER, and every site-key caller gets. An EMPTY-but-non-nil slice
// means "no sites at all", and the two are deliberately not conflated: the
// caller has to ask for nothing on purpose. Same convention as
// ListConsoleSites, so a reader who has learned one has learned both.
//
// The scope is applied in SQL rather than by filtering the result in Go. A
// filter after the fact still reads the whole company's fleet out of the
// database before discarding most of it, and -- for the summary -- there is no
// row-by-row result to filter at all, because the counts are computed by the
// query.
func siteScopeClause(siteIDs []int64) (clause string, args []any) {
	if siteIDs == nil {
		return "", nil
	}
	return ` AND d.site_id = ANY($2)`, []any{pq.Array(siteIDs)}
}

// ListDevices returns the company's device inventory, optionally narrowed to a
// set of sites. When outdatedOnly is set, only devices not running the current
// build for their channel are returned.
func ListDevices(companyID int64, outdatedOnly bool, siteIDs []int64) ([]models.DeviceInventory, error) {
	scope, scopeArgs := siteScopeClause(siteIDs)

	query := `SELECT ` + deviceInventoryColumns + deviceInventoryFrom + scope
	if outdatedOnly {
		query += `
	  AND fv.version IS NOT NULL
	  AND (d.firmware_version IS NULL OR d.firmware_version <> fv.version)`
	}
	query += ` ORDER BY s.site_name, d.serial_number`

	rows, err := DB.Query(query, append([]any{companyID}, scopeArgs...)...)
	if err != nil {
		return nil, err
	}
	return scanDeviceInventory(rows)
}

// GetFleetSummary reports device counts by state and how many are behind on
// firmware -- the numbers a dashboard header shows -- optionally narrowed to a
// set of sites.
//
// The scope parameter exists because these counts are rendered ABOVE a terminal
// list that is itself narrowed by the caller's grants. A company-wide rollup
// over a scoped list reads as a bug to the operator looking at it ("12 online"
// above a list of one), and it discloses how much hardware exists at sites they
// were deliberately not given. Both callers therefore pass the same scope they
// pass to ListDevices.
func GetFleetSummary(companyID int64, siteIDs []int64) (*models.FleetSummary, error) {
	scope, scopeArgs := siteScopeClause(siteIDs)

	var s models.FleetSummary
	err := DB.QueryRow(`
		SELECT count(*),
		       count(*) FILTER (WHERE d.status = 'ONLINE'),
		       count(*) FILTER (WHERE d.status = 'OFFLINE'),
		       count(*) FILTER (WHERE d.status = 'UPDATING'),
		       count(*) FILTER (WHERE d.status = 'ERROR'),
		       count(*) FILTER (WHERE d.status = 'DISABLED'),
		       count(*) FILTER (WHERE d.status = 'PROVISIONING'),
		       count(*) FILTER (WHERE fv.version IS NOT NULL
		                          AND (d.firmware_version IS NULL OR d.firmware_version <> fv.version))
		  FROM devices d
		  JOIN sites s ON s.id = d.site_id
		  LEFT JOIN firmware_versions fv
		         ON fv.company_id = s.company_id
		        AND fv.device_type = d.device_type
		        AND fv.release_channel = d.release_channel
		        AND fv.is_current AND fv.deleted_at IS NULL
		 WHERE s.company_id = $1 AND d.deleted_at IS NULL AND s.deleted_at IS NULL`+scope,
		append([]any{companyID}, scopeArgs...)...).
		Scan(&s.Total, &s.Online, &s.Offline, &s.Updating, &s.Error,
			&s.Disabled, &s.Provisioning, &s.FirmwareOutdated)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// Firmware catalog

// ListFirmwareVersions returns the company's firmware catalog, newest first.
//
// Scoped by company like every other read: the catalog carries download URLs and
// checksums, which are not another tenant's business.
func ListFirmwareVersions(companyID int64) ([]models.FirmwareVersion, error) {
	rows, err := DB.Query(`
		SELECT id, public_id, version, device_type, release_channel,
		       COALESCE(download_url, ''), COALESCE(TRIM(checksum_sha256), ''), size_bytes,
		       COALESCE(release_notes, ''), is_mandatory, is_current, published_at, created_at
		  FROM firmware_versions
		 WHERE company_id = $1 AND deleted_at IS NULL
		 ORDER BY device_type, release_channel, published_at DESC NULLS LAST, id DESC`,
		companyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var versions []models.FirmwareVersion
	for rows.Next() {
		var f models.FirmwareVersion
		err := rows.Scan(&f.ID, &f.PublicID, &f.Version, &f.DeviceType, &f.ReleaseChannel,
			&f.DownloadURL, &f.ChecksumSHA256, &f.SizeBytes, &f.ReleaseNotes,
			&f.IsMandatory, &f.IsCurrent, &f.PublishedAt, &f.CreatedAt)
		if err != nil {
			return nil, err
		}
		versions = append(versions, f)
	}
	return versions, rows.Err()
}

// CreateFirmwareVersion adds a build to the company's catalog. It does not
// become the deployment target until it is explicitly marked current.
func CreateFirmwareVersion(companyID int64, req models.CreateFirmwareRequest) (*models.FirmwareVersion, error) {
	deviceType := req.DeviceType
	if deviceType == "" {
		deviceType = "TERMINAL"
	}
	channel := req.ReleaseChannel
	if channel == "" {
		channel = "STABLE"
	}

	var f models.FirmwareVersion
	err := DB.QueryRow(`
		INSERT INTO firmware_versions
		    (company_id, version, device_type, release_channel, download_url, checksum_sha256,
		     size_bytes, release_notes, is_mandatory, published_at)
		VALUES ($1, $2, $3, $4, NULLIF($5,''), NULLIF($6,''), $7, NULLIF($8,''), $9, CURRENT_TIMESTAMP)
		RETURNING id, public_id, version, device_type, release_channel,
		          COALESCE(download_url, ''), COALESCE(TRIM(checksum_sha256), ''), size_bytes,
		          COALESCE(release_notes, ''), is_mandatory, is_current, published_at, created_at`,
		companyID, req.Version, deviceType, channel, req.DownloadURL, req.ChecksumSHA256,
		req.SizeBytes, req.ReleaseNotes, req.IsMandatory).
		Scan(&f.ID, &f.PublicID, &f.Version, &f.DeviceType, &f.ReleaseChannel,
			&f.DownloadURL, &f.ChecksumSHA256, &f.SizeBytes, &f.ReleaseNotes,
			&f.IsMandatory, &f.IsCurrent, &f.PublishedAt, &f.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// SetCurrentFirmware makes a build the deployment target for its device type and
// release channel within the company, demoting whatever held that slot.
//
// This only changes what "outdated" means. It does not push anything to any
// device -- that is OTA, and is not implemented.
//
// The company filter on the initial lookup is what stops one tenant retargeting
// another's fleet: a firmware id belonging to a different company reads as
// sql.ErrNoRows and the caller answers 404, exactly as it would for an id that
// does not exist.
func SetCurrentFirmware(companyID, firmwareID int64) (*models.FirmwareVersion, error) {
	tx, err := DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var deviceType, channel string
	err = tx.QueryRow(
		`SELECT device_type, release_channel FROM firmware_versions
		  WHERE id = $1 AND company_id = $2 AND deleted_at IS NULL`,
		firmwareID, companyID).Scan(&deviceType, &channel)
	if err != nil {
		return nil, err
	}

	// Demote first: the partial unique index permits only one current build per
	// (company_id, device_type, release_channel).
	if _, err = tx.Exec(
		`UPDATE firmware_versions SET is_current = FALSE
		  WHERE company_id = $1 AND device_type = $2 AND release_channel = $3
		    AND is_current AND deleted_at IS NULL`,
		companyID, deviceType, channel); err != nil {
		return nil, err
	}

	var f models.FirmwareVersion
	err = tx.QueryRow(`
		UPDATE firmware_versions SET is_current = TRUE
		 WHERE id = $1 AND company_id = $2
		RETURNING id, public_id, version, device_type, release_channel,
		          COALESCE(download_url, ''), COALESCE(TRIM(checksum_sha256), ''), size_bytes,
		          COALESCE(release_notes, ''), is_mandatory, is_current, published_at, created_at`,
		firmwareID, companyID).
		Scan(&f.ID, &f.PublicID, &f.Version, &f.DeviceType, &f.ReleaseChannel,
			&f.DownloadURL, &f.ChecksumSHA256, &f.SizeBytes, &f.ReleaseNotes,
			&f.IsMandatory, &f.IsCurrent, &f.PublishedAt, &f.CreatedAt)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &f, nil
}
