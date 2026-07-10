package http

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	appsystem "github.com/emfont/emfont/backend/internal/controller/application/system"
	domain "github.com/emfont/emfont/backend/internal/domain/system"
)

func TestRouterHealthAndOpenAPI(t *testing.T) {
	router := NewRouter(Dependencies{
		APIVersion:  "v1",
		ServiceName: "emfont-controller",
		Version:     "test",
		ReadinessCheck: func(context.Context) error {
			return nil
		},
	})

	assertStatus(t, router, http.MethodGet, "/api/v1/healthz", nil, http.StatusOK)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("openapi status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var document map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &document); err != nil {
		t.Fatalf("openapi response is not json: %v", err)
	}
	if document["openapi"] == "" {
		t.Fatal("openapi document is missing openapi version")
	}
}

func TestRouterNotFoundReturnsProblem(t *testing.T) {
	router := NewRouter(Dependencies{APIVersion: "v1"})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/missing", nil)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/problem+json; charset=utf-8" {
		t.Fatalf("content type = %q, want problem json", got)
	}
	var problem struct {
		Code      string `json:"code"`
		RequestID string `json:"requestId"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if problem.Code != "ROUTE_NOT_FOUND" || problem.RequestID == "" {
		t.Fatalf("problem = %#v", problem)
	}
}

func TestRouterRegistersLegacyAndVersionedFontRoutes(t *testing.T) {
	router := NewRouter(Dependencies{APIVersion: "v1"})
	assertStatus(t, router, http.MethodGet, "/list", nil, http.StatusServiceUnavailable)
	assertStatus(t, router, http.MethodGet, "/api/v1/list", nil, http.StatusServiceUnavailable)
	assertStatus(t, router, http.MethodGet, "/g/DemoFont", nil, http.StatusMethodNotAllowed)
}

func TestRouterSystemVerticalSlice(t *testing.T) {
	service, err := appsystem.NewService(&fakeSystemStore{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	router := NewRouter(Dependencies{
		APIVersion:    "v1",
		SystemService: service,
	})

	_, err = service.Upsert(context.Background(), appsystem.UpsertRequest{
		Name: "Emfont", Environment: "test", Version: "0.1.0", Status: "ready",
	})
	if err != nil {
		t.Fatalf("seed system: %v", err)
	}
	assertStatus(t, router, http.MethodGet, "/api/v1/system", nil, http.StatusOK)
	body := bytes.NewBufferString(`{"name":"unauthorized"}`)
	assertStatus(t, router, http.MethodPut, "/api/v1/system", body, http.StatusMethodNotAllowed)
}

func assertStatus(t *testing.T, handler http.Handler, method, path string, body *bytes.Buffer, want int) {
	t.Helper()
	var requestBody io.Reader
	if body != nil {
		requestBody = body
	}
	request := httptest.NewRequest(method, path, requestBody)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != want {
		t.Fatalf("%s %s status = %d, want %d; body=%s", method, path, recorder.Code, want, recorder.Body.String())
	}
}

type fakeSystemStore struct {
	system domain.System
}

func (s *fakeSystemStore) GetSystem(context.Context, domain.ID) (domain.System, error) {
	if s.system.ID() == "" {
		return domain.System{}, domain.ErrSystemNotFound
	}
	return s.system, nil
}

func (s *fakeSystemStore) UpsertSystem(_ context.Context, system domain.System) error {
	s.system = system
	return nil
}
