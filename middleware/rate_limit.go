package middleware

import (
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// Rate limiting for the credential endpoints.
//
// This API has no rate limiting anywhere else, and that is a defensible choice
// for machine credentials: a device key is 256 bits from crypto/rand and cannot
// be guessed at any rate. A PASSWORD can. Login is the one endpoint where an
// attacker gets unlimited cheap attempts against a human-chosen secret, so it is
// the one endpoint that gets a limiter here. Nothing else is touched.
//
// Two layers, deliberately different:
//
//   - Per account, in the database (users.locked_until): survives restarts and
//     follows the account across whatever address is attacking it.
//   - Per client address, here: stops an attacker spreading attempts thinly
//     across many accounts, which no per-account counter can see.
//
// KNOWN LIMIT. This bucket is in-process, so with more than one instance the
// effective limit multiplies by the instance count. That is honest for the
// current deployment -- Render's free tier runs one instance -- and the fix when
// it stops being true is a shared store, not a bigger comment. The per-account
// lock is unaffected either way, because it lives in the database.

const (
	defaultLoginRateLimitPerMinute = 10

	// rateLimitIdleTTL is how long an unused bucket is kept. An attacker
	// rotating source addresses would otherwise grow the map without bound.
	rateLimitIdleTTL = 10 * time.Minute

	// rateLimitSweepEvery bounds how often the map is swept, so a burst of
	// distinct addresses does not turn every request into a full scan.
	rateLimitSweepEvery = time.Minute
)

// LoginRateLimitPerMinute resolves the configured allowance.
func LoginRateLimitPerMinute() int {
	if raw := os.Getenv("LOGIN_RATE_LIMIT_PER_MINUTE"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			return v
		}
	}
	return defaultLoginRateLimitPerMinute
}

// bucket is one client's allowance, refilled continuously rather than reset on a
// boundary. A fixed window would let a caller spend a full allowance at the end
// of one window and another at the start of the next, which is twice the
// intended rate at exactly the moment it matters.
type bucket struct {
	tokens   float64
	lastFill time.Time
}

type rateLimiter struct {
	mu        sync.Mutex
	buckets   map[string]*bucket
	limit     float64
	perSecond float64
	lastSweep time.Time
}

// allow spends a token for key, reporting whether it was available and how long
// until the next one is.
func (r *rateLimiter) allow(key string, now time.Time) (bool, time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if now.Sub(r.lastSweep) >= rateLimitSweepEvery {
		for k, b := range r.buckets {
			if now.Sub(b.lastFill) > rateLimitIdleTTL {
				delete(r.buckets, k)
			}
		}
		r.lastSweep = now
	}

	b, exists := r.buckets[key]
	if !exists {
		b = &bucket{tokens: r.limit, lastFill: now}
		r.buckets[key] = b
	}

	b.tokens += now.Sub(b.lastFill).Seconds() * r.perSecond
	if b.tokens > r.limit {
		b.tokens = r.limit
	}
	b.lastFill = now

	if b.tokens < 1 {
		deficit := (1 - b.tokens) / r.perSecond
		return false, time.Duration(deficit * float64(time.Second))
	}

	b.tokens--
	return true, 0
}

// LoginRateLimiter limits credential attempts per client address.
//
// Returns a single limiter to share across the routes it is mounted on, so
// login and password-change draw from one allowance rather than two: an attacker
// must not get a second budget by alternating between them.
//
// The address comes from c.ClientIP(), which is only as trustworthy as the proxy
// configuration -- configureTrustedProxies in router.go is what makes it so, and
// without it a caller could pick its own apparent address and its own bucket.
func LoginRateLimiter() gin.HandlerFunc {
	limit := float64(LoginRateLimitPerMinute())
	limiter := &rateLimiter{
		buckets:   map[string]*bucket{},
		limit:     limit,
		perSecond: limit / 60,
		lastSweep: time.Now(),
	}

	return func(c *gin.Context) {
		allowed, retryAfter := limiter.allow(c.ClientIP(), time.Now())
		if !allowed {
			seconds := int(retryAfter.Seconds())
			if seconds < 1 {
				seconds = 1
			}
			c.Header("Retry-After", strconv.Itoa(seconds))
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error": "Too many attempts, please wait before trying again",
			})
			c.Abort()
			return
		}
		c.Next()
	}
}
