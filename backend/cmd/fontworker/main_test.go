package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emfont/emfont/backend/internal/controller/infrastructure/fontbuild/harfbuzz/protocol"
	"github.com/emfont/emfont/backend/internal/controller/infrastructure/fontbuild/harfbuzznative"
)

func TestRunReturnsStructuredPanicWithoutSourceLeak(t *testing.T) {
	stubWorkerDependencies(t)
	workerBuildSubset = func([]byte, []rune, int64) (harfbuzznative.Output, error) {
		panic("private-source-marker")
	}

	response, exitCode := runTestRequest(t, []byte("private-source-marker"))
	if exitCode != 0 {
		t.Fatalf("run exit code = %d", exitCode)
	}
	if response.Status != protocol.StatusInternalFailure || response.Message != "font worker panicked" {
		t.Fatalf("response = %#v", response)
	}
	if strings.Contains(response.Message, "private-source-marker") {
		t.Fatalf("panic response leaked source: %#v", response)
	}
}

func TestRunSanitizesNativeError(t *testing.T) {
	stubWorkerDependencies(t)
	workerBuildSubset = func([]byte, []rune, int64) (harfbuzznative.Output, error) {
		return harfbuzznative.Output{}, errors.New("private-native-detail")
	}

	response, exitCode := runTestRequest(t, []byte("private-source-marker"))
	if exitCode != 0 {
		t.Fatalf("run exit code = %d", exitCode)
	}
	if response.Status != protocol.StatusNativeFailure || response.Message != "native font subsetting failed" {
		t.Fatalf("response = %#v", response)
	}
	if strings.Contains(response.Message, "private") {
		t.Fatalf("native error response leaked detail: %#v", response)
	}
}

func TestRunAppliesSandboxAndPassesConfiguredOutputLimit(t *testing.T) {
	stubWorkerDependencies(t)
	sandboxed := false
	workerApplyProcessSecurity = func() error {
		sandboxed = true
		return nil
	}
	workerBuildSubset = func(_ []byte, _ []rune, maxOutputBytes int64) (harfbuzznative.Output, error) {
		if !sandboxed {
			t.Fatal("native processing started before the process sandbox was applied")
		}
		if maxOutputBytes != 4096 {
			t.Fatalf("native max output = %d, want 4096", maxOutputBytes)
		}
		return harfbuzznative.Output{Data: []byte("wOF2"), GlyphCount: 1}, nil
	}

	response, exitCode := runTestRequestWithArgs(t, []byte("font"), []string{"--max-output-bytes=4096"})
	if exitCode != 0 || response.Status != protocol.StatusOK {
		t.Fatalf("run = exit %d, response %#v", exitCode, response)
	}
}

func TestRunMapsNativeOutputLimitToResourceFailure(t *testing.T) {
	stubWorkerDependencies(t)
	workerBuildSubset = func([]byte, []rune, int64) (harfbuzznative.Output, error) {
		return harfbuzznative.Output{}, harfbuzznative.ErrOutputLimit
	}

	response, exitCode := runTestRequest(t, []byte("font"))
	if exitCode != 0 || response.Status != protocol.StatusResourceFailure {
		t.Fatalf("run = exit %d, response %#v", exitCode, response)
	}
}

func TestWorkerBuildRevisionChangesVersionIdentity(t *testing.T) {
	if strings.TrimSpace(workerBuildRevision) == "" {
		t.Fatal("worker build revision is empty")
	}
	first := composeBuilderVersion("native-test", "revision-a")
	second := composeBuilderVersion("native-test", "revision-b")
	if first == second {
		t.Fatalf("builder identities did not change: %q", first)
	}
	if !strings.HasSuffix(first, "-"+protocol.Identity) {
		t.Fatalf("builder identity = %q, want protocol suffix", first)
	}
}

func TestComposeBuilderVersionExposesProductionBuildIdentity(t *testing.T) {
	revision := "linux-arm64-go1.26.5-hb-10.2.0-1+deb13u1-w2-1.0.2-2+b2-src-" +
		strings.Repeat("a", 64) + "-pkg-" + strings.Repeat("b", 64)
	version := composeBuilderVersion("harfbuzz-10.2.0-woff2-1.0.2", revision)

	for _, component := range []string{
		"linux-arm64",
		"go1.26.5",
		"hb-10.2.0-1+deb13u1",
		"w2-1.0.2-2+b2",
		"src-" + strings.Repeat("a", 64),
		"pkg-" + strings.Repeat("b", 64),
	} {
		if !strings.Contains(version, component) {
			t.Fatalf("builder identity %q does not expose %q", version, component)
		}
	}
	if len(version) > protocol.AbsoluteMaxVersionBytes {
		t.Fatalf("builder identity length = %d, protocol maximum = %d", len(version), protocol.AbsoluteMaxVersionBytes)
	}
	if len(version) > protocol.AbsoluteMaxVersionBytes-8 {
		t.Fatalf("builder identity length = %d, want protocol upgrade headroom", len(version))
	}
}

