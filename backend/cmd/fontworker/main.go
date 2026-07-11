package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"strings"

	"github.com/emfont/emfont/backend/internal/controller/infrastructure/fontbuild/harfbuzz/protocol"
	"github.com/emfont/emfont/backend/internal/controller/infrastructure/fontbuild/harfbuzznative"
	"github.com/emfont/emfont/backend/internal/platform/processsecurity"
)

type workerConfig struct {
	maxSourceBytes int64
	maxOutputBytes int64
	addressSpace   uint64
	cpuSeconds     uint64
	fileSizeBytes  uint64
	openFiles      uint64
}

var (
	workerApplyResourceLimits  = applyResourceLimits
	workerApplyProcessSecurity = processsecurity.ApplyFontWorkerSandbox
	workerBuilderVersion       = builderVersion
	workerBuildSubset          = harfbuzznative.BuildSubset
	workerBuildRevision        = "development"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) (exitCode int) {
	cfg, err := parseConfig(args, stderr)
	version := composeBuilderVersion("native-unavailable", workerBuildRevision)
	if err != nil {
		return writeResponse(stdout, stderr, protocol.Response{
			Status: protocol.StatusInvalidRequest, Message: "invalid worker configuration", BuilderVersion: version,
		}, protocol.AbsoluteMaxOutputBytes)
	}

	written := false
	defer func() {
		if recovered := recover(); recovered != nil && !written {
			exitCode = writeResponse(stdout, stderr, protocol.Response{
				Status: protocol.StatusInternalFailure, Message: "font worker panicked", BuilderVersion: version,
			}, cfg.maxOutputBytes)
		}
	}()

	if err := workerApplyResourceLimits(resourceLimits{
		addressSpaceBytes: cfg.addressSpace,
		cpuSeconds:        cfg.cpuSeconds,
		fileSizeBytes:     cfg.fileSizeBytes,
		openFiles:         cfg.openFiles,
	}); err != nil {
		exitCode = writeResponse(stdout, stderr, protocol.Response{
			Status: protocol.StatusResourceFailure, Message: "could not apply worker resource limits", BuilderVersion: version,
		}, cfg.maxOutputBytes)
		written = true
		return exitCode
	}
	if err := workerApplyProcessSecurity(); err != nil {
		exitCode = writeResponse(stdout, stderr, protocol.Response{
			Status: protocol.StatusResourceFailure, Message: "could not apply worker process security", BuilderVersion: version,
		}, cfg.maxOutputBytes)
		written = true
		return exitCode
	}
	version = workerBuilderVersion()

	request, err := protocol.DecodeRequest(stdin, cfg.maxSourceBytes)
	if err != nil {
		exitCode = writeResponse(stdout, stderr, protocol.Response{
			Status: protocol.StatusInvalidRequest, Message: "malformed worker request", BuilderVersion: version,
		}, cfg.maxOutputBytes)
		written = true
		return exitCode
	}
	response := execute(request, cfg, version)
	exitCode = writeResponse(stdout, stderr, response, cfg.maxOutputBytes)
	written = true
	return exitCode
}

func execute(request protocol.Request, cfg workerConfig, version string) protocol.Response {
	if request.Operation == protocol.OperationVersion {
		return protocol.Response{Status: protocol.StatusOK, BuilderVersion: version}
	}
	if err := harfbuzznative.Available(); err != nil {
		return protocol.Response{
			Status: protocol.StatusNativeFailure, Message: "native font engine is unavailable", BuilderVersion: version,
		}
	}
	output, err := workerBuildSubset(request.Source, request.Codepoints, cfg.maxOutputBytes)
	if err != nil {
		if errors.Is(err, harfbuzznative.ErrUnsupportedCodepoints) {
			return protocol.Response{
				Status:         protocol.StatusUnsupportedCodepoints,
				Message:        "one or more requested codepoints are unsupported",
				BuilderVersion: version,
			}
		}
		if errors.Is(err, harfbuzznative.ErrOutputLimit) {
			return protocol.Response{
				Status: protocol.StatusResourceFailure, Message: "native font output exceeds the configured limit", BuilderVersion: version,
			}
		}
		return protocol.Response{
			Status: protocol.StatusNativeFailure, Message: "native font subsetting failed", BuilderVersion: version,
		}
	}
	if int64(len(output.Data)) > cfg.maxOutputBytes {
		return protocol.Response{
			Status: protocol.StatusResourceFailure, Message: "native font output exceeds the configured limit", BuilderVersion: version,
		}
	}
	if output.GlyphCount <= 0 || uint64(output.GlyphCount) > math.MaxUint32 {
		return protocol.Response{
			Status: protocol.StatusNativeFailure, Message: "native font engine returned an invalid glyph count", BuilderVersion: version,
		}
	}
	return protocol.Response{
		Status: protocol.StatusOK, Data: output.Data, GlyphCount: uint32(output.GlyphCount), BuilderVersion: version,
	}
}

