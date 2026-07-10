//go:build !cgo

package harfbuzz

import (
	"context"
	"errors"

	appfont "github.com/emfont/emfont/backend/internal/controller/application/font"
)

func (b *Builder) BuildSubset(context.Context, appfont.BuildInput) (appfont.BuildOutput, error) {
	return appfont.BuildOutput{}, errors.New("HarfBuzz builder requires cgo")
}

func (b *Builder) Version() string {
	return "harfbuzz-unavailable"
}

func (b *Builder) Available() error {
	return errors.New("HarfBuzz builder requires cgo")
}
