package term

import (
	"strings"
	"unicode/utf8"
)

// plainTail is the last n lines of what a terminal was sent, as plainLines
// reads it.
func plainTail(p []byte, n int) string {
	lines := plainLines(p)
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}

// commandTail is the last n lines a command printed, given all that came
// from the Enter that ran it - its line's end first - to the prompt after it,
// prompt lines long. A full-screen program's leaves nothing to quote.
func commandTail(p []byte, prompt, n int) string {
	if lastOf(p, altOn) >= 0 {
		return ""
	}
	lines := plainLines(p)
	if len(lines) <= 1+prompt {
		return ""
	}
	lines = trimBlank(lines[1 : len(lines)-prompt])
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}

// promptLines is how many lines a prompt takes, given what the shell printed
// from its start to where the reader pressed Enter.
func promptLines(p []byte) int {
	lines := plainLines(p)
	for len(lines) > 0 && lines[0] == "" {
		lines = lines[1:]
	}
	return len(lines)
}

func trimBlank(lines []string) []string {
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// plainLines is what a terminal was sent, as it would read on screen: escape
// sequences dropped, a line drawn over after a carriage return (a progress
// bar) as it was left, blank lines at the end left off.
func plainLines(p []byte) []string {
	var lines []string
	var line []byte
	end := func() {
		lines = append(lines, strings.TrimRight(strings.ToValidUTF8(string(line), ""), " \t"))
		line = line[:0]
	}
	for i := 0; i < len(p); i++ {
		switch c := p[i]; {
		case c == 0x1b:
			i = skipEscape(p, i)
		case c == '\n':
			end()
		case c == '\r':
			// Drawn over unless the line ends here.
			if i+1 < len(p) && p[i+1] != '\n' && p[i+1] != '\r' {
				line = line[:0]
			}
		case c == '\b':
			if len(line) > 0 {
				_, size := utf8.DecodeLastRune(line)
				line = line[:len(line)-size]
			}
		case c == '\t' || c >= 0x20 && c != 0x7f:
			line = append(line, c)
		}
	}
	end()
	return trimBlank(lines)
}

// skipEscape is where the escape sequence starting at p[i] ends.
func skipEscape(p []byte, i int) int {
	if i+1 >= len(p) {
		return i
	}
	switch p[i+1] {
	case '[':
		for j := i + 2; j < len(p); j++ {
			if p[j] >= 0x40 && p[j] <= 0x7e {
				return j
			}
		}
	case ']', 'P', 'X', '^', '_':
		// To BEL or ST.
		for j := i + 2; j < len(p); j++ {
			if p[j] == 0x07 {
				return j
			}
			if p[j] == 0x1b && j+1 < len(p) && p[j+1] == '\\' {
				return j + 1
			}
		}
	default:
		// ESC ( B and the like: intermediates, then a final byte.
		j := i + 1
		for j < len(p)-1 && p[j] >= 0x20 && p[j] <= 0x2f {
			j++
		}
		return j
	}
	return len(p) - 1
}
