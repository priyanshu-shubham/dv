package hub

import (
	"os"

	"dv/internal/gitx"
	"dv/internal/notify"
)

// Places are the hub's folders, for the reader to start a session in from
// elsewhere, as notify.Hub has them.
func (h *Hub) Places() []notify.Place {
	var out []notify.Place
	for _, f := range h.folders.List() {
		if fi, err := os.Stat(f.Path); err != nil || !fi.IsDir() {
			continue
		}
		git := f.WorktreeOf != ""
		if repo, err := gitx.Open(f.Path); !git && err == nil {
			git = repo.IsGit()
		}
		out = append(out, notify.Place{Slug: f.Slug, Name: displayName(f), Git: git})
	}
	return out
}

// Open opens a folder, as visiting its page does.
func (h *Hub) Open(slug string) error {
	_, err := h.review(slug)
	if err == nil {
		h.used(slug)
	}
	return err
}

// Worktree makes a worktree of the repository slug is in, on a new branch from
// what slug has checked out, in a folder named for it, and waits for it to be
// made and set up.
func (h *Hub) Worktree(slug, branch string, fresh bool) (notify.Place, error) {
	j, _, err := h.makeWorktree(slug, worktreeReq{Branch: branch, Here: true}, fresh)
	if err != nil {
		return notify.Place{}, err
	}
	<-j.ended
	h.mu.Lock()
	err, made := j.err, j.Slug
	h.mu.Unlock()
	if err != nil {
		return notify.Place{}, err
	}
	f, _ := h.folders.Get(made)
	return notify.Place{Slug: made, Name: displayName(f), Git: true}, nil
}
