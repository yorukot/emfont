package font

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appfont "github.com/emfont/emfont/backend/internal/controller/application/font"
	"github.com/emfont/emfont/backend/internal/controller/logger"
	"github.com/emfont/emfont/backend/internal/controller/transport/http/httpx"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestGenerateRequiresApplicationJSON(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		wantStatus  int
		wantCode    string
	}{
		{name: "missing", wantStatus: http.StatusUnsupportedMediaType, wantCode: httpx.CodeUnsupportedMediaType},
		{name: "unsupported", contentType: "text/plain", wantStatus: http.StatusUnsupportedMediaType, wantCode: httpx.CodeUnsupportedMediaType},
		{name: "malformed", contentType: "application/json; charset", wantStatus: http.StatusUnsupportedMediaType, wantCode: httpx.CodeUnsupportedMediaType},
		{name: "json", contentType: "application/json", wantStatus: http.StatusBadRequest, wantCode: httpx.CodeBadRequest},
		{name: "json with parameters", contentType: "application/json; charset=utf-8", wantStatus: http.StatusBadRequest, wantCode: httpx.CodeBadRequest},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := serveGenerate(test.contentType, `{`)
			problem := requireProblem(t, recorder, test.wantStatus)
			if problem.Code != test.wantCode {
				t.Fatalf("problem code = %q, want %q", problem.Code, test.wantCode)
			}
		})
	}
}

func TestGenerateMapsJSONBodyErrors(t *testing.T) {
	oversized := `{"words":"` + strings.Repeat("x", int(httpx.DefaultMaxBodySize)) + `"}`
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantCode   string
		wantDetail string
	}{
		{name: "empty", wantStatus: http.StatusBadRequest, wantCode: httpx.CodeBadRequest, wantDetail: "invalid request body"},
		{name: "malformed", body: `{`, wantStatus: http.StatusBadRequest, wantCode: httpx.CodeBadRequest, wantDetail: "invalid request body"},
		{name: "multiple", body: `{} {}`, wantStatus: http.StatusBadRequest, wantCode: httpx.CodeBadRequest, wantDetail: "invalid request body"},
		{name: "oversized", body: oversized, wantStatus: http.StatusRequestEntityTooLarge, wantCode: httpx.CodePayloadTooLarge, wantDetail: "request body is too large"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := serveGenerate("application/json", test.body)
			problem := requireProblem(t, recorder, test.wantStatus)
			if problem.Code != test.wantCode || problem.Detail != test.wantDetail {
				t.Fatalf("problem = %#v, want code %q and detail %q", problem, test.wantCode, test.wantDetail)
			}
		})
	}
}

func TestWriteErrorMapsBuildQueueFullToTooManyRequests(t *testing.T) {
	handler := NewHandler(nil)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/g/demo", nil)
	internalMessage := "worker state and internal build key"
	handler.writeError(recorder, request, fmt.Errorf("%s: %w", internalMessage, appfont.ErrBuildQueueFull))

	problem := requireProblem(t, recorder, http.StatusTooManyRequests)
	if problem.Code != httpx.CodeFontBuildQueueFull {
		t.Fatalf("problem code = %q, want %q", problem.Code, httpx.CodeFontBuildQueueFull)
	}
	if got := recorder.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q, want %q", got, "1")
	}
	if strings.Contains(recorder.Body.String(), internalMessage) {
		t.Fatalf("response leaked internal error: %s", recorder.Body.String())
	}
}

func TestWriteErrorMapsArtifactCapacityToTooManyRequests(t *testing.T) {
	handler := NewHandler(nil)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/g/demo", nil)
	internalMessage := "retention state and internal artifact count"
	handler.writeError(recorder, request, fmt.Errorf("%s: %w", internalMessage, appfont.ErrArtifactCapacity))

	problem := requireProblem(t, recorder, http.StatusTooManyRequests)
	if problem.Code != httpx.CodeFontArtifactCapacity {
		t.Fatalf("problem code = %q, want %q", problem.Code, httpx.CodeFontArtifactCapacity)
	}
	if problem.Detail != "font artifact capacity is temporarily unavailable" {
		t.Fatalf("problem detail = %q", problem.Detail)
	}
	if got := recorder.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q, want %q", got, "1")
	}
	if strings.Contains(recorder.Body.String(), internalMessage) {
		t.Fatalf("response leaked internal error: %s", recorder.Body.String())
	}
}

func TestWriteErrorMapsUnsupportedCodepointsToValidationFailure(t *testing.T) {
	handler := NewHandler(nil)
	request := httptest.NewRequest(http.MethodPost, "/g/DemoFont", nil)
	recorder := httptest.NewRecorder()
	handler.writeError(recorder, request, fmt.Errorf("subset: %w", appfont.ErrUnsupportedCodepoints))

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnprocessableEntity)
	}
	if body := recorder.Body.String(); !strings.Contains(body, httpx.CodeFontUnsupportedCodepoints) || !strings.Contains(body, "body.words") {
		t.Fatalf("body = %s", body)
	}
}

