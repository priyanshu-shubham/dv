package hub

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"dv/internal/gitx"
	"dv/internal/server"
	"dv/internal/store"
)

// handleUpdate changes what the hub keeps of a folder: { name, setup,
// teardown }, each left as it is when absent.
func (h *Hub) handleUpdate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     *string `json:"name"`
		Setup    *string `json:"setup"`
		Teardown *string `json:"teardown"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	f, err := h.folders.Update(r.PathValue("slug"), func(f *store.Folder) {
		if body.Name != nil {
			// Its own name back is no name of its own.
			if f.Name = strings.TrimSpace(*body.Name); f.Name == filepath.Base(f.Path) {
				f.Name = ""
			}
		}
		if body.Setup != nil {
			f.Setup = strings.TrimSpace(*body.Setup)
		}
		if body.Teardown != nil {
			f.Teardown = strings.TrimSpace(*body.Teardown)
		}
	})
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

// repoOf is the listed repository a folder's worktrees are made from: the
// folder itself, or the main checkout it is a worktree of.
func (h *Hub) repoOf(slug string) (store.Folder, error) {
	f, ok := h.folders.Get(slug)
	if !ok {
		return store.Folder{}, fmt.Errorf("no folder at /%s/ in this hub", slug)
	}
	if f.WorktreeOf == "" {
		return f, nil
	}
	if main, ok := h.folders.ByPath(f.WorktreeOf); ok {
		return main, nil
	}
	return store.Folder{}, fmt.Errorf("%s, which %s is a worktree of, is not in this hub", server.HomeRelative(f.WorktreeOf), f.Path)
}

// handleBranches offers what a new worktree could check out or start from.
func (h *Hub) handleBranches(w http.ResponseWriter, r *http.Request) {
	main, err := h.repoOf(r.PathValue("slug"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	repo, err := gitx.Open(main.Path)
	if err != nil || !repo.IsGit() {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("%s is not a git repository", server.HomeRelative(main.Path)))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"branches": append([]string{}, repo.Branches()...),
		"remote":   append([]string{}, repo.RemoteBranches()...),
		"base":     startPoint(repo, ""),
		"repo":     filepath.Base(main.Path),
		"into":     server.HomeRelative(filepath.Dir(main.Path)),
	})
}

// startPoint is where a new branch starts when nothing is said: the default
// branch as this clone has it, else wherever HEAD is.
func startPoint(repo *gitx.Repo, base string) string {
	if base != "" {
		return base
	}
	if b := repo.DefaultBranch(); b != "" && repo.HasBranch(b) {
		return b
	}
	return "HEAD"
}

// worktreeReq is a worktree to make. Here starts its branch from what the
// folder asked from has checked out, in place of base.
type worktreeReq struct {
	Branch string `json:"branch"`
	Base   string `json:"base"`
	Name   string `json:"name"`
	Here   bool   `json:"here"`
}

// handleWorktree makes a worktree beside the repository: { branch, base, name,
// here }. An existing branch, local or on a remote, is checked out; any other
// is made, from base. name is the new folder's, "" for <repo>-<branch>, or the
// first of <repo>-<branch>-2, -3... not taken. Once git has made it the
// worktree is added to the hub and the repository's setup hook runs in it.
func (h *Hub) handleWorktree(w http.ResponseWriter, r *http.Request) {
	var body worktreeReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	j, status, err := h.makeWorktree(r.PathValue("slug"), body, false)
	if err != nil {
		writeErr(w, status, err)
		return
	}
	h.mu.Lock()
	v := j.view()
	h.mu.Unlock()
	writeJSON(w, http.StatusOK, v)
}

// makeWorktree starts the job making a worktree of the repository slug is in,
// or says why it cannot, with the HTTP status for it. fresh makes a new branch
// whatever the name: one taken is numbered on, as a name made up for the
// reader is.
func (h *Hub) makeWorktree(slug string, req worktreeReq, fresh bool) (*job, int, error) {
	main, err := h.repoOf(slug)
	if err != nil {
		return nil, http.StatusNotFound, err
	}
	repo, err := gitx.Open(main.Path)
	if err != nil || !repo.IsGit() {
		return nil, http.StatusBadRequest, fmt.Errorf("%s is not a git repository", server.HomeRelative(main.Path))
	}
	branch, base := strings.TrimSpace(req.Branch), strings.TrimSpace(req.Base)
	if fresh {
		branch = freeBranch(repo, branch)
	}
	if err := repo.ValidBranch(branch); err != nil {
		return nil, http.StatusBadRequest, err
	}
	if f, ok := h.folders.Get(slug); req.Here && ok {
		if here, err := gitx.Open(f.Path); err == nil {
			head := here.Head()
			base = cmp.Or(head.Branch, head.SHA)
		}
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = filepath.Base(main.Path) + "-" + strings.ReplaceAll(branch, "/", "-")
		name = filepath.Base(h.freePath(filepath.Join(filepath.Dir(main.Path), name)))
	}
	if err := plainName(name); err != nil {
		return nil, http.StatusBadRequest, err
	}
	path := filepath.Join(filepath.Dir(main.Path), name)
	if h.taken(path) {
		return nil, http.StatusConflict, fmt.Errorf("%s already exists", server.HomeRelative(path))
	}
	args := []string{"worktree", "add", "--", path, branch}
	if !repo.HasBranch(branch) && (base != "" || !repo.HasRemoteBranch(branch)) {
		args = []string{"worktree", "add", "-b", branch, "--", path, startPoint(repo, base)}
	}

	j := &job{Kind: "worktree", Title: branch, Path: path, Place: server.HomeRelative(path), Of: main.Slug, Step: "Creating"}
	h.start(j, func(ctx context.Context) (string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir, cmd.Env = main.Path, quietGit()
		if err := h.run(j, cmd); err != nil {
			return "", err
		}
		f, err := h.folders.Add(store.Folder{Path: path, WorktreeOf: main.Path})
		if err != nil {
			return "", fmt.Errorf("made the worktree, but could not add it: %w", err)
		}
		h.mu.Lock()
		j.Slug = f.Slug
		h.mu.Unlock()
		return h.setup(ctx, j, main, path, branch)
	})
	return j, 0, nil
}

func (h *Hub) taken(path string) bool {
	_, err := os.Lstat(path)
	return err == nil || h.busy(path) != nil
}

// freePath is path, or path-2, -3... the first not taken.
func (h *Hub) freePath(path string) string {
	free := path
	for i := 2; h.taken(free); i++ {
		free = path + "-" + strconv.Itoa(i)
	}
	return free
}

// freeBranch is branch, or branch-2, -3... the first no branch here or on a
// remote has.
func freeBranch(repo *gitx.Repo, branch string) string {
	name := branch
	for i := 2; repo.HasBranch(name) || repo.HasRemoteBranch(name); i++ {
		name = branch + "-" + strconv.Itoa(i)
	}
	return name
}

// setup runs the repository's setup hook in a worktree, for j.
func (h *Hub) setup(ctx context.Context, j *job, main store.Folder, path, branch string) (string, error) {
	if main.Setup == "" {
		return "", nil
	}
	h.step(j, "Setting up")
	if err := h.hook(ctx, j, main.Setup, path, main.Path, branch); err != nil {
		return "setup", fmt.Errorf("The setup hook failed:\n%w", err)
	}
	return "", nil
}

// handleSetup runs a worktree's setup hook again.
func (h *Hub) handleSetup(w http.ResponseWriter, r *http.Request) {
	wt, main, ok := h.worktree(w, r.PathValue("slug"))
	if !ok {
		return
	}
	if main.Setup == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("%s has no setup hook", filepath.Base(main.Path)))
		return
	}
	h.forget(wt.Path)
	j := &job{Kind: "setup", Title: displayName(wt), Path: wt.Path, Place: server.HomeRelative(wt.Path), Of: main.Slug, Slug: wt.Slug}
	writeJSON(w, http.StatusOK, h.start(j, func(ctx context.Context) (string, error) {
		return h.setup(ctx, j, main, wt.Path, branchOf(wt.Path))
	}))
}

// maxListed is how many uncommitted files a refusal to delete names.
const maxListed = 20

// handleDeleteWorktree deletes a worktree the hub lists: { skipTeardown }. One
// with uncommitted changes is left as it is, before anything runs or closes,
// as git would refuse it only after the teardown hook had run; force deletes
// it anyway, and the changes with it. Otherwise the repository's teardown hook
// runs, unless skipped; then git removes the worktree and the hub lets it go.
// The branch stays.
func (h *Hub) handleDeleteWorktree(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SkipTeardown bool `json:"skipTeardown"`
		Force        bool `json:"force"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	wt, main, ok := h.worktree(w, r.PathValue("slug"))
	if !ok {
		return
	}
	h.forget(wt.Path)
	j := &job{Kind: "remove", Title: displayName(wt), Path: wt.Path, Place: server.HomeRelative(wt.Path), Of: main.Slug, Slug: wt.Slug, Step: "Checking", Force: body.Force}
	writeJSON(w, http.StatusOK, h.start(j, func(ctx context.Context) (string, error) {
		if repo, err := gitx.Open(wt.Path); err == nil && !body.Force {
			changed, err := repo.Uncommitted()
			if err != nil {
				return "", err
			}
			if n := len(changed); n > 0 {
				if n > maxListed {
					changed = append(changed[:maxListed], fmt.Sprintf("and %d more", n-maxListed))
				}
				return "force", fmt.Errorf("It has uncommitted changes, so it stays as it is. Commit, stash or discard them, or delete it anyway and lose them:\n%s", strings.Join(changed, "\n"))
			}
		}
		if main.Teardown != "" && !body.SkipTeardown {
			h.step(j, "Tearing down")
			if err := h.hook(ctx, j, main.Teardown, wt.Path, main.Path, branchOf(wt.Path)); err != nil {
				return "remove", fmt.Errorf("The teardown hook failed, so the worktree is still there:\n%w", err)
			}
		}
		h.step(j, "Deleting")
		h.closeReview(wt.Slug)
		args := []string{"worktree", "remove", "--", wt.Path}
		if body.Force {
			args = []string{"worktree", "remove", "--force", "--", wt.Path}
		}
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = wt.WorktreeOf
		if err := h.run(j, cmd); err != nil {
			if body.Force {
				return "", err
			}
			// Changed since it was looked at: the same choice as then.
			return "force", err
		}
		// Taken off the hub while it went, it has nothing more to let go of.
		if _, listed := h.folders.Get(wt.Slug); listed {
			if err := h.folders.Remove(wt.Slug); err != nil {
				return "", err
			}
		}
		h.closeReview(wt.Slug)
		return "", nil
	}))
}

// worktree finds a listed worktree and the repository it belongs to, or
// answers why not.
func (h *Hub) worktree(w http.ResponseWriter, slug string) (wt, main store.Folder, ok bool) {
	wt, found := h.folders.Get(slug)
	if !found || wt.WorktreeOf == "" {
		writeErr(w, http.StatusNotFound, fmt.Errorf("no worktree at /%s/ in this hub", slug))
		return wt, main, false
	}
	if h.busy(wt.Path) != nil {
		writeErr(w, http.StatusConflict, fmt.Errorf("%s is busy; wait for it to finish", server.HomeRelative(wt.Path)))
		return wt, main, false
	}
	// A repository no longer listed has no hooks to run; the worktree can still go.
	main, _ = h.folders.ByPath(wt.WorktreeOf)
	main.Path = wt.WorktreeOf
	return wt, main, true
}

func displayName(f store.Folder) string {
	if f.Name != "" {
		return f.Name
	}
	return filepath.Base(f.Path)
}

func branchOf(path string) string {
	if repo, err := gitx.Open(path); err == nil {
		return repo.Head().Branch
	}
	return ""
}
