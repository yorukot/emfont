//go:build linux

package main

import (
	"fmt"
	"syscall"
)

type resourceLimits struct {
	addressSpaceBytes uint64
	cpuSeconds        uint64
	fileSizeBytes     uint64
	openFiles         uint64
}

func applyResourceLimits(limits resourceLimits) error {
	settings := []struct {
		resource int
		value    uint64
		name     string
	}{
		{resource: syscall.RLIMIT_CPU, value: limits.cpuSeconds, name: "CPU"},
		{resource: syscall.RLIMIT_FSIZE, value: limits.fileSizeBytes, name: "file size"},
		{resource: syscall.RLIMIT_NOFILE, value: limits.openFiles, name: "open files"},
		{resource: syscall.RLIMIT_AS, value: limits.addressSpaceBytes, name: "address space"},
	}
	for _, setting := range settings {
		var inherited syscall.Rlimit
		if err := syscall.Getrlimit(setting.resource, &inherited); err != nil {
			return fmt.Errorf("read inherited %s limit: %w", setting.name, err)
		}
		value := setting.value
		if inherited.Max != ^uint64(0) && value > inherited.Max {
			return fmt.Errorf("inherited %s limit is below the configured limit", setting.name)
		}
		if value == 0 {
			return fmt.Errorf("inherited %s limit is zero", setting.name)
		}
		if err := syscall.Setrlimit(setting.resource, &syscall.Rlimit{Cur: value, Max: value}); err != nil {
			return fmt.Errorf("set %s limit: %w", setting.name, err)
		}
	}
	return nil
}
