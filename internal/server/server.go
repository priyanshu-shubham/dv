// Package server exposes the review UI and its JSON API. Everything is local:
// the listener binds to loopback, there is no auth layer, and all state lives in
// the repository being reviewed.
package server

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"

	"dv/internal/gitx"
	"dv/internal/permit"
	"dv/internal/store"
	"dv/internal/symindex"
)

// The bundle is built by `make build`, not checked in - only the .keep beside
// it is, because go:embed refuses a pattern that matches nothing and would
// otherwise stop the package compiling.
//
//go:embed static/*
var staticFS embed.FS

//go:embed index.html
var indexHTML []byte

// AssetsBuilt reports whether the UI bundle made it into the binary. Without it
// dv still starts and still serves the page shell, and the browser silently
// gets a blank screen - so this is checked up front instead.
func AssetsBuilt() bool {
	f, err := staticFS.Open("static/bundle.js")
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// Server wires the state a review needs.
type Server struct {
	repo   *gitx.Repo
	store  *store.Store
	viewed *store.Viewed
	index  *symindex.Index
	permit *permit.Broker
}

func New(repo *gitx.Repo, st *store.Store, vw *store.Viewed, ix *symindex.Index) *Server {
	return &Server{repo: repo, store: st, viewed: vw, index: ix, permit: permit.New(repo.Root)}
}

// Handler builds the route table.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/meta", s.handleMeta)
	mux.HandleFunc("GET /api/diff", s.handleDiffList)
	mux.HandleFunc("GET /api/diff/file", s.handleDiffFile)
	mux.HandleFunc("GET /api/version", s.handleVersion)
	mux.HandleFunc("GET /api/file", s.handleFile)
	mux.HandleFunc("GET /api/files", s.handleFiles)
	mux.HandleFunc("GET /api/tree", s.handleTree)

	mux.HandleFunc("GET /api/threads", s.handleThreads)
	mux.HandleFunc("POST /api/threads", s.handleCreateThread)
	mux.HandleFunc("POST /api/threads/{id}/replies", s.handleReply)
	mux.HandleFunc("PATCH /api/threads/{id}", s.handlePatchThread)
	mux.HandleFunc("DELETE /api/threads/{id}", s.handleDeleteThread)
	mux.HandleFunc("PATCH /api/threads/{id}/comments/{cid}", s.handleEditComment)
	mux.HandleFunc("DELETE /api/threads/{id}/comments/{cid}", s.handleDeleteComment)

	mux.HandleFunc("GET /api/viewed", s.handleViewed)
	mux.HandleFunc("POST /api/viewed", s.handleMarkViewed)
	mux.HandleFunc("POST /api/reset", s.handleReset)

	mux.HandleFunc("GET /api/symbols", s.handleSymbols)
	mux.HandleFunc("GET /api/symbols/status", s.handleSymbolStatus)
	mux.HandleFunc("POST /api/symbols/refresh", s.handleSymbolRefresh)
	mux.HandleFunc("GET /api/search", s.handleSearch)

	mux.HandleFunc("GET /api/ask/models", s.handleAskModels)
	mux.HandleFunc("POST /api/ask", s.handleAsk)

	mux.HandleFunc("POST /api/claude/hook", guarded(s.handleClaudeHook))
	mux.HandleFunc("GET /api/claude/requests", guarded(s.handleClaudeRequests))
	mux.HandleFunc("POST /api/claude/requests/{id}", guarded(s.handleClaudeAnswer))

	sub, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheHeaders(http.FileServer(http.FS(sub)))))

	// Every other path renders the SPA shell; the client owns routing.
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(indexHTML)
	})
	return mux
}

// cacheHeaders lets the browser keep chunk-*.js forever (esbuild content-hashes
// those names) while always revalidating the two stable bundle names.
func cacheHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(strings.TrimPrefix(r.URL.Path, "/"), "chunk-") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		h.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
