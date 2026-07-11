package system

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/emfont/emfont/backend/internal/controller/logger"
	"github.com/emfont/emfont/backend/internal/controller/transport/http/httpx"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestWriteErrorMapsContextTermination(t *testing.T) {
	for _, test := range []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "deadline", err: fmt.Errorf("database: %w", context.DeadlineExceeded), wantStatus: http.StatusGatewayTimeout, wantCode: httpx.CodeGatewayTimeout},
		{name: "canceled", err: context.Canceled, wantStatus: http.StatusServiceUnavailable, wantCode: httpx.CodeSystemServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/v1/system", nil)
			response := httptest.NewRecorder()
			NewHandler(nil).writeError(response, request, test.err)

			problem := requireSystemProblem(t, response, test.wantStatus)
			if problem.Code != test.wantCode {
				t.Fatalf("problem code = %q, want %q", problem.Code, test.wantCode)
			}
		})
	}
}

func TestWriteErrorLogsOperationalCauseWithoutLeakingResponse(t *testing.T) {
	const internalMessage = "database endpoint and internal row detail"
	core, logs := observer.New(zap.ErrorLevel)
	ctx := logger.IntoContext(context.Background(), zap.New(core))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/system", nil).WithContext(ctx)
	response := httptest.NewRecorder()

	NewHandler(nil).writeError(response, request, errors.New(internalMessage))
	problem := requireSystemProblem(t, response, http.StatusInternalServerError)
	if problem.Detail != "system operation failed" || strings.Contains(response.Body.String(), internalMessage) {
		t.Fatalf("response leaked operational cause: %s", response.Body.String())
	}
	entries := logs.FilterMessage("system operation failed").All()
	if len(entries) != 1 || !strings.Contains(fmt.Sprint(entries[0].ContextMap()["error"]), internalMessage) {
		t.Fatalf("operational log entries = %#v", entries)
	}
}

func requireSystemProblem(t *testing.T, response *httptest.ResponseRecorder, wantStatus int) httpx.Problem {
	t.Helper()
	if response.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, wantStatus, response.Body.String())
	}
	var problem httpx.Problem
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	return problem
}
