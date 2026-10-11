//go:build unix && !linux

package jscodec

import "syscall"

// Darwin and the BSDs do not enforce RLIMIT_DATA or RLIMIT_AS on mmap'd
// memory reliably, and reading another process's resident size needs cgo;
// the worker's own memory watchdog and the parent's deadline carry the
// hard limits there.
const rssWatch = false

func setDataLimit(int64) {}

func workerSysProcAttr() *syscall.SysProcAttr { return nil }

func processRSS(int) (int64, bool) { return 0, false }
