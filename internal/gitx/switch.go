package gitx

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SwitchBlocker is why no branch can be checked out here now, "" when one can:
// only a checkout with nothing a switch could lose or tangle up is switched.
// Untracked files are not a reason, as they come along untouched.
func (r *Repo) SwitchBlocker() string {
	if why := r.CreateBlocker(); why != "" {
		return why
	}
	st, err := r.Status()
	if err != nil {
		return err.Error()
	}
	if st.Staged+st.Unstaged+st.Conflicts > 0 {
		return "there are uncommitted changes"
	}
	if r.Head().Branch == "" {
		// Commits made on a detached HEAD would be left for the reflog only.
		if out, _ := r.run("for-each-ref", "--count=1", "--contains", "HEAD", "--format=x", "refs/heads", "refs/remotes", "refs/tags"); strings.TrimSpace(out) == "" {
			return "HEAD has commits on no branch"
		}
	}
	return ""
}

// CreateBlocker is why no branch can be made here now, "" when one can. A new
// branch starts at HEAD and changes no file, so uncommitted changes simply go
// with it, and commits on no branch are kept by it.
func (r *Repo) CreateBlocker() string {
	if !r.IsGit() {
		return "not a git repository"
	}
	for _, op := range []struct{ file, name string }{
		{"rebase-merge", "a rebase"}, {"rebase-apply", "a rebase"}, {"MERGE_HEAD", "a merge"},
		{"CHERRY_PICK_HEAD", "a cherry-pick"}, {"REVERT_HEAD", "a revert"}, {"BISECT_LOG", "a bisect"},
	} {
		if _, err := os.Stat(filepath.Join(r.GitDir, op.file)); err == nil {
			return op.name + " is in progress"
		}
	}
	return ""
}

// CreateBranch makes a branch at HEAD and checks it out.
func (r *Repo) CreateBranch(name string) error {
	if why := r.CreateBlocker(); why != "" {
		return errors.New(why)
	}
	if err := r.ValidBranch(name); err != nil {
		return err
	}
	if r.HasBranch(name) {
		return fmt.Errorf("there is a branch %s already", name)
	}
	_, err := gitIn(r.Root, time.Minute, "switch", "--quiet", "--create", name)
	return err
}

// Elsewhere lists the branches checked out in another worktree, which git
// will not check out a second time.
func (r *Repo) Elsewhere() []string {
	var names []string
	for ref, dir := range r.checkouts() {
		if dir != r.Root {
			names = append(names, strings.TrimPrefix(ref, "refs/heads/"))
		}
	}
	return names
}

// Switch checks out a local branch, when SwitchBlocker allows and the switch
// would write over no file on disk: git keeps untracked files from being
// overwritten, but not ignored ones such as a .env the branch tracks.
func (r *Repo) Switch(branch string) error {
	if why := r.SwitchBlocker(); why != "" {
		return errors.New(why)
	}
	if err := r.ValidBranch(branch); err != nil || !r.HasBranch(branch) {
		return fmt.Errorf("there is no branch %s here", branch)
	}
	if branch == r.Head().Branch {
		return fmt.Errorf("%s is checked out already", branch)
	}
	if dir := r.checkedOut("refs/heads/" + branch); dir != "" {
		return fmt.Errorf("%s is checked out in %s", branch, dir)
	}
	added, err := r.paths("diff", "--name-only", "-z", "--no-renames", "--diff-filter=A", "HEAD", "refs/heads/"+branch)
	if err != nil {
		return err
	}
	var in []string
	for _, p := range added {
		if _, err := os.Lstat(filepath.Join(r.Root, p)); err == nil {
			in = append(in, p)
		}
	}
	if len(in) > 0 {
		return fmt.Errorf("%s would overwrite files not in this branch:\n%s", branch, strings.Join(in, "\n"))
	}
	_, err = gitIn(r.Root, time.Minute, "switch", "--quiet", "--no-guess", branch)
	return err
}
