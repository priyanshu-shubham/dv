//go:build !windows

package agent

import (
	"cmp"
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// ownGroup puts the process in a group of its own, so stopping it takes the
// commands it started with it. A new session also leaves it without dv's
// terminal, so hooks that ring the session's terminal don't ring dv's.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// shellCmd runs a line as the reader's own shell would.
func shellCmd(line string) *exec.Cmd {
	return exec.Command(cmp.Or(os.Getenv("SHELL"), "/bin/sh"), "-c", line)
}

func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
