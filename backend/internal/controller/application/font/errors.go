package font

import "errors"

var (
	ErrInvalidInput             = errors.New("invalid font input")
	ErrFontNotFound             = errors.New("font not found")
	ErrFontSourceNotFound       = errors.New("font source not found")
	ErrArtifactNotFound         = errors.New("font artifact not found")
	ErrBuildNotReady            = errors.New("font build not ready")
	ErrBuildFailed              = errors.New("font build failed")
	ErrObjectNotFound           = errors.New("object not found")
	ErrObjectStorageUnavailable = errors.New("object storage unavailable")
)
