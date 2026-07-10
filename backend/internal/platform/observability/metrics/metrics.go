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
	registry *prometheus.Registry
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	inFlight prometheus.Gauge
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

	collectorsToRegister := []prometheus.Collector{requests, duration, inFlight}
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
		registry: registry,
		requests: requests,
		duration: duration,
		inFlight: inFlight,
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

func (m *Metrics) Middleware(routeName func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if m == nil {
			return next
		}

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			ww := &responseWriter{ResponseWriter: w, status: http.StatusOK}

			m.inFlight.Inc()
			defer func() {
				m.inFlight.Dec()

				route := "unknown"
				if routeName != nil {
					route = routeName(r)
				}
				if route == "" {
					route = r.URL.Path
				}
				status := strconv.Itoa(ww.status)

				m.requests.WithLabelValues(r.Method, route, status).Inc()
				m.duration.WithLabelValues(r.Method, route, status).Observe(time.Since(started).Seconds())
			}()

			next.ServeHTTP(ww, r)
		})
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
