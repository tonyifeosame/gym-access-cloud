package handlers

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// Device-facing endpoints.
//
// Identity comes from DeviceAuthMiddleware, which resolves either a per-device
// credential or the deprecated site-key-plus-serial pair, and puts device_id,
// device_serial, site_id and company_id in the context. Handlers here never
// re-derive identity from headers.
//
// Registration is the exception: a device has no credential yet, so that route
// sits behind the site API key instead.

const protocolVersionHeader = "X-Protocol-Version"

const (
	defaultJobBatch = 50
	maxJobBatch     = 200

	// maxResultCodeLength matches the sync_jobs.result_code column (028). A
	// device-written value is bounded in Go as well as by the column type, so
	// an over-long one is a 400 the terminal can log rather than a constraint
	// violation it cannot tell from an outage.
	maxResultCodeLength = 48
)

// negotiateProtocol checks the device's declared protocol version against what
// this server speaks. Returns false and writes the error response on mismatch.
func negotiateProtocol(c *gin.Context) bool {
	declared := c.GetHeader(protocolVersionHeader)
	if declared == "" {
		return true // older firmware predates the header; assume v1
	}

	version, err := strconv.Atoi(declared)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid " + protocolVersionHeader})
		return false
	}

	if version > models.SyncProtocolVersion {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":                   "Unsupported protocol version",
			"server_protocol_version": models.SyncProtocolVersion,
			"device_protocol_version": version,
		})
		return false
	}
	return true
}

// RegisterDevice handles POST /devices/register
//
// Authenticated with the site API key, which is the provisioning secret: anyone
// holding it can enrol a terminal at that site. The response contains the
// device's credential in plaintext and is the only time it is ever available.
//
// Registering an existing serial rotates its credential rather than failing, so
// a factory-reset terminal can recover. It is also re-seeded with the current
// member list, because a reset device has lost its local copy.
func RegisterDevice(c *gin.Context) {
	if !negotiateProtocol(c) {
		return
	}

	var req models.DeviceRegistrationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.IPAddress == "" {
		req.IPAddress = c.ClientIP()
	}

	// The device is seeded with current state inside the same transaction that
	// issues the credential, so there is no window in which a key is committed
	// but never returned to the caller that has to store it.
	device, apiKey, bootstrapped, err := database.RegisterDevice(c.GetInt64("site_id"), req)
	if errors.Is(err, models.ErrDeviceSiteMismatch) {
		c.JSON(http.StatusConflict, gin.H{"error": "Serial number is registered to another site"})
		return
	}
	if errors.Is(err, models.ErrDeviceDisabled) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Device is disabled; re-enable it before registering"})
		return
	}
	if err != nil {
		logError(c, "register device", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to register device"})
		return
	}

	c.JSON(http.StatusCreated, models.DeviceRegistrationResponse{
		ProtocolVersion: models.SyncProtocolVersion,
		DeviceID:        device.PublicID,
		SerialNumber:    device.SerialNumber,
		APIKey:          apiKey,
		BootstrapJobs:   bootstrapped,
		Warning:         "Store this api_key now. It cannot be retrieved again.",
	})
}

