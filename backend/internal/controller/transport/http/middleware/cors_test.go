package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCORSHandlesAllowedPreflight(t *testing.T) {
	handler := CORS(CORSConfig{AllowedOrigins: []string{"https://app.example"}})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("preflight reached application handler")
	}))
	request := httptest.NewRequest(http.MethodOptions, "/g/font", nil)
	request.Header.Set("Origin", "https://app.example")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	request.Header.Set("Access-Control-Request-Headers", "content-type")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example" {
		t.Fatalf("allow origin = %q", got)
	}
	if got := response.Header().Get("Access-Control-Allow-Headers"); got != "Content-Type, X-Request-ID" {
		t.Fatalf("allow headers = %q", got)
	}
}

func TestCORSRejectsUnlistedPreflight(t *testing.T) {
	handler := CORS(CORSConfig{AllowedOrigins: []string{"https://app.example"}})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	request := httptest.NewRequest(http.MethodOptions, "/g/font", nil)
	request.Header.Set("Origin", "https://untrusted.example")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("allow origin = %q, want empty", got)
	}
}

func TestCORSWildcardDoesNotAllowAuthorizationHeader(t *testing.T) {
	handler := CORS(CORSConfig{AllowedOrigins: []string{"*"}})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	request := httptest.NewRequest(http.MethodOptions, "/metrics", nil)
	request.Header.Set("Origin", "https://app.example")
	request.Header.Set("Access-Control-Request-Method", http.MethodGet)
	request.Header.Set("Access-Control-Request-Headers", "authorization")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
	if got := response.Header().Get("Access-Control-Allow-Headers"); got == "authorization" {
		t.Fatalf("authorization was allowed")
	}
}

func TestCORSExposesResponseHeaders(t *testing.T) {
	handler := CORS(CORSConfig{AllowedOrigins: []string{"https://app.example"}})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	request := httptest.NewRequest(http.MethodGet, "/list", nil)
	request.Header.Set("Origin", "https://app.example")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	exposed := make(map[string]bool)
	for _, header := range strings.Split(response.Header().Get("Access-Control-Expose-Headers"), ",") {
		exposed[http.CanonicalHeaderKey(strings.TrimSpace(header))] = true
	}
	for _, want := range []string{"X-Request-ID", "X-Next-Cursor", "Retry-After", "Link", "Allow"} {
		if !exposed[http.CanonicalHeaderKey(want)] {
			t.Errorf("Access-Control-Expose-Headers does not include %q: %q", want, response.Header().Get("Access-Control-Expose-Headers"))
		}
	}
}
