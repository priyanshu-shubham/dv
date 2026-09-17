// Package hub serves many folders from one dv, each at /<slug>/ on one port,
// so a single address (or tunnel) reaches all of them. A folder's review is
// opened the first time it is asked for and runs as a lone dv would.
package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	"dv/internal/gitx"
	"dv/internal/server"
	"dv/internal/store"
)

type Hub struct {
	url     string // where this hub listens, which reviews announce themselves under
	user    *store.Prefs
	folders *store.Folders

	mu   sync.Mutex
	open map[string]*review // by slug
	jobs map[string]*job    // by id
}

type review struct {
	srv        *server.Server
	handler    http.Handler
	unannounce func()
}

func New(url string, user *store.Prefs, folders *store.Folders) *Hub {
	return &Hub{url: url, user: user, folders: folders, open: map[string]*review{}, jobs: map[string]*job{}}
}

// Close stops every review and job, and the Claude Code sessions dv runs.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for slug, rv := range h.open {
		rv.close()
		delete(h.open, slug)
	}
	for _, j := range h.jobs {
		j.cancel()
	}
}

func (rv *review) close() {
	rv.srv.Close()
	if rv.unannounce != nil {
		rv.unannounce()
	}
}

func (h *Hub) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", server.Static())
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		server.WritePage(w, server.Boot{Page: "hub", Prefs: map[string]map[string]json.RawMessage{"user": h.user.All()}, PrefsVersion: h.user.Version()})
	})
	getPrefs, setPrefs := server.UserPrefs(h.user)
	mux.HandleFunc("GET /api/prefs", server.Guarded(getPrefs))
	mux.HandleFunc("PATCH /api/prefs", server.Guarded(setPrefs))
	mux.HandleFunc("GET /api/hub/folders", server.Guarded(h.handleFolders))
	mux.HandleFunc("POST /api/hub/folders", server.Guarded(h.handleAdd))
	mux.HandleFunc("PATCH /api/hub/folders/{slug}", server.Guarded(h.handleUpdate))
	mux.HandleFunc("DELETE /api/hub/folders/{slug}", server.Guarded(h.handleRemove))
	mux.HandleFunc("POST /api/hub/folders/{slug}/close", server.Guarded(h.handleClose))
	mux.HandleFunc("GET /api/hub/folders/{slug}/branches", server.Guarded(h.handleBranches))
	mux.HandleFunc("POST /api/hub/folders/{slug}/worktrees", server.Guarded(h.handleWorktree))
	mux.HandleFunc("POST /api/hub/folders/{slug}/setup", server.Guarded(h.handleSetup))
	mux.HandleFunc("POST /api/hub/folders/{slug}/delete", server.Guarded(h.handleDeleteWorktree))
	mux.HandleFunc("GET /api/hub/dirs", server.Guarded(h.handleDirs))
	mux.HandleFunc("POST /api/hub/clones", server.Guarded(h.handleClone))
	mux.HandleFunc("DELETE /api/hub/jobs/{id}", server.Guarded(h.handleDismissJob))
	mux.HandleFunc("GET /{slug}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/"+r.PathValue("slug")+"/", http.StatusFound)
	})
	mux.HandleFunc("/{slug}/{rest...}", h.serveReview)
	return mux
}

// serveReview hands a request to its folder's review, opening it if need be.
func (h *Hub) serveReview(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	api := strings.HasPrefix(r.PathValue("rest"), "api/")
	// A hook finding the record of a hub that stopped must not open the review
	// again: the prompt is the terminal's while nobody has it open.
	var rv *review
	var err error
	if api && r.PathValue("rest") == "api/claude/hook" {
		rv = h.opened(slug)
		if rv == nil {
			err = errors.New("not open")
		}
	} else {
		rv, err = h.review(slug)
	}
	if err != nil {
		if api {
			writeErr(w, http.StatusNotFound, err)
		} else {
			http.Redirect(w, r, "/?"+url.Values{"from": {slug}, "why": {err.Error()}}.Encode(), http.StatusFound)
		}
		return
	}
	http.StripPrefix("/"+slug, rv.handler).ServeHTTP(w, r)
}

func (h *Hub) opened(slug string) *review {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.open[slug]
}

