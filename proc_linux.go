//go:build linux

package jscodec

import (
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// rssWatch: the parent samples a worker's resident size from /proc. The
// race detector's shadow memory would count too, so not under -race.
const rssWatch = !raceEnabled

// setDataLimit sets RLIMIT_DATA, which Linux (4.7 and later) applies to
// the private writable mappings the Go heap lives in; past it the runtime
// fails to grow the heap and the worker dies with "out of memory". The
// race detector maps large shadow regions, so not under -race.
func setDataLimit(n int64) {
	if raceEnabled || n <= 0 {
		return
	}
	_ = syscall.Setrlimit(syscall.RLIMIT_DATA, &syscall.Rlimit{Cur: uint64(n), Max: uint64(n)})
}

// workerSysProcAttr kills the worker when the thread that started it
// exits, so workers never outlive the server.
func workerSysProcAttr(namespaces bool) *syscall.SysProcAttr {
	a := &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if namespaces {
		// New user, network, mount, IPC and UTS namespaces; inside, the
		// worker is root of its own user namespace mapped to our user.
		a.Cloneflags = syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET | syscall.CLONE_NEWNS | syscall.CLONE_NEWIPC | syscall.CLONE_NEWUTS
		a.UidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}}
		a.GidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}}
	}
	return a
}

// peakRSS is this process's peak resident size in bytes (Linux reports
// kilobytes).
func peakRSS() int64 {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return 0
	}
	return int64(ru.Maxrss) * 1024
}

func processRSS(pid int) (int64, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/statm")
	if err != nil {
		return 0, false
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return 0, false
	}
	pages, err := strconv.ParseInt(f[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return pages * int64(os.Getpagesize()), true
}

// sandboxProbe: bit 0 socket() refused, bit 1 open refused, bit 2 only
// no route out of the network namespace.
func sandboxProbe() int {
	n := 0
	if fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0); err == syscall.EPERM {
		n |= 1
	} else if err == nil {
		_ = syscall.Close(fd)
	}
	if f, err := os.Open("/proc/self/status"); err != nil {
		n |= 2
	} else {
		_ = f.Close()
	}
	// No route anywhere: a UDP "connection" to a documentation address
	// fails at once in an empty network namespace.
	if c, err := net.Dial("udp4", "192.0.2.1:9"); err != nil {
		n |= 4
	} else {
		_ = c.Close()
	}
	return n
}
