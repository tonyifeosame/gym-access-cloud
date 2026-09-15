package assistant

import (
	"encoding/json"
	"regexp"
)

// What must never reach the model, checked after projection as well as
// before it.
//
// PROJECTION IS THE REAL CONTROL: a tool names the fields it returns, and
// nothing else in an API body is copied. This file is the second line -- a
// scan of the projected result for the shapes of the platform's own secrets,
// so that a projection that one day copies a field it should not still cannot
// hand the model a key. A hit turns the whole result into an error rather than
// trimming the value, because a result that carried a secret is a result
// whose projection is wrong, and that wants noticing.

var secretShapes = []*regexp.Regexp{
	regexp.MustCompile(`ats_[0-9a-f]{32,}`),                  // site provisioning key
	regexp.MustCompile(`atd_[0-9a-f]{32,}`),                  // device credential
	regexp.MustCompile(`atp_[a-z]+_[0-9A-Za-z]{16,}`),        // integration credential (atp_<env>_…)
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._-]{20,}`), // any bearer token
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`), // key material
	regexp.MustCompile(`(?i)"(pairing_code|announce_token|claim_code|api_key|secret|password|token|csrf_token)"\s*:\s*"[^"]+"`),
}

// resultBytes encodes a projected result and refuses it if a secret shape is
// present. The error text names the tool, not the value.
func resultBytes(tool string, result any) ([]byte, error) {
	body, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if containsSecretShape(body) {
		return nil, &secretShapeError{tool: tool}
	}
	return body, nil
}

func containsSecretShape(body []byte) bool {
	for _, shape := range secretShapes {
		if shape.Match(body) {
			return true
		}
	}
	return false
}

type secretShapeError struct{ tool string }

func (e *secretShapeError) Error() string {
	return "assistant: the projected result of " + e.tool + " contained a secret-shaped value and was withheld"
}

// maxResultBytes bounds what one tool result may put in the model's context.
const maxResultBytes = 64 * 1024
