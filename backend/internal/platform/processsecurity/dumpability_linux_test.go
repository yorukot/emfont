//go:build linux

package processsecurity

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

const dumpabilityHelperMode = "EMFONT_DUMPABILITY_TEST_HELPER"

func TestDisableDumpabilityBlocksSameUIDChildFromReadingEnvironment(t *testing.T) {
	switch os.Getenv(dumpabilityHelperMode) {
	case "parent":
		runDumpabilityParentHelper()
	case "child":
		runDumpabilityChildHelper()
	}

	executable := os.Args[0]
	command := exec.Command(executable, "-test.run=^TestDisableDumpabilityBlocksSameUIDChildFromReadingEnvironment$")
	command.Env = []string{dumpabilityHelperMode + "=parent"}
	if os.Geteuid() == 0 {
		executable = copyTestExecutable(t)
		command = exec.Command(executable, "-test.run=^TestDisableDumpabilityBlocksSameUIDChildFromReadingEnvironment$")
		command.Env = []string{dumpabilityHelperMode + "=parent"}
		command.SysProcAttr = &syscall.SysProcAttr{
			Credential: &syscall.Credential{Uid: 65534, Gid: 65534},
		}
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("dumpability helper: %v\n%s", err, output)
	}
}

func runDumpabilityParentHelper() {
	if err := DisableDumpability(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestDisableDumpabilityBlocksSameUIDChildFromReadingEnvironment$")
	command.Env = []string{dumpabilityHelperMode + "=child"}
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "same-UID child: %v\n", err)
		os.Exit(3)
	}
	os.Exit(0)
}

func runDumpabilityChildHelper() {
	_, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(os.Getppid()), "environ"))
	if err == nil {
		_, _ = fmt.Fprintln(os.Stderr, "same-UID child read its non-dumpable parent's environment")
		os.Exit(4)
	}
	if !errors.Is(err, os.ErrPermission) && !errors.Is(err, syscall.EPERM) {
		_, _ = fmt.Fprintf(os.Stderr, "read parent environment: %v\n", err)
		os.Exit(5)
	}
	os.Exit(0)
}

func copyTestExecutable(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("", "emfont-processsecurity-test-")
	if err != nil {
		t.Fatalf("create helper directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatalf("make helper directory accessible: %v", err)
	}
	sourcePath, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test executable: %v", err)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatalf("open test executable: %v", err)
	}
	defer source.Close()
	targetPath := filepath.Join(directory, "processsecurity.test")
	target, err := os.OpenFile(targetPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatalf("create test executable copy: %v", err)
	}
	if _, err := io.Copy(target, source); err != nil {
		_ = target.Close()
		t.Fatalf("copy test executable: %v", err)
	}
	if err := target.Close(); err != nil {
		t.Fatalf("close test executable copy: %v", err)
	}
	return targetPath
}
