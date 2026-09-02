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

// interruptTree delivers SIGINT to the process group: every CLI treats it
// as a user abort — the turn ends and the process exits on its own, which
// is far quieter than a SIGKILL mid-tool. Escalation to SIGKILL is the
// caller's job (exec's WaitDelay).
func interruptTree(pid int) error {
	return syscall.Kill(-pid, syscall.SIGINT)
}
