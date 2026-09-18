package main

import (
	"errors"
	"os"
	"os/exec"

	"dv/internal/update"
)

// restart starts dv again from whatever binary is installed at its path now.
// Windows cannot run another program in a process, so this one then exits.
func restart(addr string) error {
	if update.Path == "" {
		return errors.New("cannot tell where dv is installed, to run it again")
	}
	cmd := exec.Command(update.Path, os.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(append(os.Environ(), update.Carry...), restartEnv+"="+addr)
	return cmd.Start()
}
