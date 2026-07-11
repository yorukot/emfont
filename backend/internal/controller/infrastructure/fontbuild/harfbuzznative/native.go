package harfbuzznative

import "errors"

var (
	ErrUnsupportedCodepoints = errors.New("font does not support every requested codepoint")
	ErrOutputLimit           = errors.New("font output exceeds the configured limit")
)

type Output struct {
	Data       []byte
	GlyphCount int
}
