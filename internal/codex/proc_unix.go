//go:build !windows

package codex

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// ownGroup puts the process in a group of its own, so stopping it takes the
// commands it started with it. A new session also leaves it without dv's
// terminal, so hooks that ring the session's terminal don't ring dv's.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// heldElsewhere reports whether some process holds a thread's writer lock.
// Codex takes the lock without waiting, so the probe is only made on a file
// that exists - a thread something has loaded, or a stale lock - where nothing
// is about to take it.
func heldElsewhere(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
	if err == nil {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return false
	}
	return errors.Is(err, syscall.EWOULDBLOCK)
}
