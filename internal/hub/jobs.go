package hub

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"
)

// job is work the hub does on its own - a clone, making a worktree and setting
// it up, deleting one - so a page closed or a phone put away does not stop it.
// It stays listed until it succeeds, or until its failure is dismissed.
type job struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"`  // clone, worktree, setup, remove, or discard (a task's)
	Title   string    `json:"title"` // what is cloned, or the worktree's branch or name
	Path    string    `json:"path"`
	Place   string    `json:"place"`
	Of      string    `json:"of,omitempty"`    // the repository's slug, for a worktree's
	Slug    string    `json:"slug,omitempty"`  // the worktree's, once it is on the list
	Force   bool      `json:"force,omitempty"` // a delete that loses uncommitted changes
	Started time.Time `json:"started"`

	// Guarded by the hub's mu.
	Step     string `json:"step,omitempty"`
	Progress string `json:"progress,omitempty"`
	Error    string `json:"error,omitempty"`
	// Retry is what a failure can be tried again as: setup, remove (without
	// the teardown) or force (losing the changes).
	Retry string `json:"retry,omitempty"`

	cancel context.CancelFunc
	ended  chan struct{} // closed once the work is over, err then its outcome
	err    error
}

// view is j as the page gets it. Callers hold h.mu.
func (j *job) view() job {
	v := *j
	v.cancel, v.ended, v.err = nil, nil, nil
	return v
}

// start lists j and runs work for it. work's error is j's failure, and retry
// what the page may offer to do about it.
func (h *Hub) start(j *job, work func(ctx context.Context) (retry string, err error)) job {
	ctx, cancel := context.WithCancel(context.Background())
	j.ID, j.Started, j.cancel, j.ended = newID(), time.Now().UTC(), cancel, make(chan struct{})
	h.mu.Lock()
	h.jobs[j.ID] = j
	v := j.view()
	h.mu.Unlock()
	go func() {
		retry, err := work(ctx)
		h.mu.Lock()
		defer h.mu.Unlock()
		j.err = cmp.Or(err, ctx.Err())
		defer close(j.ended)
		switch {
		case ctx.Err() != nil:
			delete(h.jobs, j.ID)
		case err != nil:
			j.Error, j.Retry, j.Progress = err.Error(), retry, ""
		default:
			delete(h.jobs, j.ID)
		}
	}()
	return v
}

func (h *Hub) step(j *job, step string) {
	h.mu.Lock()
	j.Step, j.Progress = step, ""
	h.mu.Unlock()
}

// busy finds a job still at work on a folder, by path.
func (h *Hub) busy(path string) *job {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, j := range h.jobs {
		if j.Path == path && j.Error == "" {
			return j
		}
	}
	return nil
}

// forget drops the failed jobs on a folder, which trying again replaces.
func (h *Hub) forget(path string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, j := range h.jobs {
		if j.Path == path && j.Error != "" {
			delete(h.jobs, id)
		}
	}
}

func (h *Hub) jobViews() []job {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]job, 0, len(h.jobs))
	for _, j := range h.jobs {
		out = append(out, j.view())
	}
	slices.SortFunc(out, func(a, b job) int { return a.Started.Compare(b.Started) })
	return out
}

// tailLines is how much of what a command said a failure keeps.
const tailLines = 40

// run runs cmd for j, its output becoming j's progress line by line. Its error
// carries the last of that output, which is where a failure says why.
func (h *Hub) run(j *job, cmd *exec.Cmd) error {
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	ownGroup(cmd)
	cmd.WaitDelay = 10 * time.Second
	if err := cmd.Start(); err != nil {
		return err
	}
	var tail []string
	sc := bufio.NewScanner(out)
	sc.Buffer(nil, 1<<20)
	sc.Split(scanProgress)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if !progressLine.MatchString(line) {
			if tail = append(tail, line); len(tail) > tailLines {
				tail = tail[1:]
			}
		}
		h.mu.Lock()
		j.Progress = line
		h.mu.Unlock()
	}
	// A line too long to scan stops the scanner, not the command, which would
	// block once the pipe filled.
	if sc.Err() != nil {
		io.Copy(io.Discard, out)
	}
	if err := cmd.Wait(); err != nil {
		if len(tail) == 0 {
			return err
		}
		return fmt.Errorf("%s\n(%v)", strings.Join(tail, "\n"), err)
	}
	return nil
}

// hook runs one of a repository's hook scripts in a worktree of it, with sh
// stopping at the first command that fails.
func (h *Hub) hook(ctx context.Context, j *job, script, worktree, repo, branch string) error {
	sh, err := exec.LookPath("sh")
	if err != nil {
		return errors.New("hooks run with sh, which is not on PATH")
	}
	cmd := exec.CommandContext(ctx, sh, "-e", "-c", script)
	cmd.Dir = worktree
	cmd.Env = append(os.Environ(), "DV_REPO="+repo, "DV_WORKTREE="+worktree, "DV_BRANCH="+branch)
	return h.run(j, cmd)
}

// progressLine is git's running counts, which it rewrites in place, and the
// line a clone starts with: neither says why anything failed.
var progressLine = regexp.MustCompile(`^((remote: )?[A-Z][a-z]+( [a-z]+)*: +\d+%|Cloning into )`)

// scanProgress splits on carriage returns too, which is how git redraws a line.
func scanProgress(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// handleDismissJob stops a job at work, or lets go of one that failed.
func (h *Hub) handleDismissJob(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	j := h.jobs[r.PathValue("id")]
	if j != nil && j.Error != "" {
		delete(h.jobs, j.ID)
	}
	h.mu.Unlock()
	if j == nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("no such job"))
		return
	}
	// One at work leaves the list once what it ran has stopped.
	j.cancel()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func newID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}
