package term

import (
	"errors"
	"os"
	"os/exec"
)

var supported = errors.New("dv cannot run a terminal on Windows yet")

func userShell() string                   { return "cmd" }
func shellCommand(shell string) *exec.Cmd { return exec.Command(shell) }
func foreground(f *os.File) int           { return 0 }
func workingDir(pid int) string           { return "" }
func argsOf(pid int) []string             { return nil }
func foregroundOf(pid int) int            { return 0 }
func hangUp(pid int)                      {}
func killGroup(pid int)                   {}
func winched(pgrp int)                    {}
