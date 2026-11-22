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
func (r *Repo) CreatePR(branch, base, title, body string) (int, error) {
	out, err := runGHIn(r.Root, time.Minute, body, "pr", "create", "--head", branch, "--base", base, "--title", title, "--body-file", "-")
	if err != nil {
		return 0, err
	}
	return prNumber(string(out))
}

// prNumber reads the number off the URL gh prints for a new pull request.
func prNumber(out string) (int, error) {
	url := strings.TrimSpace(out)
	if i := strings.LastIndex(url, "\n"); i >= 0 {
		url = url[i+1:]
	}
	n, err := strconv.Atoi(url[strings.LastIndex(url, "/")+1:])
	if err != nil || n <= 0 {
		return 0, errors.New("gh did not say which pull request it made: " + url)
	}
	return n, nil
}

// ViewPR reads pull request n afresh.
func (r *Repo) ViewPR(n int) (*PR, error) {
	out, err := runGH(r.Root, 30*time.Second, "pr", "view", strconv.Itoa(n), "--json", prFields)
	if err != nil {
		return nil, err
	}
	pr := parsePR(out)
	if pr == nil {
		return nil, errors.New("no pull request #" + strconv.Itoa(n))
	}
	return pr, nil
}

// MergePR squashes pull request n into its base.
func (r *Repo) MergePR(n int) error {
	_, err := runGH(r.Root, time.Minute, "pr", "merge", strconv.Itoa(n), "--squash")
	return err
}
