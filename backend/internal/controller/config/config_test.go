package config

import (
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestLoadWithLookupDefaults(t *testing.T) {
	cfg, err := loadWithDevelopmentLookup(func(string) (string, bool) {
		return "", false
	})
	if err != nil {
		t.Fatalf("LoadWithLookup returned error: %v", err)
	}

	if cfg.ServiceName != defaultServiceName {
		t.Fatalf("ServiceName = %q, want %q", cfg.ServiceName, defaultServiceName)
	}
	if cfg.Environment != "development" {
		t.Fatalf("Environment = %q, want development", cfg.Environment)
	}
	if cfg.ShutdownPropagationDelay != defaultShutdownPropagationDelay {
		t.Fatalf("ShutdownPropagationDelay = %s, want %s", cfg.ShutdownPropagationDelay, defaultShutdownPropagationDelay)
	}
	if cfg.APIVersion != defaultAPIVersion {
		t.Fatalf("APIVersion = %q, want %q", cfg.APIVersion, defaultAPIVersion)
	}
	if cfg.Database.URL != defaultDatabaseURL {
		t.Fatalf("Database.URL = %q, want default URL", cfg.Database.URL)
	}
	if !cfg.RateLimit.Enabled || cfg.RateLimit.RequestsPerSecond != defaultRateLimitRPS ||
		cfg.RateLimit.GlobalRequestsPerSecond != defaultGlobalRateLimitRPS ||
		cfg.RateLimit.GlobalBurst != defaultGlobalRateBurst {
		t.Fatalf("RateLimit defaults = %+v", cfg.RateLimit)
	}
	if cfg.RateLimit.GlobalRequestsPerSecond <= cfg.RateLimit.RequestsPerSecond || cfg.RateLimit.GlobalBurst <= cfg.RateLimit.Burst {
		t.Fatalf("global rate limit defaults must exceed per-client defaults: %+v", cfg.RateLimit)
	}
	if cfg.RateLimit.TrustProxyHeaders || len(cfg.RateLimit.TrustedProxyCIDRs) != 0 {
		t.Fatalf("trusted proxy defaults = %+v, want disabled with no CIDRs", cfg.RateLimit)
	}
	if cfg.Metrics.Enabled {
		t.Fatalf("Metrics.Enabled = true, want false without an explicit protected configuration")
	}
	if cfg.Metrics.AuthRequestsPerSecond != defaultMetricsAuthRateRPS || cfg.Metrics.AuthBurst != defaultMetricsAuthBurst {
		t.Fatalf("Metrics auth rate-limit defaults = %+v", cfg.Metrics)
	}
	if cfg.FontBuild.MaxPendingBuilds != defaultMaxPendingBuilds {
		t.Fatalf("FontBuild.MaxPendingBuilds = %d, want %d", cfg.FontBuild.MaxPendingBuilds, defaultMaxPendingBuilds)
	}
	if cfg.FontBuild.MaxArtifacts != defaultMaxArtifacts ||
		cfg.FontBuild.MaxAccountedBytes != defaultMaxAccountedBytes ||
		cfg.FontBuild.MaxTerminalFailures != defaultMaxTerminalFailures {
		t.Fatalf("FontBuild artifact limits = %+v", cfg.FontBuild)
	}
	if cfg.FontBuild.WorkerPath != defaultFontWorkerPath ||
		cfg.FontBuild.WorkerMaxOutputBytes != defaultWorkerMaxOutput ||
		cfg.FontBuild.WorkerAddressSpaceBytes != defaultWorkerAddressSpace ||
		cfg.FontBuild.WorkerCPUSeconds != defaultWorkerCPUSeconds ||
		cfg.FontBuild.WorkerFileSizeBytes != defaultWorkerFileSize ||
		cfg.FontBuild.WorkerOpenFiles != defaultWorkerOpenFiles ||
		cfg.FontBuild.WorkerStderrBytes != defaultWorkerStderrBytes {
		t.Fatalf("FontBuild worker defaults = %+v", cfg.FontBuild)
	}
	if len(cfg.HTTP.AllowedOrigins) != 1 || cfg.HTTP.AllowedOrigins[0] != "*" {
		t.Fatalf("HTTP.AllowedOrigins = %#v, want wildcard", cfg.HTTP.AllowedOrigins)
	}
	if cfg.FontBuild.ArtifactTouchInterval != defaultArtifactTouch || cfg.Cleanup.ArtifactRetention != defaultArtifactRetention {
		t.Fatalf("artifact lifecycle defaults = touch %s cleanup %+v", cfg.FontBuild.ArtifactTouchInterval, cfg.Cleanup)
	}
}

func TestLoadWithLookupRequiresAllowlistedEnvironment(t *testing.T) {
	_, err := LoadWithLookup(func(string) (string, bool) { return "", false })
	if err == nil || !strings.Contains(err.Error(), "EMFONT_ENV") {
		t.Fatalf("missing environment error = %v", err)
	}

	_, err = LoadWithLookup(func(key string) (string, bool) {
		if key == "EMFONT_ENV" {
			return "prodution", true
		}
		return "", false
	})
	if err == nil || !strings.Contains(err.Error(), "EMFONT_ENV") {
		t.Fatalf("unknown environment error = %v", err)
	}

	for _, environment := range []string{"development", "local", "test", "staging", "production", "migration", "cleanup"} {
		t.Run(environment, func(t *testing.T) {
			cfg, err := LoadWithLookup(func(key string) (string, bool) {
				switch key {
				case "EMFONT_ENV":
					return environment, true
				case "EMFONT_CORS_ALLOWED_ORIGINS":
					if IsHardenedEnvironment(environment) {
						return "https://fonts.example", true
					}
				}
				return "", false
			})
			if err != nil {
				t.Fatalf("LoadWithLookup: %v", err)
			}
			if cfg.Environment != environment {
				t.Fatalf("Environment = %q, want %q", cfg.Environment, environment)
			}
		})
	}
}

func TestLoadWithLookupHardensStagingLikeProduction(t *testing.T) {
	_, err := LoadWithLookup(func(key string) (string, bool) {
		values := map[string]string{
			"EMFONT_ENV":                "staging",
			"EMFONT_RATE_LIMIT_ENABLED": "false",
		}
		value, ok := values[key]
		return value, ok
	})
	if err == nil || !strings.Contains(err.Error(), "EMFONT_CORS_ALLOWED_ORIGINS") ||
		!strings.Contains(err.Error(), "EMFONT_RATE_LIMIT_ENABLED") {
		t.Fatalf("staging hardening error = %v", err)
	}
}

func TestLoadWithLookupValidatesCORSOrigins(t *testing.T) {
	cfg, err := loadWithDevelopmentLookup(func(key string) (string, bool) {
		if key == "EMFONT_CORS_ALLOWED_ORIGINS" {
			return "https://fonts.example, https://admin.example,https://fonts.example", true
		}
		return "", false
	})
	if err != nil {
		t.Fatalf("valid CORS origins: %v", err)
	}
	if len(cfg.HTTP.AllowedOrigins) != 2 {
		t.Fatalf("AllowedOrigins = %#v, want two unique origins", cfg.HTTP.AllowedOrigins)
	}

	_, err = loadWithDevelopmentLookup(func(key string) (string, bool) {
		if key == "EMFONT_CORS_ALLOWED_ORIGINS" {
			return "*,https://fonts.example", true
		}
		return "", false
	})
	if err == nil || !strings.Contains(err.Error(), "cannot combine") {
		t.Fatalf("wildcard CORS error = %v", err)
	}
}

func TestLoadWithLookupRejectsWildcardCORSInProduction(t *testing.T) {
	_, err := loadWithDevelopmentLookup(func(key string) (string, bool) {
		if key == "EMFONT_ENV" {
			return "production", true
		}
		return "", false
	})
	if err == nil || !strings.Contains(err.Error(), "EMFONT_CORS_ALLOWED_ORIGINS") {
		t.Fatalf("production wildcard CORS error = %v", err)
	}

	cfg, err := loadWithDevelopmentLookup(func(key string) (string, bool) {
		values := map[string]string{
			"EMFONT_ENV":                  "production",
			"EMFONT_CORS_ALLOWED_ORIGINS": "https://fonts.example",
		}
		value, ok := values[key]
		return value, ok
	})
	if err != nil {
		t.Fatalf("production explicit CORS origin: %v", err)
	}
	if len(cfg.HTTP.AllowedOrigins) != 1 || cfg.HTTP.AllowedOrigins[0] != "https://fonts.example" {
		t.Fatalf("AllowedOrigins = %#v", cfg.HTTP.AllowedOrigins)
	}
}

func TestLoadWithLookupRejectsDisabledRateLimitInProduction(t *testing.T) {
	_, err := loadWithDevelopmentLookup(func(key string) (string, bool) {
		values := map[string]string{
			"EMFONT_ENV":                  "production",
			"EMFONT_CORS_ALLOWED_ORIGINS": "https://fonts.example",
			"EMFONT_RATE_LIMIT_ENABLED":   "false",
		}
		value, ok := values[key]
		return value, ok
	})
	if err == nil || !strings.Contains(err.Error(), "EMFONT_RATE_LIMIT_ENABLED must be true in production") {
		t.Fatalf("production rate limit error = %v", err)
	}
}

func TestLoadWithLookupRequiresTrustedProxyCIDRs(t *testing.T) {
	_, err := loadWithDevelopmentLookup(func(key string) (string, bool) {
		if key == "EMFONT_TRUST_PROXY_HEADERS" {
			return "true", true
		}
		return "", false
	})
	if err == nil || !strings.Contains(err.Error(), "EMFONT_TRUSTED_PROXY_CIDRS") {
		t.Fatalf("missing trusted proxy CIDRs error = %v", err)
	}

	cfg, err := loadWithDevelopmentLookup(func(key string) (string, bool) {
		values := map[string]string{
			"EMFONT_TRUST_PROXY_HEADERS": "true",
			"EMFONT_TRUSTED_PROXY_CIDRS": "10.0.0.0/8, 2001:db8:1234::/48,10.0.0.0/8",
		}
		value, ok := values[key]
		return value, ok
	})
	if err != nil {
		t.Fatalf("valid trusted proxy CIDRs: %v", err)
	}
	if len(cfg.RateLimit.TrustedProxyCIDRs) != 2 ||
		cfg.RateLimit.TrustedProxyCIDRs[0].String() != "10.0.0.0/8" ||
		cfg.RateLimit.TrustedProxyCIDRs[1].String() != "2001:db8:1234::/48" {
		t.Fatalf("TrustedProxyCIDRs = %#v", cfg.RateLimit.TrustedProxyCIDRs)
	}
}

func TestLoadWithLookupRejectsMalformedOrBroadTrustedProxyCIDRs(t *testing.T) {
	for _, value := range []string{
		"not-a-cidr",
		"10.0.0.1/8",
		"0.0.0.0/0",
		"::/0",
		"::ffff:192.0.2.0/120",
		"10.0.0.0/8,",
	} {
		t.Run(value, func(t *testing.T) {
			_, err := loadWithDevelopmentLookup(func(key string) (string, bool) {
				if key == "EMFONT_TRUSTED_PROXY_CIDRS" {
					return value, true
				}
				return "", false
			})
			if err == nil || !strings.Contains(err.Error(), "EMFONT_TRUSTED_PROXY_CIDRS") {
				t.Fatalf("trusted proxy CIDR error = %v", err)
			}
		})
	}
}

func TestConfigValidateRejectsUnsafeTrustedProxyPrefixes(t *testing.T) {
	cfg, err := loadWithDevelopmentLookup(func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("load defaults: %v", err)
	}
	cfg.RateLimit.TrustProxyHeaders = true
	cfg.RateLimit.TrustedProxyCIDRs = []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "overly broad") {
		t.Fatalf("Validate broad trusted proxy prefix error = %v", err)
	}
}

