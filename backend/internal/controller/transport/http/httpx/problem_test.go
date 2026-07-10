package httpx_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/emfont/emfont/backend/internal/controller/transport/http/httpx"
	"github.com/emfont/emfont/backend/internal/controller/transport/http/middleware"
)

func TestWriteProblemIncludesCodeAndRequestID(t *testing.T) {
	handler := middleware.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = httpx.WriteProblem(w, r, httpx.NotFoundCode(httpx.CodeFontNotFound, "font not found"))
	}))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/missing", nil)
	request.Header.Set(middleware.RequestIDHeader, "request-123")
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
	if got := recorder.Header().Get("Content-Type"); got != httpx.ContentTypeProblem {
		t.Fatalf("content type = %q, want %q", got, httpx.ContentTypeProblem)
	}
	var problem httpx.Problem
	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if problem.Code != httpx.CodeFontNotFound || problem.RequestID != "request-123" {
		t.Fatalf("problem = %#v", problem)
	}
}

func TestValidationProblemIncludesFieldDetails(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/g/demo", nil)
	err := httpx.UnprocessableEntity("invalid font request", httpx.ErrorDetail{
		Code: httpx.FieldCodeRequired, Message: "words is required", Location: "body.words",
	})
	_ = httpx.WriteProblem(recorder, request, err)

	var problem httpx.Problem
	if decodeErr := json.Unmarshal(recorder.Body.Bytes(), &problem); decodeErr != nil {
		t.Fatalf("decode problem: %v", decodeErr)
	}
	if problem.Code != httpx.CodeValidationFailed || len(problem.Errors) != 1 {
		t.Fatalf("problem = %#v", problem)
	}
}
