package middleware

import (
	"container/heap"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"
)

const (
	DefaultRateLimitMaxClients  = 10_000
	DefaultRateLimitIdleTimeout = 10 * time.Minute
)

const globalRateLimitKey = "global"

// RateLimitKeyFunc groups requests into independently limited buckets.
type RateLimitKeyFunc func(*http.Request) string

// RateLimitClock permits deterministic tests without sleeping.
type RateLimitClock interface {
	Now() time.Time
}

type RateLimitConfig struct {
	// Rate is the number of tokens replenished per second.
	Rate float64
	// Burst is the bucket capacity. Each request consumes one token.
	Burst int
	// GlobalRequestsPerSecond and GlobalBurst configure an optional
	// process-wide bucket checked before the bucket selected by KeyFunc. Leave
	// both at zero to omit it for isolated middleware use.
	GlobalRequestsPerSecond float64
	GlobalBurst             int

	// KeyFunc defaults to GlobalRateLimitKey. Use RemoteIPRateLimitKey or an
	// application-specific function for per-client limits.
	KeyFunc RateLimitKeyFunc

	// MaxClients bounds the number of tracked keys. Zero uses the default.
	MaxClients int
	// IdleTimeout controls when an inactive key is forgotten. Zero uses the
	// default. Expired entries are removed opportunistically on requests.
	IdleTimeout time.Duration

	Clock RateLimitClock
}

// RateLimiter is a concurrency-safe token-bucket HTTP middleware.
type RateLimiter struct {
	rate                    float64
	burst                   float64
	globalRequestsPerSecond float64
	globalBurst             float64
	globalTokens            float64
	globalLastRefill        time.Time
	keyFunc                 RateLimitKeyFunc
	maxClients              int
	idleTimeout             time.Duration
	clock                   RateLimitClock

	mu       sync.Mutex
	lastNow  time.Time
	clients  map[string]*rateLimitBucket
	idleHeap rateLimitBucketHeap
	seen     uint64
}

type systemRateLimitClock struct{}

func (systemRateLimitClock) Now() time.Time { return time.Now() }

// NewRateLimiter validates cfg and creates a bounded token-bucket limiter.
func NewRateLimiter(cfg RateLimitConfig) (*RateLimiter, error) {
	if cfg.Rate <= 0 || math.IsNaN(cfg.Rate) || math.IsInf(cfg.Rate, 0) {
		return nil, fmt.Errorf("rate must be finite and greater than zero")
	}
	if cfg.Burst <= 0 {
		return nil, fmt.Errorf("burst must be greater than zero")
	}
	if cfg.GlobalRequestsPerSecond != 0 || cfg.GlobalBurst != 0 {
		if cfg.GlobalRequestsPerSecond <= 0 || math.IsNaN(cfg.GlobalRequestsPerSecond) || math.IsInf(cfg.GlobalRequestsPerSecond, 0) {
			return nil, fmt.Errorf("global requests per second must be finite and greater than zero")
		}
		if cfg.GlobalBurst <= 0 {
			return nil, fmt.Errorf("global burst must be greater than zero")
		}
	}
	if cfg.MaxClients < 0 {
		return nil, fmt.Errorf("max clients must not be negative")
	}
	if cfg.IdleTimeout < 0 {
		return nil, fmt.Errorf("idle timeout must not be negative")
	}

	if cfg.KeyFunc == nil {
		cfg.KeyFunc = GlobalRateLimitKey
	}
	if cfg.MaxClients == 0 {
		cfg.MaxClients = DefaultRateLimitMaxClients
	}
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = DefaultRateLimitIdleTimeout
	}
	if cfg.Clock == nil {
		cfg.Clock = systemRateLimitClock{}
	}

	return &RateLimiter{
		rate:                    cfg.Rate,
		burst:                   float64(cfg.Burst),
		globalRequestsPerSecond: cfg.GlobalRequestsPerSecond,
		globalBurst:             float64(cfg.GlobalBurst),
		globalTokens:            float64(cfg.GlobalBurst),
		keyFunc:                 cfg.KeyFunc,
		maxClients:              cfg.MaxClients,
		idleTimeout:             cfg.IdleTimeout,
		clock:                   cfg.Clock,
		clients:                 make(map[string]*rateLimitBucket),
	}, nil
}

