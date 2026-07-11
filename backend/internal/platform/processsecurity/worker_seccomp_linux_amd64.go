//go:build linux && amd64

package processsecurity

const (
	auditArchitecture = 0xc000003e
	seccompSystemCall = 317
	cloneSystemCall   = 56
	clone3SystemCall  = 435
	tgkillSystemCall  = 234
)

var deniedWorkerSyscalls = [...]uint32{
	2,   // open
	41,  // socket
	42,  // connect
	43,  // accept
	44,  // sendto
	45,  // recvfrom
	46,  // sendmsg
	47,  // recvmsg
	48,  // shutdown
	49,  // bind
	50,  // listen
	51,  // getsockname
	52,  // getpeername
	53,  // socketpair
	54,  // setsockopt
	55,  // getsockopt
	57,  // fork
	58,  // vfork
	62,  // kill
	82,  // rename
	83,  // mkdir
	84,  // rmdir
	85,  // creat
	86,  // link
	87,  // unlink
	88,  // symlink
	90,  // chmod
	92,  // chown
	94,  // lchown
	109, // setpgid
	112, // setsid
	129, // rt_sigqueueinfo
	132, // utime
	133, // mknod
	200, // tkill
	235, // utimes
	261, // futimesat
	288, // accept4
	297, // rt_tgsigqueueinfo
	299, // recvmmsg
	307, // sendmmsg
	424, // pidfd_send_signal
	425, // io_uring_setup
	426, // io_uring_enter
	427, // io_uring_register
}
