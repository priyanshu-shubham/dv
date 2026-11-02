package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"dv/internal/term"
)

// A terminal's page talks to it over a WebSocket: binary messages are what it
// prints and what the reader types; text ones are JSON, the page's size
// ({cols, rows}) one way, and the other {reset} ahead of all the terminal
// keeps, the end of what it printed before the page came ({attached, resumed,
// ended, exit, epoch, at}), then its shell ending ({ended, exit}) or starting
// again ({started}), and {} while quiet. A page attaching again names where
// it got to, ?epoch=&at=, to be sent only what came after.

const (
	// Well inside a tunnel's idle timeout, and often enough for a page that
	// hears nothing for a few to soon tell the connection has gone dead.
	pingEvery  = 15 * time.Second
	writeLimit = 10 * time.Second
	// The slowest a link is taken to be, for a long replay's deadline.
	slowLink = 8 << 10 // bytes a second
	// Close codes: the terminal was closed, or the page fell behind and is to
	// attach again, which sends it what it missed.
	closedCode = 4000
	behindCode = 4001
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4 << 10,
	WriteBufferSize: 32 << 10,
	// The ticket stands in for the origin check.
	CheckOrigin:       func(*http.Request) bool { return true },
	EnableCompression: true,
}

// place writes a folder as the page shows it: "" for the folder dv has open,
// a path inside it, or one from ~.
func (s *Server) place(dir string) string {
	if dir == "" {
		return ""
	}
	if rel, err := filepath.Rel(s.repo.Root, dir); err == nil && rel == "." {
		return ""
	} else if err == nil && filepath.IsLocal(rel) {
		return filepath.ToSlash(rel)
	}
	return HomeRelative(dir)
}

func (s *Server) terminalInfo(t term.Info) term.Info {
	t.Dir = s.place(t.Dir)
	return t
}

// handleTerminals lists the folder's terminals, and the tmux sessions none of
// them shows, to open one on.
func (s *Server) handleTerminals(w http.ResponseWriter, r *http.Request) {
	list := s.terms.List()
	for i := range list {
		list[i] = s.terminalInfo(list[i])
	}
	sessions := s.terms.Sessions()
	for i := range sessions {
		sessions[i].Dir = s.place(sessions[i].Dir)
	}
	writeJSON(w, http.StatusOK, map[string]any{"terminals": list, "tmux": sessions})
}

