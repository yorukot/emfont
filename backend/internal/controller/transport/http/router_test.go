package http

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	appsystem "github.com/emfont/emfont/backend/internal/controller/application/system"
	"github.com/emfont/emfont/backend/internal/controller/transport/http/middleware"
	domain "github.com/emfont/emfont/backend/internal/domain/system"
	observabilitymetrics "github.com/emfont/emfont/backend/internal/platform/observability/metrics"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
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
	assertStatus(t, router, http.MethodGet, "/api/v1/livez", nil, http.StatusOK)
	assertStatus(t, router, http.MethodGet, "/api/v1/readyz", nil, http.StatusOK)

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

func TestRouterSeparatesLivenessAndReadiness(t *testing.T) {
	router := NewRouter(Dependencies{
		APIVersion: "v1",
		ReadinessCheck: func(context.Context) error {
			return context.DeadlineExceeded
		},
	})
	assertStatus(t, router, http.MethodGet, "/api/v1/livez", nil, http.StatusOK)
	assertStatus(t, router, http.MethodGet, "/api/v1/readyz", nil, http.StatusServiceUnavailable)
	assertStatus(t, router, http.MethodGet, "/api/v1/healthz", nil, http.StatusServiceUnavailable)
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
	assertStatus(t, router, http.MethodPost, "/g/DemoFont", bytes.NewBufferString(`{"words":"A","min":true}`), http.StatusServiceUnavailable)
}

func TestRouterMethodNotAllowedIncludesAllowHeader(t *testing.T) {
	for _, test := range []struct {
		method string
		path   string
		want   string
	}{
		{method: http.MethodPost, path: "/api/v1/docs", want: http.MethodGet},
		{method: http.MethodGet, path: "/g/DemoFont", want: http.MethodPost},
	} {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			NewRouter(Dependencies{APIVersion: "v1"}).ServeHTTP(
				response,
				httptest.NewRequest(test.method, test.path, nil),
			)
			if response.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusMethodNotAllowed)
			}
			if got := response.Header().Get("Allow"); got != test.want {
				t.Fatalf("Allow = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRouterRateLimitsAllPublicFontRoutes(t *testing.T) {
	tests := []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodPost, path: "/g/DemoFont", body: `{"words":"A","min":true}`},
		{method: http.MethodGet, path: "/css/DemoFont"},
		{method: http.MethodGet, path: "/list"},
		{method: http.MethodGet, path: "/info/DemoFont"},
		{method: http.MethodPost, path: "/api/v1/g/DemoFont", body: `{"words":"A","min":true}`},
		{method: http.MethodGet, path: "/api/v1/css/DemoFont"},
		{method: http.MethodGet, path: "/api/v1/list"},
		{method: http.MethodGet, path: "/api/v1/info/DemoFont"},
	}

	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			limiter, err := middleware.NewRateLimiter(middleware.RateLimitConfig{
				Rate: 0.001, Burst: 1, MaxClients: 1, IdleTimeout: time.Hour,
			})
			if err != nil {
				t.Fatalf("NewRateLimiter: %v", err)
			}
			router := NewRouter(Dependencies{APIVersion: "v1", FontRateLimit: limiter.Middleware})
			assertStatus(t, router, test.method, test.path, requestBody(test.body), http.StatusServiceUnavailable)
			assertStatus(t, router, test.method, test.path, requestBody(test.body), http.StatusTooManyRequests)
		})
	}
}

