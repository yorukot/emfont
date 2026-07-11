package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	controllerapp "github.com/emfont/emfont/backend/internal/controller/app"
	appcleanup "github.com/emfont/emfont/backend/internal/controller/application/fontcleanup"
)

type cleanupRunner interface {
	Run(context.Context) (appcleanup.Report, error)
}

type cleanupFactory func(context.Context) (cleanupRunner, func() error, error)

var newCleanupRunner cleanupFactory = buildCleanupRunner

type timeoutRunner struct {
	runner  cleanupRunner
	timeout time.Duration
}

func (r timeoutRunner) Run(ctx context.Context) (appcleanup.Report, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	return r.runner.Run(ctx)
}

func buildCleanupRunner(ctx context.Context) (cleanupRunner, func() error, error) {
	application, err := controllerapp.NewFontCleanup(ctx)
	if err != nil {
		return nil, nil, err
	}
	return timeoutRunner{
		runner: application.Service, timeout: application.Config.Cleanup.Timeout,
	}, application.Close, nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Stdout, os.Stderr))
}

func run(ctx context.Context, stdout, stderr io.Writer) int {
	if ctx == nil {
		ctx = context.Background()
	}
	if newCleanupRunner == nil {
		_, _ = fmt.Fprintln(stderr, "font cleanup runtime is not configured")
		return 1
	}
	runner, closeResources, err := newCleanupRunner(ctx)
	if err != nil {
		if closeResources != nil {
			err = errors.Join(err, closeResources())
		}
		_, _ = fmt.Fprintf(stderr, "font cleanup startup failed: %v\n", err)
		return 1
	}
	if runner == nil {
		var closeErr error
		if closeResources != nil {
			closeErr = closeResources()
		}
		startupErr := errors.Join(errors.New("runner is not configured"), closeErr)
		_, _ = fmt.Fprintf(stderr, "font cleanup startup failed: %v\n", startupErr)
		return 1
	}

	report, cleanupErr := runner.Run(ctx)
	encodeErr := json.NewEncoder(stdout).Encode(report)
	var closeErr error
	if closeResources != nil {
		closeErr = closeResources()
	}
	err = errors.Join(cleanupErr, encodeErr, closeErr)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "font cleanup failed: %v\n", err)
		return 1
	}
	return 0
}
