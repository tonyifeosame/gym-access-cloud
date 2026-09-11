package middleware

import (
	"log"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
	"access-terminal-cloud-api/service"
)

// Rate limiting for the public API -- API_SPEC.md section 18, "Rate limits".
//
// ---------------------------------------------------------------------------
// THREE BUCKETS, TWO OF WHICH A REQUEST MUST PASS
// ---------------------------------------------------------------------------
//
//	read / credential   the key's own allowance. Burst 60, refill 300 a
//	                    minute: a full-roster sync (10,000 members at 200 a
//	                    page) is fifty requests and fits inside one burst; a
//	                    poller at one request a second never touches it.
//
//	read / company      the tenant's aggregate. Burst 200, refill 900 a
//	                    minute. A company may hold twenty credentials, so the
//	                    per-key allowance alone would let one tenant take a
//	                    hundred requests a second; the aggregate caps a tenant
//	                    at a share of the service that leaves the terminal sync
//	                    paths, which draw on the same pool, their headroom.
//
//	auth_failure        unauthenticated attempts. Burst 30, refill 60 a
//	                    minute, and CHARGED ONLY ON FAILURE: a request that
//	                    authenticates never touches this bucket, and a request
//	                    that does not never touches a customer's. So a spray of
//	                    bad keys cannot drain an integrator's allowance, and an
//	                    integrator's traffic cannot be counted as attack.
//
// THE CREDENTIAL BUCKET IS CHECKED FIRST, then the company's, so a refusal's
// Retry-After describes the caller's own bucket when that is the one that is
// empty. Both are spent on every authenticated request, whatever the handler
// then answers -- a 403, a 404 or a 400 still did the lookup work.
//
// THE AUTH-FAILURE SUBJECT IS THE CLIENT ADDRESS AS gin REPORTS IT. On the
// Render deployment TRUSTED_PROXIES=none makes that Render's own proxy for
// every request, so the bucket is effectively ONE, service-wide: a cap on how
// many refused attempts the deployment will serve per minute, not a per-source
// fairness mechanism. That is accepted for now and recorded in
// docs/operations.md; trusting proxy ranges is a separate decision, and this
// file deliberately does not depend on it. What the design guarantees on any
// deployment: an exhausted auth-failure bucket never delays a request that
// carries a valid key, because the bucket is consulted only after
// authentication has already failed.
//
// AN EXHAUSTED BUCKET IS REMEMBERED IN PROCESS for exactly its Retry-After, so
// a caller hammering an empty bucket costs no database statement until the
// bucket could plausibly have a token again. This is a per-instance negative
// cache over the shared decision, not a second limiter: a sibling instance that
// has not seen the refusal asks the store and gets the same answer.
//
// FAIL CLOSED. A store that cannot answer is a 503 service_unavailable with
// Retry-After, exactly as the credential-endpoint limiters and the auth path
// behave. No request is served unlimited because the limiter was down.
//
// THE NUMBERS ARE DEFAULTS, NOT CONTRACT. Each is overridable by environment
// (below) and section 18 publishes them as subject to change with notice; the
// RateLimit-* headers on every authenticated response are what a client paces
// against (a 401, the 429 in its place, and a 503 never reached a credential
// bucket and carry none).

const (
	RateClassRead        = "read"
	RateClassAuthFailure = "auth_failure"

	defaultPublicReadPerMinute        = 300
	defaultPublicReadBurst            = 60
	defaultPublicCompanyPerMinute     = 900
	defaultPublicCompanyBurst         = 200
	defaultPublicAuthFailurePerMinute = 60
	defaultPublicAuthFailureBurst     = 30
)

