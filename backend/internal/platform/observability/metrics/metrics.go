package metrics

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Config struct {
	Namespace      string
	Subsystem      string
	Service        string
	Version        string
	IncludeRuntime bool
}

type Metrics struct {
	registry        *prometheus.Registry
	requests        *prometheus.CounterVec
	duration        *prometheus.HistogramVec
	inFlight        prometheus.Gauge
	fontCache       *prometheus.CounterVec
	fontAdmissions  *prometheus.CounterVec
	fontBuildActive prometheus.Gauge
	fontBuildQueued prometheus.Gauge
	fontLeases      *prometheus.CounterVec
	fontBuilds      *prometheus.CounterVec
	fontBuildTime   *prometheus.HistogramVec
}

func New(cfg Config) (*Metrics, error) {
	if cfg.Namespace == "" {
		cfg.Namespace = "emfont"
	}
	if cfg.Subsystem == "" {
		cfg.Subsystem = "http"
	}

	registry := prometheus.NewRegistry()
	requests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: cfg.Namespace,
		Subsystem: cfg.Subsystem,
		Name:      "requests_total",
		Help:      "Total HTTP requests.",
	}, []string{"method", "route", "status"})
	duration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: cfg.Namespace,
		Subsystem: cfg.Subsystem,
		Name:      "request_duration_seconds",
		Help:      "HTTP request duration in seconds.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"method", "route", "status"})
	inFlight := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: cfg.Namespace,
		Subsystem: cfg.Subsystem,
		Name:      "requests_in_flight",
		Help:      "Current number of in-flight HTTP requests.",
	})
	fontCache := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: cfg.Namespace, Subsystem: "font", Name: "cache_lookups_total",
		Help: "Font artifact cache lookups by result.",
	}, []string{"result"})
	fontAdmissions := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: cfg.Namespace, Subsystem: "font", Name: "build_admissions_total",
		Help: "Font build admission decisions.",
	}, []string{"result"})
	fontBuildActive := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: cfg.Namespace, Subsystem: "font", Name: "builds_active",
		Help: "Font builds currently holding a local build slot.",
	})
	fontBuildQueued := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: cfg.Namespace, Subsystem: "font", Name: "builds_queued",
		Help: "Admitted font builds waiting for a local build slot.",
	})
	fontLeases := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: cfg.Namespace, Subsystem: "font", Name: "build_leases_total",
		Help: "PostgreSQL font build lease acquisition results.",
	}, []string{"result"})
	fontBuilds := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: cfg.Namespace, Subsystem: "font", Name: "builds_total",
		Help: "Completed font build attempts by kind and outcome.",
	}, []string{"kind", "outcome"})
	fontBuildTime := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: cfg.Namespace, Subsystem: "font", Name: "build_duration_seconds",
		Help:    "Font build attempt duration in seconds.",
		Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 90},
	}, []string{"kind", "outcome"})

	collectorsToRegister := []prometheus.Collector{
		requests, duration, inFlight, fontCache, fontAdmissions,
		fontBuildActive, fontBuildQueued, fontLeases, fontBuilds, fontBuildTime,
	}
	if cfg.Service != "" || cfg.Version != "" {
		buildInfo := prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: cfg.Namespace,
			Name:      "build_info",
			Help:      "Build and service metadata.",
		}, []string{"service", "version"})
		buildInfo.WithLabelValues(cfg.Service, cfg.Version).Set(1)
		collectorsToRegister = append(collectorsToRegister, buildInfo)
	}
	if cfg.IncludeRuntime {
		collectorsToRegister = append(collectorsToRegister,
			collectors.NewGoCollector(),
			collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		)
	}

	if err := register(registry, collectorsToRegister...); err != nil {
		return nil, err
	}

	return &Metrics{
		registry: registry, requests: requests, duration: duration, inFlight: inFlight,
		fontCache: fontCache, fontAdmissions: fontAdmissions,
		fontBuildActive: fontBuildActive, fontBuildQueued: fontBuildQueued,
		fontLeases: fontLeases, fontBuilds: fontBuilds, fontBuildTime: fontBuildTime,
	}, nil
}

