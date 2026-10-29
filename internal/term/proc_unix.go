//go:build !windows

package term

import (
	"cmp"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"
)

var supported error

func userShell() string { return cmp.Or(os.Getenv("SHELL"), "/bin/sh") }

func shellCommand(shell string) *exec.Cmd {
	cmd := exec.Command(shell)
	// As Terminal.app does: a Mac's PATH is put together by the login profile.
	if runtime.GOOS == "darwin" {
		cmd.Args[0] = "-" + filepath.Base(shell)
	}
	return cmd
}

// foreground is the terminal's foreground process group, 0 if it can't tell.
func foreground(f *os.File) int {
	rc, err := f.SyscallConn()
	if err != nil {
		return 0
	}
	var pgrp int32
	var errno syscall.Errno
	// Control rather than Fd, which would turn the file blocking.
	rc.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCGPGRP, uintptr(unsafe.Pointer(&pgrp)))
	})
	if errno != 0 {
		return 0
	}
	return int(pgrp)
}

func hangUp(pid int)    { syscall.Kill(-pid, syscall.SIGHUP) }
func killGroup(pid int) { syscall.Kill(-pid, syscall.SIGKILL) }

func winched(pgrp int) {
	if pgrp > 0 {
		syscall.Kill(-pgrp, syscall.SIGWINCH)
	}
}
