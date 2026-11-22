package server

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"dv/internal/agent"
	"dv/internal/gitx"
	"dv/internal/store"
)

// A ticket's agents, each an ordinary session the reader can talk to.
const (
	roleScope        = "scope"
	rolePlanner      = "planner"
	rolePlanReviewer = "plan-reviewer"
	roleBuilder      = "builder"
	roleReviewer     = "reviewer"
)

var roleTitle = map[string]string{roleScope: "Scope", rolePlanner: "Plan", rolePlanReviewer: "Plan review", roleBuilder: "Build", roleReviewer: "Review"}

// drivesBoard says whether this dv runs the board's agents. Only the main
// checkout's does, so two dvs never drive one ticket.
func (s *Server) drivesBoard() bool { return s.repo.IsGit() && s.repo.MainRoot() == "" }

var errNotDriver = errors.New("the board is run by the dv of the repository's main checkout; open it there")

// boardEnv is what a ticket's session needs for `dv ticket` to know it.
func (s *Server) boardEnv(session string) []string {
	if s.board == nil {
		return nil
	}
	for _, t := range s.board.List() {
		for role, id := range t.Sessions {
			if id == session {
				env := []string{"DV_ROOT=" + s.repo.Root, "DV_TICKET=" + t.ID, "DV_ROLE=" + role}
				// `dv ticket` is this dv's, whatever other dv the PATH has.
				if exe, err := os.Executable(); err == nil {
					env = append(env, "PATH="+filepath.Dir(exe)+string(os.PathListSeparator)+os.Getenv("PATH"))
				}
				return env
			}
		}
	}
	return nil
}

// boardSession is which ticket a session works on, and as what.
type boardSession struct {
	Ticket string `json:"ticket"`
	Title  string `json:"title"`
	Role   string `json:"role"`
	Round  int    `json:"round,omitempty"` // a reviewer's, from 1
}

// boardSessions are the sessions of the board's tickets, earlier rounds'
// reviewers too, by session ID: the session list keeps them out of the way.
func (s *Server) boardSessions() map[string]boardSession {
	out := map[string]boardSession{}
	if s.board == nil {
		return out
	}
	for _, t := range s.board.List() {
		for role, id := range t.Sessions {
			out[id] = boardSession{Ticket: t.ID, Title: t.Title, Role: role}
		}
		for i, r := range t.Rounds {
			if r.Session != "" {
				out[r.Session] = boardSession{Ticket: t.ID, Title: t.Title, Role: roleReviewer, Round: i + 1}
			}
		}
		for i, r := range t.EarlierPlanReviews {
			if r.Session != "" {
				out[r.Session] = boardSession{Ticket: t.ID, Title: t.Title, Role: rolePlanReviewer, Round: i + 1}
			}
		}
		if r := t.PlanReview; r != nil && r.Session != "" && len(t.EarlierPlanReviews) > 0 {
			out[r.Session] = boardSession{Ticket: t.ID, Title: t.Title, Role: rolePlanReviewer, Round: len(t.EarlierPlanReviews) + 1}
		}
	}
	return out
}

// ownerOf is the role whose session the ticket's stage is with.
func ownerOf(t store.Ticket) string {
	switch t.Stage {
	case "scope":
		return roleScope
	case "plan":
		if t.PlanReview != nil && t.PlanReview.Phase == "review" {
			return rolePlanReviewer
		}
		return rolePlanner
	case "build", "review", "merged":
		return roleBuilder
	case "agent-review":
		if r := lastRound(t); r != nil && r.Phase == "fix" {
			return roleBuilder
		}
		return roleReviewer
	}
	return ""
}

func lastRound(t store.Ticket) *store.Round {
	if len(t.Rounds) == 0 {
		return nil
	}
	return &t.Rounds[len(t.Rounds)-1]
}

// ticketView is a ticket as the page shows it: with what its agents are doing
// now, which no file holds.
type ticketView struct {
	store.Ticket
	Owner    string   `json:"owner,omitempty"`
	Working  bool     `json:"working,omitempty"`
	Asking   bool     `json:"asking,omitempty"` // a session waits on the reader's answer or permission
	Blocked  []string `json:"blocked,omitempty"`
	Children []string `json:"children,omitempty"`
}

func (s *Server) ticketViews() []ticketView {
	tickets := s.board.List()
	busy := map[string]bool{}
	for _, a := range s.agent.Activity() {
		busy[a.ID] = a.Busy
	}
	waiting := map[string]bool{}
	for _, r := range s.permit.Waiting() {
		waiting[r.Session] = true
	}
	merged := map[string]bool{}
	for _, t := range tickets {
		merged[t.ID] = t.Stage == "merged"
	}
	out := make([]ticketView, len(tickets))
	for i, t := range tickets {
		v := ticketView{Ticket: t, Owner: ownerOf(t)}
		v.Working = busy[t.Sessions[v.Owner]]
		for _, id := range t.Sessions {
			v.Asking = v.Asking || waiting[id]
		}
		for _, a := range t.After {
			if !merged[a] {
				v.Blocked = append(v.Blocked, a)
			}
		}
		for _, c := range tickets {
			if c.Parent == t.ID {
				v.Children = append(v.Children, c.ID)
			}
		}
		out[i] = v
	}
	return out
}

func (s *Server) handleBoard(w http.ResponseWriter, r *http.Request) {
	version := s.board.Version()
	writeJSON(w, http.StatusOK, map[string]any{
		"tickets": s.ticketViews(), "version": version, "drives": s.drivesBoard(),
		"templates": ReviewTemplates, "base": s.repo.DefaultBranch(),
	})
}

// handleTicket is one ticket with its documents, which change apart from the
// board file, so the page asks for them while it shows the ticket.
func (s *Server) handleTicket(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var view *ticketView
	for _, v := range s.ticketViews() {
		if v.ID == id {
			view = &v
			break
		}
	}
	if view == nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("%w: %s", store.ErrNoTicket, id))
		return
	}
	ticket, err := s.board.Doc(id, "ticket")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	plan, err := s.board.Doc(id, "plan")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ticket": view, "docs": map[string]string{"ticket": ticket, "plan": plan},
		"versions": map[string][]store.DocVersion{"ticket": s.board.DocVersions(id, "ticket"), "plan": s.board.DocVersions(id, "plan")},
	})
}