func builderVersion() string {
	return composeBuilderVersion(harfbuzznative.Version(), workerBuildRevision)
}

func composeBuilderVersion(nativeVersion, revision string) string {
	revision = strings.TrimSpace(revision)
	if revision == "" {
		revision = "missing"
	}
	return nativeVersion + "-worker-" + revision + "-" + protocol.Identity
}

func parseConfig(args []string, stderr io.Writer) (workerConfig, error) {
	cfg := workerConfig{
		maxSourceBytes: 128 << 20, maxOutputBytes: 128 << 20,
		addressSpace: 2 << 30, cpuSeconds: 60, fileSizeBytes: 128 << 20, openFiles: 32,
	}
	flags := flag.NewFlagSet("fontworker", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Int64Var(&cfg.maxSourceBytes, "max-source-bytes", cfg.maxSourceBytes, "maximum request source bytes")
	flags.Int64Var(&cfg.maxOutputBytes, "max-output-bytes", cfg.maxOutputBytes, "maximum response font bytes")
	flags.Uint64Var(&cfg.addressSpace, "address-space-bytes", cfg.addressSpace, "RLIMIT_AS in bytes")
	flags.Uint64Var(&cfg.cpuSeconds, "cpu-seconds", cfg.cpuSeconds, "RLIMIT_CPU in seconds")
	flags.Uint64Var(&cfg.fileSizeBytes, "file-size-bytes", cfg.fileSizeBytes, "RLIMIT_FSIZE in bytes")
	flags.Uint64Var(&cfg.openFiles, "open-files", cfg.openFiles, "RLIMIT_NOFILE")
	if err := flags.Parse(args); err != nil {
		return cfg, err
	}
	if flags.NArg() != 0 {
		return cfg, errors.New("fontworker does not accept positional arguments")
	}
	if cfg.maxSourceBytes <= 0 || cfg.maxSourceBytes > protocol.AbsoluteMaxSourceBytes {
		return cfg, errors.New("max source bytes is outside the protocol limit")
	}
	if cfg.maxOutputBytes <= 0 || cfg.maxOutputBytes > protocol.AbsoluteMaxOutputBytes {
		return cfg, errors.New("max output bytes is outside the protocol limit")
	}
	minimumAddressSpace := uint64(cfg.maxSourceBytes) + uint64(cfg.maxOutputBytes) + (256 << 20)
	if minimumAddressSpace < 2<<30 {
		minimumAddressSpace = 2 << 30
	}
	if cfg.addressSpace < minimumAddressSpace {
		return cfg, errors.New("address-space limit does not cover bounded worker memory")
	}
	if cfg.cpuSeconds == 0 || cfg.fileSizeBytes < uint64(cfg.maxOutputBytes) || cfg.openFiles < 16 {
		return cfg, errors.New("worker resource limits are invalid")
	}
	return cfg, nil
}

func writeResponse(stdout, stderr io.Writer, response protocol.Response, maxOutputBytes int64) int {
	if err := protocol.EncodeResponse(stdout, response, maxOutputBytes); err != nil {
		_, _ = fmt.Fprintln(stderr, "font worker could not encode its bounded response")
		return 1
	}
	return 0
}
