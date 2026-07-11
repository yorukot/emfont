package harfbuzz

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	appfont "github.com/emfont/emfont/backend/internal/controller/application/font"
	"github.com/emfont/emfont/backend/internal/controller/infrastructure/fontbuild/harfbuzz/protocol"
	"github.com/emfont/emfont/backend/internal/domain/font"
)

var (
	testBinariesDir string
	actualWorker    string
	actualWorkerErr error
	workerBuildOnce sync.Once
)

func TestMain(m *testing.M) {
	code := m.Run()
	if testBinariesDir != "" {
		_ = os.RemoveAll(testBinariesDir)
	}
	os.Exit(code)
}

func TestBuildSubsetProducesDeterministicWOFF2(t *testing.T) {
	source := readFixture(t)
	builder := newActualBuilder(t)
	if err := builder.Available(); err != nil {
		t.Fatalf("Available: %v", err)
	}
	if version := builder.Version(); !strings.Contains(version, "harfbuzz-") ||
		!strings.Contains(version, "woff2-") || !strings.Contains(version, protocol.Identity) {
		t.Fatalf("Version() = %q, want native and protocol identities", version)
	} else if os.Getenv("EMFONT_TEST_FONT_WORKER_PATH") == "" &&
		!strings.Contains(version, "-worker-test-source-digest-") {
		t.Fatalf("Version() = %q, want linker-injected worker revision", version)
	}

	input := appfont.BuildInput{
		Source: source, Codepoints: []rune("Hello"), SourceFormat: "ttf", TargetFormat: font.OutputFormatWOFF2,
	}
	output, err := builder.BuildSubset(context.Background(), input)
	if err != nil {
		t.Fatalf("BuildSubset: %v", err)
	}
	if !bytes.HasPrefix(output.Data, []byte("wOF2")) {
		t.Fatalf("output magic = %q, want wOF2", output.Data[:min(4, len(output.Data))])
	}
	if output.GlyphCount == 0 || output.BuilderVersion != builder.Version() {
		t.Fatalf("output metadata = %#v; builder version = %q", output, builder.Version())
	}
	digest := sha256.Sum256(output.Data)
	t.Logf("worker=%s output_sha256=%s output_bytes=%d glyphs=%d", output.BuilderVersion, hex.EncodeToString(digest[:]), len(output.Data), output.GlyphCount)
	for iteration := 0; iteration < 4; iteration++ {
		rebuilt, err := builder.BuildSubset(context.Background(), input)
		if err != nil {
			t.Fatalf("repeat BuildSubset %d: %v", iteration, err)
		}
		if !bytes.Equal(rebuilt.Data, output.Data) {
			t.Fatalf("repeat BuildSubset %d produced different bytes", iteration)
		}
	}
}

func TestProductionIdentityRequiredAtReadinessAndUse(t *testing.T) {
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		t.Skip("production fontworker images support linux/amd64 and linux/arm64")
	}
	cfg := DefaultConfig()
	cfg.RequireProductionIdentity = true
	builder, err := NewWithConfig(cfg)
	if err != nil {
		t.Fatalf("NewWithConfig: %v", err)
	}
	valid := "harfbuzz-10.2.0-woff2-1.0.2-worker-linux-" + runtime.GOARCH + "-go1.26.5-" +
		"hb-10.2.0-1+deb13u1-w2-1.0.2-2+b2-src-" + strings.Repeat("a", 64) +
		"-pkg-" + strings.Repeat("b", 64) + "-" + protocol.Identity
	if err := builder.validateVersion(valid); err != nil {
		t.Fatalf("validate production worker identity: %v", err)
	}
	for _, invalid := range []string{
		"development-" + protocol.Identity,
		strings.Replace(valid, "-pkg-", "-package-", 1),
		strings.TrimSuffix(valid, "-"+protocol.Identity),
		strings.Replace(valid, "-worker-linux-"+runtime.GOARCH+"-", "-worker-linux-"+otherArchitecture(runtime.GOARCH)+"-", 1),
	} {
		if err := builder.validateVersion(invalid); err == nil {
			t.Fatalf("validateVersion accepted %q", invalid)
		}
	}
}

func otherArchitecture(architecture string) string {
	if architecture == "amd64" {
		return "arm64"
	}
	return "amd64"
}

