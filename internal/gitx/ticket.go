package gitx

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

// AddWorktree checks branch out at path, making the branch from base if it
// does not exist yet.
func (r *Repo) AddWorktree(path, branch, base string) error {
	if err := r.ValidBranch(branch); err != nil {
		return err
	}
	args := []string{"worktree", "add", "--", path, branch}
	if _, err := gitIn(r.Root, time.Minute, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err != nil {
		args = []string{"worktree", "add", "-b", branch, "--", path, base}
	}
	_, err := gitIn(r.Root, 2*time.Minute, args...)
	return err
}

// RefExists says whether ref names a commit here.
func (r *Repo) RefExists(ref string) bool {
	_, err := gitIn(r.Root, 10*time.Second, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	return err == nil
}

// ChangedSince lists the files the branch checked out in dir changes since
// it left base, as `git diff base...HEAD` has them.
func ChangedSince(dir, base string) ([]string, error) {
	out, err := gitIn(dir, time.Minute, "diff", "--name-only", "--no-renames", base+"...HEAD")
	if err != nil {
		return nil, err
	}
	return strings.Fields(strings.TrimSpace(string(out))), nil
}

// HasFile says whether path is a file at rev in the repository at dir.
func HasFile(dir, rev, path string) bool {
	_, err := gitIn(dir, 10*time.Second, "cat-file", "-e", rev+":"+path)
	return err == nil
}

// RemoveWorktree takes the worktree at path away, changes and all, and then
// its branch, merged or not.
func (r *Repo) RemoveWorktree(path, branch string) error {
	if _, err := os.Stat(path); err == nil {
		if _, err := gitIn(r.Root, time.Minute, "worktree", "remove", "--force", "--", path); err != nil {
			return err
		}
	}
	if branch == "" {
		return nil
	}
	if _, err := gitIn(r.Root, time.Minute, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err != nil {
		return nil
	}
	_, err := gitIn(r.Root, time.Minute, "branch", "-D", "--", branch)
	return err
}

// Push sends the branch checked out in dir to origin and tracks it there.
func Push(dir, branch string) error {
	_, err := gitIn(dir, 3*time.Minute, "push", "--set-upstream", "origin", "HEAD:refs/heads/"+branch)
	return err
}

// CreatePR opens a pull request of branch into base and returns its number.
func (r *Repo) CreatePR(branch, base, title, body string) (int, string, error) {
	out, err := runGHIn(r.Root, time.Minute, body, "pr", "create", "--head", branch, "--base", base, "--title", title, "--body-file", "-")
	if err != nil {
		return 0, "", err
	}
	return prNumber(string(out))
}

// EditPR gives pull request n a new title and description.
func (r *Repo) EditPR(n int, title, body string) error {
	_, err := runGHIn(r.Root, time.Minute, body, "pr", "edit", strconv.Itoa(n), "--title", title, "--body-file", "-")
	return err
}

// prNumber reads the number off the URL gh prints for a new pull request.
func prNumber(out string) (int, string, error) {
	url := strings.TrimSpace(out)
	if i := strings.LastIndex(url, "\n"); i >= 0 {
		url = url[i+1:]
	}
	n, err := strconv.Atoi(url[strings.LastIndex(url, "/")+1:])
	if err != nil || n <= 0 {
		return 0, "", errors.New("gh did not say which pull request it made: " + url)
	}
	return n, url, nil
}

// ViewPR reads pull request n afresh.
func (r *Repo) ViewPR(n int) (*PR, error) {
	out, err := runGH(r.Root, 30*time.Second, "pr", "view", strconv.Itoa(n), "--json", prFields+",mergeable")
	if err != nil {
		return nil, err
	}
	pr := parsePR(out)
	if pr == nil {
		return nil, errors.New("no pull request #" + strconv.Itoa(n))
	}
	return pr, nil
}

// Behind reports whether trunk has moved on past where the branch checked out
// in dir left it, and from where: their merge base.
func Behind(dir, trunk string) (bool, string, error) {
	mb, err := gitIn(dir, time.Minute, "merge-base", trunk, "HEAD")
	if err != nil {
		return false, "", err
	}
	tip, err := gitIn(dir, time.Minute, "rev-parse", trunk+"^{commit}")
	if err != nil {
		return false, "", err
	}
	since := strings.TrimSpace(string(mb))
	return since != strings.TrimSpace(string(tip)), since, nil
}

// MergeIn merges trunk into the branch checked out in dir. A merge with
// conflicts is undone, and reports false: someone has to resolve them.
func MergeIn(dir, trunk string) (bool, error) {
	if _, err := gitIn(dir, 2*time.Minute, "merge", "--no-edit", trunk); err != nil {
		if out, _ := gitIn(dir, time.Minute, "diff", "--name-only", "--diff-filter=U"); len(strings.TrimSpace(string(out))) > 0 {
			gitIn(dir, time.Minute, "merge", "--abort")
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Refresh brings the branch checked out in dir up to trunk: a fast-forward
// while it has no commits of its own, a merge otherwise. from is where it had
// left trunk and to is trunk's tip; clean is false for a merge with conflicts,
// undone.
func Refresh(dir, trunk string) (from, to string, clean bool, err error) {
	behind, from, err := Behind(dir, trunk)
	if err != nil {
		return "", "", false, err
	}
	out, err := gitIn(dir, time.Minute, "rev-parse", trunk+"^{commit}")
	if err != nil {
		return "", "", false, err
	}
	to = strings.TrimSpace(out)
	if !behind {
		return from, to, true, nil
	}
	own, err := gitIn(dir, time.Minute, "rev-list", "--count", trunk+"..HEAD")
	if err != nil {
		return "", "", false, err
	}
	if strings.TrimSpace(own) == "0" {
		_, err = gitIn(dir, 2*time.Minute, "merge", "--ff-only", trunk)
		return from, to, err == nil, err
	}
	clean, err = MergeIn(dir, trunk)
	return from, to, clean, err
}

// MergePR squashes pull request n into its base.
func (r *Repo) MergePR(n int) error {
	_, err := runGH(r.Root, time.Minute, "pr", "merge", strconv.Itoa(n), "--squash")
	return err
}
