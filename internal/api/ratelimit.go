package api

import (
	"container/list"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// RateLimitConfig configures the per-client token-bucket limiter (§G22).
type RateLimitConfig struct {
	// RequestsPerSecond is the steady-state token refill rate per client key.
	RequestsPerSecond float64
	// Burst is the token-bucket depth: the maximum instantaneous number of
	// requests a single client key may make before being throttled.
	Burst int
	// TTL is the idle window after which an unused per-client limiter entry is
	// evicted so the tracking map does not retain long-idle keys.
	TTL time.Duration
	// MaxClients is the HARD upper bound on the number of distinct client keys
	// tracked at once. When it is reached, the least-recently-used entries are
	// evicted to make room, so a distinct-key/IP flood can never grow the map
	// without bound (F2). A non-positive value clamps to a safe default.
	MaxClients int
}

// clientLimiter is one client key's token bucket plus its LRU bookkeeping: the
// last-seen time (for idle reaping) and the client's node in the LRU list (for
// O(1) eviction when the hard size cap is reached).
type clientLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
	// elem is this key's node in RateLimiter.lru; its Value is the key string so
	// evicting the LRU node also removes the matching map entry.
	elem *list.Element
}

// RateLimiter is a per-client token-bucket rate limiter (§G22). It keeps one
// *rate.Limiter per client key so one client's flood cannot starve another
// (per-client isolation). The tracking map is bounded by TWO independent
// mechanisms so it genuinely cannot grow without bound: (1) idle keys are
// reaped after TTL, and (2) a HARD MaxClients cap evicts the least-recently-used
// entry the instant a new key would exceed it — so even a burst of distinct keys
// faster than the reap interval stays bounded (F2). It is safe for concurrent
// use: every map/list mutation is performed under mu.
type RateLimiter struct {
	mu      sync.Mutex
	clients map[string]*clientLimiter
	// lru orders tracked keys most-recently-used (front) to least (back); its
	// Values are the key strings. The back is the eviction victim at the cap.
	lru        *list.List
	maxClients int
	r          rate.Limit
	b          int
	ttl        time.Duration
	lastReap   time.Time
	// now is injectable so eviction is deterministically testable; it defaults
	// to time.Now.
	now func() time.Time
}

// Sensible per-limiter clamps applied when a caller supplies a non-positive
// rate or burst so an ENABLED-but-misconfigured limiter never degrades to
// "reject everything" (a §11.4.201 false-positive refusal) — the guard must
// assert the REAL over-limit condition, not block a correctly-behaving client.
// MaxClients likewise clamps so the map bound is always a positive number.
const (
	defaultRatePerSecond = 50.0
	defaultBurst         = 100
	defaultLimiterTTL    = 10 * time.Minute
	defaultMaxClients    = 100000
)

// NewRateLimiter builds a per-client token-bucket limiter from cfg. Non-positive
// rate/burst/TTL/MaxClients values are clamped to safe defaults rather than
// producing a limiter that throttles every request or an unbounded map.
func NewRateLimiter(cfg RateLimitConfig) *RateLimiter {
	rps := cfg.RequestsPerSecond
	if rps <= 0 {
		rps = defaultRatePerSecond
	}
	burst := cfg.Burst
	if burst <= 0 {
		burst = defaultBurst
	}
	ttl := cfg.TTL
	if ttl <= 0 {
		ttl = defaultLimiterTTL
	}
	maxClients := cfg.MaxClients
	if maxClients <= 0 {
		maxClients = defaultMaxClients
	}
	return &RateLimiter{
		clients:    make(map[string]*clientLimiter),
		lru:        list.New(),
		maxClients: maxClients,
		r:          rate.Limit(rps),
		b:          burst,
		ttl:        ttl,
		lastReap:   time.Now(),
		now:        time.Now,
	}
}

