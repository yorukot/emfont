package harfbuzz

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	appfont "github.com/emfont/emfont/backend/internal/controller/application/font"
	"github.com/emfont/emfont/backend/internal/controller/infrastructure/fontbuild/harfbuzz/protocol"
	domain "github.com/emfont/emfont/backend/internal/domain/font"
)

const (
	defaultWorkerPath             = "emfont-fontworker"
	defaultMaxSourceBytes         = int64(128 << 20)
	defaultMaxOutputBytes         = int64(128 << 20)
	defaultAddressSpaceLimitBytes = uint64(2 << 30)
	defaultCPUTimeLimitSeconds    = uint64(60)
	defaultFileSizeLimitBytes     = uint64(128 << 20)
	defaultOpenFilesLimit         = uint64(32)
	defaultStderrLimitBytes       = int64(16 << 10)
	workerVersionProbeTimeout     = 5 * time.Second
	workerTerminationWaitDelay    = time.Second
	minimumAddressSpaceOverhead   = uint64(256 << 20)
	minimumAddressSpaceLimitBytes = uint64(2 << 30)
)

// Config controls the process boundary around the native font builder.
type Config struct {
	WorkerPath                string
	RequireProductionIdentity bool
	MaxSourceBytes            int64
	MaxOutputBytes            int64
	AddressSpaceLimitBytes    uint64
	CPUTimeLimitSeconds       uint64
	FileSizeLimitBytes        uint64
	OpenFilesLimit            uint64
	StderrLimitBytes          int64
}

func DefaultConfig() Config {
	return Config{
		WorkerPath:             defaultWorkerPath,
		MaxSourceBytes:         defaultMaxSourceBytes,
		MaxOutputBytes:         defaultMaxOutputBytes,
		AddressSpaceLimitBytes: defaultAddressSpaceLimitBytes,
		CPUTimeLimitSeconds:    defaultCPUTimeLimitSeconds,
		FileSizeLimitBytes:     defaultFileSizeLimitBytes,
		OpenFilesLimit:         defaultOpenFilesLimit,
		StderrLimitBytes:       defaultStderrLimitBytes,
	}
}

type Builder struct {
	config Config

	versionMu sync.Mutex
	version   string
}

type decodeResult struct {
	response protocol.Response
	err      error
}

// New retains the existing constructor for local tools. Production wiring
// should use NewWithConfig so every worker limit is explicit.
func New() *Builder {
	builder, err := NewWithConfig(DefaultConfig())
	if err != nil {
		panic(err)
	}
	return builder
}

func NewWithConfig(cfg Config) (*Builder, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	return &Builder{config: cfg}, nil
}

func validateConfig(cfg Config) error {
	if strings.TrimSpace(cfg.WorkerPath) == "" {
		return errors.New("font worker path is required")
	}
	if cfg.MaxSourceBytes <= 0 || cfg.MaxSourceBytes > protocol.AbsoluteMaxSourceBytes {
		return fmt.Errorf("font worker max source bytes must be between 1 and %d", protocol.AbsoluteMaxSourceBytes)
	}
	if cfg.MaxOutputBytes <= 0 || cfg.MaxOutputBytes > protocol.AbsoluteMaxOutputBytes {
		return fmt.Errorf("font worker max output bytes must be between 1 and %d", protocol.AbsoluteMaxOutputBytes)
	}
	minimumAddressSpace := uint64(cfg.MaxSourceBytes) + uint64(cfg.MaxOutputBytes) + minimumAddressSpaceOverhead
	if minimumAddressSpace < minimumAddressSpaceLimitBytes {
		minimumAddressSpace = minimumAddressSpaceLimitBytes
	}
	if cfg.AddressSpaceLimitBytes < minimumAddressSpace {
		return fmt.Errorf("font worker address-space limit must be at least %d bytes", minimumAddressSpace)
	}
	if cfg.CPUTimeLimitSeconds == 0 {
		return errors.New("font worker CPU time limit must be greater than zero")
	}
	if cfg.FileSizeLimitBytes < uint64(cfg.MaxOutputBytes) {
		return errors.New("font worker file-size limit must cover the maximum output size")
	}
	if cfg.OpenFilesLimit < 16 {
		return errors.New("font worker open-files limit must be at least 16")
	}
	if cfg.StderrLimitBytes <= 0 || cfg.StderrLimitBytes > protocol.AbsoluteMaxDiagnosticBytes {
		return fmt.Errorf("font worker stderr limit must be between 1 and %d", protocol.AbsoluteMaxDiagnosticBytes)
	}
	return nil
}

func (b *Builder) Version() string {
	ctx, cancel := context.WithTimeout(context.Background(), workerVersionProbeTimeout)
	defer cancel()
	version, err := b.probeVersion(ctx)
	if err != nil {
		return protocol.Identity + "-native-unavailable"
	}
	return version
}

