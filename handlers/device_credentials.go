package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// The device-facing credential API, /api/v1/devices/credentials/*.
//
// firmware-protocol-requirements.md §4. The schema for credentials and
// placements has existed since 012; nothing device-facing reached it, so a
// terminal could only report an enrolment as a `fingerprint_template` string
// that overwrote the single `people` column the placement tables replaced.
//
// WHAT THESE ROUTES DO NOT CARRY: biometric material, in either direction. The
// store's SELECT lists are the boundary and are written to be read as such --
// see database/device_credentials.go. A credential id here is a HANDLE that
// names which credential a report is about; it is not the thing itself.
//
// AUTHENTICATED AS THE DEVICE, and the device is taken from the credential
// rather than from any parameter. A terminal cannot ask what another terminal is
// missing, and cannot report a placement onto one.

// DeviceCredentialItem is one entry in a terminal's work list.
//
// FIELD NAMES ARE A WIRE CONTRACT. `member_id` matches what the person sync
// payload and the enrolment result already call a person, so the terminal is not
// asked to learn a second name for the same thing.
type DeviceCredentialItem struct {
	CredentialID string `json:"credential_id"`
	PlacementID  string `json:"placement_id,omitempty"`

	MemberID string `json:"member_id"`
	FullName string `json:"full_name,omitempty"`

	CredentialType string `json:"credential_type"`
	TemplateFormat string `json:"template_format,omitempty"`
	Vendor         string `json:"vendor,omitempty"`

	State     string `json:"state"`
	Attempts  int    `json:"attempts"`
	LastError string `json:"last_error,omitempty"`

	Generation int `json:"generation"`
}

// GetPendingCredentials handles GET /api/v1/devices/credentials/pending
//
// "What am I expected to hold that I do not." The high-value half of §4 and the
// cheapest: no biometric material moves, the schema already models it, and it
// turns "enrolled at one door, silently does nothing at the others" from an
// unexplained fault into a visible task list an installer can work through.
func GetPendingCredentials(c *gin.Context) {
	if !negotiateProtocol(c) {
		return
	}

	limit := 0
	if raw := c.Query("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}

	pending, err := database.PendingPlacementsFor(c.GetInt64("device_id"), limit)
	if err != nil {
		logError(c, "list pending credentials", err)
		c.JSON(http.StatusInternalServerError,
			gin.H{"error": "Failed to retrieve pending credentials"})
		return
	}

	items := make([]DeviceCredentialItem, 0, len(pending))
	for _, p := range pending {
		items = append(items, DeviceCredentialItem{
			CredentialID:   p.CredentialID,
			PlacementID:    p.PlacementID,
			MemberID:       p.ExternalID,
			FullName:       p.FullName,
			CredentialType: p.CredentialType,
			TemplateFormat: p.TemplateFormat,
			Vendor:         p.Vendor,
			State:          p.State,
			Attempts:       p.Attempts,
			LastError:      p.LastError,
			Generation:     p.Generation,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"protocol_version": models.SyncProtocolVersion,
		"device_id":        c.GetString("device_serial"),
		"count":            len(items),
		"credentials":      items,
	})
}

// DevicePlacementRequest is a terminal reporting what it did.
//
// The shape the requirements document specifies, field for field:
// {credential_id?, member_id, credential_type, template_format, vendor, slot,
// state, error?}.
type DevicePlacementRequest struct {
	// Optional: present when the terminal was working from a pending item.
	CredentialID string `json:"credential_id,omitempty"`

	MemberID string `json:"member_id" binding:"required"`

	CredentialType string `json:"credential_type,omitempty"`
	TemplateFormat string `json:"template_format,omitempty"`
	Vendor         string `json:"vendor,omitempty"`

	// Slot is 1-based. Not `binding:"required"` -- validator's "required"
	// rejects zero, and a FAILED report legitimately carries no slot. The store
	// requires it for PLACED specifically, which is the case where it means
	// something.
	Slot int `json:"slot,omitempty"`

	State string `json:"state" binding:"required"`

	// Error is the device's own words, so an operator reads "sensor full"
	// rather than "failed".
	Error string `json:"error,omitempty"`
}

// ReportCredentialPlacement handles POST /api/v1/devices/credentials/placement
//
// Replaces the enrolment-result call for terminals that use it. Idempotent on
// (credential, device): a terminal retrying a report whose response it never
// heard must not create a second placement.
func ReportCredentialPlacement(c *gin.Context) {
	if !negotiateProtocol(c) {
		return
	}

	var req DevicePlacementRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	result, err := database.RecordPlacement(c.GetInt64("device_id"), database.PlacementReport{
		CredentialID:   req.CredentialID,
		ExternalID:     req.MemberID,
		CredentialType: req.CredentialType,
		TemplateFormat: req.TemplateFormat,
		Vendor:         req.Vendor,
		Slot:           req.Slot,
		State:          req.State,
		Error:          req.Error,
	})
	switch {
	case errors.Is(err, models.ErrPlacementStateInvalid),
		errors.Is(err, models.ErrPlacementSlotRequired),
		errors.Is(err, models.ErrPlacementSubjectRequired),
		errors.Is(err, models.ErrCredentialTypeInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	case errors.Is(err, models.ErrPersonNotFound):
		// A terminal must not retry a person that does not exist in its tenant.
		// 404 rather than 500 is what stops it retrying forever.
		c.JSON(http.StatusNotFound, gin.H{"error": "Member not found"})
		return
	case errors.Is(err, models.ErrCredentialNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Credential not found"})
		return
	case errors.Is(err, models.ErrDeviceNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Terminal not found"})
		return
	case err != nil:
		if database.IsConstraintViolation(err) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		logError(c, "record credential placement", err)
		c.JSON(http.StatusInternalServerError,
			gin.H{"error": "Failed to record placement"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"protocol_version": models.SyncProtocolVersion,
		"credential_id":    result.CredentialID,
		"placement_id":     result.PlacementID,
		"member_id":        result.ExternalID,
		"state":            result.State,
		"generation":       result.Generation,
	})
}