// handleNewTerminal opens a terminal, attached to a tmux session if asked:
// {"tmux": {"session", "flags"}}.
func (s *Server) handleNewTerminal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Tmux *struct {
			Session string   `json:"session"`
			Flags   []string `json:"flags"`
		} `json:"tmux"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var t *term.Terminal
	var err error
	if req.Tmux != nil {
		t, err = s.terms.Attach(req.Tmux.Session, req.Tmux.Flags)
	} else {
		t, err = s.terms.Start()
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, s.terminalInfo(t.Info()))
}

func (s *Server) terminal(w http.ResponseWriter, r *http.Request) *term.Terminal {
	t := s.terms.Get(r.PathValue("id"))
	if t == nil {
		writeErr(w, http.StatusNotFound, errors.New("that terminal is closed"))
	}
	return t
}

func (s *Server) handleRenameTerminal(w http.ResponseWriter, r *http.Request) {
	t := s.terminal(w, r)
	if t == nil {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	name := strings.TrimSpace(req.Name)
	if len(name) > 100 {
		name = strings.ToValidUTF8(name[:100], "")
	}
	t.Rename(name)
	writeJSON(w, http.StatusOK, s.terminalInfo(t.Info()))
}

func (s *Server) handleCloseTerminal(w http.ResponseWriter, r *http.Request) {
	if !s.terms.Remove(r.PathValue("id")) {
		writeErr(w, http.StatusNotFound, errors.New("that terminal is closed"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRestartTerminal(w http.ResponseWriter, r *http.Request) {
	t := s.terminal(w, r)
	if t == nil {
		return
	}
	if err := t.Restart(); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, s.terminalInfo(t.Info()))
}

func (s *Server) handleTerminalTicket(w http.ResponseWriter, r *http.Request) {
	t := s.terminal(w, r)
	if t == nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ticket": s.terms.Ticket(t.ID())})
}

func (s *Server) handleAttachTerminal(w http.ResponseWriter, r *http.Request) {
	t := s.terminal(w, r)
	if t == nil {
		return
	}
	if !s.terms.Redeem(r.URL.Query().Get("ticket"), t.ID()) {
		writeErr(w, http.StatusForbidden, errors.New("attaching takes a ticket from POST /api/terminals/{id}/ticket"))
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // answered by Upgrade
	}
	defer conn.Close()
	// The server's read timeout is still on the connection taken over.
	conn.NetConn().SetDeadline(time.Time{})
	conn.SetReadLimit(1 << 20)

	q := r.URL.Query()
	at, _ := strconv.ParseInt(q.Get("at"), 10, 64)
	watcher, back, resumed, mark, state := t.Attach(term.Mark{Epoch: q.Get("epoch"), At: at})
	defer watcher.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		defer conn.Close() // which ends the reading below
		write := func(kind int, p []byte) bool {
			conn.SetWriteDeadline(time.Now().Add(max(writeLimit, time.Duration(len(p)/slowLink)*time.Second)))
			// Keystrokes' echoes are too small to gain from it.
			conn.EnableWriteCompression(len(p) > 256)
			return conn.WriteMessage(kind, p) == nil
		}
		status := func(v map[string]any) bool {
			b, _ := json.Marshal(v)
			return write(websocket.TextMessage, b)
		}
		if !resumed && !status(map[string]any{"reset": true}) {
			return
		}
		// In pieces: the page hears nothing of a message until it is all in,
		// and on a slow link takes a long silence for a dead connection.
		for p := range slices.Chunk(back, 64<<10) {
			if !write(websocket.BinaryMessage, p) {
				return
			}
		}
		// Marks the end of the replay, whose questions to the terminal (where is
		// the cursor?) the page is not to answer.
		if !status(map[string]any{"attached": true, "resumed": resumed, "ended": state.Ended, "exit": state.Exit, "epoch": mark.Epoch, "at": mark.At}) {
			return
		}
		ping := time.NewTicker(pingEvery)
		defer ping.Stop()
		for {
			select {
			case e, ok := <-watcher.C:
				if ok && e.Output != nil {
					// Output that piled up while the last went out goes as one.
					out := e.Output
					var held *term.Event // what came after it that is not output
					closed := false
				gather:
					for len(out) < 64<<10 {
						select {
						case next, open := <-watcher.C:
							switch {
							case !open:
								closed = true
								break gather
							case next.Output != nil:
								// Not into Output's spare room: other pages share it.
								out = append(out[:len(out):len(out)], next.Output...)
							default:
								held = &next
								break gather
							}
						default:
							break gather
						}
					}
					if !write(websocket.BinaryMessage, out) {
						return
					}
					switch {
					case closed:
						ok = false
					case held != nil:
						e = *held
					default:
						continue
					}
				}
				if !ok {
					code, why := behindCode, "fell behind"
					if s.terms.Get(t.ID()) == nil {
						code, why = closedCode, "closed"
					}
					conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, why), time.Now().Add(time.Second))
					return
				}
				if !status(map[string]any{"ended": e.Ended, "exit": e.Exit, "started": e.Started}) {
					return
				}
			case <-ping.C:
				if !write(websocket.TextMessage, []byte("{}")) {
					return
				}
			case <-done:
				return
			}
		}
	}()

	// Back where it was, the page's screen needs no redrawing.
	sized := resumed
	for {
		kind, p, err := conn.ReadMessage()
		if err != nil {
			return
		}
		switch kind {
		case websocket.BinaryMessage:
			t.Write(p)
		case websocket.TextMessage:
			var size struct {
				Cols int `json:"cols"`
				Rows int `json:"rows"`
			}
			if json.Unmarshal(p, &size) == nil {
				t.Resize(size.Cols, size.Rows, !sized)
				sized = true
			}
		}
	}
}
