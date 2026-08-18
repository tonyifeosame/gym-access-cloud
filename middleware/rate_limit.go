package middleware

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
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

	// defaultAnnounceRateLimitPerMinute bounds the device-facing provisioning
	// endpoints per client address.
	//
	// ---------------------------------------------------------------------
	// WHY THIS IS 300 AND NOT 60
	// ---------------------------------------------------------------------
	//
	// A SITE IS ONE ADDRESS. Every terminal in a building shares the customer's
	// public IP, and a terminal waiting to be approved polls on the server's own
	// cadence -- five seconds, so twelve requests a minute each. At sixty a
	// minute that is FIVE TERMINALS before a legitimate installation starts
	// being refused, and the refusals land on the customer commissioning
	// hardware rather than on anybody attacking anything.
	//
	// Three hundred covers a twenty-five terminal simultaneous install, which is
	// larger than any single site this platform has. It is not a weakening: the
	// per-terminal limiter below did not exist when this was sixty, and it is
	// what now bounds ONE device -- the thing the address limiter was being
	// asked to do and could not, because it cannot see the difference between
	// one terminal asking twelve times and twelve terminals asking once.
	//
	// WHAT IT PROTECTS is not the pairing code -- that is never guessed here,
	// because guessing it happens in an authenticated console against
	// AdoptRateLimiter -- but the cost of CREATING announcement rows. That is
	// the residual: an attacker varying the serial gets a fresh row per request
	// up to this ceiling. PurgeAnnouncements is the backstop, and a shared
	// store would be the fix if one address ever needed a tighter bound than
	// the whole site's traffic.
	defaultAnnounceRateLimitPerMinute = 300

	// defaultAnnounceDeviceRateLimitPerMinute bounds ONE TERMINAL, whatever
	// address it arrives from.
	//
	// This is the limiter that makes the address allowance above safe to raise.
	// It is keyed on the identity the request carries -- the serial on an
	// announce, the announce token on a poll -- so a device in a loop is bounded
	// by its own behaviour rather than by its neighbours', and a hundred
	// terminals at one site cannot be spent by one of them misbehaving.
	//
	// TWENTY against a legitimate peak of about fourteen: twelve polls a minute
	// at the five-second cadence, plus the occasional re-announce after a
	// reboot. A terminal that hammers is cut off at twenty and backs off, which
	// its firmware already does correctly on a 429 -- it treats the rate limit
	// as "the announcement is fine, I asked too often" and keeps its stored
	// code rather than discarding it.
	defaultAnnounceDeviceRateLimitPerMinute = 20

	// defaultAdoptRateLimitPerMinute bounds pairing-code attempts from ONE
	// SIGNED-IN SESSION.
	//
	// This is the limiter that matters for the code itself. Thirty-nine bits is
	// ample against an attacker who gets ten attempts a minute and fifteen
	// minutes of validity, and useless against one who gets unlimited attempts
	// from a session they already hold -- which is the case this exists for. An
	// operator who mistypes a code a few times is nowhere near it.
	defaultAdoptRateLimitPerMinute = 10

	// maxAnnounceBodyBytes bounds what the announce limiter will buffer to find
	// a serial. See announceDeviceKey.
	maxAnnounceBodyBytes = 8 << 10

	// rateLimitIdleTTL is how long an unused bucket is kept. An attacker
	// rotating source addresses would otherwise grow the map without bound.
	rateLimitIdleTTL = 10 * time.Minute

	// rateLimitSweepEvery bounds how often the map is swept, so a burst of
	// distinct addresses does not turn every request into a full scan.
	rateLimitSweepEvery = time.Minute
)

// LoginRateLimitPerMinute resolves the configured allowance.
func LoginRateLimitPerMinute() int {
	return envRateLimit("LOGIN_RATE_LIMIT_PER_MINUTE", defaultLoginRateLimitPerMinute)
}

// AnnounceRateLimitPerMinute resolves the device provisioning allowance.
func AnnounceRateLimitPerMinute() int {
	return envRateLimit("ANNOUNCE_RATE_LIMIT_PER_MINUTE", defaultAnnounceRateLimitPerMinute)
}