func (s *Server) handleAddTicket(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title   string   `json:"title"`
		Request string   `json:"request"`
		Labels  []string `json:"labels"`
		Stage   string   `json:"stage"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	t, err := s.board.Add(store.Ticket{Title: req.Title, Request: req.Request, Labels: req.Labels})
	if err != nil {
		writeErr(w, ticketStatus(err), err)
		return
	}
	if req.Stage != "" && req.Stage != "inbox" {
		if t, err = s.moveTicket(t.ID, req.Stage, ""); err != nil {
			writeErr(w, ticketStatus(err), err)
			return
		}
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handlePatchTicket(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Title        *string            `json:"title"`
		Request      *string            `json:"request"`
		Labels       *[]string          `json:"labels"`
		Archived     *bool              `json:"archived"`
		NoPlanReview *bool              `json:"noPlanReview"`
		Templates    *[]string          `json:"templates"`
		Model        *map[string]string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	t, err := s.board.Update(r.PathValue("id"), func(t *store.Ticket) error {
		if p.Title != nil {
			t.Title = *p.Title
		}
		if p.Request != nil {
			t.Request = strings.TrimSpace(*p.Request)
		}
		if p.Labels != nil {
			t.Labels = *p.Labels
		}
		if p.Archived != nil {
			t.Archived = *p.Archived
		}
		if p.NoPlanReview != nil {
			t.NoPlanReview = *p.NoPlanReview
		}
		if p.Templates != nil {
			t.Templates = slices.DeleteFunc(slices.Clone(*p.Templates), func(k string) bool { return !slices.Contains(ReviewTemplates, k) })
		}
		if p.Model != nil {
			t.Model = *p.Model
		}
		return nil
	})
	if err != nil {
		writeErr(w, ticketStatus(err), err)
		return
	}
	// Archived, its agents stop; its worktree stays, for a restore to go on.
	if t.Archived {
		for _, id := range t.Sessions {
			s.agent.SetOpen(id, false)
		}
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleDeleteTicket(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, err := s.board.Get(id)
	if err == nil && t.Worktree != "" {
		err = s.repo.RemoveWorktree(t.Worktree, t.Branch)
	}
	if err == nil {
		for _, sid := range t.Sessions {
			s.agent.SetOpen(sid, false)
		}
		err = s.board.Delete(id)
	}
	if err != nil {
		writeErr(w, ticketStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleMoveTicket(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Stage string `json:"stage"`
		Note  string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	t, err := s.moveTicket(r.PathValue("id"), req.Stage, strings.TrimSpace(req.Note))
	if err != nil {
		writeErr(w, ticketStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// docAuthor is the role that writes a ticket's document, and the stage its
// writing is done in.
var docAuthor = map[string][2]string{"ticket": {roleScope, "scope"}, "plan": {rolePlanner, "plan"}}

// handleCommentTicket sends the reader's comments on the ticket or the plan to
// the agent writing it, to revise it and hand it back.
func (s *Server) handleCommentTicket(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Doc      string `json:"doc"`
		Comments []struct {
			Quote string `json:"quote"`
			Text  string `json:"text"`
		} `json:"comments"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	author, ok := docAuthor[req.Doc]
	if !ok || len(req.Comments) == 0 {
		writeErr(w, http.StatusBadRequest, store.TicketError("comment on the ticket or the plan"))
		return
	}
	if !s.drivesBoard() {
		writeErr(w, http.StatusConflict, errNotDriver)
		return
	}
	s.boardMu.Lock()
	defer s.boardMu.Unlock()
	t, err := s.board.Get(r.PathValue("id"))
	if err == nil && (t.Stage != author[1] || t.Sessions[author[0]] == "") {
		err = store.TicketError(fmt.Sprintf("the %s is changed in %s; send the ticket back there to change it", req.Doc, author[1]))
	}
	if err != nil {
		writeErr(w, ticketStatus(err), err)
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "The reader commented on the %s:\n\n", req.Doc)
	for _, c := range req.Comments {
		if q := strings.TrimSpace(c.Quote); q != "" {
			b.WriteString("> " + strings.ReplaceAll(q, "\n", "\n> ") + "\n")
		}
		b.WriteString(strings.TrimSpace(c.Text) + "\n\n")
	}
	fmt.Fprintf(&b, "Revise the %s for each comment, or answer it where it is a question, then save it with `dv ticket write %s` and run `dv ticket ready` again.", req.Doc, req.Doc)
	if err := s.tell(t, author[0], b.String()); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	t, err = s.board.Update(t.ID, func(x *store.Ticket) error {
		x.Ready, x.Status = false, "Revising after your comments"
		return nil
	})
	if err != nil {
		writeErr(w, ticketStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// handleRunRound starts another round of agent review, as the ticket's
// templates and reviewer model say.
func (s *Server) handleRunRound(w http.ResponseWriter, r *http.Request) {
	if !s.drivesBoard() {
		writeErr(w, http.StatusConflict, errNotDriver)
		return
	}
	s.boardMu.Lock()
	defer s.boardMu.Unlock()
	t, err := s.board.Update(r.PathValue("id"), func(t *store.Ticket) error {
		if t.Worktree == "" {
			return store.TicketError("there is nothing built to review yet")
		}
		t.Stage, t.Ready, t.Problem, t.Merging = "agent-review", false, "", false
		return nil
	})
	if err == nil {
		t, err = s.startRound(t)
	}
	if err != nil {
		writeErr(w, ticketStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func ticketStatus(err error) int {
	var bad store.TicketError
	switch {
	case errors.Is(err, store.ErrNoTicket):
		return http.StatusNotFound
	case errors.As(err, &bad), errors.Is(err, errNotDriver):
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

// moveTicket is the reader moving a ticket. Each stage's agent starts as the
// ticket enters it; going back, the agent there is told why with note. Into
// merged is the reader's approval, carried out once CI passes.
func (s *Server) moveTicket(id, stage, note string) (store.Ticket, error) {
	if !s.drivesBoard() {
		return store.Ticket{}, errNotDriver
	}
	if store.StageIndex(stage) < 0 {
		return store.Ticket{}, store.TicketError(fmt.Sprintf("stage %q is not one of %s", stage, strings.Join(store.Stages, ", ")))
	}
	s.boardMu.Lock()
	defer s.boardMu.Unlock()
	t, err := s.board.Get(id)
	if err != nil || t.Stage == stage {
		return t, err
	}
	if t.Archived {
		return t, store.TicketError("restore it from the archive first")
	}
	if stage == "merged" && s.repo.Origin() == nil {
		return s.mergeHere(t)
	}
	if stage == "merged" {
		return s.board.Update(id, func(t *store.Ticket) error {
			if t.PR == 0 {
				return store.TicketError("it has no pull request to merge yet")
			}
			t.Merging, t.Problem = true, ""
			return nil
		})
	}
	from := t.Stage
	if from == "scope" && stage == "plan" && s.hasChildren(id) {
		return s.board.Update(id, func(t *store.Ticket) error {
			t.Split, t.Ready, t.Problem, t.Status = true, false, "", ""
			return nil
		})
	}
	t, err = s.board.Update(id, func(t *store.Ticket) error {
		if from == "inbox" {
			t.Started = stage
		}
		if store.StageIndex(stage) < store.StageIndex(from) && store.StageIndex(from) > store.StageIndex(t.Reached) {
			t.Reached = from
		}
		t.Stage, t.Ready, t.Problem, t.Merging, t.Status = stage, false, "", false, ""
		return nil
	})
	if err != nil {
		return t, err
	}
	return s.enter(t, from, note)
}

func (s *Server) hasChildren(id string) bool {
	return slices.ContainsFunc(s.board.List(), func(t store.Ticket) bool { return t.Parent == id })
}

// enter starts or tells the agent of the stage a ticket just entered. A
// failure stays on the ticket as its problem, as well as being returned.
func (s *Server) enter(t store.Ticket, from, note string) (store.Ticket, error) {
	back := store.StageIndex(t.Stage) < store.StageIndex(from)
	told := func(what string) string {
		if note == "" {
			return what
		}
		return what + "\n\nThe reader says:\n" + note
	}
	var err error
	// Every stage past the inbox works in the ticket's worktree, so each
	// reads the code the builder will change, whatever the main checkout has.
	if t.Stage != "inbox" && t.Stage != "merged" {
		if t, err = s.ensureWorktree(t); err != nil {
			t, _ = s.board.Update(t.ID, func(x *store.Ticket) error { x.Problem = err.Error(); return nil })
			return t, err
		}
	}
	switch t.Stage {
	case "scope":
		if back && t.Sessions[roleScope] != "" {
			err = s.tell(t, roleScope, told("The reader sent the ticket back to you. Change it as they ask, save it, and run `dv ticket ready` again."))
		} else {
			doc, _ := s.board.Doc(t.ID, "ticket")
			t, err = s.startRole(t, roleScope, t.Worktree, told(scopePrompt(t, s.baseRef(false), doc)))
		}
	case "plan":
		if back && t.Sessions[rolePlanner] != "" {
			err = s.tell(t, rolePlanner, told("The reader sent the ticket back to planning. Change the plan as they ask, save it, and run `dv ticket ready` again."))
		} else {
			// A fresh planner's plan gets a review of its own.
			if t, err = s.board.Update(t.ID, func(x *store.Ticket) error { x.SetAsidePlanReview(); return nil }); err == nil {
				t, err = s.startRole(t, rolePlanner, t.Worktree, told(s.planPromptFor(t)))
			}
		}
	case "build":
		switch {
		case t.Sessions[roleBuilder] == "":
			t, err = s.startRole(t, roleBuilder, t.Worktree, told(buildPrompt(t, s.baseRef(false))))
		case back:
			err = s.tell(t, roleBuilder, told("The reader sent the work back to you. Make the changes they ask for, commit, and run `dv ticket ready` again."))
		default:
			err = s.tell(t, roleBuilder, told("The plan was changed and approved again. Read it with `dv ticket show plan`, carry on from where you are, and run `dv ticket ready` when done."))
		}
	case "agent-review":
		t, err = s.startRound(t)
	case "review":
		t, err = s.publish(t)
	}
	if err != nil {
		t, _ = s.board.Update(t.ID, func(x *store.Ticket) error { x.Problem = err.Error(); return nil })
	}
	return t, err
}

func (s *Server) planPromptFor(t store.Ticket) string {
	ticket, _ := s.board.Doc(t.ID, "ticket")
	plan, _ := s.board.Doc(t.ID, "plan")
	var parent string
	if t.Parent != "" {
		parent, _ = s.board.Doc(t.Parent, "ticket")
	}
	return planPrompt(t, s.baseRef(false), parent, ticket, plan)
}

// startRole starts a fresh session for the ticket's role, working in cwd, and
// sends it prompt.
func (s *Server) startRole(t store.Ticket, role, cwd, prompt string) (store.Ticket, error) {
	id, err := s.agent.CreateIn(cwd)
	if err != nil {
		return t, err
	}
	model, effort := s.picks(id, t.Model[role], "")
	mode := "auto"
	if err := s.agent.Configure(id, model, &mode, effort); err != nil {
		return t, err
	}
	s.agent.Rename(id, fmt.Sprintf("%s %s · %s", t.ID, roleTitle[role], t.Title))
	t, err = s.board.Update(t.ID, func(x *store.Ticket) error {
		if x.Sessions == nil {
			x.Sessions = map[string]string{}
		}
		x.Sessions[role] = id
		return nil
	})
	if err != nil {
		return t, err
	}
	images := s.ticketImages(t)
	if len(images) > 0 {
		prompt += fmt.Sprintf("\nThe reader attached %d image(s) to the ticket; they come with this message.\n", len(images))
	}
	_, err = s.sendVia(id, "", prompt, images, "")
	return t, err
}

// ticketImages are the pictures the ticket holds, as a message takes them; one
// Claude can't take is left out.
func (s *Server) ticketImages(t store.Ticket) []agent.Image {
	var out []agent.Image
	for _, name := range t.Images {
		data, err := os.ReadFile(s.board.ImagePath(t.ID, name))
		img := agent.Image{MediaType: store.ImageType(name), Data: data}
		if err == nil && img.Check() == nil {
			out = append(out, img)
		}
	}
	return out
}

func (s *Server) handleAddTicketImage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MediaType string `json:"mediaType"`
		Data      []byte `json:"data"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	name, err := s.board.AddImage(r.PathValue("id"), req.MediaType, req.Data)
	if err != nil {
		writeErr(w, ticketStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": name})
}

func (s *Server) handleTicketImage(w http.ResponseWriter, r *http.Request) {
	id, name := r.PathValue("id"), r.PathValue("name")
	t, err := s.board.Get(id)
	if err != nil || !slices.Contains(t.Images, name) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", store.ImageType(name))
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	http.ServeFile(w, r, s.board.ImagePath(id, name))
}

func (s *Server) handleDeleteTicketImage(w http.ResponseWriter, r *http.Request) {
	if err := s.board.RemoveImage(r.PathValue("id"), r.PathValue("name")); err != nil {
		writeErr(w, ticketStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// tell sends the ticket's role a message, starting it again if it stopped.
func (s *Server) tell(t store.Ticket, role, text string) error {
	id := t.Sessions[role]
	if id == "" {
		return fmt.Errorf("%s has no %s session", t.ID, role)
	}
	_, err := s.sendVia(id, "", text, nil, "")
	return err
}

var notBranchy = regexp.MustCompile(`[^a-z0-9]+`)

// baseRef is what a ticket's branch is made from and reviewed against: the
// trunk as the remote has it, fetched first with fetch, or the local trunk in
// a repository with no remote.
func (s *Server) baseRef(fetch bool) string {
	trunk := cmp.Or(s.repo.DefaultBranch(), "HEAD")
	o := s.repo.Origin()
	if o == nil || trunk == "HEAD" {
		return trunk
	}
	if fetch {
		if err := s.repo.FetchStale(30 * time.Second); err != nil {
			fmt.Fprintln(os.Stderr, "dv: warning: board: fetching", o.Name, "failed:", err)
		}
	}
	if ref := o.Name + "/" + trunk; s.repo.RefExists(ref) {
		return ref
	}
	return trunk
}

// ensureWorktree gives the ticket its worktree and branch, inside the
// repository's .dv so its sessions are the repository's too.
func (s *Server) ensureWorktree(t store.Ticket) (store.Ticket, error) {
	if t.Worktree != "" {
		if _, err := os.Stat(t.Worktree); err == nil {
			return t, nil
		}
	}
	slug := strings.Trim(notBranchy.ReplaceAllString(strings.ToLower(t.Title), "-"), "-")
	if len(slug) > 32 {
		slug = strings.TrimRight(slug[:32], "-")
	}
	branch := cmp.Or(t.Branch, strings.ToLower(t.ID)+"-"+slug)
	path := filepath.Join(s.repo.Root, ".dv", "worktrees", t.ID)
	if err := s.repo.AddWorktree(path, branch, s.baseRef(true)); err != nil {
		return t, fmt.Errorf("could not make its worktree: %w", err)
	}
	return s.board.Update(t.ID, func(x *store.Ticket) error {
		x.Worktree, x.Branch = path, branch
		return nil
	})
}

// startRound has a fresh reviewer go over the branch.
func (s *Server) startRound(t store.Ticket) (store.Ticket, error) {
	templates := t.Templates
	if len(templates) == 0 {
		templates = ReviewTemplates
	}
	t, err := s.board.Update(t.ID, func(x *store.Ticket) error {
		x.Rounds = append(x.Rounds, store.Round{Templates: templates, Model: x.Model[roleReviewer], Phase: "review", At: time.Now().UTC().Truncate(time.Second)})
		return nil
	})
	if err != nil {
		return t, err
	}
	t, err = s.startRole(t, roleReviewer, t.Worktree, reviewPrompt(t, s.baseRef(false), templates))
	if err != nil {
		return t, err
	}
	return s.board.Update(t.ID, func(x *store.Ticket) error {
		x.Rounds[len(x.Rounds)-1].Session = x.Sessions[roleReviewer]
		return nil
	})
}

// publish pushes the ticket's branch and opens its pull request if it has
// none.
func (s *Server) publish(t store.Ticket) (store.Ticket, error) {
	if t.Worktree == "" || s.repo.Origin() == nil {
		return t, nil
	}
	if err := gitx.Push(t.Worktree, t.Branch); err != nil {
		return t, fmt.Errorf("could not push %s: %w", t.Branch, err)
	}
	if t.PR != 0 {
		return t, nil
	}
	ticket, _ := s.board.Doc(t.ID, "ticket")
	plan, _ := s.board.Doc(t.ID, "plan")
	body := strings.TrimSpace(cmpOrStr(ticket, t.Request))
	if plan != "" {
		body += "\n\n<details><summary>Plan</summary>\n\n" + strings.TrimSpace(plan) + "\n\n</details>"
	}
	n, err := s.repo.CreatePR(t.Branch, cmp.Or(s.repo.DefaultBranch(), "main"), t.Title, body)
	if err != nil {
		return t, fmt.Errorf("could not open its pull request: %w", err)
	}
	return s.board.Update(t.ID, func(x *store.Ticket) error {
		x.PR, x.CI = n, "pending"
		return nil
	})
}

// mergeHere merges a ticket's branch into the trunk checked out in the main
// checkout, for a repository with no remote to open a pull request on.
func (s *Server) mergeHere(t store.Ticket) (store.Ticket, error) {
	trunk := s.repo.DefaultBranch()
	if t.Branch == "" {
		return t, store.TicketError("there is nothing built to merge yet")
	}
	if at := gitx.BranchAt(s.repo.Root); at != trunk {
		return t, store.TicketError(fmt.Sprintf("the repository has no remote, so dv merges here: check out %s first (%s is checked out)", trunk, cmp.Or(at, "no branch")))
	}
	if err := s.repo.Merge(t.Branch); err != nil {
		return t, err
	}
	t, err := s.board.Update(t.ID, func(x *store.Ticket) error {
		x.Stage, x.Merging, x.Problem = "merged", false, ""
		return nil
	})
	if err == nil {
		s.retire(t)
	}
	return t, err
}

// ---- what the agents report, through `dv ticket` ----

type agentReport struct {
	Role string   `json:"role"`
	Verb string   `json:"verb"`
	Args []string `json:"args"`
	Body string   `json:"body"`
	Flag string   `json:"flag"` // --after for split, --source for answer
}

func (s *Server) handleTicketAgent(w http.ResponseWriter, r *http.Request) {
	var rep agentReport
	if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	out, err := s.report(r.PathValue("id"), rep)
	if err != nil {
		writeErr(w, ticketStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"out": out})
}

func (s *Server) report(id string, rep agentReport) (string, error) {
	if rep.Verb == "show" {
		return s.showTicket(id, rep.Args)
	}
	if !s.drivesBoard() {
		return "", errNotDriver
	}
	s.boardMu.Lock()
	defer s.boardMu.Unlock()
	t, err := s.board.Get(id)
	if err != nil {
		return "", err
	}
	arg := strings.TrimSpace(strings.Join(rep.Args, " "))
	wrong := store.TicketError(fmt.Sprintf("the %s can't %s while the ticket is in %s", rep.Role, rep.Verb, t.Stage))
	switch rep.Verb {
	case "status":
		_, err = s.board.Update(id, func(x *store.Ticket) error { x.Status = arg; return nil })
		return "Status set.", err
	case "title":
		if rep.Role != roleScope && rep.Role != rolePlanner {
			return "", wrong
		}
		_, err = s.board.Update(id, func(x *store.Ticket) error { x.Title = arg; return nil })
		return "Renamed.", err
	case "write":
		doc := map[string]string{roleScope: "ticket", rolePlanner: "plan"}[rep.Role]
		if doc == "" || arg != doc {
			return "", store.TicketError(fmt.Sprintf("the %s writes the %s, not %q", rep.Role, cmp.Or(doc, "nothing"), arg))
		}
		if strings.TrimSpace(rep.Body) == "" {
			return "", store.TicketError("nothing came on stdin to write")
		}
		return "Saved the " + doc + ".", s.board.SaveDoc(id, doc, rep.Body)
	case "split":
		if rep.Role != roleScope {
			return "", wrong
		}
		var after []string
		if rep.Flag != "" {
			after = []string{rep.Flag}
		}
		c, err := s.board.Add(store.Ticket{Title: arg, Request: strings.TrimSpace(rep.Body), Parent: id, After: after, Stage: "plan", Started: "plan"})
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(rep.Body) != "" {
			if err := s.board.SaveDoc(c.ID, "ticket", rep.Body); err != nil {
				return "", err
			}
		}
		return "Filed " + c.ID + ". Its planning starts once the reader approves the split.", nil
	case "ready":
		return s.ready(t, rep.Role, arg)
	case "ask":
		if rep.Role != roleBuilder || t.Stage != "build" {
			return "", wrong
		}
		if arg == "" {
			return "", store.TicketError("say what the plan doesn't settle")
		}
		t, err = s.board.Update(id, func(x *store.Ticket) error {
			x.Question = &store.Question{Text: arg, At: time.Now().UTC().Truncate(time.Second)}
			x.Stage, x.Reached, x.Status = "plan", "build", ""
			return nil
		})
		if err != nil {
			return "", err
		}
		if t.Sessions[rolePlanner] == "" {
			_, err = s.startRole(t, rolePlanner, cmp.Or(t.Worktree, s.repo.Root), s.planPromptFor(t)+"\n\n"+questionPrompt(t, arg))
		} else {
			err = s.tell(t, rolePlanner, questionPrompt(t, arg))
		}
		return "Sent to the planner. Stop here; its answer comes as your next message.", err
	case "answer":
		if rep.Role != rolePlanner || t.Question == nil {
			return "", store.TicketError("there is no question from the builder to answer")
		}
		if arg == "" || rep.Flag == "" {
			return "", store.TicketError(`give the answer and its --source: what approved thing it follows from, or "you" for the reader's choice`)
		}
		q := *t.Question
		q.Answer, q.Source = arg, rep.Flag
		q.By = "planner"
		if strings.EqualFold(rep.Flag, "you") {
			q.By = "you"
		}
		t, err = s.board.Update(id, func(x *store.Ticket) error {
			x.Answered = append(x.Answered, q)
			x.Question, x.Stage, x.Ready = nil, "build", false
			return nil
		})
		if err != nil {
			return "", err
		}
		err = s.tell(t, roleBuilder, fmt.Sprintf("The planner answered your question (%s):\n\n%s\n\nThe plan is updated; `dv ticket show plan` has it. Carry on.", sourceLine(q), arg))
		return "Answer sent; the builder carries on.", err
	case "finding", "verdict", "recheck":
		return s.reviewReport(t, rep.Role, rep.Verb, rep.Args)
	}
	return "", store.TicketError(fmt.Sprintf("dv ticket has no %q", rep.Verb))
}

func sourceLine(q store.Question) string {
	if q.By == "you" {
		return "the reader decided"
	}
	return "from " + q.Source
}

// ready is a role saying its part is done.
func (s *Server) ready(t store.Ticket, role, summary string) (string, error) {
	wrong := store.TicketError(fmt.Sprintf("the %s has nothing to hand on while the ticket is in %s", role, t.Stage))
	switch {
	case role == rolePlanner && t.Stage == "plan" && t.PlanReview == nil && !t.NoPlanReview:
		return s.startPlanReview(t, summary)
	case role == rolePlanner && t.Stage == "plan" && t.PlanReview != nil && t.PlanReview.Phase == "review":
		return "", store.TicketError("the plan's reviewer hasn't finished; its findings will come to you")
	case role == rolePlanner && t.Stage == "plan" && t.PlanReview != nil && t.PlanReview.Phase == "judge":
		if missing := unanswered(t.PlanReview.Findings, func(f store.Finding) string { return f.Verdict }); missing != "" {
			return "", store.TicketError(missing + " has no verdict yet")
		}
		_, err := s.board.Update(t.ID, func(x *store.Ticket) error {
			x.PlanReview.Phase = "done"
			x.Ready, x.Status = true, summary
			return nil
		})
		return "The reader has the plan to approve, with the review and your verdicts on it.", err
	case role == rolePlanReviewer && t.Stage == "plan" && t.PlanReview != nil && t.PlanReview.Phase == "review":
		return s.planReviewReady(t)
	case role == roleScope && t.Stage == "scope", role == rolePlanner && t.Stage == "plan":
		_, err := s.board.Update(t.ID, func(x *store.Ticket) error {
			x.Ready, x.Status = true, summary
			return nil
		})
		return "The reader has it to approve.", err
	case role == roleBuilder && t.Stage == "build":
		t, err := s.board.Update(t.ID, func(x *store.Ticket) error {
			x.Stage, x.Status, x.Problem = "agent-review", summary, ""
			return nil
		})
		if err != nil {
			return "", err
		}
		if t, err = s.publish(t); err != nil {
			s.board.Update(t.ID, func(x *store.Ticket) error { x.Problem = err.Error(); return nil })
		}
		if _, err := s.startRound(t); err != nil {
			return "", err
		}
		return "A reviewer has it now. Its findings will come to you.", nil
	case t.Stage == "agent-review":
		return s.roundReady(t, role)
	}
	return "", wrong
}

// handleReviewPlan is the reader asking for the plan to be reviewed: one
// written before plan review was on, or revised enough to be read again.
func (s *Server) handleReviewPlan(w http.ResponseWriter, r *http.Request) {
	if !s.drivesBoard() {
		writeErr(w, http.StatusConflict, errNotDriver)
		return
	}
	s.boardMu.Lock()
	defer s.boardMu.Unlock()
	t, err := s.board.Get(r.PathValue("id"))
	plan, _ := s.board.Doc(t.ID, "plan")
	switch {
	case err != nil:
	case t.Stage != "plan" || strings.TrimSpace(plan) == "":
		err = store.TicketError("only a plan in Plan can be reviewed")
	case t.PlanReview != nil && t.PlanReview.Phase != "done":
		err = store.TicketError("the plan is being reviewed already")
	case t.Sessions[rolePlanner] == "":
		err = store.TicketError("the plan has no planner to answer its review")
	}
	if err == nil {
		t, err = s.board.Update(t.ID, func(x *store.Ticket) error {
			x.SetAsidePlanReview()
			x.NoPlanReview, x.Ready = false, false
			return nil
		})
	}
	if err == nil {
		_, err = s.startPlanReview(t, "")
	}
	if err != nil {
		writeErr(w, ticketStatus(err), err)
		return
	}
	t, _ = s.board.Get(t.ID)
	writeJSON(w, http.StatusOK, t)
}

// startPlanReview has another model read the plan cold before the reader
// does.
func (s *Server) startPlanReview(t store.Ticket, summary string) (string, error) {
	model, picked := t.Model[rolePlanReviewer]
	if !picked {
		model = s.otherModel(t.Model[rolePlanner])
	}
	t, err := s.board.Update(t.ID, func(x *store.Ticket) error {
		x.PlanReview = &store.Round{Model: model, Phase: "review", At: time.Now().UTC().Truncate(time.Second)}
		x.Status = "The plan is with a reviewer"
		if x.Model == nil {
			x.Model = map[string]string{}
		}
		x.Model[rolePlanReviewer] = model
		return nil
	})
	if err != nil {
		return "", err
	}
	ticket, _ := s.board.Doc(t.ID, "ticket")
	plan, _ := s.board.Doc(t.ID, "plan")
	var parent string
	if t.Parent != "" {
		parent, _ = s.board.Doc(t.Parent, "ticket")
	}
	if t, err = s.startRole(t, rolePlanReviewer, t.Worktree, planReviewPrompt(t, s.baseRef(false), parent, ticket, plan)); err != nil {
		return "", err
	}
	_, err = s.board.Update(t.ID, func(x *store.Ticket) error {
		x.PlanReview.Session = x.Sessions[rolePlanReviewer]
		return nil
	})
	return "Another model is reviewing the plan first. Its findings come to you to weigh; then the reader gets the plan.", err
}

var families = []string{"fable", "opus", "sonnet", "haiku"}

func familyOf(name string) string {
	for _, f := range families {
		if strings.Contains(name, f) {
			return f
		}
	}
	return name
}

// otherModel is the plan reviewer's model where none was picked: one of
// another family than the planner's, so the plan gets a second family's eyes -
// Fable for an Opus planner, Opus for a Fable one - as the model list names
// it. Before the list is known, the family's own name stands in.
func (s *Server) otherModel(planner string) string { return otherModelIn(s.modelsFor(""), planner) }

func otherModelIn(models []agent.Model, planner string) string {
	mine := familyOf(planner)
	for _, m := range models {
		if m.ID == planner {
			mine = familyOf(cmp.Or(m.Model, m.ID))
		}
	}
	want := "fable"
	if mine == "fable" {
		want = "opus"
	}
	for _, m := range models {
		if familyOf(cmp.Or(m.Model, m.ID)) == want {
			return m.ID
		}
	}
	return want
}

func (s *Server) planReviewReady(t store.Ticket) (string, error) {
	r := t.PlanReview
	if len(r.Findings) == 0 {
		_, err := s.board.Update(t.ID, func(x *store.Ticket) error {
			x.PlanReview.Phase = "done"
			x.Ready, x.Status = true, "The plan's review found nothing"
			return nil
		})
		return "Done; the reader has the plan.", err
	}
	t, err := s.board.Update(t.ID, func(x *store.Ticket) error {
		x.PlanReview.Phase = "judge"
		x.Status = "The planner is weighing the review"
		return nil
	})
	if err != nil {
		return "", err
	}
	return "Sent to the planner.", s.tell(t, rolePlanner, planJudgePrompt(*t.PlanReview))
}

func (s *Server) roundReady(t store.Ticket, role string) (string, error) {
	r := lastRound(t)
	if r == nil {
		return "", store.TicketError("no review round is running")
	}
	switch {
	case role == roleReviewer && r.Phase == "review" && len(r.Findings) == 0,
		role == roleReviewer && r.Phase == "recheck" && !slices.ContainsFunc(r.Findings, func(f store.Finding) bool { return f.Recheck == "fails" }):
		if missing := unanswered(r.Findings, func(f store.Finding) string { return f.Recheck }); r.Phase == "recheck" && missing != "" {
			return "", store.TicketError(missing + " has no recheck yet")
		}
		t, err := s.board.Update(t.ID, func(x *store.Ticket) error {
			x.Rounds[len(x.Rounds)-1].Phase = "done"
			x.Stage = "review"
			return nil
		})
		if err != nil {
			return "", err
		}
		if _, err := s.publish(t); err != nil {
			s.board.Update(t.ID, func(x *store.Ticket) error { x.Problem = err.Error(); return nil })
		}
		return "Review done; the reader has it.", nil
	case role == roleReviewer && (r.Phase == "review" || r.Phase == "recheck"):
		if missing := unanswered(r.Findings, func(f store.Finding) string { return f.Recheck }); r.Phase == "recheck" && missing != "" {
			return "", store.TicketError(missing + " has no recheck yet")
		}
		t, err := s.board.Update(t.ID, func(x *store.Ticket) error {
			round := &x.Rounds[len(x.Rounds)-1]
			for i, f := range round.Findings {
				if f.Recheck == "fails" {
					round.Findings[i].Verdict, round.Findings[i].Reason = "", ""
				}
			}
			round.Phase = "fix"
			return nil
		})
		if err != nil {
			return "", err
		}
		return "Sent to the builder.", s.tell(t, roleBuilder, fixPrompt(*lastRound(t)))
	case role == roleBuilder && r.Phase == "fix":
		if missing := unanswered(r.Findings, func(f store.Finding) string { return f.Verdict }); missing != "" {
			return "", store.TicketError(missing + " has no verdict yet")
		}
		t, err := s.board.Update(t.ID, func(x *store.Ticket) error {
			round := &x.Rounds[len(x.Rounds)-1]
			for i, f := range round.Findings {
				if f.Recheck == "fails" {
					round.Findings[i].Recheck = ""
				}
			}
			round.Phase = "recheck"
			return nil
		})
		if err != nil {
			return "", err
		}
		return "The reviewer is checking your answers.", s.tell(t, roleReviewer, recheckPrompt(*lastRound(t)))
	}
	return "", store.TicketError(fmt.Sprintf("the review round waits on the %s, not the %s", ownerOf(t), role))
}

// unanswered names the first finding field leaves empty, "" for none.
func unanswered(fs []store.Finding, field func(store.Finding) string) string {
	for _, f := range fs {
		if field(f) == "" {
			return f.ID
		}
	}
	return ""
}

// reviewReport takes a finding, a verdict on one or a recheck of one, for the
// review running: the plan's in Plan, a round of the code's in Agent review.
func (s *Server) reviewReport(t store.Ticket, role, verb string, args []string) (string, error) {
	plan := t.Stage == "plan" && t.PlanReview != nil
	r := lastRound(t)
	of := func(x *store.Ticket) *store.Round { return &x.Rounds[len(x.Rounds)-1] }
	want := map[string][2]string{"finding": {roleReviewer, "review"}, "verdict": {roleBuilder, "fix"}, "recheck": {roleReviewer, "recheck"}}[verb]
	words := map[string][]string{"verdict": {"fixed", "rejected"}, "recheck": {"holds", "fails"}}[verb]
	if plan {
		r = t.PlanReview
		of = func(x *store.Ticket) *store.Round { return x.PlanReview }
		want = map[string][2]string{"finding": {rolePlanReviewer, "review"}, "verdict": {rolePlanner, "judge"}}[verb]
		words = map[string][]string{"verdict": {"taken", "rejected"}}[verb]
	} else if t.Stage != "agent-review" || r == nil {
		return "", store.TicketError("no review is running")
	}
	if role != want[0] || r.Phase != want[1] {
		return "", store.TicketError(fmt.Sprintf("the review is at %s, which takes no %s from the %s", r.Phase, verb, role))
	}
	if verb == "finding" {
		text := strings.TrimSpace(strings.Join(args, " "))
		if text == "" {
			return "", store.TicketError("say what you found")
		}
		fid := fmt.Sprintf("F%d", len(r.Findings)+1)
		_, err := s.board.Update(t.ID, func(x *store.Ticket) error {
			round := of(x)
			round.Findings = append(round.Findings, store.Finding{ID: fid, Text: text})
			return nil
		})
		return "Recorded " + fid + ".", err
	}
	if len(args) < 2 {
		return "", store.TicketError(fmt.Sprintf("usage: dv ticket %s <finding> <word> \"reason\"", verb))
	}
	fid, word, why := strings.ToUpper(args[0]), args[1], strings.TrimSpace(strings.Join(args[2:], " "))
	if !slices.Contains(words, word) {
		return "", store.TicketError(fmt.Sprintf("a %s is %s", verb, strings.Join(words, " or ")))
	}
	if verb == "verdict" && word == "rejected" && why == "" {
		return "", store.TicketError("a rejection needs its reason")
	}
	_, err := s.board.Update(t.ID, func(x *store.Ticket) error {
		round := of(x)
		i := slices.IndexFunc(round.Findings, func(f store.Finding) bool { return f.ID == fid })
		if i < 0 {
			return store.TicketError("this round has no finding " + fid)
		}
		if verb == "verdict" {
			round.Findings[i].Verdict, round.Findings[i].Reason = word, why
		} else {
			round.Findings[i].Recheck, round.Findings[i].Note = word, why
		}
		return nil
	})
	return "Recorded.", err
}

// showTicket is what `dv ticket show` prints.
func (s *Server) showTicket(id string, args []string) (string, error) {
	t, err := s.board.Get(id)
	if err != nil {
		return "", err
	}
	which := strings.Join(args, " ")
	var b strings.Builder
	// A ticket started past a stage has the reader's request in place of what
	// that stage would have written.
	doc := func(name string) {
		text, _ := s.board.Doc(id, name)
		text = strings.TrimSpace(text)
		skipped := map[string]string{"ticket": "plan", "plan": "build"}[name]
		if text == "" && t.Request != "" && store.StageIndex(t.Started) >= store.StageIndex(skipped) {
			text = "The reader started this ticket past this stage, so their request stands in for it:\n\n" + t.Request
		}
		fmt.Fprintf(&b, "# The %s\n\n%s\n\n", name, cmp.Or(text, "(none written yet)"))
	}
	if which == "" {
		fmt.Fprintf(&b, "%s: %s\nStage: %s\n", t.ID, t.Title, t.Stage)
		if t.Branch != "" {
			fmt.Fprintf(&b, "Branch: %s, from %s, checked out in %s\n", t.Branch, s.baseRef(false), t.Worktree)
		}
		if t.Request != "" {
			fmt.Fprintf(&b, "\nThe reader's request:\n%s\n", t.Request)
		}
		if len(t.Images) > 0 {
			b.WriteString("\nImages the reader attached (open them with your Read tool):\n")
			for _, name := range t.Images {
				fmt.Fprintf(&b, "- %s\n", s.board.ImagePath(id, name))
			}
		}
		b.WriteString("\n")
	}
	if which == "" || which == "ticket" {
		doc("ticket")
	}
	if which == "" || which == "plan" {
		doc("plan")
	}
	if which == "" {
		if t.Question != nil {
			fmt.Fprintf(&b, "# Open question from the builder\n\n%s\n\n", t.Question.Text)
		}
		for _, q := range t.Answered {
			fmt.Fprintf(&b, "Answered during build (%s): %s\n  %s\n", sourceLine(q), q.Text, q.Answer)
		}
		if r := t.PlanReview; t.Stage == "plan" && r != nil && r.Phase != "done" && len(r.Findings) > 0 {
			b.WriteString("\n# The plan's review\n\n")
			for _, f := range r.Findings {
				fmt.Fprintf(&b, "%s: %s\n", f.ID, f.Text)
				if f.Verdict != "" {
					fmt.Fprintf(&b, "   %s: %s\n", f.Verdict, f.Reason)
				}
			}
		}
		if r := lastRound(t); t.Stage == "agent-review" && r != nil && r.Phase != "done" {
			fmt.Fprintf(&b, "\n# Review round %d (%s)\n\n", len(t.Rounds), r.Phase)
			for _, f := range r.Findings {
				fmt.Fprintf(&b, "%s: %s\n", f.ID, f.Text)
				if f.Verdict != "" {
					fmt.Fprintf(&b, "   %s: %s\n", f.Verdict, f.Reason)
				}
				if f.Recheck != "" {
					fmt.Fprintf(&b, "   recheck %s: %s\n", f.Recheck, f.Note)
				}
			}
		}
	}
	return strings.TrimSpace(b.String()) + "\n", nil
}

// ---- what runs on its own ----

// watchBoard starts sub-tickets whose turn has come, keeps pull requests' CI
// in view, and merges what the reader approved once CI passes.
func (s *Server) watchBoard(ctx context.Context) {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	var checked time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		s.startSubTickets()
		if time.Since(checked) > 30*time.Second {
			checked = time.Now()
			s.followPRs()
		}
	}
}

func (s *Server) startSubTickets() {
	s.boardMu.Lock()
	defer s.boardMu.Unlock()
	views := s.ticketViews()
	split := map[string]bool{}
	children := map[string][]ticketView{}
	for _, v := range views {
		split[v.ID] = v.Split
		if v.Parent != "" {
			children[v.Parent] = append(children[v.Parent], v)
		}
	}
	for _, v := range views {
		if v.Archived {
			continue
		}
		if v.Parent != "" && split[v.Parent] && v.Stage == "plan" && v.Sessions[rolePlanner] == "" && len(v.Blocked) == 0 && v.Problem == "" {
			s.enter(v.Ticket, "inbox", "")
		}
		if v.Split && v.Stage != "merged" && len(children[v.ID]) > 0 && !slices.ContainsFunc(children[v.ID], func(c ticketView) bool { return c.Stage != "merged" }) {
			if t, err := s.board.Update(v.ID, func(x *store.Ticket) error { x.Stage = "merged"; return nil }); err == nil {
				s.retire(t)
			}
		}
	}
}

func (s *Server) followPRs() {
	for _, t := range s.board.List() {
		if t.PR == 0 || t.Stage == "merged" || t.Archived {
			continue
		}
		pr, err := s.repo.ViewPR(t.PR)
		if err != nil {
			continue
		}
		s.boardMu.Lock()
		t, _ = s.board.Update(t.ID, func(x *store.Ticket) error {
			x.CI = pr.Checks
			if pr.State == "merged" {
				x.Stage, x.Merging = "merged", false
			}
			if x.Merging && pr.Checks == "failing" {
				x.Merging, x.Problem = false, "CI failed, so it wasn't merged"
			}
			return nil
		})
		if t.Merging && pr.Checks != "pending" && pr.Checks != "failing" {
			if err := s.repo.MergePR(t.PR); err != nil {
				s.board.Update(t.ID, func(x *store.Ticket) error { x.Merging, x.Problem = false, "could not merge: "+err.Error(); return nil })
			} else {
				t, _ = s.board.Update(t.ID, func(x *store.Ticket) error { x.Stage, x.Merging = "merged", false; return nil })
			}
		}
		if t.Stage == "merged" {
			s.retire(t)
		}
		s.boardMu.Unlock()
	}
}

// retire puts a merged ticket's sessions and worktree away.
func (s *Server) retire(t store.Ticket) {
	for _, id := range t.Sessions {
		s.agent.SetOpen(id, false)
	}
	if t.Worktree == "" {
		return
	}
	if err := s.repo.RemoveWorktree(t.Worktree, t.Branch); err != nil {
		s.board.Update(t.ID, func(x *store.Ticket) error { x.Problem = "could not remove its worktree: " + err.Error(); return nil })
		return
	}
	s.board.Update(t.ID, func(x *store.Ticket) error { x.Worktree = ""; return nil })
}
