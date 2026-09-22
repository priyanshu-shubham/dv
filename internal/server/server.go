// Package server exposes the review UI and its JSON API. Everything is local:
// the listener binds to loopback, there is no auth layer, and all state lives in
// the repository being reviewed, but for the settings that follow the user
// from one repository to the next.
package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"dv/internal/agent"
	"dv/internal/gitx"
	"dv/internal/notify"
	"dv/internal/permit"
	"dv/internal/store"
	"dv/internal/symindex"
	"dv/internal/update"
)

// The bundle is built by `make build`, not checked in - only the .keep beside
// it is, because go:embed refuses a pattern that matches nothing and would
// otherwise stop the package compiling.
//
//go:embed static/*
var staticFS embed.FS

//go:embed index.html
var indexHTML []byte

// assetVersion is a hash of the two bundles, put on the page's URLs for them.
// A proxy in front of dv can stretch their no-cache into hours - Cloudflare's
// default does - and a reload fetches the page again but not what it loads.
var assetVersion = func() string {
	h := sha256.New()
	for _, name := range []string{"static/bundle.js", "static/bundle.css"} {
		b, err := staticFS.ReadFile(name)
		if err != nil {
			return ""
		}
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}()

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
	prefs  *store.Prefs // the repository's
	user   *store.Prefs // shared by every Server in the process
	index  *symindex.Index
	permit *permit.Broker
	agent  *agent.Manager
	saved  *store.Sessions // what dv keeps of the sessions, the agent's too

	notices     *notify.Folder
	stopNotices context.CancelFunc

	task bool // a hub's one-off task, which has no worktrees
}

// MarkTask has the page treat the folder as a hub's one-off task. It is for
// before the Server serves.
func (s *Server) MarkTask() { s.task = true }

// Open readies a review of the repository or folder holding dir. Its
// sessions' notices go to notices, if any.
func Open(dir string, user *store.Prefs, notices *notify.Folder) (*Server, error) {
	repo, err := gitx.Open(dir)
	if err != nil {
		return nil, err
	}
	st, err := store.Open(repo.Root, repo.Name())
	if err != nil {
		return nil, err
	}
	vw, err := store.OpenViewed(repo.Root)
	if err != nil {
		return nil, err
	}
	sessions, err := store.OpenSessions(repo.Root)
	if err != nil {
		return nil, err
	}
	prefs, err := store.OpenRepoPrefs(repo.Root)
	if err != nil {
		return nil, err
	}
	if repo.IsGit() {
		excludeNotes(repo)
	}
	ix := symindex.New(repo.Root, repo) // built once a page wants it
	broker := permit.New(repo.Root)
	s := &Server{
		repo: repo, store: st, viewed: vw, prefs: prefs, user: user, index: ix,
		permit: broker, agent: agent.New(repo.Root, broker, sessions), saved: sessions, notices: notices,
		stopNotices: func() {},
	}
	if notices != nil {
		notices.Serve(talk{s})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			s.raiseNotices(ctx)
		}()
		// Waited for, so nothing is raised after the folder's notices are settled.
		s.stopNotices = func() {
			cancel()
			<-done
		}
	}
	return s, nil
}

func excludeNotes(repo *gitx.Repo) {
	if added, err := store.EnsureExcluded(repo.CommonDir); err != nil {
		fmt.Fprintln(os.Stderr, "dv: warning: could not update .git/info/exclude:", err)
	} else if added {
		fmt.Println("dv: added /.dv/ to .git/info/exclude so review notes stay out of git")
	}
}

func (s *Server) Repo() *gitx.Repo { return s.repo }

// CommentsPath is the file the review's comments are written to.
func (s *Server) CommentsPath() string { return s.store.Path() }

// Close stops the Claude Code sessions dv is running.
func (s *Server) Close() {
	s.stopNotices()
	if s.notices != nil {
		s.notices.Close()
	}
	s.index.Close()
	s.agent.Close()
}

