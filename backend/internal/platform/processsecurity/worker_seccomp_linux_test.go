//go:build linux && (amd64 || arm64)

package processsecurity

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

const seccompHelper = "EMFONT_SECCOMP_TEST_HELPER"
const seccompFixture = "EMFONT_SECCOMP_TEST_FIXTURE"
const seccompParentResourceHelper = "EMFONT_SECCOMP_PARENT_RESOURCE_TEST_HELPER"

func TestApplyFontWorkerSandboxDeniesExternalResources(t *testing.T) {
	if os.Getenv(seccompHelper) == "1" {
		runSeccompHelper()
	}

	fixture := filepath.Join(t.TempDir(), "worker-must-not-read")
	if err := os.WriteFile(fixture, []byte("controller-private-data"), 0o600); err != nil {
		t.Fatalf("write sandbox fixture: %v", err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestApplyFontWorkerSandboxDeniesExternalResources$")
	command.Env = append(os.Environ(), seccompHelper+"=1", seccompFixture+"="+fixture)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("seccomp helper: %v\n%s", err, output)
	}
}

func TestApplyFontWorkerSandboxDeniesParentResourceControls(t *testing.T) {
	if os.Getenv(seccompParentResourceHelper) == "1" {
		runParentResourceHelper()
	}

	command := exec.Command(os.Args[0], "-test.run=^TestApplyFontWorkerSandboxDeniesParentResourceControls$")
	command.Env = append(os.Environ(), seccompParentResourceHelper+"=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("parent resource helper: %v\n%s", err, output)
	}
}

func TestNetworkSyscallFilterValidatesArchitectureFirst(t *testing.T) {
	filter := workerSyscallFilter()
	if len(filter) < 7 {
		t.Fatalf("filter length = %d", len(filter))
	}
	if filter[0].Code != bpfLoadWordAbsolute || filter[0].K != seccompDataArchOffset {
		t.Fatalf("first instruction = %#v, want architecture load", filter[0])
	}
	if filter[1].Code != bpfJumpEqual || filter[1].K != auditArchitecture || filter[1].Jt != 1 {
		t.Fatalf("architecture comparison = %#v", filter[1])
	}
	if filter[2].Code != bpfReturn || filter[2].K != seccompReturnKillProcess {
		t.Fatalf("architecture mismatch action = %#v", filter[2])
	}
	if filter[4].Code != bpfJumpGreaterEqual || filter[5].K != seccompReturnKillProcess {
		t.Fatalf("high syscall namespace guard = %#v, %#v", filter[4], filter[5])
	}
}

func TestWorkerSyscallFilterContainsProcessIsolationRules(t *testing.T) {
	filter := workerSyscallFilter()
	want := map[uint32]bool{
		cloneSystemCall:                     false,
		clone3SystemCall:                    false,
		tgkillSystemCall:                    false,
		uint32(unix.SYS_PRLIMIT64):          false,
		uint32(unix.SYS_SETPRIORITY):        false,
		uint32(unix.SYS_SCHED_SETPARAM):     false,
		uint32(unix.SYS_SCHED_SETSCHEDULER): false,
		uint32(unix.SYS_SCHED_SETAFFINITY):  false,
		uint32(unix.SYS_SCHED_SETATTR):      false,
		uint32(unix.SYS_IOPRIO_SET):         false,
		uint32(unix.SYS_OPENAT):             false,
		uint32(unix.SYS_OPENAT2):            false,
		uint32(unix.SYS_EXECVE):             false,
		uint32(unix.SYS_EXECVEAT):           false,
		uint32(unix.SYS_PTRACE):             false,
		uint32(unix.SYS_PROCESS_VM_READV):   false,
		uint32(unix.SYS_PROCESS_VM_WRITEV):  false,
		uint32(unix.SYS_PIDFD_GETFD):        false,
		uint32(unix.SYS_UNSHARE):            false,
		uint32(unix.SYS_SETNS):              false,
		uint32(unix.SYS_MOUNT):              false,
	}
	for _, instruction := range filter {
		if instruction.Code == bpfJumpEqual {
			if _, ok := want[instruction.K]; ok {
				want[instruction.K] = true
			}
		}
	}
	for syscallNumber, found := range want {
		if !found {
			t.Errorf("worker filter does not inspect syscall %d", syscallNumber)
		}
	}
}

type schedAttr struct {
	Size     uint32
	Policy   uint32
	Flags    uint64
	Nice     int32
	Priority uint32
	Runtime  uint64
	Deadline uint64
	Period   uint64
	UtilMin  uint32
	UtilMax  uint32
}

type schedParam struct {
	Priority int32
}

type parentResourceMutation struct {
	name string
	call func() syscall.Errno
}

func runParentResourceHelper() {
	mutations, err := parentResourceMutations(os.Getppid())
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(20)
	}
	for _, mutation := range mutations {
		if errno := mutation.call(); errno != 0 {
			_, _ = fmt.Fprintf(os.Stderr, "preflight %s against parent: %v\n", mutation.name, errno)
			os.Exit(21)
		}
	}
	if err := ApplyFontWorkerSandbox(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(22)
	}
	for _, mutation := range mutations {
		if errno := mutation.call(); errno != syscall.EPERM {
			_, _ = fmt.Fprintf(os.Stderr, "%s against parent: %v, want EPERM\n", mutation.name, errno)
			os.Exit(23)
		}
	}
	os.Exit(0)
}

