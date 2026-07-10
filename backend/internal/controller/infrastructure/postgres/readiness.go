package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNilPool = errors.New("postgres pool is nil")

// Readiness reports whether the database dependency is usable.
type Readiness struct {
	Ready     bool
	CheckedAt time.Time
	Latency   time.Duration
	Error     string
}

// Ping verifies that the pool can acquire a connection and round-trip to PostgreSQL.
func Ping(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return ErrNilPool
	}
	return pool.Ping(ctx)
}

func FontSchemaReady(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return ErrNilPool
	}
	var ready bool
	err := pool.QueryRow(ctx, `
		SELECT to_regclass('public.font_family') IS NOT NULL
		   AND to_regclass('public.font_sources') IS NOT NULL
		   AND to_regclass('public.font_artifacts') IS NOT NULL
		   AND to_regclass('public.font_build_jobs') IS NOT NULL`).Scan(&ready)
	if err != nil {
		return fmt.Errorf("check font schema: %w", err)
	}
	if !ready {
		return errors.New("font database migrations are not applied")
	}
	return nil
}

// CheckReadiness is a status-oriented wrapper around Ping for health endpoints.
func CheckReadiness(ctx context.Context, pool *pgxpool.Pool) Readiness {
	startedAt := time.Now()
	status := Readiness{
		CheckedAt: startedAt,
	}

	err := Ping(ctx, pool)
	status.Latency = time.Since(startedAt)
	if err != nil {
		status.Error = err.Error()
		return status
	}

	status.Ready = true
	return status
}