func TestLoadWithLookupRejectsUnsafeCapacityLimits(t *testing.T) {
	_, err := loadWithDevelopmentLookup(func(key string) (string, bool) {
		switch key {
		case "EMFONT_RATE_LIMIT_BURST":
			return "0", true
		case "EMFONT_FONT_BUILD_CONCURRENCY":
			return "4", true
		case "EMFONT_FONT_MAX_PENDING_BUILDS":
			return "2", true
		default:
			return "", false
		}
	})
	if err == nil {
		t.Fatal("LoadWithLookup returned nil error")
	}
	if !strings.Contains(err.Error(), "EMFONT_RATE_LIMIT_BURST") || !strings.Contains(err.Error(), "EMFONT_FONT_MAX_PENDING_BUILDS") {
		t.Fatalf("error = %v, want rate and build capacity errors", err)
	}
}

func TestLoadWithLookupRejectsExcessiveRateLimitClients(t *testing.T) {
	_, err := loadWithDevelopmentLookup(func(key string) (string, bool) {
		if key == "EMFONT_RATE_LIMIT_MAX_CLIENTS" {
			return "100001", true
		}
		return "", false
	})
	if err == nil || !strings.Contains(err.Error(), "EMFONT_RATE_LIMIT_MAX_CLIENTS") {
		t.Fatalf("error = %v, want max clients limit error", err)
	}
}

