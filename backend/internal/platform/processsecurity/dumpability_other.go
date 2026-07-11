//go:build !linux

package processsecurity

func DisableDumpability() error { return nil }
