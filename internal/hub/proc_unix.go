//go:build !windows

package hub

import (
	"os/exec"
	"syscall"
)

// ownGroup puts cmd in a process group of its own, so stopping it interrupts
// what it started too: a hook's package manager, a clone's ssh.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGINT) }
}
