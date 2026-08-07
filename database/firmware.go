package database

import (
	"database/sql"

	"access-terminal-cloud-api/models"
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
	d.id, d.public_id, d.site_id, s.site_name, d.serial_number, d.device_name,
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

const deviceInventoryFrom = `
	FROM devices d
	JOIN sites s ON s.id = d.site_id
	LEFT JOIN firmware_versions fv
	       ON fv.device_type = d.device_type
	      AND fv.release_channel = d.release_channel
	      AND fv.is_current
	      AND fv.deleted_at IS NULL
	WHERE s.company_id = $1
	  AND d.deleted_at IS NULL
	  AND s.deleted_at IS NULL`

func scanDeviceInventory(rows *sql.Rows) ([]models.DeviceInventory, error) {
	defer rows.Close()

	var devices []models.DeviceInventory
	for rows.Next() {
		var d models.DeviceInventory
		err := rows.Scan(
			&d.ID, &d.PublicID, &d.SiteID, &d.SiteName, &d.SerialNumber, &d.DeviceName,
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

// ListDevices returns the company's device inventory. When outdatedOnly is set,
// only devices not running the current build for their channel are returned.
func ListDevices(companyID int64, outdatedOnly bool) ([]models.DeviceInventory, error) {
	query := `SELECT ` + deviceInventoryColumns + deviceInventoryFrom
	if outdatedOnly {
		query += `
	  AND fv.version IS NOT NULL
	  AND (d.firmware_version IS NULL OR d.firmware_version <> fv.version)`
	}
	query += ` ORDER BY s.site_name, d.serial_number`

	rows, err := DB.Query(query, companyID)
	if err != nil {
		return nil, err
	}
	return scanDeviceInventory(rows)
}

// GetFleetSummary reports device counts by state and how many are behind on
// firmware -- the numbers a dashboard header shows.
func GetFleetSummary(companyID int64) (*models.FleetSummary, error) {
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
		         ON fv.device_type = d.device_type
		        AND fv.release_channel = d.release_channel
		        AND fv.is_current AND fv.deleted_at IS NULL
		 WHERE s.company_id = $1 AND d.deleted_at IS NULL AND s.deleted_at IS NULL`,
		companyID).Scan(&s.Total, &s.Online, &s.Offline, &s.Updating, &s.Error,
		&s.Disabled, &s.Provisioning, &s.FirmwareOutdated)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// Firmware catalog

// ListFirmwareVersions returns the firmware catalog, newest first
func ListFirmwareVersions() ([]models.FirmwareVersion, error) {
	rows, err := DB.Query(`
		SELECT id, public_id, version, device_type, release_channel,
		       COALESCE(download_url, ''), COALESCE(TRIM(checksum_sha256), ''), size_bytes,
		       COALESCE(release_notes, ''), is_mandatory, is_current, published_at, created_at
		  FROM firmware_versions
		 WHERE deleted_at IS NULL
		 ORDER BY device_type, release_channel, published_at DESC NULLS LAST, id DESC`)
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

// CreateFirmwareVersion adds a build to the catalog. It does not become the
// deployment target until it is explicitly marked current.
func CreateFirmwareVersion(req models.CreateFirmwareRequest) (*models.FirmwareVersion, error) {
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
		    (version, device_type, release_channel, download_url, checksum_sha256,
		     size_bytes, release_notes, is_mandatory, published_at)
		VALUES ($1, $2, $3, NULLIF($4,''), NULLIF($5,''), $6, NULLIF($7,''), $8, CURRENT_TIMESTAMP)
		RETURNING id, public_id, version, device_type, release_channel,
		          COALESCE(download_url, ''), COALESCE(TRIM(checksum_sha256), ''), size_bytes,
		          COALESCE(release_notes, ''), is_mandatory, is_current, published_at, created_at`,
		req.Version, deviceType, channel, req.DownloadURL, req.ChecksumSHA256,
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
// release channel, demoting whatever held that slot.
//
// This only changes what "outdated" means. It does not push anything to any
// device -- that is OTA, and is not implemented.
func SetCurrentFirmware(firmwareID int64) (*models.FirmwareVersion, error) {
	tx, err := DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var deviceType, channel string
	err = tx.QueryRow(
		`SELECT device_type, release_channel FROM firmware_versions
		  WHERE id = $1 AND deleted_at IS NULL`, firmwareID).Scan(&deviceType, &channel)
	if err != nil {
		return nil, err
	}

	// Demote first: the partial unique index permits only one current build per
	// (device_type, release_channel).
	if _, err = tx.Exec(
		`UPDATE firmware_versions SET is_current = FALSE
		  WHERE device_type = $1 AND release_channel = $2 AND is_current AND deleted_at IS NULL`,
		deviceType, channel); err != nil {
		return nil, err
	}

	var f models.FirmwareVersion
	err = tx.QueryRow(`
		UPDATE firmware_versions SET is_current = TRUE WHERE id = $1
		RETURNING id, public_id, version, device_type, release_channel,
		          COALESCE(download_url, ''), COALESCE(TRIM(checksum_sha256), ''), size_bytes,
		          COALESCE(release_notes, ''), is_mandatory, is_current, published_at, created_at`,
		firmwareID).
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
