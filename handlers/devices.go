package handlers

import (
	"database/sql"
	"net/http"
	"strconv"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// Device-facing synchronization endpoints.
//
// Protocol versioning: a device may declare the protocol it speaks with the
// X-Protocol-Version header. The server refuses a version it does not
// understand rather than sending a payload the firmware will misparse, and
// echoes the negotiated version in every envelope.
//
// Device identity is currently the X-Device-Serial header, checked against the
// site the API key authenticated. That authenticates the *site*, not the
// device -- per-device credentials are Sprint 5. Until then, a device at a
// given site could poll for another device at that same site.

const deviceSerialHeader = "X-Device-Serial"
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

// resolveDevice identifies the calling device within the authenticated site
func resolveDevice(c *gin.Context) (*models.Device, bool) {
	serial := c.GetHeader(deviceSerialHeader)
	if serial == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": deviceSerialHeader + " header required"})
		return nil, false
	}

	device, err := database.GetDeviceBySerial(c.GetInt64("site_id"), serial)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "Device not registered for this site"})
		return nil, false
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to resolve device"})
		return nil, false
	}

	if !device.Active || device.Status == "RETIRED" {
		c.JSON(http.StatusForbidden, gin.H{"error": "Device is inactive"})
		return nil, false
	}

	return device, true
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

	device, ok := resolveDevice(c)
	if !ok {
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

	jobs, err := database.GetPendingJobsForDevice(device.ID, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve sync jobs"})
		return
	}
	if jobs == nil {
		jobs = []models.SyncJob{}
	}

	c.JSON(http.StatusOK, models.SyncJobBatch{
		ProtocolVersion: models.SyncProtocolVersion,
		DeviceID:        device.SerialNumber,
		ServerTime:      time.Now().UTC(),
		Count:           len(jobs),
		Jobs:            jobs,
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

	device, ok := resolveDevice(c)
	if !ok {
		return
	}

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
		found, err = database.AckJobCompleted(device.ID, jobID)
	case "FAILED":
		found, err = database.AckJobFailed(device.ID, jobID, result.Error)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "status must be COMPLETED or FAILED"})
		return
	}

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to acknowledge job"})
		return
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "Job not found for this device"})
		return
	}

	pending, err := database.GetDeviceSyncBacklog(device.ID)
	if err != nil {
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
