package handlers

import (
	"encoding/base64"
	"errors"
	"net/http"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// Sealed biometric material, device-facing (026).
//
// ---------------------------------------------------------------------------
// TWO ROUTES, AND THE FETCH IS THE ONLY WAY MATERIAL LEAVES THIS PLATFORM
// ---------------------------------------------------------------------------
//
// That is why it is its own endpoint rather than a field on
// GET /devices/credentials/pending. One route to audit, one route to rate
// limit, one route to test, and a work-list response a reviewer can still clear
// at a glance because it demonstrably carries no bytes.
//
// NEITHER HANDLER HOLDS A KEY. They validate a shape, delegate an authorisation
// decision to database/credential_material.go, and move opaque base64. Sealing
// and unsealing both happen on terminals. See docs/sealing-key-lifecycle.md.
//
// ---------------------------------------------------------------------------
// WHAT IS AUDITED, AND WHAT IS NOT IN THE AUDIT ROW
// ---------------------------------------------------------------------------
//
// Every upload and every fetch, successful or refused. The rows carry the
// terminal, the person and the credential -- never the ciphertext, never the
// digest, never the key id. "Which doors received this person's fingerprint" is
// a question a customer has a right to ask, and this is what answers it.

// DeviceMaterialRequest is the body of a material upload.
//
// Ciphertext arrives base64 because JSON has no bytes. It is decoded here rather
// than in the store so that a malformed body is a 400 from the layer whose job
// is parsing, and the store below it only ever sees real bytes.
type DeviceMaterialRequest struct {
	MemberID     string `json:"member_id" binding:"required"`
	CredentialID string `json:"credential_id,omitempty"`

	CredentialType string `json:"credential_type,omitempty"`
	Vendor         string `json:"vendor,omitempty"`
	TemplateFormat string `json:"template_format,omitempty"`

	SensorProfile string `json:"sensor_profile" binding:"required"`

	Sealed struct {
		Ciphertext string `json:"ciphertext" binding:"required"`
		KeyID      string `json:"key_id" binding:"required"`
		Algorithm  string `json:"algorithm" binding:"required"`
		Digest     string `json:"digest" binding:"required"`
	} `json:"sealed" binding:"required"`
}

// UploadCredentialMaterial handles POST /api/v1/devices/credentials/material
//
// Called by the ENROLLING terminal, after it has reported its own placement
// through the existing endpoint. Two calls rather than one, deliberately: it
// leaves the placement path exactly as it was, tests included, and enrolment is
// not a hot path -- a person is standing at the door either way.
//
// IDEMPOTENT. A terminal that never heard the response retries, and a retry of
// the same material is a 200 rather than a conflict.
func UploadCredentialMaterial(c *gin.Context) {
	if !negotiateProtocol(c) {
		return
	}

	var req DeviceMaterialRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Decoded before the size check in the store, so a client sending 10 MB of
	// base64 is refused on the decoded length rather than on whatever the
	// encoding inflated it to.
	ciphertext, err := base64.StdEncoding.DecodeString(req.Sealed.Ciphertext)
	if err != nil {
		c.JSON(http.StatusBadRequest,
			gin.H{"error": "sealed.ciphertext must be base64"})
		return
	}

	result, err := database.StoreCredentialMaterial(c.GetInt64("device_id"),
		models.SealedMaterialUpload{
			MemberID:       req.MemberID,
			CredentialID:   req.CredentialID,
			CredentialType: req.CredentialType,
			Vendor:         req.Vendor,
			TemplateFormat: req.TemplateFormat,
			SensorProfile:  req.SensorProfile,
			Sealed: models.SealedCredentialMaterial{
				Ciphertext: ciphertext,
				KeyID:      req.Sealed.KeyID,
				Algorithm:  req.Sealed.Algorithm,
				Digest:     req.Sealed.Digest,
			},
		})

	switch {
	case errors.Is(err, models.ErrSealingAlgorithmUnknown),
		errors.Is(err, models.ErrSealedMaterialTooLarge),
		errors.Is(err, models.ErrSealedMaterialEmpty),
		errors.Is(err, models.ErrMaterialDigestInvalid),
		errors.Is(err, models.ErrSensorProfileRequired),
		errors.Is(err, models.ErrPlacementSubjectRequired),
		errors.Is(err, models.ErrCredentialTypeInvalid),
		errors.Is(err, database.ErrCredentialNotBiometric):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return

	case errors.Is(err, models.ErrSealingKeyUnknown):
		// 409 rather than 400: the request is well formed, and what is wrong is
		// that this terminal is sealing under a key the company is not using.
		// The remedy is on the terminal -- re-collect its credential -- and a
		// distinct status is what lets firmware tell that apart from a body it
		// should stop sending.
		c.JSON(http.StatusConflict, gin.H{
			"error": err.Error(),
			"code":  "SEALING_KEY_UNKNOWN",
		})
		return

	case errors.Is(err, models.ErrDeviceCannotExport):
		c.JSON(http.StatusConflict, gin.H{
			"error": err.Error(),
			"code":  "CAPABILITY_NOT_REPORTED",
		})
		return

	case errors.Is(err, database.ErrMaterialAlreadyPresent):
		c.JSON(http.StatusConflict, gin.H{
			"error": err.Error(),
			"code":  "MATERIAL_ALREADY_PRESENT",
		})
		return

	case errors.Is(err, models.ErrPersonNotFound):
		// 404, so a terminal stops retrying a person its tenant does not have.
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
		logError(c, "store credential material", err)
		c.JSON(http.StatusInternalServerError,
			gin.H{"error": "Failed to store credential material"})
		return
	}

	// NO CIPHERTEXT, NO DIGEST, NO KEY ID in the audit payload. What is recorded
	// is that this terminal enrolled this person and how many other doors were
	// told to expect them.
	database.WriteAuditEvent(database.AuditEntry{
		CompanyID:   c.GetInt64("company_id"),
		ActorEmail:  c.GetString("device_serial"),
		IPAddress:   c.ClientIP(),
		UserAgent:   c.Request.UserAgent(),
		RequestID:   middleware.RequestID(c),
		Action:      auditMaterialUploaded,
		TargetType:  auditTargetCredential,
		TargetLabel: result.MemberID,
		Changes: gin.H{
			"member_id":          result.MemberID,
			"placements_created": result.PlacementsCreated,
			"duplicate":          result.Duplicate,
			"enrolled_by":        c.GetString("device_serial"),
		},
	})

	c.JSON(http.StatusOK, gin.H{
		"protocol_version": models.SyncProtocolVersion,
		"credential_id":    result.CredentialID,
		"member_id":        result.MemberID,

		// How many OTHER terminals were just told to expect this person. Zero is
		// an ordinary answer, not a failure: a single-terminal site, or a fleet
		// where nothing else reports a matching sensor profile.
		"placements_created": result.PlacementsCreated,
		"duplicate":          result.Duplicate,
	})
}

// FetchCredentialMaterial handles
// GET /api/v1/devices/credentials/:id/material
//
// ---------------------------------------------------------------------------
// ONE ANSWER FOR EVERY REFUSAL: 404
// ---------------------------------------------------------------------------
//
// No placement, wrong placement state, capability never reported, mismatched
// sensor profile, revoked or suspended credential, deactivated person, roster
// says no -- all of them are the same 404 with the same body. A terminal that is
// not entitled to a credential does not learn which rule stopped it, or that the
// credential exists at all.
//
// The same discipline the announce token uses ("ONE ANSWER for an absent,
// malformed or unknown token"), for the same reason: a refusal that explains
// itself is an oracle.
//
// The reason IS recorded, on the server, in the audit row. Field diagnosis does
// not need the terminal to be told.
func FetchCredentialMaterial(c *gin.Context) {
	if !negotiateProtocol(c) {
		return
	}

	credentialID := c.Param("id")
	material, err := database.FetchCredentialMaterial(c.GetInt64("device_id"), credentialID)

	switch {
	case errors.Is(err, models.ErrMaterialNotAvailable):
		database.WriteAuditEvent(database.AuditEntry{
			CompanyID:   c.GetInt64("company_id"),
			ActorEmail:  c.GetString("device_serial"),
			IPAddress:   c.ClientIP(),
			UserAgent:   c.Request.UserAgent(),
			RequestID:   middleware.RequestID(c),
			Action:      auditMaterialRefused,
			TargetType:  auditTargetCredential,
			TargetLabel: credentialID,
			Changes: gin.H{
				"credential_id": credentialID,
				"requested_by":  c.GetString("device_serial"),
			},
		})
		c.JSON(http.StatusNotFound, gin.H{
			"error": "No material available for that credential",
			"code":  "MATERIAL_NOT_AVAILABLE",
		})
		return

	case err != nil:
		logError(c, "fetch credential material", err)
		c.JSON(http.StatusInternalServerError,
			gin.H{"error": "Failed to retrieve credential material"})
		return
	}

	// THE RECORD THAT A TEMPLATE LEFT THE PLATFORM. Carries who asked and for
	// whom; carries no ciphertext, no digest and no key id.
	database.WriteAuditEvent(database.AuditEntry{
		CompanyID:   c.GetInt64("company_id"),
		ActorEmail:  c.GetString("device_serial"),
		IPAddress:   c.ClientIP(),
		UserAgent:   c.Request.UserAgent(),
		RequestID:   middleware.RequestID(c),
		Action:      auditMaterialServed,
		TargetType:  auditTargetCredential,
		TargetLabel: material.MemberID,
		Changes: gin.H{
			"credential_id": material.CredentialID,
			"member_id":     material.MemberID,
			"served_to":     c.GetString("device_serial"),
		},
	})

	c.JSON(http.StatusOK, gin.H{
		"protocol_version": models.SyncProtocolVersion,
		"credential_id":    material.CredentialID,

		// Echoed because the receiving terminal needs it to rebuild the AAD the
		// enrolling terminal sealed under. Without it the unseal fails -- which
		// is the point of binding material to a person. Not new information: the
		// roster already delivered this person to this terminal.
		"member_id": material.MemberID,

		"credential_type": material.CredentialType,
		"vendor":          material.Vendor,
		"template_format": material.TemplateFormat,
		"sensor_profile":  material.SensorProfile,

		"sealed": gin.H{
			"ciphertext": base64.StdEncoding.EncodeToString(material.Sealed.Ciphertext),
			"key_id":     material.Sealed.KeyID,
			"algorithm":  material.Sealed.Algorithm,
			"digest":     material.Sealed.Digest,
		},
	})
}