// limiterFor returns the token bucket for key, creating it on first use. It
// opportunistically reaps idle entries, keeps the LRU order current, and — when
// a new key would exceed MaxClients — evicts the least-recently-used entries so
// the map is hard-bounded at all times.
func (rl *RateLimiter) limiterFor(key string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := rl.now()

	// Opportunistic eviction of idle keys, at most once per TTL window, so the
	// map does not retain long-idle entries while staying O(1) amortised.
	if now.Sub(rl.lastReap) >= rl.ttl {
		rl.reapIdleLocked(now)
		rl.lastReap = now
	}

	if cl, ok := rl.clients[key]; ok {
		cl.lastSeen = now
		rl.lru.MoveToFront(cl.elem)
		return cl.limiter
	}

	// New key: enforce the HARD cap BEFORE inserting. Evict the least-recently-
	// used entries until there is room, so len(rl.clients) never exceeds
	// maxClients regardless of how many distinct keys flood in.
	for len(rl.clients) >= rl.maxClients {
		rl.evictOldestLocked()
	}

	elem := rl.lru.PushFront(key)
	cl := &clientLimiter{
		limiter:  rate.NewLimiter(rl.r, rl.b),
		lastSeen: now,
		elem:     elem,
	}
	rl.clients[key] = cl
	return cl.limiter
}

// reapIdleLocked removes every entry idle for longer than ttl. Caller holds mu.
func (rl *RateLimiter) reapIdleLocked(now time.Time) {
	for k, cl := range rl.clients {
		if now.Sub(cl.lastSeen) > rl.ttl {
			rl.lru.Remove(cl.elem)
			delete(rl.clients, k)
		}
	}
}

// evictOldestLocked drops the single least-recently-used entry (the LRU list
// back). Caller holds mu. It is a no-op on an empty map.
func (rl *RateLimiter) evictOldestLocked() {
	back := rl.lru.Back()
	if back == nil {
		return
	}
	key := back.Value.(string)
	rl.lru.Remove(back)
	delete(rl.clients, key)
}

// Allow reports whether a request under key may proceed right now. It is the
// unit-testable core of the middleware.
func (rl *RateLimiter) Allow(key string) bool {
	return rl.limiterFor(key).Allow()
}

// clientKey derives the throttling identity for a request. The pre-auth limiter
// keys ONLY on the resolved socket peer (c.ClientIP()) — NEVER on the raw
// X-API-Key or any other request header. An unauthenticated attacker controls
// every header, so keying on one would let them mint an unlimited number of
// fresh buckets by rotating that header and bypass the limit entirely (F1). The
// router hardens c.ClientIP() to the real TCP peer (SetTrustedProxies(nil) +
// ForwardedByClientIP=false), so a spoofed X-Forwarded-For cannot move the key
// either. In the R15 no-proxy single-node topology the socket peer IS the client
// identity, so socket-keyed throttling gives per-client isolation without
// trusting anything the caller can forge. Per-VALIDATED-key fairness, if ever
// required, belongs AFTER authentication where the identity is proven — never
// here on an unauthenticated header.
func clientKey(c *gin.Context) string {
	return c.ClientIP()
}

// Middleware returns a Gin middleware that rejects a request with HTTP 429 when
// the client's token bucket is empty, and otherwise lets it proceed. It is
// installed BEFORE authentication on the live router so throttling precedes
// credential checks (a flood cannot force the auth path). A throttled response
// carries a Retry-After hint (W3) so a well-behaved client knows when to retry.
func (rl *RateLimiter) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		lim := rl.limiterFor(clientKey(c))
		if !lim.Allow() {
			c.Header("Retry-After", strconv.Itoa(retryAfterSeconds(lim)))
			RespondErrorWithCode(c, http.StatusTooManyRequests, "rate_limited",
				"Too many requests. Slow down and retry after a short delay.")
			c.Abort()
			return
		}
		c.Next()
	}
}

// retryAfterSeconds computes the whole seconds until lim would next admit a
// request. It reads the limiter's OWN reservation delay (never a guessed
// constant, §11.4.6) and cancels the reservation so no token is consumed, then
// rounds up to a minimum of one second (Retry-After is expressed in integer
// seconds and must be positive to be a useful hint).
func retryAfterSeconds(lim *rate.Limiter) int {
	res := lim.Reserve()
	delay := res.Delay()
	res.Cancel()
	secs := int(math.Ceil(delay.Seconds()))
	if secs < 1 {
		secs = 1
	}
	return secs
}
