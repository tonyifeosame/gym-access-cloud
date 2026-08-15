package handlers

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"time"

	"access-terminal-cloud-api/database"
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

	pending, err := database.RecordHeartbeat(c.GetInt64("device_id"), req)
	if err != nil {
		logError(c, "record heartbeat", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to record heartbeat"})
		return
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

	c.JSON(http.StatusOK, models.DeviceHeartbeatResponse{
		ProtocolVersion: models.SyncProtocolVersion,
		DeviceID:        c.GetString("device_serial"),
		ServerTime:      time.Now().UTC(),
		PendingJobs:     pending,
		FirmwareUpdate:  offer,
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

	var found bool
	switch result.Status {
	case "COMPLETED":
		found, err = database.AckJobCompleted(deviceID, jobID)
	case "FAILED":
		found, err = database.AckJobFailed(deviceID, jobID, result.Error)
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

	c.JSON(http.StatusOK, gin.H{
		"protocol_version": models.SyncProtocolVersion,
		"job_id":           jobID,
		"status":           result.Status,
		"pending_jobs":     pending,
	})
}
