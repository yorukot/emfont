package httpx_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/emfont/emfont/backend/internal/controller/transport/http/httpx"
)

func TestRequireJSONContentType(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		wantErr     bool
	}{
		{name: "json", contentType: "application/json"},
		{name: "json with charset", contentType: "application/json; charset=utf-8"},
		{name: "json with quoted parameters", contentType: `Application/JSON; Charset="UTF-8"; profile="generate"`},
		{name: "missing", wantErr: true},
		{name: "unsupported", contentType: "text/plain", wantErr: true},
		{name: "json suffix", contentType: "application/problem+json", wantErr: true},
		{name: "malformed", contentType: "application/json; charset", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/g/demo", nil)
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}

			err := httpx.RequireJSONContentType(request)
			if test.wantErr && !errors.Is(err, httpx.ErrUnsupportedMediaType) {
				t.Fatalf("RequireJSONContentType() error = %v, want ErrUnsupportedMediaType", err)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("RequireJSONContentType() error = %v, want nil", err)
			}
		})
	}
}

func TestDecodeJSONLimitAcceptsBodyAtLimit(t *testing.T) {
	const limit = int64(32)
	body := `"` + strings.Repeat("x", int(limit)-2) + `"`
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	var decoded string

	if err := httpx.DecodeJSONLimit(request, &decoded, limit); err != nil {
		t.Fatalf("DecodeJSONLimit() error = %v, want nil", err)
	}
	if decoded != strings.Repeat("x", int(limit)-2) {
		t.Fatalf("decoded value length = %d, want %d", len(decoded), limit-2)
	}
}

func TestDecodeJSONLimitRejectsOversizedBodies(t *testing.T) {
	const limit = int64(64)
	tests := []struct {
		name string
		body string
	}{
		{name: "large value", body: `{"value":"` + strings.Repeat("x", int(limit)) + `"}`},
		{name: "large trailing whitespace", body: `{}` + strings.Repeat(" ", int(limit))},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(test.body))
			var decoded struct {
				Value string `json:"value"`
			}

			err := httpx.DecodeJSONLimit(request, &decoded, limit)
			if !errors.Is(err, httpx.ErrBodyTooLarge) {
				t.Fatalf("DecodeJSONLimit() error = %v, want ErrBodyTooLarge", err)
			}
		})
	}
}

func TestDecodeJSONLimitRejectsInvalidJSONFraming(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr error
	}{
		{name: "empty", body: "", wantErr: httpx.ErrEmptyBody},
		{name: "whitespace", body: " \n\t", wantErr: httpx.ErrEmptyBody},
		{name: "multiple values", body: `{} {}`, wantErr: httpx.ErrMultipleJSON},
		{name: "malformed", body: `{"value":`, wantErr: nil},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(test.body))
			var decoded map[string]any
			err := httpx.DecodeJSONLimit(request, &decoded, 1024)
			if err == nil {
				t.Fatal("DecodeJSONLimit() error = nil, want an error")
			}
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("DecodeJSONLimit() error = %v, want %v", err, test.wantErr)
			}
			if test.wantErr == nil && (errors.Is(err, httpx.ErrEmptyBody) || errors.Is(err, httpx.ErrMultipleJSON) || errors.Is(err, httpx.ErrBodyTooLarge)) {
				t.Fatalf("DecodeJSONLimit() malformed error = %v, want syntax error", err)
			}
		})
	}
}
