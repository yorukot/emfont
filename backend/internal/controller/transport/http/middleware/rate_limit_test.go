package middleware

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRateLimiterTokenBucketAndRetryAfter(t *testing.T) {
	clock := &fakeRateLimitClock{now: time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)}
	limiter := mustRateLimiter(t, RateLimitConfig{
		Rate:        2,
		Burst:       2,
		MaxClients:  1,
		IdleTimeout: time.Minute,
		Clock:       clock,
	})

	var calls atomic.Int32
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))

	assertRateLimitStatus(t, handler, "", http.StatusNoContent, "")
	assertRateLimitStatus(t, handler, "", http.StatusNoContent, "")
	recorder := assertRateLimitStatus(t, handler, "", http.StatusTooManyRequests, "1")

	if got := recorder.Header().Get("Content-Type"); got != "application/problem+json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	var problem struct {
		Code   string `json:"code"`
		Status int    `json:"status"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&problem); err != nil {
		t.Fatalf("decode problem response: %v", err)
	}
	if problem.Code != "RATE_LIMITED" || problem.Status != http.StatusTooManyRequests {
		t.Fatalf("problem = %+v", problem)
	}

	clock.Advance(499 * time.Millisecond)
	assertRateLimitStatus(t, handler, "", http.StatusTooManyRequests, "1")
	clock.Advance(time.Millisecond)
	assertRateLimitStatus(t, handler, "", http.StatusNoContent, "")

	if got := calls.Load(); got != 3 {
		t.Fatalf("next handler calls = %d, want 3", got)
	}
}

func TestRateLimiterUsesIndependentInjectedKeys(t *testing.T) {
	clock := &fakeRateLimitClock{now: time.Unix(1_700_000_000, 0)}
	limiter := mustRateLimiter(t, RateLimitConfig{
		Rate:        1,
		Burst:       1,
		MaxClients:  2,
		IdleTimeout: time.Minute,
		Clock:       clock,
		KeyFunc: func(r *http.Request) string {
			return r.Header.Get("X-Client")
		},
	})
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	assertRateLimitStatus(t, handler, "client-a", http.StatusOK, "")
	assertRateLimitStatus(t, handler, "client-a", http.StatusTooManyRequests, "1")
	assertRateLimitStatus(t, handler, "client-b", http.StatusOK, "")
}

func TestRateLimiterEvictsLeastRecentlyUsedClientAtCapacity(t *testing.T) {
	clock := &fakeRateLimitClock{now: time.Unix(1_700_000_000, 0)}
	limiter := mustRateLimiter(t, RateLimitConfig{
		Rate:        1,
		Burst:       1,
		MaxClients:  2,
		IdleTimeout: 10 * time.Second,
		Clock:       clock,
		KeyFunc: func(r *http.Request) string {
			return r.Header.Get("X-Client")
		},
	})
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	assertRateLimitStatus(t, handler, "client-a", http.StatusOK, "")
	assertRateLimitStatus(t, handler, "client-b", http.StatusOK, "")
	assertRateLimitStatus(t, handler, "client-a", http.StatusTooManyRequests, "1")
	assertRateLimitStatus(t, handler, "client-c", http.StatusOK, "")
	if got := trackedRateLimitClients(limiter); got != 2 {
		t.Fatalf("tracked clients = %d, want 2", got)
	}
	if !isTrackedRateLimitClient(limiter, "client-a") || isTrackedRateLimitClient(limiter, "client-b") || !isTrackedRateLimitClient(limiter, "client-c") {
		t.Fatalf("tracked clients do not reflect least-recently-used eviction")
	}
}

func TestRateLimiterEvictsIdleClients(t *testing.T) {
	clock := &fakeRateLimitClock{now: time.Unix(1_700_000_000, 0)}
	limiter := mustRateLimiter(t, RateLimitConfig{
		Rate:        1,
		Burst:       1,
		MaxClients:  2,
		IdleTimeout: 10 * time.Second,
		Clock:       clock,
		KeyFunc: func(r *http.Request) string {
			return r.Header.Get("X-Client")
		},
	})
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	assertRateLimitStatus(t, handler, "client-a", http.StatusOK, "")
	assertRateLimitStatus(t, handler, "client-b", http.StatusOK, "")
	clock.Advance(10 * time.Second)
	assertRateLimitStatus(t, handler, "client-c", http.StatusOK, "")

	if got := trackedRateLimitClients(limiter); got != 1 {
		t.Fatalf("tracked clients after idle eviction = %d, want 1", got)
	}
}

func TestRateLimiterBoundsClientsUnderRotatingIPs(t *testing.T) {
	clock := &fakeRateLimitClock{now: time.Unix(1_700_000_000, 0)}
	limiter := mustRateLimiter(t, RateLimitConfig{
		Rate:        1,
		Burst:       1,
		MaxClients:  3,
		IdleTimeout: time.Hour,
		Clock:       clock,
		KeyFunc:     RemoteIPRateLimitKey,
	})
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for i := range 1_000 {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.RemoteAddr = "198.51." + strconv.Itoa(i/256) + "." + strconv.Itoa(i%256) + ":1234"
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("rotating IP %d status = %d, want %d", i, recorder.Code, http.StatusOK)
		}
		if got := trackedRateLimitClients(limiter); got > 3 {
			t.Fatalf("tracked clients = %d, want at most 3", got)
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "203.0.113.1:1234"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unseen client status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := trackedRateLimitClients(limiter); got != 3 {
		t.Fatalf("tracked clients = %d, want 3", got)
	}
}

func TestRateLimiterGlobalBucketBoundsRotatingIPs(t *testing.T) {
	clock := &fakeRateLimitClock{now: time.Unix(1_700_000_000, 0)}
	limiter := mustRateLimiter(t, RateLimitConfig{
		Rate: 100, Burst: 100, GlobalRequestsPerSecond: 1, GlobalBurst: 2,
		MaxClients: 3, IdleTimeout: time.Hour, Clock: clock, KeyFunc: RemoteIPRateLimitKey,
	})
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for i := range 2 {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.RemoteAddr = "198.51.100." + strconv.Itoa(i+1) + ":1234"
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("request %d status = %d, want %d", i, recorder.Code, http.StatusOK)
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "198.51.100.3:1234"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("global-limited request status = %d, want %d", recorder.Code, http.StatusTooManyRequests)
	}
	if got := recorder.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("global Retry-After = %q, want %q", got, "1")
	}
	if got := trackedRateLimitClients(limiter); got != 2 {
		t.Fatalf("tracked clients = %d, want 2; global rejection must precede client allocation", got)
	}
}

func TestRateLimiterGlobalBucketAllowsRefill(t *testing.T) {
	clock := &fakeRateLimitClock{now: time.Unix(1_700_000_000, 0)}
	limiter := mustRateLimiter(t, RateLimitConfig{
		Rate: 100, Burst: 100, GlobalRequestsPerSecond: 0.5, GlobalBurst: 1,
		MaxClients: 3, IdleTimeout: time.Hour, Clock: clock,
	})
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	assertRateLimitStatus(t, handler, "", http.StatusNoContent, "")
	assertRateLimitStatus(t, handler, "", http.StatusTooManyRequests, "2")
	clock.Advance(2 * time.Second)
	assertRateLimitStatus(t, handler, "", http.StatusNoContent, "")
}

func TestPerClientRejectionsDoNotExhaustGlobalBucket(t *testing.T) {
	clock := &fakeRateLimitClock{now: time.Unix(1_700_000_000, 0)}
	limiter := mustRateLimiter(t, RateLimitConfig{
		Rate: 1, Burst: 1, GlobalRequestsPerSecond: 1, GlobalBurst: 2,
		MaxClients: 2, IdleTimeout: time.Minute, Clock: clock,
		KeyFunc: func(r *http.Request) string { return r.Header.Get("X-Client") },
	})
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	assertRateLimitStatus(t, handler, "noisy", http.StatusNoContent, "")
	for range 10 {
		assertRateLimitStatus(t, handler, "noisy", http.StatusTooManyRequests, "1")
	}
	assertRateLimitStatus(t, handler, "other", http.StatusNoContent, "")
}

func TestRateLimiterGlobalBucketAllowsOnlyBurstUnderConcurrency(t *testing.T) {
	const (
		burst    = 8
		requests = 64
	)
	limiter := mustRateLimiter(t, RateLimitConfig{
		Rate: 100, Burst: 100, GlobalRequestsPerSecond: 1, GlobalBurst: burst,
		MaxClients: 1, IdleTimeout: time.Minute,
		Clock: &fakeRateLimitClock{now: time.Unix(1_700_000_000, 0)},
	})

	var allowed atomic.Int32
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		allowed.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))

	var waitGroup sync.WaitGroup
	waitGroup.Add(requests)
	for range requests {
		go func() {
			defer waitGroup.Done()
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
		}()
	}
	waitGroup.Wait()

	if got := allowed.Load(); got != burst {
		t.Fatalf("allowed requests = %d, want %d", got, burst)
	}
}

func TestRateLimiterAllowsOnlyBurstUnderConcurrency(t *testing.T) {
	const (
		burst    = 8
		requests = 64
	)
	limiter := mustRateLimiter(t, RateLimitConfig{
		Rate:        1,
		Burst:       burst,
		MaxClients:  1,
		IdleTimeout: time.Minute,
		Clock:       &fakeRateLimitClock{now: time.Unix(1_700_000_000, 0)},
	})

	var allowed atomic.Int32
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		allowed.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))

	var waitGroup sync.WaitGroup
	waitGroup.Add(requests)
	for range requests {
		go func() {
			defer waitGroup.Done()
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
		}()
	}
	waitGroup.Wait()

	if got := allowed.Load(); got != burst {
		t.Fatalf("allowed requests = %d, want %d", got, burst)
	}
}

func TestNewRateLimiterRejectsInvalidConfig(t *testing.T) {
	tests := []RateLimitConfig{
		{Rate: 0, Burst: 1},
		{Rate: math.NaN(), Burst: 1},
		{Rate: math.Inf(1), Burst: 1},
		{Rate: 1, Burst: 0},
		{Rate: 1, Burst: 1, MaxClients: -1},
		{Rate: 1, Burst: 1, IdleTimeout: -1},
		{Rate: 1, Burst: 1, GlobalRequestsPerSecond: 1},
		{Rate: 1, Burst: 1, GlobalBurst: 1},
		{Rate: 1, Burst: 1, GlobalRequestsPerSecond: math.NaN(), GlobalBurst: 1},
	}
	for index, cfg := range tests {
		t.Run(strconv.Itoa(index), func(t *testing.T) {
			if _, err := NewRateLimiter(cfg); err == nil {
				t.Fatal("NewRateLimiter() error = nil")
			}
		})
	}
}

func TestRemoteIPRateLimitKey(t *testing.T) {
	tests := map[string]string{
		"192.0.2.10:1234":          "192.0.2.10",
		"[2001:db8::1]:80":         "2001:db8::1",
		"[::ffff:192.0.2.10]:1234": "192.0.2.10",
		"::ffff:192.0.2.10":        "192.0.2.10",
		"192.0.2.10":               "192.0.2.10",
		"not-a-network-address":    "not-a-network-address",
		"":                         "",
	}
	for remoteAddr, want := range tests {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.RemoteAddr = remoteAddr
		if got := RemoteIPRateLimitKey(request); got != want {
			t.Errorf("RemoteIPRateLimitKey(%q) = %q, want %q", remoteAddr, got, want)
		}
	}
}

func TestTrustedProxyIPRateLimitKey(t *testing.T) {
	keyFunc, err := NewTrustedProxyIPRateLimitKey([]netip.Prefix{
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("2001:db8:1234::/48"),
	})
	if err != nil {
		t.Fatalf("NewTrustedProxyIPRateLimitKey: %v", err)
	}

	for name, test := range map[string]struct {
		remote string
		values []string
		want   string
	}{
		"canonical IPv4": {
			remote: "192.0.2.10:1234",
			values: []string{"192.0.2.20"},
			want:   "192.0.2.20",
		},
		"canonical IPv4 mapped IPv6": {
			remote: "[::ffff:192.0.2.10]:1234",
			values: []string{"::ffff:192.0.2.20"},
			want:   "192.0.2.20",
		},
		"trusted IPv6 proxy": {
			remote: "[2001:db8:1234::10]:1234",
			values: []string{"2001:db8:ffff::20"},
			want:   "2001:db8:ffff::20",
		},
		"untrusted direct peer cannot spoof": {
			remote: "198.51.100.10:1234",
			values: []string{"203.0.113.20"},
			want:   "198.51.100.10",
		},
		"rejects forwarded chain": {
			remote: "192.0.2.10:1234",
			values: []string{"198.51.100.20, 192.0.2.20"},
			want:   "192.0.2.10",
		},
		"rejects malformed forwarded address": {
			remote: "192.0.2.10:1234",
			values: []string{"invalid, 2001:db8::1"},
			want:   "192.0.2.10",
		},
		"rejects duplicate header": {
			remote: "192.0.2.10:1234",
			values: []string{"198.51.100.20", "192.0.2.20"},
			want:   "192.0.2.10",
		},
		"rejects address with port": {
			remote: "192.0.2.10:1234",
			values: []string{"198.51.100.20:1234"},
			want:   "192.0.2.10",
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.RemoteAddr = test.remote
			request.Header["X-Forwarded-For"] = test.values
			if got := keyFunc(request); got != test.want {
				t.Fatalf("trusted proxy key = %q, want %q", got, test.want)
			}
		})
	}
}

func TestNewTrustedProxyIPRateLimitKeyRejectsUnsafeCIDRs(t *testing.T) {
	for name, prefixes := range map[string][]netip.Prefix{
		"empty":              nil,
		"invalid":            {{}},
		"host bits":          {netip.MustParsePrefix("10.0.0.1/8")},
		"IPv4 all addresses": {netip.MustParsePrefix("0.0.0.0/0")},
		"IPv6 all addresses": {netip.MustParsePrefix("::/0")},
		"mapped IPv4":        {netip.MustParsePrefix("::ffff:192.0.2.0/120")},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewTrustedProxyIPRateLimitKey(prefixes); err == nil {
				t.Fatal("NewTrustedProxyIPRateLimitKey() error = nil")
			}
		})
	}
}

func TestUntrustedDirectClientCannotRotateXForwardedFor(t *testing.T) {
	keyFunc, err := NewTrustedProxyIPRateLimitKey([]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})
	if err != nil {
		t.Fatalf("NewTrustedProxyIPRateLimitKey: %v", err)
	}
	limiter := mustRateLimiter(t, RateLimitConfig{
		Rate: 1, Burst: 1, MaxClients: 10, IdleTimeout: time.Minute,
		Clock: &fakeRateLimitClock{now: time.Unix(1_700_000_000, 0)}, KeyFunc: keyFunc,
	})
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for index, forwardedFor := range []string{"203.0.113.10", "203.0.113.11"} {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.RemoteAddr = "198.51.100.10:1234"
		request.Header.Set("X-Forwarded-For", forwardedFor)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		want := http.StatusNoContent
		if index == 1 {
			want = http.StatusTooManyRequests
		}
		if response.Code != want {
			t.Fatalf("request %d status = %d, want %d", index, response.Code, want)
		}
	}
}

type fakeRateLimitClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeRateLimitClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeRateLimitClock) Advance(duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(duration)
}

func mustRateLimiter(t *testing.T, cfg RateLimitConfig) *RateLimiter {
	t.Helper()
	limiter, err := NewRateLimiter(cfg)
	if err != nil {
		t.Fatalf("NewRateLimiter(): %v", err)
	}
	return limiter
}

func assertRateLimitStatus(t *testing.T, handler http.Handler, client string, wantStatus int, wantRetryAfter string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	if client != "" {
		request.Header.Set("X-Client", client)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != wantStatus {
		t.Fatalf("status = %d, want %d", recorder.Code, wantStatus)
	}
	if got := recorder.Header().Get("Retry-After"); got != wantRetryAfter {
		t.Fatalf("Retry-After = %q, want %q", got, wantRetryAfter)
	}
	return recorder
}

func trackedRateLimitClients(limiter *RateLimiter) int {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	return len(limiter.clients)
}

func isTrackedRateLimitClient(limiter *RateLimiter, key string) bool {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	_, ok := limiter.clients[key]
	return ok
}