func parentResourceMutations(parentPID int) ([]parentResourceMutation, error) {
	var limit unix.Rlimit
	if _, _, errno := syscall.RawSyscall6(
		unix.SYS_PRLIMIT64,
		uintptr(parentPID),
		unix.RLIMIT_NOFILE,
		0,
		uintptr(unsafe.Pointer(&limit)),
		0,
		0,
	); errno != 0 {
		return nil, fmt.Errorf("read parent rlimit: %w", errno)
	}

	attribute := schedAttr{Size: uint32(unsafe.Sizeof(schedAttr{}))}
	if _, _, errno := syscall.RawSyscall6(
		unix.SYS_SCHED_GETATTR,
		uintptr(parentPID),
		uintptr(unsafe.Pointer(&attribute)),
		uintptr(attribute.Size),
		0,
		0,
		0,
	); errno != 0 {
		return nil, fmt.Errorf("read parent scheduler attributes: %w", errno)
	}
	parameter := schedParam{Priority: int32(attribute.Priority)}

	var affinity unix.CPUSet
	if err := unix.SchedGetaffinity(parentPID, &affinity); err != nil {
		return nil, fmt.Errorf("read parent CPU affinity: %w", err)
	}
	ioprio, _, errno := syscall.RawSyscall(unix.SYS_IOPRIO_GET, 1, uintptr(parentPID), 0)
	if errno != 0 {
		return nil, fmt.Errorf("read parent I/O priority: %w", errno)
	}

	return []parentResourceMutation{
		{
			name: "prlimit64",
			call: func() syscall.Errno {
				_, _, errno := syscall.RawSyscall6(
					unix.SYS_PRLIMIT64,
					uintptr(parentPID),
					unix.RLIMIT_NOFILE,
					uintptr(unsafe.Pointer(&limit)),
					0,
					0,
					0,
				)
				return errno
			},
		},
		{
			name: "setpriority",
			call: func() syscall.Errno {
				_, _, errno := syscall.RawSyscall(
					unix.SYS_SETPRIORITY,
					unix.PRIO_PROCESS,
					uintptr(parentPID),
					uintptr(attribute.Nice),
				)
				return errno
			},
		},
		{
			name: "sched_setparam",
			call: func() syscall.Errno {
				_, _, errno := syscall.RawSyscall(
					unix.SYS_SCHED_SETPARAM,
					uintptr(parentPID),
					uintptr(unsafe.Pointer(&parameter)),
					0,
				)
				return errno
			},
		},
		{
			name: "sched_setscheduler",
			call: func() syscall.Errno {
				_, _, errno := syscall.RawSyscall(
					unix.SYS_SCHED_SETSCHEDULER,
					uintptr(parentPID),
					uintptr(attribute.Policy),
					uintptr(unsafe.Pointer(&parameter)),
				)
				return errno
			},
		},
		{
			name: "sched_setaffinity",
			call: func() syscall.Errno {
				_, _, errno := syscall.RawSyscall(
					unix.SYS_SCHED_SETAFFINITY,
					uintptr(parentPID),
					unsafe.Sizeof(affinity),
					uintptr(unsafe.Pointer(&affinity)),
				)
				return errno
			},
		},
		{
			name: "sched_setattr",
			call: func() syscall.Errno {
				_, _, errno := syscall.RawSyscall(
					unix.SYS_SCHED_SETATTR,
					uintptr(parentPID),
					uintptr(unsafe.Pointer(&attribute)),
					0,
				)
				return errno
			},
		},
		{
			name: "ioprio_set",
			call: func() syscall.Errno {
				_, _, errno := syscall.RawSyscall(
					unix.SYS_IOPRIO_SET,
					1,
					uintptr(parentPID),
					ioprio,
				)
				return errno
			},
		},
	}, nil
}