func (b *Builder) Available() error {
	if b == nil {
		return errors.New("font worker builder is nil")
	}
	if _, err := exec.LookPath(b.config.WorkerPath); err != nil {
		return fmt.Errorf("locate font worker: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), workerVersionProbeTimeout)
	defer cancel()
	_, err := b.probeVersion(ctx)
	return err
}

func (b *Builder) probeVersion(ctx context.Context) (string, error) {
	b.versionMu.Lock()
	defer b.versionMu.Unlock()
	if b.version != "" {
		return b.version, nil
	}

	response, err := b.invoke(ctx, protocol.Request{Operation: protocol.OperationVersion})
	if err != nil {
		return "", fmt.Errorf("query font worker version: %w", err)
	}
	if response.Status != protocol.StatusOK || response.BuilderVersion == "" || len(response.Data) != 0 || response.GlyphCount != 0 {
		return "", errors.New("font worker returned an invalid version response")
	}
	if err := b.validateVersion(response.BuilderVersion); err != nil {
		return "", err
	}
	b.version = response.BuilderVersion
	return b.version, nil
}

func (b *Builder) BuildSubset(ctx context.Context, input appfont.BuildInput) (appfont.BuildOutput, error) {
	if b == nil {
		return appfont.BuildOutput{}, errors.New("font worker builder is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return appfont.BuildOutput{}, err
	}
	if len(input.Source) == 0 {
		return appfont.BuildOutput{}, errors.New("source font is empty")
	}
	if int64(len(input.Source)) > b.config.MaxSourceBytes {
		return appfont.BuildOutput{}, fmt.Errorf("source font exceeds %d bytes", b.config.MaxSourceBytes)
	}
	if len(input.Codepoints) == 0 {
		return appfont.BuildOutput{}, errors.New("requested codepoint set is empty")
	}
	if input.TargetFormat != "" && input.TargetFormat != domain.OutputFormatWOFF2 {
		return appfont.BuildOutput{}, fmt.Errorf("unsupported target format %q", input.TargetFormat)
	}

	response, err := b.invoke(ctx, protocol.Request{
		Operation:    protocol.OperationSubset,
		Source:       input.Source,
		Codepoints:   input.Codepoints,
		TargetFormat: protocol.FormatWOFF2,
	})
	if err != nil {
		return appfont.BuildOutput{}, err
	}
	if err := b.validateVersion(response.BuilderVersion); err != nil {
		return appfont.BuildOutput{}, err
	}
	if err := b.acceptVersion(response.BuilderVersion); err != nil {
		return appfont.BuildOutput{}, err
	}

	switch response.Status {
	case protocol.StatusOK:
		if len(response.Data) == 0 || response.GlyphCount == 0 {
			return appfont.BuildOutput{}, errors.New("font worker returned an empty subset")
		}
		return appfont.BuildOutput{
			Data: response.Data, ContentType: domain.ContentTypeWOFF2, Format: domain.OutputFormatWOFF2,
			GlyphCount: int(response.GlyphCount), BuilderVersion: response.BuilderVersion,
		}, nil
	case protocol.StatusUnsupportedCodepoints:
		return appfont.BuildOutput{}, fmt.Errorf("%w: %s", appfont.ErrUnsupportedCodepoints, response.Message)
	case protocol.StatusInvalidRequest:
		return appfont.BuildOutput{}, fmt.Errorf("font worker rejected the request: %s", response.Message)
	case protocol.StatusResourceFailure:
		return appfont.BuildOutput{}, fmt.Errorf("font worker resource limit: %s", response.Message)
	case protocol.StatusNativeFailure:
		return appfont.BuildOutput{}, fmt.Errorf("native font build failed: %s", response.Message)
	case protocol.StatusInternalFailure:
		return appfont.BuildOutput{}, fmt.Errorf("font worker failed: %s", response.Message)
	default:
		return appfont.BuildOutput{}, fmt.Errorf("font worker returned unknown status %d", response.Status)
	}
}

func (b *Builder) validateVersion(version string) error {
	if version == "" || !strings.HasSuffix(version, "-"+protocol.Identity) {
		return errors.New("font worker omitted its native/protocol version")
	}
	if b.config.RequireProductionIdentity {
		if err := protocol.ValidateProductionBuilderIdentity(version); err != nil {
			return fmt.Errorf("font worker returned a non-production cache identity: %w", err)
		}
		expectedPlatform := "-worker-" + runtime.GOOS + "-" + runtime.GOARCH + "-"
		if !strings.Contains(version, expectedPlatform) {
			return fmt.Errorf("font worker production identity does not match controller platform %s/%s", runtime.GOOS, runtime.GOARCH)
		}
	}
	return nil
}

func (b *Builder) acceptVersion(version string) error {
	b.versionMu.Lock()
	defer b.versionMu.Unlock()
	if b.version == "" {
		b.version = version
		return nil
	}
	if b.version != version {
		return fmt.Errorf("font worker version changed from %q to %q", b.version, version)
	}
	return nil
}

func (b *Builder) invoke(ctx context.Context, request protocol.Request) (protocol.Response, error) {
	requestReader, err := protocol.NewRequestReader(request, b.config.MaxSourceBytes)
	if err != nil {
		return protocol.Response{}, err
	}
	arguments := b.workerArguments()
	processCtx, cancelProcess := context.WithCancel(ctx)
	defer cancelProcess()
	command := exec.CommandContext(processCtx, b.config.WorkerPath, arguments...)
	command.Cancel = func() error {
		terminateProcessGroup(command)
		return nil
	}
	command.WaitDelay = workerTerminationWaitDelay
	configureProcess(command)
	command.Env = []string{"LANG=C", "LC_ALL=C", "GOTRACEBACK=none"}
	command.Stdin = requestReader
	stdout, err := command.StdoutPipe()
	if err != nil {
		return protocol.Response{}, fmt.Errorf("open font worker stdout: %w", err)
	}
	stderr := newLimitedCapture(b.config.StderrLimitBytes)
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		return protocol.Response{}, fmt.Errorf("start font worker: %w", err)
	}

	decoded := make(chan decodeResult, 1)
	go func() {
		response, decodeErr := protocol.DecodeResponse(stdout, b.config.MaxOutputBytes)
		decoded <- decodeResult{response: response, err: decodeErr}
	}()

	select {
	case <-ctx.Done():
		killAndWait(command, stdout, decoded, cancelProcess)
		return protocol.Response{}, ctx.Err()
	case <-stderr.Exceeded():
		killAndWait(command, stdout, decoded, cancelProcess)
		return protocol.Response{}, fmt.Errorf("font worker stderr exceeded %d bytes", b.config.StderrLimitBytes)
	case result := <-decoded:
		if result.err != nil {
			waitErr := killAndWait(command, stdout, nil, cancelProcess)
			if waitErr != nil {
				return protocol.Response{}, fmt.Errorf("decode font worker response: %w (worker exit: %v)", result.err, waitErr)
			}
			return protocol.Response{}, fmt.Errorf("decode font worker response: %w", result.err)
		}

		waited := make(chan error, 1)
		go func() { waited <- command.Wait() }()
		select {
		case <-ctx.Done():
			cancelProcess()
			terminateProcessGroup(command)
			<-waited
			return protocol.Response{}, ctx.Err()
		case <-stderr.Exceeded():
			cancelProcess()
			terminateProcessGroup(command)
			<-waited
			return protocol.Response{}, fmt.Errorf("font worker stderr exceeded %d bytes", b.config.StderrLimitBytes)
		case waitErr := <-waited:
			if waitErr != nil {
				return protocol.Response{}, fmt.Errorf("font worker exited unsuccessfully: %w", waitErr)
			}
			return result.response, nil
		}
	}
}

func (b *Builder) workerArguments() []string {
	return []string{
		"--max-source-bytes=" + strconv.FormatInt(b.config.MaxSourceBytes, 10),
		"--max-output-bytes=" + strconv.FormatInt(b.config.MaxOutputBytes, 10),
		"--address-space-bytes=" + strconv.FormatUint(b.config.AddressSpaceLimitBytes, 10),
		"--cpu-seconds=" + strconv.FormatUint(b.config.CPUTimeLimitSeconds, 10),
		"--file-size-bytes=" + strconv.FormatUint(b.config.FileSizeLimitBytes, 10),
		"--open-files=" + strconv.FormatUint(b.config.OpenFilesLimit, 10),
	}
}

func killAndWait(command *exec.Cmd, stdout io.Closer, decoded <-chan decodeResult, cancel context.CancelFunc) error {
	cancel()
	terminateProcessGroup(command)
	_ = stdout.Close()
	if decoded != nil {
		<-decoded
	}
	return command.Wait()
}

type limitedCapture struct {
	mu       sync.Mutex
	limit    int64
	written  int64
	exceeded chan struct{}
	once     sync.Once
}

func newLimitedCapture(limit int64) *limitedCapture {
	return &limitedCapture{limit: limit, exceeded: make(chan struct{})}
}

func (c *limitedCapture) Write(data []byte) (int, error) {
	c.mu.Lock()
	c.written += int64(len(data))
	exceeded := c.written > c.limit
	c.mu.Unlock()
	if exceeded {
		c.once.Do(func() { close(c.exceeded) })
	}
	return len(data), nil
}

func (c *limitedCapture) Exceeded() <-chan struct{} { return c.exceeded }

var _ io.Writer = (*limitedCapture)(nil)
