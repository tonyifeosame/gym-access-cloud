package database

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"fmt"

	"access-terminal-cloud-api/models"
)

// Device identity and credentials (migrations/005_device_identity.sql).
//
// A device credential is generated server-side, returned to the caller exactly
// once at registration, and stored only as a SHA-256 hash. There is no way to
// recover a lost key -- the device re-registers and receives a new one.
//
// SHA-256 rather than bcrypt is deliberate here: the secret is 256 bits of
// output from crypto/rand, not a human-chosen password, so it is not subject to
// dictionary attack and does not need a slow KDF. What it does need is to be
// verifiable on every single device poll without burning CPU.

const deviceKeyPrefix = "atd_"

// execer is whatever can run a statement -- *sql.DB or *sql.Tx. It exists so a
// helper can be called either on its own or as part of a caller's transaction,
// without the helper having to know which.
type execer interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
}

// generateDeviceKey returns a new credential and its storage form
func generateDeviceKey() (key, hash, prefix string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", "", fmt.Errorf("generating device key: %w", err)
	}

	key = deviceKeyPrefix + hex.EncodeToString(raw)
	hash = hashDeviceKey(key)
	prefix = key[:12] // non-secret, for logs and admin display
	return key, hash, prefix, nil
}

// hashDeviceKey returns the stored form of a device credential
func hashDeviceKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// RegisterDevice provisions a device at a site and issues it a fresh credential.
//
// Registration is idempotent by serial number: registering an existing device
// rotates its credential rather than creating a duplicate. That is what lets a
// factory-reset terminal recover -- it re-registers and is issued a new key.
//
// The returned plaintext key is the only time it exists outside the device, and
// the bootstrap jobs are queued INSIDE this transaction. Both of those are the
// same requirement: the credential is committed the moment this function
// succeeds, and a caller that then fails has no way to tell the device what its
// key is. Seeding afterwards -- which is what this used to do -- meant a failure
// to queue the roster left a terminal with a rotated, committed, unrecoverable
// credential and a 500 in place of the response carrying it. The device was
// locked out until someone re-registered it by hand.
//
// Returns the device, its plaintext key, and how many bootstrap jobs were
// queued.
func RegisterDevice(siteID int64, req models.DeviceRegistrationRequest) (*models.Device, string, int, error) {
	tx, err := DB.Begin()
	if err != nil {
		return nil, "", 0, err
	}
	defer tx.Rollback()

	device, key, bootstrapped, err := registerDeviceTx(tx, siteID, req)
	if err != nil {
		return nil, "", 0, err
	}

	if err := tx.Commit(); err != nil {
		return nil, "", 0, err
	}
	return device, key, bootstrapped, nil
}

// registerDeviceTx is the whole of registration, on the caller's transaction.
//
// Factored out for the CLAIM path (database/claim.go), which has to mark a code
// redeemed in the same transaction that issues the credential -- a code marked
// used with no key issued strands the installer, and a key issued with the code
// still live leaves a reusable code behind.
//
// A claimed terminal therefore goes through EXACTLY this lifecycle: the same
// upsert, the same refusal to resurrect a DISABLED unit, the same site-mismatch
// check, the same bootstrap seeding. A second registration path that drifted
// from this one is how a claimed device would quietly become a device with
// different rules.
func registerDeviceTx(tx *sql.Tx, siteID int64,
	req models.DeviceRegistrationRequest) (*models.Device, string, int, error) {

	key, hash, prefix, err := generateDeviceKey()
	if err != nil {
		return nil, "", 0, err
	}

	deviceType := req.DeviceType
	if deviceType == "" {
		deviceType = "TERMINAL"
	}
	deviceName := req.DeviceName
	if deviceName == "" {
		deviceName = req.SerialNumber
	}

	// Re-registration must not resurrect a device that was deliberately
	// retired, so the conflict target excludes soft-deleted rows via the
	// partial unique index on serial_number.
	channel := req.ReleaseChannel
	if channel == "" {
		channel = "STABLE"
	}

	var device models.Device
	var wasRegistered sql.NullTime
	err = tx.QueryRow(`
		INSERT INTO devices (site_id, serial_number, device_name, device_type,
		                     status, api_key_hash, api_key_prefix, registered_at,
		                     firmware_version, hardware_revision, build_number,
		                     release_channel, ip_address, active)
		VALUES ($1, $2, $3, $4, 'ONLINE', $5, $6, CURRENT_TIMESTAMP,
		        NULLIF($7,''), NULLIF($8,''), NULLIF($9,''), $10, NULLIF($11,''), TRUE)
		ON CONFLICT (serial_number) WHERE deleted_at IS NULL
		DO UPDATE SET api_key_hash = EXCLUDED.api_key_hash,
		              api_key_prefix = EXCLUDED.api_key_prefix,
		              device_name = EXCLUDED.device_name,
		              device_type = EXCLUDED.device_type,
		              firmware_version = COALESCE(EXCLUDED.firmware_version, devices.firmware_version),
		              hardware_revision = COALESCE(EXCLUDED.hardware_revision, devices.hardware_revision),
		              build_number = COALESCE(EXCLUDED.build_number, devices.build_number),
		              release_channel = EXCLUDED.release_channel,
		              ip_address = COALESCE(EXCLUDED.ip_address, devices.ip_address),
		              -- DISABLED survives re-registration.
		              --
		              -- This used to be an unconditional 'ONLINE', which made
		              -- registration undo the only revocation this system has:
		              -- an operator disables a stolen terminal, whoever holds
		              -- the site key re-registers its serial, and the device is
		              -- back in service with a fresh working credential. The
		              -- heartbeat path has always been careful about this
		              -- (RecordHeartbeat below); registration was not.
		              status = CASE WHEN devices.status = 'DISABLED'
		                            THEN 'DISABLED' ELSE 'ONLINE' END,
		              last_error = NULL,
		              last_error_at = NULL,
		              registered_at = CURRENT_TIMESTAMP
		RETURNING id, public_id, site_id, serial_number, device_name, device_type,
		          status, active, api_key_prefix, registered_at`,
		siteID, req.SerialNumber, deviceName, deviceType, hash, prefix,
		req.FirmwareVersion, req.HardwareRevision, req.BuildNumber, channel, req.IPAddress).
		Scan(&device.ID, &device.PublicID, &device.SiteID, &device.SerialNumber,
			&device.DeviceName, &device.DeviceType, &device.Status, &device.Active,
			&device.APIKeyPrefix, &wasRegistered)
	if err != nil {
		return nil, "", 0, err
	}

	// A device registering at a different site than it currently belongs to is
	// a provisioning mistake, not a move. Refuse rather than silently reassign.
	if device.SiteID != siteID {
		return nil, "", 0, models.ErrDeviceSiteMismatch
	}

	// Administratively out of service. The row above has already had a new
	// credential written into it, so this rolls back -- which is the point: a
	// disabled terminal must not be brought back by anyone who can reach the
	// registration endpoint, and it must not be handed a working key either.
	//
	// `active` is checked as well as the status. They are set independently and
	// either one is an operator saying no.
	if device.Status == models.DeviceDisabled || !device.Active {
		return nil, "", 0, models.ErrDeviceDisabled
	}

	// Inside the transaction. See the note on this function.
	bootstrapped, err := enqueueBootstrapJobs(tx, device.ID)
	if err != nil {
		return nil, "", 0, err
	}

	return &device, key, bootstrapped, nil
}