func TestLoadWithLookupLoadsAndValidatesGlobalRateLimit(t *testing.T) {
	cfg, err := loadWithDevelopmentLookup(func(key string) (string, bool) {
		values := map[string]string{
			"EMFONT_GLOBAL_RATE_LIMIT_REQUESTS_PER_SECOND": "250",
			"EMFONT_GLOBAL_RATE_LIMIT_BURST":               "500",
		}
		value, ok := values[key]
		return value, ok
	})
	if err != nil {
		t.Fatalf("LoadWithLookup: %v", err)
	}
	if cfg.RateLimit.GlobalRequestsPerSecond != 250 || cfg.RateLimit.GlobalBurst != 500 {
		t.Fatalf("global rate limit = %+v", cfg.RateLimit)
	}

	_, err = loadWithDevelopmentLookup(func(key string) (string, bool) {
		if key == "EMFONT_GLOBAL_RATE_LIMIT_BURST" {
			return "0", true
		}
		return "", false
	})
	if err == nil || !strings.Contains(err.Error(), "EMFONT_GLOBAL_RATE_LIMIT_BURST") {
		t.Fatalf("global rate limit validation error = %v", err)
	}
}

func TestLoadWithLookupLoadsFontWorkerLimits(t *testing.T) {
	cfg, err := loadWithDevelopmentLookup(func(key string) (string, bool) {
		values := map[string]string{
			"EMFONT_FONT_WORKER_PATH":                "/opt/emfont/fontworker",
			"EMFONT_FONT_WORKER_MAX_OUTPUT_BYTES":    "67108864",
			"EMFONT_FONT_WORKER_ADDRESS_SPACE_BYTES": "2147483648",
			"EMFONT_FONT_WORKER_CPU_SECONDS":         "30",
			"EMFONT_FONT_WORKER_FILE_SIZE_BYTES":     "67108864",
			"EMFONT_FONT_WORKER_OPEN_FILES":          "24",
			"EMFONT_FONT_WORKER_STDERR_BYTES":        "8192",
			"EMFONT_FONT_MAX_ARTIFACTS":              "2500",
			"EMFONT_FONT_MAX_ACCOUNTED_BYTES":        "10737418240",
			"EMFONT_FONT_MAX_TERMINAL_FAILURES":      "333",
		}
		value, ok := values[key]
		return value, ok
	})
	if err != nil {
		t.Fatalf("LoadWithLookup: %v", err)
	}
	if cfg.FontBuild.WorkerPath != "/opt/emfont/fontworker" ||
		cfg.FontBuild.WorkerMaxOutputBytes != 67108864 ||
		cfg.FontBuild.WorkerAddressSpaceBytes != 2147483648 ||
		cfg.FontBuild.WorkerCPUSeconds != 30 || cfg.FontBuild.WorkerOpenFiles != 24 ||
		cfg.FontBuild.WorkerStderrBytes != 8192 || cfg.FontBuild.MaxArtifacts != 2500 ||
		cfg.FontBuild.MaxAccountedBytes != 10737418240 || cfg.FontBuild.MaxTerminalFailures != 333 {
		t.Fatalf("FontBuild worker config = %+v", cfg.FontBuild)
	}
}

