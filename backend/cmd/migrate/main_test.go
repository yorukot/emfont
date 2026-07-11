package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestConcurrentMigrationsUseDedicatedSessionLockIntegration(t *testing.T) {
	databaseURL := os.Getenv("EMFONT_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("EMFONT_TEST_DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	controlConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse test database config: %v", err)
	}
	controlPool, err := pgxpool.NewWithConfig(ctx, controlConfig)
	if err != nil {
		t.Fatalf("open control database: %v", err)
	}
	t.Cleanup(controlPool.Close)

	databaseName := fmt.Sprintf("emfont_migrate_lock_%x", time.Now().UnixNano())
	quotedDatabaseName := pgx.Identifier{databaseName}.Sanitize()
	if _, err := controlPool.Exec(ctx, "CREATE DATABASE "+quotedDatabaseName); err != nil {
		var postgresError *pgconn.PgError
		if errors.As(err, &postgresError) && postgresError.Code == "42501" {
			t.Skipf("test database role cannot create the isolated migration database: %v", err)
		}
		t.Fatalf("create isolated migration database: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := controlPool.Exec(
			cleanupCtx,
			"DROP DATABASE "+quotedDatabaseName+" WITH (FORCE)",
		); err != nil {
			t.Errorf("drop isolated migration database: %v", err)
		}
	})

	targetConfig := controlConfig.Copy()
	targetConfig.ConnConfig.Database = databaseName
	targetPool, err := pgxpool.NewWithConfig(ctx, targetConfig)
	if err != nil {
		t.Fatalf("open isolated migration database: %v", err)
	}
	t.Cleanup(targetPool.Close)
	targetURL := stdlib.RegisterConnConfig(targetConfig.ConnConfig)
	t.Cleanup(func() { stdlib.UnregisterConnConfig(targetURL) })

	migrationDir := t.TempDir()
	firstMigration := fmt.Sprintf(`-- +goose Up
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_catalog.pg_locks AS lock_record
        WHERE lock_record.locktype = 'advisory'
          AND lock_record.pid = pg_backend_pid()
          AND lock_record.granted
          AND ((lock_record.classid::BIGINT << 32) | lock_record.objid::BIGINT) = %d
    ) THEN
        RAISE EXCEPTION 'migration advisory lock is not held on the migration session';
    END IF;
    PERFORM pg_sleep(1.5);
END;
$$;
-- +goose StatementEnd
CREATE TABLE migration_lock_probe (id INTEGER PRIMARY KEY);

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_catalog.pg_locks AS lock_record
        WHERE lock_record.locktype = 'advisory'
          AND lock_record.pid = pg_backend_pid()
          AND lock_record.granted
          AND ((lock_record.classid::BIGINT << 32) | lock_record.objid::BIGINT) = %d
    ) THEN
        RAISE EXCEPTION 'migration advisory lock is not held on the migration session';
    END IF;
END;
$$;
-- +goose StatementEnd
DROP TABLE migration_lock_probe;
`, migrationAdvisoryLockID, migrationAdvisoryLockID)
	if err := os.WriteFile(filepath.Join(migrationDir, "000001_lock_probe.sql"), []byte(firstMigration), 0o600); err != nil {
		t.Fatalf("write lock probe migration: %v", err)
	}

	args := []string{
		"-command", "up",
		"-dir", migrationDir,
		"-database-connection-string", targetURL,
	}
	start := make(chan struct{})
	type migrationResult struct {
		index int
		err   error
	}
	results := make(chan migrationResult, 2)
	for index := 0; index < 2; index++ {
		go func(index int) {
			<-start
			results <- migrationResult{index: index, err: runContext(ctx, args)}
		}(index)
	}
	close(start)
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent migration %d: %v", result.index, result.err)
		}
	}

	var probeExists bool
	var appliedRows int
	if err := targetPool.QueryRow(ctx, `
		SELECT to_regclass('public.migration_lock_probe') IS NOT NULL,
		       (SELECT COUNT(*) FROM goose_db_version WHERE version_id = 1 AND is_applied)
	`).Scan(&probeExists, &appliedRows); err != nil {
		t.Fatalf("inspect concurrent migration result: %v", err)
	}
	if !probeExists || appliedRows != 1 {
		t.Fatalf("concurrent migration result = table:%t applied rows:%d, want true and 1", probeExists, appliedRows)
	}

	failingMigration := fmt.Sprintf(`-- +goose Up
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_catalog.pg_locks AS lock_record
        WHERE lock_record.locktype = 'advisory'
          AND lock_record.pid = pg_backend_pid()
          AND lock_record.granted
          AND ((lock_record.classid::BIGINT << 32) | lock_record.objid::BIGINT) = %d
    ) THEN
        RAISE EXCEPTION 'migration advisory lock is not held on the migration session';
    END IF;
    RAISE EXCEPTION 'intentional migration failure';
END;
$$;
-- +goose StatementEnd

-- +goose Down
SELECT TRUE;
`, migrationAdvisoryLockID)
	if err := os.WriteFile(filepath.Join(migrationDir, "000002_failure.sql"), []byte(failingMigration), 0o600); err != nil {
		t.Fatalf("write failing migration: %v", err)
	}
	if err := runContext(ctx, args); err == nil || !strings.Contains(err.Error(), "intentional migration failure") {
		t.Fatalf("failing migration error = %v, want intentional failure", err)
	}

	connection, err := targetPool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire lock verification connection: %v", err)
	}
	defer connection.Release()
	var lockAcquired bool
	if err := connection.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", migrationAdvisoryLockID).Scan(&lockAcquired); err != nil {
		t.Fatalf("verify migration lock release: %v", err)
	}
	if !lockAcquired {
		t.Fatal("migration advisory lock remained held after migration failure")
	}
	var unlocked bool
	if err := connection.QueryRow(ctx, "SELECT pg_advisory_unlock($1)", migrationAdvisoryLockID).Scan(&unlocked); err != nil {
		t.Fatalf("release verification advisory lock: %v", err)
	}
	if !unlocked {
		t.Fatal("verification advisory lock was not held by its dedicated connection")
	}

	downArgs := append([]string(nil), args...)
	downArgs[1] = "down"
	if err := runContext(ctx, downArgs); err != nil {
		t.Fatalf("migrate down with dedicated session lock: %v", err)
	}
	if err := targetPool.QueryRow(ctx, "SELECT to_regclass('public.migration_lock_probe') IS NULL").Scan(&probeExists); err != nil {
		t.Fatalf("inspect down migration result: %v", err)
	}
	if !probeExists {
		t.Fatal("down migration did not remove lock probe table")
	}
}
