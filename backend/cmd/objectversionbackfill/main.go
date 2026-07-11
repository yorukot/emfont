package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/emfont/emfont/backend/internal/platform/objectversionbackfill"
)

func main() {
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "object-version-backfill: command-line arguments are not accepted")
		os.Exit(2)
	}
	config, err := objectversionbackfill.LoadConfig(os.LookupEnv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "object-version-backfill: invalid environment: %v\n", err)
		os.Exit(2)
	}
	store, err := objectversionbackfill.NewMinIOStore(config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "object-version-backfill: startup failed: %v\n", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result, runErr := objectversionbackfill.Run(ctx, store, config.Concurrency)
	fmt.Printf(
		"object-version-backfill: scanned=%d null_versions=%d rewritten=%d already_versioned=%d\n",
		result.Scanned,
		result.NullVersions,
		result.Rewritten,
		result.AlreadyVersioned,
	)
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "object-version-backfill: failed: %v\n", runErr)
		os.Exit(1)
	}
}