func TestLoadWithLookupRejectsInvalidArtifactLimits(t *testing.T) {
	_, err := loadWithDevelopmentLookup(func(key string) (string, bool) {
		switch key {
		case "EMFONT_FONT_MAX_ARTIFACTS":
			return "0", true
		case "EMFONT_FONT_MAX_ACCOUNTED_BYTES":
			return "67108864", true
		case "EMFONT_FONT_MAX_TERMINAL_FAILURES":
			return "0", true
		default:
			return "", false
		}
	})
	if err == nil || !strings.Contains(err.Error(), "EMFONT_FONT_MAX_ARTIFACTS") ||
		!strings.Contains(err.Error(), "EMFONT_FONT_MAX_ACCOUNTED_BYTES") ||
		!strings.Contains(err.Error(), "EMFONT_FONT_MAX_TERMINAL_FAILURES") {
		t.Fatalf("artifact limit validation error = %v", err)
	}
}

func TestLoadWithLookupRejectsUnsafeFontWorkerLimits(t *testing.T) {
	_, err := loadWithDevelopmentLookup(func(key string) (string, bool) {
		switch key {
		case "EMFONT_FONT_WORKER_PATH":
			return "", true
		case "EMFONT_FONT_WORKER_MAX_OUTPUT_BYTES":
			return "536870912", true
		case "EMFONT_FONT_WORKER_ADDRESS_SPACE_BYTES":
			return "1", true
		case "EMFONT_FONT_WORKER_CPU_SECONDS":
			return "0", true
		case "EMFONT_FONT_WORKER_FILE_SIZE_BYTES":
			return "1", true
		case "EMFONT_FONT_WORKER_OPEN_FILES":
			return "2", true
		case "EMFONT_FONT_WORKER_STDERR_BYTES":
			return "2097152", true
		default:
			return "", false
		}
	})
	if err == nil {
		t.Fatal("LoadWithLookup returned nil error")
	}
	for _, key := range []string{
		"EMFONT_FONT_WORKER_PATH", "EMFONT_FONT_WORKER_MAX_OUTPUT_BYTES",
		"EMFONT_FONT_WORKER_ADDRESS_SPACE_BYTES", "EMFONT_FONT_WORKER_CPU_SECONDS",
		"EMFONT_FONT_WORKER_FILE_SIZE_BYTES", "EMFONT_FONT_WORKER_OPEN_FILES",
		"EMFONT_FONT_WORKER_STDERR_BYTES",
	} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error %q does not mention %s", err, key)
		}
	}
}

func TestLoadWithLookupRejectsInvalidValues(t *testing.T) {
	_, err := loadWithDevelopmentLookup(func(key string) (string, bool) {
		switch key {
		case "EMFONT_LOG_LEVEL":
			return "verbose", true
		case "EMFONT_HTTP_REQUEST_TIMEOUT":
			return "nope", true
		default:
			return "", false
		}
	})
	if err == nil {
		t.Fatal("LoadWithLookup returned nil error")
	}
	if !strings.Contains(err.Error(), "EMFONT_LOG_LEVEL") {
		t.Fatalf("error %q does not mention invalid log level", err)
	}
	if !strings.Contains(err.Error(), "EMFONT_HTTP_REQUEST_TIMEOUT") {
		t.Fatalf("error %q does not mention invalid request timeout", err)
	}
}

