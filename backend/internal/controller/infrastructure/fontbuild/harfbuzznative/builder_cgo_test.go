//go:build cgo

package harfbuzznative

import (
	"errors"
	"os"
	"testing"
)

func TestBuildSubsetRejectsWOFF2CapacityBeforeOutputAllocation(t *testing.T) {
	fixture := os.Getenv("EMFONT_TEST_FONT_PATH")
	if fixture == "" {
		fixture = "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf"
	}
	source, err := os.ReadFile(fixture)
	if os.IsNotExist(err) {
		t.Skip("system font fixture is not installed")
	}
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	_, err = BuildSubset(source, []rune("Hello"), 1)
	if !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("BuildSubset error = %v, want ErrOutputLimit", err)
	}
}
