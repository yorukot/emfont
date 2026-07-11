package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
)

const (
	ContentTypeJSON    = "application/json; charset=utf-8"
	DefaultMaxBodySize = int64(1 << 20)
)

var (
	ErrEmptyBody            = errors.New("request body is empty")
	ErrBodyTooLarge         = errors.New("request body is too large")
	ErrMultipleJSON         = errors.New("request body must contain a single JSON value")
	ErrUnsupportedMediaType = errors.New("content type must be application/json")
)

func RequireJSONContentType(r *http.Request) error {
	if r == nil {
		return ErrUnsupportedMediaType
	}

	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return ErrUnsupportedMediaType
	}
	return nil
}

func WriteJSON(w http.ResponseWriter, status int, value any) error {
	w.Header().Set("Content-Type", ContentTypeJSON)
	w.WriteHeader(status)
	if value == nil {
		return nil
	}
	return json.NewEncoder(w).Encode(value)
}

func WriteNoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

func DecodeJSON(r *http.Request, dst any) error {
	return DecodeJSONLimit(r, dst, DefaultMaxBodySize)
}

func DecodeJSONLimit(r *http.Request, dst any, maxBytes int64) error {
	if r.Body == nil {
		return ErrEmptyBody
	}

	reader := io.Reader(r.Body)
	var limited *io.LimitedReader
	if maxBytes > 0 {
		limited = &io.LimitedReader{R: r.Body, N: maxBytes + 1}
		reader = limited
	}

	dec := json.NewDecoder(reader)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		if exceedsLimit(limited) {
			return ErrBodyTooLarge
		}
		if errors.Is(err, io.EOF) {
			return ErrEmptyBody
		}
		return fmt.Errorf("decode json: %w", err)
	}

	var extra any
	err := dec.Decode(&extra)
	if exceedsLimit(limited) {
		return ErrBodyTooLarge
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("decode json trailer: %w", err)
	} else if err == nil {
		return ErrMultipleJSON
	}

	return nil
}

func exceedsLimit(limited *io.LimitedReader) bool {
	if limited == nil {
		return false
	}
	if limited.N > 0 {
		_, _ = io.Copy(io.Discard, limited)
	}
	return limited.N == 0
}
