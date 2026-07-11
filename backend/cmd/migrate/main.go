package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"

	"github.com/emfont/emfont/backend/internal/controller/config"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	gooselock "github.com/pressly/goose/v3/lock"
)

const migrationAdvisoryLockID = gooselock.DefaultLockID

func main() {
	if err := run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "migration failed: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	return runContext(context.Background(), args)
}

func runContext(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	command := flags.String("command", "status", "migration command: status, up, down")
	dir := flags.String("dir", "db/migrations", "migration directory")
	databaseURL := flags.String("database-connection-string", "", "PostgreSQL connection string")
	if err := flags.Parse(args); err != nil {
		return err
	}

	dsn := *databaseURL
	if dsn == "" {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		dsn = cfg.Database.URL
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	switch *command {
	case "status":
		if err := goose.SetDialect("postgres"); err != nil {
			return fmt.Errorf("set goose dialect: %w", err)
		}
		return goose.StatusContext(ctx, db, *dir)
	case "up":
		return runLockedMigration(ctx, db, *dir, true)
	case "down":
		return runLockedMigration(ctx, db, *dir, false)
	default:
		return fmt.Errorf("unsupported command %q", *command)
	}
}

func runLockedMigration(ctx context.Context, db *sql.DB, dir string, up bool) error {
	locker, err := gooselock.NewPostgresSessionLocker(
		gooselock.WithLockID(migrationAdvisoryLockID),
		gooselock.WithLockTimeout(1, 300),
		gooselock.WithUnlockTimeout(1, 60),
	)
	if err != nil {
		return fmt.Errorf("configure postgres migration lock: %w", err)
	}
	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		os.DirFS(dir),
		goose.WithSessionLocker(locker),
		goose.WithVerbose(true),
	)
	if err != nil {
		return fmt.Errorf("configure goose migrations: %w", err)
	}
	if up {
		_, err = provider.Up(ctx)
		if err != nil {
			return fmt.Errorf("migrate up: %w", err)
		}
		return nil
	}
	_, err = provider.Down(ctx)
	if err != nil {
		return fmt.Errorf("migrate down: %w", err)
	}
	return nil
}
