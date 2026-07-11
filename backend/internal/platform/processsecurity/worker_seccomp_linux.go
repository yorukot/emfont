//go:build linux && (amd64 || arm64)

package processsecurity

import (
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	prSetNoNewPrivileges = 38

	seccompSetModeFilter      = 1
	seccompFilterFlagTSync    = 1
	seccompReturnKillProcess  = 0x80000000
	seccompReturnErrno        = 0x00050000
	seccompReturnAllow        = 0x7fff0000
	seccompDataSyscallOffset  = 0
	seccompDataArchOffset     = 4
	seccompDataArgumentOffset = 16
	firstReservedSyscallValue = 0x40000000
	cloneThreadFlag           = 0x00010000

	bpfLoadWordAbsolute = 0x20
	bpfJumpEqual        = 0x15
	bpfJumpGreaterEqual = 0x35
	bpfJumpBitsSet      = 0x45
	bpfReturn           = 0x06
)

// ApplyFontWorkerSandbox permanently prevents the worker from gaining new
// privileges and denies filesystem, network, and process-isolation access on
// every thread. The worker communicates only through inherited standard I/O.
func ApplyFontWorkerSandbox() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := prctl(prSetNoNewPrivileges, 1); err != nil {
		return fmt.Errorf("set PR_SET_NO_NEW_PRIVS: %w", err)
	}

	filters := workerSyscallFilter()
	program := syscall.SockFprog{
		Len:    uint16(len(filters)),
		Filter: &filters[0],
	}
	result, _, errno := syscall.RawSyscall(
		uintptr(seccompSystemCall),
		seccompSetModeFilter,
		seccompFilterFlagTSync,
		uintptr(unsafe.Pointer(&program)),
	)
	runtime.KeepAlive(filters)
	return validateSeccompSync(result, errno)
}

func validateSeccompSync(result uintptr, errno syscall.Errno) error {
	if errno != 0 {
		return fmt.Errorf("install seccomp worker filter: %w", errno)
	}
	// With TSYNC, a positive result identifies a thread that could not be
	// synchronized. Treat it as failure even though errno remains zero.
	if result != 0 {
		return fmt.Errorf("install seccomp worker filter: thread %d could not be synchronized", result)
	}
	return nil
}

