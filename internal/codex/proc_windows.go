package codex

import (
	"os"
	"os/exec"
)

func ownGroup(cmd *exec.Cmd) {}

func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		cmd.Process.Kill()
	}
}

// heldElsewhere is the lock file being there: Codex removes it when it lets
// the thread go, and a probe of its own would need the file opened for locking.
func heldElsewhere(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
