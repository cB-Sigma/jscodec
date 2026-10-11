package jscodec_test

import (
	"os"
	"strconv"
)

func processEnv(pid int) (string, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	return string(b), err == nil
}
