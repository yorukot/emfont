package logger

import "testing"

func TestNewAcceptsConfiguredTextEncoding(t *testing.T) {
	log, err := New(Config{Level: "info", Environment: "development", Encoding: "text"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = Sync(log) }()
	log.Info("logger smoke test")
}
