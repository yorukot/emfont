package http

import (
	"context"
	stdhttp "net/http"
	"strings"
	"time"

	appfont "github.com/emfont/emfont/backend/internal/controller/application/font"
	appsystem "github.com/emfont/emfont/backend/internal/controller/application/system"
	fonthttp "github.com/emfont/emfont/backend/internal/controller/transport/http/handler/font"
	systemhttp "github.com/emfont/emfont/backend/internal/controller/transport/http/handler/system"
	"github.com/emfont/emfont/backend/internal/controller/transport/http/httpx"
	"github.com/emfont/emfont/backend/internal/controller/transport/http/middleware"
	"github.com/emfont/emfont/backend/internal/controller/transport/http/openapi"
	"github.com/emfont/emfont/backend/internal/platform/observability/metrics"
	"github.com/emfont/emfont/backend/internal/platform/observability/tracing"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

type Dependencies struct {
	Log                           *zap.Logger
	APIVersion                    string
	ServiceName                   string
	Version                       string
	BackendBaseURL                string
	RequestTimeout                time.Duration
	ReadinessCheck                func(context.Context) error
	Metrics                       *metrics.Metrics
	MetricsPath                   string
	MetricsToken                  string
	AllowedOrigins                []string
	FontService                   *appfont.Service
	SystemService                 *appsystem.Service
	OpenAPI                       OpenAPIConfig
	Tracing                       bool
	FontRateLimit                 func(stdhttp.Handler) stdhttp.Handler
	AggregateRateLimit            func(stdhttp.Handler) stdhttp.Handler
	HealthRateLimit               func(stdhttp.Handler) stdhttp.Handler
	MetricsAuthenticatedRateLimit func(stdhttp.Handler) stdhttp.Handler
	MetricsRejectedRateLimit      func(stdhttp.Handler) stdhttp.Handler
}

func NewRouter(dep Dependencies) stdhttp.Handler {
	if dep.Log == nil {
		dep.Log = zap.NewNop()
	}
	if dep.APIVersion == "" {
		dep.APIVersion = "v1"
	}
	if dep.ServiceName == "" {
		dep.ServiceName = "emfont-controller"
	}
	if dep.Version == "" {
		dep.Version = "0.1.0"
	}
	if dep.RequestTimeout <= 0 {
		dep.RequestTimeout = 15 * time.Second
	}
	if dep.MetricsPath == "" {
		dep.MetricsPath = "/metrics"
	}
	if len(dep.AllowedOrigins) == 0 {
		dep.AllowedOrigins = []string{"*"}
	}
	if dep.OpenAPI.Version == "" {
		dep.OpenAPI.Version = dep.APIVersion
	}
	if dep.OpenAPI.BackendBaseURL == "" {
		dep.OpenAPI.BackendBaseURL = dep.BackendBaseURL
	}
	basePath := "/api/" + dep.APIVersion
	healthPaths := []string{
		basePath + "/livez",
		basePath + "/readyz",
		basePath + "/healthz",
	}
	var aggregateRateLimit func(stdhttp.Handler) stdhttp.Handler
	if dep.AggregateRateLimit != nil {
		exemptPaths := append([]string(nil), healthPaths...)
		if dep.Metrics != nil {
			exemptPaths = append(exemptPaths, dep.MetricsPath)
		}
		aggregateRateLimit = exceptPaths(dep.AggregateRateLimit, exemptPaths...)
	}
	healthRateLimit := onlyPaths(dep.HealthRateLimit, healthPaths...)
	metricsPreflightRateLimit := onlyPath(dep.MetricsRejectedRateLimit, dep.MetricsPath)
	preflightRateLimit := chainMiddleware(metricsPreflightRateLimit, aggregateRateLimit)
	var metricsAccessLimit func(stdhttp.Handler) stdhttp.Handler
	if dep.Metrics != nil {
		if dep.MetricsToken != "" {
			metricsAccessLimit = onlyPath(middleware.BearerTokenWithRateLimits(
				dep.MetricsToken,
				dep.MetricsAuthenticatedRateLimit,
				dep.MetricsRejectedRateLimit,
			), dep.MetricsPath)
		} else {
			metricsAccessLimit = onlyPath(dep.MetricsAuthenticatedRateLimit, dep.MetricsPath)
		}
	}

	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.SecurityHeaders(middleware.DefaultSecurityConfig()))
	router.Use(middleware.Timeout(dep.RequestTimeout))
	// The outer recovery also catches failures in observability or policy middleware.
	router.Use(middleware.Recovery(dep.Log))
	if dep.Metrics != nil {
		// Keep metrics outside admission controls so bounded 401/429 outcomes remain visible.
		router.Use(dep.Metrics.Middleware(RoutePattern))
	}
	// Convert failures in policy and observability middleware before metrics unwind.
	router.Use(middleware.Recovery(dep.Log))
	router.Use(middleware.CORS(middleware.CORSConfig{
		AllowedOrigins: dep.AllowedOrigins, PreflightMiddleware: preflightRateLimit,
	}))
	if metricsAccessLimit != nil {
		router.Use(metricsAccessLimit)
	}
	if aggregateRateLimit != nil {
		router.Use(aggregateRateLimit)
	}
	if healthRateLimit != nil {
		router.Use(healthRateLimit)
	}
	router.Use(middleware.Logging(dep.Log))
	if dep.Tracing {
		router.Use(tracing.Middleware(tracing.Tracer(dep.ServiceName), RoutePattern))
	}
	// Handler panics must become a response before logging, metrics, and tracing unwind.
	router.Use(middleware.Recovery(dep.Log))

	if dep.Metrics != nil {
		router.Handle(dep.MetricsPath, dep.Metrics.Handler())
	}

	fontHandler := fonthttp.NewHandler(dep.FontService, dep.FontRateLimit)
	router.Route(basePath, func(api chi.Router) {
		healthConfig := HealthConfig{
			ServiceName:    dep.ServiceName,
			Version:        dep.Version,
			ReadinessCheck: dep.ReadinessCheck,
		}
		api.Get("/livez", LivenessHandler(healthConfig))
		api.Get("/readyz", HealthHandler(healthConfig))
		api.Get("/healthz", HealthHandler(healthConfig))
		api.Get("/openapi.json", openAPIHandler(dep.OpenAPI, dep.Log))
		api.Get("/docs", DocsHandler(dep.OpenAPI))
		systemhttp.NewHandler(dep.SystemService).RegisterRoutes(api)
		fontHandler.RegisterRoutes(api)
	})
	fontHandler.RegisterRoutes(router)

	router.NotFound(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		middleware.WriteProblemCode(w, r, stdhttp.StatusNotFound, httpx.CodeRouteNotFound, "route not found")
	})
	router.MethodNotAllowed(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		setAllowHeader(w, router, r.URL.Path)
		middleware.WriteProblemCode(w, r, stdhttp.StatusMethodNotAllowed, httpx.CodeMethodNotAllowed, "method not allowed")
	})

	return router
}

