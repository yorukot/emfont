package system

import (
	"errors"
	"testing"
)

func TestVNNormalizesSystem(t *testing.T) {
	got, err := VN(VNInput{
		ID:          " Controller_01 ",
		Name:        "  Emfont   Controller ",
		Environment: " Production ",
		Version:     " v1.2.3 ",
		Revision:    " abc123 ",
	})
	if err != nil {
		t.Fatalf("VN returned error: %v", err)
	}

	if got.ID() != "controller_01" {
		t.Fatalf("ID() = %q", got.ID())
	}
	if got.Name() != "Emfont Controller" {
		t.Fatalf("Name() = %q", got.Name())
	}
	if got.Environment() != "production" {
		t.Fatalf("Environment() = %q", got.Environment())
	}
	if got.Version() != "v1.2.3" {
		t.Fatalf("Version() = %q", got.Version())
	}
	if got.Revision() != "abc123" {
		t.Fatalf("Revision() = %q", got.Revision())
	}
	if got.Status() != StatusReady {
		t.Fatalf("Status() = %q", got.Status())
	}
}

func TestVNRejectsInvalidSystem(t *testing.T) {
	_, err := VN(VNInput{
		ID:          "bad id",
		Environment: "prod",
		Status:      "unknown",
	})
	if err == nil {
		t.Fatal("VN returned nil error")
	}
	if !errors.Is(err, ErrInvalidSystem) {
		t.Fatalf("error = %v, want ErrInvalidSystem", err)
	}
}

func TestNormalizeIDDefaultsEmptyID(t *testing.T) {
	got, err := NormalizeID(" ")
	if err != nil {
		t.Fatalf("NormalizeID returned error: %v", err)
	}
	if got != DefaultID {
		t.Fatalf("NormalizeID() = %q, want %q", got, DefaultID)
	}
}
