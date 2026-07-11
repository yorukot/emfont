//go:build linux && arm64

package processsecurity

const (
	auditArchitecture = 0xc00000b7
	seccompSystemCall = 277
	cloneSystemCall   = 220
	clone3SystemCall  = 435
	tgkillSystemCall  = 131
)

var deniedWorkerSyscalls = [...]uint32{
	129, // kill
	130, // tkill
	138, // rt_sigqueueinfo
	154, // setpgid
	157, // setsid
	198, // socket
	199, // socketpair
	200, // bind
	201, // listen
	202, // accept
	203, // connect
	204, // getsockname
	205, // getpeername
	206, // sendto
	207, // recvfrom
	208, // setsockopt
	209, // getsockopt
	210, // shutdown
	211, // sendmsg
	212, // recvmsg
	240, // rt_tgsigqueueinfo
	242, // accept4
	243, // recvmmsg
	269, // sendmmsg
	424, // pidfd_send_signal
	425, // io_uring_setup
	426, // io_uring_enter
	427, // io_uring_register
}
