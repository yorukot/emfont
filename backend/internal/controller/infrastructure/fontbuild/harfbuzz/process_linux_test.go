//go:build linux

package harfbuzz

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	appfont "github.com/emfont/emfont/backend/internal/controller/application/font"
	"github.com/emfont/emfont/backend/internal/domain/font"
)

func TestBuildSubsetCancellationKillsWorkerProcessGroup(t *testing.T) {
	helper := buildTestBinary(t, "./internal/controller/infrastructure/fontbuild/harfbuzz/testdata/treeworker.go", "treeworker-cancel")
	builder := newBuilderWithWorker(t, helper)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	started := time.Now()
	_, err := builder.BuildSubset(ctx, appfont.BuildInput{
		Source: []byte("cancel"), Codepoints: []rune{'A'}, TargetFormat: font.OutputFormatWOFF2,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("BuildSubset error = %v, want context deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("process-group cancellation returned after %s", elapsed)
	}
	assertWorkerTreeTerminated(t, helper)
}

func TestBuildSubsetProtocolErrorKillsWorkerProcessGroupWithoutPipeHang(t *testing.T) {
	helper := buildTestBinary(t, "./internal/controller/infrastructure/fontbuild/harfbuzz/testdata/treeworker.go", "treeworker-error")
	builder := newBuilderWithWorker(t, helper)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	started := time.Now()
	_, err := builder.BuildSubset(ctx, appfont.BuildInput{
		Source: []byte("malformed"), Codepoints: []rune{'A'}, TargetFormat: font.OutputFormatWOFF2,
	})
	if err == nil || !strings.Contains(err.Error(), "decode font worker response") {
		t.Fatalf("BuildSubset error = %v, want protocol decode error", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("protocol-error cleanup returned after %s", elapsed)
	}
	assertWorkerTreeTerminated(t, helper)
}

func TestBuildSubsetCancellationDoesNotHangOnEscapedPipeHolder(t *testing.T) {
	helper := buildTestBinary(t, "./internal/controller/infrastructure/fontbuild/harfbuzz/testdata/escapeworker.go", "escapeworker")
	builder := newBuilderWithWorker(t, helper)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	started := time.Now()
	_, err := builder.BuildSubset(ctx, appfont.BuildInput{
		Source: []byte("escape"), Codepoints: []rune{'A'}, TargetFormat: font.OutputFormatWOFF2,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("BuildSubset error = %v, want context deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Fatalf("escaped pipe holder delayed cancellation by %s", elapsed)
	}

	data, readErr := os.ReadFile(helper + ".escaped")
	if readErr != nil {
		t.Fatalf("read escaped descendant PID: %v", readErr)
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
	if parseErr != nil {
		t.Fatalf("parse escaped descendant PID: %v", parseErr)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	assertProcessTerminated(t, pid)
}

func newBuilderWithWorker(t *testing.T, workerPath string) *Builder {
	t.Helper()
	cfg := DefaultConfig()
	cfg.WorkerPath = workerPath
	builder, err := NewWithConfig(cfg)
	if err != nil {
		t.Fatalf("NewWithConfig: %v", err)
	}
	return builder
}

func assertWorkerTreeTerminated(t *testing.T, helper string) {
	t.Helper()
	data, err := os.ReadFile(helper + ".pids")
	if err != nil {
		t.Fatalf("read worker tree PIDs: %v", err)
	}
	fields := strings.Fields(string(data))
	if len(fields) != 4 {
		t.Fatalf("worker tree state = %q", data)
	}
	values := make([]int, len(fields))
	for index, field := range fields {
		values[index], err = strconv.Atoi(field)
		if err != nil {
			t.Fatalf("parse worker tree state %q: %v", data, err)
		}
	}
	if values[2] != values[0] || values[3] != values[0] {
		t.Fatalf("worker tree process groups = parent pid %d, parent pgid %d, descendant pgid %d", values[0], values[2], values[3])
	}
	assertProcessTerminated(t, values[0])
	assertProcessTerminated(t, values[1])
}

func assertProcessTerminated(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
		if os.IsNotExist(err) {
			return
		}
		if err == nil {
			closing := strings.LastIndexByte(string(stat), ')')
			if closing >= 0 {
				fields := strings.Fields(string(stat[closing+1:]))
				if len(fields) > 0 && fields[0] == "Z" {
					return
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d is still running", pid)
}
