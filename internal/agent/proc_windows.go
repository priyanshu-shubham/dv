package agent

import (
	"os"
	"os/exec"
)

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	// On Windows finding a process opens it, which fails once it has exited.
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	p.Release()
	return true
}

func ownGroup(cmd *exec.Cmd) {}

func shellCmd(line string) *exec.Cmd {
	return exec.Command("cmd", "/C", line)
}

func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		cmd.Process.Kill()
	}
}
