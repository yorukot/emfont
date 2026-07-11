package tracing

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestProviderExportsCompletedSpan(t *testing.T) {
	var requests atomic.Int32
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/traces" {
			t.Errorf("collector path = %q, want /v1/traces", r.URL.Path)
		}
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Errorf("read collector body: %v", err)
		}
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()

	provider, err := NewProvider(context.Background(), Config{
		Enabled: true, ServiceName: "emfont-test", ServiceVersion: "test",
		OTLPEndpoint: collector.URL, SampleRatio: 1,
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	_, span := Tracer("emfont-test").Start(context.Background(), "completed-operation")
	span.End()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := provider.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if requests.Load() == 0 {
		t.Fatal("collector received no trace export")
	}
}

func TestMiddlewareDoesNotTrustInboundTraceparentForSampling(t *testing.T) {
	collector := httptest.NewServer(http.NotFoundHandler())
	defer collector.Close()

	provider, err := NewProvider(context.Background(), Config{
		Enabled: true, OTLPEndpoint: collector.URL, SampleRatio: 0,
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	defer func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	}()

	var spanContext trace.SpanContext
	handler := Middleware(Tracer("sample-ratio-test"), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		spanContext = trace.SpanContextFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if spanContext.IsSampled() {
		t.Fatal("inbound sampled traceparent forced a span to be sampled with local sample ratio 0")
	}
	if spanContext.TraceID().String() == "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatal("inbound traceparent was used as the request trace root")
	}
}

func TestMiddlewareDoesNotRecordRawUnmatchedPath(t *testing.T) {
	spanRecorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(spanRecorder),
	)
	defer func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	}()

	const rawPath = "/missing/9b8b8d23-1e74-4bc2-bc61-7704c5f3eb87"
	handler := Middleware(provider.Tracer("unmatched-path-test"), func(*http.Request) string {
		return ""
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, rawPath, nil))

	spans := spanRecorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	for _, keyValue := range spans[0].Attributes() {
		if keyValue.Key == "url.path" {
			t.Fatalf("raw URL path attribute was recorded: %v", keyValue)
		}
		if keyValue.Value.Type() == attribute.STRING && keyValue.Value.AsString() == rawPath {
			t.Fatalf("raw unmatched path was recorded in attribute %q", keyValue.Key)
		}
	}
}

func TestNewProviderRejectsUnsafeOTLPEndpointsWithoutLeakingThem(t *testing.T) {
	const sentinel = "OTLP_ENDPOINT_SECRET_4b829e"

	for _, test := range []struct {
		name     string
		endpoint string
		want     string
	}{
		{name: "malformed", endpoint: "http://collector.example.test/%zz", want: "valid absolute HTTP(S) URL"},
		{name: "unsupported scheme", endpoint: "grpc://collector.example.test:4317", want: "must use HTTP or HTTPS"},
		{name: "missing host", endpoint: "http:///v1/traces", want: "must include a host"},
		{name: "invalid port", endpoint: "http://collector.example.test:99999", want: "must include a valid port"},
		{name: "username", endpoint: "http://audit_user@collector.example.test:4318", want: "must not include user info"},
		{name: "password", endpoint: "http://audit_user:" + sentinel + "@collector.example.test:4318", want: "must not include user info"},
		{name: "query", endpoint: "http://collector.example.test:4318?token=" + sentinel, want: "must not include a query string"},
		{name: "empty query", endpoint: "http://collector.example.test:4318?", want: "must not include a query string"},
		{name: "fragment", endpoint: "http://collector.example.test:4318#" + sentinel, want: "must not include a fragment"},
		{name: "empty fragment", endpoint: "http://collector.example.test:4318#", want: "must not include a fragment"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewProvider(context.Background(), Config{
				Enabled: true, OTLPEndpoint: test.endpoint, SampleRatio: 1,
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewProvider error = %v, want error containing %q", err, test.want)
			}
			assertTracingStartupErrorOmits(t, err, test.endpoint, sentinel)
		})
	}
}

func TestNewProviderRejectsNonFiniteSampleRatios(t *testing.T) {
	for _, ratio := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		_, err := NewProvider(context.Background(), Config{
			Enabled: true, OTLPEndpoint: "http://collector.example:4318", SampleRatio: ratio,
		})
		if err == nil || !strings.Contains(err.Error(), "sample ratio") {
			t.Fatalf("sample ratio %v error = %v", ratio, err)
		}
	}
}

func TestNewProviderRequiresHTTPSWhenConfigured(t *testing.T) {
	_, err := NewProvider(context.Background(), Config{
		Enabled: true, OTLPEndpoint: "http://collector.example:4318", SampleRatio: 1,
		RequireHTTPS: true,
	})
	if err == nil || !strings.Contains(err.Error(), "must use HTTPS") {
		t.Fatalf("HTTP endpoint error = %v", err)
	}

	provider, err := NewProvider(context.Background(), Config{
		Enabled: true, OTLPEndpoint: "https://collector.example:4318", SampleRatio: 1,
		RequireHTTPS: true,
	})
	if err != nil {
		t.Fatalf("HTTPS endpoint: %v", err)
	}
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestOTLPEndpointAuditReproductionDoesNotLeakToOTelLogs(t *testing.T) {
	const (
		helperEnvironment = "EMFONT_TEST_OTLP_AUDIT_HELPER"
		sentinel          = "OTLP_AUDIT_SECRET_31dc75"
	)
	endpoint := "http://audit_user:" + sentinel + "@collector.example.test/%zz"

	if os.Getenv(helperEnvironment) == "1" {
		provider, err := NewProvider(context.Background(), Config{
			Enabled: true, OTLPEndpoint: endpoint, SampleRatio: 1,
		})
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "startup failed: %v\n", err)
			return
		}
		_ = provider.Shutdown(context.Background())
		_, _ = fmt.Fprintln(os.Stderr, "startup unexpectedly accepted unsafe OTLP endpoint")
		return
	}

	command := exec.Command(os.Args[0], "-test.run=^TestOTLPEndpointAuditReproductionDoesNotLeakToOTelLogs$")
	command.Env = append(environmentWithoutPrefix(os.Environ(), "OTEL_"), helperEnvironment+"=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("audit reproduction helper failed: %v\n%s", err, output)
	}
	if strings.Contains(string(output), sentinel) || strings.Contains(string(output), endpoint) {
		t.Fatalf("OTel or startup log leaked the audit endpoint credential: %s", output)
	}
	if !strings.Contains(string(output), "startup failed: OTLP endpoint") {
		t.Fatalf("audit reproduction did not return the safe endpoint error: %s", output)
	}
}

func assertTracingStartupErrorOmits(t *testing.T, err error, forbidden ...string) {
	t.Helper()

	var startupLog bytes.Buffer
	_, _ = fmt.Fprintf(&startupLog, "startup failed: %v\n", err)
	for _, value := range forbidden {
		if value == "" {
			continue
		}
		if strings.Contains(err.Error(), value) {
			t.Fatalf("error leaked forbidden OTLP endpoint content %q: %v", value, err)
		}
		if strings.Contains(startupLog.String(), value) {
			t.Fatalf("startup log leaked forbidden OTLP endpoint content %q: %s", value, startupLog.String())
		}
	}
}

func environmentWithoutPrefix(environment []string, prefix string) []string {
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}
