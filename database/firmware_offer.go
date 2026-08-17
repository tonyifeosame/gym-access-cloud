package database

import (
	"database/sql"
	"errors"
	"log"
	"regexp"
	"strings"

	"access-terminal-cloud-api/models"
)

// Platform-driven OTA: deciding what to offer a terminal.
//
// firmware-protocol-requirements.md §5. `firmware_versions` already held
// everything needed -- version, device_type, release_channel, download_url,
// checksum_sha256, size_bytes, is_mandatory, is_current -- and API_SPEC.md said
// plainly that "nothing here downloads, schedules, or applies firmware". The
// catalogue was inventory.
//
// The device half is built: decision logic, download, streaming SHA-256
// verification, commit, and a trial boot that rolls itself back if the new image
// cannot reach its sensor or its platform. What was missing is the server
// telling a terminal that an image exists.
//
// ---------------------------------------------------------------------------
// WHICH TRANSPORT, AND WHY ONLY ONE
// ---------------------------------------------------------------------------
//
// The requirements document offers two shapes and says either works: (A) a
// FIRMWARE sync job, or (B) a field on the heartbeat response. THIS IMPLEMENTS
// (B) ONLY, deliberately.
//
// The device already heartbeats every poll interval and already sends its
// firmware_version, so the server knows on every beat whether that terminal is
// outdated -- firmware_outdated is computed today. Option B therefore needs no
// new route, no new job type, and no queue.
//
// Option A would enqueue rows that NOTHING CURRENTLY CONSUMES: the firmware's
// syncJobTypeFromName has no FIRMWARE case, so such a job parses as kUnknown and
// is acknowledged and discarded. That is safe -- it is exactly the additive-type
// path the compatibility policy relies on -- but it would mean writing a job per
// terminal per poll that is guaranteed to be thrown away. Building both would
// also be the "second OTA protocol" the requirements explicitly warn against.
//
// If the fleet ever needs staged rollout or per-terminal scheduling, option A
// becomes worth adding, and it is additive when it does.
//
// ---------------------------------------------------------------------------
// THE SERVER REFUSES BEFORE THE DEVICE HAS TO
// ---------------------------------------------------------------------------
//
// The requirements list four hard rules -- digest present, lower-case hex,
// accurate size, https URL -- and the firmware enforces every one of them and
// refuses an offer that breaks any. It could therefore be left entirely to the
// device.
//
// It is not, because a refusal at the terminal is invisible: the operator sees a
// fleet that will not update and no reason anywhere. Withholding the offer here
// and logging WHY turns "OTA does not work" into a line naming the catalogue row
// and the field that is wrong. The device's checks stay exactly where they are
// -- they are the ones that matter, because they are the ones an attacker would
// have to defeat.

// firmwareDigestPattern is the same shape the device enforces and the same one
// the schema's CHECK constraint carries: 64 LOWER-CASE hex characters.
//
// Lower case specifically. Folding case here would mean the two sides disagreed
// about what a digest is, and the disagreement would surface as an update that
// always fails verification -- at the last step, after the download, on a
// terminal that then reports itself as broken.
var firmwareDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Device-side buffer limits, from the firmware's FirmwareOffer struct.
//
// Mirrored here so the server withholds an offer the terminal would truncate
// rather than sending one it will silently mangle. A truncated URL fetches the
// wrong thing or nothing; a truncated version compares wrongly.
const (
	// firmware_update.h: char url[128].
	maxFirmwareURLLength = 127
	// firmware_update.h: kFirmwareVersionSize = 24.
	maxFirmwareVersionLength = 23
)

