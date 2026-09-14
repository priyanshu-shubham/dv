package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"dv/internal/gitx"
	"dv/internal/store"
	"dv/internal/symindex"
)

// scopeFromRequest reads the scope the client is viewing. Callers get the
// resolved scope or an HTTP error already written.
func (s *Server) scopeFromRequest(w http.ResponseWriter, r *http.Request) (*gitx.Scope, bool) {
	q := r.URL.Query()
	sc, err := s.repo.ResolveScope(q.Get("scope"), q.Get("rev"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return nil, false
	}
	return sc, true
}

func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"repo":          s.repo.Name(),
		"root":          s.repo.Root,
		"head":          s.repo.Head(),
		"defaultBranch": s.repo.DefaultBranch(),
		"branches":      s.repo.Branches(),
		"recentCommits": s.repo.RecentCommits(20),
		"commentsPath":  s.store.Path(),
		"symbolStatus":  s.index.Status(),
	})
}

func (s *Server) handleDiffList(w http.ResponseWriter, r *http.Request) {
	// Taken before listing, so an edit that lands mid-request reads as newer on
	// the client's next poll rather than being folded into this one unnoticed.
	version, _ := s.repo.Version()
	sc, ok := s.scopeFromRequest(w, r)
	if !ok {
		return
	}
	files, err := s.repo.Files(sc)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if files == nil {
		files = []gitx.FileEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"scope":   sc,
		"files":   files,
		"head":    s.repo.Head(),
		"version": version,
	})
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	v, err := s.repo.Version()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// The notes are fingerprinted apart from the repository: they change on
	// their own, and a page follows them without reloading the diff.
	writeJSON(w, http.StatusOK, map[string]string{
		"version":  v,
		"comments": s.store.Version(),
		"viewed":   s.viewed.Version(),
	})
}

func (s *Server) handleDiffFile(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scopeFromRequest(w, r)
	if !ok {
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("path is required"))
		return
	}
	// Re-listing is what keeps status and rename detection honest; the list is
	// already in git's cache by the time a file is opened.
	files, err := s.repo.Files(sc)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	for _, e := range files {
		if e.Path != path {
			continue
		}
		fd, err := s.repo.Diff(sc, e)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, fd)
		return
	}
	writeErr(w, http.StatusNotFound, fmt.Errorf("%s is not part of this diff", path))
}

// handleFile serves the viewer: the working tree, or with side=old|new, that
// side of the scope the request names.
func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	path := q.Get("path")
	if path == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("path is required"))
		return
	}
	var sc *gitx.Scope
	if q.Get("side") != "" {
		var ok bool
		if sc, ok = s.scopeFromRequest(w, r); !ok {
			return
		}
	}
	lines, at, err := s.repo.FileAt(path, sc, q.Get("side") == "old")
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":  path,
		"lines": lines,
		"lang":  gitx.LangFor(path),
		"at":    at,
	})
}

