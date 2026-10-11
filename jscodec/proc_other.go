//go:build !unix

package jscodec

import (
	"os"
	"syscall"
	"time"
)

const (
	processSupported = false
	rssWatch         = false
)

func setCPULimit(uint64)                      {}
func setDataLimit(int64)                      {}
func cpuTime() time.Duration                  { return 0 }
func cpuSignaled(*os.ProcessState) bool       { return false }
func workerSysProcAttr() *syscall.SysProcAttr { return nil }
func processRSS(int) (int64, bool)            { return 0, false }