// DeviceHeartbeat handles POST /devices/heartbeat
//
// Records liveness and what the device is running, and tells it whether work is
// waiting so it can skip polling when there is nothing to do.
func DeviceHeartbeat(c *gin.Context) {
	if !negotiateProtocol(c) {
		return
	}

	var req models.DeviceHeartbeatRequest
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	if req.IPAddress == "" {
		req.IPAddress = c.ClientIP()
	}

	pending, capacityChanged, err := database.RecordHeartbeat(c.GetInt64("device_id"), req)
	if err != nil {
		logError(c, "record heartbeat", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to record heartbeat"})
		return
	}

	// FW-01. A terminal that has just told us its ceiling for the first time --
	// or a different one after a firmware change -- is measured against its
	// roster now, rather than at whatever future moment somebody happens to
	// request a snapshot.
	//
	// BEST EFFORT, like the firmware offer below. A heartbeat records liveness;
	// failing it because a capacity review could not be completed would take a
	// door's liveness reporting down over a diagnostic.
	if capacityChanged {
		if err := database.ReviewTerminalCapacity(c.GetInt64("device_id")); err != nil {
			logError(c, "review terminal capacity", err)
		}
	}

	// What the platform would like this terminal to be running (OTA, §5).
	//
	// BEST EFFORT, AND DELIBERATELY SO. A heartbeat records liveness and tells
	// a terminal whether it has work; failing it because the firmware
	// catalogue could not be read would take the fleet's heartbeat down with
	// it, and the console would show every door offline because of a firmware
	// query. An offer that could not be built is simply absent, which the
	// device reads as "nothing to do" -- exactly what it read before this
	// field existed.
	offer, err := database.FirmwareOfferFor(c.GetInt64("device_id"))
	if err != nil {
		logError(c, "resolve firmware offer", err)
		offer = nil
	}

	// The release order, while one is outstanding for this terminal (032).
	//
	// ON EVERY HEARTBEAT until the terminal confirms, because a heartbeat is
	// at-least-once delivery and the terminal dedupes on release_id. Best
	// effort on the read, like the two above: a heartbeat must record
	// liveness whatever else fails, and an order that could not be read is
	// simply re-sent on the next one.
	var order *models.DeviceReleaseOrder
	if ro, err := database.ReleaseOrderForDevice(c.GetInt64("device_id")); err != nil {
		logError(c, "resolve release order", err)
	} else if ro != nil {
		order = &models.DeviceReleaseOrder{
			SerialNumber: ro.SerialNumber,
			ReleaseID:    ro.ReleaseID,
			OrderedAt:    ro.OrderedAt.Unix(),
			MAC:          ro.MACHex(),
		}
	}

	c.JSON(http.StatusOK, models.DeviceHeartbeatResponse{
		ProtocolVersion: models.SyncProtocolVersion,
		DeviceID:        c.GetString("device_serial"),
		ServerTime:      time.Now().UTC(),
		PendingJobs:     pending,
		FirmwareUpdate:  offer,
		ReleaseOrder:    order,
	})
}

// ConfirmDeviceRelease handles POST /devices/release/confirm
//
// The terminal's authenticated proof that it executed a release order: the
// receipt is an HMAC only the holder of this credential can produce, over the
// order this row carries. Finalizing revokes that very credential, so this is
// the last authenticated call the terminal makes -- and the reason it treats a
// 401 here as "already released" rather than as a failure.
//
// A receipt that does not verify changes nothing and is a 409: the terminal
// has computed something the platform cannot accept, which is worth a
// distinct answer because the remedy (re-fetch the order, recompute) is not
// the remedy for "there is no order" (stop).
func ConfirmDeviceRelease(c *gin.Context) {
	var req models.DeviceReleaseConfirmRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "release_id and receipt are required"})
		return
	}
	receipt, err := hex.DecodeString(req.Receipt)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "receipt must be hex"})
		return
	}

	fin, err := database.ConfirmTerminalRelease(c.GetInt64("device_id"), req.ReleaseID,
		receipt, req.Report)
	switch {
	case errors.Is(err, database.ErrReleaseNotOrdered):
		c.JSON(http.StatusNotFound, gin.H{"error": "No release is ordered for this terminal",
			"code": "RELEASE_NOT_ORDERED"})
		return
	case errors.Is(err, database.ErrReleaseReceiptMismatch):
		c.JSON(http.StatusConflict, gin.H{"error": "The receipt does not verify",
			"code": "RELEASE_RECEIPT_MISMATCH"})
		return
	case err != nil:
		logError(c, "confirm device release", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to confirm the release"})
		return
	}

	auditReleaseConfirmed(c, fin, req.Report)

	c.JSON(http.StatusOK, models.DeviceReleaseConfirmResponse{
		SerialNumber: fin.SerialNumber,
		ReleaseID:    fin.ReleaseID,
		Released:     true,
	})
}

