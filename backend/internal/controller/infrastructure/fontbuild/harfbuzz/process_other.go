//go:build !linux

package harfbuzz

import "os/exec"

func configureProcess(*exec.Cmd) {}

func terminateProcessGroup(command *exec.Cmd) {
	if command == nil || command.Process == nil {
		return
	}
	_ = command.Process.Kill()
}