func TestLoadWithLookupRejectsIncompleteMinIOConfig(t *testing.T) {
	_, err := loadWithDevelopmentLookup(func(key string) (string, bool) {
		if key == "EMFONT_MINIO_ENABLED" {
			return "true", true
		}
		return "", false
	})
	if err == nil || !strings.Contains(err.Error(), "EMFONT_MINIO_ENDPOINT") {
		t.Fatalf("error = %v, want incomplete MinIO config error", err)
	}
}

func TestLoadWithLookupRequiresProtectedMetricsInEveryEnvironment(t *testing.T) {
	for _, test := range []struct {
		name      string
		values    map[string]string
		wantError string
	}{
		{
			name: "enabled without token in development",
			values: map[string]string{
				"EMFONT_ENV":             "development",
				"EMFONT_METRICS_ENABLED": "true",
			},
			wantError: "EMFONT_METRICS_BEARER_TOKEN",
		},
		{
			name: "enabled with short token and misspelled environment",
			values: map[string]string{
				"EMFONT_ENV":                  "prodution",
				"EMFONT_METRICS_ENABLED":      "true",
				"EMFONT_METRICS_BEARER_TOKEN": "too-short",
			},
			wantError: "EMFONT_METRICS_BEARER_TOKEN",
		},
		{
			name: "enabled with sufficient token in misspelled environment",
			values: map[string]string{
				"EMFONT_ENV":                  "prodution",
				"EMFONT_METRICS_ENABLED":      "true",
				"EMFONT_METRICS_BEARER_TOKEN": "test-token-value!",
			},
			wantError: "EMFONT_ENV",
		},
		{
			name: "disabled in production without token",
			values: map[string]string{
				"EMFONT_ENV":                  "production",
				"EMFONT_METRICS_ENABLED":      "false",
				"EMFONT_CORS_ALLOWED_ORIGINS": "https://fonts.example",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := loadWithDevelopmentLookup(func(key string) (string, bool) {
				value, ok := test.values[key]
				return value, ok
			})
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("error = %v, want validation error containing %s", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadWithLookup returned error: %v", err)
			}
		})
	}
}

func TestLoadWithLookupValidatesMetricsAuthRateLimit(t *testing.T) {
	base := map[string]string{
		"EMFONT_METRICS_ENABLED":      "true",
		"EMFONT_METRICS_BEARER_TOKEN": "test-token-value!",
	}
	for _, test := range []struct {
		key   string
		value string
	}{
		{key: "EMFONT_METRICS_AUTH_RATE_LIMIT_REQUESTS_PER_SECOND", value: "NaN"},
		{key: "EMFONT_METRICS_AUTH_RATE_LIMIT_REQUESTS_PER_SECOND", value: "+Inf"},
		{key: "EMFONT_METRICS_AUTH_RATE_LIMIT_REQUESTS_PER_SECOND", value: "0"},
		{key: "EMFONT_METRICS_AUTH_RATE_LIMIT_BURST", value: "0"},
	} {
		t.Run(test.key+"="+test.value, func(t *testing.T) {
			_, err := loadWithDevelopmentLookup(func(key string) (string, bool) {
				if key == test.key {
					return test.value, true
				}
				value, ok := base[key]
				return value, ok
			})
			if err == nil || !strings.Contains(err.Error(), test.key) {
				t.Fatalf("metrics auth limit error = %v", err)
			}
		})
	}
}

func TestLoadWithLookupRejectsMetricsPathCollisionsAndNonCanonicalPaths(t *testing.T) {
	if err := validateMetricsPath(" /metrics"); err == nil {
		t.Fatal("metrics path with surrounding whitespace passed direct validation")
	}
	for _, metricsPath := range []string{
		"/", "/api", "/api/v1/readyz", "/g/Font", "/css/Font", "/list", "/info/Font",
		"/internal//metrics", "/internal/../metrics", "/metrics?format=openmetrics", "/metrics#fragment",
		"/internal/%2e%2e/metrics", "/internal\\metrics", "/internal metrics",
	} {
		t.Run(metricsPath, func(t *testing.T) {
			_, err := loadWithDevelopmentLookup(func(key string) (string, bool) {
				switch key {
				case "EMFONT_METRICS_ENABLED":
					return "true", true
				case "EMFONT_METRICS_BEARER_TOKEN":
					return "test-token-value!", true
				case "EMFONT_METRICS_PATH":
					return metricsPath, true
				default:
					return "", false
				}
			})
			if err == nil || !strings.Contains(err.Error(), "EMFONT_METRICS_PATH") {
				t.Fatalf("metrics path validation error = %v", err)
			}
		})
	}

	cfg, err := loadWithDevelopmentLookup(func(key string) (string, bool) {
		switch key {
		case "EMFONT_METRICS_ENABLED":
			return "true", true
		case "EMFONT_METRICS_BEARER_TOKEN":
			return "test-token-value!", true
		case "EMFONT_METRICS_PATH":
			return "/internal/metrics", true
		default:
			return "", false
		}
	})
	if err != nil || cfg.Metrics.Path != "/internal/metrics" {
		t.Fatalf("valid metrics path config = %+v, error = %v", cfg.Metrics, err)
	}
}