func TestRouterFontRateLimitDoesNotApplyToHealthDocsOrSystemRoutes(t *testing.T) {
	limiter, err := middleware.NewRateLimiter(middleware.RateLimitConfig{
		Rate: 0.001, Burst: 1, MaxClients: 1, IdleTimeout: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewRateLimiter: %v", err)
	}
	router := NewRouter(Dependencies{APIVersion: "v1", FontRateLimit: limiter.Middleware})
	assertStatus(t, router, http.MethodPost, "/g/DemoFont", requestBody(`{"words":"A","min":true}`), http.StatusServiceUnavailable)

	assertStatus(t, router, http.MethodGet, "/api/v1/livez", nil, http.StatusOK)
	assertStatus(t, router, http.MethodGet, "/api/v1/readyz", nil, http.StatusOK)
	assertStatus(t, router, http.MethodGet, "/api/v1/healthz", nil, http.StatusOK)
	assertStatus(t, router, http.MethodGet, "/api/v1/openapi.json", nil, http.StatusOK)
	assertStatus(t, router, http.MethodGet, "/api/v1/docs", nil, http.StatusOK)
	assertStatus(t, router, http.MethodGet, "/api/v1/system", nil, http.StatusServiceUnavailable)
}

func TestRouterAggregateRateLimitsEveryPublicSurface(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		path        string
		body        string
		firstStatus int
	}{
		{name: "OpenAPI", method: http.MethodGet, path: "/api/v1/openapi.json", firstStatus: http.StatusOK},
		{name: "docs", method: http.MethodGet, path: "/api/v1/docs", firstStatus: http.StatusOK},
		{name: "system", method: http.MethodGet, path: "/api/v1/system", firstStatus: http.StatusServiceUnavailable},
		{name: "versioned font", method: http.MethodGet, path: "/api/v1/list", firstStatus: http.StatusServiceUnavailable},
		{name: "legacy font", method: http.MethodGet, path: "/list", firstStatus: http.StatusServiceUnavailable},
		{name: "not found", method: http.MethodGet, path: "/missing", firstStatus: http.StatusNotFound},
		{name: "method not allowed", method: http.MethodPost, path: "/api/v1/docs", firstStatus: http.StatusMethodNotAllowed},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			limiter, err := middleware.NewRateLimiter(middleware.RateLimitConfig{
				Rate: 0.001, Burst: 1, MaxClients: 1, IdleTimeout: time.Hour,
			})
			if err != nil {
				t.Fatalf("NewRateLimiter: %v", err)
			}
			router := NewRouter(Dependencies{APIVersion: "v1", AggregateRateLimit: limiter.Middleware})
			assertStatus(t, router, test.method, test.path, requestBody(test.body), test.firstStatus)
			assertStatus(t, router, test.method, test.path, requestBody(test.body), http.StatusTooManyRequests)
		})
	}
}

func TestRouterAggregateLimitIsSharedAcrossPublicRoutes(t *testing.T) {
	limiter, err := middleware.NewRateLimiter(middleware.RateLimitConfig{
		Rate: 0.001, Burst: 2, MaxClients: 1, IdleTimeout: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewRateLimiter: %v", err)
	}
	router := NewRouter(Dependencies{APIVersion: "v1", AggregateRateLimit: limiter.Middleware})

	assertStatus(t, router, http.MethodGet, "/api/v1/docs", nil, http.StatusOK)
	assertStatus(t, router, http.MethodGet, "/api/v1/system", nil, http.StatusServiceUnavailable)
	assertStatus(t, router, http.MethodGet, "/list", nil, http.StatusTooManyRequests)
}

func TestRouterAggregateLimitRunsBeforeRequestLogging(t *testing.T) {
	limiter, err := middleware.NewRateLimiter(middleware.RateLimitConfig{
		Rate: 0.001, Burst: 1, MaxClients: 1, IdleTimeout: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewRateLimiter: %v", err)
	}
	core, logs := observer.New(zap.InfoLevel)
	router := NewRouter(Dependencies{
		APIVersion: "v1", Log: zap.New(core), AggregateRateLimit: limiter.Middleware,
	})

	assertStatus(t, router, http.MethodGet, "/api/v1/docs", nil, http.StatusOK)
	assertStatus(t, router, http.MethodGet, "/api/v1/docs", nil, http.StatusTooManyRequests)
	if got := logs.FilterMessage("http request").Len(); got != 1 {
		t.Fatalf("request log entries = %d, want only the admitted request", got)
	}
}

func TestRouterHealthRateLimitIsSharedAndDoesNotLimitPublicRoutes(t *testing.T) {
	limiter, err := middleware.NewRateLimiter(middleware.RateLimitConfig{
		Rate: 0.001, Burst: 1, MaxClients: 1, IdleTimeout: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewRateLimiter: %v", err)
	}
	router := NewRouter(Dependencies{APIVersion: "v1", HealthRateLimit: limiter.Middleware})

	assertStatus(t, router, http.MethodGet, "/api/v1/livez", nil, http.StatusOK)
	assertStatus(t, router, http.MethodGet, "/api/v1/readyz", nil, http.StatusTooManyRequests)
	assertStatus(t, router, http.MethodGet, "/api/v1/docs", nil, http.StatusOK)
}

func TestRouterRecordsAggregateRateLimitRejectionsInMetrics(t *testing.T) {
	limiter, err := middleware.NewRateLimiter(middleware.RateLimitConfig{
		Rate: 0.001, Burst: 1, MaxClients: 1, IdleTimeout: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewRateLimiter: %v", err)
	}
	metricsProvider := observabilitymetrics.MustNew(observabilitymetrics.Config{})
	router := NewRouter(Dependencies{
		APIVersion: "v1", Metrics: metricsProvider, MetricsPath: "/internal/metrics",
		AggregateRateLimit: limiter.Middleware,
	})

	assertStatus(t, router, http.MethodGet, "/api/v1/docs", nil, http.StatusOK)
	assertStatus(t, router, http.MethodGet, "/api/v1/docs", nil, http.StatusTooManyRequests)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/internal/metrics", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, want %d", response.Code, http.StatusOK)
	}
	if body := response.Body.String(); !strings.Contains(body, `emfont_http_requests_total{method="GET",route="unknown",status="429"} 1`) {
		t.Fatalf("metrics do not contain the aggregate 429 outcome:\n%s", body)
	}
}

func TestRouterRecordsAdmissionMiddlewarePanicAsInternalServerError(t *testing.T) {
	metricsProvider := observabilitymetrics.MustNew(observabilitymetrics.Config{})
	core, logs := observer.New(zap.ErrorLevel)
	panicLimit := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic("admission panic test")
		})
	}
	router := NewRouter(Dependencies{
		APIVersion: "v1", Log: zap.New(core), Metrics: metricsProvider, MetricsPath: "/internal/metrics",
		AggregateRateLimit: panicLimit,
	})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/docs", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("admission panic status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	if got := logs.FilterMessage("panic recovered").Len(); got != 1 {
		t.Fatalf("panic recovery logs = %d, want 1", got)
	}

	metricsResponse := httptest.NewRecorder()
	router.ServeHTTP(metricsResponse, httptest.NewRequest(http.MethodGet, "/internal/metrics", nil))
	if body := metricsResponse.Body.String(); !strings.Contains(body, `emfont_http_requests_total{method="GET",route="unknown",status="500"} 1`) {
		t.Fatalf("metrics do not contain the recovered admission 500 outcome:\n%s", body)
	}
}

