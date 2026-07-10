package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultEnvironment       = "development"
	defaultServiceName       = "emfont-controller"
	defaultVersion           = "0.1.0"
	defaultAPIVersion        = "v1"
	defaultShutdownTimeout   = 10 * time.Second
	defaultLogLevel          = "info"
	defaultLogFormat         = "text"
	defaultHTTPAddr          = ":8080"
	defaultBackendBaseURL    = ""
	defaultRequestTimeout    = 15 * time.Second
	defaultReadHeaderTimeout = 5 * time.Second
	defaultReadTimeout       = 5 * time.Second
	defaultWriteTimeout      = 20 * time.Second
	defaultIdleTimeout       = 120 * time.Second
	defaultMetricsPath       = "/metrics"
	defaultDatabaseURL       = ""
	defaultMaxOpenConns      = 10
	defaultMinIdleConns      = 0
	defaultConnMaxLifetime   = 30 * time.Minute
	defaultMinIOPresignTTL   = time.Hour
	defaultBuildLease        = 2 * time.Minute
	defaultBuildTimeout      = 90 * time.Second
	defaultBuildConcurrency  = 2
	defaultMaxSourceBytes    = int64(128 << 20)
)

// Config is the controller process configuration assembled from environment
// variables. Keep this package dependency-free so early boot can fail clearly.
type Config struct {
	Environment     string
	ServiceName     string
	Version         string
	APIVersion      string
	ShutdownTimeout time.Duration

	Log           LogConfig
	HTTP          HTTPConfig
	Database      DatabaseConfig
	ObjectStorage ObjectStorageConfig
	FontBuild     FontBuildConfig
	Metrics       MetricsConfig
	Tracing       TracingConfig
}

type LogConfig struct {
	Level  string
	Format string
}

type HTTPConfig struct {
	Addr              string
	BackendBaseURL    string
	RequestTimeout    time.Duration
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
}

type DatabaseConfig struct {
	URL             string
	MaxOpenConns    int
	MinIdleConns    int
	ConnMaxLifetime time.Duration
}

type ObjectStorageConfig struct {
	Enabled       bool
	Endpoint      string
	AccessKey     string
	SecretKey     string
	SessionToken  string
	Bucket        string
	Region        string
	Secure        bool
	PublicBaseURL string
	PresignExpiry time.Duration
}

type FontBuildConfig struct {
	ForceMin               bool
	BuilderVersion         string
	BuildLease             time.Duration
	BuildTimeout           time.Duration
	StaticBuildConcurrency int
	MaxSourceBytes         int64
}

type MetricsConfig struct {
	Enabled bool
	Path    string
}

type TracingConfig struct {
	Enabled      bool
	OTLPEndpoint string
	SampleRatio  float64
}

type lookupFunc func(string) (string, bool)

// Load reads controller configuration from the process environment.
func Load() (Config, error) {
	return LoadWithLookup(os.LookupEnv)
}

