package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// QuietEnv is the environment for git with nobody at a terminal to type a
// password or accept a host key: without it git would wait on one forever.
func QuietEnv() []string {
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if os.Getenv("GIT_SSH_COMMAND") == "" && os.Getenv("GIT_SSH") == "" {
		env = append(env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
	}
	return env
}

// Pull fast-forwards the checked-out branch, or with main the trunk, and
// says which branch that was and by how many commits it moved.
func (r *Repo) Pull(main bool) (string, int, error) {
	if !r.IsGit() {
		return "", 0, errors.New("not a git repository")
	}
	branch := r.Head().Branch
	if main {
		if branch = r.DefaultBranch(); branch == "" || !r.HasBranch(branch) {
			return "", 0, errors.New("there is no main branch here")
		}
	} else if branch == "" {
		return "", 0, errors.New("HEAD is on no branch")
	}
	n, err := r.FastForward(branch)
	return branch, n, err
}

// FastForward brings a local branch up to what it tracks, fetching that first,
// and only ever as a fast-forward. It says how many commits it moved by, 0 for
// one already there. A branch checked out somewhere is merged in that
// checkout, where git refuses to overwrite uncommitted changes; any other is
// moved by a local fetch, which refuses one being rebased.
func (r *Repo) FastForward(branch string) (int, error) {
	remote, _ := r.run("config", "--get", "branch."+branch+".remote")
	remote = strings.TrimSpace(remote)
	up, err := r.run("rev-parse", "--symbolic-full-name", branch+"@{upstream}")
	if remote == "" || err != nil {
		return 0, fmt.Errorf("%s tracks no remote branch", branch)
	}
	up = strings.TrimSpace(up)
	if remote != "." {
		if _, err := gitIn(r.Root, 2*time.Minute, "fetch", "--quiet", "--", remote); err != nil {
			return 0, fmt.Errorf("could not fetch %s: %w", remote, err)
		}
	}

	head := "refs/heads/" + branch
	if r.isAncestor(up, head) {
		return 0, nil
	}
	if !r.isAncestor(head, up) {
		return 0, fmt.Errorf("%s and %s have diverged; pull it yourself", branch, strings.TrimPrefix(up, "refs/remotes/"))
	}
	count, _ := r.run("rev-list", "--count", head+".."+up)
	n, _ := strconv.Atoi(strings.TrimSpace(count))

	if dir := r.checkedOut(head); dir != "" {
		_, err = gitIn(dir, time.Minute, "merge", "--ff-only", "--quiet", up)
	} else {
		_, err = gitIn(r.Root, time.Minute, "fetch", "--quiet", ".", up+":"+head)
	}
	if err != nil {
		return 0, err
	}
	return n, nil
}

func (r *Repo) isAncestor(a, b string) bool {
	_, err := r.run("merge-base", "--is-ancestor", a, b)
	return err == nil
}

// checkedOut is the worktree ref is checked out in, "" for none.
func (r *Repo) checkedOut(ref string) string {
	out, err := r.run("worktree", "list", "--porcelain")
	if err != nil {
		return ""
	}
	var dir string
	for _, l := range strings.Split(out, "\n") {
		if p, ok := strings.CutPrefix(l, "worktree "); ok {
			dir = p
		} else if l == "branch "+ref {
			return dir
		}
	}
	return ""
}

// gitIn runs git for the reader to see its failure: the error is what git
// said, without the command line.
func gitIn(dir string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir, cmd.Env = dir, QuietEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", errors.New("git took too long")
		}
		msg := strings.TrimSpace(stderr.String())
		for _, p := range []string{"error: ", "fatal: "} {
			msg = strings.TrimPrefix(msg, p)
		}
		if msg != "" {
			return "", errors.New(msg)
		}
		return "", err
	}
	return stdout.String(), nil
}
