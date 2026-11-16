package gitx

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Worktree is one of a repository's checkouts. Branch is "" when detached.
type Worktree struct {
	Path   string `json:"path"`
	Branch string `json:"branch,omitempty"`
	SHA    string `json:"sha,omitempty"`
}

// OtherWorktrees lists the repository's checkouts besides this one, the main
// checkout first, as git has them. One whose folder is gone is left out.
func (r *Repo) OtherWorktrees() []Worktree {
	out, err := r.run("worktree", "list", "--porcelain")
	if err != nil {
		return nil
	}
	var list []Worktree
	for _, block := range strings.Split(out, "\n\n") {
		var w Worktree
		skip := false
		for _, l := range strings.Split(block, "\n") {
			k, v, _ := strings.Cut(l, " ")
			switch k {
			case "worktree":
				w.Path = v
			case "HEAD":
				w.SHA = v[:min(len(v), 7)]
			case "branch":
				w.Branch = strings.TrimPrefix(v, "refs/heads/")
			case "bare", "prunable":
				skip = true
			}
		}
		if w.Path != "" && w.Path != r.Root && !skip {
			list = append(list, w)
		}
	}
	return list
}

// BranchAt is the branch checked out at a checkout's root, "" when detached
// or not a checkout. It reads HEAD rather than running git, for lists asked
// for every second.
func BranchAt(root string) string {
	gitDir := filepath.Join(root, ".git")
	// A linked worktree's .git is a file naming its own git directory.
	if b, err := os.ReadFile(gitDir); err == nil {
		d, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir: ")
		if !ok {
			return ""
		}
		if !filepath.IsAbs(d) {
			d = filepath.Join(root, d)
		}
		gitDir = d
	}
	head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return ""
	}
	branch, ok := strings.CutPrefix(strings.TrimSpace(string(head)), "ref: refs/heads/")
	if !ok {
		return "" // detached: HEAD is a bare SHA
	}
	return branch
}

// Count is how many commits to has that from has not.
func (r *Repo) Count(from, to string) int {
	out, err := r.run("rev-list", "--count", from+".."+to)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(out))
	return n
}

// MergeBlocker is why nothing can be merged into this checkout now, "" when
// something can.
func (r *Repo) MergeBlocker() string {
	if why := r.CreateBlocker(); why != "" {
		return why
	}
	if BranchAt(r.Root) == "" {
		return "HEAD is on no branch"
	}
	st, err := r.Status()
	if err != nil {
		return err.Error()
	}
	if st.Staged+st.Unstaged+st.Conflicts > 0 {
		return "there are uncommitted changes"
	}
	return ""
}

// Merge merges branch into the branch checked out here, as `git merge` would:
// a fast-forward when it can be, else a merge commit. One that conflicts is
// undone, leaving the checkout as it was, and says which files conflicted.
func (r *Repo) Merge(branch string) error {
	if why := r.MergeBlocker(); why != "" {
		return errors.New(why)
	}
	if !r.HasBranch(branch) {
		return fmt.Errorf("there is no branch %s here", branch)
	}
	_, err := gitIn(r.Root, time.Minute, "merge", "--no-edit", "--quiet", branch)
	if err == nil {
		return nil
	}
	if _, e := os.Stat(filepath.Join(r.GitDir, "MERGE_HEAD")); e == nil {
		files, _ := r.paths("diff", "--name-only", "-z", "--diff-filter=U")
		if _, e := gitIn(r.Root, time.Minute, "merge", "--abort"); e != nil {
			return fmt.Errorf("%w; undoing the merge failed too: %v", err, e)
		}
		if len(files) > 0 {
			return fmt.Errorf("%s conflicts with %s in these files, so nothing was merged:\n%s", branch, BranchAt(r.Root), strings.Join(files, "\n"))
		}
	}
	return err
}

// DeleteBranch deletes a branch that is merged, as `git branch -d` would.
func (r *Repo) DeleteBranch(branch string) error {
	if err := r.ValidBranch(branch); err != nil {
		return err
	}
	_, err := gitIn(r.Root, time.Minute, "branch", "--delete", branch)
	return err
}
