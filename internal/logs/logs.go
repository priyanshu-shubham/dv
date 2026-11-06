// Package logs keeps what dv prints to its terminal, so a page can show it:
// a hub's, or a dv's started where its terminal is out of reach.
package logs

import (
	"bytes"
	"io"
	"log"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	maxLines = 2000
	maxLine  = 4 << 10
)

// Line is one line printed, numbered from 1 in the order it was.
type Line struct {
	N    int       `json:"n"`
	At   time.Time `json:"at"`
	Err  bool      `json:"err,omitempty"`
	Text string    `json:"text"`
}

var (
	mu    sync.Mutex
	lines []Line
	next  = 1

	stdout, stderr = os.Stdout, os.Stderr
	pipes          []*os.File
	pumping        sync.WaitGroup
)

// colour is the terminal's styling, which the page has no use for.
var colour = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// Capture has everything written to os.Stdout and os.Stderr from now on,
// fmt's and log's, go on to the terminal and be kept as well. The process's
// own descriptors stay the terminal's, so what it runs, or execs into on a
// restart, writes there as before.
func Capture() {
	for _, s := range []struct {
		f   **os.File
		err bool
	}{{&os.Stdout, false}, {&os.Stderr, true}} {
		r, w, err := os.Pipe()
		if err != nil {
			continue
		}
		real := *s.f
		*s.f = w
		pipes = append(pipes, w)
		pumping.Add(1)
		go pump(r, real, s.err)
	}
	log.SetOutput(os.Stderr)
}

// Release gives os.Stdout and os.Stderr back to the terminal once all that
// was written to them is there, before dv exits or runs itself again.
func Release() {
	if pipes == nil {
		return
	}
	os.Stdout, os.Stderr = stdout, stderr
	log.SetOutput(stderr)
	for _, w := range pipes {
		w.Close()
	}
	pipes = nil
	pumping.Wait()
}

// Terminal is the terminal's own stdout and stderr, for a process dv starts
// that outlives it.
func Terminal() (out, err *os.File) { return stdout, stderr }

func pump(r io.Reader, to io.Writer, isErr bool) {
	defer pumping.Done()
	var part []byte
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			to.Write(buf[:n])
			part = append(part, buf[:n]...)
			for {
				i := bytes.IndexByte(part, '\n')
				if i < 0 {
					break
				}
				keep(string(part[:i]), isErr)
				part = part[i+1:]
			}
			// A line that never ends is kept in pieces rather than held whole.
			if len(part) > maxLine {
				keep(string(part), isErr)
				part = nil
			}
		}
		if err != nil {
			if len(part) > 0 {
				keep(string(part), isErr)
			}
			return
		}
	}
}

func keep(text string, isErr bool) {
	text = strings.TrimRight(colour.ReplaceAllString(text, ""), " \t\r")
	// The terminal's spacing is not worth a timestamped line of its own.
	if text == "" {
		return
	}
	if len(text) > maxLine {
		text = text[:maxLine]
	}
	mu.Lock()
	defer mu.Unlock()
	lines = append(lines, Line{N: next, At: time.Now(), Err: isErr, Text: text})
	next++
	// Let go of a quarter at a time, not a line at a time.
	if len(lines) > maxLines+maxLines/4 {
		lines = append(lines[:0:0], lines[len(lines)-maxLines:]...)
	}
}

// Since is the lines kept after line n, and the number the next will take.
func Since(n int) ([]Line, int) {
	mu.Lock()
	defer mu.Unlock()
	out := []Line{}
	for _, l := range lines {
		if l.N > n {
			out = append(out, l)
		}
	}
	return out, next
}
