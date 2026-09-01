//go:build windows

package worker

import (
	"os/exec"
	"strconv"
)

// configureProcessGroup: Windows needs no process group — taskkill /T walks
// the whole tree.
func configureProcessGroup(cmd *exec.Cmd) {}

func killTree(pid int) error {
	return exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
}