// Status is what a hub shows of a review it serves.
type Status struct {
	Sessions int `json:"sessions"` // run by dv
	Working  int `json:"working"`
	// Codex marks Codex among the agents working, which are otherwise Claude.
	Codex   bool `json:"codex,omitempty"`
	Claude  bool `json:"claude,omitempty"`
	Waiting int  `json:"waiting"` // prompts on the reader
}

// Asked is a request waiting on the reader, named by its session, for a page
// that shows it away from the conversation.
type Asked struct {
	*permit.Request
	Title string `json:"title,omitempty"`
}

// Waiting lists the requests on the reader, of the sessions dv shows.
func (s *Server) Waiting() []Asked {
	asked := []Asked{}
	for _, r := range s.permit.Waiting() {
		if s.agent.Visible(r.Session) {
			asked = append(asked, Asked{r, s.agent.Title(r.Session)})
		}
	}
	return asked
}

// Live is what a folder's sessions are doing, which a hub passes to the pages
// of its other folders for the notices they raise.
type Live struct {
	Sessions []agent.Activity `json:"sessions"`
	Requests []Asked          `json:"requests"`
}

func (s *Server) Live() Live { return Live{Sessions: s.agent.Activity(), Requests: s.Waiting()} }

// InUse is whether anything goes on in the folder that letting it go would
// stop or miss: a session running, here or in a terminal, or a request waiting.
func (s *Server) InUse() bool {
	for _, a := range s.agent.Activity() {
		if a.Running != "" {
			return true
		}
	}
	return len(s.permit.Waiting()) > 0
}

func (s *Server) Status() Status {
	var st Status
	for _, a := range s.agent.Activity() {
		if a.Running == "dv" {
			st.Sessions++
		}
		if a.Busy {
			st.Working++
			if a.Agent == "codex" {
				st.Codex = true
			} else {
				st.Claude = true
			}
		}
	}
	for _, r := range s.permit.Waiting() {
		if s.agent.Visible(r.Session) {
			st.Waiting++
		}
	}
	return st
}

