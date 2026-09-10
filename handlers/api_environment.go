package handlers

import (
	"encoding/json"
	"sync"

	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
)

// The deployment's API environment.
//
// ---------------------------------------------------------------------------
// WHY THIS EXISTS AND WHY IT DEFAULTS TO LIVE
// ---------------------------------------------------------------------------
//
// An integration credential carries its environment in the string itself --
// atp_live_… or atp_test_… -- and a deployment accepts one kind. That is what
// makes a staging key presented to production fail immediately and visibly
// rather than miss a hash lookup and look like a typo.
//
// THE DEFAULT IS `live`, and the direction matters. A deployment that configures
// nothing accepts only live credentials, so a test key never works unless
// somebody deliberately opted in. The other default -- test unless told
// otherwise -- would mean production silently accepting staging credentials for
// as long as it took anyone to notice the variable was unset.
//
// Set API_ENVIRONMENT=test on a staging deployment. main() validates it at
// startup and refuses to boot on an unrecognised value, so a typo in the one
// variable that decides this cannot be what changes it.

var (
	apiEnvironmentMu sync.RWMutex
	apiEnvironment   = models.APIEnvironmentLive
)

// SetAPIEnvironment installs the deployment's environment.
//
// Called from main after validation. Never from a handler.
func SetAPIEnvironment(environment string) {
	apiEnvironmentMu.Lock()
	defer apiEnvironmentMu.Unlock()
	apiEnvironment = environment
}

// APIEnvironment reports which credentials this deployment mints and accepts.
func APIEnvironment() string {
	apiEnvironmentMu.RLock()
	defer apiEnvironmentMu.RUnlock()
	return apiEnvironment
}

// bodyHasKey reports whether the request body carried a top-level key.
//
// ---------------------------------------------------------------------------
// WHY PRESENCE HAS TO BE ASKED SEPARATELY
// ---------------------------------------------------------------------------
//
// `expires_at` has three meanings and a *time.Time can only carry two of them:
//
//	absent   apply the default lifetime
//	null     never expires -- a deliberate choice
//	a value  expire then
//
// encoding/json decodes both "absent" and "null" to a nil pointer, so the
// difference between "the operator did not think about expiry" and "the operator
// asked for a credential that never expires" is invisible at the struct. Getting
// that wrong in either direction is bad: defaulting a deliberate `null` hands
// back a credential that stops working in a year and surprises somebody, and
// treating an absent field as `null` mints non-expiring credentials by accident.
//
// The body is read from gin's cache, which ShouldBindBodyWith populates, so this
// does not consume the reader the handler's own bind needs.
func bodyHasKey(c *gin.Context, key string) bool {
	raw, ok := c.Get(gin.BodyBytesKey)
	if !ok {
		return false
	}
	body, ok := raw.([]byte)
	if !ok || len(body) == 0 {
		return false
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return false
	}
	_, present := fields[key]
	return present
}

// bindCachedJSON binds a body and keeps it available for bodyHasKey.
//
// ShouldBindBodyWith caches the bytes in the context, so the handler's bind and
// the presence check read the same body rather than the second one finding an
// already-drained reader.
func bindCachedJSON(c *gin.Context, out any) error {
	return c.ShouldBindBodyWith(out, binding.JSON)
}