func onlyPath(routeMiddleware func(stdhttp.Handler) stdhttp.Handler, path string) func(stdhttp.Handler) stdhttp.Handler {
	if routeMiddleware == nil {
		return nil
	}
	return func(next stdhttp.Handler) stdhttp.Handler {
		limited := routeMiddleware(next)
		return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			if r.URL.Path == path {
				limited.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func onlyPaths(routeMiddleware func(stdhttp.Handler) stdhttp.Handler, paths ...string) func(stdhttp.Handler) stdhttp.Handler {
	if routeMiddleware == nil || len(paths) == 0 {
		return nil
	}
	allowed := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		allowed[path] = struct{}{}
	}
	return func(next stdhttp.Handler) stdhttp.Handler {
		limited := routeMiddleware(next)
		return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			if _, ok := allowed[r.URL.Path]; ok {
				limited.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func chainMiddleware(middlewares ...func(stdhttp.Handler) stdhttp.Handler) func(stdhttp.Handler) stdhttp.Handler {
	filtered := make([]func(stdhttp.Handler) stdhttp.Handler, 0, len(middlewares))
	for _, candidate := range middlewares {
		if candidate != nil {
			filtered = append(filtered, candidate)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return func(next stdhttp.Handler) stdhttp.Handler {
		for index := len(filtered) - 1; index >= 0; index-- {
			next = filtered[index](next)
		}
		return next
	}
}

func setAllowHeader(w stdhttp.ResponseWriter, router *chi.Mux, path string) {
	methods := []string{
		stdhttp.MethodConnect,
		stdhttp.MethodDelete,
		stdhttp.MethodGet,
		stdhttp.MethodHead,
		stdhttp.MethodOptions,
		stdhttp.MethodPatch,
		stdhttp.MethodPost,
		stdhttp.MethodPut,
		stdhttp.MethodTrace,
	}
	allowed := make([]string, 0, len(methods))
	for _, method := range methods {
		if router.Match(chi.NewRouteContext(), method, path) {
			allowed = append(allowed, method)
		}
	}
	if len(allowed) > 0 {
		w.Header().Set("Allow", strings.Join(allowed, ", "))
	}
}

func exceptPaths(routeMiddleware func(stdhttp.Handler) stdhttp.Handler, paths ...string) func(stdhttp.Handler) stdhttp.Handler {
	exempt := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		exempt[path] = struct{}{}
	}
	return func(next stdhttp.Handler) stdhttp.Handler {
		limited := routeMiddleware(next)
		return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			if _, ok := exempt[r.URL.Path]; ok {
				next.ServeHTTP(w, r)
				return
			}
			limited.ServeHTTP(w, r)
		})
	}
}

func RoutePattern(r *stdhttp.Request) string {
	routeContext := chi.RouteContext(r.Context())
	if routeContext == nil {
		return ""
	}
	return routeContext.RoutePattern()
}

func OpenAPIHandler(cfg OpenAPIConfig) stdhttp.HandlerFunc {
	return openAPIHandler(cfg, zap.NewNop())
}

func openAPIHandler(cfg OpenAPIConfig, log *zap.Logger) stdhttp.HandlerFunc {
	if log == nil {
		log = zap.NewNop()
	}
	return func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		data, err := openapi.Spec(cfg.Version, cfg.BackendBaseURL)
		if err != nil {
			log.Error("generate OpenAPI document", zap.Error(err))
			middleware.WriteProblemCode(w, r, stdhttp.StatusInternalServerError, httpx.CodeInternalError, "openapi unavailable")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(stdhttp.StatusOK)
		_, _ = w.Write(data)
	}
}

func DocsHandler(cfg OpenAPIConfig) stdhttp.HandlerFunc {
	return func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(stdhttp.StatusOK)
		_, _ = w.Write(openapi.ScalarHTML("/api/" + cfg.Version + "/openapi.json"))
	}
}