// RateLimit creates middleware suitable for router.Use. Invalid configuration
// panics so a broken limiter cannot silently fail open during startup.
func RateLimit(cfg RateLimitConfig) func(http.Handler) http.Handler {
	limiter, err := NewRateLimiter(cfg)
	if err != nil {
		panic("middleware.RateLimit: " + err.Error())
	}
	return limiter.Middleware
}

// Middleware wraps next with rate limiting.
func (l *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowed, retryAfter := l.allow(l.keyFunc(r))
		if !allowed {
			w.Header().Set("Retry-After", formatRetryAfter(retryAfter))
			WriteProblem(w, r, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// GlobalRateLimitKey places all requests in one shared bucket.
func GlobalRateLimitKey(*http.Request) string { return globalRateLimitKey }

// RemoteIPRateLimitKey groups requests by the canonical direct peer IP. It
// deliberately does not trust proxy headers.
func RemoteIPRateLimitKey(r *http.Request) string {
	if r == nil {
		return ""
	}
	if peer, ok := directPeerIP(r); ok {
		return peer.String()
	}
	return r.RemoteAddr
}

// NewTrustedProxyIPRateLimitKey returns a key function that accepts a single
// X-Forwarded-For IP only when the direct peer is in trustedProxyCIDRs.
func NewTrustedProxyIPRateLimitKey(trustedProxyCIDRs []netip.Prefix) (RateLimitKeyFunc, error) {
	if len(trustedProxyCIDRs) == 0 {
		return nil, fmt.Errorf("trusted proxy CIDRs must not be empty")
	}

	trusted := make([]netip.Prefix, len(trustedProxyCIDRs))
	copy(trusted, trustedProxyCIDRs)
	for _, prefix := range trusted {
		if !prefix.IsValid() {
			return nil, fmt.Errorf("trusted proxy CIDR is invalid")
		}
		if prefix.Addr().Is4In6() || prefix != prefix.Masked() {
			return nil, fmt.Errorf("trusted proxy CIDR %q is not canonical", prefix)
		}
		if prefix.Bits() == 0 {
			return nil, fmt.Errorf("trusted proxy CIDR %q is overly broad", prefix)
		}
	}

	return func(r *http.Request) string {
		peer, ok := directPeerIP(r)
		if !ok || !prefixesContain(trusted, peer) {
			return RemoteIPRateLimitKey(r)
		}

		values := r.Header.Values("X-Forwarded-For")
		if len(values) == 1 {
			if key, ok := canonicalIP(values[0]); ok {
				return key
			}
		}
		return peer.String()
	}, nil
}

func directPeerIP(r *http.Request) (netip.Addr, bool) {
	if r == nil {
		return netip.Addr{}, false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return parseCanonicalIP(host)
	}
	return parseCanonicalIP(r.RemoteAddr)
}

func prefixesContain(prefixes []netip.Prefix, address netip.Addr) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func canonicalIP(value string) (string, bool) {
	addr, ok := parseCanonicalIP(value)
	if !ok {
		return "", false
	}
	return addr.String(), true
}

func parseCanonicalIP(value string) (netip.Addr, bool) {
	addr, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}

func (l *RateLimiter) allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.clock.Now()
	if now.Before(l.lastNow) {
		now = l.lastNow
	} else {
		l.lastNow = now
	}
	if l.globalBurst > 0 {
		elapsed := now.Sub(l.globalLastRefill)
		if elapsed > 0 {
			l.globalTokens = math.Min(l.globalBurst, l.globalTokens+elapsed.Seconds()*l.globalRequestsPerSecond)
		}
		l.globalLastRefill = now
		if l.globalTokens < 1 {
			return false, durationUntilToken(l.globalTokens, l.globalRequestsPerSecond)
		}
		l.globalTokens--
	}

	l.evictIdle(now)
	bucket, ok := l.clients[key]
	if !ok {
		if len(l.clients) >= l.maxClients {
			l.evictOldest()
		}

		bucket = &rateLimitBucket{
			key:        key,
			tokens:     l.burst - 1,
			lastRefill: now,
			lastSeen:   now,
			seen:       l.nextSeen(),
		}
		l.clients[key] = bucket
		heap.Push(&l.idleHeap, bucket)
		return true, 0
	}

	elapsed := now.Sub(bucket.lastRefill)
	if elapsed > 0 {
		bucket.tokens = math.Min(l.burst, bucket.tokens+elapsed.Seconds()*l.rate)
		bucket.lastRefill = now
	}
	bucket.lastSeen = now
	bucket.seen = l.nextSeen()
	heap.Fix(&l.idleHeap, bucket.heapIndex)

	if bucket.tokens >= 1 {
		bucket.tokens--
		return true, 0
	}
	if l.globalBurst > 0 {
		l.globalTokens = math.Min(l.globalBurst, l.globalTokens+1)
	}
	return false, durationUntilToken(bucket.tokens, l.rate)
}

func (l *RateLimiter) nextSeen() uint64 {
	l.seen++
	return l.seen
}

func (l *RateLimiter) evictIdle(now time.Time) {
	for len(l.idleHeap) > 0 {
		oldest := l.idleHeap[0]
		if now.Sub(oldest.lastSeen) < l.idleTimeout {
			return
		}
		removed := heap.Pop(&l.idleHeap).(*rateLimitBucket)
		delete(l.clients, removed.key)
	}
}

func (l *RateLimiter) evictOldest() {
	removed := heap.Pop(&l.idleHeap).(*rateLimitBucket)
	delete(l.clients, removed.key)
}

func durationUntilToken(tokens, rate float64) time.Duration {
	missing := 1 - tokens
	if missing <= 0 {
		return 0
	}

	nanoseconds := missing / rate * float64(time.Second)
	if math.IsInf(nanoseconds, 1) || nanoseconds >= float64(math.MaxInt64) {
		return time.Duration(math.MaxInt64)
	}
	wait := time.Duration(math.Ceil(nanoseconds))
	if wait <= 0 {
		return time.Nanosecond
	}
	return wait
}

func formatRetryAfter(wait time.Duration) string {
	seconds := wait / time.Second
	if wait%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		seconds = 1
	}
	return strconv.FormatInt(int64(seconds), 10)
}

type rateLimitBucket struct {
	key        string
	tokens     float64
	lastRefill time.Time
	lastSeen   time.Time
	seen       uint64
	heapIndex  int
}

type rateLimitBucketHeap []*rateLimitBucket

func (h rateLimitBucketHeap) Len() int { return len(h) }

func (h rateLimitBucketHeap) Less(i, j int) bool {
	if h[i].lastSeen.Equal(h[j].lastSeen) {
		return h[i].seen < h[j].seen
	}
	return h[i].lastSeen.Before(h[j].lastSeen)
}

func (h rateLimitBucketHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].heapIndex = i
	h[j].heapIndex = j
}

func (h *rateLimitBucketHeap) Push(value any) {
	bucket := value.(*rateLimitBucket)
	bucket.heapIndex = len(*h)
	*h = append(*h, bucket)
}

func (h *rateLimitBucketHeap) Pop() any {
	old := *h
	last := len(old) - 1
	bucket := old[last]
	old[last] = nil
	bucket.heapIndex = -1
	*h = old[:last]
	return bucket
}
