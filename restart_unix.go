//go:build !windows

package main

import (
	"os"
	"syscall"
)

// restart runs dv again in this same process - its PID, terminal and
// arguments - from whatever binary is installed at its path now.
func restart(addr string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(exe, os.Args, append(os.Environ(), restartEnv+"="+addr))
}
