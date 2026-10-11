//go:build linux

package jscodec

import (
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
func workerSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
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
