package postgres

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

var ErrInvalidConfig = errors.New("invalid postgres config")

func (cfg Config) Validate() error {
	var problems []error

	if cfg.DatabaseURL != "" {
		if _, err := url.ParseRequestURI(cfg.DatabaseURL); err != nil {
			problems = append(problems, fmt.Errorf("database_url: %w", err))
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
