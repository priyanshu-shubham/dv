package gitx

import (
	"fmt"
	"strings"
)

// Focus narrows a rendered diff to the lines a reviewer selected. Side is
// "old" or "new"; Start and End are 1-based and inclusive.
type Focus struct {
	Side  string
	Start int
	End   int
}

// RenderUnified turns a FileDiff back into unified-diff text. The viewer never
// needs this — it renders from the ops directly — but it is exactly the form to
// hand a model when asking what a change does.
func RenderUnified(fd *FileDiff, context int, focus *Focus) string {
	if fd.Binary || fd.TooLarge {
		return fmt.Sprintf("(%s: contents not shown)", fd.Path)
	}

	type entry struct {
		t    byte // 'e', 'd', 'i'
		o, n int  // 0-based indices, -1 when absent
	}
	var lines []entry
	for _, op := range fd.Ops {
		switch op.Kind {
		case OpEqual:
			for i := 0; i < op.OldLen; i++ {
				lines = append(lines, entry{'e', op.OldStart + i, op.NewStart + i})
			}
		case OpDelete:
			for i := 0; i < op.OldLen; i++ {
				lines = append(lines, entry{'d', op.OldStart + i, -1})
			}
		case OpInsert:
			for i := 0; i < op.NewLen; i++ {
				lines = append(lines, entry{'i', -1, op.NewStart + i})
			}
		}
	}

	keep := make([]bool, len(lines))
	if focus != nil {
		// A focused ask shows the selected span plus context, changed or not,
		// so the model sees exactly what the reviewer was looking at.
		lo, hi := focus.Start-context, focus.End+context
		for i, l := range lines {
			no := l.n + 1
			if focus.Side == "old" {
				no = l.o + 1
			}
			if (l.o >= 0 || l.n >= 0) && no >= lo && no <= hi {
				keep[i] = true
			}
		}
	} else {
		for i, l := range lines {
			if l.t == 'e' {
				continue
			}
			for j := max(0, i-context); j <= min(len(lines)-1, i+context); j++ {
				keep[j] = true
			}
		}
	}

	var b strings.Builder
	old := fd.Path
	if fd.OldPath != "" {
		old = fd.OldPath
	}
	fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n", old, fd.Path)

	for i := 0; i < len(lines); {
		if !keep[i] {
			i++
			continue
		}
		start := i
		for i < len(lines) && keep[i] {
			i++
		}
		hunk := lines[start:i]

		oldStart, newStart, oldCount, newCount := 0, 0, 0, 0
		for _, l := range hunk {
			if l.o >= 0 {
				if oldCount == 0 {
					oldStart = l.o + 1
				}
				oldCount++
			}
			if l.n >= 0 {
				if newCount == 0 {
					newStart = l.n + 1
				}
				newCount++
			}
		}
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
		for _, l := range hunk {
			switch l.t {
			case 'e':
				fmt.Fprintf(&b, " %s\n", fd.OldLines[l.o])
			case 'd':
				fmt.Fprintf(&b, "-%s\n", fd.OldLines[l.o])
			case 'i':
				fmt.Fprintf(&b, "+%s\n", fd.NewLines[l.n])
			}
		}
	}
	return b.String()
}