func TestDockerfileBuildRevisionCoversNativeOutputInputs(t *testing.T) {
	dockerfile, err := os.ReadFile(filepath.Join("..", "..", "Dockerfile"))
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	text := string(dockerfile)
	for _, required := range []string{
		"go env GOOS",
		"go env GOARCH",
		"go env GOVERSION",
		"dpkg-query -W -f='${Version}' libharfbuzz-dev",
		"dpkg-query -W -f='${Version}' libwoff-dev",
		"scripts/fontworker-package-manifest.sh build",
		"fontworker-build-packages.tsv",
		"fontworker-runtime-packages.tsv",
		"PACKAGE_MANIFEST_DIGEST",
		"-src-${WORKER_INPUT_DIGEST}-pkg-${PACKAGE_MANIFEST_DIGEST}",
		"cmd/fontworker",
		"internal/controller/infrastructure/fontbuild/harfbuzz/protocol",
		"internal/controller/infrastructure/fontbuild/harfbuzznative",
		"internal/platform/processsecurity",
		"-X main.workerBuildRevision=${WORKER_BUILD_REVISION}",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("Dockerfile worker identity does not cover %q", required)
		}
	}
}

func TestPackageManifestDependencyChangesAlterWorkerIdentity(t *testing.T) {
	helper := filepath.Join("..", "..", "scripts", "fontworker-package-manifest.sh")
	temporary := t.TempDir()
	digest := func(name, compilerVersion, pkgConfigVersion, runtimeVersion string) string {
		t.Helper()
		manifest := fmt.Sprintf(
			"schema\temfont-fontworker-native-packages-v1\n"+
				"runtime\tlibexample.so.1\t/usr/lib/libexample.so.1\tlibexample1:amd64\t%s\tamd64\n"+
				"tool\tcxx\t/usr/bin/g++\tg++:amd64\t%s\tamd64\n"+
				"tool\tpkg-config\t/usr/bin/pkgconf\tpkgconf:amd64\t%s\tamd64\n",
			runtimeVersion, compilerVersion, pkgConfigVersion,
		)
		path := filepath.Join(temporary, name+".tsv")
		if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
			t.Fatalf("write %s package manifest: %v", name, err)
		}
		output, err := exec.Command(helper, "digest", path).CombinedOutput()
		if err != nil {
			t.Fatalf("digest %s package manifest: %v\n%s", name, err, output)
		}
		return strings.TrimSpace(string(output))
	}

	base := digest("base", "4:14.2.0-1", "1.8.1-4", "2.41-5")
	changed := map[string]string{
		"compiler":   digest("compiler", "4:14.2.0-2", "1.8.1-4", "2.41-5"),
		"pkg-config": digest("pkg-config", "4:14.2.0-1", "1.8.1-5", "2.41-5"),
		"runtime":    digest("runtime", "4:14.2.0-1", "1.8.1-4", "2.41-6"),
	}
	baseIdentity := composeBuilderVersion("native", "linux-amd64-pkg-"+base)
	for dependency, changedDigest := range changed {
		if changedDigest == base {
			t.Fatalf("%s package revision did not change manifest digest", dependency)
		}
		if identity := composeBuilderVersion("native", "linux-amd64-pkg-"+changedDigest); identity == baseIdentity {
			t.Fatalf("%s package revision did not change worker identity", dependency)
		}
	}
}

func stubWorkerDependencies(t *testing.T) {
	t.Helper()
	originalLimits := workerApplyResourceLimits
	originalSecurity := workerApplyProcessSecurity
	originalVersion := workerBuilderVersion
	originalBuild := workerBuildSubset
	t.Cleanup(func() {
		workerApplyResourceLimits = originalLimits
		workerApplyProcessSecurity = originalSecurity
		workerBuilderVersion = originalVersion
		workerBuildSubset = originalBuild
	})
	workerApplyResourceLimits = func(resourceLimits) error { return nil }
	workerApplyProcessSecurity = func() error { return nil }
	workerBuilderVersion = func() string { return "harfbuzz-test-woff2-test-" + protocol.Identity }
}

func runTestRequest(t *testing.T, source []byte) (protocol.Response, int) {
	return runTestRequestWithArgs(t, source, nil)
}

func runTestRequestWithArgs(t *testing.T, source []byte, args []string) (protocol.Response, int) {
	t.Helper()
	var request bytes.Buffer
	if err := protocol.EncodeRequest(&request, protocol.Request{
		Operation: protocol.OperationSubset, Source: source,
		Codepoints: []rune{'A'}, TargetFormat: protocol.FormatWOFF2,
	}, 128<<20); err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(args, &request, &stdout, &stderr)
	response, err := protocol.DecodeResponse(bytes.NewReader(stdout.Bytes()), 128<<20)
	if err != nil {
		t.Fatalf("DecodeResponse: %v; stderr=%s", err, stderr.String())
	}
	return response, exitCode
}
