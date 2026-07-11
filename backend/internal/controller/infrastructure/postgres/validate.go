package postgres

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrInvalidConfig = errors.New("invalid postgres config")

func (cfg Config) Validate() error {
	var problems []error

	if cfg.DatabaseURL != "" {
		if err := validateDatabaseURL(cfg.DatabaseURL); err != nil {
			problems = append(problems, err)
		}
	} else {
		if strings.TrimSpace(cfg.Host) == "" {
			problems = append(problems, errors.New("host is required"))
		}
		if strings.TrimSpace(cfg.Database) == "" {
			problems = append(problems, errors.New("database is required"))
		}
		if strings.TrimSpace(cfg.User) == "" {
			problems = append(problems, errors.New("user is required"))
		}
	}

	if cfg.MinConns < 0 {
		problems = append(problems, errors.New("min_conns cannot be negative"))
	}
	if cfg.MaxConns < 0 {
		problems = append(problems, errors.New("max_conns cannot be negative"))
	}
	if cfg.MinConns > 0 && cfg.MaxConns > 0 && cfg.MinConns > cfg.MaxConns {
		problems = append(problems, errors.New("min_conns cannot exceed max_conns"))
	}

	if len(problems) > 0 {
		return fmt.Errorf("%w: %w", ErrInvalidConfig, errors.Join(problems...))
	}
	return nil
}

func validateDatabaseURL(databaseURL string) error {
	parsed, err := url.ParseRequestURI(databaseURL)
	if err != nil {
		return errors.New("database_url is invalid")
	}
	if parsed.User != nil {
		if _, passwordSet := parsed.User.Password(); passwordSet {
			return errors.New("database_url must not include a userinfo password")
		}
	}

	// Parse with the same parser NewPool uses so its later parse cannot expose
	// the connection string through a third-party validation error.
	if _, err := pgxpool.ParseConfig(databaseURL); err != nil {
		return errors.New("database_url is not a valid PostgreSQL connection URL")
	}
	return nil
}

func ValidateMetadataKey(key string) error {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return fmt.Errorf("%w: empty key", ErrInvalidMetadataKey)
	}
	if trimmed != key {
		return fmt.Errorf("%w: key has surrounding whitespace", ErrInvalidMetadataKey)
	}
	if len(key) > 128 {
		return fmt.Errorf("%w: key exceeds 128 characters", ErrInvalidMetadataKey)
	}
	return nil
}
