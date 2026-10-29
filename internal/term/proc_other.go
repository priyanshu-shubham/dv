//go:build !linux && !darwin && !windows

package term

func workingDir(pid int) string { return "" }
func argsOf(pid int) []string   { return nil }
func foregroundOf(pid int) int  { return 0 }
