package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestRecoveryLogsThroughBaseLoggerBeforeRequestLogging(t *testing.T) {
	core, logs := observer.New(zap.ErrorLevel)
	handler := RequestID(Recovery(zap.New(core))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("internal panic detail")
	})))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/system", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	if strings.Contains(response.Body.String(), "internal panic detail") {
		t.Fatalf("response leaked panic detail: %s", response.Body.String())
	}
	entries := logs.FilterMessage("panic recovered").All()
	if len(entries) != 1 {
		t.Fatalf("panic log entries = %d, want 1", len(entries))
	}
	if requestID, _ := entries[0].ContextMap()["request_id"].(string); requestID == "" {
		t.Fatalf("panic log is missing request ID: %#v", entries[0].ContextMap())
	}
}
