package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/emfont/emfont/backend/internal/controller/app"
	"github.com/emfont/emfont/backend/internal/controller/logger"
	"github.com/emfont/emfont/backend/internal/platform/processsecurity"
	"go.uber.org/zap"
)

func main() {
	os.Exit(run())
}

func run() int {
	if err := processsecurity.DisableDumpability(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "disable process dumpability: %v\n", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	controller, err := app.New(ctx)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "startup failed: %v\n", err)
		return 1
	}
	defer func() {
		if err := logger.Sync(controller.Log); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "sync logger: %v\n", err)
		}
	}()

	err = controller.Run(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		controller.Log.Error("controller exited", zap.Error(err))
		return 1
	}
	return 0
}