// Configuration, all optional.
func publicReadPerMinute() int {
	return envRateLimit("PUBLIC_READ_RATE_LIMIT_PER_MINUTE", defaultPublicReadPerMinute)
}
func publicReadBurst() int { return envRateLimit("PUBLIC_READ_RATE_BURST", defaultPublicReadBurst) }
func publicCompanyPerMinute() int {
	return envRateLimit("PUBLIC_COMPANY_RATE_LIMIT_PER_MINUTE", defaultPublicCompanyPerMinute)
}
func publicCompanyBurst() int {
	return envRateLimit("PUBLIC_COMPANY_RATE_BURST", defaultPublicCompanyBurst)
}
func publicAuthFailurePerMinute() int {
	return envRateLimit("PUBLIC_AUTH_FAILURE_RATE_LIMIT_PER_MINUTE", defaultPublicAuthFailurePerMinute)
}
func publicAuthFailureBurst() int {
	return envRateLimit("PUBLIC_AUTH_FAILURE_RATE_BURST", defaultPublicAuthFailureBurst)
}

// refusalCache remembers, per subject, when an empty bucket could next hold a
// token. See "AN EXHAUSTED BUCKET IS REMEMBERED" above.
type refusalCache struct {
	mu    sync.Mutex
	until map[string]time.Time
}

func newRefusalCache() *refusalCache { return &refusalCache{until: map[string]time.Time{}} }

// blocked reports a remembered refusal that has not yet elapsed, and how long
// remains. Expired entries are dropped as they are met, so the map is bounded
// by the number of subjects refused within one Retry-After.
func (r *refusalCache) blocked(key string, now time.Time) (time.Duration, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	until, ok := r.until[key]
	if !ok {
		return 0, false
	}
	if !now.Before(until) {
		delete(r.until, key)
		return 0, false
	}
	return until.Sub(now), true
}

func (r *refusalCache) remember(key string, retryAfter time.Duration, now time.Time) {
	if retryAfter <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.until[key] = now.Add(retryAfter)
	// Opportunistic sweep so a long-running process does not accumulate
	// entries for subjects that never return.
	if len(r.until) > 4096 {
		for k, t := range r.until {
			if !now.Before(t) {
				delete(r.until, k)
			}
		}
	}
}

// publicLimiter is one public allowance with a burst distinct from its refill.
type publicLimiter struct {
	*limiter
	refusals *refusalCache
}

func newPublicLimiter(class string, perMinute, burst int,
	subject func(*gin.Context) (string, string)) *publicLimiter {
	l := newLimiter(class, perMinute, true, subject)
	l.burst = float64(burst)
	return &publicLimiter{limiter: l, refusals: newRefusalCache()}
}

// decide asks the store, honouring the negative cache, and remembers a refusal.
func (p *publicLimiter) decide(c *gin.Context, kind, key string) (database.RateDecision, error) {
	now := time.Now()
	if wait, blocked := p.refusals.blocked(kind+":"+key, now); blocked {
		return database.RateDecision{Allowed: false, RetryAfter: wait}, nil
	}
	decision, err := p.limiter.decideFor(c, kind, key)
	if err == nil && !decision.Allowed {
		p.refusals.remember(kind+":"+key, decision.RetryAfter, now)
	}
	return decision, err
}