func TestLoadWithLookupRequiresRouteSafeAPIVersion(t *testing.T) {
	for _, version := range []string{"v", "V1", "v1/metrics", "v1?format=json", "v01-beta", "v1.0"} {
		t.Run(version, func(t *testing.T) {
			_, err := loadWithDevelopmentLookup(func(key string) (string, bool) {
				if key == "EMFONT_API_VERSION" {
					return version, true
				}
				return "", false
			})
			if err == nil || !strings.Contains(err.Error(), "EMFONT_API_VERSION") {
				t.Fatalf("error = %v, want API version validation error", err)
			}
		})
	}

	cfg, err := loadWithDevelopmentLookup(func(key string) (string, bool) {
		if key == "EMFONT_API_VERSION" {
			return "v2026", true
		}
		return "", false
	})
	if err != nil || cfg.APIVersion != "v2026" {
		t.Fatalf("API version config = %+v, error = %v", cfg, err)
	}
}

func TestLoadWithLookupValidatesPublicBaseURLs(t *testing.T) {
	for _, test := range []struct {
		name    string
		key     string
		value   string
		env     string
		wantErr bool
	}{
		{name: "backend HTTP path outside production", key: "EMFONT_BACKEND_BASE_URL", value: "http://api.example.test/emfont"},
		{name: "MinIO HTTPS path in production", key: "EMFONT_MINIO_PUBLIC_BASE_URL", value: "https://cdn.example.test/fonts", env: "production"},
		{name: "backend missing scheme", key: "EMFONT_BACKEND_BASE_URL", value: "api.example.test", wantErr: true},
		{name: "MinIO user info", key: "EMFONT_MINIO_PUBLIC_BASE_URL", value: "https://user:secret@cdn.example.test/fonts", wantErr: true},
		{name: "backend query", key: "EMFONT_BACKEND_BASE_URL", value: "https://api.example.test/?debug=true", wantErr: true},
		{name: "MinIO empty query", key: "EMFONT_MINIO_PUBLIC_BASE_URL", value: "https://cdn.example.test/fonts?", wantErr: true},
		{name: "backend fragment", key: "EMFONT_BACKEND_BASE_URL", value: "https://api.example.test/#section", wantErr: true},
		{name: "MinIO empty fragment", key: "EMFONT_MINIO_PUBLIC_BASE_URL", value: "https://cdn.example.test/fonts#", wantErr: true},
		{name: "backend HTTP in production", key: "EMFONT_BACKEND_BASE_URL", value: "http://api.example.test", env: "production", wantErr: true},
		{name: "MinIO HTTP in production", key: "EMFONT_MINIO_PUBLIC_BASE_URL", value: "http://cdn.example.test/fonts", env: "production", wantErr: true},
		{name: "MinIO HTTP in whitespace production", key: "EMFONT_MINIO_PUBLIC_BASE_URL", value: "http://cdn.example.test/fonts", env: " production ", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := loadWithDevelopmentLookup(func(key string) (string, bool) {
				if key == test.key {
					return test.value, true
				}
				if key == "EMFONT_ENV" && test.env != "" {
					return test.env, true
				}
				if key == "EMFONT_CORS_ALLOWED_ORIGINS" && strings.EqualFold(strings.TrimSpace(test.env), "production") {
					return "https://fonts.example", true
				}
				return "", false
			})
			if test.wantErr {
				if err == nil || !strings.Contains(err.Error(), test.key) {
					t.Fatalf("error = %v, want %s validation error", err, test.key)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadWithLookup returned error: %v", err)
			}
		})
	}
}