// Handler builds the route table. base is where the page is served: "" on its
// own, /<slug> in a hub, which takes it off the path before this sees it.
func (s *Server) Handler(base string) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/ping", s.handlePing)
	mux.HandleFunc("GET /api/meta", s.handleMeta)
	mux.HandleFunc("GET /api/diff", s.handleDiffList)
	mux.HandleFunc("GET /api/diff/file", s.handleDiffFile)
	mux.HandleFunc("POST /api/diff/files", s.handleDiffFiles)
	mux.HandleFunc("GET /api/version", s.handleVersion)
	mux.HandleFunc("GET /api/file", s.handleFile)
	mux.HandleFunc("GET /api/media", s.handleMedia)
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

	mux.HandleFunc("GET /api/prefs", Guarded(s.handlePrefs))
	mux.HandleFunc("PATCH /api/prefs", Guarded(s.handleSetPref))

	mux.HandleFunc("GET /api/restart", Guarded(HandleRestart))
	mux.HandleFunc("POST /api/restart", Guarded(HandleRestart))
	mux.HandleFunc("GET /api/update", Guarded(HandleUpdateCheck))
	mux.HandleFunc("POST /api/update", Guarded(HandleUpdateInstall))

	mux.HandleFunc("GET /api/symbols", s.handleSymbols)
	mux.HandleFunc("GET /api/symbols/status", s.handleSymbolStatus)
	mux.HandleFunc("POST /api/symbols/refresh", s.handleSymbolRefresh)
	mux.HandleFunc("GET /api/search", s.handleSearch)

	mux.HandleFunc("POST /api/claude/hook", Guarded(s.handleClaudeHook))
	mux.HandleFunc("GET /api/claude/requests", Guarded(s.handleClaudeRequests))
	mux.HandleFunc("POST /api/claude/requests/{id}", Guarded(s.handleClaudeAnswer))
	mux.HandleFunc("GET /api/claude/hooks", Guarded(s.handleClaudeHooks))
	mux.HandleFunc("POST /api/claude/hooks", Guarded(s.handleClaudeSetHooks))

	mux.HandleFunc("GET /api/agent/sessions", Guarded(s.handleAgentSessions))
	mux.HandleFunc("POST /api/agent/sessions", Guarded(s.handleAgentCreate))
	mux.HandleFunc("GET /api/agent/commands", Guarded(s.handleAgentCommands))
	mux.HandleFunc("POST /api/agent/sessions/{id}/open", Guarded(s.handleAgentOpen))
	mux.HandleFunc("POST /api/agent/sessions/{id}/temporary", Guarded(s.handleAgentTemporary))
	mux.HandleFunc("GET /api/agent/sessions/{id}/events", Guarded(s.handleAgentEvents))
	mux.HandleFunc("POST /api/agent/sessions/{id}/messages", Guarded(s.handleAgentSend))
	mux.HandleFunc("POST /api/agent/sessions/{id}/shell", Guarded(s.handleAgentShell))
	mux.HandleFunc("POST /api/agent/uploads", Guarded(s.handleUpload))
	mux.HandleFunc("POST /api/agent/sessions/{id}/messages/{message}/unqueue", Guarded(s.handleAgentUnqueue))
	mux.HandleFunc("GET /api/agent/sessions/{id}/messages/{message}/images/{n}", Guarded(s.handleAgentImage))
	mux.HandleFunc("POST /api/agent/sessions/{id}/interrupt", Guarded(s.handleAgentInterrupt))
	mux.HandleFunc("POST /api/agent/sessions/{id}/settings", Guarded(s.handleAgentSettings))
	mux.HandleFunc("POST /api/agent/sessions/{id}/rewind", Guarded(s.handleAgentRewind))
	mux.HandleFunc("GET /api/agent/sessions/{id}/prompts", Guarded(s.handleAgentPrompts))
	mux.HandleFunc("GET /api/agent/sessions/{id}/edits/{tool}", Guarded(s.handleAgentEdit))
	mux.HandleFunc("POST /api/agent/sessions/{id}/title", Guarded(s.handleAgentRename))
	mux.HandleFunc("GET /api/agent/sessions/{id}/tools/{tool}", Guarded(s.handleAgentOutput))
	mux.HandleFunc("GET /api/agent/sessions/{id}/tools/{tool}/image", Guarded(s.handleAgentImage))
	mux.HandleFunc("GET /api/agent/sessions/{id}/tools/{tool}/live", Guarded(s.handleAgentTaskOutput))
	mux.HandleFunc("GET /api/agent/sessions/{id}/agents/{tool}/events", Guarded(s.handleAgentSubagentEvents))

	mux.Handle("GET /static/", Static())

	// Every other path renders the SPA shell; the client owns routing.
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		WritePage(w, Boot{Base: base, Prefs: s.allPrefs(), PrefsVersion: s.prefsVersion()})
	})
	return mux
}

// Restart asks main to stop dv and run it again in place, from the binary now
// installed at its path, which is how an update is taken.
var Restart = make(chan struct{}, 1)

// Version is dv's, as main has it: a release's number, or "dev".
var Version = "dev"

// Run is which run of dv answers, for a page to tell that dv restarted and say
// what changed. Builds of one's own are all "dev", so they are told apart by
// the binary's modification time; UI changes only when the page's code did.
type Run struct {
	Started string `json:"started"`
	Version string `json:"version"`
	Binary  string `json:"binary"`
	UI      string `json:"ui"`
}

var started = strconv.FormatInt(time.Now().UnixNano(), 36)

var binaryStamp = func() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	fi, err := os.Stat(exe)
	if err != nil {
		return ""
	}
	return strconv.FormatInt(fi.ModTime().UnixNano(), 36)
}()

func thisRun() Run { return Run{started, Version, binaryStamp, assetVersion} }

// HandleRestart says which run this is, and on POST restarts, once the binary
// installed has shown in a trial that it starts; if not, this dv keeps
// running. It is the whole process's, a hub's folders and all, so a hub
// serves it at its root too.
func HandleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		if err := update.Try(r.Context(), ""); err != nil {
			writeErr(w, http.StatusInternalServerError, fmt.Errorf("%w. This one keeps running.", err))
			return
		}
		restart()
	}
	writeJSON(w, http.StatusOK, thisRun())
}

func restart() {
	select {
	case Restart <- struct{}{}:
	default:
	}
}

