package tracing

import (
	"context"
	"net/http"
	"strconv"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type Config struct {
	ServiceName  string
	Enabled      bool
	OTLPEndpoint string
}

type Provider struct {
	enabled bool
}

type ShutdownFunc func(context.Context) error

func NewProvider(_ context.Context, cfg Config) (*Provider, error) {
	return &Provider{enabled: cfg.Enabled}, nil
}

func (p *Provider) Shutdown(context.Context) error {
	return nil
}

func Tracer(serviceName string) trace.Tracer {
	if serviceName == "" {
		serviceName = "github.com/emfont/emfont/backend"
	}
	return otel.Tracer(serviceName)
}

func Shutdown(context.Context) error {
	return nil
}

func Middleware(tracer trace.Tracer, routeName func(*http.Request) string) func(http.Handler) http.Handler {
	if tracer == nil {
		tracer = Tracer("")
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, span := tracer.Start(
				r.Context(),
				r.Method,
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(
					attribute.String("http.request.method", r.Method),
					attribute.String("url.path", r.URL.Path),
				),
			)
			defer span.End()

			ww := &responseWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(ww, r.WithContext(ctx))

			route := "unknown"
			if routeName != nil {
				route = routeName(r)
			}
			if route == "" {
				route = r.URL.Path
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