func prctl(option, argument uintptr) error {
	_, _, errno := syscall.RawSyscall6(syscall.SYS_PRCTL, option, argument, 0, 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func workerSyscallFilter() []syscall.SockFilter {
	filters := make([]syscall.SockFilter, 0, 24+2*(len(commonDeniedWorkerSyscalls)+len(deniedWorkerSyscalls)))
	filters = append(filters,
		syscall.SockFilter{Code: bpfLoadWordAbsolute, K: seccompDataArchOffset},
		syscall.SockFilter{Code: bpfJumpEqual, Jt: 1, K: auditArchitecture},
		syscall.SockFilter{Code: bpfReturn, K: seccompReturnKillProcess},
		syscall.SockFilter{Code: bpfLoadWordAbsolute, K: seccompDataSyscallOffset},
		// Reject x32 and any other high syscall namespace before comparing
		// architecture-specific syscall numbers.
		syscall.SockFilter{Code: bpfJumpGreaterEqual, Jf: 1, K: firstReservedSyscallValue},
		syscall.SockFilter{Code: bpfReturn, K: seccompReturnKillProcess},
	)
	for _, number := range commonDeniedWorkerSyscalls {
		filters = append(filters,
			syscall.SockFilter{Code: bpfJumpEqual, Jf: 1, K: number},
			syscall.SockFilter{Code: bpfReturn, K: seccompReturnErrno | uint32(syscall.EPERM)},
		)
	}
	for _, number := range deniedWorkerSyscalls {
		filters = append(filters,
			syscall.SockFilter{Code: bpfJumpEqual, Jf: 1, K: number},
			syscall.SockFilter{Code: bpfReturn, K: seccompReturnErrno | uint32(syscall.EPERM)},
		)
	}
	// Modern glibc tries clone3 before clone. Returning ENOSYS forces its
	// fallback to clone, whose flags can be inspected by classic BPF below.
	filters = append(filters,
		syscall.SockFilter{Code: bpfJumpEqual, Jf: 1, K: clone3SystemCall},
		syscall.SockFilter{Code: bpfReturn, K: seccompReturnErrno | uint32(syscall.ENOSYS)},
	)

	// clone is permitted only for threads. The kernel independently enforces
	// the required CLONE_VM/CLONE_SIGHAND combination for CLONE_THREAD.
	filters = append(filters,
		syscall.SockFilter{Code: bpfJumpEqual, Jf: 7, K: cloneSystemCall},
		syscall.SockFilter{Code: bpfLoadWordAbsolute, K: seccompDataArgumentOffset + 4},
		syscall.SockFilter{Code: bpfJumpEqual, Jt: 1, K: 0},
		syscall.SockFilter{Code: bpfReturn, K: seccompReturnErrno | uint32(syscall.EPERM)},
		syscall.SockFilter{Code: bpfLoadWordAbsolute, K: seccompDataArgumentOffset},
		syscall.SockFilter{Code: bpfJumpBitsSet, Jt: 1, K: cloneThreadFlag},
		syscall.SockFilter{Code: bpfReturn, K: seccompReturnErrno | uint32(syscall.EPERM)},
		syscall.SockFilter{Code: bpfReturn, K: seccompReturnAllow},
	)

	// Go uses tgkill for in-process signaling. Restrict it to this worker's
	// thread group so native code cannot signal the controller.
	filters = append(filters,
		syscall.SockFilter{Code: bpfJumpEqual, Jf: 6, K: tgkillSystemCall},
		syscall.SockFilter{Code: bpfLoadWordAbsolute, K: seccompDataArgumentOffset + 4},
		syscall.SockFilter{Code: bpfJumpEqual, Jt: 1, K: 0},
		syscall.SockFilter{Code: bpfReturn, K: seccompReturnErrno | uint32(syscall.EPERM)},
		syscall.SockFilter{Code: bpfLoadWordAbsolute, K: seccompDataArgumentOffset},
		syscall.SockFilter{Code: bpfJumpEqual, Jt: 1, K: uint32(os.Getpid())},
		syscall.SockFilter{Code: bpfReturn, K: seccompReturnErrno | uint32(syscall.EPERM)},
		syscall.SockFilter{Code: bpfReturn, K: seccompReturnAllow},
	)
	return filters
}

// These syscalls can reopen the container filesystem, mutate shared state,
// inspect another process, create a new executable, or establish a new
// namespace. They exist on both supported production architectures.
var commonDeniedWorkerSyscalls = [...]uint32{
	uint32(unix.SYS_PRLIMIT64),
	uint32(unix.SYS_SETPRIORITY),
	uint32(unix.SYS_SCHED_SETPARAM),
	uint32(unix.SYS_SCHED_SETSCHEDULER),
	uint32(unix.SYS_SCHED_SETAFFINITY),
	uint32(unix.SYS_SCHED_SETATTR),
	uint32(unix.SYS_IOPRIO_SET),
	uint32(unix.SYS_OPENAT),
	uint32(unix.SYS_OPENAT2),
	uint32(unix.SYS_NAME_TO_HANDLE_AT),
	uint32(unix.SYS_OPEN_BY_HANDLE_AT),
	uint32(unix.SYS_EXECVE),
	uint32(unix.SYS_EXECVEAT),
	uint32(unix.SYS_UNLINKAT),
	uint32(unix.SYS_RENAMEAT),
	uint32(unix.SYS_RENAMEAT2),
	uint32(unix.SYS_MKDIRAT),
	uint32(unix.SYS_MKNODAT),
	uint32(unix.SYS_LINKAT),
	uint32(unix.SYS_SYMLINKAT),
	uint32(unix.SYS_TRUNCATE),
	uint32(unix.SYS_FTRUNCATE),
	uint32(unix.SYS_FALLOCATE),
	uint32(unix.SYS_FCHMOD),
	uint32(unix.SYS_FCHMODAT),
	uint32(unix.SYS_FCHOWN),
	uint32(unix.SYS_FCHOWNAT),
	uint32(unix.SYS_UTIMENSAT),
	uint32(unix.SYS_SETXATTR),
	uint32(unix.SYS_LSETXATTR),
	uint32(unix.SYS_FSETXATTR),
	uint32(unix.SYS_REMOVEXATTR),
	uint32(unix.SYS_LREMOVEXATTR),
	uint32(unix.SYS_FREMOVEXATTR),
	uint32(unix.SYS_PTRACE),
	uint32(unix.SYS_PROCESS_VM_READV),
	uint32(unix.SYS_PROCESS_VM_WRITEV),
	uint32(unix.SYS_PIDFD_OPEN),
	uint32(unix.SYS_PIDFD_GETFD),
	uint32(unix.SYS_UNSHARE),
	uint32(unix.SYS_SETNS),
	uint32(unix.SYS_MOUNT),
	uint32(unix.SYS_UMOUNT2),
	uint32(unix.SYS_PIVOT_ROOT),
	uint32(unix.SYS_CHROOT),
	uint32(unix.SYS_OPEN_TREE),
	uint32(unix.SYS_MOVE_MOUNT),
	uint32(unix.SYS_FSOPEN),
	uint32(unix.SYS_FSCONFIG),
	uint32(unix.SYS_FSMOUNT),
	uint32(unix.SYS_FSPICK),
	uint32(unix.SYS_MOUNT_SETATTR),
	uint32(unix.SYS_MEMFD_CREATE),
	uint32(unix.SYS_BPF),
	uint32(unix.SYS_USERFAULTFD),
	uint32(unix.SYS_PERF_EVENT_OPEN),
	uint32(unix.SYS_ADD_KEY),
	uint32(unix.SYS_REQUEST_KEY),
	uint32(unix.SYS_KEYCTL),
}
