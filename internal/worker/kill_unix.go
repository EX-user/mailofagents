//go:build !windows

package worker

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup puts the CLI (and its npm-shim grandchildren) into
// its own process group so a timeout can kill the whole tree in one signal.
func configureProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

func killTree(pid int) error {
	return syscall.Kill(-pid, syscall.SIGKILL)
}
