//go:build !linux

package main

import "errors"

type resourceLimits struct {
	addressSpaceBytes uint64
	cpuSeconds        uint64
	fileSizeBytes     uint64
	openFiles         uint64
}

func applyResourceLimits(resourceLimits) error {
	return errors.New("font worker resource limits require Linux")
}