func MustNew(cfg Config) *Metrics {
	metrics, err := New(cfg)
	if err != nil {
		panic(err)
	}
	return metrics
}

func (m *Metrics) Handler() http.Handler {
	if m == nil || m.registry == nil {
		return promhttp.Handler()
	}
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	})
}

func (m *Metrics) Shutdown(context.Context) error {
	return nil
}

func (m *Metrics) ObserveFontCache(result string) {
	if m != nil && m.fontCache != nil {
		m.fontCache.WithLabelValues(boundedLabel(result, "hit", "miss", "error")).Inc()
	}
}

func (m *Metrics) ObserveFontBuildAdmission(result string) {
	if m != nil && m.fontAdmissions != nil {
		m.fontAdmissions.WithLabelValues(boundedLabel(result, "accepted", "rejected")).Inc()
	}
}

func (m *Metrics) ObserveFontBuildQueue(active, queued int) {
	if m == nil {
		return
	}
	if active < 0 {
		active = 0
	}
	if queued < 0 {
		queued = 0
	}
	if m.fontBuildActive != nil {
		m.fontBuildActive.Set(float64(active))
	}
	if m.fontBuildQueued != nil {
		m.fontBuildQueued.Set(float64(queued))
	}
}

func (m *Metrics) ObserveFontBuildLease(result string) {
	if m != nil && m.fontLeases != nil {
		m.fontLeases.WithLabelValues(boundedLabel(result, "acquired", "contended", "error")).Inc()
	}
}

func (m *Metrics) ObserveFontBuild(kind, outcome string, duration time.Duration) {
	if m == nil {
		return
	}
	kind = boundedLabel(kind, "dynamic", "static", "unknown")
	outcome = boundedLabel(outcome, "success", "contended", "timeout", "canceled", "storage_error", "unsupported", "failed", "error")
	if m.fontBuilds != nil {
		m.fontBuilds.WithLabelValues(kind, outcome).Inc()
	}
	if m.fontBuildTime != nil {
		m.fontBuildTime.WithLabelValues(kind, outcome).Observe(duration.Seconds())
	}
}

func (m *Metrics) Middleware(routeName func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if m == nil {
			return next
		}

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			ww := &responseWriter{ResponseWriter: w, status: http.StatusOK}
			method := normalizedHTTPMethod(r.Method)

			m.inFlight.Inc()
			defer func() {
				m.inFlight.Dec()

				route := "unknown"
				if routeName != nil {
					if matchedRoute := routeName(r); matchedRoute != "" {
						route = matchedRoute
					}
				}
				status := strconv.Itoa(ww.status)

				m.requests.WithLabelValues(method, route, status).Inc()
				m.duration.WithLabelValues(method, route, status).Observe(time.Since(started).Seconds())
			}()

			next.ServeHTTP(ww, r)
		})
	}
}

func normalizedHTTPMethod(method string) string {
	switch method {
	case http.MethodGet,
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
		http.MethodHead,
		http.MethodOptions:
		return method
	default:
		return "OTHER"
	}
}

func register(registry *prometheus.Registry, collectors ...prometheus.Collector) error {
	for _, collector := range collectors {
		if err := registry.Register(collector); err != nil {
			if _, ok := err.(prometheus.AlreadyRegisteredError); ok {
				continue
			}
			return fmt.Errorf("register prometheus collector: %w", err)
		}
	}
	return nil
}

func boundedLabel(value string, allowed ...string) string {
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}
	return "unknown"
}

type responseWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *responseWriter) WriteHeader(status int) {
	if w.wrote {
		return
	}
	w.status = status
	w.wrote = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseWriter) Write(body []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}
