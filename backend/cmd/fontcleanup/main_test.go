package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	appcleanup "github.com/emfont/emfont/backend/internal/controller/application/fontcleanup"
)

func TestRunEmitsPartialReportAndClosesResources(t *testing.T) {
	originalFactory := newCleanupRunner
	t.Cleanup(func() { newCleanupRunner = originalFactory })
	cleanupFailure := errors.New("partial cleanup failure")
	closed := false
	newCleanupRunner = func(context.Context) (cleanupRunner, func() error, error) {
		return stubCleanupRunner{
				report: appcleanup.Report{StartedAt: time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC), ObjectsDeleted: 2},
				err:    cleanupFailure,
			}, func() error {
				closed = true
				return nil
			}, nil
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run(context.Background(), &stdout, &stderr)
	if exitCode != 1 {
		t.Fatalf("exit code = %d, want 1", exitCode)
	}
	if !closed {
		t.Fatal("resources were not closed")
	}
	if !strings.Contains(stdout.String(), `"objectsDeleted":2`) {
		t.Fatalf("stdout = %q, want JSON report", stdout.String())
	}
	if !strings.Contains(stderr.String(), cleanupFailure.Error()) {
		t.Fatalf("stderr = %q, want cleanup failure", stderr.String())
	}
}

func TestRunRejectsMissingRuntimeWiring(t *testing.T) {
	originalFactory := newCleanupRunner
	newCleanupRunner = nil
	t.Cleanup(func() { newCleanupRunner = originalFactory })
	var stderr bytes.Buffer

	if exitCode := run(context.Background(), &bytes.Buffer{}, &stderr); exitCode != 1 {
		t.Fatalf("exit code = %d, want 1", exitCode)
	}
	if !strings.Contains(stderr.String(), "runtime is not configured") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunClosesResourcesWhenFactoryReturnsNilRunner(t *testing.T) {
	originalFactory := newCleanupRunner
	t.Cleanup(func() { newCleanupRunner = originalFactory })
	closed := false
	newCleanupRunner = func(context.Context) (cleanupRunner, func() error, error) {
		return nil, func() error {
			closed = true
			return nil
		}, nil
	}
	var stderr bytes.Buffer

	if exitCode := run(context.Background(), &bytes.Buffer{}, &stderr); exitCode != 1 {
		t.Fatalf("exit code = %d, want 1", exitCode)
	}
	if !closed {
		t.Fatal("resources were not closed")
	}
	if !strings.Contains(stderr.String(), "runner is not configured") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

type stubCleanupRunner struct {
	report appcleanup.Report
	err    error
}

func (s stubCleanupRunner) Run(context.Context) (appcleanup.Report, error) {
	return s.report, s.err
}