// review is the folder's review, opened now if it is not yet. A folder another
// dv has open stays that dv's: two would each think the prompts theirs.
func (h *Hub) review(slug string) (*review, error) {
	if rv := h.opened(slug); rv != nil {
		return rv, nil
	}
	f, ok := h.folders.Get(slug)
	if !ok {
		return nil, fmt.Errorf("no folder at /%s/ in this hub", slug)
	}
	if j := h.busy(f.Path); j != nil && j.Kind == "remove" {
		return nil, fmt.Errorf("%s is being deleted", server.HomeRelative(f.Path))
	}
	// Asked before locking: an answer can take a while not to come.
	if addr := h.elsewhere(f.Path); addr != "" {
		return nil, fmt.Errorf("%s is open in another dv, at %s", server.HomeRelative(f.Path), addr)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if rv := h.open[slug]; rv != nil {
		return rv, nil
	}
	srv, err := server.Open(f.Path, h.user)
	if err != nil {
		return nil, err
	}
	base := "/" + slug
	rv := &review{srv: srv, handler: srv.Handler(base)}
	if rv.unannounce, err = store.Announce(srv.Repo().Root, h.url+base); err != nil {
		fmt.Fprintln(os.Stderr, "dv: warning: Claude Code's prompts cannot find", f.Path+":", err)
	}
	h.open[slug] = rv
	return rv, nil
}

// elsewhere is the address of another dv that has root open, "" for none. A
// record under this hub's own address is this hub's, or one left by a hub here
// that stopped.
func (h *Hub) elsewhere(root string) string {
	s, ok := store.Announced(root)
	if !ok || strings.HasPrefix(s.URL, h.url+"/") {
		return ""
	}
	if !server.Answers(s.URL, root) {
		return ""
	}
	return s.URL
}

type folderView struct {
	store.Folder
	Name      string         `json:"name"` // what to call it, its own name or the one given
	Place     string         `json:"place"`
	Missing   bool           `json:"missing,omitempty"`
	Open      *server.Status `json:"open,omitempty"`      // served here
	Elsewhere string         `json:"elsewhere,omitempty"` // another dv's address
	// With ?details=1, what takes git to read, which the page asks for as it
	// loads and every so often after rather than each time it polls.
	Git    bool         `json:"git,omitempty"`
	Remote *gitx.Remote `json:"remote,omitempty"`
	Status *gitx.Status `json:"status,omitempty"`
}

func (h *Hub) handleFolders(w http.ResponseWriter, r *http.Request) {
	details := r.URL.Query().Get("details") != ""
	list := h.folders.List()
	views := make([]folderView, len(list))
	var wg sync.WaitGroup
	for i, f := range list {
		v := &views[i]
		v.Folder, v.Name, v.Place = f, displayName(f), server.HomeRelative(f.Path)
		if fi, err := os.Stat(f.Path); err != nil || !fi.IsDir() {
			v.Missing = true
			continue
		}
		rv := h.opened(f.Slug)
		if rv != nil {
			st := rv.srv.Status()
			v.Open = &st
		}
		if rv != nil && !details {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rv == nil {
				v.Elsewhere = h.elsewhere(f.Path)
			}
			if !details {
				return
			}
			if repo, err := gitx.Open(f.Path); err == nil && repo.IsGit() {
				v.Git, v.Remote = true, repo.Origin()
				v.Status, _ = repo.Status()
				// A remote with no web page is a path on this machine, written as the folders are.
				if v.Remote != nil && v.Remote.Web == "" {
					v.Remote.Label = server.HomeRelative(v.Remote.Label)
				}
			}
		}()
	}
	wg.Wait()
	into := h.folders.CloneInto()
	if into != "" {
		into = server.HomeRelative(into)
	}
	writeJSON(w, http.StatusOK, map[string]any{"folders": views, "jobs": h.jobViews(), "cloneInto": into, "prefs": h.user.Version()})
}

// handleAdd puts a folder on the list: { path }. A folder inside a repository
// adds the repository, which is what dv would open there.
func (h *Hub) handleAdd(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	path, err := expand(body.Path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	repo, err := gitx.Open(path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	f, err := h.folders.Add(store.Folder{Path: repo.Root, WorktreeOf: repo.MainRoot()})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

// handleRemove takes a folder off the list, closing its review first.
func (h *Hub) handleRemove(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	h.closeReview(slug)
	if err := h.folders.Remove(slug); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleClose stops a review this hub serves, and the sessions dv runs in it,
// until it is next opened.
func (h *Hub) handleClose(w http.ResponseWriter, r *http.Request) {
	h.closeReview(r.PathValue("slug"))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Hub) closeReview(slug string) {
	h.mu.Lock()
	rv := h.open[slug]
	delete(h.open, slug)
	h.mu.Unlock()
	if rv != nil {
		rv.close()
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
