package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emfont/emfont/backend/internal/controller/config"
	httpserver "github.com/emfont/emfont/backend/internal/controller/transport/http"
)

func TestNewWithConfigRejectsNonControllerRoleBeforeOpeningDependencies(t *testing.T) {
	for _, environment := range []string{"cleanup", "migration", "development"} {
		t.Run(environment, func(t *testing.T) {
			_, err := NewWithConfig(context.Background(), config.Config{Environment: environment})
			if err == nil || !strings.Contains(err.Error(), "controller EMFONT_ENV") {
				t.Fatalf("NewWithConfig role error = %v", err)
			}
		})
	}
}

func TestRunReturnsAfterExternalHTTPShutdown(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve address: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release address: %v", err)
	}

	server := httpserver.NewServer(httpserver.ServerConfig{
		Addr: address, ReadTimeout: time.Second, ReadHeaderTimeout: time.Second,
		WriteTimeout: time.Second, IdleTimeout: time.Second, ShutdownTimeout: time.Second,
	}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), nil)
	application := &Application{
		Config:     config.Config{ShutdownTimeout: time.Second},
		HTTPServer: server,
	}
	runResult := make(chan error, 1)
	go func() {
		runResult <- application.Run(context.Background())
	}()

	waitForHTTPServer(t, "http://"+address)
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("external HTTP shutdown: %v", err)
	}
	select {
	case err := <-runResult:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Application.Run did not return after HTTP server stopped")
	}
}

func TestShutdownForceClosesLongRunningHTTPConnectionAtDeadline(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve address: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release address: %v", err)
	}

	requestStarted := make(chan struct{})
	server := httpserver.NewServer(httpserver.ServerConfig{
		Addr: address, ReadTimeout: time.Second, ReadHeaderTimeout: time.Second,
		WriteTimeout: time.Second, IdleTimeout: time.Second, ShutdownTimeout: 50 * time.Millisecond,
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/block" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		close(requestStarted)
		<-r.Context().Done()
		w.WriteHeader(http.StatusServiceUnavailable)
	}), nil)
	application := &Application{
		Config:     config.Config{ShutdownTimeout: 50 * time.Millisecond},
		HTTPServer: server,
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.ListenAndServe() }()
	waitForHTTPServer(t, "http://"+address)

	requestResult := make(chan error, 1)
	go func() {
		response, requestErr := http.Get("http://" + address + "/block")
		if response != nil {
			_ = response.Body.Close()
		}
		requestResult <- requestErr
	}()
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("long-running request did not start")
	}

	started := time.Now()
	err = application.Shutdown(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown error = %v, want context deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("Shutdown took %s, want bounded shutdown", elapsed)
	}
	select {
	case <-requestResult:
	case <-time.After(time.Second):
		t.Fatal("forced HTTP close did not release client")
	}
	select {
	case serveErr := <-serveResult:
		if serveErr != nil {
			t.Fatalf("ListenAndServe: %v", serveErr)
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP server did not stop")
	}
}