// AuthenticateDevice resolves a device from its credential. Returns nil when the
// credential is unknown, revoked, or belongs to an inactive device.
func AuthenticateDevice(key string) (*models.DeviceIdentity, error) {
	if key == "" {
		return nil, nil
	}

	var d models.DeviceIdentity
	var storedHash string
	err := DB.QueryRow(`
		SELECT d.id, d.public_id, d.serial_number, d.site_id, s.company_id,
		       d.status, d.active, d.api_key_hash
		  FROM devices d
		  JOIN sites s ON s.id = d.site_id
		 WHERE d.api_key_hash = $1
		   AND d.deleted_at IS NULL
		   AND s.deleted_at IS NULL
		   AND s.active = TRUE`,
		hashDeviceKey(key)).
		Scan(&d.ID, &d.PublicID, &d.SerialNumber, &d.SiteID, &d.CompanyID,
			&d.Status, &d.Active, &storedHash)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	// The lookup above already matched on the hash; this constant-time compare
	// guards against a future refactor that widens the query.
	if subtle.ConstantTimeCompare([]byte(storedHash), []byte(hashDeviceKey(key))) != 1 {
		return nil, nil
	}

	return &d, nil
}

// RecordHeartbeat marks a device as alive and records what it is running.
// Returns the device's unacknowledged job backlog so the response can tell the
// device whether it needs to poll.
//
// A device may report ONLINE, UPDATING or ERROR for itself. It cannot claim
// OFFLINE (inferred by the server from missed heartbeats) or DISABLED (an
// administrative decision) -- a heartbeat from a DISABLED device records
// liveness without silently putting it back into service.
func RecordHeartbeat(deviceID int64, req models.DeviceHeartbeatRequest) (int, error) {
	reported := req.Status
	if !models.DeviceReportableStates[reported] {
		reported = models.DeviceOnline
	}

	_, err := DB.Exec(`
		UPDATE devices
		   SET last_heartbeat_at = CURRENT_TIMESTAMP,
		       last_seen_at = CURRENT_TIMESTAMP,
		       status = CASE WHEN status = 'DISABLED' THEN status ELSE $1 END,
		       firmware_version = COALESCE(NULLIF($2,''), firmware_version),
		       hardware_revision = COALESCE(NULLIF($3,''), hardware_revision),
		       build_number = COALESCE(NULLIF($4,''), build_number),
		       boot_count = COALESCE($5, boot_count),
		       ip_address = COALESCE(NULLIF($6,''), ip_address),
		       last_error = CASE WHEN $1 = 'ERROR' THEN NULLIF($7,'') ELSE NULL END,
		       last_error_at = CASE WHEN $1 = 'ERROR' THEN CURRENT_TIMESTAMP ELSE NULL END
		 WHERE id = $8 AND deleted_at IS NULL`,
		reported, req.FirmwareVersion, req.HardwareRevision, req.BuildNumber,
		req.BootCount, req.IPAddress, req.Error, deviceID)
	if err != nil {
		return 0, err
	}

	return GetDeviceSyncBacklog(deviceID)
}

// MarkDevicesOffline flags devices that have missed heartbeats for longer than
// the given number of seconds. Intended for a scheduled sweep.
func MarkDevicesOffline(staleAfterSeconds int) (int, error) {
	result, err := DB.Exec(`
		UPDATE devices
		   SET status = 'OFFLINE'
		 WHERE status = 'ONLINE'
		   AND deleted_at IS NULL
		   AND last_heartbeat_at IS NOT NULL
		   AND last_heartbeat_at < CURRENT_TIMESTAMP - ($1 || ' seconds')::interval`,
		staleAfterSeconds)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	return int(affected), err
}