// AnnounceDeviceRateLimitPerMinute resolves the per-terminal allowance.
func AnnounceDeviceRateLimitPerMinute() int {
	return envRateLimit("ANNOUNCE_DEVICE_RATE_LIMIT_PER_MINUTE",
		defaultAnnounceDeviceRateLimitPerMinute)
}

// AdoptRateLimitPerMinute resolves the pairing-code allowance per session.
func AdoptRateLimitPerMinute() int {
	return envRateLimit("ADOPT_RATE_LIMIT_PER_MINUTE", defaultAdoptRateLimitPerMinute)
}

func envRateLimit(key string, fallback int) int {
	if raw := os.Getenv(key); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			return v
		}
	}
	return fallback
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
	return keyedRateLimiter(LoginRateLimitPerMinute(), clientAddressKey)
}

// AnnounceRateLimiter limits the device provisioning endpoints per client
// address.
//
// ITS OWN INSTANCE, never shared with login, and the reason is the same one the
// claim route already states: a terminal in a legitimate polling loop must not
// exhaust the allowance an operator needs to sign in and approve it, and an
// attacker hammering login must not stop a customer commissioning hardware.
// TWO BUCKETS, BOTH OF WHICH MUST HAVE A TOKEN. Neither is sufficient alone,
// and the failure each one covers is the other's blind spot:
//
//	PER ADDRESS   bounds what one network can do in aggregate. It cannot tell
//	              twelve terminals asking once from one terminal asking twelve
//	              times, so on its own it either refuses legitimate installs or
//	              permits one device to spin.
//
//	PER TERMINAL  bounds one device by its own identity, so a site's allowance
//	              cannot be spent by one unit in a loop. It cannot bound an
//	              attacker who varies the serial, because every request then
//	              lands in a fresh bucket -- which is exactly what the address
//	              limiter is still there for.
//
// The device key is absent on a malformed request -- no serial and no token --
// and that case falls through to the address limiter alone. It is not a bypass:
// such a request earns a 400 from the handler a moment later, and inventing a
// bucket for "unidentifiable" would put every one of them in the same one,
// which is a shared allowance an attacker could exhaust on a customer's behalf.
func AnnounceRateLimiter() gin.HandlerFunc {
	byAddress := newRateLimiter(AnnounceRateLimitPerMinute())
	byDevice := newRateLimiter(AnnounceDeviceRateLimitPerMinute())

	return func(c *gin.Context) {
		now := time.Now()

		// The address first, so a flood from one network is refused before the
		// body is parsed for a key.
		if allowed, retryAfter := byAddress.allow(clientAddressKey(c), now); !allowed {
			refuseForRate(c, retryAfter)
			return
		}

		if key := announceDeviceKey(c); key != "" {
			if allowed, retryAfter := byDevice.allow(key, now); !allowed {
				refuseForRate(c, retryAfter)
				return
			}
		}

		c.Next()
	}
}