func TestValidateSeccompSyncRejectsUnsynchronizedThread(t *testing.T) {
	if err := validateSeccompSync(42, 0); err == nil {
		t.Fatal("positive TSYNC result was accepted")
	}
}

func runSeccompHelper() {
	if err := ApplyFontWorkerSandbox(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	value, _, errno := syscall.RawSyscall6(syscall.SYS_PRCTL, 39, 0, 0, 0, 0, 0)
	if errno != 0 || value != 1 {
		_, _ = fmt.Fprintf(os.Stderr, "PR_GET_NO_NEW_PRIVS = %d, %v\n", value, errno)
		os.Exit(3)
	}
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err == nil {
		_ = syscall.Close(fd)
		_, _ = fmt.Fprintln(os.Stderr, "socket unexpectedly succeeded")
		os.Exit(4)
	}
	if !errors.Is(err, syscall.EPERM) {
		_, _ = fmt.Fprintf(os.Stderr, "socket error = %v, want EPERM\n", err)
		os.Exit(5)
	}
	if _, _, errno := syscall.RawSyscall(uintptr(cloneSystemCall), uintptr(syscall.SIGCHLD), 0, 0); errno != syscall.EPERM {
		_, _ = fmt.Fprintf(os.Stderr, "process clone error = %v, want EPERM\n", errno)
		os.Exit(6)
	}
	if _, _, errno := syscall.RawSyscall(uintptr(clone3SystemCall), 0, 0, 0); errno != syscall.ENOSYS {
		_, _ = fmt.Fprintf(os.Stderr, "clone3 error = %v, want ENOSYS\n", errno)
		os.Exit(10)
	}
	if _, _, errno := syscall.RawSyscall(uintptr(tgkillSystemCall), uintptr(os.Getppid()), uintptr(os.Getppid()), 0); errno != syscall.EPERM {
		_, _ = fmt.Fprintf(os.Stderr, "cross-process tgkill error = %v, want EPERM\n", errno)
		os.Exit(7)
	}
	if _, _, errno := syscall.RawSyscall(uintptr(tgkillSystemCall), uintptr(os.Getpid()), uintptr(syscall.Gettid()), 0); errno != 0 {
		_, _ = fmt.Fprintf(os.Stderr, "same-process tgkill error = %v, want success\n", errno)
		os.Exit(8)
	}
	if _, err := syscall.Setsid(); !errors.Is(err, syscall.EPERM) {
		_, _ = fmt.Fprintf(os.Stderr, "setsid error = %v, want EPERM\n", err)
		os.Exit(9)
	}
	if _, err := os.ReadFile(os.Getenv(seccompFixture)); !errors.Is(err, syscall.EPERM) {
		_, _ = fmt.Fprintf(os.Stderr, "filesystem read error = %v, want EPERM\n", err)
		os.Exit(11)
	}
	if err := os.WriteFile(os.Getenv(seccompFixture)+".created", []byte("x"), 0o600); !errors.Is(err, syscall.EPERM) {
		_, _ = fmt.Fprintf(os.Stderr, "filesystem write error = %v, want EPERM\n", err)
		os.Exit(12)
	}
	if _, _, errno := syscall.RawSyscall6(
		unix.SYS_PROCESS_VM_READV,
		uintptr(os.Getppid()),
		0,
		0,
		0,
		0,
		0,
	); errno != syscall.EPERM {
		_, _ = fmt.Fprintf(os.Stderr, "process_vm_readv error = %v, want EPERM\n", errno)
		os.Exit(13)
	}
	os.Exit(0)
}
