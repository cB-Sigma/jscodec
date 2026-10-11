//go:build !unix

package jscodec

import (
	"errors"
	"os"
	"syscall"
	"time"
)

const (
	processSupported = false
	rssWatch         = false
)

func setCPULimit(uint64)                          {}
func setDataLimit(int64)                          {}
func cpuTime() time.Duration                      { return 0 }
func cpuSignaled(*os.ProcessState) bool           { return false }
func workerSysProcAttr(bool) *syscall.SysProcAttr { return nil }
func peakRSS() int64                              { return 0 }
func installSeccomp() error                       { return errors.New("seccomp is available on Linux only") }
func processRSS(int) (int64, bool)                { return 0, false }
func sandboxProbe() int                           { return 0 }
