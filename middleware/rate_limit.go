package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"access-terminal-cloud-api/database"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
)

// Rate limiting.
//
// ---------------------------------------------------------------------------
// WHAT CHANGED, AND WHAT DELIBERATELY DID NOT
// ---------------------------------------------------------------------------
//
// This file used to hold ONE implementation: an in-process token bucket in a Go
// map. Its own comment recorded the limit -- "with more than one instance the
// effective limit multiplies by the instance count" -- and that is SEC-09.
//
// There are now TWO implementations behind one interface:
//
//	sharedStore   database.PostgresRateStore. One row per (subject, class),
//	              refilled and spent in a single statement, so the allowance
//	              holds however many instances are serving.
//
//	memoryStore   the original map. Still here, still correct for one instance,
//	              and still used -- see the announce exception below.
//
// LOGIN, CLAIM, PLATFORM LOGIN AND ADOPT MOVE TO THE SHARED STORE. They are the
// endpoints where an attacker gets unlimited cheap attempts against a
// human-chosen secret or a short code, which is the case a per-instance
// allowance is worst for.
//
// THE ANNOUNCE LIMITER STAYS IN PROCESS, AND THAT IS A DELIBERATE EXCEPTION.
// Two reasons, both load-bearing:
//
//   - This file's own argument against a lookup inside a limiter still holds.
//     Resolving a terminal's identity here "would mean a database lookup inside
//     a rate limiter -- which is the work the limiter exists to avoid doing",
//     and a shared store makes EVERY announce a database write, not just the
//     ones that need identity.
//   - The volume is the wrong shape for it. A twenty-five terminal site polls
//     twelve times a minute each: about three hundred limiter writes a minute
//     before the handler has done any work of its own, on one instance with a
//     ten-connection pool.
//
//   What is given up is real and bounded: with two instances the announce
//   allowance doubles. The endpoint it protects grants nothing -- an
//   announcement has no company and becomes a credential only through an ADMIN
//   typing a code off the physical unit -- and the pairing code itself is
//   guarded by AdoptRateLimiter, which IS shared. When the announce path is
//   moved, it should be moved onto a store that is not the request database.
//
// TWO LAYERS ARE UNCHANGED. The per-account lockout in users.locked_until still
// lives in the database and still survives restarts; nothing here replaces it.

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
	// up to this ceiling. PurgeAnnouncements is the backstop.
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
	// its firmware already does correctly on a 429.
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

	// rateLimitIdleTTL is how long an unused in-process bucket is kept. An
	// attacker rotating source addresses would otherwise grow the map without
	// bound. The shared store's equivalent is PruneIdleRateBuckets, registered
	// as a maintenance task.
	rateLimitIdleTTL = 10 * time.Minute

	// rateLimitSweepEvery bounds how often the map is swept, so a burst of
	// distinct addresses does not turn every request into a full scan.
	rateLimitSweepEvery = time.Minute

	// rateStoreTimeout bounds a shared-store decision. A limiter that can block
	// a request indefinitely is a worse availability problem than the one it
	// was added to solve.
	rateStoreTimeout = 2 * time.Second
)

// Bucket subject kinds, as stored in api_rate_buckets.subject_type.
const (
	RateSubjectAddress    = "address"
	RateSubjectCredential = "credential"
	RateSubjectCompany    = "company"
)

// Endpoint classes. Three separate credential-endpoint classes rather than one,
// because router.go builds three separate limiters today and collapsing them
// would be a behaviour change: an installer retrying a mistyped claim code must
// not exhaust the allowance an operator needs to sign in and fix it.
const (
	RateClassLogin         = "login"
	RateClassClaim         = "claim"
	RateClassPlatformLogin = "platform_login"
	RateClassAdopt         = "adopt"
)

// RateStore decides whether a subject may spend a token.
//
// Declared here, in the consumer, rather than in database/: that is what lets
// the in-process implementation below satisfy it without the database package
// knowing anything about middleware, and what lets a test substitute a store
// that fails on demand.
type RateStore interface {
	Allow(ctx context.Context, subjectType, subjectKey, class string,
		capacity, perSecond float64) (database.RateDecision, error)
}

var (
	rateStoreMu sync.RWMutex
	// sharedStore is nil until UseSharedRateStore is called. Nil means every
	// limiter falls back to its in-process bucket, which is the correct
	// behaviour for a process that has no database -- and is announced, so a
	// deployment cannot silently run with per-instance allowances.
	sharedStore RateStore
)

// UseSharedRateStore installs the process-wide shared limiter store.
//
// Called from main after the database connects, and from the test harness. A
// deployment that does not call it keeps the previous behaviour exactly.
func UseSharedRateStore(store RateStore) {
	rateStoreMu.Lock()
	defer rateStoreMu.Unlock()
	sharedStore = store
	log.Println("Rate limiting: shared store active for login, claim, " +
		"platform login and adopt (announce remains in-process by design)")
}

