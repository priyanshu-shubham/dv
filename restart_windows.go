package main

import (
	"os"
	"os/exec"
)

// restart starts dv again from whatever binary is installed at its path now.
// Windows cannot run another program in a process, so this one then exits.
func restart(addr string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), restartEnv+"="+addr)
	return cmd.Start()
}
