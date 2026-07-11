//go:build linux

package processsecurity

import (
	"fmt"
	"syscall"
)

const prSetDumpable = 4

// DisableDumpability prevents same-UID processes from using ptrace-gated procfs
// files to inspect the controller.
func DisableDumpability() error {
	_, _, errno := syscall.RawSyscall6(
		syscall.SYS_PRCTL,
		prSetDumpable,
		0,
		0,
		0,
		0,
		0,
	)
	if errno != 0 {
		return fmt.Errorf("set PR_SET_DUMPABLE=0: %w", errno)
	}
	return nil
}
