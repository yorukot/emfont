package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestConfigValidateAllowsDatabaseURLUsernameWithoutPassword(t *testing.T) {
	t.Setenv("PGPASSWORD", "password-from-compose-secret")

	err := (Config{
		DatabaseURL: "postgres://emfont_app@postgres:5432/emfont?sslmode=disable",
	}).Validate()
	if err != nil {
		t.Fatalf("Validate returned error for username-only database URL: %v", err)
	}
}

func TestConfigValidateRejectsDatabaseURLUserinfoPassword(t *testing.T) {
	const sentinel = "DB_USERINFO_SECRET_7a43c9"

	for _, test := range []struct {
		name        string
		databaseURL string
	}{
		{
			name:        "password",
			databaseURL: "postgres://emfont_app:" + sentinel + "@postgres:5432/emfont",
		},
		{
			name:        "empty password",
			databaseURL: "postgres://emfont_app:@postgres:5432/emfont",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := (Config{DatabaseURL: test.databaseURL}).Validate()
			if err == nil || !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("Validate error = %v, want ErrInvalidConfig", err)
			}
			if !strings.Contains(err.Error(), "must not include a userinfo password") {
				t.Fatalf("Validate error = %v, want userinfo password validation error", err)
			}
			assertStartupErrorOmits(t, err, test.databaseURL, sentinel)
		})
	}
}

func TestNewPoolDatabaseURLParseErrorsDoNotLeakCredentials(t *testing.T) {
	const sentinel = "DB_AUDIT_SECRET_91f6d2"

	for _, test := range []struct {
		name        string
		databaseURL string
	}{
		{
			name:        "audit malformed URL escape",
			databaseURL: "postgres://audit_user:" + sentinel + "@postgres:5432/emfont/%zz",
		},
		{
			name:        "pgx URL validation",
			databaseURL: "postgres://audit_user@postgres:5432/emfont?connect_timeout=" + sentinel,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewPool(context.Background(), Config{DatabaseURL: test.databaseURL})
			if err == nil || !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("NewPool error = %v, want ErrInvalidConfig", err)
			}
			assertStartupErrorOmits(t, err, test.databaseURL, sentinel)
		})
	}
}

func assertStartupErrorOmits(t *testing.T, err error, forbidden ...string) {
	t.Helper()

	var startupLog bytes.Buffer
	_, _ = fmt.Fprintf(&startupLog, "startup failed: %v\n", err)
	for _, value := range forbidden {
		if value == "" {
			continue
		}
		if strings.Contains(err.Error(), value) {
			t.Fatalf("error leaked forbidden database URL content %q: %v", value, err)
		}
		if strings.Contains(startupLog.String(), value) {
			t.Fatalf("startup log leaked forbidden database URL content %q: %s", value, startupLog.String())
		}
	}
}
