// Package gitx wraps the git plumbing dv needs: locating the repository,
// resolving a review scope (what is being compared against what), listing the
// files that changed, and reading blob contents. Everything shells out to the
// git binary — no libgit2, no in-process object parsing.
package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Repo is a handle on one git working copy.
type Repo struct {
	Root   string // absolute path to the working tree root
	GitDir string // absolute path to .git (or the real dir for worktrees)
}

// Open finds the repository containing dir and returns a handle to it.
func Open(dir string) (*Repo, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	root, err := gitOutput(abs, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("not a git repository: %s", abs)
	}
	gitDir, err := gitOutput(abs, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, err
	}
	return &Repo{Root: strings.TrimSpace(root), GitDir: strings.TrimSpace(gitDir)}, nil
}

// Name is the repository's directory name, used for window titles.
func (r *Repo) Name() string { return filepath.Base(r.Root) }

// run executes a git command in the repository root.
func (r *Repo) run(args ...string) (string, error) { return gitOutput(r.Root, args...) }

// runBytes is run for commands whose output is file content and must not be
// trimmed or forced through string conversion assumptions.
func (r *Repo) runBytes(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.Root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// runInput is run for the plumbing commands that take their arguments on
// stdin, which is how a long path list is passed without risking the command
// line length limit.
func (r *Repo) runInput(stdin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.Root
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, stderr.String())
	}
	return stdout.String(), nil
}

func gitOutput(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// HeadRef describes where HEAD currently points, for display in the header.
type HeadRef struct {
	Branch  string `json:"branch"`  // empty when detached
	SHA     string `json:"sha"`     // short sha
	Subject string `json:"subject"` // commit subject line
}

func (r *Repo) Head() HeadRef {
	h := HeadRef{}
	if b, err := r.run("rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		if b = strings.TrimSpace(b); b != "HEAD" {
			h.Branch = b
		}
	}
	if s, err := r.run("rev-parse", "--short", "HEAD"); err == nil {
		h.SHA = strings.TrimSpace(s)
	}
	if s, err := r.run("log", "-1", "--pretty=%s"); err == nil {
		h.Subject = strings.TrimSpace(s)
	}
	return h
}

// Version fingerprints what every scope is computed from - HEAD, the index,
// and the content of each changed or untracked file - so a client can poll it
// to learn that the diff it is showing has gone stale. It is one `git status`.
func (r *Repo) Version() (string, error) {
	// Plain status refreshes the index under index.lock; polled, that makes the
	// user's own commits fail now and then on the lock.
	out, err := r.run("--no-optional-locks", "status", "--porcelain=v2", "-z",
		"--branch", "--no-ahead-behind", "--untracked-files=all")
	if err != nil {
		return "", err
	}
	h := fnv.New64a()
	h.Write([]byte(out))
	// status says which files differ, not what they now hold: a second edit to
	// an already modified file shows only in its size and mtime.
	for _, p := range statusPaths(out) {
		if fi, err := os.Lstat(filepath.Join(r.Root, p)); err == nil {
			fmt.Fprintf(h, "\x00%d.%d", fi.Size(), fi.ModTime().UnixNano())
		}
	}
	return strconv.FormatUint(h.Sum64(), 36), nil
}

// statusPaths pulls the paths out of `git status --porcelain=v2 -z`.
func statusPaths(z string) []string {
	var paths []string
	fields := strings.Split(z, "\x00")
	for i := 0; i < len(fields); i++ {
		rec := fields[i]
		if len(rec) < 3 {
			continue
		}
		switch rec[0] {
		case '1':
			if f := strings.SplitN(rec, " ", 9); len(f) == 9 {
				paths = append(paths, f[8])
			}
		case '2':
			if f := strings.SplitN(rec, " ", 10); len(f) == 10 {
				paths = append(paths, f[9])
			}
			i++ // the rename's original path follows as a field of its own
		case 'u':
			if f := strings.SplitN(rec, " ", 11); len(f) == 11 {
				paths = append(paths, f[10])
			}
		case '?':
			paths = append(paths, rec[2:])
		}
	}
	return paths
}

// DefaultBranch guesses the repository's trunk, used by the "branch" scope. It
// prefers the remote's HEAD, then the conventional names, then gives up.
func (r *Repo) DefaultBranch() string {
	if out, err := r.run("symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if name := strings.TrimPrefix(strings.TrimSpace(out), "origin/"); name != "" {
			return name
		}
	}
	for _, cand := range []string{"main", "master", "trunk", "develop"} {
		if _, err := r.run("rev-parse", "--verify", "--quiet", "refs/heads/"+cand); err == nil {
			return cand
		}
	}
	return ""
}

// Branches lists local branches, most recently committed first, so the scope
// picker can offer them without the user typing a revspec.
func (r *Repo) Branches() []string {
	out, err := r.run("for-each-ref", "--sort=-committerdate", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return nil
	}
	var names []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			names = append(names, l)
		}
	}
	return names
}

// RecentCommits lists the last n commits on HEAD for the scope picker.
type Commit struct {
	SHA     string `json:"sha"`
	Short   string `json:"short"`
	Subject string `json:"subject"`
	Author  string `json:"author"`
	When    string `json:"when"`
}

func (r *Repo) RecentCommits(n int) []Commit {
	out, err := r.run("log", fmt.Sprintf("-%d", n), "--pretty=%H%x1f%h%x1f%s%x1f%an%x1f%ar")
	if err != nil {
		return nil
	}
	var cs []Commit
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		f := strings.Split(l, "\x1f")
		if len(f) == 5 {
			cs = append(cs, Commit{SHA: f[0], Short: f[1], Subject: f[2], Author: f[3], When: f[4]})
		}
	}
	return cs
}

// blob reads a path at a revision. A missing path is not an error: a file that
// does not exist on one side of the diff simply has no content there.
func (r *Repo) blob(rev, path string) ([]byte, bool, error) {
	if rev == "" {
		return nil, false, nil
	}
	b, err := r.runBytes("show", rev+":"+path)
	if err != nil {
		// Distinguish "path absent at this rev" from a real git failure by
		// asking whether the object exists at all.
		if _, e2 := r.run("cat-file", "-e", rev+":"+path); e2 != nil {
			return nil, false, nil
		}
		return nil, false, err
	}
	return b, true, nil
}

// worktreeFile reads a path from the working tree.
func (r *Repo) worktreeFile(path string) ([]byte, bool, error) {
	b, err := os.ReadFile(filepath.Join(r.Root, path))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return b, true, nil
}

// TrackedFiles lists every file git knows about plus untracked-but-not-ignored
// ones. It is the corpus for the symbol index and repo-wide search.
func (r *Repo) TrackedFiles() ([]string, error) {
	return r.paths("ls-files", "--cached", "--others", "--exclude-standard", "-z")
}

// SideFiles lists every file on the new side of the scope: the repository as
// the diff leaves it, which is what Code mode browses.
func (r *Repo) SideFiles(s *Scope) ([]string, error) {
	switch {
	case s.new.worktree:
		return r.TrackedFiles()
	case s.new.index:
		return r.paths("ls-files", "--cached", "-z")
	}
	return r.paths("ls-tree", "-r", "--name-only", "-z", s.new.rev)
}

// paths runs a git listing that prints NUL-separated paths.
func (r *Repo) paths(args ...string) ([]string, error) {
	out, err := r.run(args...)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, p := range strings.Split(out, "\x00") {
		// A conflicted path is listed once per merge stage, one after another.
		if p != "" && (len(files) == 0 || files[len(files)-1] != p) {
			files = append(files, p)
		}
	}
	return files, nil
}
