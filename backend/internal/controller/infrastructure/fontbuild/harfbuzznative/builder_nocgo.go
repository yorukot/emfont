//go:build !cgo

package harfbuzznative

import "errors"

func Version() string { return "harfbuzz-unavailable-woff2-unavailable" }

func Available() error { return errors.New("native HarfBuzz worker requires cgo") }

func BuildSubset([]byte, []rune, int64) (Output, error) {
	return Output{}, errors.New("native HarfBuzz worker requires cgo")
}