func TestWriteOperationErrorMapsCancellationByOperation(t *testing.T) {
	tests := []struct {
		name       string
		operation  operation
		err        error
		wantStatus int
		wantCode   string
		wantRetry  string
	}{
		{name: "build deadline", operation: operationBuild, err: context.DeadlineExceeded, wantStatus: http.StatusGatewayTimeout, wantCode: httpx.CodeGatewayTimeout},
		{name: "typed build contention", operation: operationBuild, err: fmt.Errorf("claim: %w", appfont.ErrBuildNotReady), wantStatus: http.StatusServiceUnavailable, wantCode: httpx.CodeFontBuildNotReady, wantRetry: "1"},
		{name: "list deadline", operation: operationList, err: context.DeadlineExceeded, wantStatus: http.StatusGatewayTimeout, wantCode: httpx.CodeGatewayTimeout},
		{name: "info deadline", operation: operationInfo, err: fmt.Errorf("lookup: %w", context.DeadlineExceeded), wantStatus: http.StatusGatewayTimeout, wantCode: httpx.CodeGatewayTimeout},
		{name: "build canceled", operation: operationBuild, err: context.Canceled, wantStatus: http.StatusServiceUnavailable, wantCode: httpx.CodeServiceUnavailable},
		{name: "list canceled", operation: operationList, err: context.Canceled, wantStatus: http.StatusServiceUnavailable, wantCode: httpx.CodeServiceUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/list", nil)
			response := httptest.NewRecorder()
			NewHandler(nil).writeOperationError(response, request, test.operation, test.err)
			problem := requireProblem(t, response, test.wantStatus)
			if problem.Code != test.wantCode {
				t.Fatalf("problem code = %q, want %q", problem.Code, test.wantCode)
			}
			if got := response.Header().Get("Retry-After"); got != test.wantRetry {
				t.Fatalf("Retry-After = %q, want %q", got, test.wantRetry)
			}
		})
	}
}

func TestWriteOperationErrorLogsCauseWithoutLeakingResponse(t *testing.T) {
	const internalMessage = "postgres shard secret detail"
	core, logs := observer.New(zap.ErrorLevel)
	ctx := logger.IntoContext(context.Background(), zap.New(core))
	request := httptest.NewRequest(http.MethodGet, "/list", nil).WithContext(ctx)
	response := httptest.NewRecorder()

	NewHandler(nil).writeOperationError(response, request, operationList, errors.New(internalMessage))
	problem := requireProblem(t, response, http.StatusInternalServerError)
	if problem.Detail != "font operation failed" || strings.Contains(response.Body.String(), internalMessage) {
		t.Fatalf("response leaked operational cause: %s", response.Body.String())
	}
	entries := logs.FilterMessage("unexpected font operation failure").All()
	if len(entries) != 1 || !strings.Contains(fmt.Sprint(entries[0].ContextMap()["error"]), internalMessage) {
		t.Fatalf("operational log entries = %#v", entries)
	}
}

func TestListRejectsNonNumericLimit(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/list?limit=all", nil)
	NewHandler(nil).list(recorder, request)
	problem := requireProblem(t, recorder, http.StatusUnprocessableEntity)
	if problem.Code != httpx.CodeValidationFailed || len(problem.Errors) != 1 || problem.Errors[0].Location != "query.limit" {
		t.Fatalf("problem = %#v", problem)
	}
}

func TestCSSRejectsInvalidMinQueryValue(t *testing.T) {
	tests := []struct {
		name  string
		query string
		value string
	}{
		{name: "not boolean", query: "min=maybe", value: "maybe"},
		{name: "empty", query: "min=", value: ""},
		{name: "numeric", query: "min=1", value: "1"},
		{name: "multiple", query: "min=true&min=false", value: "true"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/css/demo?"+test.query, nil)
			NewHandler(nil).css(recorder, request)

			problem := requireProblem(t, recorder, http.StatusUnprocessableEntity)
			if problem.Code != httpx.CodeValidationFailed || len(problem.Errors) != 1 {
				t.Fatalf("problem = %#v", problem)
			}
			detail := problem.Errors[0]
			if detail.Code != httpx.FieldCodeInvalidValue || detail.Location != "query.min" || detail.Message != "min must be a boolean" {
				t.Fatalf("error detail = %#v", detail)
			}
			if fmt.Sprintf("%v", detail.Value) != test.value {
				t.Fatalf("error value = %#v, want %#v", detail.Value, test.value)
			}
		})
	}
}

func TestRetryAfterHeaderRoundsUp(t *testing.T) {
	if got := retryAfterHeader(1500 * time.Millisecond); got != "2" {
		t.Fatalf("retryAfterHeader = %q, want 2", got)
	}
}

func serveGenerate(contentType, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/g/demo", strings.NewReader(body))
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	NewHandler(nil).generate(recorder, request)
	return recorder
}

func requireProblem(t *testing.T, recorder *httptest.ResponseRecorder, wantStatus int) httpx.Problem {
	t.Helper()
	if recorder.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, wantStatus, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != httpx.ContentTypeProblem {
		t.Fatalf("Content-Type = %q, want %q", got, httpx.ContentTypeProblem)
	}
	var problem httpx.Problem
	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if problem.Status != wantStatus || problem.Title != http.StatusText(wantStatus) {
		t.Fatalf("problem = %#v, want status %d", problem, wantStatus)
	}
	return problem
}