// LoadWithLookup reads controller configuration from a caller-provided lookup
// function. Tests and embedding processes can use it without mutating env.
func LoadWithLookup(lookup lookupFunc) (Config, error) {
	var parseErrs []error

	shutdownTimeout, err := getDuration(lookup, "EMFONT_SHUTDOWN_TIMEOUT", defaultShutdownTimeout)
	parseErrs = appendParseErr(parseErrs, err)
	httpRequestTimeout, err := getDuration(lookup, "EMFONT_HTTP_REQUEST_TIMEOUT", defaultRequestTimeout)
	parseErrs = appendParseErr(parseErrs, err)
	httpReadHeaderTimeout, err := getDuration(lookup, "EMFONT_HTTP_READ_HEADER_TIMEOUT", defaultReadHeaderTimeout)
	parseErrs = appendParseErr(parseErrs, err)
	httpReadTimeout, err := getDuration(lookup, "EMFONT_HTTP_READ_TIMEOUT", defaultReadTimeout)
	parseErrs = appendParseErr(parseErrs, err)
	httpWriteTimeout, err := getDuration(lookup, "EMFONT_HTTP_WRITE_TIMEOUT", defaultWriteTimeout)
	parseErrs = appendParseErr(parseErrs, err)
	httpIdleTimeout, err := getDuration(lookup, "EMFONT_HTTP_IDLE_TIMEOUT", defaultIdleTimeout)
	parseErrs = appendParseErr(parseErrs, err)
	databaseMaxOpenConns, err := getInt(lookup, "EMFONT_DATABASE_MAX_OPEN_CONNS", defaultMaxOpenConns)
	parseErrs = appendParseErr(parseErrs, err)
	databaseMinIdleConns, err := getInt(lookup, "EMFONT_DATABASE_MIN_IDLE_CONNS", defaultMinIdleConns)
	parseErrs = appendParseErr(parseErrs, err)
	databaseConnMaxLifetime, err := getDuration(lookup, "EMFONT_DATABASE_CONN_MAX_LIFETIME", defaultConnMaxLifetime)
	parseErrs = appendParseErr(parseErrs, err)
	objectStorageEnabled, err := getBool(lookup, "EMFONT_MINIO_ENABLED", false)
	parseErrs = appendParseErr(parseErrs, err)
	objectStorageSecure, err := getBool(lookup, "EMFONT_MINIO_SECURE", false)
	parseErrs = appendParseErr(parseErrs, err)
	objectStoragePresignExpiry, err := getDuration(lookup, "EMFONT_MINIO_PRESIGN_EXPIRY", defaultMinIOPresignTTL)
	parseErrs = appendParseErr(parseErrs, err)
	fontForceMin, err := getBool(lookup, "EMFONT_FORCE_MIN", false)
	parseErrs = appendParseErr(parseErrs, err)
	fontBuildLease, err := getDuration(lookup, "EMFONT_FONT_BUILD_LEASE", defaultBuildLease)
	parseErrs = appendParseErr(parseErrs, err)
	fontBuildTimeout, err := getDuration(lookup, "EMFONT_FONT_BUILD_TIMEOUT", defaultBuildTimeout)
	parseErrs = appendParseErr(parseErrs, err)
	fontBuildConcurrency, err := getInt(lookup, "EMFONT_FONT_BUILD_CONCURRENCY", defaultBuildConcurrency)
	parseErrs = appendParseErr(parseErrs, err)
	fontMaxSourceBytes, err := getInt64(lookup, "EMFONT_FONT_MAX_SOURCE_BYTES", defaultMaxSourceBytes)
	parseErrs = appendParseErr(parseErrs, err)
	metricsEnabled, err := getBool(lookup, "EMFONT_METRICS_ENABLED", true)
	parseErrs = appendParseErr(parseErrs, err)
	tracingEnabled, err := getBool(lookup, "EMFONT_TRACING_ENABLED", false)
	parseErrs = appendParseErr(parseErrs, err)
	tracingSampleRatio, err := getFloat(lookup, "EMFONT_TRACING_SAMPLE_RATIO", 1.0)
	parseErrs = appendParseErr(parseErrs, err)

	cfg := Config{
		Environment:     getString(lookup, "EMFONT_ENV", defaultEnvironment),
		ServiceName:     getString(lookup, "EMFONT_SERVICE_NAME", defaultServiceName),
		Version:         getString(lookup, "EMFONT_VERSION", defaultVersion),
		APIVersion:      getString(lookup, "EMFONT_API_VERSION", defaultAPIVersion),
		ShutdownTimeout: shutdownTimeout,
		Log: LogConfig{
			Level:  strings.ToLower(getString(lookup, "EMFONT_LOG_LEVEL", defaultLogLevel)),
			Format: strings.ToLower(getString(lookup, "EMFONT_LOG_FORMAT", defaultLogFormat)),
		},
		HTTP: HTTPConfig{
			Addr:              getString(lookup, "EMFONT_HTTP_ADDR", defaultHTTPAddr),
			BackendBaseURL:    getString(lookup, "EMFONT_BACKEND_BASE_URL", defaultBackendBaseURL),
			RequestTimeout:    httpRequestTimeout,
			ReadHeaderTimeout: httpReadHeaderTimeout,
			ReadTimeout:       httpReadTimeout,
			WriteTimeout:      httpWriteTimeout,
			IdleTimeout:       httpIdleTimeout,
		},
		Database: DatabaseConfig{
			URL:             getString(lookup, "EMFONT_DATABASE_URL", defaultDatabaseURL),
			MaxOpenConns:    databaseMaxOpenConns,
			MinIdleConns:    databaseMinIdleConns,
			ConnMaxLifetime: databaseConnMaxLifetime,
		},
		ObjectStorage: ObjectStorageConfig{
			Enabled:       objectStorageEnabled,
			Endpoint:      getString(lookup, "EMFONT_MINIO_ENDPOINT", ""),
			AccessKey:     getString(lookup, "EMFONT_MINIO_ACCESS_KEY", ""),
			SecretKey:     getString(lookup, "EMFONT_MINIO_SECRET_KEY", ""),
			SessionToken:  getString(lookup, "EMFONT_MINIO_SESSION_TOKEN", ""),
			Bucket:        getString(lookup, "EMFONT_MINIO_BUCKET", ""),
			Region:        getString(lookup, "EMFONT_MINIO_REGION", ""),
			Secure:        objectStorageSecure,
			PublicBaseURL: getString(lookup, "EMFONT_MINIO_PUBLIC_BASE_URL", ""),
			PresignExpiry: objectStoragePresignExpiry,
		},
		FontBuild: FontBuildConfig{
			ForceMin:               fontForceMin,
			BuilderVersion:         getString(lookup, "EMFONT_FONT_BUILDER_VERSION", "harfbuzz-woff2-v1"),
			BuildLease:             fontBuildLease,
			BuildTimeout:           fontBuildTimeout,
			StaticBuildConcurrency: fontBuildConcurrency,
			MaxSourceBytes:         fontMaxSourceBytes,
		},
		Metrics: MetricsConfig{
			Enabled: metricsEnabled,
			Path:    getString(lookup, "EMFONT_METRICS_PATH", defaultMetricsPath),
		},
		Tracing: TracingConfig{
			Enabled:      tracingEnabled,
			OTLPEndpoint: getString(lookup, "EMFONT_OTEL_EXPORTER_OTLP_ENDPOINT", ""),
			SampleRatio:  tracingSampleRatio,
		},
	}
	return cfg, errors.Join(errors.Join(parseErrs...), cfg.Validate())
}

