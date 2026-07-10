package config

import (
	"strings"
	"testing"
)

func TestLoadWithLookupDefaults(t *testing.T) {
	cfg, err := LoadWithLookup(func(string) (string, bool) {
		return "", false
	})
	if err != nil {
		t.Fatalf("LoadWithLookup returned error: %v", err)
	}

	if cfg.ServiceName != defaultServiceName {
		t.Fatalf("ServiceName = %q, want %q", cfg.ServiceName, defaultServiceName)
	}
	if cfg.APIVersion != defaultAPIVersion {
		t.Fatalf("APIVersion = %q, want %q", cfg.APIVersion, defaultAPIVersion)
	}
	if cfg.Database.URL != defaultDatabaseURL {
		t.Fatalf("Database.URL = %q, want default URL", cfg.Database.URL)
	}
}

func TestLoadWithLookupRejectsInvalidValues(t *testing.T) {
	_, err := LoadWithLookup(func(key string) (string, bool) {
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
	_, err := LoadWithLookup(func(key string) (string, bool) {
		if key == "EMFONT_MINIO_ENABLED" {
			return "true", true
		}
		return "", false
	})
	if err == nil || !strings.Contains(err.Error(), "EMFONT_MINIO_ENDPOINT") {
		t.Fatalf("error = %v, want incomplete MinIO config error", err)
	}
}

func TestLoadWithLookupRejectsUnsafeTimeoutOrdering(t *testing.T) {
	_, err := LoadWithLookup(func(key string) (string, bool) {
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
