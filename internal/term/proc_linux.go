package term

import (
	"bytes"
	"os"
	"strconv"
	"strings"
)

func workingDir(pid int) string {
	dir, _ := os.Readlink("/proc/" + strconv.Itoa(pid) + "/cwd")
	return dir
}

// foregroundOf is the foreground process group of pid's terminal, 0 if it
// can't tell: field 8 of its stat, after the name in brackets, which can
// itself hold spaces and brackets.
func foregroundOf(pid int) int {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	_, rest, _ := strings.Cut(string(b[bytes.LastIndexByte(b, ')')+1:]), " ")
	f := strings.Fields(rest)
	if len(f) < 6 {
		return 0
	}
	n, _ := strconv.Atoi(f[5])
	return max(n, 0)
}

func argsOf(pid int) []string {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return nil
	}
	args := strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
	// A program that set its title, as npm does, has it all in the first.
	if len(args) == 1 {
		args = strings.Fields(args[0])
	}
	return args
}
