package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

const (
	ContentTypeJSON    = "application/json; charset=utf-8"
	DefaultMaxBodySize = int64(1 << 20)
)

var (
	ErrEmptyBody    = errors.New("request body is empty")
	ErrBodyTooLarge = errors.New("request body is too large")
	ErrMultipleJSON = errors.New("request body must contain a single JSON value")
)

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
		if errors.Is(err, io.EOF) {
			return ErrEmptyBody
		}
		return fmt.Errorf("decode json: %w", err)
	}
	if limited != nil && limited.N == 0 {
		return ErrBodyTooLarge
	}

	var extra any
	if err := dec.Decode(&extra); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("decode json trailer: %w", err)
	} else if err == nil {
		return ErrMultipleJSON
	}

	return nil
}