// auditReleaseConfirmed writes the terminal's confirmation into the trail of
// the company that lost it.
//
// NO OPERATOR ON THE REQUEST, exactly as credential collection is audited: the
// actor named is the role that performed it. The report is counts only -- the
// terminal never sends biometric material, and the schema would refuse it as
// anything but an object.
func auditReleaseConfirmed(c *gin.Context, fin *database.ReleaseFinalization, report []byte) {
	if fin == nil || fin.AlreadyReleased {
		return
	}
	changes := gin.H{
		"release_id":             fin.ReleaseID,
		"site":                   fin.SiteName,
		"device_name":            fin.DeviceName,
		"confirmed_by":           fin.ConfirmedBy,
		"pending_jobs_cancelled": fin.PendingJobsCancelled,
		"announcements_voided":   fin.AnnouncementsVoided,
	}
	if len(report) > 0 {
		changes["report"] = json.RawMessage(report)
	}
	database.WriteAuditEvent(database.AuditEntry{
		CompanyID:   fin.CompanyID,
		ActorRole:   actorRoleTerminal,
		IPAddress:   c.ClientIP(),
		UserAgent:   c.Request.UserAgent(),
		RequestID:   middleware.RequestID(c),
		Action:      auditTerminalReleaseConfirmed,
		TargetType:  auditTargetTerminal,
		TargetLabel: fin.SerialNumber,
		Changes:     changes,
	})
}

// actorRoleTerminal names the credential class in the audit trail for actions
// a terminal performed on its own, with no human on the request.
const actorRoleTerminal = "TERMINAL"

// GetReleaseOrderBySerial handles GET /devices/release-order?serial=
//
// UNAUTHENTICATED, on the announce rate limiter, and the only thing it will
// say is whether a release order exists for a serial and what it is. A
// terminal calls it when its credential is refused, to find out whether the
// 401 is a release it must execute or something else. The MAC is unforgeable
// and unusable without the terminal's own key, so this discloses nothing a
// caller could act on.
//
// 204 for "no order" AND for an unknown serial, uniformly.
func GetReleaseOrderBySerial(c *gin.Context) {
	serial := c.Query("serial")
	if serial == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "serial is required"})
		return
	}
	order, err := database.ReleaseOrderForSerial(serial)
	if err != nil {
		logError(c, "release order by serial", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read the release order"})
		return
	}
	if order == nil {
		c.Status(http.StatusNoContent)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"release_order": models.DeviceReleaseOrder{
			SerialNumber: order.SerialNumber,
			ReleaseID:    order.ReleaseID,
			OrderedAt:    order.OrderedAt.Unix(),
			MAC:          order.MACHex(),
		},
	})
}

