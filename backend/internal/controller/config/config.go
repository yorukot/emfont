package config

import (
	"errors"
	"fmt"
	"math"
	"net/netip"
	"net/url"
	"os"
	pathpkg "path"
	"strconv"
	"strings"
	"time"
)

const (
	defaultServiceName              = "emfont-controller"
	defaultVersion                  = "0.1.0"
	defaultAPIVersion               = "v1"
	defaultShutdownTimeout          = 30 * time.Second
	defaultShutdownPropagationDelay = 5 * time.Second
	shutdownResourceCloseMargin     = 10 * time.Second
	defaultLogLevel                 = "info"
	defaultLogFormat                = "text"
	defaultHTTPAddr                 = ":8080"
	defaultBackendBaseURL           = ""
	defaultRequestTimeout           = 15 * time.Second
	defaultReadHeaderTimeout        = 5 * time.Second
	defaultReadTimeout              = 5 * time.Second
	defaultWriteTimeout             = 20 * time.Second
	defaultIdleTimeout              = 120 * time.Second
	defaultMaxHeaderBytes           = 64 << 10
	defaultCORSOrigins              = "*"
	defaultRateLimitRPS             = 20.0
	defaultRateLimitBurst           = 40
	defaultGlobalRateLimitRPS       = 200.0
	defaultGlobalRateBurst          = 400
	defaultRateLimitClients         = 10_000
	maxRateLimitClients             = 100_000
	defaultRateLimitIdle            = 10 * time.Minute
	defaultMetricsPath              = "/metrics"
	defaultMetricsAuthRateRPS       = 5.0
	defaultMetricsAuthBurst         = 10
	defaultDatabaseURL              = ""
	defaultMaxOpenConns             = 10
	defaultMinIdleConns             = 0
	defaultConnMaxLifetime          = 30 * time.Minute
	defaultMinIOPresignTTL          = time.Hour
	defaultBuildLease               = 2 * time.Minute
	defaultBuildTimeout             = 90 * time.Second
	defaultBuildConcurrency         = 2
	defaultMaxPendingBuilds         = 16
	defaultMaxArtifacts             = int64(100_000)
	defaultMaxAccountedBytes        = int64(50 << 30)
	defaultMaxTerminalFailures      = int64(10_000)
	defaultMaxSourceBytes           = int64(128 << 20)
	defaultFontWorkerPath           = "emfont-fontworker"
	defaultWorkerMaxOutput          = int64(128 << 20)
	defaultWorkerAddressSpace       = int64(2 << 30)
	defaultWorkerCPUSeconds         = int64(60)
	defaultWorkerFileSize           = int64(128 << 20)
	defaultWorkerOpenFiles          = int64(32)
	defaultWorkerStderrBytes        = int64(16 << 10)
	defaultArtifactTouch            = 5 * time.Minute
	defaultArtifactRetention        = 30 * 24 * time.Hour
	defaultRetirementGrace          = 2 * time.Hour
	defaultOrphanGrace              = 6 * time.Hour
	defaultCleanupTimeout           = 30 * time.Minute
	defaultCleanupDBBatch           = 100
	defaultCleanupMaxDBRows         = 10_000
	defaultCleanupObjectPage        = 500
	defaultCleanupMaxPages          = 10_000
)

// Config is the controller process configuration assembled from environment
// variables. Keep this package dependency-free so early boot can fail clearly.
type Config struct {
	Environment              string
	ServiceName              string
	Version                  string
	APIVersion               string
	ShutdownTimeout          time.Duration
	ShutdownPropagationDelay time.Duration

	Log           LogConfig
	HTTP          HTTPConfig
	RateLimit     RateLimitConfig
	Database      DatabaseConfig
	ObjectStorage ObjectStorageConfig
	FontBuild     FontBuildConfig
	Cleanup       ArtifactCleanupConfig
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
	MaxHeaderBytes    int
	AllowedOrigins    []string
}

type DatabaseConfig struct {
	URL             string
	MaxOpenConns    int
	MinIdleConns    int
	ConnMaxLifetime time.Duration
}

type RateLimitConfig struct {
	Enabled                 bool
	RequestsPerSecond       float64
	Burst                   int
	GlobalRequestsPerSecond float64
	GlobalBurst             int
	MaxClients              int
	IdleTimeout             time.Duration
	TrustProxyHeaders       bool
	TrustedProxyCIDRs       []netip.Prefix
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
	ForceMin                bool
	BuilderVersion          string
	BuildLease              time.Duration
	BuildTimeout            time.Duration
	StaticBuildConcurrency  int
	MaxPendingBuilds        int
	MaxArtifacts            int64
	MaxAccountedBytes       int64
	MaxTerminalFailures     int64
	MaxSourceBytes          int64
	WorkerPath              string
	WorkerMaxOutputBytes    int64
	WorkerAddressSpaceBytes int64
	WorkerCPUSeconds        int64
	WorkerFileSizeBytes     int64
	WorkerOpenFiles         int64
	WorkerStderrBytes       int64
	ArtifactTouchInterval   time.Duration
}

