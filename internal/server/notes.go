package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"dv/internal/store"
)

// handleNotes takes the version first, as handleThreads does.
func (s *Server) handleNotes(w http.ResponseWriter, r *http.Request) {
	version := s.notes.Version()
	writeJSON(w, http.StatusOK, map[string]any{"notes": s.notes.List(), "version": version, "path": s.notes.Path()})
}

func (s *Server) handleAddNote(w http.ResponseWriter, r *http.Request) {
	var req struct {
		store.Note
		Author string `json:"author"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	n, err := s.notes.Add(req.Note, authorOr(req.Author))
	if err != nil {
		writeErr(w, noteStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (s *Server) handlePatchNote(w http.ResponseWriter, r *http.Request) {
	var p store.NotePatch
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	n, err := s.notes.Update(r.PathValue("id"), p)
	if err != nil {
		writeErr(w, noteStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (s *Server) handleDeleteNote(w http.ResponseWriter, r *http.Request) {
	if err := s.notes.Delete(r.PathValue("id")); err != nil {
		writeErr(w, noteStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func noteStatus(err error) int {
	var bad store.NoteError
	switch {
	case errors.Is(err, store.ErrNoNote):
		return http.StatusNotFound
	case errors.As(err, &bad):
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}