func (cfg Config) Validate() error {
	var errs []error

	if strings.TrimSpace(cfg.Environment) == "" {
		errs = append(errs, errors.New("EMFONT_ENV must not be empty"))
	}
	if strings.TrimSpace(cfg.ServiceName) == "" {
		errs = append(errs, errors.New("EMFONT_SERVICE_NAME must not be empty"))
	}
	if strings.TrimSpace(cfg.Version) == "" {
		errs = append(errs, errors.New("EMFONT_VERSION must not be empty"))
	}
	if strings.TrimSpace(cfg.APIVersion) == "" {
		errs = append(errs, errors.New("EMFONT_API_VERSION must not be empty"))
	}
	if cfg.ShutdownTimeout <= 0 {
		errs = append(errs, errors.New("EMFONT_SHUTDOWN_TIMEOUT must be greater than zero"))
	}
	if !oneOf(cfg.Log.Level, "debug", "info", "warn", "error") {
		errs = append(errs, fmt.Errorf("EMFONT_LOG_LEVEL must be one of debug, info, warn, error"))
	}
	if !oneOf(cfg.Log.Format, "text", "json") {
		errs = append(errs, fmt.Errorf("EMFONT_LOG_FORMAT must be one of text, json"))
	}
	if strings.TrimSpace(cfg.HTTP.Addr) == "" {
		errs = append(errs, errors.New("EMFONT_HTTP_ADDR must not be empty"))
	}
	if cfg.HTTP.RequestTimeout <= 0 {
		errs = append(errs, errors.New("EMFONT_HTTP_REQUEST_TIMEOUT must be greater than zero"))
	}
	if cfg.HTTP.ReadHeaderTimeout <= 0 {
		errs = append(errs, errors.New("EMFONT_HTTP_READ_HEADER_TIMEOUT must be greater than zero"))
	}
	if cfg.HTTP.ReadTimeout <= 0 {
		errs = append(errs, errors.New("EMFONT_HTTP_READ_TIMEOUT must be greater than zero"))
	}
	if cfg.HTTP.WriteTimeout <= 0 {
		errs = append(errs, errors.New("EMFONT_HTTP_WRITE_TIMEOUT must be greater than zero"))
	}
	if cfg.HTTP.WriteTimeout <= cfg.HTTP.RequestTimeout {
		errs = append(errs, errors.New("EMFONT_HTTP_WRITE_TIMEOUT must be greater than EMFONT_HTTP_REQUEST_TIMEOUT"))
	}
	if cfg.HTTP.IdleTimeout <= 0 {
		errs = append(errs, errors.New("EMFONT_HTTP_IDLE_TIMEOUT must be greater than zero"))
	}
	if cfg.Database.MaxOpenConns < 0 {
		errs = append(errs, errors.New("EMFONT_DATABASE_MAX_OPEN_CONNS must be zero or greater"))
	}
	if cfg.Database.MinIdleConns < 0 {
		errs = append(errs, errors.New("EMFONT_DATABASE_MIN_IDLE_CONNS must be zero or greater"))
	}
	if cfg.Database.MaxOpenConns > 0 && cfg.Database.MinIdleConns > cfg.Database.MaxOpenConns {
		errs = append(errs, errors.New("EMFONT_DATABASE_MIN_IDLE_CONNS must be less than or equal to EMFONT_DATABASE_MAX_OPEN_CONNS"))
	}
	if cfg.Database.ConnMaxLifetime <= 0 {
		errs = append(errs, errors.New("EMFONT_DATABASE_CONN_MAX_LIFETIME must be greater than zero"))
	}
	if cfg.ObjectStorage.Enabled {
		if strings.TrimSpace(cfg.ObjectStorage.Endpoint) == "" {
			errs = append(errs, errors.New("EMFONT_MINIO_ENDPOINT must not be empty when MinIO is enabled"))
		}
		if strings.TrimSpace(cfg.ObjectStorage.Bucket) == "" {
			errs = append(errs, errors.New("EMFONT_MINIO_BUCKET must not be empty when MinIO is enabled"))
		}
		if strings.TrimSpace(cfg.ObjectStorage.AccessKey) == "" || strings.TrimSpace(cfg.ObjectStorage.SecretKey) == "" {
			errs = append(errs, errors.New("EMFONT_MINIO_ACCESS_KEY and EMFONT_MINIO_SECRET_KEY must not be empty when MinIO is enabled"))
		}
	}
	if cfg.ObjectStorage.PresignExpiry <= 0 {
		errs = append(errs, errors.New("EMFONT_MINIO_PRESIGN_EXPIRY must be greater than zero"))
	}
	if cfg.ObjectStorage.PresignExpiry < time.Second || cfg.ObjectStorage.PresignExpiry > 7*24*time.Hour || cfg.ObjectStorage.PresignExpiry%time.Second != 0 {
		errs = append(errs, errors.New("EMFONT_MINIO_PRESIGN_EXPIRY must be a whole-second duration between 1s and 168h"))
	}
	if strings.TrimSpace(cfg.FontBuild.BuilderVersion) == "" {
		errs = append(errs, errors.New("EMFONT_FONT_BUILDER_VERSION must not be empty"))
	}
	if cfg.FontBuild.BuildLease <= 0 {
		errs = append(errs, errors.New("EMFONT_FONT_BUILD_LEASE must be greater than zero"))
	}
	if cfg.FontBuild.BuildTimeout <= 0 {
		errs = append(errs, errors.New("EMFONT_FONT_BUILD_TIMEOUT must be greater than zero"))
	}
	if cfg.FontBuild.BuildLease <= cfg.FontBuild.BuildTimeout {
		errs = append(errs, errors.New("EMFONT_FONT_BUILD_LEASE must be greater than EMFONT_FONT_BUILD_TIMEOUT"))
	}
	if cfg.FontBuild.StaticBuildConcurrency <= 0 {
		errs = append(errs, errors.New("EMFONT_FONT_BUILD_CONCURRENCY must be greater than zero"))
	}
	if cfg.FontBuild.MaxSourceBytes <= 0 {
		errs = append(errs, errors.New("EMFONT_FONT_MAX_SOURCE_BYTES must be greater than zero"))
	}
	if cfg.Metrics.Enabled {
		if !strings.HasPrefix(cfg.Metrics.Path, "/") {
			errs = append(errs, errors.New("EMFONT_METRICS_PATH must start with /"))
		}
	}
	if cfg.Tracing.Enabled && strings.TrimSpace(cfg.Tracing.OTLPEndpoint) == "" {
		errs = append(errs, errors.New("EMFONT_OTEL_EXPORTER_OTLP_ENDPOINT must not be empty when tracing is enabled"))
	}
	if cfg.Tracing.SampleRatio < 0 || cfg.Tracing.SampleRatio > 1 {
		errs = append(errs, errors.New("EMFONT_TRACING_SAMPLE_RATIO must be between 0 and 1"))
	}

	return errors.Join(errs...)
}

func getString(lookup lookupFunc, key, fallback string) string {
	if value, ok := lookup(key); ok {
		return strings.TrimSpace(value)
	}
	return fallback
}

func getDuration(lookup lookupFunc, key string, fallback time.Duration) (time.Duration, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return fallback, fmt.Errorf("%s must be a duration: %w", key, err)
	}
	return parsed, nil
}

func getInt(lookup lookupFunc, key string, fallback int) (int, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return fallback, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return parsed, nil
}

func getInt64(lookup lookupFunc, key string, fallback int64) (int64, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return fallback, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return parsed, nil
}

func getBool(lookup lookupFunc, key string, fallback bool) (bool, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return fallback, fmt.Errorf("%s must be a boolean: %w", key, err)
	}
	return parsed, nil
}

func getFloat(lookup lookupFunc, key string, fallback float64) (float64, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return fallback, fmt.Errorf("%s must be a number: %w", key, err)
	}
	return parsed, nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func appendParseErr(errs []error, err error) []error {
	if err != nil {
		return append(errs, err)
	}
	return errs
}