func TestLoadWithLookupRequiresProductionObjectGateway(t *testing.T) {
	base := map[string]string{
		"EMFONT_ENV":                         "production",
		"EMFONT_CORS_ALLOWED_ORIGINS":        "https://fonts.example",
		"EMFONT_MINIO_ENABLED":               "true",
		"EMFONT_MINIO_ENDPOINT":              "minio:9000",
		"EMFONT_MINIO_ACCESS_KEY":            "app-access-key",
		"EMFONT_MINIO_SECRET_KEY":            "app-secret-key",
		"EMFONT_MINIO_BUCKET":                "emfont",
		"EMFONT_MINIO_PUBLIC_BASE_URL":       "",
		"EMFONT_RATE_LIMIT_ENABLED":          "true",
		"EMFONT_TRUST_PROXY_HEADERS":         "false",
		"EMFONT_TRUSTED_PROXY_CIDRS":         "",
		"EMFONT_METRICS_ENABLED":             "false",
		"EMFONT_MINIO_SESSION_TOKEN":         "",
		"EMFONT_OTEL_EXPORTER_OTLP_ENDPOINT": "",
	}

	load := func(values map[string]string) (Config, error) {
		return loadWithDevelopmentLookup(func(key string) (string, bool) {
			value, ok := values[key]
			return value, ok
		})
	}

	_, err := load(base)
	if err == nil || !strings.Contains(err.Error(), "EMFONT_MINIO_PUBLIC_BASE_URL must be a non-empty HTTPS GET/HEAD gateway base") {
		t.Fatalf("empty production object gateway error = %v", err)
	}

	httpValues := make(map[string]string, len(base))
	for key, value := range base {
		httpValues[key] = value
	}
	httpValues["EMFONT_MINIO_PUBLIC_BASE_URL"] = "http://gateway.example/fonts"
	_, err = load(httpValues)
	if err == nil || !strings.Contains(err.Error(), "EMFONT_MINIO_PUBLIC_BASE_URL must use HTTPS in production") {
		t.Fatalf("HTTP production object gateway error = %v", err)
	}

	httpsValues := make(map[string]string, len(base))
	for key, value := range base {
		httpsValues[key] = value
	}
	httpsValues["EMFONT_MINIO_PUBLIC_BASE_URL"] = "https://gateway.example/fonts"
	cfg, err := load(httpsValues)
	if err != nil {
		t.Fatalf("HTTPS production object gateway: %v", err)
	}
	if cfg.ObjectStorage.PublicBaseURL != "https://gateway.example/fonts" {
		t.Fatalf("PublicBaseURL = %q", cfg.ObjectStorage.PublicBaseURL)
	}

	URLEndpointValues := make(map[string]string, len(httpsValues))
	for key, value := range httpsValues {
		URLEndpointValues[key] = value
	}
	URLEndpointValues["EMFONT_MINIO_ENDPOINT"] = "https://minio:9000"
	_, err = load(URLEndpointValues)
	if err == nil || !strings.Contains(err.Error(), "EMFONT_MINIO_ENDPOINT must contain only a host and optional port") {
		t.Fatalf("URL-form MinIO endpoint error = %v", err)
	}

	testValues := make(map[string]string, len(base))
	for key, value := range base {
		testValues[key] = value
	}
	testValues["EMFONT_ENV"] = "test"
	if _, err := load(testValues); err != nil {
		t.Fatalf("explicit test environment with internal presigning: %v", err)
	}
}

func TestLoadWithLookupRejectsNonFiniteTracingSampleRatio(t *testing.T) {
	for _, value := range []string{"NaN", "+Inf", "-Inf"} {
		t.Run(value, func(t *testing.T) {
			_, err := loadWithDevelopmentLookup(func(key string) (string, bool) {
				if key == "EMFONT_TRACING_SAMPLE_RATIO" {
					return value, true
				}
				return "", false
			})
			if err == nil || !strings.Contains(err.Error(), "EMFONT_TRACING_SAMPLE_RATIO") {
				t.Fatalf("sample ratio error = %v", err)
			}
		})
	}
}

func TestLoadWithLookupRequiresHTTPSOTLPInHardenedEnvironments(t *testing.T) {
	for _, environment := range []string{"production", "staging"} {
		t.Run(environment, func(t *testing.T) {
			values := map[string]string{
				"EMFONT_ENV":                         environment,
				"EMFONT_CORS_ALLOWED_ORIGINS":        "https://fonts.example",
				"EMFONT_TRACING_ENABLED":             "true",
				"EMFONT_OTEL_EXPORTER_OTLP_ENDPOINT": "http://collector.example:4318",
			}
			_, err := LoadWithLookup(func(key string) (string, bool) {
				value, ok := values[key]
				return value, ok
			})
			if err == nil || !strings.Contains(err.Error(), "must use HTTPS") {
				t.Fatalf("HTTP OTLP endpoint error = %v", err)
			}

			values["EMFONT_OTEL_EXPORTER_OTLP_ENDPOINT"] = "https://collector.example:4318"
			if _, err := LoadWithLookup(func(key string) (string, bool) {
				value, ok := values[key]
				return value, ok
			}); err != nil {
				t.Fatalf("HTTPS OTLP endpoint: %v", err)
			}
		})
	}

	_, err := loadWithDevelopmentLookup(func(key string) (string, bool) {
		values := map[string]string{
			"EMFONT_TRACING_ENABLED":             "true",
			"EMFONT_OTEL_EXPORTER_OTLP_ENDPOINT": "http://collector.example:4318",
		}
		value, ok := values[key]
		return value, ok
	})
	if err != nil {
		t.Fatalf("development HTTP OTLP endpoint: %v", err)
	}
}

