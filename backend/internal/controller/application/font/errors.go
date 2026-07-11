package font

import (
	"errors"
	"time"
)

var (
	ErrInvalidInput             = errors.New("invalid font input")
	ErrFontNotFound             = errors.New("font not found")
	ErrFontSourceNotFound       = errors.New("font source not found")
	ErrArtifactNotFound         = errors.New("font artifact not found")
	ErrArtifactCapacity         = errors.New("font artifact capacity exceeded")
	ErrBuildNotReady            = errors.New("font build not ready")
	ErrBuildQueueFull           = errors.New("font build queue is full")
	ErrBuildFailed              = errors.New("font build failed")
	ErrUnsupportedCodepoints    = errors.New("font does not support every requested codepoint")
	ErrObjectNotFound           = errors.New("object not found")
	ErrObjectStorageUnavailable = errors.New("object storage unavailable")
)

type retryAfterError struct {
	err   error
	after time.Duration
}

func (e retryAfterError) Error() string { return e.err.Error() }
func (e retryAfterError) Unwrap() error { return e.err }

func (e retryAfterError) RetryAfter() time.Duration { return e.after }

func withRetryAfter(err error, after time.Duration) error {
	if after < time.Second {
		after = time.Second
	}
	return retryAfterError{err: err, after: after}
}

func RetryAfterDuration(err error) time.Duration {
	var retryable interface{ RetryAfter() time.Duration }
	if errors.As(err, &retryable) {
		return retryable.RetryAfter()
	}
	return time.Second
}