// ClearSharedRateStore reverts to in-process limiting. For tests.
func ClearSharedRateStore() {
	rateStoreMu.Lock()
	defer rateStoreMu.Unlock()
	sharedStore = nil
}

func currentRateStore() RateStore {
	rateStoreMu.RLock()
	defer rateStoreMu.RUnlock()
	return sharedStore
}

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

// ---------------------------------------------------------------------------
// In-process store
// ---------------------------------------------------------------------------

// bucket is one client's allowance, refilled continuously rather than reset on a
// boundary. A fixed window would let a caller spend a full allowance at the end
// of one window and another at the start of the next, which is twice the
// intended rate at exactly the moment it matters.
type bucket struct {
	tokens   float64
	lastFill time.Time
}

// memoryStore is the original in-process limiter, now behind the interface.
//
// One instance per limiter, as before: a shared map across unrelated routes
// would be a shared allowance, and that has to be done by passing one value to
// two routes rather than by accident.
type memoryStore struct {
	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

func newMemoryStore() *memoryStore {
	return &memoryStore{buckets: map[string]*bucket{}, lastSweep: time.Now()}
}

// Allow satisfies RateStore. subjectType is part of the key so one store can
// serve more than one kind of subject without them colliding.
func (m *memoryStore) Allow(_ context.Context, subjectType, subjectKey, class string,
	capacity, perSecond float64) (database.RateDecision, error) {

	if subjectKey == "" {
		return database.RateDecision{Allowed: true, Remaining: capacity}, nil
	}

	key := subjectType + "\x00" + class + "\x00" + subjectKey
	now := time.Now()

	m.mu.Lock()
	defer m.mu.Unlock()

	if now.Sub(m.lastSweep) >= rateLimitSweepEvery {
		for k, b := range m.buckets {
			if now.Sub(b.lastFill) > rateLimitIdleTTL {
				delete(m.buckets, k)
			}
		}
		m.lastSweep = now
	}

	b, exists := m.buckets[key]
	if !exists {
		b = &bucket{tokens: capacity, lastFill: now}
		m.buckets[key] = b
	}

	b.tokens += now.Sub(b.lastFill).Seconds() * perSecond
	if b.tokens > capacity {
		b.tokens = capacity
	}
	b.lastFill = now

	if b.tokens < 1 {
		deficit := (1 - b.tokens) / perSecond
		return database.RateDecision{
			Allowed:    false,
			RetryAfter: time.Duration(deficit * float64(time.Second)),
		}, nil
	}

	b.tokens--
	return database.RateDecision{Allowed: true, Remaining: b.tokens}, nil
}

// ---------------------------------------------------------------------------
// Limiter middleware
// ---------------------------------------------------------------------------

// limiter is one mounted allowance.
type limiter struct {
	class     string
	perMinute int
	// subject resolves the bucket key for a request. Returning "" means the
	// request carries no identity this limiter can bound; the request is
	// permitted, because inventing a bucket for "unidentifiable" would put every
	// one of them in the same one -- a shared allowance an attacker could
	// exhaust on a customer's behalf.
	subject func(*gin.Context) (kind, key string)
	// fallback is used when no shared store is installed.
	fallback *memoryStore
	// shared says whether this limiter should use the shared store when one is
	// available. False pins a limiter in process regardless -- see the announce
	// exception at the top of this file.
	shared bool
}

func (l *limiter) capacity() float64  { return float64(l.perMinute) }
func (l *limiter) perSecond() float64 { return float64(l.perMinute) / 60 }

func (l *limiter) store() RateStore {
	if l.shared {
		if store := currentRateStore(); store != nil {
			return store
		}
	}
	return l.fallback
}

// handle applies the limiter, reporting whether the chain should continue.
func (l *limiter) handle(c *gin.Context) bool {
	kind, key := l.subject(c)
	if key == "" {
		return true
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), rateStoreTimeout)
	defer cancel()

	decision, err := l.store().Allow(ctx, kind, key, l.class, l.capacity(), l.perSecond())
	if err != nil {
		// FAIL CLOSED. A limiter that cannot answer must not be read as "yes":
		// the store is the database, and a database that cannot serve this
		// cannot serve the request behind it either. 503 rather than 500,
		// because it is a dependency being unavailable rather than a fault in
		// handling the request, and it carries a Retry-After.
		log.Printf("request_id=%s error op=\"rate limit\" class=%s: %v",
			RequestID(c), l.class, err)
		c.Header("Retry-After", "5")
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "Rate limiting is temporarily unavailable, please retry",
		})
		c.Abort()
		return false
	}

	if !decision.Allowed {
		refuseForRate(c, decision.RetryAfter)
		return false
	}
	return true
}