func TestLoadWithLookupValidatesShutdownPropagationDelay(t *testing.T) {
	for _, value := range []string{"-1s", "30s", "31s"} {
		t.Run(value, func(t *testing.T) {
			_, err := loadWithDevelopmentLookup(func(key string) (string, bool) {
				if key == "EMFONT_SHUTDOWN_PROPAGATION_DELAY" {
					return value, true
				}
				return "", false
			})
			if err == nil || !strings.Contains(err.Error(), "EMFONT_SHUTDOWN_PROPAGATION_DELAY") {
				t.Fatalf("shutdown propagation error = %v", err)
			}
		})
	}

	cfg, err := loadWithDevelopmentLookup(func(key string) (string, bool) {
		if key == "EMFONT_SHUTDOWN_PROPAGATION_DELAY" {
			return "0s", true
		}
		return "", false
	})
	if err != nil || cfg.ShutdownPropagationDelay != 0 {
		t.Fatalf("zero shutdown propagation delay = %s, error = %v", cfg.ShutdownPropagationDelay, err)
	}
}

func TestValidateControllerRequiresHardenedRoleAndCompleteShutdownBudget(t *testing.T) {
	cfg, err := loadWithDevelopmentLookup(func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("load development config: %v", err)
	}
	if err := cfg.ValidateController(); err == nil || !strings.Contains(err.Error(), "controller EMFONT_ENV") {
		t.Fatalf("development controller validation error = %v", err)
	}

	cfg.Environment = "production"
	cfg.HTTP.AllowedOrigins = []string{"https://fonts.example"}
	cfg.ShutdownTimeout = cfg.ShutdownPropagationDelay + cfg.HTTP.RequestTimeout + shutdownResourceCloseMargin - time.Nanosecond
	if err := cfg.ValidateController(); err == nil || !strings.Contains(err.Error(), "resource-close margin") {
		t.Fatalf("undersized shutdown budget error = %v", err)
	}
	cfg.ShutdownTimeout++
	if err := cfg.ValidateController(); err != nil {
		t.Fatalf("exact shutdown budget: %v", err)
	}
}

func TestValidateControllerRejectsCleanupAndMigrationRolesWithoutInvalidatingSharedConfig(t *testing.T) {
	for _, environment := range []string{"cleanup", "migration"} {
		t.Run(environment, func(t *testing.T) {
			cfg, err := LoadWithLookup(func(key string) (string, bool) {
				if key == "EMFONT_ENV" {
					return environment, true
				}
				return "", false
			})
			if err != nil {
				t.Fatalf("shared config validation: %v", err)
			}
			if err := cfg.ValidateController(); err == nil || !strings.Contains(err.Error(), "controller EMFONT_ENV") {
				t.Fatalf("controller role error = %v", err)
			}
		})
	}
}

func TestLoadWithLookupRejectsUnsafeTimeoutOrdering(t *testing.T) {
	_, err := loadWithDevelopmentLookup(func(key string) (string, bool) {
		switch key {
		case "EMFONT_HTTP_WRITE_TIMEOUT":
			return "10s", true
		case "EMFONT_FONT_BUILD_LEASE":
			return "30s", true
		default:
			return "", false
		}
	})
	if err == nil {
		t.Fatal("LoadWithLookup returned nil error")
	}
	if !strings.Contains(err.Error(), "EMFONT_HTTP_WRITE_TIMEOUT") || !strings.Contains(err.Error(), "EMFONT_FONT_BUILD_LEASE") {
		t.Fatalf("error = %v, want timeout ordering errors", err)
	}
}

func loadWithDevelopmentLookup(lookup lookupFunc) (Config, error) {
	return LoadWithLookup(func(key string) (string, bool) {
		if value, ok := lookup(key); ok {
			return value, true
		}
		if key == "EMFONT_ENV" {
			return "development", true
		}
		return "", false
	})
}

func TestLoadWithLookupRejectsUnsafeCleanupWindows(t *testing.T) {
	_, err := loadWithDevelopmentLookup(func(key string) (string, bool) {
		switch key {
		case "EMFONT_MINIO_PRESIGN_EXPIRY":
			return "3h", true
		case "EMFONT_CLEANUP_RETIREMENT_GRACE":
			return "2h", true
		case "EMFONT_CLEANUP_ORPHAN_GRACE":
			return "1m", true
		default:
			return "", false
		}
	})
	if err == nil {
		t.Fatal("LoadWithLookup returned nil error")
	}
	if !strings.Contains(err.Error(), "EMFONT_CLEANUP_RETIREMENT_GRACE") ||
		!strings.Contains(err.Error(), "EMFONT_CLEANUP_ORPHAN_GRACE") {
		t.Fatalf("cleanup window error = %v", err)
	}
}