func TestBuildSubsetSupportsParallelWorkers(t *testing.T) {
	source := readFixture(t)
	builder := newActualBuilder(t)
	input := appfont.BuildInput{
		Source: source, Codepoints: []rune("Parallel"), SourceFormat: "ttf", TargetFormat: font.OutputFormatWOFF2,
	}

	const jobs = 4
	outputs := make(chan []byte, jobs)
	errs := make(chan error, jobs)
	var workers sync.WaitGroup
	for range jobs {
		workers.Add(1)
		go func() {
			defer workers.Done()
			output, err := builder.BuildSubset(context.Background(), input)
			if err != nil {
				errs <- err
				return
			}
			outputs <- output.Data
		}()
	}
	workers.Wait()
	close(outputs)
	close(errs)
	for err := range errs {
		t.Errorf("parallel BuildSubset: %v", err)
	}
	var expected []byte
	for output := range outputs {
		if expected == nil {
			expected = output
			continue
		}
		if !bytes.Equal(output, expected) {
			t.Fatal("parallel workers produced different output")
		}
	}
	if len(expected) == 0 {
		t.Fatal("parallel workers returned no output")
	}
}

func TestBuildSubsetRejectsPartiallyUnsupportedCodepoints(t *testing.T) {
	source := readFixture(t)
	_, err := newActualBuilder(t).BuildSubset(context.Background(), appfont.BuildInput{
		Source: source, Codepoints: []rune{'H', rune(0x10FFFF)}, SourceFormat: "ttf", TargetFormat: font.OutputFormatWOFF2,
	})
	if !errors.Is(err, appfont.ErrUnsupportedCodepoints) {
		t.Fatalf("BuildSubset error = %v, want ErrUnsupportedCodepoints", err)
	}
}

func TestBuildSubsetDoesNotLeakMalformedSource(t *testing.T) {
	const secret = "private-source-marker-3956"
	_, err := newActualBuilder(t).BuildSubset(context.Background(), appfont.BuildInput{
		Source: []byte(secret), Codepoints: []rune{'A'}, SourceFormat: "ttf", TargetFormat: font.OutputFormatWOFF2,
	})
	if err == nil {
		t.Fatal("BuildSubset returned nil error for malformed source")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("BuildSubset leaked source bytes: %v", err)
	}
}

func TestBuildSubsetRejectsMalformedWorkerResponse(t *testing.T) {
	path, err := exec.LookPath("echo")
	if err != nil {
		t.Skip("echo is not installed")
	}
	cfg := DefaultConfig()
	cfg.WorkerPath = path
	builder, err := NewWithConfig(cfg)
	if err != nil {
		t.Fatalf("NewWithConfig: %v", err)
	}
	_, err = builder.BuildSubset(context.Background(), appfont.BuildInput{
		Source: []byte("font"), Codepoints: []rune{'A'}, TargetFormat: font.OutputFormatWOFF2,
	})
	if err == nil || !strings.Contains(err.Error(), "decode font worker response") {
		t.Fatalf("BuildSubset error = %v, want malformed response error", err)
	}
}

func TestActualWorkerRejectsTrailingRequestData(t *testing.T) {
	builder := newActualBuilder(t)
	var request bytes.Buffer
	if err := protocol.EncodeRequest(&request, protocol.Request{
		Operation: protocol.OperationSubset, Source: []byte("font"),
		Codepoints: []rune{'A'}, TargetFormat: protocol.FormatWOFF2,
	}, builder.config.MaxSourceBytes); err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	request.WriteByte(0)
	command := exec.Command(actualWorkerPath(t), builder.workerArguments()...)
	command.Stdin = &request
	responseData, err := command.Output()
	if err != nil {
		t.Fatalf("run actual worker: %v", err)
	}
	response, err := protocol.DecodeResponse(bytes.NewReader(responseData), builder.config.MaxOutputBytes)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if response.Status != protocol.StatusInvalidRequest || response.Message != "malformed worker request" {
		t.Fatalf("worker response = %#v", response)
	}
}

func TestBuildSubsetKillsWorkerThatExceedsStderrLimit(t *testing.T) {
	helper := buildTestBinary(t, "./internal/controller/infrastructure/fontbuild/harfbuzz/testdata/noisyworker.go", "noisyworker")
	cfg := DefaultConfig()
	cfg.WorkerPath = helper
	cfg.StderrLimitBytes = 1024
	builder, err := NewWithConfig(cfg)
	if err != nil {
		t.Fatalf("NewWithConfig: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = builder.BuildSubset(ctx, appfont.BuildInput{
		Source: []byte("font"), Codepoints: []rune{'A'}, TargetFormat: font.OutputFormatWOFF2,
	})
	if err == nil || !strings.Contains(err.Error(), "stderr exceeded 1024 bytes") {
		t.Fatalf("BuildSubset error = %v, want stderr limit", err)
	}
}