func TestShutdownMarksUnreadyPropagatesAndDrainsBeforeFontCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve address: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release address: %v", err)
	}

	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	requestFinished := make(chan struct{})
	server := httpserver.NewServer(httpserver.ServerConfig{
		Addr: address, ReadTimeout: time.Second, ReadHeaderTimeout: time.Second,
		WriteTimeout: time.Second, IdleTimeout: time.Second, ShutdownTimeout: 2 * time.Second,
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			close(requestStarted)
			<-releaseRequest
			w.WriteHeader(http.StatusNoContent)
			close(requestFinished)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}), nil)
	checker := newReadinessChecker(func(context.Context) error { return nil })
	fontShutdown := make(chan struct{})
	propagationStarted := make(chan time.Duration, 1)
	releasePropagation := make(chan struct{})
	application := &Application{
		Config: config.Config{
			ShutdownTimeout:          2 * time.Second,
			ShutdownPropagationDelay: 150 * time.Millisecond,
		},
		HTTPServer: server,
		readiness:  checker,
		waitForPropagation: func(ctx context.Context, delay time.Duration) error {
			propagationStarted <- delay
			select {
			case <-releasePropagation:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
		shutdownFont: func(context.Context) error {
			select {
			case <-requestFinished:
			default:
				t.Error("font service canceled before HTTP request drained")
			}
			close(fontShutdown)
			return nil
		},
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.ListenAndServe() }()
	waitForHTTPServer(t, "http://"+address)

	requestResult := make(chan error, 1)
	go func() {
		response, requestErr := http.Get("http://" + address + "/slow")
		if response != nil {
			_ = response.Body.Close()
		}
		requestResult <- requestErr
	}()
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("slow request did not start")
	}

	shutdownResult := make(chan error, 1)
	go func() { shutdownResult <- application.Shutdown(context.Background()) }()
	select {
	case delay := <-propagationStarted:
		if delay != application.Config.ShutdownPropagationDelay {
			t.Fatalf("propagation delay = %s, want %s", delay, application.Config.ShutdownPropagationDelay)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not enter readiness propagation")
	}
	if err := checker.Check(context.Background()); !errors.Is(err, errReadinessDraining) {
		t.Fatalf("readiness after shutdown started = %v", err)
	}

	response, err := http.Get("http://" + address + "/alive")
	if err != nil {
		t.Fatalf("HTTP server unavailable during propagation delay: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status during propagation = %d, want %d", response.StatusCode, http.StatusNoContent)
	}

	close(releasePropagation)
	deadline := time.Now().Add(time.Second)
	for server.IsServing() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if server.IsServing() {
		t.Fatal("HTTP listener did not stop after propagation")
	}
	select {
	case <-fontShutdown:
		t.Fatal("font service canceled while HTTP request was active")
	default:
	}
	close(releaseRequest)
	select {
	case err := <-requestResult:
		if err != nil {
			t.Fatalf("slow request: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("slow request did not drain")
	}
	select {
	case err := <-shutdownResult:
		if err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not finish")
	}
	select {
	case <-fontShutdown:
	default:
		t.Fatal("font service was not shut down")
	}
	select {
	case err := <-serveResult:
		if err != nil {
			t.Fatalf("ListenAndServe: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP server did not stop")
	}
}

func TestShutdownIsIdempotentUnderConcurrentCalls(t *testing.T) {
	checker := newReadinessChecker(func(context.Context) error { return nil })
	var shutdownCalls atomic.Int32
	application := &Application{
		Config:    config.Config{ShutdownTimeout: time.Second},
		readiness: checker,
		shutdownFont: func(context.Context) error {
			shutdownCalls.Add(1)
			time.Sleep(10 * time.Millisecond)
			return nil
		},
	}

	const callers = 32
	start := make(chan struct{})
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			<-start
			errs <- application.Shutdown(context.Background())
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	}
	if got := shutdownCalls.Load(); got != 1 {
		t.Fatalf("font shutdown calls = %d, want 1", got)
	}
	if err := checker.Check(context.Background()); !errors.Is(err, errReadinessDraining) {
		t.Fatalf("readiness after shutdown = %v", err)
	}
}

func TestBuildAggregateRateLimitEnforcesProcessLimit(t *testing.T) {
	rateLimit, err := buildAggregateRateLimit(config.Config{RateLimit: config.RateLimitConfig{
		Enabled:                 true,
		GlobalRequestsPerSecond: 1, GlobalBurst: 2,
		IdleTimeout: time.Minute,
	}})
	if err != nil {
		t.Fatalf("buildAggregateRateLimit: %v", err)
	}
	if rateLimit == nil {
		t.Fatal("buildAggregateRateLimit returned nil middleware")
	}
	handler := rateLimit(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for i := range 2 {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.RemoteAddr = "198.51.100." + strconv.Itoa(i+1) + ":1234"
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("request %d status = %d, want %d", i, recorder.Code, http.StatusNoContent)
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "198.51.100.3:1234"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("global-limited request status = %d, want %d", recorder.Code, http.StatusTooManyRequests)
	}
	if got := recorder.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q, want %q", got, "1")
	}
}

func TestBuildHealthRateLimitKeepsSourcesIndependent(t *testing.T) {
	rateLimit, err := buildHealthRateLimit(config.Config{})
	if err != nil {
		t.Fatalf("buildHealthRateLimit: %v", err)
	}
	handler := rateLimit(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for range 10 {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/readyz", nil)
		request.RemoteAddr = "198.51.100.1:1234"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent {
			t.Fatalf("first source status = %d, want %d", response.Code, http.StatusNoContent)
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/readyz", nil)
	request.RemoteAddr = "198.51.100.1:1234"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("limited source status = %d, want %d", response.Code, http.StatusTooManyRequests)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/readyz", nil)
	request.RemoteAddr = "198.51.100.2:1234"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("independent source status = %d, want %d", response.Code, http.StatusNoContent)
	}
}

func TestBuildHealthRateLimitReservesPrivateReadinessCapacity(t *testing.T) {
	rateLimit, err := buildHealthRateLimit(config.Config{})
	if err != nil {
		t.Fatalf("buildHealthRateLimit: %v", err)
	}
	handler := rateLimit(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	limited := false
	for requestNumber := range 400 {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/livez", nil)
		request.RemoteAddr = "198.51.100." + strconv.Itoa(requestNumber/10+1) + ":1234"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
		if response.Code != http.StatusNoContent {
			t.Fatalf("liveness request %d status = %d, want %d", requestNumber, response.Code, http.StatusNoContent)
		}
	}
	if !limited {
		t.Fatal("public liveness did not reach its process-wide limit")
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/readyz", nil)
	request.RemoteAddr = "198.51.100.250:1234"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("reserved readiness status = %d, want %d", response.Code, http.StatusNoContent)
	}
}

func TestBuildMetricsRateLimitIsIndependentOfPublicRateLimit(t *testing.T) {
	rateLimit, err := buildMetricsRateLimit(config.Config{
		Metrics: config.MetricsConfig{
			Enabled: true, AuthRequestsPerSecond: 0.001, AuthBurst: 1,
		},
		RateLimit: config.RateLimitConfig{Enabled: false, IdleTimeout: time.Minute},
	})
	if err != nil {
		t.Fatalf("buildMetricsRateLimit: %v", err)
	}
	if rateLimit == nil {
		t.Fatal("buildMetricsRateLimit returned nil middleware")
	}
	handler := rateLimit(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	assertAppHandlerStatus(t, handler, http.StatusNoContent)
	assertAppHandlerStatus(t, handler, http.StatusTooManyRequests)
}

func TestBuildFontRateLimitKeepsPerClientBucketsIndependent(t *testing.T) {
	rateLimit, err := buildFontRateLimit(config.Config{RateLimit: config.RateLimitConfig{
		Enabled: true, RequestsPerSecond: 1, Burst: 1,
		GlobalRequestsPerSecond: 0.001, GlobalBurst: 1,
		MaxClients: 10, IdleTimeout: time.Minute,
	}})
	if err != nil {
		t.Fatalf("buildFontRateLimit: %v", err)
	}
	handler := rateLimit(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for index, remoteAddr := range []string{"198.51.100.1:1234", "198.51.100.2:1234", "198.51.100.1:1234"} {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.RemoteAddr = remoteAddr
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		want := http.StatusNoContent
		if index == 2 {
			want = http.StatusTooManyRequests
		}
		if response.Code != want {
			t.Fatalf("request %d status = %d, want %d", index, response.Code, want)
		}
	}
}

func TestBuildFontRateLimitDoesNotTrustDirectClientForwardingHeader(t *testing.T) {
	rateLimit, err := buildFontRateLimit(config.Config{RateLimit: config.RateLimitConfig{
		Enabled: true, RequestsPerSecond: 1, Burst: 1, MaxClients: 10, IdleTimeout: time.Minute,
		TrustProxyHeaders: true,
		TrustedProxyCIDRs: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
	}})
	if err != nil {
		t.Fatalf("buildFontRateLimit: %v", err)
	}
	handler := rateLimit(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for index, forwardedFor := range []string{"203.0.113.1", "203.0.113.2"} {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.RemoteAddr = "198.51.100.10:1234"
		request.Header.Set("X-Forwarded-For", forwardedFor)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		want := http.StatusNoContent
		if index == 1 {
			want = http.StatusTooManyRequests
		}
		if response.Code != want {
			t.Fatalf("request %d status = %d, want %d", index, response.Code, want)
		}
	}
}

func TestBuildFontRateLimitRejectsTrustedHeadersWithoutCIDRs(t *testing.T) {
	_, err := buildFontRateLimit(config.Config{RateLimit: config.RateLimitConfig{
		Enabled: true, RequestsPerSecond: 1, Burst: 1, MaxClients: 1,
		IdleTimeout: time.Minute, TrustProxyHeaders: true,
	}})
	if err == nil {
		t.Fatal("buildFontRateLimit error = nil")
	}
}

func TestFontRepositoryConfigUsesBuildCapacityLimits(t *testing.T) {
	got := fontRepositoryConfig(config.Config{FontBuild: config.FontBuildConfig{
		MaxArtifacts: 123, MaxAccountedBytes: 456, WorkerMaxOutputBytes: 78, MaxTerminalFailures: 90,
	}})
	if got.MaxArtifacts != 123 || got.MaxAccountedBytes != 456 ||
		got.ArtifactReservation != 78 || got.MaxTerminalFailures != 90 {
		t.Fatalf("font repository config = %+v", got)
	}
}

func assertAppHandlerStatus(t *testing.T, handler http.Handler, want int) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != want {
		t.Fatalf("status = %d, want %d", response.Code, want)
	}
}

func waitForHTTPServer(t *testing.T, endpoint string) {
	t.Helper()
	client := &http.Client{Timeout: 100 * time.Millisecond}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(endpoint)
		if err == nil {
			_ = response.Body.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("HTTP server %s did not start", endpoint)
}