type ArtifactCleanupConfig struct {
	ArtifactRetention time.Duration
	RetirementGrace   time.Duration
	OrphanGrace       time.Duration
	Timeout           time.Duration
	DatabaseBatchSize int
	MaxDatabaseRows   int
	ObjectPageSize    int
	MaxObjectPages    int
}

type MetricsConfig struct {
	Enabled               bool
	Path                  string
	BearerToken           string
	AuthRequestsPerSecond float64
	AuthBurst             int
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
	shutdownPropagationDelay, err := getDuration(lookup, "EMFONT_SHUTDOWN_PROPAGATION_DELAY", defaultShutdownPropagationDelay)
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
	httpMaxHeaderBytes, err := getInt(lookup, "EMFONT_HTTP_MAX_HEADER_BYTES", defaultMaxHeaderBytes)
	parseErrs = appendParseErr(parseErrs, err)
	rateLimitEnabled, err := getBool(lookup, "EMFONT_RATE_LIMIT_ENABLED", true)
	parseErrs = appendParseErr(parseErrs, err)
	rateLimitRPS, err := getFloat(lookup, "EMFONT_RATE_LIMIT_REQUESTS_PER_SECOND", defaultRateLimitRPS)
	parseErrs = appendParseErr(parseErrs, err)
	rateLimitBurst, err := getInt(lookup, "EMFONT_RATE_LIMIT_BURST", defaultRateLimitBurst)
	parseErrs = appendParseErr(parseErrs, err)
	globalRateLimitRPS, err := getFloat(lookup, "EMFONT_GLOBAL_RATE_LIMIT_REQUESTS_PER_SECOND", defaultGlobalRateLimitRPS)
	parseErrs = appendParseErr(parseErrs, err)
	globalRateLimitBurst, err := getInt(lookup, "EMFONT_GLOBAL_RATE_LIMIT_BURST", defaultGlobalRateBurst)
	parseErrs = appendParseErr(parseErrs, err)
	rateLimitClients, err := getInt(lookup, "EMFONT_RATE_LIMIT_MAX_CLIENTS", defaultRateLimitClients)
	parseErrs = appendParseErr(parseErrs, err)
	rateLimitIdle, err := getDuration(lookup, "EMFONT_RATE_LIMIT_IDLE_TIMEOUT", defaultRateLimitIdle)
	parseErrs = appendParseErr(parseErrs, err)
	trustProxyHeaders, err := getBool(lookup, "EMFONT_TRUST_PROXY_HEADERS", false)
	parseErrs = appendParseErr(parseErrs, err)
	trustedProxyCIDRs, err := getIPPrefixes(lookup, "EMFONT_TRUSTED_PROXY_CIDRS")
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
	fontMaxPendingBuilds, err := getInt(lookup, "EMFONT_FONT_MAX_PENDING_BUILDS", defaultMaxPendingBuilds)
	parseErrs = appendParseErr(parseErrs, err)
	fontMaxArtifacts, err := getInt64(lookup, "EMFONT_FONT_MAX_ARTIFACTS", defaultMaxArtifacts)
	parseErrs = appendParseErr(parseErrs, err)
	fontMaxAccountedBytes, err := getInt64(lookup, "EMFONT_FONT_MAX_ACCOUNTED_BYTES", defaultMaxAccountedBytes)
	parseErrs = appendParseErr(parseErrs, err)
	fontMaxTerminalFailures, err := getInt64(lookup, "EMFONT_FONT_MAX_TERMINAL_FAILURES", defaultMaxTerminalFailures)
	parseErrs = appendParseErr(parseErrs, err)
	fontMaxSourceBytes, err := getInt64(lookup, "EMFONT_FONT_MAX_SOURCE_BYTES", defaultMaxSourceBytes)
	parseErrs = appendParseErr(parseErrs, err)
	fontWorkerMaxOutput, err := getInt64(lookup, "EMFONT_FONT_WORKER_MAX_OUTPUT_BYTES", defaultWorkerMaxOutput)
	parseErrs = appendParseErr(parseErrs, err)
	fontWorkerAddressSpace, err := getInt64(lookup, "EMFONT_FONT_WORKER_ADDRESS_SPACE_BYTES", defaultWorkerAddressSpace)
	parseErrs = appendParseErr(parseErrs, err)
	fontWorkerCPUSeconds, err := getInt64(lookup, "EMFONT_FONT_WORKER_CPU_SECONDS", defaultWorkerCPUSeconds)
	parseErrs = appendParseErr(parseErrs, err)
	fontWorkerFileSize, err := getInt64(lookup, "EMFONT_FONT_WORKER_FILE_SIZE_BYTES", defaultWorkerFileSize)
	parseErrs = appendParseErr(parseErrs, err)
	fontWorkerOpenFiles, err := getInt64(lookup, "EMFONT_FONT_WORKER_OPEN_FILES", defaultWorkerOpenFiles)
	parseErrs = appendParseErr(parseErrs, err)
	fontWorkerStderrBytes, err := getInt64(lookup, "EMFONT_FONT_WORKER_STDERR_BYTES", defaultWorkerStderrBytes)
	parseErrs = appendParseErr(parseErrs, err)
	fontArtifactTouch, err := getDuration(lookup, "EMFONT_FONT_ARTIFACT_TOUCH_INTERVAL", defaultArtifactTouch)
	parseErrs = appendParseErr(parseErrs, err)
	artifactRetention, err := getDuration(lookup, "EMFONT_CLEANUP_ARTIFACT_RETENTION", defaultArtifactRetention)
	parseErrs = appendParseErr(parseErrs, err)
	retirementGrace, err := getDuration(lookup, "EMFONT_CLEANUP_RETIREMENT_GRACE", defaultRetirementGrace)
	parseErrs = appendParseErr(parseErrs, err)
	orphanGrace, err := getDuration(lookup, "EMFONT_CLEANUP_ORPHAN_GRACE", defaultOrphanGrace)
	parseErrs = appendParseErr(parseErrs, err)
	cleanupTimeout, err := getDuration(lookup, "EMFONT_CLEANUP_TIMEOUT", defaultCleanupTimeout)
	parseErrs = appendParseErr(parseErrs, err)
	cleanupDBBatch, err := getInt(lookup, "EMFONT_CLEANUP_DATABASE_BATCH_SIZE", defaultCleanupDBBatch)
	parseErrs = appendParseErr(parseErrs, err)
	cleanupMaxDBRows, err := getInt(lookup, "EMFONT_CLEANUP_MAX_DATABASE_ROWS", defaultCleanupMaxDBRows)
	parseErrs = appendParseErr(parseErrs, err)
	cleanupObjectPage, err := getInt(lookup, "EMFONT_CLEANUP_OBJECT_PAGE_SIZE", defaultCleanupObjectPage)
	parseErrs = appendParseErr(parseErrs, err)
	cleanupMaxPages, err := getInt(lookup, "EMFONT_CLEANUP_MAX_OBJECT_PAGES", defaultCleanupMaxPages)
	parseErrs = appendParseErr(parseErrs, err)
	metricsEnabled, err := getBool(lookup, "EMFONT_METRICS_ENABLED", false)
	parseErrs = appendParseErr(parseErrs, err)
	metricsAuthRate, err := getFloat(lookup, "EMFONT_METRICS_AUTH_RATE_LIMIT_REQUESTS_PER_SECOND", defaultMetricsAuthRateRPS)
	parseErrs = appendParseErr(parseErrs, err)
	metricsAuthBurst, err := getInt(lookup, "EMFONT_METRICS_AUTH_RATE_LIMIT_BURST", defaultMetricsAuthBurst)
	parseErrs = appendParseErr(parseErrs, err)
	tracingEnabled, err := getBool(lookup, "EMFONT_TRACING_ENABLED", false)
	parseErrs = appendParseErr(parseErrs, err)
	tracingSampleRatio, err := getFloat(lookup, "EMFONT_TRACING_SAMPLE_RATIO", 1.0)
	parseErrs = appendParseErr(parseErrs, err)

	cfg := Config{
		Environment:              strings.ToLower(getString(lookup, "EMFONT_ENV", "")),
		ServiceName:              getString(lookup, "EMFONT_SERVICE_NAME", defaultServiceName),
		Version:                  getString(lookup, "EMFONT_VERSION", defaultVersion),
		APIVersion:               getString(lookup, "EMFONT_API_VERSION", defaultAPIVersion),
		ShutdownTimeout:          shutdownTimeout,
		ShutdownPropagationDelay: shutdownPropagationDelay,
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
			MaxHeaderBytes:    httpMaxHeaderBytes,
			AllowedOrigins:    splitCSV(getString(lookup, "EMFONT_CORS_ALLOWED_ORIGINS", defaultCORSOrigins)),
		},
		RateLimit: RateLimitConfig{
			Enabled: rateLimitEnabled, RequestsPerSecond: rateLimitRPS,
			Burst: rateLimitBurst, GlobalRequestsPerSecond: globalRateLimitRPS,
			GlobalBurst: globalRateLimitBurst, MaxClients: rateLimitClients,
			IdleTimeout: rateLimitIdle, TrustProxyHeaders: trustProxyHeaders,
			TrustedProxyCIDRs: trustedProxyCIDRs,
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
			ForceMin:                fontForceMin,
			BuilderVersion:          getString(lookup, "EMFONT_FONT_BUILDER_VERSION", "harfbuzz-woff2-v1"),
			BuildLease:              fontBuildLease,
			BuildTimeout:            fontBuildTimeout,
			StaticBuildConcurrency:  fontBuildConcurrency,
			MaxPendingBuilds:        fontMaxPendingBuilds,
			MaxArtifacts:            fontMaxArtifacts,
			MaxAccountedBytes:       fontMaxAccountedBytes,
			MaxTerminalFailures:     fontMaxTerminalFailures,
			MaxSourceBytes:          fontMaxSourceBytes,
			WorkerPath:              getString(lookup, "EMFONT_FONT_WORKER_PATH", defaultFontWorkerPath),
			WorkerMaxOutputBytes:    fontWorkerMaxOutput,
			WorkerAddressSpaceBytes: fontWorkerAddressSpace,
			WorkerCPUSeconds:        fontWorkerCPUSeconds,
			WorkerFileSizeBytes:     fontWorkerFileSize,
			WorkerOpenFiles:         fontWorkerOpenFiles,
			WorkerStderrBytes:       fontWorkerStderrBytes,
			ArtifactTouchInterval:   fontArtifactTouch,
		},
		Cleanup: ArtifactCleanupConfig{
			ArtifactRetention: artifactRetention,
			RetirementGrace:   retirementGrace,
			OrphanGrace:       orphanGrace,
			Timeout:           cleanupTimeout,
			DatabaseBatchSize: cleanupDBBatch,
			MaxDatabaseRows:   cleanupMaxDBRows,
			ObjectPageSize:    cleanupObjectPage,
			MaxObjectPages:    cleanupMaxPages,
		},
		Metrics: MetricsConfig{
			Enabled: metricsEnabled, Path: getString(lookup, "EMFONT_METRICS_PATH", defaultMetricsPath),
			BearerToken:           getString(lookup, "EMFONT_METRICS_BEARER_TOKEN", ""),
			AuthRequestsPerSecond: metricsAuthRate, AuthBurst: metricsAuthBurst,
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
	environment := strings.ToLower(strings.TrimSpace(cfg.Environment))
	hardened := IsHardenedEnvironment(environment)

	if !oneOf(environment, "development", "local", "test", "staging", "production", "migration", "cleanup") {
		errs = append(errs, errors.New("EMFONT_ENV must be one of development, local, test, staging, production, migration, cleanup"))
	}
	if strings.TrimSpace(cfg.ServiceName) == "" {
		errs = append(errs, errors.New("EMFONT_SERVICE_NAME must not be empty"))
	}
	if strings.TrimSpace(cfg.Version) == "" {
		errs = append(errs, errors.New("EMFONT_VERSION must not be empty"))
	}
	if !isAPIVersion(cfg.APIVersion) {
		errs = append(errs, errors.New("EMFONT_API_VERSION must match v[0-9]+"))
	}
	if cfg.ShutdownTimeout <= 0 {
		errs = append(errs, errors.New("EMFONT_SHUTDOWN_TIMEOUT must be greater than zero"))
	}
	if cfg.ShutdownPropagationDelay < 0 {
		errs = append(errs, errors.New("EMFONT_SHUTDOWN_PROPAGATION_DELAY must be zero or greater"))
	}
	if cfg.ShutdownTimeout > 0 && cfg.ShutdownPropagationDelay >= cfg.ShutdownTimeout {
		errs = append(errs, errors.New("EMFONT_SHUTDOWN_PROPAGATION_DELAY must be less than EMFONT_SHUTDOWN_TIMEOUT"))
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
	if err := validateHTTPBaseURL("EMFONT_BACKEND_BASE_URL", cfg.HTTP.BackendBaseURL, cfg.Environment); err != nil {
		errs = append(errs, err)
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
	if cfg.HTTP.MaxHeaderBytes < 4096 || cfg.HTTP.MaxHeaderBytes > 1<<20 {
		errs = append(errs, errors.New("EMFONT_HTTP_MAX_HEADER_BYTES must be between 4096 and 1048576"))
	}
	if len(cfg.HTTP.AllowedOrigins) == 0 {
		errs = append(errs, errors.New("EMFONT_CORS_ALLOWED_ORIGINS must contain at least one origin"))
	}
	for _, origin := range cfg.HTTP.AllowedOrigins {
		if origin == "*" {
			if len(cfg.HTTP.AllowedOrigins) != 1 {
				errs = append(errs, errors.New("EMFONT_CORS_ALLOWED_ORIGINS cannot combine * with explicit origins"))
			}
			if hardened {
				errs = append(errs, errors.New("EMFONT_CORS_ALLOWED_ORIGINS must not contain * in production or staging"))
			}
			continue
		}
		parsed, err := url.Parse(origin)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
			parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			errs = append(errs, fmt.Errorf("EMFONT_CORS_ALLOWED_ORIGINS contains invalid origin %q", origin))
		}
	}
	if hardened && !cfg.RateLimit.Enabled {
		errs = append(errs, errors.New("EMFONT_RATE_LIMIT_ENABLED must be true in production or staging"))
	}
	if cfg.RateLimit.Enabled {
		if cfg.RateLimit.RequestsPerSecond <= 0 || math.IsNaN(cfg.RateLimit.RequestsPerSecond) || math.IsInf(cfg.RateLimit.RequestsPerSecond, 0) {
			errs = append(errs, errors.New("EMFONT_RATE_LIMIT_REQUESTS_PER_SECOND must be finite and greater than zero"))
		}
		if cfg.RateLimit.Burst <= 0 {
			errs = append(errs, errors.New("EMFONT_RATE_LIMIT_BURST must be greater than zero"))
		}
		if cfg.RateLimit.GlobalRequestsPerSecond <= 0 || math.IsNaN(cfg.RateLimit.GlobalRequestsPerSecond) || math.IsInf(cfg.RateLimit.GlobalRequestsPerSecond, 0) {
			errs = append(errs, errors.New("EMFONT_GLOBAL_RATE_LIMIT_REQUESTS_PER_SECOND must be finite and greater than zero"))
		}
		if cfg.RateLimit.GlobalBurst <= 0 {
			errs = append(errs, errors.New("EMFONT_GLOBAL_RATE_LIMIT_BURST must be greater than zero"))
		}
		if cfg.RateLimit.MaxClients <= 0 || cfg.RateLimit.MaxClients > maxRateLimitClients {
			errs = append(errs, fmt.Errorf("EMFONT_RATE_LIMIT_MAX_CLIENTS must be between 1 and %d", maxRateLimitClients))
		}
		if cfg.RateLimit.IdleTimeout <= 0 {
			errs = append(errs, errors.New("EMFONT_RATE_LIMIT_IDLE_TIMEOUT must be greater than zero"))
		}
	}
	if cfg.RateLimit.TrustProxyHeaders && len(cfg.RateLimit.TrustedProxyCIDRs) == 0 {
		errs = append(errs, errors.New("EMFONT_TRUSTED_PROXY_CIDRS must contain at least one CIDR when EMFONT_TRUST_PROXY_HEADERS is enabled"))
	}
	for _, prefix := range cfg.RateLimit.TrustedProxyCIDRs {
		if err := validateTrustedProxyPrefix(prefix); err != nil {
			errs = append(errs, err)
		}
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
		endpoint := strings.TrimSpace(cfg.ObjectStorage.Endpoint)
		if endpoint == "" {
			errs = append(errs, errors.New("EMFONT_MINIO_ENDPOINT must not be empty when MinIO is enabled"))
		} else if strings.Contains(endpoint, "://") || strings.ContainsAny(endpoint, "/?#@") ||
			strings.ContainsAny(endpoint, " \t\r\n") {
			errs = append(errs, errors.New("EMFONT_MINIO_ENDPOINT must contain only a host and optional port; use EMFONT_MINIO_SECURE for TLS"))
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
	if hardened && cfg.ObjectStorage.Enabled && strings.TrimSpace(cfg.ObjectStorage.PublicBaseURL) == "" {
		errs = append(errs, errors.New("EMFONT_MINIO_PUBLIC_BASE_URL must be a non-empty HTTPS GET/HEAD gateway base in production or staging"))
	}
	if err := validateHTTPBaseURL("EMFONT_MINIO_PUBLIC_BASE_URL", cfg.ObjectStorage.PublicBaseURL, cfg.Environment); err != nil {
		errs = append(errs, err)
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
	if cfg.FontBuild.MaxPendingBuilds < cfg.FontBuild.StaticBuildConcurrency {
		errs = append(errs, errors.New("EMFONT_FONT_MAX_PENDING_BUILDS must be greater than or equal to EMFONT_FONT_BUILD_CONCURRENCY"))
	}
	if cfg.FontBuild.MaxArtifacts <= 0 {
		errs = append(errs, errors.New("EMFONT_FONT_MAX_ARTIFACTS must be greater than zero"))
	}
	if cfg.FontBuild.MaxAccountedBytes <= 0 {
		errs = append(errs, errors.New("EMFONT_FONT_MAX_ACCOUNTED_BYTES must be greater than zero"))
	}
	if cfg.FontBuild.MaxTerminalFailures <= 0 {
		errs = append(errs, errors.New("EMFONT_FONT_MAX_TERMINAL_FAILURES must be greater than zero"))
	}
	if cfg.FontBuild.MaxSourceBytes <= 0 {
		errs = append(errs, errors.New("EMFONT_FONT_MAX_SOURCE_BYTES must be greater than zero"))
	}
	if cfg.FontBuild.MaxSourceBytes > 512<<20 {
		errs = append(errs, errors.New("EMFONT_FONT_MAX_SOURCE_BYTES must not exceed 536870912"))
	}
	if strings.TrimSpace(cfg.FontBuild.WorkerPath) == "" {
		errs = append(errs, errors.New("EMFONT_FONT_WORKER_PATH must not be empty"))
	}
	if cfg.FontBuild.WorkerMaxOutputBytes <= 0 || cfg.FontBuild.WorkerMaxOutputBytes > 256<<20 {
		errs = append(errs, errors.New("EMFONT_FONT_WORKER_MAX_OUTPUT_BYTES must be between 1 and 268435456"))
	}
	if cfg.FontBuild.MaxAccountedBytes > 0 && cfg.FontBuild.WorkerMaxOutputBytes > cfg.FontBuild.MaxAccountedBytes {
		errs = append(errs, errors.New("EMFONT_FONT_MAX_ACCOUNTED_BYTES must cover EMFONT_FONT_WORKER_MAX_OUTPUT_BYTES"))
	}
	minimumWorkerAddressSpace := cfg.FontBuild.MaxSourceBytes + cfg.FontBuild.WorkerMaxOutputBytes + (256 << 20)
	if minimumWorkerAddressSpace < 2<<30 {
		minimumWorkerAddressSpace = 2 << 30
	}
	if cfg.FontBuild.WorkerAddressSpaceBytes < minimumWorkerAddressSpace {
		errs = append(errs, errors.New("EMFONT_FONT_WORKER_ADDRESS_SPACE_BYTES must cover source, output, and 268435456 bytes of runtime overhead"))
	}
	if cfg.FontBuild.WorkerCPUSeconds <= 0 {
		errs = append(errs, errors.New("EMFONT_FONT_WORKER_CPU_SECONDS must be greater than zero"))
	}
	if cfg.FontBuild.WorkerFileSizeBytes < cfg.FontBuild.WorkerMaxOutputBytes {
		errs = append(errs, errors.New("EMFONT_FONT_WORKER_FILE_SIZE_BYTES must cover EMFONT_FONT_WORKER_MAX_OUTPUT_BYTES"))
	}
	if cfg.FontBuild.WorkerOpenFiles < 16 {
		errs = append(errs, errors.New("EMFONT_FONT_WORKER_OPEN_FILES must be at least 16"))
	}
	if cfg.FontBuild.WorkerStderrBytes <= 0 || cfg.FontBuild.WorkerStderrBytes > 1<<20 {
		errs = append(errs, errors.New("EMFONT_FONT_WORKER_STDERR_BYTES must be between 1 and 1048576"))
	}
	if cfg.FontBuild.ArtifactTouchInterval <= 0 {
		errs = append(errs, errors.New("EMFONT_FONT_ARTIFACT_TOUCH_INTERVAL must be greater than zero"))
	}
	if cfg.Cleanup.ArtifactRetention <= 0 {
		errs = append(errs, errors.New("EMFONT_CLEANUP_ARTIFACT_RETENTION must be greater than zero"))
	}
	if cfg.FontBuild.ArtifactTouchInterval >= cfg.Cleanup.ArtifactRetention {
		errs = append(errs, errors.New("EMFONT_FONT_ARTIFACT_TOUCH_INTERVAL must be less than EMFONT_CLEANUP_ARTIFACT_RETENTION"))
	}
	if cfg.Cleanup.RetirementGrace < cfg.ObjectStorage.PresignExpiry {
		errs = append(errs, errors.New("EMFONT_CLEANUP_RETIREMENT_GRACE must be greater than or equal to EMFONT_MINIO_PRESIGN_EXPIRY"))
	}
	if cfg.Cleanup.OrphanGrace <= cfg.FontBuild.BuildLease || cfg.Cleanup.OrphanGrace <= cfg.FontBuild.BuildTimeout {
		errs = append(errs, errors.New("EMFONT_CLEANUP_ORPHAN_GRACE must be greater than build lease and build timeout"))
	}
	if cfg.Cleanup.Timeout <= 0 {
		errs = append(errs, errors.New("EMFONT_CLEANUP_TIMEOUT must be greater than zero"))
	}
	if cfg.Cleanup.DatabaseBatchSize <= 0 || cfg.Cleanup.MaxDatabaseRows < cfg.Cleanup.DatabaseBatchSize {
		errs = append(errs, errors.New("cleanup database bounds must be positive and max rows must cover one batch"))
	}
	if cfg.Cleanup.ObjectPageSize <= 0 || cfg.Cleanup.ObjectPageSize > 1000 || cfg.Cleanup.MaxObjectPages <= 0 {
		errs = append(errs, errors.New("cleanup object page size must be 1..1000 and max pages must be positive"))
	}
	if cfg.Metrics.Enabled {
		if err := validateMetricsPath(cfg.Metrics.Path); err != nil {
			errs = append(errs, err)
		}
		if len(cfg.Metrics.BearerToken) < 16 {
			errs = append(errs, errors.New("EMFONT_METRICS_BEARER_TOKEN must contain at least 16 characters when metrics are enabled"))
		}
		if cfg.Metrics.AuthRequestsPerSecond <= 0 || math.IsNaN(cfg.Metrics.AuthRequestsPerSecond) || math.IsInf(cfg.Metrics.AuthRequestsPerSecond, 0) {
			errs = append(errs, errors.New("EMFONT_METRICS_AUTH_RATE_LIMIT_REQUESTS_PER_SECOND must be finite and greater than zero when metrics are enabled"))
		}
		if cfg.Metrics.AuthBurst <= 0 {
			errs = append(errs, errors.New("EMFONT_METRICS_AUTH_RATE_LIMIT_BURST must be greater than zero when metrics are enabled"))
		}
	}
	if cfg.Tracing.Enabled {
		if err := validateOTLPEndpoint(cfg.Tracing.OTLPEndpoint, hardened); err != nil {
			errs = append(errs, err)
		}
	}
	if cfg.Tracing.SampleRatio < 0 || cfg.Tracing.SampleRatio > 1 || math.IsNaN(cfg.Tracing.SampleRatio) || math.IsInf(cfg.Tracing.SampleRatio, 0) {
		errs = append(errs, errors.New("EMFONT_TRACING_SAMPLE_RATIO must be finite and between 0 and 1"))
	}

	return errors.Join(errs...)
}

func validateMetricsPath(value string) error {
	trimmed := strings.TrimSpace(value)
	if value != trimmed {
		return errors.New("EMFONT_METRICS_PATH must not contain surrounding whitespace")
	}
	value = trimmed
	if !strings.HasPrefix(value, "/") || value == "/" || strings.ContainsAny(value, "?#\\% \t\r\n") || pathpkg.Clean(value) != value {
		return errors.New("EMFONT_METRICS_PATH must be a canonical absolute path outside reserved API routes")
	}
	for _, prefix := range []string{"/api", "/g", "/css", "/list", "/info"} {
		if value == prefix || strings.HasPrefix(value, prefix+"/") {
			return errors.New("EMFONT_METRICS_PATH must not collide with reserved API routes")
		}
	}
	return nil
}

// ValidateController applies process-role and graceful-drain invariants that
// are intentionally irrelevant to the migration and cleanup binaries.
func (cfg Config) ValidateController() error {
	var errs []error
	if err := cfg.Validate(); err != nil {
		errs = append(errs, err)
	}
	environment := strings.ToLower(strings.TrimSpace(cfg.Environment))
	if !oneOf(environment, "production", "staging") {
		errs = append(errs, errors.New("controller EMFONT_ENV must be production or staging"))
	}
	minimumShutdownTimeout, overflow := addDurations(
		cfg.ShutdownPropagationDelay,
		cfg.HTTP.RequestTimeout,
		shutdownResourceCloseMargin,
	)
	if overflow || cfg.ShutdownTimeout < minimumShutdownTimeout {
		errs = append(errs, fmt.Errorf(
			"EMFONT_SHUTDOWN_TIMEOUT must be at least EMFONT_SHUTDOWN_PROPAGATION_DELAY + EMFONT_HTTP_REQUEST_TIMEOUT + %s resource-close margin",
			shutdownResourceCloseMargin,
		))
	}
	return errors.Join(errs...)
}

func addDurations(values ...time.Duration) (time.Duration, bool) {
	var total time.Duration
	for _, value := range values {
		if value > 0 && total > time.Duration(1<<63-1)-value {
			return 0, true
		}
		if value < 0 && total < time.Duration(-1<<63)-value {
			return 0, true
		}
		total += value
	}
	return total, false
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

func getIPPrefixes(lookup lookupFunc, key string) ([]netip.Prefix, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return nil, nil
	}

	parts := strings.Split(value, ",")
	prefixes := make([]netip.Prefix, 0, len(parts))
	seen := make(map[netip.Prefix]struct{}, len(parts))
	var errs []error
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			errs = append(errs, fmt.Errorf("%s contains an empty CIDR", key))
			continue
		}
		prefix, err := netip.ParsePrefix(part)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s contains invalid CIDR %q: %w", key, part, err))
			continue
		}
		if err := validateTrustedProxyPrefix(prefix); err != nil {
			errs = append(errs, err)
			continue
		}
		if _, exists := seen[prefix]; exists {
			continue
		}
		seen[prefix] = struct{}{}
		prefixes = append(prefixes, prefix)
	}
	return prefixes, errors.Join(errs...)
}

func validateTrustedProxyPrefix(prefix netip.Prefix) error {
	if !prefix.IsValid() {
		return errors.New("EMFONT_TRUSTED_PROXY_CIDRS contains an invalid CIDR")
	}
	if prefix.Addr().Is4In6() {
		return fmt.Errorf("EMFONT_TRUSTED_PROXY_CIDRS contains non-canonical IPv4-mapped CIDR %q", prefix)
	}
	if prefix != prefix.Masked() {
		return fmt.Errorf("EMFONT_TRUSTED_PROXY_CIDRS contains CIDR with host bits set %q", prefix)
	}
	if prefix.Bits() == 0 {
		return fmt.Errorf("EMFONT_TRUSTED_PROXY_CIDRS contains overly broad CIDR %q", prefix)
	}
	return nil
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, ok := seen[part]; ok {
			continue
		}
		seen[part] = struct{}{}
		result = append(result, part)
	}
	return result
}

func isAPIVersion(value string) bool {
	if len(value) < 2 || value[0] != 'v' {
		return false
	}
	for _, character := range value[1:] {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func validateHTTPBaseURL(key, value, environment string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}

	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.Hostname() == "" ||
		!oneOf(strings.ToLower(parsed.Scheme), "http", "https") {
		return fmt.Errorf("%s must be an absolute HTTP(S) URL", key)
	}
	if parsed.User != nil {
		return fmt.Errorf("%s must not include user info", key)
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return fmt.Errorf("%s must not include a query string", key)
	}
	if parsed.Fragment != "" || strings.Contains(value, "#") {
		return fmt.Errorf("%s must not include a fragment", key)
	}
	if IsHardenedEnvironment(environment) && !strings.EqualFold(parsed.Scheme, "https") {
		return fmt.Errorf("%s must use HTTPS in production or staging", key)
	}
	return nil
}

func validateOTLPEndpoint(value string, requireHTTPS bool) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("EMFONT_OTEL_EXPORTER_OTLP_ENDPOINT must not be empty when tracing is enabled")
	}

	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.Hostname() == "" ||
		!oneOf(strings.ToLower(parsed.Scheme), "http", "https") {
		return errors.New("EMFONT_OTEL_EXPORTER_OTLP_ENDPOINT must be an absolute HTTP(S) URL")
	}
	if parsed.User != nil {
		return errors.New("EMFONT_OTEL_EXPORTER_OTLP_ENDPOINT must not include user info")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return errors.New("EMFONT_OTEL_EXPORTER_OTLP_ENDPOINT must not include a query string")
	}
	if parsed.Fragment != "" || strings.Contains(value, "#") {
		return errors.New("EMFONT_OTEL_EXPORTER_OTLP_ENDPOINT must not include a fragment")
	}
	port := parsed.Port()
	if strings.HasSuffix(parsed.Host, ":") || port != "" {
		if _, err := strconv.ParseUint(port, 10, 16); err != nil {
			return errors.New("EMFONT_OTEL_EXPORTER_OTLP_ENDPOINT must include a valid port")
		}
	}
	if requireHTTPS && !strings.EqualFold(parsed.Scheme, "https") {
		return errors.New("EMFONT_OTEL_EXPORTER_OTLP_ENDPOINT must use HTTPS in production or staging")
	}
	return nil
}

// IsHardenedEnvironment reports whether production transport and exposure
// requirements apply to the named deployment environment.
func IsHardenedEnvironment(environment string) bool {
	switch strings.ToLower(strings.TrimSpace(environment)) {
	case "production", "staging":
		return true
	default:
		return false
	}
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