// announceDeviceKey identifies the TERMINAL behind an announce request, or ""
// when the request names neither.
//
// TWO KEY SPACES, deliberately not unified. A poll carries the announce token
// and no body; an announce carries a serial and may carry no token at all, on
// the very first request a unit ever makes. Trying to resolve them to one
// identity would mean a database lookup inside a rate limiter -- which is the
// work the limiter exists to avoid doing.
//
// The cost of two spaces is that a terminal which both announces and polls draws
// on two buckets. That is correct rather than a leak: they are two different
// operations with different costs, and neither allowance is spendable by
// anybody else.
func announceDeviceKey(c *gin.Context) string {
	// THE TOKEN IS HASHED before it becomes a map key. It is a bearer secret --
	// whoever holds it collects a credential -- and a limiter's key set is the
	// sort of thing that ends up in a debug dump. Sixteen hex characters of
	// SHA-256 is far more than enough to separate buckets.
	if token := strings.TrimSpace(c.GetHeader("X-Announce-Token")); token != "" {
		sum := sha256.Sum256([]byte(token))
		return "announce-token:" + hex.EncodeToString(sum[:])[:16]
	}

	// No token: a first announce. The serial is in the body.
	//
	// ShouldBindBodyWith CACHES the body in the context, so the handler's own
	// bind reads the same bytes rather than an already-drained reader. That is
	// why AnnounceTerminal binds the same way -- see the note there.
	if c.Request == nil || c.Request.Method != http.MethodPost {
		return ""
	}

	// BOUNDED BEFORE IT IS BUFFERED. Reading a body inside a rate limiter is
	// this function's own doing, and an unauthenticated endpoint must not let a
	// caller decide how much memory that costs. An announce body is a couple of
	// hundred bytes; eight kilobytes is generous and finite.
	//
	// The limit rides on the request, so the HANDLER's bind inherits it too --
	// an over-long body fails to parse here, falls through to the address
	// limiter alone, and earns a 400 from the handler a moment later.
	if c.Request.Body != nil {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body,
			maxAnnounceBodyBytes)
	}

	var body struct {
		SerialNumber string `json:"serial_number"`
	}
	if err := c.ShouldBindBodyWith(&body, binding.JSON); err != nil {
		return ""
	}

	serial := strings.TrimSpace(body.SerialNumber)
	if serial == "" {
		return ""
	}

	// Bounded, so a caller cannot grow the bucket map with one enormous key per
	// request. The platform refuses anything over fifteen characters anyway;
	// this only has to stop the map from being a memory sink before it gets
	// there.
	if len(serial) > 64 {
		serial = serial[:64]
	}
	return "announce-serial:" + serial
}

// AdoptRateLimiter limits pairing-code attempts PER SESSION, falling back to the
// client address when there is no session.
//
// KEYED ON THE SESSION rather than the address, which is the opposite of every
// other limiter here and is deliberate. The attacker this bounds is one who
// already holds a valid operator session and is guessing codes with it; keying
// on the address would let them spread attempts across a proxy pool, and keying
// on the company would let one operator's typo budget be spent by a colleague.
func AdoptRateLimiter() gin.HandlerFunc {
	return keyedRateLimiter(AdoptRateLimitPerMinute(), func(c *gin.Context) string {
		if id := c.GetInt64(ContextSessionID); id != 0 {
			return "session:" + strconv.FormatInt(id, 10)
		}
		return clientAddressKey(c)
	})
}

func clientAddressKey(c *gin.Context) string { return c.ClientIP() }

// keyedRateLimiter builds one limiter instance over a caller-chosen bucket key.
//
// Each call returns a SEPARATE limiter with its own map. Sharing one across
// unrelated routes is occasionally what you want -- login and password change do
// it on purpose, so an attacker cannot get a second budget by alternating -- and
// is a mistake everywhere else, so it has to be done by passing one value to two
// routes rather than by accident.
func keyedRateLimiter(perMinute int, key func(*gin.Context) string) gin.HandlerFunc {
	limiter := newRateLimiter(perMinute)

	return func(c *gin.Context) {
		allowed, retryAfter := limiter.allow(key(c), time.Now())
		if !allowed {
			refuseForRate(c, retryAfter)
			return
		}
		c.Next()
	}
}

// newRateLimiter builds one bucket map. Factored out so a route can hold more
// than one -- the announce pair does -- without the composition being done by
// calling one gin.HandlerFunc from inside another, which would run the rest of
// the chain twice.
func newRateLimiter(perMinute int) *rateLimiter {
	limit := float64(perMinute)
	return &rateLimiter{
		buckets:   map[string]*bucket{},
		limit:     limit,
		perSecond: limit / 60,
		lastSweep: time.Now(),
	}
}

// refuseForRate answers 429 and stops the chain.
func refuseForRate(c *gin.Context, retryAfter time.Duration) {
	seconds := int(retryAfter.Seconds())
	if seconds < 1 {
		seconds = 1
	}
	c.Header("Retry-After", strconv.Itoa(seconds))
	c.JSON(http.StatusTooManyRequests, gin.H{
		"error": "Too many attempts, please wait before trying again",
	})
	c.Abort()
}
