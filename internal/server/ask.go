package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"dv/internal/ask"
	"dv/internal/gitx"
)

// askRequest is what the Ask panel posts. A nil line range means "the whole
// file's diff"; otherwise the prompt is narrowed to the selected span.
type askRequest struct {
	Scope     string `json:"scope"`
	Rev       string `json:"rev"`
	File      string `json:"file"`
	Side      string `json:"side"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
	Question  string `json:"question"`
	Model     string `json:"model"`
	SessionID string `json:"sessionId"` // continue an existing conversation
}

func (s *Server) handleAskModels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"models":    ask.Models,
		"default":   ask.DefaultModel,
		"available": ask.Available(),
	})
}

// handleAsk streams the model's answer as server-sent events. It is a POST
// because the prompt context can be large, so the client reads the stream from
// the fetch response body rather than through EventSource.
func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	var req askRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.Question) == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("ask a question first"))
		return
	}
	if !ask.Available() {
		writeErr(w, http.StatusPreconditionFailed, fmt.Errorf("the `claude` CLI is not on PATH"))
		return
	}

	prompt, err := s.buildAskPrompt(req)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func(ev ask.Event) {
		b, err := json.Marshal(ev)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}

	if err := ask.Run(r.Context(), s.repo.Root, req.Model, prompt, req.SessionID, send); err != nil && r.Context().Err() == nil {
		send(ask.Event{Type: "error", Text: err.Error()})
	}
}

// buildAskPrompt assembles the diff slice and framing the model sees. A
// follow-up in an existing session gets the bare question: the diff is already
// in that session's context, and re-sending it would only cost tokens.
func (s *Server) buildAskPrompt(req askRequest) (string, error) {
	if req.SessionID != "" {
		return strings.TrimSpace(req.Question), nil
	}
	sc, err := s.repo.ResolveScope(req.Scope, req.Rev)
	if err != nil {
		return "", err
	}
	files, err := s.repo.Files(sc)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Repository: %s\nComparing: %s (%s)\n\n", s.repo.Name(), sc.Label, sc.Desc)

	if req.File == "" {
		// No file selected: summarise the whole review so the question can be
		// about the change as a whole.
		fmt.Fprintf(&b, "The comparison touches %d file(s):\n", len(files))
		for _, f := range files {
			fmt.Fprintf(&b, "  %s %s (+%d -%d)\n", f.Status, f.Path, f.Additions, f.Deletions)
		}
		b.WriteString("\nUse Read and Grep to inspect anything you need.\n")
	} else {
		var entry *gitx.FileEntry
		for i := range files {
			if files[i].Path == req.File {
				entry = &files[i]
				break
			}
		}
		if entry == nil {
			return "", fmt.Errorf("%s is not part of this diff", req.File)
		}
		fd, err := s.repo.Diff(sc, *entry)
		if err != nil {
			return "", err
		}

		var focus *gitx.Focus
		if req.StartLine > 0 {
			side := req.Side
			if side != "old" {
				side = "new"
			}
			end := req.EndLine
			if end < req.StartLine {
				end = req.StartLine
			}
			focus = &gitx.Focus{Side: side, Start: req.StartLine, End: end}
			fmt.Fprintf(&b, "The reviewer is looking at %s, lines %d-%d of the %s side.\n\n",
				req.File, req.StartLine, end, side)
		} else {
			fmt.Fprintf(&b, "The reviewer is looking at the whole diff of %s.\n\n", req.File)
		}

		// Focused asks get wider context, since the surrounding code is usually
		// what the question is really about.
		ctxLines := 6
		if focus != nil {
			ctxLines = 12
		}
		b.WriteString("```diff\n")
		b.WriteString(gitx.RenderUnified(fd, ctxLines, focus))
		b.WriteString("```\n")
	}

	fmt.Fprintf(&b, "\nQuestion: %s\n", strings.TrimSpace(req.Question))
	return b.String(), nil
}
