//go:build !windows

package main

import (
	"errors"
	"os"
	"syscall"

	"dv/internal/update"
)

// restart runs dv again in this same process - its PID, terminal and
// arguments - from whatever binary is installed at its path now.
func restart(addr string) error {
	if update.Path == "" {
		return errors.New("cannot tell where dv is installed, to run it again")
	}
	return syscall.Exec(update.Path, os.Args, append(os.Environ(), restartEnv+"="+addr))
}