// PublicAPILimiter authenticates the request and applies the public allowances.
//
// It REPLACES APICredentialAuthMiddleware on the public group rather than
// sitting beside it, because the auth-failure bucket must be charged only when
// authentication has failed and the read buckets only when it has succeeded --
// one middleware that sees both outcomes is simpler and harder to mis-order
// than two that have to agree about what the other did. Everything
// APICredentialAuthMiddleware set is still set here, so Tenant(c) and
// RequireScope work unchanged.
func PublicAPILimiter(environment string) gin.HandlerFunc {
	byCredential := newPublicLimiter(RateClassRead, publicReadPerMinute(), publicReadBurst(), nil)
	byCompany := newPublicLimiter(RateClassRead, publicCompanyPerMinute(), publicCompanyBurst(), nil)
	authFailure := newPublicLimiter(RateClassAuthFailure, publicAuthFailurePerMinute(),
		publicAuthFailureBurst(), nil)

	return func(c *gin.Context) {
		presented := service.BearerCredential(c.GetHeader("Authorization"))
		tc, authErr := service.Authenticate(presented, environment, RequestID(c))
		if authErr != nil {
			code := service.ErrCredentialInvalid().Code()
			if svcErr, ok := service.As(authErr); ok {
				code = svcErr.Code()
			}
			if models.APIErrors[code].Status != http.StatusUnauthorized {
				// A store failure during authentication: 503, and no bucket is
				// charged for a failure that was ours.
				writeAPIError(c, code)
				return
			}
			// Charged on failure only. A refusal here is a 429 in place of the
			// 401 -- the caller learns nothing about the key it presented.
			decision, err := authFailure.decide(c, RateSubjectAddress, c.ClientIP())
			if err != nil {
				logRateStoreFailure(c, RateClassAuthFailure, err)
				writeAPIError(c, models.CodeServiceUnavailable)
				return
			}
			if !decision.Allowed {
				refusePublicForRate(c, decision.RetryAfter, nil)
				return
			}
			c.Header("WWW-Authenticate", `Bearer realm="accesslink"`)
			writeAPIError(c, code)
			return
		}

		// Authenticated. The credential's bucket first, then the company's.
		credKey := strconv.FormatInt(tc.CredentialID(), 10)
		companyKey := strconv.FormatInt(tc.CompanyID(), 10)

		decision, err := byCredential.decide(c, RateSubjectCredential, credKey)
		if err != nil {
			logRateStoreFailure(c, RateClassRead, err)
			writeAPIError(c, models.CodeServiceUnavailable)
			return
		}
		if !decision.Allowed {
			database.NoteAPICredentialUse(tc.CredentialID(), c.ClientIP(), RateClassRead, time.Now(), true)
			refusePublicForRate(c, decision.RetryAfter, byCredential.limiter)
			return
		}
		companyDecision, err := byCompany.decide(c, RateSubjectCompany, companyKey)
		if err != nil {
			logRateStoreFailure(c, RateClassRead, err)
			writeAPIError(c, models.CodeServiceUnavailable)
			return
		}
		if !companyDecision.Allowed {
			database.NoteAPICredentialUse(tc.CredentialID(), c.ClientIP(), RateClassRead, time.Now(), true)
			// The credential's token was spent; the headers still describe the
			// credential's bucket, which is what the caller can act on.
			setRateHeaders(c, byCredential.limiter, decision.Remaining)
			refusePublicForRate(c, companyDecision.RetryAfter, nil)
			return
		}

		setRateHeaders(c, byCredential.limiter, decision.Remaining)
		database.NoteAPICredentialUse(tc.CredentialID(), c.ClientIP(), RateClassRead, time.Now(), false)

		c.Set(ContextTenant, tc)
		c.Set(ContextAuthActor, ActorIntegration)
		c.Next()
	}
}

// setRateHeaders emits the IETF RateLimit header fields for a bucket.
//
//	RateLimit-Limit      the burst ceiling
//	RateLimit-Remaining  whole tokens left after this request
//	RateLimit-Reset      seconds until the bucket is full again
//	RateLimit-Policy     the sustained allowance, "<per minute>;w=60"
func setRateHeaders(c *gin.Context, l *limiter, remaining float64) {
	capacity := l.capacity()
	if remaining < 0 {
		remaining = 0
	}
	reset := 0.0
	if l.perSecond() > 0 {
		reset = (capacity - remaining) / l.perSecond()
	}
	c.Header("RateLimit-Limit", strconv.Itoa(int(capacity)))
	c.Header("RateLimit-Remaining", strconv.Itoa(int(math.Floor(remaining))))
	c.Header("RateLimit-Reset", strconv.Itoa(int(math.Ceil(reset))))
	c.Header("RateLimit-Policy", strconv.Itoa(l.perMinute)+";w=60")
}

// refusePublicForRate writes the section 18 429. When the refusing bucket is
// known its headers are emitted with Remaining 0; a company-level refusal
// leaves the credential's headers, already set, in place.
func refusePublicForRate(c *gin.Context, retryAfter time.Duration, l *limiter) {
	seconds := int(math.Ceil(retryAfter.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	if l != nil {
		setRateHeaders(c, l, 0)
	}
	c.Header("Retry-After", strconv.Itoa(seconds))
	writeAPIError(c, models.CodeRateLimitExceeded)
}

func logRateStoreFailure(c *gin.Context, class string, err error) {
	log.Printf("request_id=%s error op=\"rate limit\" class=%s: %v", RequestID(c), class, err)
}