func TestRouterDoesNotRewriteCommittedResponseAfterMiddlewarePanic(t *testing.T) {
	metricsProvider := observabilitymetrics.MustNew(observabilitymetrics.Config{})
	core, logs := observer.New(zap.ErrorLevel)
	writeThenPanic := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte("partial"))
			panic("post-write admission panic test")
		})
	}
	router := NewRouter(Dependencies{
		APIVersion: "v1", Log: zap.New(core), Metrics: metricsProvider, MetricsPath: "/internal/metrics",
		AggregateRateLimit: writeThenPanic,
	})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/docs", nil))
	if response.Code != http.StatusAccepted || response.Body.String() != "partial" {
		t.Fatalf("committed response = status %d body %q", response.Code, response.Body.String())
	}
	if got := logs.FilterMessage("panic recovered").Len(); got != 1 {
		t.Fatalf("panic recovery logs = %d, want 1", got)
	}

	metricsResponse := httptest.NewRecorder()
	router.ServeHTTP(metricsResponse, httptest.NewRequest(http.MethodGet, "/internal/metrics", nil))
	if body := metricsResponse.Body.String(); !strings.Contains(body, `emfont_http_requests_total{method="GET",route="unknown",status="202"} 1`) {
		t.Fatalf("metrics do not preserve the committed 202 outcome:\n%s", body)
	}
}

type panicSystemStore struct{}

func (panicSystemStore) GetSystem(context.Context, domain.ID) (domain.System, error) {
	panic("router panic test")
}

func (panicSystemStore) UpsertSystem(context.Context, domain.System) error {
	panic("router panic test")
}

