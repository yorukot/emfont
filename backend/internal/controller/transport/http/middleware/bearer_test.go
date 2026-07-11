package middleware

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestBearerToken(t *testing.T) {
	handler := BearerToken("secret-token")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for name, testCase := range map[string]struct {
		header string
		status int
	}{
		"missing":      {},
		"wrong scheme": {header: "Basic secret-token"},
		"wrong token":  {header: "Bearer wrong"},
		"valid":        {header: "Bearer secret-token", status: http.StatusNoContent},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			request.Header.Set("Authorization", testCase.header)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			wantStatus := testCase.status
			if wantStatus == 0 {
				wantStatus = http.StatusUnauthorized
			}
			if recorder.Code != wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, wantStatus)
			}
		})
	}
}

func TestBearerTokenWithRateLimitsKeepsRejectedFloodOutOfAuthenticatedBucket(t *testing.T) {
	rejected, err := NewRateLimiter(RateLimitConfig{Rate: 0.001, Burst: 1, MaxClients: 1, IdleTimeout: time.Hour})
	if err != nil {
		t.Fatalf("rejected limiter: %v", err)
	}
	authenticated, err := NewRateLimiter(RateLimitConfig{Rate: 0.001, Burst: 1, MaxClients: 1, IdleTimeout: time.Hour})
	if err != nil {
		t.Fatalf("authenticated limiter: %v", err)
	}
	handler := BearerTokenWithRateLimits(
		"secret-token",
		authenticated.Middleware,
		rejected.Middleware,
	)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	const attackers = 64
	var wg sync.WaitGroup
	wg.Add(attackers)
	for range attackers {
		go func() {
			defer wg.Done()
			request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			request.Header.Set("Authorization", "Bearer wrong")
			handler.ServeHTTP(httptest.NewRecorder(), request)
		}()
	}
	wg.Wait()

	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("authenticated status after rejected flood = %d, want %d", response.Code, http.StatusNoContent)
	}
}

func TestBearerTokenFailsClosedWhenEmpty(t *testing.T) {
	handler := BearerToken("")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	request.Header.Set("Authorization", "Bearer anything")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}
