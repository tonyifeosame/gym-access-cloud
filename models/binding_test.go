package models_test

import (
	"testing"

	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin/binding"
)

// Regression tests for request-binding contracts.
//
// These need no database: they exercise the struct tags, which is where a whole
// class of silent API bugs lives. In go-playground/validator, `required` means
// "not the zero value" -- so on a bool it rejects `false`, and on a string it
// rejects "". Each test below pins a case where that behaviour previously made a
// legitimate request fail.

func TestAccessLogAcceptsDeniedAttempt(t *testing.T) {
	// `granted: false` is the whole point of an audit trail. A `required` tag on
	// this bool made it impossible to record a denied entry.
	body := []byte(`{"member_id":"MEM001","granted":false,"source":"fingerprint"}`)

	var req models.AccessLogRequest
	if err := binding.JSON.BindBody(body, &req); err != nil {
		t.Fatalf("a denied access attempt must bind: %v", err)
	}
	if req.Granted {
		t.Error("granted should have decoded as false")
	}
}

func TestAccessLogAcceptsUnknownCredential(t *testing.T) {
	// An unrecognised finger has no member id. It must still be loggable --
	// these are the attempts an operator most wants to see.
	body := []byte(`{"granted":false,"source":"fingerprint","message":"unknown finger"}`)

	var req models.AccessLogRequest
	if err := binding.JSON.BindBody(body, &req); err != nil {
		t.Fatalf("an unknown credential must bind: %v", err)
	}
	if req.MemberID != "" {
		t.Errorf("member_id should be empty, got %q", req.MemberID)
	}
}

func TestAccessLogStillRequiresSource(t *testing.T) {
	// Loosening the other fields must not have loosened this one.
	body := []byte(`{"granted":true}`)

	var req models.AccessLogRequest
	if err := binding.JSON.BindBody(body, &req); err == nil {
		t.Error("source is required and its absence must be rejected")
	}
}

func TestMemberUpdateDoesNotRequireMemberID(t *testing.T) {
	// The member id comes from the URL. Requiring it in the body too made a
	// correct-looking update fail validation.
	body := []byte(`{"full_name":"Ada B","membership_type":"MONTHLY","active":true}`)

	var req models.MemberUpdateRequest
	if err := binding.JSON.BindBody(body, &req); err != nil {
		t.Fatalf("update without member_id in the body must bind: %v", err)
	}
	if req.FullName != "Ada B" {
		t.Errorf("full_name = %q, want %q", req.FullName, "Ada B")
	}
}

func TestMemberUpdateAllowsDeactivation(t *testing.T) {
	// `active: false` must survive binding, or a member could never be
	// deactivated through the API.
	body := []byte(`{"full_name":"Ada","membership_type":"MONTHLY","active":false}`)

	var req models.MemberUpdateRequest
	if err := binding.JSON.BindBody(body, &req); err != nil {
		t.Fatalf("deactivating a member must bind: %v", err)
	}
	if req.Active {
		t.Error("active should have decoded as false")
	}
}

func TestMemberUpdateStillRequiresName(t *testing.T) {
	body := []byte(`{"membership_type":"MONTHLY"}`)

	var req models.MemberUpdateRequest
	if err := binding.JSON.BindBody(body, &req); err == nil {
		t.Error("full_name is required and its absence must be rejected")
	}
}

func TestSyncJobResultAcceptsEmptyObject(t *testing.T) {
	// Constrained firmware may acknowledge with `{}`. That must mean COMPLETED,
	// exactly as an empty body does, rather than failing validation.
	var req models.SyncJobResult
	if err := binding.JSON.BindBody([]byte(`{}`), &req); err != nil {
		t.Fatalf("an empty acknowledgement object must bind: %v", err)
	}
	if req.Status != "" {
		t.Errorf("status should be empty so the handler can default it, got %q", req.Status)
	}
}

func TestDeviceReportableStates(t *testing.T) {
	// A device may describe itself, but must not be able to claim states the
	// server owns: OFFLINE is inferred, DISABLED is administrative.
	reportable := []string{models.DeviceOnline, models.DeviceUpdating, models.DeviceError}
	for _, state := range reportable {
		if !models.DeviceReportableStates[state] {
			t.Errorf("%s should be reportable by a device", state)
		}
	}

	forbidden := []string{models.DeviceOffline, models.DeviceDisabled, models.DeviceProvisioning}
	for _, state := range forbidden {
		if models.DeviceReportableStates[state] {
			t.Errorf("%s must not be claimable by a device", state)
		}
	}
}