func TestRouterPanicIsObservedAsInternalServerError(t *testing.T) {
	service, err := appsystem.NewService(panicSystemStore{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	metricsProvider := observabilitymetrics.MustNew(observabilitymetrics.Config{})
	core, logs := observer.New(zap.InfoLevel)
	spanRecorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(spanRecorder),
	)
	previousProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown trace provider: %v", err)
		}
	})

	router := NewRouter(Dependencies{
		APIVersion: "v1", ServiceName: "router-panic-test", Log: zap.New(core),
		Metrics: metricsProvider, MetricsPath: "/internal/metrics", Tracing: true,
		SystemService: service,
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/system", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("panic status = %d, want %d", response.Code, http.StatusInternalServerError)
	}

	requestLogs := logs.FilterMessage("http request").All()
	if len(requestLogs) != 1 || requestLogs[0].ContextMap()["http.status"] != int64(http.StatusInternalServerError) {
		t.Fatalf("panic request logs = %#v, want one status 500 entry", requestLogs)
	}
	if got := logs.FilterMessage("panic recovered").Len(); got != 1 {
		t.Fatalf("panic recovery logs = %d, want 1", got)
	}
	metricsResponse := httptest.NewRecorder()
	router.ServeHTTP(metricsResponse, httptest.NewRequest(http.MethodGet, "/internal/metrics", nil))
	if body := metricsResponse.Body.String(); !strings.Contains(body, `emfont_http_requests_total{method="GET",route="/api/v1/system",status="500"} 1`) {
		t.Fatalf("metrics do not contain the recovered 500 outcome:\n%s", body)
	}

	var foundHTTPSpan bool
	for _, span := range spanRecorder.Ended() {
		if span.Name() != "GET /api/v1/system" {
			continue
		}
		foundHTTPSpan = true
		for _, attribute := range span.Attributes() {
			if attribute.Key == "http.response.status_code" && attribute.Value.AsInt64() == http.StatusInternalServerError {
				return
			}
		}
		t.Fatalf("HTTP panic span has no status 500 attribute: %#v", span.Attributes())
	}
	if !foundHTTPSpan {
		t.Fatal("recovered panic produced no completed HTTP span")
	}
}

func TestRouterSeparatelyLimitsRejectedAndAuthenticatedMetricsRequests(t *testing.T) {
	publicLimiter, err := middleware.NewRateLimiter(middleware.RateLimitConfig{
		Rate: 0.001, Burst: 1, MaxClients: 1, IdleTimeout: time.Hour,
	})
	if err != nil {
		t.Fatalf("public NewRateLimiter: %v", err)
	}
	rejectedLimiter, err := middleware.NewRateLimiter(middleware.RateLimitConfig{
		Rate: 0.001, Burst: 1, MaxClients: 1, IdleTimeout: time.Hour,
	})
	if err != nil {
		t.Fatalf("rejected metrics NewRateLimiter: %v", err)
	}
	authenticatedLimiter, err := middleware.NewRateLimiter(middleware.RateLimitConfig{
		Rate: 0.001, Burst: 1, MaxClients: 1, IdleTimeout: time.Hour,
	})
	if err != nil {
		t.Fatalf("authenticated metrics NewRateLimiter: %v", err)
	}
	metricsProvider := observabilitymetrics.MustNew(observabilitymetrics.Config{})
	router := NewRouter(Dependencies{
		APIVersion: "v1", Metrics: metricsProvider, MetricsPath: "/internal/metrics",
		MetricsToken: "metrics-token-value", AggregateRateLimit: publicLimiter.Middleware,
		MetricsAuthenticatedRateLimit: authenticatedLimiter.Middleware,
		MetricsRejectedRateLimit:      rejectedLimiter.Middleware,
	})

	assertStatus(t, router, http.MethodGet, "/api/v1/docs", nil, http.StatusOK)
	assertStatus(t, router, http.MethodGet, "/internal/metrics", nil, http.StatusUnauthorized)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/internal/metrics", nil))
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("second metrics auth status = %d, want %d", response.Code, http.StatusTooManyRequests)
	}
	var problem struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode 429 response: %v", err)
	}
	if problem.Code != "RATE_LIMITED" {
		t.Fatalf("429 code = %q, want RATE_LIMITED", problem.Code)
	}
	authorized := httptest.NewRequest(http.MethodGet, "/internal/metrics", nil)
	authorized.Header.Set("Authorization", "Bearer metrics-token-value")
	authorizedResponse := httptest.NewRecorder()
	router.ServeHTTP(authorizedResponse, authorized)
	if authorizedResponse.Code != http.StatusOK {
		t.Fatalf("authorized metrics status after rejected bucket exhausted = %d, want %d", authorizedResponse.Code, http.StatusOK)
	}
	secondAuthorized := httptest.NewRequest(http.MethodGet, "/internal/metrics", nil)
	secondAuthorized.Header.Set("Authorization", "Bearer metrics-token-value")
	secondAuthorizedResponse := httptest.NewRecorder()
	router.ServeHTTP(secondAuthorizedResponse, secondAuthorized)
	if secondAuthorizedResponse.Code != http.StatusTooManyRequests {
		t.Fatalf("second authorized metrics status = %d, want %d", secondAuthorizedResponse.Code, http.StatusTooManyRequests)
	}
	assertStatus(t, router, http.MethodGet, "/api/v1/openapi.json", nil, http.StatusTooManyRequests)
}

