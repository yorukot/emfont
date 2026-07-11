package tracing

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
	"go.opentelemetry.io/otel/trace"
)

type Config struct {
	ServiceName    string
	ServiceVersion string
	Enabled        bool
	OTLPEndpoint   string
	SampleRatio    float64
	RequireHTTPS   bool
}

type Provider struct {
	provider *sdktrace.TracerProvider
}

type ShutdownFunc func(context.Context) error

var textMapPropagator = propagation.NewCompositeTextMapPropagator(
	propagation.TraceContext{},
	propagation.Baggage{},
)

func NewProvider(ctx context.Context, cfg Config) (*Provider, error) {
	if !cfg.Enabled {
		return &Provider{}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if cfg.ServiceName == "" {
		cfg.ServiceName = "emfont-controller"
	}
	endpoint, err := parseOTLPEndpoint(cfg.OTLPEndpoint, cfg.RequireHTTPS)
	if err != nil {
		return nil, err
	}
	if cfg.SampleRatio < 0 || cfg.SampleRatio > 1 || math.IsNaN(cfg.SampleRatio) || math.IsInf(cfg.SampleRatio, 0) {
		return nil, fmt.Errorf("trace sample ratio %f is outside [0,1]", cfg.SampleRatio)
	}

	exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(endpoint.String()))
	if err != nil {
		return nil, errors.New("create OTLP trace exporter")
	}
	attributes := []attribute.KeyValue{semconv.ServiceName(cfg.ServiceName)}
	if cfg.ServiceVersion != "" {
		attributes = append(attributes, semconv.ServiceVersion(cfg.ServiceVersion))
	}
	serviceResource, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(semconv.SchemaURL, attributes...),
	)
	if err != nil {
		return nil, fmt.Errorf("create trace resource: %w", err)
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(serviceResource),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))),
	)
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(textMapPropagator)
	return &Provider{provider: provider}, nil
}

func parseOTLPEndpoint(endpoint string, requireHTTPS bool) (*url.URL, error) {
	if strings.TrimSpace(endpoint) == "" {
		return nil, errors.New("OTLP endpoint is required when tracing is enabled")
	}

	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, errors.New("OTLP endpoint must be a valid absolute HTTP(S) URL")
	}
	if parsed.User != nil {
		return nil, errors.New("OTLP endpoint must not include user info")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return nil, errors.New("OTLP endpoint must not include a query string")
	}
	if parsed.Fragment != "" || strings.Contains(endpoint, "#") {
		return nil, errors.New("OTLP endpoint must not include a fragment")
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return nil, errors.New("OTLP endpoint must use HTTP or HTTPS")
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return nil, errors.New("OTLP endpoint must include a host")
	}
	if requireHTTPS && !strings.EqualFold(parsed.Scheme, "https") {
		return nil, errors.New("OTLP endpoint must use HTTPS")
	}
	port := parsed.Port()
	if strings.HasSuffix(parsed.Host, ":") || port != "" {
		if _, err := strconv.ParseUint(port, 10, 16); err != nil {
			return nil, errors.New("OTLP endpoint must include a valid port")
		}
	}
	return parsed, nil
}

func (p *Provider) Shutdown(ctx context.Context) error {
	if p == nil || p.provider == nil {
		return nil
	}
	return p.provider.Shutdown(ctx)
}

func Tracer(serviceName string) trace.Tracer {
	if serviceName == "" {
		serviceName = "github.com/emfont/emfont/backend"
	}
	return otel.Tracer(serviceName)
}

func Middleware(tracer trace.Tracer, routeName func(*http.Request) string) func(http.Handler) http.Handler {
	if tracer == nil {
		tracer = Tracer("")
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Public requests are local trace roots. Do not trust client-provided trace context.
			ctx, span := tracer.Start(
				r.Context(),
				r.Method,
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithNewRoot(),
				trace.WithAttributes(
					attribute.String("http.request.method", r.Method),
				),
			)
			defer span.End()

			ww := &responseWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(ww, r.WithContext(ctx))

			route := "unknown"
			if routeName != nil {
				if matchedRoute := routeName(r); matchedRoute != "" {
					route = matchedRoute
				}
			}

			status := ww.status
			span.SetName(r.Method + " " + route)
			span.SetAttributes(
				attribute.String("http.route", route),
				attribute.Int("http.response.status_code", status),
			)
			if status >= http.StatusInternalServerError {
				span.SetStatus(codes.Error, http.StatusText(status))
			}
		})
	}
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

func StatusAttribute(status int) attribute.KeyValue {
	return attribute.String("http.response.status_code", strconv.Itoa(status))
}