// GetDeviceJobs handles GET /devices/jobs
//
// Returns the device's due work, oldest first. Jobs stay pending until the
// device acknowledges them, so an unanswered response simply results in the
// same jobs being offered again once the delivery lease expires.
func GetDeviceJobs(c *gin.Context) {
	if !negotiateProtocol(c) {
		return
	}

	limit := defaultJobBatch
	if limitStr := c.Query("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}
	if limit > maxJobBatch {
		limit = maxJobBatch
	}

	// A device far enough behind has its queue collapsed into a snapshot before
	// anything is handed out, so recovery does not mean replaying history.
	jobs, compacted, err := database.FetchDeviceWork(c.GetInt64("device_id"), limit)
	if err != nil {
		logError(c, "fetch device work", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve sync jobs"})
		return
	}
	if jobs == nil {
		jobs = []models.SyncJob{}
	}

	// An enrolment job leaving here means the selected terminal now HAS it and
	// is about to show the prompt.
	//
	// "Waiting for terminal" and "the terminal is showing the prompt" are the
	// two states an operator watching a customer walk to a door most needs to
	// tell apart, and nothing else in the system could distinguish them: a job
	// stays PENDING while it is being applied, deliberately, so its status says
	// nothing about whether a device has seen it.
	//
	// BEST EFFORT. Failing a device's job poll because a progress label could
	// not be written would break sync to fix a screen.
	var enrolmentJobs []int64
	for _, job := range jobs {
		if job.JobType == models.SyncJobEnrollFingerprint {
			enrolmentJobs = append(enrolmentJobs, job.ID)
		}
	}
	if len(enrolmentJobs) > 0 {
		if err := database.MarkEnrollmentDelivered(c.GetInt64("device_id"), enrolmentJobs); err != nil {
			logError(c, "mark enrolment delivered", err)
		}
	}

	c.JSON(http.StatusOK, models.SyncJobBatch{
		ProtocolVersion: models.SyncProtocolVersion,
		DeviceID:        c.GetString("device_serial"),
		ServerTime:      time.Now().UTC(),
		Count:           len(jobs),
		SnapshotTaken:   compacted,
		Jobs:            jobs,
	})
}

// ResyncDevice handles POST /devices/:serial/resync
//
// Forces a device's queue to be replaced with a snapshot of current state.
// Authenticated with the site API key -- this is an operator action, used when
// a terminal is believed to have drifted.
func ResyncDevice(c *gin.Context) {
	device, err := database.GetDeviceBySerial(c.GetInt64("site_id"), c.Param("serial"))
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Device not registered for this site"})
		return
	}
	if err != nil {
		logError(c, "resolve device", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to resolve device"})
		return
	}

	superseded, err := database.CompactDeviceBacklog(device.ID)
	if respondIfOverCapacity(c, err) {
		return
	}
	if err != nil {
		logError(c, "compact device backlog", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to queue full sync"})
		return
	}

	pending, err := database.GetDeviceSyncBacklog(device.ID)
	if err != nil {
		logError(c, "read sync backlog", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read sync backlog"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"serial_number":   device.SerialNumber,
		"superseded_jobs": superseded,
		"pending_jobs":    pending,
	})
}

// GetDeviceSettings handles GET /devices/settings
//
// The device's effective settings, inherited from its site. Devices normally
// receive settings as SETTINGS sync jobs; this is the pull equivalent, for a
// device that wants to confirm its configuration after a restart.
func GetDeviceSettings(c *gin.Context) {
	if !negotiateProtocol(c) {
		return
	}

	// GetDeviceSettings, not GetSiteSettings: the device-facing object layers
	// the validated offline-policy columns over the site's free-form settings
	// blob. GetSiteSettings returns the blob alone and is what the OPERATOR
	// endpoints read, because an operator edits the blob and must not be shown
	// the merged result as though it were what they had stored.
	settings, err := database.GetDeviceSettings(c.GetInt64("site_id"))
	if err != nil {
		logError(c, "get device settings", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve settings"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"protocol_version": models.SyncProtocolVersion,
		"device_id":        c.GetString("device_serial"),
		"settings_version": settings.Version,
		"settings":         settings.Settings,
	})
}

// CompleteDeviceJob handles POST /devices/jobs/:id/complete
//
// Acknowledging a job that is already complete succeeds rather than erroring:
// a device whose previous acknowledgement was lost in transit must be able to
// retry it safely.
func CompleteDeviceJob(c *gin.Context) {
	if !negotiateProtocol(c) {
		return
	}

	deviceID := c.GetInt64("device_id")

	jobID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid job id"})
		return
	}

	// A bare acknowledgement with no body means success
	var result models.SyncJobResult
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&result); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	if result.Status == "" {
		result.Status = "COMPLETED"
	}

	// A DEVICE-WRITTEN VALUE IS BOUNDED BEFORE IT REACHES THE DATABASE (028).
	//
	// Refused rather than truncated: half a JSON document is not a smaller
	// result, it is a malformed one, and storing it would mean the console
	// renders something no terminal ever said. The firmware applies the same
	// ceiling on its side and refuses to build a body over it, so a device that
	// reaches this branch is either not this firmware or is faulty -- both of
	// which are worth a 400 rather than a silent trim.
	if len(result.Result) > models.MaxCommandResultBytes {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("result must be at most %d bytes",
				models.MaxCommandResultBytes),
		})
		return
	}
	if len(result.ResultCode) > maxResultCodeLength {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("result_code must be at most %d characters",
				maxResultCodeLength),
		})
		return
	}

	// STORED BEFORE THE ACKNOWLEDGEMENT, so a console reading between the two
	// writes never sees an ACCEPTED command with nothing behind it. Best effort
	// on purpose: the acknowledgement is what must not fail, and a terminal has
	// nothing useful to do with "your result was not stored" except retry an
	// acknowledgement the platform has already accepted.
	//
	// A no-op for every job that is not a command, and for every firmware built
	// before the fields existed -- both send neither, and RecordCommandResult
	// returns immediately.
	if result.ResultCode != "" || len(result.Result) > 0 {
		if resErr := database.RecordCommandResult(
			deviceID, jobID, result.ResultCode, result.Result); resErr != nil {
			logError(c, "record command result", resErr)
		}
	}

	var found bool
	switch result.Status {
	case "COMPLETED":
		found, err = database.AckJobCompleted(deviceID, jobID)
	case "FAILED":
		// A BUSY TERMINAL IS NOT A FAILED COMMAND. It never started, so it is
		// re-offered without spending one of its attempts -- see AckJobBusy.
		// Keyed on the result code rather than on the message, because the
		// message is prose and the code is the contract.
		//
		// The enrolment note below is deliberately NOT reached on this path: a
		// busy refusal can only come from the command slot, which enrolments do
		// not pass through, so it would be a no-op with a misleading name.
		if result.ResultCode == models.CommandResultBusy {
			found, err = database.AckJobBusy(deviceID, jobID, result.Error)
			break
		}

		found, err = database.AckJobFailed(deviceID, jobID, result.Error)

		// An enrolment that failed is one an operator is watching, and the
		// terminal's own words are the useful part of it -- "sensor error" and
		// "the window closed with nobody at the door" send somebody to two
		// different places.
		//
		// UNCONDITIONAL rather than gated on the job type, because the update is
		// keyed on (sync_job_id, device_id) and matches nothing for any other
		// job. Reading the job back first to check its type would be a second
		// query to avoid a no-op.
		//
		// Best effort: the acknowledgement itself has already been recorded, and
		// failing this response would have the terminal retry an acknowledgement
		// the platform accepted.
		if err == nil {
			if failErr := database.FailEnrollmentForJob(deviceID, jobID, result.Error); failErr != nil {
				logError(c, "record enrolment failure", failErr)
			}
		}
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "status must be COMPLETED or FAILED"})
		return
	}

	if err != nil {
		logError(c, "acknowledge job", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to acknowledge job"})
		return
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "Job not found for this device"})
		return
	}

	pending, err := database.GetDeviceSyncBacklog(deviceID)
	if err != nil {
		logError(c, "read sync backlog", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read sync backlog"})
		return
	}

	// READY (032). The acknowledgement that passed this terminal's readiness
	// gate is recorded for the company, because on a transferred unit it is
	// the moment the previous owner's roster is provably gone. Best effort:
	// the acknowledgement itself is already committed.
	if result.Status == "COMPLETED" {
		if ready, err := database.ReadinessPassed(deviceID, jobID); err != nil {
			logError(c, "check readiness", err)
		} else if ready {
			database.WriteAuditEvent(database.AuditEntry{
				CompanyID:   c.GetInt64("company_id"),
				ActorRole:   actorRoleTerminal,
				IPAddress:   c.ClientIP(),
				UserAgent:   c.Request.UserAgent(),
				RequestID:   middleware.RequestID(c),
				Action:      auditTerminalReady,
				TargetType:  auditTargetTerminal,
				TargetLabel: c.GetString("device_serial"),
				Changes:     gin.H{"job_id": jobID},
			})
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"protocol_version": models.SyncProtocolVersion,
		"job_id":           jobID,
		"status":           result.Status,
		"pending_jobs":     pending,
	})
}