// HandleUpdateCheck says whether a newer release than this dv is out. Builds
// of one's own, "dev", are not checked.
func HandleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if Version == "dev" {
		writeJSON(w, http.StatusOK, map[string]string{"version": Version})
		return
	}
	latest, err := update.Latest(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": Version, "latest": latest, "newer": update.Newer(latest, Version)})
}

// HandleUpdateInstall installs the release asked for, {"version"}, over this
// dv and restarts into it - or, failing its trial, puts the binary it replaced
// back. Newer is also what keeps the version to digits and dots, as it goes
// into the download's URL.
func HandleUpdateInstall(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !update.Newer(req.Version, Version) {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("%q is not a release newer than this dv, %s", req.Version, Version))
		return
	}
	if err := update.Install(r.Context(), req.Version); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := update.Try(r.Context(), req.Version); err != nil {
		if back := update.Rollback(); back != nil {
			err = fmt.Errorf("%w. Putting v%s back failed too: %v", err, Version, back)
		} else {
			err = fmt.Errorf("%w. dv stays on v%s.", err, Version)
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	update.Commit()
	restart()
	writeJSON(w, http.StatusOK, thisRun())
}

// Static serves the UI bundle.
func Static() http.Handler {
	sub, _ := fs.Sub(staticFS, "static")
	return http.StripPrefix("/static/", cacheHeaders(http.FileServer(http.FS(sub))))
}

// Boot is what the page needs before its first request: where its API is, and
// the settings to draw with, so it does not flash the defaults first.
type Boot struct {
	Page         string                                `json:"page,omitempty"` // "hub" for the hub's own
	Base         string                                `json:"base"`
	Prefs        map[string]map[string]json.RawMessage `json:"prefs"`
	PrefsVersion string                                `json:"prefsVersion,omitempty"`
	Run          Run                                   `json:"run"` // filled in by WritePage
}

// WritePage sends the SPA shell with boot in it.
func WritePage(w http.ResponseWriter, boot Boot) {
	boot.Run = thisRun()
	// Marshal escapes <, > and &, so nothing in a value can close the script.
	b, err := json.Marshal(boot)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	const root = `<div id="root"></div>`
	script := `<script id="dv-boot" type="application/json">` + string(b) + "</script>\n"
	page := bytes.Replace(indexHTML, []byte(root), []byte(script+root), 1)
	if assetVersion != "" {
		for _, src := range []string{`"/static/bundle.js"`, `"/static/bundle.css"`} {
			page = bytes.Replace(page, []byte(src), []byte(src[:len(src)-1]+"?v="+assetVersion+`"`), 1)
		}
	}
	// The theme is on <html> from the first frame, before any script has run.
	var theme string
	var settings struct {
		CodeColors string `json:"codeColors"`
	}
	json.Unmarshal(boot.Prefs["user"]["theme"], &theme)
	json.Unmarshal(boot.Prefs["user"]["settings"], &settings)
	attrs := ""
	if word.MatchString(theme) {
		attrs += ` data-theme="` + theme + `"`
	}
	if word.MatchString(settings.CodeColors) {
		attrs += ` data-code="` + settings.CodeColors + `"`
	}
	page = bytes.Replace(page, []byte(`<html lang="en">`), []byte(`<html lang="en"`+attrs+`>`), 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(page)
}

var word = regexp.MustCompile(`^[a-z]+$`)

// Answers reports whether a dv reviewing root answers at url, as one
// announced there may have stopped without taking its record down.
func Answers(url, root string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/api/ping", nil)
	if err != nil {
		return false
	}
	store.MarkLocal(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var got struct {
		Root string `json:"root"`
	}
	return resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(&got) == nil && got.Root == root
}

// cacheHeaders lets the browser keep chunk-*.js forever (esbuild content-hashes
// those names), and the two bundles when asked for at this build's version.
// Anything else, a bundle at another version included, is revalidated.
func cacheHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := strings.HasPrefix(strings.TrimPrefix(r.URL.Path, "/"), "chunk-")
		if chunk || assetVersion != "" && r.URL.Query().Get("v") == assetVersion {
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
