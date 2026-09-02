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

// interruptTree: Windows has no cross-process console-ctrl delivery for
// other process groups, so interruption degrades to the hard kill.
func interruptTree(pid int) error {
	return killTree(pid)
}
