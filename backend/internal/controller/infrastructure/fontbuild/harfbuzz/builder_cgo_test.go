//go:build cgo

package harfbuzz

import (
	"bytes"
	"context"
	"os"
	"testing"

	appfont "github.com/emfont/emfont/backend/internal/controller/application/font"
	"github.com/emfont/emfont/backend/internal/domain/font"
)

func TestBuildSubsetProducesWOFF2(t *testing.T) {
	const fixture = "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf"
	source, err := os.ReadFile(fixture)
	if os.IsNotExist(err) {
		t.Skip("system font fixture is not installed")
	}
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	output, err := New().BuildSubset(context.Background(), appfont.BuildInput{
		Source: source, Codepoints: []rune("Hello"), SourceFormat: "ttf", TargetFormat: font.OutputFormatWOFF2,
	})
	if err != nil {
		t.Fatalf("BuildSubset: %v", err)
	}
	if !bytes.HasPrefix(output.Data, []byte("wOF2")) {
		t.Fatalf("output magic = %q, want wOF2", output.Data[:min(4, len(output.Data))])
	}
	if output.GlyphCount == 0 {
		t.Fatal("output glyph count is zero")
	}
	for iteration := 0; iteration < 16; iteration++ {
		rebuilt, err := New().BuildSubset(context.Background(), appfont.BuildInput{
			Source: source, Codepoints: []rune("Hello"), SourceFormat: "ttf", TargetFormat: font.OutputFormatWOFF2,
		})
		if err != nil {
			t.Fatalf("repeat BuildSubset %d: %v", iteration, err)
		}
		if !bytes.Equal(rebuilt.Data, output.Data) {
			t.Fatalf("repeat BuildSubset %d produced different bytes", iteration)
		}
	}
}
