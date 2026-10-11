//go:build unix && !linux

package jscodec

import (
	"errors"
	"runtime"
	"syscall"
)

// Darwin and the BSDs do not enforce RLIMIT_DATA or RLIMIT_AS on mmap'd
// memory reliably, and reading another process's resident size needs cgo;
// the worker's own memory watchdog and the parent's deadline carry the
// hard limits there.
const rssWatch = false

func setDataLimit(int64) {}

func workerSysProcAttr(bool) *syscall.SysProcAttr { return nil }

// peakRSS is this process's peak resident size in bytes (Darwin reports
// bytes, the BSDs kilobytes).
func peakRSS() int64 {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return 0
	}
	if runtime.GOOS == "darwin" {
		return int64(ru.Maxrss)
	}
	return int64(ru.Maxrss) * 1024
}

func installSeccomp() error { return errors.New("seccomp is available on Linux only") }

func processRSS(int) (int64, bool) { return 0, false }

func sandboxProbe() int { return 0 }
