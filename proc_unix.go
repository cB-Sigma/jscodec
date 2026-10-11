//go:build unix

package jscodec

import (
	"os"
	"os/signal"
	"syscall"
	"time"
)

const processSupported = true

// setCPULimit caps the worker's lifetime CPU time: SIGXCPU at secs (the
// worker exits with exitCPU), SIGKILL from the kernel five seconds later.
func setCPULimit(secs uint64) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGXCPU)
	go func() {
		<-ch
		os.Stderr.WriteString("codec worker: CPU time limit reached\n")
		os.Exit(exitCPU)
	}()
	_ = syscall.Setrlimit(syscall.RLIMIT_CPU, &syscall.Rlimit{Cur: secs, Max: secs + 5})
}

// cpuTime is the worker's user and system CPU time so far.
func cpuTime() time.Duration {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

func cpuSignaled(st *os.ProcessState) bool {
	ws, ok := st.Sys().(syscall.WaitStatus)
	return ok && ws.Signaled() && ws.Signal() == syscall.SIGXCPU
}