func TestBuildSubsetTimeoutKillsAndReapsWorker(t *testing.T) {
	helper := buildTestBinary(t, "./internal/controller/infrastructure/fontbuild/harfbuzz/testdata/hangworker.go", "hangworker")
	pidPath := helper + ".pid"
	t.Setenv("EMFONT_TEST_WORKER_SECRET", "must-not-reach-worker")
	cfg := DefaultConfig()
	cfg.WorkerPath = helper
	builder, err := NewWithConfig(cfg)
	if err != nil {
		t.Fatalf("NewWithConfig: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = builder.BuildSubset(ctx, appfont.BuildInput{
		Source: []byte("font"), Codepoints: []rune{'A'}, TargetFormat: font.OutputFormatWOFF2,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("BuildSubset error = %v, want context deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("timed-out worker returned after %s", elapsed)
	}

	pidData, readErr := os.ReadFile(pidPath)
	if readErr != nil {
		t.Fatalf("read helper PID: %v", readErr)
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if parseErr != nil {
		t.Fatalf("parse helper PID: %v", parseErr)
	}
	if _, statErr := os.Stat(filepath.Join("/proc", strconv.Itoa(pid))); !os.IsNotExist(statErr) {
		t.Fatalf("worker PID %d still exists after BuildSubset returned: %v", pid, statErr)
	}
	if _, statErr := os.Stat(helper + ".leaked"); !os.IsNotExist(statErr) {
		t.Fatalf("worker inherited the controller environment: %v", statErr)
	}
}

func TestLimitedCaptureSignalsWithoutRetainingOutput(t *testing.T) {
	capture := newLimitedCapture(8)
	if count, err := capture.Write(bytes.Repeat([]byte{'x'}, 1<<20)); err != nil || count != 1<<20 {
		t.Fatalf("Write() = %d, %v", count, err)
	}
	select {
	case <-capture.Exceeded():
	default:
		t.Fatal("capture did not signal its hard limit")
	}
	if capture.written != 1<<20 {
		t.Fatalf("capture.written = %d", capture.written)
	}
}

func newActualBuilder(t *testing.T) *Builder {
	t.Helper()
	cfg := DefaultConfig()
	cfg.WorkerPath = actualWorkerPath(t)
	builder, err := NewWithConfig(cfg)
	if err != nil {
		t.Fatalf("NewWithConfig: %v", err)
	}
	return builder
}

func actualWorkerPath(t *testing.T) string {
	t.Helper()
	if path := os.Getenv("EMFONT_TEST_FONT_WORKER_PATH"); path != "" {
		return path
	}
	workerBuildOnce.Do(func() {
		if err := exec.Command("pkg-config", "--exists", "harfbuzz", "harfbuzz-subset", "libwoff2enc").Run(); err != nil {
			actualWorkerErr = fmt.Errorf("native development packages are unavailable: %w", err)
			return
		}
		versionData, err := exec.Command("pkg-config", "--modversion", "libwoff2enc").Output()
		if err != nil {
			actualWorkerErr = fmt.Errorf("query WOFF2 version: %w", err)
			return
		}
		actualWorker = buildTestBinaryPath(
			"./cmd/fontworker",
			"fontworker",
			"-X=github.com/emfont/emfont/backend/internal/controller/infrastructure/fontbuild/harfbuzznative.woff2Version="+strings.TrimSpace(string(versionData))+
				" -X=main.workerBuildRevision=test-source-digest",
		)
	})
	if actualWorkerErr != nil {
		t.Fatal(actualWorkerErr)
	}
	return actualWorker
}

func buildTestBinary(t *testing.T, packagePath, name string) string {
	t.Helper()
	path := buildTestBinaryPath(packagePath, name, "")
	if actualWorkerErr != nil {
		t.Fatal(actualWorkerErr)
	}
	return path
}

func buildTestBinaryPath(packagePath, name, ldflag string) string {
	if testBinariesDir == "" {
		directory, err := os.MkdirTemp("", "emfont-fontworker-tests-")
		if err != nil {
			actualWorkerErr = err
			return ""
		}
		testBinariesDir = directory
	}
	path := filepath.Join(testBinariesDir, name)
	arguments := []string{"build", "-trimpath", "-o", path}
	if ldflag != "" {
		arguments = append(arguments, "-ldflags", ldflag)
	}
	arguments = append(arguments, packagePath)
	command := exec.Command("go", arguments...)
	command.Dir = moduleRoot()
	if output, err := command.CombinedOutput(); err != nil {
		actualWorkerErr = fmt.Errorf("build %s: %w\n%s", packagePath, err, output)
		return ""
	}
	return path
}

func moduleRoot() string {
	workingDirectory, err := os.Getwd()
	if err != nil {
		return ""
	}
	return filepath.Clean(filepath.Join(workingDirectory, "../../../../.."))
}

func readFixture(t *testing.T) []byte {
	t.Helper()
	fixture := os.Getenv("EMFONT_TEST_FONT_PATH")
	if fixture == "" {
		fixture = "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf"
	}
	source, err := os.ReadFile(fixture)
	if os.IsNotExist(err) {
		t.Skip("system font fixture is not installed")
	}
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return source
}
