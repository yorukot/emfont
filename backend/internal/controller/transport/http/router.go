package http

import (
	"context"
	stdhttp "net/http"
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
	Log            *zap.Logger
	APIVersion     string
	ServiceName    string
	Version        string
	BackendBaseURL string
	RequestTimeout time.Duration
	ReadinessCheck func(context.Context) error
	Metrics        *metrics.Metrics
	MetricsPath    string
	FontService    *appfont.Service
	SystemService  *appsystem.Service
	OpenAPI        OpenAPIConfig
	Tracing        bool
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
	if dep.OpenAPI.Version == "" {
		dep.OpenAPI.Version = dep.APIVersion
	}
	if dep.OpenAPI.BackendBaseURL == "" {
		dep.OpenAPI.BackendBaseURL = dep.BackendBaseURL
	}

	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.SecurityHeaders(middleware.DefaultSecurityConfig()))
	router.Use(middleware.Timeout(dep.RequestTimeout))
	router.Use(middleware.Logging(dep.Log))
	if dep.Metrics != nil {
		router.Use(dep.Metrics.Middleware(RoutePattern))
	}
	if dep.Tracing {
		router.Use(tracing.Middleware(tracing.Tracer(dep.ServiceName), RoutePattern))
	}
	router.Use(middleware.Recovery(dep.Log))

	if dep.Metrics != nil {
		router.Handle(dep.MetricsPath, dep.Metrics.Handler())
	}

	basePath := "/api/" + dep.APIVersion
	fontHandler := fonthttp.NewHandler(dep.FontService)
	router.Route(basePath, func(api chi.Router) {
		api.Get("/healthz", HealthHandler(HealthConfig{
			ServiceName:    dep.ServiceName,
			Version:        dep.Version,
			ReadinessCheck: dep.ReadinessCheck,
		}))
		api.Get("/openapi.json", OpenAPIHandler(dep.OpenAPI))
		api.Get("/docs", DocsHandler(dep.OpenAPI))
		systemhttp.NewHandler(dep.SystemService).RegisterRoutes(api)
		fontHandler.RegisterRoutes(api)
	})
	fontHandler.RegisterRoutes(router)

	router.NotFound(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		middleware.WriteProblemCode(w, r, stdhttp.StatusNotFound, httpx.CodeRouteNotFound, "route not found")
	})
	router.MethodNotAllowed(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		middleware.WriteProblemCode(w, r, stdhttp.StatusMethodNotAllowed, httpx.CodeMethodNotAllowed, "method not allowed")
	})

	return router
}

func RoutePattern(r *stdhttp.Request) string {
	routeContext := chi.RouteContext(r.Context())
	if routeContext == nil {
		return ""
	}
	return routeContext.RoutePattern()
}

func OpenAPIHandler(cfg OpenAPIConfig) stdhttp.HandlerFunc {
	return func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		data, err := openapi.Spec(cfg.Version, cfg.BackendBaseURL)
		if err != nil {
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
