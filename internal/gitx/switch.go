package gitx

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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
	if err := r.overwrites(branch, "refs/heads/"+branch); err != nil {
		return err
	}
	_, err := gitIn(r.Root, time.Minute, "switch", "--quiet", "--no-guess", branch)
	return err
}

// overwrites says which files on disk checking out ref would write over.
func (r *Repo) overwrites(name, ref string) error {
	added, err := r.paths("diff", "--name-only", "-z", "--no-renames", "--diff-filter=A", "HEAD", ref)
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
		return fmt.Errorf("%s would overwrite files not in this branch:\n%s", name, strings.Join(in, "\n"))
	}
	return nil
}

// RemoteRef is a remote's branch: Ref as "origin/fix/login", Branch as the
// local one tracking it would be named, "fix/login".
type RemoteRef struct {
	Ref    string `json:"ref"`
	Branch string `json:"branch"`
}

// RemoteRefs lists the remotes' branches no local branch is named for, most
// recently committed first.
func (r *Repo) RemoteRefs() []RemoteRef {
	local := map[string]bool{}
	for _, b := range r.Branches() {
		local[b] = true
	}
	var refs []RemoteRef
	for _, x := range r.allRemoteRefs() {
		if !local[x.Branch] {
			refs = append(refs, x)
		}
	}
	return refs
}

func (r *Repo) allRemoteRefs() []RemoteRef {
	if !r.IsGit() {
		return nil
	}
	out, err := r.run("for-each-ref", "--sort=-committerdate", "--format=%(refname:lstrip=2)", "refs/remotes")
	if err != nil {
		return nil
	}
	var refs []RemoteRef
	for _, ref := range strings.Fields(out) {
		if _, branch, ok := strings.Cut(ref, "/"); ok && branch != "HEAD" {
			refs = append(refs, RemoteRef{ref, branch})
		}
	}
	return refs
}

// Tracking is the remote branch a new local one called name would track,
// with that local one's name: name may be the remote branch's own, as
// "origin/fix/login", or the branch's, as "fix/login", when only one remote
// has it. No ref and no error is a name no remote has; a local branch it
// would be is the caller's to check first.
func (r *Repo) Tracking(name string) (RemoteRef, error) {
	var on []RemoteRef
	for _, x := range r.allRemoteRefs() {
		if x.Ref == name {
			if r.HasBranch(x.Branch) {
				return RemoteRef{}, fmt.Errorf("there is a branch %s already", x.Branch)
			}
			return x, nil
		}
		if x.Branch == name {
			on = append(on, x)
		}
	}
	if len(on) > 1 {
		var refs []string
		for _, x := range on {
			refs = append(refs, x.Ref)
		}
		return RemoteRef{}, fmt.Errorf("more than one remote has %s: name one of %s", name, strings.Join(refs, ", "))
	}
	if len(on) == 1 {
		return on[0], nil
	}
	return RemoteRef{}, nil
}

// Fetch brings every remote's branches up to date, forgetting those deleted.
func (r *Repo) Fetch() error {
	if out, _ := r.run("remote"); strings.TrimSpace(out) == "" {
		return nil
	}
	_, err := gitIn(r.Root, 2*time.Minute, "fetch", "--all", "--prune", "--quiet")
	return err
}

// fetched is when each repository, by its common git dir, was last fetched
// by FetchStale; its worktrees share it.
var (
	fetchMu sync.Mutex
	fetched = map[string]time.Time{}
)

// FetchStale is Fetch, unless this repository was fetched within every.
func (r *Repo) FetchStale(every time.Duration) error {
	dir, _ := r.run("rev-parse", "--path-format=absolute", "--git-common-dir")
	dir = strings.TrimSpace(dir)
	fetchMu.Lock()
	defer fetchMu.Unlock()
	if time.Since(fetched[dir]) < every {
		return nil
	}
	if err := r.Fetch(); err != nil {
		return err
	}
	fetched[dir] = time.Now()
	return nil
}

// Track checks out a remote's branch as a new local branch tracking it, as
// Switch would a local one.
func (r *Repo) Track(ref string) error {
	if why := r.SwitchBlocker(); why != "" {
		return errors.New(why)
	}
	refs := r.RemoteRefs()
	i := slices.IndexFunc(refs, func(x RemoteRef) bool { return x.Ref == ref })
	if i < 0 {
		return fmt.Errorf("there is no remote branch %s without a local one", ref)
	}
	branch := refs[i].Branch
	if err := r.ValidBranch(branch); err != nil {
		return err
	}
	if err := r.overwrites(ref, "refs/remotes/"+ref); err != nil {
		return err
	}
	_, err := gitIn(r.Root, time.Minute, "switch", "--quiet", "--create", branch, "--track", "refs/remotes/"+ref)
	return err
}