// FirmwareOfferFor returns the update a terminal should be told about, or nil.
//
// nil is the ordinary answer and is not an error: most terminals are up to date
// most of the time. An unusable catalogue row is also nil, with a log line
// naming what is wrong with it.
func FirmwareOfferFor(deviceID int64) (*models.FirmwareUpdateOffer, error) {
	var (
		offer      models.FirmwareUpdateOffer
		running    string
		url        sql.NullString
		checksum   sql.NullString
		sizeBytes  sql.NullInt64
		version    string
		mandatory  bool
		publicID   string
		deviceType string
	)

	// The candidate is the CURRENT build for this device's own company, type and
	// release channel. All four narrowings matter: "current" is a per-tenant
	// target, a READER must not be offered a TERMINAL image, and a device on
	// STABLE must not receive a CANARY build.
	err := DB.QueryRow(`
		SELECT COALESCE(d.firmware_version, ''), d.device_type,
		       fv.public_id, fv.version, fv.download_url, fv.checksum_sha256,
		       fv.size_bytes, fv.is_mandatory
		  FROM devices d
		  JOIN sites s ON s.id = d.site_id
		  JOIN firmware_versions fv
		       ON fv.company_id = s.company_id
		      AND fv.device_type = d.device_type
		      AND fv.release_channel = d.release_channel
		      AND fv.is_current
		      AND fv.deleted_at IS NULL
		 WHERE d.id = $1
		   AND d.deleted_at IS NULL
		   AND s.deleted_at IS NULL`, deviceID).
		Scan(&running, &deviceType, &publicID, &version, &url, &checksum,
			&sizeBytes, &mandatory)
	if errors.Is(err, sql.ErrNoRows) {
		// No current build for this channel. Not an error.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	// ALREADY RUNNING IT. Compared as an exact string, matching how
	// firmware_outdated is computed, rather than by parsing versions: the
	// DEVICE performs the ordering comparison (compareFirmwareVersions) and
	// refuses anything not newer than what it runs. Two implementations of
	// version ordering that could disagree is a worse problem than this
	// comparison being coarse.
	if running != "" && running == version {
		return nil, nil
	}

	if !firmwareOfferIsDeliverable(publicID, version, url, checksum, sizeBytes) {
		return nil, nil
	}

	offer.Version = version
	offer.DownloadURL = url.String
	offer.ChecksumSHA256 = checksum.String
	offer.SizeBytes = sizeBytes.Int64
	offer.IsMandatory = mandatory
	return &offer, nil
}

// firmwareOfferIsDeliverable applies the requirements document's four rules,
// plus the device's own buffer limits, and says why when it refuses.
func firmwareOfferIsDeliverable(publicID, version string, url, checksum sql.NullString,
	sizeBytes sql.NullInt64) bool {

	refuse := func(reason string) bool {
		// One line per poll per terminal would be a flood, so this is
		// deliberately at the catalogue row rather than the device: every
		// terminal on the channel produces the same message, and the message
		// names the row somebody has to fix.
		log.Printf("FIRMWARE OFFER WITHHELD build=%s version=%q: %s", publicID, version, reason)
		return false
	}

	// 1. A digest must exist. TLS proves the image came from the right server,
	//    not that it arrived intact and not that anybody signed it. The digest
	//    is the only end-to-end check the device has.
	if !checksum.Valid || checksum.String == "" {
		return refuse("checksum_sha256 is null; the device refuses an offer without one")
	}
	// 2. Lower-case hex.
	if !firmwareDigestPattern.MatchString(checksum.String) {
		return refuse("checksum_sha256 is not 64 lower-case hex characters")
	}

	// 3. An accurate size. It sizes the flash write and is deliberately trusted
	//    over Content-Length -- a length from a compromised or misconfigured
	//    host is exactly the value not to trust with a flash write.
	if !sizeBytes.Valid || sizeBytes.Int64 <= 0 {
		return refuse("size_bytes is null or not positive")
	}

	// 4. https. A plaintext download is refused by the device before a socket is
	//    opened; an update channel is the highest-value thing to attack here.
	if !url.Valid || url.String == "" {
		return refuse("download_url is null")
	}
	if !strings.HasPrefix(strings.ToLower(url.String), "https://") {
		return refuse("download_url is not https")
	}

	// The device's fixed buffers. Sending something it would truncate is worse
	// than sending nothing: a truncated URL fetches the wrong image or none.
	if len(url.String) > maxFirmwareURLLength {
		return refuse("download_url is longer than the device's 127-character buffer")
	}
	if len(version) > maxFirmwareVersionLength {
		return refuse("version is longer than the device's 23-character buffer")
	}

	return true
}
