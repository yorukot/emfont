//go:build cgo

package harfbuzznative

/*
#cgo pkg-config: harfbuzz harfbuzz-subset libwoff2enc
#include "bridge.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"unsafe"
)

var woff2Version = "unknown"

func Version() string {
	version := strings.TrimSpace(woff2Version)
	if version == "" {
		version = "unknown"
	}
	return "harfbuzz-" + C.GoString(C.emfont_harfbuzz_version()) + "-woff2-" + version
}

func Available() error { return nil }

func BuildSubset(source []byte, codepoints []rune, maxOutputBytes int64) (Output, error) {
	if len(source) == 0 {
		return Output{}, errors.New("source font is empty")
	}
	if len(codepoints) == 0 {
		return Output{}, errors.New("requested codepoint set is empty")
	}
	if maxOutputBytes <= 0 || uint64(maxOutputBytes) > uint64(^C.size_t(0)) {
		return Output{}, errors.New("maximum output size is outside the native bridge range")
	}

	nativeCodepoints := make([]C.uint32_t, len(codepoints))
	for index, codepoint := range codepoints {
		nativeCodepoints[index] = C.uint32_t(codepoint)
	}

	var output *C.uint8_t
	var outputLength C.size_t
	var glyphCount C.size_t
	var errorMessage *C.char
	result := C.emfont_subset_woff2(
		(*C.uint8_t)(unsafe.Pointer(&source[0])),
		C.size_t(len(source)),
		(*C.uint32_t)(unsafe.Pointer(&nativeCodepoints[0])),
		C.size_t(len(nativeCodepoints)),
		C.size_t(maxOutputBytes),
		&output,
		&outputLength,
		&glyphCount,
		&errorMessage,
	)
	if errorMessage != nil {
		defer C.emfont_free(unsafe.Pointer(errorMessage))
	}
	if result <= 0 {
		message := "HarfBuzz subset failed"
		if errorMessage != nil {
			message = C.GoString(errorMessage)
		}
		if result == -1 {
			return Output{}, fmt.Errorf("%w: %s", ErrUnsupportedCodepoints, message)
		}
		if result == -2 {
			return Output{}, fmt.Errorf("%w: %s", ErrOutputLimit, message)
		}
		return Output{}, errors.New(message)
	}
	if output == nil || outputLength == 0 {
		return Output{}, errors.New("HarfBuzz returned an empty WOFF2 font")
	}
	defer C.emfont_free(unsafe.Pointer(output))
	if uint64(outputLength) > math.MaxInt32 {
		return Output{}, errors.New("HarfBuzz output exceeds the Go bridge limit")
	}
	if uint64(outputLength) > uint64(maxOutputBytes) {
		return Output{}, ErrOutputLimit
	}
	if uint64(glyphCount) > math.MaxInt {
		return Output{}, errors.New("HarfBuzz glyph count exceeds the Go bridge limit")
	}

	return Output{
		Data:       C.GoBytes(unsafe.Pointer(output), C.int(outputLength)),
		GlyphCount: int(glyphCount),
	}, nil
}