func TestRouterRejectsMetricsFloodBeforeObservabilityMiddleware(t *testing.T) {
	rejectedLimiter, err := middleware.NewRateLimiter(middleware.RateLimitConfig{
		Rate: 0.001, Burst: 1, MaxClients: 1, IdleTimeout: time.Hour,
	})
	if err != nil {
		t.Fatalf("rejected metrics NewRateLimiter: %v", err)
	}
	authenticatedLimiter, err := middleware.NewRateLimiter(middleware.RateLimitConfig{
		Rate: 1, Burst: 1, MaxClients: 1, IdleTimeout: time.Hour,
	})
	if err != nil {
		t.Fatalf("authenticated metrics NewRateLimiter: %v", err)
	}
	core, logs := observer.New(zap.InfoLevel)
	router := NewRouter(Dependencies{
		APIVersion: "v1", Log: zap.New(core),
		Metrics: observabilitymetrics.MustNew(observabilitymetrics.Config{}), MetricsPath: "/internal/metrics",
		MetricsToken:                  "metrics-token-value",
		MetricsAuthenticatedRateLimit: authenticatedLimiter.Middleware,
		MetricsRejectedRateLimit:      rejectedLimiter.Middleware,
	})

	assertStatus(t, router, http.MethodGet, "/internal/metrics", nil, http.StatusUnauthorized)
	assertStatus(t, router, http.MethodGet, "/internal/metrics", nil, http.StatusTooManyRequests)
	if got := logs.FilterMessage("http request").Len(); got != 0 {
		t.Fatalf("request log entries for rejected metrics flood = %d, want 0", got)
	}
	request := httptest.NewRequest(http.MethodGet, "/internal/metrics", nil)
	request.Header.Set("Authorization", "Bearer metrics-token-value")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("authenticated metrics status = %d, want %d", response.Code, http.StatusOK)
	}
	if got := logs.FilterMessage("http request").Len(); got != 1 {
		t.Fatalf("request log entries after authenticated metrics = %d, want 1", got)
	}
}

func TestRouterAggregateLimitExplicitlyExemptsHealthAndMetrics(t *testing.T) {
	limiter, err := middleware.NewRateLimiter(middleware.RateLimitConfig{
		Rate: 0.001, Burst: 1, MaxClients: 1, IdleTimeout: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewRateLimiter: %v", err)
	}
	metricsProvider := observabilitymetrics.MustNew(observabilitymetrics.Config{})
	router := NewRouter(Dependencies{
		APIVersion: "v1", Metrics: metricsProvider, MetricsPath: "/internal/metrics",
		AggregateRateLimit: limiter.Middleware,
	})

	assertStatus(t, router, http.MethodGet, "/api/v1/docs", nil, http.StatusOK)
	for range 2 {
		assertStatus(t, router, http.MethodGet, "/api/v1/livez", nil, http.StatusOK)
		assertStatus(t, router, http.MethodGet, "/api/v1/readyz", nil, http.StatusOK)
		assertStatus(t, router, http.MethodGet, "/api/v1/healthz", nil, http.StatusOK)
		assertStatus(t, router, http.MethodGet, "/internal/metrics", nil, http.StatusOK)
	}
	assertStatus(t, router, http.MethodGet, "/api/v1/openapi.json", nil, http.StatusTooManyRequests)
}

func TestRouterDoesNotExemptDisabledMetricsPathOrHealthLookalikes(t *testing.T) {
	for _, path := range []string{"/metrics", "/api/v1/healthz/", "/api/v1/livez/missing"} {
		t.Run(path, func(t *testing.T) {
			limiter, err := middleware.NewRateLimiter(middleware.RateLimitConfig{
				Rate: 0.001, Burst: 1, MaxClients: 1, IdleTimeout: time.Hour,
			})
			if err != nil {
				t.Fatalf("NewRateLimiter: %v", err)
			}
			router := NewRouter(Dependencies{APIVersion: "v1", AggregateRateLimit: limiter.Middleware})
			assertStatus(t, router, http.MethodGet, path, nil, http.StatusNotFound)
			assertStatus(t, router, http.MethodGet, path, nil, http.StatusTooManyRequests)
		})
	}
}

func TestRouterAppliesAggregateLimitOnceToFontRoutes(t *testing.T) {
	var calls atomic.Int32
	aggregate := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			next.ServeHTTP(w, r)
		})
	}
	router := NewRouter(Dependencies{APIVersion: "v1", AggregateRateLimit: aggregate})

	assertStatus(t, router, http.MethodGet, "/api/v1/list", nil, http.StatusServiceUnavailable)
	if got := calls.Load(); got != 1 {
		t.Fatalf("aggregate middleware calls = %d, want 1", got)
	}
}

