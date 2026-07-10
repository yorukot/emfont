package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"

	"github.com/emfont/emfont/backend/internal/controller/config"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "migration failed: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
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

	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}

	switch *command {
	case "status":
		return goose.Status(db, *dir)
	case "up":
		return goose.Up(db, *dir)
	case "down":
		return goose.Down(db, *dir)
	default:
		return fmt.Errorf("unsupported command %q", *command)
	}
}
