package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestIDAcceptsBoundedSafeValue(t *testing.T) {
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := RequestIDFromContext(r.Context()); got != "client-123:abc" {
			t.Fatalf("request ID in context = %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set(RequestIDHeader, "client-123:abc")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if got := recorder.Header().Get(RequestIDHeader); got != "client-123:abc" {
		t.Fatalf("response request ID = %q", got)
	}
}

func TestRequestIDReplacesUnsafeOrOversizedValue(t *testing.T) {
	for name, supplied := range map[string]string{
		"oversized": strings.Repeat("a", maxRequestIDLength+1),
		"control":   "safe\nforged-log-line",
		"spaces":    "contains spaces",
	} {
		t.Run(name, func(t *testing.T) {
			handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				generated := RequestIDFromContext(r.Context())
				if generated == supplied || !validRequestID(generated) {
					t.Fatalf("generated request ID = %q", generated)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Header.Set(RequestIDHeader, supplied)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
		})
	}
}
