//go:build !linux

package jscodec_test

import (
	"os/exec"
	"strconv"
)

// processEnv reads another process's environment with ps (Darwin, BSD).
func processEnv(pid int) (string, bool) {
	out, err := exec.Command("ps", "eww", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	return string(out), err == nil
}