func TestRouterAggregateLimitCoversCORSPreflightAndSharesThePublicBucket(t *testing.T) {
	limiter, err := middleware.NewRateLimiter(middleware.RateLimitConfig{
		Rate: 0.001, Burst: 1, MaxClients: 1, IdleTimeout: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewRateLimiter: %v", err)
	}
	router := NewRouter(Dependencies{
		APIVersion: "v1", AllowedOrigins: []string{"https://app.example"},
		AggregateRateLimit: limiter.Middleware,
	})

	preflight := httptest.NewRequest(http.MethodOptions, "/api/v1/g/DemoFont", nil)
	preflight.Header.Set("Origin", "https://app.example")
	preflight.Header.Set("Access-Control-Request-Method", http.MethodPost)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, preflight)
	if response.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want %d", response.Code, http.StatusNoContent)
	}
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example" {
		t.Fatalf("preflight allow origin = %q", got)
	}

	limited := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/docs", nil)
	request.Header.Set("Origin", "https://app.example")
	router.ServeHTTP(limited, request)
	if limited.Code != http.StatusTooManyRequests {
		t.Fatalf("request after preflight status = %d, want %d", limited.Code, http.StatusTooManyRequests)
	}
	if got := limited.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example" {
		t.Fatalf("rate-limited allow origin = %q", got)
	}
}

func TestRouterMetricsRejectedLimitCoversPreflightWithoutStarvingAuthenticatedRequests(t *testing.T) {
	rejectedLimiter, err := middleware.NewRateLimiter(middleware.RateLimitConfig{
		Rate: 0.001, Burst: 1, MaxClients: 1, IdleTimeout: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewRateLimiter: %v", err)
	}
	authenticatedLimiter, err := middleware.NewRateLimiter(middleware.RateLimitConfig{
		Rate: 0.001, Burst: 1, MaxClients: 1, IdleTimeout: time.Hour,
	})
	if err != nil {
		t.Fatalf("authenticated NewRateLimiter: %v", err)
	}
	router := NewRouter(Dependencies{
		APIVersion: "v1", AllowedOrigins: []string{"https://ops.example"},
		Metrics:     observabilitymetrics.MustNew(observabilitymetrics.Config{}),
		MetricsPath: "/internal/metrics", MetricsToken: "metrics-token-value",
		MetricsAuthenticatedRateLimit: authenticatedLimiter.Middleware,
		MetricsRejectedRateLimit:      rejectedLimiter.Middleware,
	})

	preflight := httptest.NewRequest(http.MethodOptions, "/internal/metrics", nil)
	preflight.Header.Set("Origin", "https://ops.example")
	preflight.Header.Set("Access-Control-Request-Method", http.MethodGet)
	preflightResponse := httptest.NewRecorder()
	router.ServeHTTP(preflightResponse, preflight)
	if preflightResponse.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want %d", preflightResponse.Code, http.StatusNoContent)
	}

	request := httptest.NewRequest(http.MethodGet, "/internal/metrics", nil)
	request.Header.Set("Origin", "https://ops.example")
	request.Header.Set("Authorization", "Bearer metrics-token-value")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("authenticated metrics status after preflight = %d, want %d", response.Code, http.StatusOK)
	}
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "https://ops.example" {
		t.Fatalf("rate-limited allow origin = %q", got)
	}
}

func TestRouterHandlesFontAPIPreflight(t *testing.T) {
	router := NewRouter(Dependencies{APIVersion: "v1", AllowedOrigins: []string{"https://app.example"}})
	request := httptest.NewRequest(http.MethodOptions, "/api/v1/g/DemoFont", nil)
	request.Header.Set("Origin", "https://app.example")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	request.Header.Set("Access-Control-Request-Headers", "content-type")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want %d", response.Code, http.StatusNoContent)
	}
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example" {
		t.Fatalf("allow origin = %q", got)
	}
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

func requestBody(body string) *bytes.Buffer {
	if body == "" {
		return nil
	}
	return bytes.NewBufferString(body)
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
