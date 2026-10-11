// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

//go:build linux && (amd64 || arm64)

package jscodec

import (
	"errors"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// deniedSyscalls are refused with EPERM once a worker's script is loaded.
// A codec needs none of them; the Go runtime needs none of them after
// start-up (it keeps using memory, threads, futexes, signals, pipes and
// clocks, which stay allowed). The list is a deny list rather than an
// allow list so that a Go release that adds a runtime system call does not
// kill the worker.
var deniedSyscalls = []uintptr{
	unix.SYS_SOCKET, unix.SYS_SOCKETPAIR, unix.SYS_CONNECT, unix.SYS_BIND, unix.SYS_LISTEN, unix.SYS_ACCEPT, unix.SYS_ACCEPT4,
	unix.SYS_SENDTO, unix.SYS_RECVFROM, unix.SYS_SENDMSG, unix.SYS_RECVMSG, unix.SYS_SENDMMSG, unix.SYS_RECVMMSG,
	unix.SYS_EXECVE, unix.SYS_EXECVEAT, unix.SYS_OPENAT, unix.SYS_OPENAT2, unix.SYS_OPEN_BY_HANDLE_AT, unix.SYS_NAME_TO_HANDLE_AT,
	unix.SYS_PTRACE, unix.SYS_PROCESS_VM_READV, unix.SYS_PROCESS_VM_WRITEV, unix.SYS_MOUNT, unix.SYS_UMOUNT2, unix.SYS_PIVOT_ROOT,
	unix.SYS_CHROOT, unix.SYS_UNSHARE, unix.SYS_SETNS, unix.SYS_REBOOT, unix.SYS_KEXEC_LOAD, unix.SYS_INIT_MODULE,
	unix.SYS_FINIT_MODULE, unix.SYS_DELETE_MODULE, unix.SYS_ADD_KEY, unix.SYS_REQUEST_KEY, unix.SYS_KEYCTL, unix.SYS_BPF,
	unix.SYS_PERF_EVENT_OPEN, unix.SYS_PERSONALITY, unix.SYS_USERFAULTFD, unix.SYS_SETUID, unix.SYS_SETGID, unix.SYS_SETREUID,
	unix.SYS_SETREGID, unix.SYS_SETRESUID, unix.SYS_SETRESGID, unix.SYS_MKNODAT, unix.SYS_UNLINKAT, unix.SYS_RENAMEAT,
	unix.SYS_LINKAT, unix.SYS_SYMLINKAT, unix.SYS_FCHMODAT, unix.SYS_FCHOWNAT, unix.SYS_MKDIRAT, unix.SYS_SWAPON, unix.SYS_SWAPOFF,
	unix.SYS_SETTIMEOFDAY, unix.SYS_CLOCK_SETTIME, unix.SYS_ACCT, unix.SYS_QUOTACTL, unix.SYS_IO_URING_SETUP,
}

func auditArch() (uint32, error) {
	switch runtime.GOARCH {
	case "amd64":
		return unix.AUDIT_ARCH_X86_64, nil
	case "arm64":
		return unix.AUDIT_ARCH_AARCH64, nil
	}
	return 0, errors.New("seccomp: architecture not supported")
}

// installSeccomp loads a classic BPF filter on every thread of the worker:
// a system call of another architecture kills the process; a denied one
// returns EPERM; everything else is allowed.
func installSeccomp() error {
	arch, err := auditArch()
	if err != nil {
		return err
	}
	const (
		offNr   = 0 // struct seccomp_data: nr, arch, ...
		offArch = 4
	)
	ld := func(k uint32) unix.SockFilter {
		return unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: k}
	}
	ret := func(k uint32) unix.SockFilter { return unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: k} }
	prog := []unix.SockFilter{
		ld(offArch),
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: arch, Jt: 1, Jf: 0},
		ret(unix.SECCOMP_RET_KILL_PROCESS),
		ld(offNr),
	}
	n := len(deniedSyscalls)
	for i, nr := range deniedSyscalls {
		// Jump to the EPERM return (after the remaining checks and the
		// ALLOW) when equal.
		prog = append(prog, unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: uint32(nr), Jt: uint8(n - i), Jf: 0})
	}
	prog = append(prog, ret(unix.SECCOMP_RET_ALLOW), ret(unix.SECCOMP_RET_ERRNO|uint32(unix.EPERM)))
	fprog := unix.SockFprog{Len: uint16(len(prog)), Filter: &prog[0]}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	if _, _, e := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(&fprog))); e != 0 {
		return e
	}
	return nil
}
