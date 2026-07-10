//go:build cgo

package harfbuzz

/*
#cgo pkg-config: harfbuzz harfbuzz-subset libwoff2enc
#include "bridge.h"
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"unsafe"

	appfont "github.com/emfont/emfont/backend/internal/controller/application/font"
	domain "github.com/emfont/emfont/backend/internal/domain/font"
)

func (b *Builder) Version() string {
	return "harfbuzz-" + C.GoString(C.emfont_harfbuzz_version()) + "-woff2"
}

func (b *Builder) Available() error {
	return nil
}

func (b *Builder) BuildSubset(ctx context.Context, input appfont.BuildInput) (appfont.BuildOutput, error) {
	if err := ctx.Err(); err != nil {
		return appfont.BuildOutput{}, err
	}
	if len(input.Source) == 0 {
		return appfont.BuildOutput{}, errors.New("source font is empty")
	}
	if len(input.Codepoints) == 0 {
		return appfont.BuildOutput{}, errors.New("requested codepoint set is empty")
	}
	if input.TargetFormat != "" && input.TargetFormat != domain.OutputFormatWOFF2 {
		return appfont.BuildOutput{}, fmt.Errorf("unsupported target format %q", input.TargetFormat)
	}

	codepoints := make([]C.uint32_t, len(input.Codepoints))
	for index, codepoint := range input.Codepoints {
		codepoints[index] = C.uint32_t(codepoint)
	}

	var output *C.uint8_t
	var outputLength C.size_t
	var glyphCount C.size_t
	var errorMessage *C.char
	result := C.emfont_subset_woff2(
		(*C.uint8_t)(unsafe.Pointer(&input.Source[0])),
		C.size_t(len(input.Source)),
		(*C.uint32_t)(unsafe.Pointer(&codepoints[0])),
		C.size_t(len(codepoints)),
		&output,
		&outputLength,
		&glyphCount,
		&errorMessage,
	)
	if errorMessage != nil {
		defer C.emfont_free(unsafe.Pointer(errorMessage))
	}
	if result == 0 {
		message := "HarfBuzz subset failed"
		if errorMessage != nil {
			message = C.GoString(errorMessage)
		}
		return appfont.BuildOutput{}, errors.New(message)
	}
	if output == nil || outputLength == 0 {
		return appfont.BuildOutput{}, errors.New("HarfBuzz returned an empty WOFF2 font")
	}
	defer C.emfont_free(unsafe.Pointer(output))
	if err := ctx.Err(); err != nil {
		return appfont.BuildOutput{}, err
	}

	data := C.GoBytes(unsafe.Pointer(output), C.int(outputLength))
	return appfont.BuildOutput{
		Data: data, ContentType: domain.ContentTypeWOFF2, Format: domain.OutputFormatWOFF2,
		GlyphCount: int(glyphCount), BuilderVersion: b.Version(),
	}, nil
}