// handleTree lists the whole repository as the scope's new side has it.
func (s *Server) handleTree(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scopeFromRequest(w, r)
	if !ok {
		return
	}
	files, err := s.repo.SideFiles(sc)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if files == nil {
		files = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

// handleFiles is quick open: every file in the repository, ranked against q.
func (s *Server) handleFiles(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	paths, err := s.repo.TrackedFiles()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 60
	}
	hits, matched := symindex.RankPaths(paths, q.Get("q"), limit)
	writeJSON(w, http.StatusOK, map[string]any{"hits": hits, "matched": matched, "total": len(paths)})
}

// handleThreads and handleViewed take the version first, as handleDiffList
// does, so a write landing mid-request reads as newer on the next poll.
func (s *Server) handleThreads(w http.ResponseWriter, r *http.Request) {
	version := s.store.Version()
	writeJSON(w, http.StatusOK, map[string]any{"threads": s.store.Threads(), "version": version})
}

func (s *Server) handleViewed(w http.ResponseWriter, r *http.Request) {
	version := s.viewed.Version()
	paths := s.viewed.Paths(r.URL.Query().Get("key"))
	writeJSON(w, http.StatusOK, map[string]any{"paths": paths, "version": version})
}

func (s *Server) handleMarkViewed(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key    string   `json:"key"`
		Paths  []string `json:"paths"`
		Viewed bool     `json:"viewed"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Key == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("key is required"))
		return
	}
	if err := s.viewed.Mark(req.Key, req.Paths, req.Viewed); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleReset is `dv reset` from the page: every comment and viewed mark goes.
func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	threads, err := s.store.Reset()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	marks, err := s.viewed.Reset()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"threads": threads, "marks": marks})
}

type threadReq struct {
	File      string   `json:"file"`
	Side      string   `json:"side"`
	StartLine int      `json:"startLine"`
	EndLine   int      `json:"endLine"`
	Quote     []string `json:"quote"`
	Scope     string   `json:"scope"`
	Body      string   `json:"body"`
	Author    string   `json:"author"`
}

func (s *Server) handleCreateThread(w http.ResponseWriter, r *http.Request) {
	var req threadReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.Body) == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("comment body is empty"))
		return
	}
	if req.Side == "" {
		req.Side = "new"
	}
	if req.EndLine == 0 {
		req.EndLine = req.StartLine
	}
	t, err := s.store.AddThread(&store.Thread{
		File:      req.File,
		Side:      req.Side,
		StartLine: req.StartLine,
		EndLine:   req.EndLine,
		Quote:     req.Quote,
		Scope:     req.Scope,
		BaseSHA:   s.repo.Head().SHA,
	}, req.Body, authorOr(req.Author))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleReply(w http.ResponseWriter, r *http.Request) {
	var req threadReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.Body) == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("comment body is empty"))
		return
	}
	t, err := s.store.AddReply(r.PathValue("id"), req.Body, authorOr(req.Author))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handlePatchThread(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Resolved *bool `json:"resolved"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Resolved == nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("nothing to update"))
		return
	}
	t, err := s.store.SetResolved(r.PathValue("id"), *req.Resolved)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleDeleteThread(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteThread(r.PathValue("id")); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleEditComment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.Body) == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("comment body is empty"))
		return
	}
	t, err := s.store.EditComment(r.PathValue("id"), r.PathValue("cid"), req.Body)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleDeleteComment(w http.ResponseWriter, r *http.Request) {
	t, err := s.store.DeleteComment(r.PathValue("id"), r.PathValue("cid"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	// A nil thread means that was its last comment and the thread went with it.
	writeJSON(w, http.StatusOK, map[string]any{"thread": t, "deleted": t == nil})
}

func (s *Server) handleSymbols(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.index.EnsureFresh(5 * time.Minute)

	// from is the file the reader is in. It does not narrow the search, it
	// ranks it: the nearest definition is nearly always the intended one.
	from := q.Get("from")
	if name := q.Get("name"); name != "" {
		writeJSON(w, http.StatusOK, s.index.Resolve(name, from, 40))
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 60
	}
	hits := s.index.Query(q.Get("q"), from, limit)
	if hits == nil {
		hits = []symindex.Hit{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"hits": hits, "status": s.index.Status()})
}

func (s *Server) handleSymbolStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.index.Status())
}

func (s *Server) handleSymbolRefresh(w http.ResponseWriter, r *http.Request) {
	s.index.BuildAsync()
	writeJSON(w, http.StatusOK, s.index.Status())
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	res, err := s.index.Search(symindex.SearchOpts{
		Query:     q.Get("q"),
		Regex:     q.Get("regex") == "1",
		CaseSens:  q.Get("case") == "1",
		WholeWord: q.Get("word") == "1",
		Glob:      q.Get("glob"),
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func authorOr(a string) string {
	if strings.TrimSpace(a) == "" {
		return "you"
	}
	return a
}