// newLimiter builds one allowance over a caller-chosen subject.
func newLimiter(class string, perMinute int, shared bool,
	subject func(*gin.Context) (string, string)) *limiter {
	return &limiter{
		class:     class,
		perMinute: perMinute,
		subject:   subject,
		fallback:  newMemoryStore(),
		shared:    shared,
	}
}

// LoginRateLimiter limits credential attempts per client address.
//
// Returns a single limiter to share across the routes it is mounted on, so
// login, registration, password change and the handover routes draw from one
// allowance rather than four: an attacker must not get a second budget by
// alternating between them.
//
// SHARED STORE. This is the endpoint an attacker gets unlimited cheap attempts
// against a human-chosen secret on, so a per-instance allowance is exactly the
// wrong shape for it.
//
// The address comes from c.ClientIP(), which is only as trustworthy as the proxy
// configuration -- configureTrustedProxies in router.go is what makes it so, and
// without it a caller could pick its own apparent address and its own bucket.
func LoginRateLimiter() gin.HandlerFunc {
	return mount(newLimiter(RateClassLogin, LoginRateLimitPerMinute(), true, addressSubject))
}

// ClaimRateLimiter limits claim-code redemption per client address.
//
// ITS OWN CLASS, never shared with login, and the reason the route already
// states: an installer standing at a door retrying a mistyped code must not
// exhaust the allowance an operator needs to sign in and fix it, and an attacker
// hammering login must not lock out a commissioning engineer.
func ClaimRateLimiter() gin.HandlerFunc {
	return mount(newLimiter(RateClassClaim, LoginRateLimitPerMinute(), true, addressSubject))
}

// PlatformLoginRateLimiter limits platform administrator sign-in.
//
// Its own class rather than the operator one: a platform administrator locked
// out because an attacker was hammering a tenant's login is an outage of the
// surface that fixes outages.
func PlatformLoginRateLimiter() gin.HandlerFunc {
	return mount(newLimiter(RateClassPlatformLogin, LoginRateLimitPerMinute(), true, addressSubject))
}

// AdoptRateLimiter limits pairing-code attempts PER SESSION, falling back to the
// client address when there is no session.
//
// KEYED ON THE SESSION rather than the address, which is the opposite of every
// other limiter here and is deliberate. The attacker this bounds is one who
// already holds a valid operator session and is guessing codes with it; keying
// on the address would let them spread attempts across a proxy pool, and keying
// on the company would let one operator's typo budget be spent by a colleague.
//
// SHARED STORE, because this is the limiter that actually protects the pairing
// code and a per-instance allowance would multiply the attempts available.
func AdoptRateLimiter() gin.HandlerFunc {
	return mount(newLimiter(RateClassAdopt, AdoptRateLimitPerMinute(), true,
		func(c *gin.Context) (string, string) {
			if id := c.GetInt64(ContextSessionID); id != 0 {
				return RateSubjectCredential, "session:" + strconv.FormatInt(id, 10)
			}
			return addressSubject(c)
		}))
}

// AnnounceRateLimiter limits the device provisioning endpoints.
//
// TWO BUCKETS, BOTH OF WHICH MUST HAVE A TOKEN, and the failure each one covers
// is the other's blind spot:
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
// BOTH ARE IN-PROCESS, DELIBERATELY. See the exception recorded at the top of
// this file: a shared store would make every announce poll a database write, on
// the one path in this API whose whole design avoids a lookup, and a
// twenty-five terminal site generates about three hundred of them a minute.
func AnnounceRateLimiter() gin.HandlerFunc {
	byAddress := newLimiter("announce_address", AnnounceRateLimitPerMinute(), false, addressSubject)
	byDevice := newLimiter("announce_device", AnnounceDeviceRateLimitPerMinute(), false,
		func(c *gin.Context) (string, string) { return RateSubjectAddress, announceDeviceKey(c) })

	return func(c *gin.Context) {
		// The address first, so a flood from one network is refused before the
		// body is parsed for a key.
		if !byAddress.handle(c) {
			return
		}
		if !byDevice.handle(c) {
			return
		}
		c.Next()
	}
}

func mount(l *limiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !l.handle(c) {
			return
		}
		c.Next()
	}
}

func addressSubject(c *gin.Context) (string, string) {
	return RateSubjectAddress, c.ClientIP()
}

// announceDeviceKey identifies the TERMINAL behind an announce request, or ""
// when the request names neither.
//
// TWO KEY SPACES, deliberately not unified. A poll carries the announce token
// and no body; an announce carries a serial and may carry no token at all, on
// the very first request a unit ever makes. Trying to resolve them to one
// identity would mean a database lookup inside a rate limiter -- which is the
// work the limiter exists to avoid doing, and is the reason this path is the one
// that stays in process.
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
