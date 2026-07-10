package postgres

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	DefaultPort    uint16 = 5432
	DefaultSSLMode        = "disable"
)

// Config contains the PostgreSQL connection settings used by the backend.
type Config struct {
	DatabaseURL string

	Host     string
	Port     uint16
	Database string
	User     string
	Password string
	SSLMode  string

	ApplicationName  string
	StatementTimeout time.Duration
	MaxConns         int32
	MinConns         int32
	MinIdleConns     int32
	MaxConnLifetime  time.Duration
	MaxConnIdleTime  time.Duration
	HealthCheckEvery time.Duration
}

// PoolOption mutates the parsed pgx pool config before the pool is opened.
type PoolOption func(*pgxpool.Config)

// NewPool validates cfg, opens a pgx pool, and verifies that PostgreSQL is reachable.
func NewPool(ctx context.Context, cfg Config, options ...PoolOption) (*pgxpool.Pool, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	poolConfig, err := pgxpool.ParseConfig(cfg.ConnectionString())
	if err != nil {
		return nil, fmt.Errorf("parse postgres config: %w", err)
	}

	applyPoolConfig(poolConfig, cfg)
	for _, option := range options {
		option(poolConfig)
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("open postgres pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	return pool, nil
}

// ConnectionString returns DatabaseURL when present, otherwise a URL built from fields.
func (cfg Config) ConnectionString() string {
	if cfg.DatabaseURL != "" {
		return cfg.DatabaseURL
	}

	port := cfg.Port
	if port == 0 {
		port = DefaultPort
	}

	sslMode := cfg.SSLMode
	if sslMode == "" {
		sslMode = DefaultSSLMode
	}

	dsn := url.URL{
		Scheme: "postgres",
		Host:   net.JoinHostPort(cfg.Host, strconv.FormatUint(uint64(port), 10)),
		Path:   cfg.Database,
	}
	if cfg.User != "" {
		dsn.User = url.UserPassword(cfg.User, cfg.Password)
	}

	query := dsn.Query()
	query.Set("sslmode", sslMode)
	dsn.RawQuery = query.Encode()

	return dsn.String()
}

// WithQueryTracer installs a pgx query tracer on new pool connections.
func WithQueryTracer(tracer pgx.QueryTracer) PoolOption {
	return func(cfg *pgxpool.Config) {
		cfg.ConnConfig.Tracer = tracer
	}
}

func applyPoolConfig(poolConfig *pgxpool.Config, cfg Config) {
	if cfg.ApplicationName != "" {
		poolConfig.ConnConfig.RuntimeParams["application_name"] = cfg.ApplicationName
	}
	if cfg.StatementTimeout > 0 {
		poolConfig.ConnConfig.RuntimeParams["statement_timeout"] = strconv.FormatInt(cfg.StatementTimeout.Milliseconds(), 10)
	}
	if cfg.MaxConns > 0 {
		poolConfig.MaxConns = cfg.MaxConns
	}
	if cfg.MinConns > 0 {
		poolConfig.MinConns = cfg.MinConns
	}
	if cfg.MinIdleConns > 0 {
		poolConfig.MinIdleConns = cfg.MinIdleConns
	}
	if cfg.MaxConnLifetime > 0 {
		poolConfig.MaxConnLifetime = cfg.MaxConnLifetime
	}
	if cfg.MaxConnIdleTime > 0 {
		poolConfig.MaxConnIdleTime = cfg.MaxConnIdleTime
	}
	if cfg.HealthCheckEvery > 0 {
		poolConfig.HealthCheckPeriod = cfg.HealthCheckEvery
	}
}
