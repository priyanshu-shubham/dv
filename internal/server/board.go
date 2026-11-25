package server

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"dv/internal/agent"
	"dv/internal/gitx"
	"dv/internal/notify"
	"dv/internal/store"
	"dv/internal/symindex"
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

var roleName = map[string]string{roleScope: "scoper", rolePlanner: "planner", rolePlanReviewer: "plan reviewer", roleBuilder: "builder", roleReviewer: "reviewer"}

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
	case "build":
		if reviewing(t) && lastRound(t).Phase != "fix" {
			return roleReviewer
		}
		return roleBuilder
	case "review", "merged":
		return roleBuilder
	}
	return ""
}

// reviewing is the built work under agent review: Build, with a round of it
// under way.
func reviewing(t store.Ticket) bool {
	r := lastRound(t)
	return t.Stage == "build" && r != nil && r.Phase != "done"
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
	// Picks is the model and effort each role starts on next.
	Picks map[string]rolePickView `json:"picks"`
	// AskingSession is the agent waiting on the reader, of whichever stage,
	// and AskingStage the stage that shows it.
	AskingSession string `json:"askingSession,omitempty"`
	AskingStage   string `json:"askingStage,omitempty"`
}

// roleStage is the stage whose view shows the role's agent.
func roleStage(t store.Ticket, role string) string {
	switch role {
	case roleScope:
		return "scope"
	case rolePlanner, rolePlanReviewer:
		return "plan"
	case roleBuilder:
		if t.Stage == "review" || t.Stage == "merged" {
			return t.Stage
		}
	}
	return "build"
}

type rolePickView struct {
	Model  string `json:"model"`
	Effort string `json:"effort"`
}

// waitLoop is which of after already waits, directly or through others, for
// id, "" for none: id waiting for it would leave neither able to go first.
func (s *Server) waitLoop(id string, after []string) string {
	waits := map[string][]string{}
	for _, t := range s.board.List() {
		waits[t.ID] = t.After
	}
	for _, a := range after {
		if a == id {
			continue // refused on its own terms when saved
		}
		seen := map[string]bool{}
		for todo := []string{a}; len(todo) > 0; {
			x := todo[len(todo)-1]
			todo = todo[:len(todo)-1]
			if x == id {
				return a
			}
			if !seen[x] {
				seen[x] = true
				todo = append(todo, waits[x]...)
			}
		}
	}
	return ""
}

// waitsOn is what t waits for that isn't merged: the tickets it is after,
// and while it is in the Inbox, the one it follows up, whose changes its
// worktree, cut from trunk, would lack. An archived one's aren't coming.
func waitsOn(t store.Ticket, byID map[string]*store.Ticket) []string {
	var out []string
	for _, a := range t.After {
		if p := byID[a]; p == nil || p.Stage != "merged" {
			out = append(out, a)
		}
	}
	if p := byID[t.From]; t.Stage == "inbox" && p != nil && p.Stage != "merged" && !p.Archived && !slices.Contains(out, p.ID) {
		out = append(out, p.ID)
	}
	return out
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
	byID := map[string]*store.Ticket{}
	for i, t := range tickets {
		byID[t.ID] = &tickets[i]
	}
	models, last := s.modelsFor(""), s.boardPicks()
	agents := s.boardSessions()
	out := make([]ticketView, len(tickets))
	for i, t := range tickets {
		v := ticketView{Ticket: t, Owner: ownerOf(t), Picks: map[string]rolePickView{}}
		for role := range roleTitle {
			m, e := pickFor(t, role, models, last)
			v.Picks[role] = rolePickView{m, e}
		}
		v.Working = busy[t.Sessions[v.Owner]]
		for sid, b := range agents {
			if b.Ticket == t.ID && waiting[sid] {
				v.Asking, v.AskingSession, v.AskingStage = true, sid, roleStage(t, b.Role)
			}
		}
		// A part's question with its parent's scoper is the part waiting on the
		// reader, too, while that scoper asks them.
		if p := byID[t.Parent]; t.ScopeQuestion != nil && t.Sessions[roleScope] == "" && p != nil && waiting[p.Sessions[roleScope]] {
			v.Asking, v.AskingSession, v.AskingStage = true, p.Sessions[roleScope], t.Stage
		}
		v.Blocked = waitsOn(t, byID)
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
		"templates": ReviewTemplates, "base": s.repo.DefaultBranch(), "remote": s.repo.Origin() != nil,
		"picks": s.newTicketPicks(),
	})
}

// newTicketPicks are what each role of a ticket not yet made would start on.
func (s *Server) newTicketPicks() map[string]rolePickView {
	models, last := s.modelsFor(""), s.boardPicks()
	out := map[string]rolePickView{}
	for role := range roleTitle {
		m, e := pickFor(store.Ticket{}, role, models, last)
		out[role] = rolePickView{m, e}
	}
	return out
}

// handleSuggestion is the reader filing a follow-up an agent suggested, to
// the Inbox, or dismissing it: { file }.
func (s *Server) handleSuggestion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		File bool `json:"file"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.boardMu.Lock()
	defer s.boardMu.Unlock()
	var taken store.Suggestion
	t, err := s.board.Update(r.PathValue("id"), func(x *store.Ticket) error {
		i := slices.IndexFunc(x.Suggestions, func(sg store.Suggestion) bool { return sg.ID == r.PathValue("sid") })
		if i < 0 {
			return store.TicketError("that suggestion is gone")
		}
		taken = x.Suggestions[i]
		x.Suggestions = slices.Delete(x.Suggestions, i, i+1)
		return nil
	})
	if err == nil && req.File {
		_, err = s.board.Add(store.Ticket{Title: taken.Title, Request: taken.Request, Labels: t.Labels, From: t.ID})
	}
	if err != nil {
		writeErr(w, ticketStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// handleEditSuggestion is the reader rewording a suggested follow-up before
// deciding on it: { title, request }.
func (s *Server) handleEditSuggestion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title   string `json:"title"`
		Request string `json:"request"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		writeErr(w, http.StatusBadRequest, store.TicketError("a follow-up needs a title"))
		return
	}
	s.boardMu.Lock()
	defer s.boardMu.Unlock()
	t, err := s.board.Update(r.PathValue("id"), func(x *store.Ticket) error {
		i := slices.IndexFunc(x.Suggestions, func(sg store.Suggestion) bool { return sg.ID == r.PathValue("sid") })
		if i < 0 {
			return store.TicketError("that suggestion is gone")
		}
		x.Suggestions[i].Title, x.Suggestions[i].Request = strings.TrimSpace(req.Title), strings.TrimSpace(req.Request)
		return nil
	})
	if err != nil {
		writeErr(w, ticketStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// handleNothing is the reader deciding on an agent's finding that there is
// nothing to do: { close } closes the ticket as merged with no change, else
// the agent is told note and carries on.
func (s *Server) handleNothing(w http.ResponseWriter, r *http.Request) {
	if !s.drivesBoard() {
		writeErr(w, http.StatusConflict, errNotDriver)
		return
	}
	var req struct {
		Close bool   `json:"close"`
		Note  string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.boardMu.Lock()
	defer s.boardMu.Unlock()
	t, err := s.board.Get(r.PathValue("id"))
	if err == nil && (t.Nothing == nil || t.Stage == "merged") {
		err = store.TicketError("no agent has said there's nothing to do")
	}
	if err == nil && req.Close {
		t, err = s.closeUnchanged(t)
	} else if err == nil {
		by := t.Nothing.By
		if t, err = s.board.Update(t.ID, func(x *store.Ticket) error { x.Nothing = nil; return nil }); err == nil {
			msg := "The reader doesn't agree there is nothing to do."
			if note := strings.TrimSpace(req.Note); note != "" {
				msg += " They say:\n" + note
			}
			err = s.tell(t, by, msg+"\n\nCarry on with the ticket as your stage asks.")
		}
	}
	if err != nil {
		writeErr(w, ticketStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// closeUnchanged takes a ticket needing no change to Merged, which releases
// what waits for it, and clears away its agents and worktree.
func (s *Server) closeUnchanged(t store.Ticket) (store.Ticket, error) {
	t, err := s.board.Update(t.ID, func(x *store.Ticket) error {
		x.Stage, x.Ready, x.Merging, x.Status, x.Problem = "merged", false, false, "", ""
		return nil
	})
	if err == nil {
		s.retire(t)
	}
	return t, err
}

// handlePublish is the reader opening the ticket's pull request: the branch
// pushed, and the builder's description put up for anyone to see.
func (s *Server) handlePublish(w http.ResponseWriter, r *http.Request) {
	if !s.drivesBoard() {
		writeErr(w, http.StatusConflict, errNotDriver)
		return
	}
	s.boardMu.Lock()
	defer s.boardMu.Unlock()
	t, err := s.board.Get(r.PathValue("id"))
	if err == nil && (t.Worktree == "" || s.repo.Origin() == nil) {
		err = store.TicketError("there is nothing to publish, or nowhere to publish it")
	}
	if err == nil {
		t, err = s.publish(t)
	}
	if err != nil {
		s.board.Update(t.ID, func(x *store.Ticket) error { x.Problem = err.Error(); return nil })
		writeErr(w, ticketStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, t)
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
	// A sub-ticket's background is its parent's ticket.
	var parent map[string]string
	if p, err := s.board.Get(view.Parent); err == nil {
		text, _ := s.board.Doc(p.ID, "ticket")
		parent = map[string]string{"id": p.ID, "title": p.Title, "ticket": cmp.Or(text, p.Request)}
	}
	pr, _ := s.board.Doc(id, "pr")
	guide, _ := s.board.Doc(id, "guide")
	// What the branch changes, as a diff range from where it left the trunk.
	// Its commits too, newest first, to read the changes from one of them on.
	var changes string
	var commits []gitx.Commit
	if view.Branch != "" && s.repo.RefExists("refs/heads/"+view.Branch) {
		changes = s.baseRef(false) + "..." + view.Branch
		commits = s.repo.Log(s.baseRef(false)+".."+view.Branch, 200)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ticket": view, "docs": map[string]string{"ticket": ticket, "plan": plan, "pr": pr, "guide": guide}, "parent": parent, "changes": changes, "remote": s.repo.Origin() != nil,
		"commits":  commits,
		"versions": map[string][]store.DocVersion{"ticket": s.board.DocVersions(id, "ticket"), "plan": s.board.DocVersions(id, "plan"), "pr": s.board.DocVersions(id, "pr")},
		"mockups":  s.board.Mockups(id),
	})
}

func (s *Server) handleAddTicket(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title    string   `json:"title"`
		Request  string   `json:"request"`
		Labels   []string `json:"labels"`
		Priority string   `json:"priority"`
		Stage    string   `json:"stage"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	t, err := s.board.Add(store.Ticket{Title: req.Title, Request: req.Request, Labels: req.Labels, Priority: req.Priority})
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
		Title    *string   `json:"title"`
		Request  *string   `json:"request"`
		Labels   *[]string `json:"labels"`
		Priority *string   `json:"priority"`
		// After is the tickets it waits for, whole.
		After        *[]string `json:"after"`
		Archived     *bool     `json:"archived"`
		NoPlanReview *bool     `json:"noPlanReview"`
		Templates    *[]string `json:"templates"`
		// Pick is a role's model or effort for when it next starts, remembered
		// for the role on the tickets to come.
		Pick *struct {
			Role   string  `json:"role"`
			Model  *string `json:"model"`
			Effort *string `json:"effort"`
		} `json:"pick"`
	}
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if p.After != nil {
		if loop := s.waitLoop(r.PathValue("id"), *p.After); loop != "" {
			writeErr(w, http.StatusBadRequest, store.TicketError(fmt.Sprintf("%[1]s can't wait for %[2]s: %[2]s already waits for %[1]s, so neither could go first", r.PathValue("id"), loop)))
			return
		}
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
		if p.Priority != nil {
			t.Priority = *p.Priority
		}
		if p.After != nil {
			t.After = slices.Compact(slices.Sorted(slices.Values(*p.After)))
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
		if p.Pick != nil {
			if roleTitle[p.Pick.Role] == "" {
				return store.TicketError(fmt.Sprintf("%q is not a role", p.Pick.Role))
			}
			setPick(t, p.Pick.Role, p.Pick.Model, p.Pick.Effort)
		}
		return nil
	})
	if err != nil {
		writeErr(w, ticketStatus(err), err)
		return
	}
	if p.Pick != nil {
		s.rememberPick(p.Pick.Role, p.Pick.Model, p.Pick.Effort)
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
		// Anyway starts a ticket out of the Inbox though what it waits for isn't merged.
		Anyway bool `json:"anyway"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.checkWaits(r.PathValue("id"), req.Stage, req.Anyway); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	t, err := s.moveTicket(r.PathValue("id"), req.Stage, strings.TrimSpace(req.Note))
	if err != nil {
		writeErr(w, ticketStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// checkWaits holds a ticket in the Inbox while what it waits for isn't
// merged, unless the reader starts it anyway.
func (s *Server) checkWaits(id, stage string, anyway bool) error {
	t, err := s.board.Get(id)
	if err != nil || anyway || t.Stage != "inbox" || stage == "inbox" {
		return nil
	}
	tickets := s.board.List()
	byID := map[string]*store.Ticket{}
	for i := range tickets {
		byID[tickets[i].ID] = &tickets[i]
	}
	if w := waitsOn(t, byID); len(w) > 0 {
		return store.TicketError(fmt.Sprintf("%s waits for %s, which isn't merged; started now, its worktree is cut from trunk without those changes", id, strings.Join(w, ", ")))
	}
	return nil
}

// docAuthor is the role that writes a ticket's document, and the stage its
// writing is done in.
var docAuthor = map[string][2]string{"ticket": {roleScope, "scope"}, "plan": {rolePlanner, "plan"}, "pr": {roleBuilder, "review"}}

var docName = map[string]string{"ticket": "ticket", "plan": "plan", "pr": "pull request's description"}

// replyToComment takes an author's answer to one of the reader's comments: on
// the ticket's record for one on a document, in the review thread itself for
// one on the code.
func (s *Server) replyToComment(t store.Ticket, role string, args []string) (string, error) {
	if len(args) != 3 || (args[1] != "changed" && args[1] != "answered") || strings.TrimSpace(args[2]) == "" {
		return "", store.TicketError(`answer with: dv ticket reply <ID> changed|answered "..."`)
	}
	id, kind, text := args[0], args[1], strings.TrimSpace(args[2])
	if i := slices.IndexFunc(t.Comments, func(c store.DocComment) bool { return c.ID == id }); i >= 0 {
		if doc := t.Comments[i].Doc; docAuthor[doc][0] != role {
			return "", store.TicketError(fmt.Sprintf("%s is on the %s, which its %s answers", id, docName[doc], roleName[docAuthor[doc][0]]))
		}
		_, err := s.board.Update(t.ID, func(x *store.Ticket) error {
			for i := range x.Comments {
				if x.Comments[i].ID == id {
					x.Comments[i].Thread = append(x.Comments[i].Thread, store.CommentPost{By: role, Kind: kind, Text: text, At: time.Now().UTC().Truncate(time.Second)})
				}
			}
			return nil
		})
		return "Answered " + id + "; the reader sees it with their comment.", err
	}
	if slices.ContainsFunc(t.CodeThreads, func(th store.SentThread) bool { return th.ID == id }) {
		if role != roleBuilder {
			return "", store.TicketError(id + " is on the code, which the builder answers")
		}
		if _, err := s.store.AddReply(id, kind+": "+text, roleBuilder); err != nil {
			return "", err
		}
		return "Answered in the reader's thread on those lines.", nil
	}
	return "", store.TicketError("this ticket has no comment " + id)
}

// handleCommentReply is the reader's turn on one of their comments: a reply,
// which goes to the author while the ticket is still in its stage, or
// resolving it.
func (s *Server) handleCommentReply(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text     string `json:"text"`
		Resolved *bool  `json:"resolved"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.boardMu.Lock()
	defer s.boardMu.Unlock()
	cid, text := r.PathValue("cid"), strings.TrimSpace(req.Text)
	t, err := s.board.Get(r.PathValue("id"))
	i := slices.IndexFunc(t.Comments, func(c store.DocComment) bool { return c.ID == cid })
	if err == nil && i < 0 {
		err = store.TicketError("this ticket has no comment " + cid)
	}
	var author [2]string
	if err == nil && text != "" {
		author = docAuthor[t.Comments[i].Doc]
		switch {
		case !s.drivesBoard():
			err = errNotDriver
		case t.Stage != author[1] || t.Sessions[author[0]] == "":
			err = store.TicketError(fmt.Sprintf("the %s is answered in %s; send the ticket back there to take it up again", docName[t.Comments[i].Doc], author[1]))
		}
	}
	if err == nil {
		t, err = s.board.Update(t.ID, func(x *store.Ticket) error {
			c := &x.Comments[i]
			if text != "" {
				c.Thread = append(c.Thread, store.CommentPost{By: "you", Text: text, At: time.Now().UTC().Truncate(time.Second)})
				c.Resolved = false
				x.Ready, x.Status = false, "Taking up your reply"
			}
			if req.Resolved != nil {
				c.Resolved = *req.Resolved
			}
			return nil
		})
	}
	if err == nil && text != "" {
		err = s.tell(t, author[0], fmt.Sprintf("The reader replied on %s, their comment on the %s:\n\n%s\n\n%s", cid, docName[t.Comments[i].Doc], text, replyRule(t.Comments[i].Doc)))
	}
	if err != nil {
		writeErr(w, ticketStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// unanswered is the first of the reader's comments role has yet to answer:
// on the documents it writes, and for the builder, on the code.
func (s *Server) unanswered(t store.Ticket, role string) string {
	for _, c := range t.Comments {
		if c.Waiting() && docAuthor[c.Doc][0] == role {
			return c.ID
		}
	}
	if role != roleBuilder || len(t.CodeThreads) == 0 {
		return ""
	}
	byID := map[string]*store.Thread{}
	for _, th := range s.store.Threads() {
		byID[th.ID] = th
	}
	for _, sent := range t.CodeThreads {
		if th := byID[sent.ID]; th != nil && !th.Resolved && len(th.Comments) > 0 && th.Comments[len(th.Comments)-1].Author != roleBuilder {
			return sent.ID
		}
	}
	return ""
}

// replyRule is how a document's author answers the reader's comments on it.
// An answer that settles something goes into the document as well, since
// the stages after it work from the document and never see the comments.
func replyRule(doc string) string {
	answer := "Answer each comment with `dv ticket reply <ID> changed \"what you changed\"` or `dv ticket reply <ID> answered \"your answer\"`, a sentence or two the reader takes in at a glance. "
	if doc == "pr" {
		return answer + "Revise the title and description where a comment calls for it, and save them with `dv ticket pr`."
	}
	return answer + fmt.Sprintf("Revise the %[1]s where a comment calls for it. If an answer decides anything - a choice, a limit, how something behaves - write that into the %[1]s as well: the stages after you work from the %[1]s and never see these comments. Save it with `dv ticket write %[1]s`, then run `dv ticket ready`, which dv refuses while a comment has no answer.", doc)
}

// handleCommentTicket sends the reader's comments on the ticket or the plan to
// the agent writing it, to revise it, answer each, and hand it back.
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
		writeErr(w, http.StatusBadRequest, store.TicketError("comment on the ticket, the plan or the pull request's description"))
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
	// Kept on the ticket, each answered on its own, as findings are.
	now := time.Now().UTC().Truncate(time.Second)
	var made []store.DocComment
	for i, c := range req.Comments {
		made = append(made, store.DocComment{
			ID: fmt.Sprintf("C%d", len(t.Comments)+i+1), Doc: req.Doc, Quote: strings.TrimSpace(c.Quote),
			Thread: []store.CommentPost{{By: "you", Text: strings.TrimSpace(c.Text), At: now}},
		})
	}
	var b strings.Builder
	fmt.Fprintf(&b, "The reader commented on the %s:\n\n", docName[req.Doc])
	for _, c := range made {
		b.WriteString(c.ID + ":\n")
		if c.Quote != "" {
			b.WriteString("> " + strings.ReplaceAll(c.Quote, "\n", "\n> ") + "\n")
		}
		b.WriteString(c.Thread[0].Text + "\n\n")
	}
	b.WriteString(replyRule(req.Doc))
	if err := s.tell(t, author[0], b.String()); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	t, err = s.board.Update(t.ID, func(x *store.Ticket) error {
		x.Comments = append(x.Comments, made...)
		x.Ready, x.Status = false, "Revising after your comments"
		return nil
	})
	if err != nil {
		writeErr(w, ticketStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// handleCodeComments sends the reader's comments on the branch's code to the
// builder: at once while it builds, and from a review by sending the ticket
// back to Build with them.
func (s *Server) handleCodeComments(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Comments []struct {
			// Thread is the review thread it was written in, which the
			// builder's answer goes into.
			Thread string `json:"thread"`
			File   string `json:"file"`
			Start  int    `json:"start"`
			End    int    `json:"end"`
			Side   string `json:"side"`
			Quote  string `json:"quote"`
			Text   string `json:"text"`
		} `json:"comments"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if len(req.Comments) == 0 {
		writeErr(w, http.StatusBadRequest, store.TicketError("no comments to send"))
		return
	}
	var threads []string
	var b strings.Builder
	b.WriteString("The reader reviewed the code on the branch and commented:\n\n")
	for _, c := range req.Comments {
		if c.Thread != "" {
			threads = append(threads, c.Thread)
			b.WriteString(c.Thread + ": ")
		}
		at := c.File
		if c.Start > 0 {
			at += fmt.Sprintf(":%d", c.Start)
			if c.End > c.Start {
				at += fmt.Sprintf("-%d", c.End)
			}
			if c.Side == "old" {
				at += " (as it was before the branch)"
			}
		}
		fmt.Fprintf(&b, "- `%s`\n", at)
		if q := strings.TrimSpace(c.Quote); q != "" {
			b.WriteString("  > " + strings.ReplaceAll(q, "\n", "\n  > ") + "\n")
		}
		b.WriteString("  " + strings.ReplaceAll(strings.TrimSpace(c.Text), "\n", "\n  ") + "\n\n")
	}
	b.WriteString("Fix each, or answer it where it is a question or you disagree, and commit. Answer each with `dv ticket reply <ID> changed \"what you did\"` or `dv ticket reply <ID> answered \"your answer\"`, a sentence or two: it goes into the reader's thread on those lines. Run `dv ticket ready` once the work is complete again; dv refuses it while a comment has no answer. A reviewer checks any new commits first; with none, the ticket goes straight back to the reader with your answers.")

	id := r.PathValue("id")
	t, err := s.board.Get(id)
	if err == nil && t.Sessions[roleBuilder] == "" {
		err = store.TicketError("the ticket has no builder yet")
	}
	if err == nil && len(threads) > 0 {
		held := map[string]int{}
		for _, th := range s.store.Threads() {
			held[th.ID] = len(th.Comments)
		}
		t, err = s.board.Update(id, func(x *store.Ticket) error {
			for _, th := range threads {
				x.CodeThreads = slices.DeleteFunc(x.CodeThreads, func(s store.SentThread) bool { return s.ID == th })
				x.CodeThreads = append(x.CodeThreads, store.SentThread{ID: th, Sent: held[th]})
			}
			return nil
		})
	}
	if err == nil {
		switch t.Stage {
		case "build":
			if !s.drivesBoard() {
				err = errNotDriver
			} else {
				err = s.tell(t, roleBuilder, b.String())
			}
		case "review":
			t, err = s.moveTicket(id, "build", b.String())
		default:
			err = store.TicketError("code is commented on from Build to your review")
		}
	}
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
		if reviewing(*t) {
			return store.TicketError("a round of review is already under way")
		}
		t.Stage, t.Ready, t.Problem, t.Merging = "build", false, "", false
		return nil
	})
	if err == nil {
		var conflicts bool
		if t, conflicts, err = s.beginRound(t); conflicts && err == nil {
			err = s.tell(t, roleBuilder, roundConflictPrompt(s.baseRef(false)))
		}
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
	if stage == "merged" && t.Nothing != nil {
		return s.closeUnchanged(t)
	}
	if stage == "merged" && s.repo.Origin() == nil {
		return s.mergeHere(t)
	}
	if stage == "merged" {
		// A trunk that moved on is caught up with first; the approval waits on it.
		if t, behind, err := s.catchUp(t, true); behind || err != nil {
			return t, err
		}
		// Approving one not yet published publishes it.
		if t.PR == 0 {
			if t, err = s.publish(t); err != nil {
				return t, err
			}
		}
		return s.board.Update(id, func(t *store.Ticket) error {
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
	// Sent back from their review, it was reviewed as the branch stands: what
	// the builder does next is what the reader looks at when it returns.
	// Skipped to the reader's review, a round under way is ended, its reviewer
	// stopped: another is the reader's to ask for.
	skipped := from == "build" && stage == "review" && reviewing(t)
	if skipped {
		s.agent.Interrupt(t.Sessions[roleReviewer])
	}
	var reviewed, handed string
	if from == "review" && stage == "build" {
		reviewed = s.headOf(t)
	}
	if stage == "review" {
		handed = s.headOf(t)
	}
	t, err = s.board.Update(id, func(t *store.Ticket) error {
		if handed != "" {
			t.HandedAt = handed
		}
		if from == "inbox" {
			t.Started = stage
		}
		if store.StageIndex(stage) < store.StageIndex(from) && store.StageIndex(from) > store.StageIndex(t.Reached) {
			t.Reached = from
		}
		t.Stage, t.Ready, t.Problem, t.Merging, t.Status = stage, false, "", false, ""
		t.Nothing = nil
		if skipped {
			t.Rounds[len(t.Rounds)-1].Phase = "done"
		}
		if reviewed != "" {
			t.ReviewedAt = reviewed
		}
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
	// The trunk may have moved on since the worktree was cut, or since the
	// last stage read it; a builder already at work keeps its floor.
	var news string
	if t.Stage == "scope" || t.Stage == "plan" || t.Stage == "build" && t.Sessions[roleBuilder] == "" {
		t, news = s.startNews(t, map[string]string{"scope": roleScope, "plan": rolePlanner, "build": roleBuilder}[t.Stage])
	}
	switch t.Stage {
	case "scope":
		if back && t.Sessions[roleScope] != "" {
			err = s.tell(t, roleScope, told("The reader sent the ticket back to you. Change it as they ask, save it, and run `dv ticket ready` again."+news))
		} else {
			doc, _ := s.board.Doc(t.ID, "ticket")
			t, err = s.startRole(t, roleScope, t.Worktree, told(scopePrompt(t, s.baseRef(false), doc)+s.originNote(t)+news))
		}
	case "plan":
		if back && t.Sessions[rolePlanner] != "" {
			err = s.tell(t, rolePlanner, told("The reader sent the ticket back to planning. Change the plan as they ask, save it, and run `dv ticket ready` again."+news))
		} else {
			// A fresh planner's plan gets a review of its own.
			if t, err = s.board.Update(t.ID, func(x *store.Ticket) error { x.SetAsidePlanReview(); return nil }); err == nil {
				t, err = s.startRole(t, rolePlanner, t.Worktree, told(s.planPromptFor(t)+s.originNote(t)+news))
			}
		}
	case "build":
		switch {
		case t.Sessions[roleBuilder] == "":
			t, err = s.startRole(t, roleBuilder, t.Worktree, told(buildPrompt(t, s.baseRef(false))+s.originNote(t)+news))
		case back:
			err = s.tell(t, roleBuilder, told("The reader sent the work back to you. Make the changes they ask for, commit, answer what they asked, and run `dv ticket ready` again. A reviewer checks any new commits first; with none, the ticket goes straight back to the reader."))
		default:
			err = s.tell(t, roleBuilder, told("The plan was changed and approved again. Read it with `dv ticket show plan`, carry on from where you are, and run `dv ticket ready` when done."))
		}
	case "review":
		// Publishing is the reader's; an open pull request gets the fixes.
		if t.PR != 0 {
			t, err = s.publish(t)
		}
	}
	if err != nil {
		t, _ = s.board.Update(t.ID, func(x *store.Ticket) error { x.Problem = err.Error(); return nil })
	}
	return t, err
}

// originNote tells a follow-up started before the ticket it follows is merged
// that its worktree, cut from trunk, lacks that ticket's changes.
func (s *Server) originNote(t store.Ticket) string {
	o, err := s.board.Get(t.From)
	if t.From == "" || err != nil || o.Stage == "merged" || o.Branch == "" {
		return ""
	}
	return fmt.Sprintf("\n\nThis follows up %s, %q, which isn't merged: the reader started it anyway, so your worktree, cut from trunk, doesn't have its changes. See them with `git diff %s...%s`, and don't take today's code as how things will be once it lands.",
		o.ID, o.Title, s.baseRef(false), o.Branch)
}

func (s *Server) planPromptFor(t store.Ticket) string {
	ticket, _ := s.board.Doc(t.ID, "ticket")
	plan, _ := s.board.Doc(t.ID, "plan")
	var parent string
	if t.Parent != "" {
		parent, _ = s.board.Doc(t.Parent, "ticket")
	}
	_, scoped := s.scoperOf(t)
	return planPrompt(t, s.baseRef(false), parent, ticket, plan, scoped)
}

// startRole starts a fresh session for the ticket's role, working in cwd, and
// sends it prompt.
func (s *Server) startRole(t store.Ticket, role, cwd, prompt string) (store.Ticket, error) {
	id, err := s.agent.CreateIn(cwd)
	if err != nil {
		return t, err
	}
	m, e := s.rolePick(t, role)
	model, effort := s.picks(id, m, e)
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
		// What it runs on, which a reviewer's family is kept apart from.
		setPick(x, role, cmp.Or(model, new(string)), cmp.Or(effort, new(string)))
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

// ticketTree is a board ticket's worktree, as Go to file, Search and its
// uncommitted changes read it.
type ticketTree struct {
	repo *gitx.Repo
	ix   *symindex.Index
}

// treeIn is the checkout's, or with in=<ticket> that ticket's worktree's,
// whose branch has files and work the checkout doesn't. Paths in it are the
// worktree's own.
func (s *Server) treeIn(r *http.Request) (ticketTree, error) {
	id := r.URL.Query().Get("in")
	if id == "" {
		return ticketTree{s.repo, s.index}, nil
	}
	if s.board == nil {
		return ticketTree{}, fmt.Errorf("no board here")
	}
	t, err := s.board.Get(id)
	if err != nil {
		return ticketTree{}, err
	}
	wt := filepath.Join(s.repo.Root, ".dv", "worktrees", t.ID)
	if fi, err := os.Stat(wt); err != nil || !fi.IsDir() {
		return ticketTree{}, fmt.Errorf("%s has no worktree", t.ID)
	}
	s.wtMu.Lock()
	defer s.wtMu.Unlock()
	if tree, ok := s.wtTrees[wt]; ok {
		return tree, nil
	}
	repo, err := gitx.Open(wt)
	if err != nil {
		return ticketTree{}, err
	}
	if s.wtTrees == nil {
		s.wtTrees = map[string]ticketTree{}
	}
	tree := ticketTree{repo, symindex.New(wt, repo)}
	s.wtTrees[wt] = tree
	return tree, nil
}

func (s *Server) lookIn(r *http.Request) (*symindex.Index, error) {
	tree, err := s.treeIn(r)
	return tree.ix, err
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
	base := s.baseRef(true)
	if err := s.repo.AddWorktree(path, branch, base); err != nil {
		return t, fmt.Errorf("could not make its worktree: %w", err)
	}
	var tip string
	if c := s.repo.Log(base, 1); len(c) > 0 {
		tip = c[0].SHA
	}
	return s.board.Update(t.ID, func(x *store.Ticket) error {
		x.Worktree, x.Branch = path, branch
		// A part keeps its parent's, which its ticket was written against.
		x.Base = cmp.Or(x.Base, tip)
		return nil
	})
}

// catchUp brings a branch about to merge up with a trunk that moved on since
// it was cut, so it doesn't merge untried against what landed meanwhile:
// passing its own tests is no proof against a migration number taken twice or
// a function renamed under it. A clean merge dv makes and pushes, conflicts go
// to the builder; either way a reviewer then checks what the trunk brought in
// against the branch. It reports whether the branch had fallen behind.
func (s *Server) catchUp(t store.Ticket, approved bool) (store.Ticket, bool, error) {
	if t.Worktree == "" || s.repo.Origin() == nil {
		return t, false, nil
	}
	trunk := s.baseRef(true)
	behind, since, err := gitx.Behind(t.Worktree, trunk)
	if err != nil || !behind {
		return t, false, err
	}
	clean, err := gitx.MergeIn(t.Worktree, trunk)
	if err != nil {
		return t, true, fmt.Errorf("could not bring in %s: %w", trunk, err)
	}
	var tip string
	if c := s.repo.Log(trunk, 1); len(c) > 0 {
		tip = c[0].SHA
	}
	t, err = s.board.Update(t.ID, func(x *store.Ticket) error {
		x.Base = cmp.Or(tip, x.Base)
		x.Stage, x.Ready, x.Merging, x.Problem = "build", false, false, ""
		x.CatchUp = &store.CatchUp{Since: since, Conflicts: !clean, Approved: approved}
		x.Status = "Catching up with " + trunk
		return nil
	})
	if err != nil {
		return t, true, err
	}
	if !clean {
		return t, true, s.tell(t, roleBuilder, conflictPrompt(trunk))
	}
	if t.PR != 0 {
		if t, err = s.publish(t); err != nil {
			return t, true, err
		}
	}
	t, err = s.startRound(t, "")
	return t, true, err
}

// refresh brings the ticket's worktree up to the trunk as an agent starts on
// it, and lists what landed since its agents last read the code; "" when
// nothing did. conflicts is a merge undone, someone's to resolve.
func (s *Server) refresh(t store.Ticket) (_ store.Ticket, news string, conflicts bool, err error) {
	if t.Worktree == "" {
		return t, "", false, nil
	}
	trunk := s.baseRef(true)
	from, to, clean, err := gitx.Refresh(t.Worktree, trunk)
	if err != nil {
		err = fmt.Errorf("could not bring its worktree up to %s: %w", trunk, err)
		t, _ = s.board.Update(t.ID, func(x *store.Ticket) error { x.Problem = err.Error(); return nil })
		return t, "", false, err
	}
	if !clean {
		return t, "", true, nil
	}
	since := cmp.Or(t.Base, from)
	if t.Base != to {
		if t, err = s.board.Update(t.ID, func(x *store.Ticket) error { x.Base = to; return nil }); err != nil {
			return t, "", false, err
		}
	}
	if since == to {
		return t, "", false, nil
	}
	landed := s.repo.Log(since+".."+to, 200)
	if len(landed) == 0 {
		return t, "", false, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n\n%s moved on since this ticket's code was last read: %d commit(s) landed, now in your worktree:\n", trunk, len(landed))
	for i, c := range landed {
		if i == 15 {
			fmt.Fprintf(&b, "- and %d more (`git log --oneline %s..%s`)\n", len(landed)-i, since[:12], to[:12])
			break
		}
		fmt.Fprintf(&b, "- %s %s\n", c.Short, c.Subject)
	}
	fmt.Fprintf(&b, "See what changed with `git diff %s %s`.", since[:12], to[:12])
	return t, b.String(), false, nil
}

// startNews is the refresh as a scoper, planner or builder starts: what it is
// told, or how to bring the trunk in itself.
func (s *Server) startNews(t store.Ticket, role string) (store.Ticket, string) {
	t, news, conflicts, err := s.refresh(t)
	trunk := s.baseRef(false)
	switch {
	case err != nil || conflicts:
		return t, fmt.Sprintf("\n\nYour worktree is behind %[1]s and dv couldn't bring it up to date. Before anything else, run `git merge %[1]s`, resolving any conflict keeping what both sides meant, and commit the merge.", trunk)
	case news == "":
		return t, ""
	case role == roleScope:
		return t, news + " Write the ticket against the code as it stands now."
	case role == rolePlanner:
		return t, news + " The ticket may have been written before they landed: plan against the code as it stands now, and if they change what the ticket should ask for, raise it with `dv ticket ask`."
	}
	return t, news + " The plan was written before they landed. Before you write any code, check it still holds against them; if it doesn't, ask with `dv ticket ask` rather than building around it."
}

// beginRound brings the branch up to the trunk and has a reviewer go over
// it. A merge that conflicts is the builder's first, and starts no round.
func (s *Server) beginRound(t store.Ticket) (store.Ticket, bool, error) {
	t, news, conflicts, err := s.refresh(t)
	if conflicts {
		t, err = s.board.Update(t.ID, func(x *store.Ticket) error {
			x.Stage, x.Ready, x.Status = "build", false, "Bringing in "+s.baseRef(false)+": its conflicts are the builder's"
			return nil
		})
		return t, true, err
	}
	if news != "" {
		news += " dv merged them into the branch. Besides its own changes, check the branch against what landed: clashes that merge cleanly but break together - a migration, ID or version number both took; something renamed, removed or changed under the branch; config keys, schemas, formats or messages both touched. Report them as findings like any other.\n"
	}
	// A failed refresh is left on the ticket; the round goes ahead without it.
	t, err = s.startRound(t, news)
	return t, false, err
}

// startRound has a fresh reviewer go over the branch, or over what the trunk
// brought in as it caught up.
func (s *Server) startRound(t store.Ticket, news string) (store.Ticket, error) {
	templates := t.Templates
	if len(templates) == 0 {
		templates = ReviewTemplates
	}
	model, effort := s.rolePick(t, roleReviewer)
	kind, prompt := "", reviewPrompt(t, s.baseRef(false), templates)+news
	if t.CatchUp != nil {
		kind, templates, prompt = "main", nil, catchUpPrompt(t, s.baseRef(false))
	}
	t, err := s.board.Update(t.ID, func(x *store.Ticket) error {
		x.Rounds = append(x.Rounds, store.Round{Kind: kind, Templates: templates, Model: model, Phase: "review", At: time.Now().UTC().Truncate(time.Second)})
		setPick(x, roleReviewer, &model, &effort)
		return nil
	})
	if err != nil {
		return t, err
	}
	t, err = s.startRole(t, roleReviewer, t.Worktree, prompt)
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
	t, err := s.board.Update(t.ID, func(x *store.Ticket) error { x.PushedAt = time.Now().UTC(); return nil })
	if err != nil || t.PR != 0 {
		return t, err
	}
	// The builder's description, as the repository's pull requests are
	// written; the ticket stands in only where it wrote none.
	body, _ := s.board.Doc(t.ID, "pr")
	if strings.TrimSpace(body) == "" {
		ticket, _ := s.board.Doc(t.ID, "ticket")
		body = cmpOrStr(ticket, t.Request)
	}
	n, url, err := s.repo.CreatePR(t.Branch, cmp.Or(s.repo.DefaultBranch(), "main"), cmp.Or(t.PRTitle, t.Title), strings.TrimSpace(body))
	if err != nil {
		return t, fmt.Errorf("could not open its pull request: %w", err)
	}
	return s.board.Update(t.ID, func(x *store.Ticket) error {
		x.PR, x.PRURL, x.CI = n, url, "pending"
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
	// By is the ticket the agent belongs to, where it acts on another: a
	// scoper on one of its sub-tickets.
	By string `json:"by"`
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
	if rep.By != "" && rep.By != id {
		if rep.Role != roleScope || t.Parent != rep.By || (rep.Verb != "write" && rep.Verb != "answer") {
			return "", store.TicketError(fmt.Sprintf("only %s's scoper writes or answers for it, from its parent", id))
		}
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
	case "guide":
		if r := lastRound(t); rep.Role != roleReviewer || r == nil || r.Phase != "guide" {
			return "", store.TicketError("the guide is written once a review round is settled, by its reviewer")
		}
		return s.saveGuide(t, rep.Body)
	case "nothing":
		if map[string]string{roleScope: "scope", rolePlanner: "plan", roleBuilder: "build"}[rep.Role] != t.Stage {
			return "", wrong
		}
		if arg == "" {
			return "", store.TicketError("say why there is nothing to do, with the evidence")
		}
		_, err := s.board.Update(id, func(x *store.Ticket) error {
			x.Nothing = &store.Nothing{By: rep.Role, Why: arg, At: time.Now().UTC().Truncate(time.Second)}
			x.Ready, x.Status = false, ""
			return nil
		})
		return "The reader has it: they close the ticket, or tell you why not. Stop here.", err
	case "suggest":
		if arg == "" || strings.TrimSpace(rep.Body) == "" {
			return "", store.TicketError("give the follow-up a title, and its request on stdin")
		}
		_, err := s.board.Update(id, func(x *store.Ticket) error {
			x.Suggestions = append(x.Suggestions, store.Suggestion{ID: fmt.Sprintf("S%d", len(x.Suggestions)+1), Title: arg, Request: strings.TrimSpace(rep.Body), By: rep.Role, At: time.Now().UTC().Truncate(time.Second)})
			return nil
		})
		return "Suggested; the reader files it or dismisses it from the ticket. Carry on.", err
	case "pr":
		if rep.Role != roleBuilder {
			return "", wrong
		}
		if arg == "" || strings.TrimSpace(rep.Body) == "" {
			return "", store.TicketError("give the pull request a title, and its description on stdin")
		}
		if err := s.board.SaveDoc(id, "pr", rep.Body); err != nil {
			return "", err
		}
		if t, err = s.board.Update(id, func(x *store.Ticket) error { x.PRTitle = arg; return nil }); err != nil {
			return "", err
		}
		if t.PR == 0 {
			return "Saved; the reader opens the pull request with it after reviewing the work.", nil
		}
		if err := s.repo.EditPR(t.PR, arg, strings.TrimSpace(rep.Body)); err != nil {
			return "", fmt.Errorf("saved, but #%d could not be updated: %w", t.PR, err)
		}
		return fmt.Sprintf("Saved, and #%d updated.", t.PR), nil
	case "mockup":
		if rep.Role != roleScope && rep.Role != rolePlanner {
			return "", wrong
		}
		if !strings.Contains(strings.ToLower(rep.Body), "<html") && !strings.Contains(strings.ToLower(rep.Body), "<body") {
			return "", store.TicketError("a mock-up is one HTML page on stdin")
		}
		path, err := s.board.SaveMockup(id, arg, rep.Body)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Saved as %s; the ticket shows it. Name it in the ticket's Interface section.", filepath.Base(path)), nil
	case "reply":
		return s.replyToComment(t, rep.Role, rep.Args)
	case "file":
		if arg == "" {
			return "", store.TicketError("give the follow-up a title")
		}
		c, err := s.board.Add(store.Ticket{Title: arg, Request: rep.Body, Labels: t.Labels, From: id})
		if err != nil {
			return "", err
		}
		return "Filed " + c.ID + " in the Inbox, as a follow-up of " + id + ". It waits there until " + id + " is merged, unless the reader starts it sooner.", nil
	case "split":
		if rep.Role != roleScope {
			return "", store.TicketError("only the scoper splits a ticket into parts; file separate work with `dv ticket file`")
		}
		var after []string
		if rep.Flag != "" {
			after = []string{rep.Flag}
		}
		// The scoper wrote it, so it is the part's ticket; nobody requested it in words of their own.
		c, err := s.board.Add(store.Ticket{Title: arg, Parent: id, After: after, Labels: t.Labels, Base: t.Base, Stage: "plan", Started: "plan"})
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
		if rep.Role == rolePlanner && t.Stage == "plan" {
			return s.askScoper(t, arg)
		}
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
		if rep.Role == roleScope {
			return s.scoperAnswers(t, arg, rep.Flag)
		}
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

// askScoper sends the planner's product question back to Scope, where the
// scoper settles it, with the reader where the ticket doesn't, and updates
// the ticket. The plan waits.
func (s *Server) askScoper(t store.Ticket, q string) (string, error) {
	scoper, scoped := s.scoperOf(t)
	switch {
	case q == "":
		return "", store.TicketError("say what the ticket doesn't settle")
	case !scoped:
		return "", store.TicketError("this ticket wasn't scoped: ask the reader yourself, with AskUserQuestion")
	case t.ScopeQuestion != nil:
		return "", store.TicketError("your last question to the scoper is still open")
	}
	own := scoper.ID == t.ID
	t, err := s.board.Update(t.ID, func(x *store.Ticket) error {
		x.ScopeQuestion = &store.Question{Text: q, Asker: rolePlanner, At: time.Now().UTC().Truncate(time.Second)}
		x.Ready, x.Status = false, ""
		// A part was scoped in its parent, so it waits where it is.
		if own {
			x.Stage = "scope"
			if store.StageIndex(x.Reached) < store.StageIndex("plan") {
				x.Reached = "plan"
			}
		} else {
			x.Status = "With " + scoper.ID + "'s scoper"
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return "Sent to the scoper. Stop here; its answer comes as your next message.", s.tell(scoper, roleScope, scopeQuestionPrompt(t, q, !own))
}

// scoperOf is the ticket whose scoper settles t's product questions: its own,
// or for a part of a split ticket, the parent's, which scoped the part.
func (s *Server) scoperOf(t store.Ticket) (store.Ticket, bool) {
	if t.Sessions[roleScope] != "" {
		return t, true
	}
	if p, err := s.board.Get(t.Parent); err == nil && p.Sessions[roleScope] != "" {
		return p, true
	}
	return store.Ticket{}, false
}

func (s *Server) scoperAnswers(t store.Ticket, answer, source string) (string, error) {
	if t.ScopeQuestion == nil {
		return "", store.TicketError("there is no question from the planner to answer")
	}
	if answer == "" || source == "" {
		return "", store.TicketError(`give the answer and its --source: the part of the ticket it follows from, or "you" for the reader's choice`)
	}
	q := *t.ScopeQuestion
	q.Answer, q.Source, q.By = answer, source, roleScope
	if strings.EqualFold(source, "you") {
		q.By = "you"
	}
	t, err := s.board.Update(t.ID, func(x *store.Ticket) error {
		x.Answered = append(x.Answered, q)
		x.ScopeQuestion, x.Stage, x.Ready, x.Status = nil, "plan", false, ""
		return nil
	})
	if err != nil {
		return "", err
	}
	err = s.tell(t, rolePlanner, fmt.Sprintf("The scoper answered your question (%s):\n\n%s\n\nThe ticket is updated; `dv ticket show ticket` has it. Carry on.", sourceLine(q), answer))
	return "Answer sent; the planner carries on.", err
}

// reviewGuide is the reader's walk through a branch, one step at a time: what
// users see, then how it works, then what the tests guarantee; and what can be
// skimmed.
type reviewGuide struct {
	Steps []struct {
		Part    string `json:"part"` // users, internals or tests
		Title   string `json:"title"`
		File    string `json:"file"`
		Start   int    `json:"start"`
		End     int    `json:"end"`
		Say     string `json:"say"`
		Check   string `json:"check,omitempty"`
		Proves  string `json:"proves,omitempty"` // a test's
		Misses  string `json:"misses,omitempty"` // a test's
		Context bool   `json:"context,omitempty"`
	} `json:"steps"`
	Skim []struct {
		File string `json:"file"`
		Why  string `json:"why"`
	} `json:"skim"`
}

var guideParts = []string{"users", "internals", "tests"}

// saveGuide checks the reviewer's guide covers every file the branch changes
// and names only files there are, keeps it, and hands the ticket to the
// reader.
func (s *Server) saveGuide(t store.Ticket, body string) (string, error) {
	var g reviewGuide
	if err := json.Unmarshal([]byte(body), &g); err != nil {
		return "", store.TicketError("the guide is not the JSON asked for: " + err.Error())
	}
	if len(g.Steps) == 0 {
		return "", store.TicketError("the guide has no steps")
	}
	base := s.baseRef(false)
	changed, err := gitx.ChangedSince(t.Worktree, base)
	if err != nil {
		return "", err
	}
	named := map[string]bool{}
	var problems []string
	exists := func(f string) bool {
		_, err := os.Stat(filepath.Join(t.Worktree, filepath.FromSlash(f)))
		return err == nil || gitx.HasFile(t.Worktree, base, f)
	}
	note := func(f string) {
		if !named[f] && !exists(f) {
			problems = append(problems, f+" is neither on the branch nor in "+base)
		}
		named[f] = true
	}
	last := 0
	for i, st := range g.Steps {
		part := slices.Index(guideParts, st.Part)
		switch {
		case part < 0:
			problems = append(problems, fmt.Sprintf("step %d: part is users, internals or tests, not %q", i+1, st.Part))
		case part < last:
			problems = append(problems, fmt.Sprintf("step %d: the parts go in order, users, internals, then tests", i+1))
		case strings.TrimSpace(st.Title) == "" || st.File == "" || st.Start < 1:
			problems = append(problems, fmt.Sprintf("step %d needs a title, a file and the line it starts at", i+1))
		}
		last = max(last, part)
		note(st.File)
	}
	for _, sk := range g.Skim {
		note(sk.File)
	}
	for _, f := range changed {
		if !named[f] {
			problems = append(problems, f+" is changed but in no step and not in the skim list")
		}
	}
	if len(problems) > 0 {
		return "", store.TicketError("the guide was not saved:\n- " + strings.Join(slices.Compact(problems), "\n- "))
	}
	if err := s.board.SaveDoc(t.ID, "guide", body); err != nil {
		return "", err
	}
	// Caught up cleanly with nothing found, the reader's approval stands: what
	// changed is the trunk's, already merged there. Otherwise it is theirs again.
	resumed := false
	head := s.headOf(t)
	t, err = s.board.Update(t.ID, func(x *store.Ticket) error {
		round := &x.Rounds[len(x.Rounds)-1]
		round.Phase = "done"
		x.GuideRound = len(x.Rounds)
		x.Stage, x.Status, x.HandedAt = "review", "", head
		if c := x.CatchUp; c != nil && round.Kind == "main" {
			resumed = c.Approved && !c.Conflicts && len(round.Findings) == 0
			x.Merging, x.CatchUp = resumed, nil
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	// An open pull request gets the reviewed fixes; publishing one is the
	// reader's, which an approval that stands has given: approving opens it,
	// and catching up came before it could.
	if t.PR != 0 || resumed {
		if _, err := s.publish(t); err != nil {
			s.board.Update(t.ID, func(x *store.Ticket) error { x.Problem, x.Merging = err.Error(), false; return nil })
		}
	}
	if resumed {
		return "Saved; the branch merges once CI passes, as the reader approved it.", nil
	}
	return "Saved; the reader has the branch to review, with your guide.", nil
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
	if id := s.unanswered(t, role); id != "" {
		return "", store.TicketError(fmt.Sprintf("the reader's comment %s has no answer yet: dv ticket reply %s changed|answered \"...\"", id, id))
	}
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
	case reviewing(t):
		return s.roundReady(t, role)
	case role == roleBuilder && (t.Stage == "build" || t.Stage == "review"):
		if desc, _ := s.board.Doc(t.ID, "pr"); s.repo.Origin() != nil && t.PR == 0 && strings.TrimSpace(desc) == "" {
			return "", store.TicketError("write the pull request's title and description first, with `dv ticket pr`")
		}
		since := t.ReviewedAt
		if t.Stage == "review" {
			since = t.HandedAt
		}
		// Sent back with only questions, the builder answers and changes
		// nothing: there's no new code for a reviewer.
		if t.CatchUp == nil && since != "" && s.unchangedSince(t, since) {
			if t.Stage == "review" {
				return "Nothing changed since the reader got it, so there is nothing to hand on.", nil
			}
			_, err := s.board.Update(t.ID, func(x *store.Ticket) error {
				x.Stage, x.Status, x.Problem, x.HandedAt = "review", summary, "", since
				return nil
			})
			return "Nothing changed since the reader's review, so no reviewer is needed: it goes straight back to them with your answers.", err
		}
		// Changed while the reader had it, talking it over with them: reviewed
		// as work sent back is, and an approval given before doesn't cover it.
		t, err := s.board.Update(t.ID, func(x *store.Ticket) error {
			if x.Stage == "review" {
				x.Stage, x.Ready, x.Merging = "build", false, false
				x.ReviewedAt = cmp.Or(x.HandedAt, x.ReviewedAt)
			}
			x.Status, x.Problem = summary, ""
			return nil
		})
		if err != nil {
			return "", err
		}
		// Nothing leaves the machine until the reader opens the pull request;
		// one already open is kept up to date.
		if t.PR != 0 {
			if t, err = s.publish(t); err != nil {
				s.board.Update(t.ID, func(x *store.Ticket) error { x.Problem = err.Error(); return nil })
			}
		}
		_, conflicts, err := s.beginRound(t)
		if err != nil {
			return "", err
		}
		if conflicts {
			return roundConflictPrompt(s.baseRef(false)), nil
		}
		return "A reviewer has it now. Its findings will come to you.", nil
	}
	return "", wrong
}

// headOf is the commit the ticket's branch is at, "" before it has one.
func (s *Server) headOf(t store.Ticket) string {
	if t.Branch == "" {
		return ""
	}
	if c := s.repo.Log(t.Branch, 1); len(c) > 0 {
		return c[0].SHA
	}
	return ""
}

// unchangedSince is the branch still at commit, with nothing uncommitted in
// its worktree.
func (s *Server) unchangedSince(t store.Ticket, commit string) bool {
	head := s.repo.Log(t.Branch, 1)
	if len(head) == 0 || head[0].SHA != commit {
		return false
	}
	wt, err := gitx.Open(t.Worktree)
	if err != nil {
		return false
	}
	dirty, err := wt.Uncommitted()
	return err == nil && len(dirty) == 0
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
	model, effort := s.rolePick(t, rolePlanReviewer)
	t, err := s.board.Update(t.ID, func(x *store.Ticket) error {
		x.PlanReview = &store.Round{Model: model, Phase: "review", At: time.Now().UTC().Truncate(time.Second)}
		x.Status = "The plan is with a reviewer"
		setPick(x, rolePlanReviewer, &model, &effort)
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

// boardPick is a role's last model and effort picked on any ticket, nil for
// none, kept in the user's prefs under boardPicksPref by role.
type boardPick struct {
	Model  *string `json:"model,omitempty"`
	Effort *string `json:"effort,omitempty"`
}

const boardPicksPref = "boardPicks"

func (s *Server) boardPicks() map[string]boardPick {
	var all map[string]boardPick
	json.Unmarshal(s.user.All()[boardPicksPref], &all)
	return all
}

func (s *Server) rememberPick(role string, model, effort *string) {
	all := s.boardPicks()
	if all == nil {
		all = map[string]boardPick{}
	}
	p := all[role]
	if model != nil {
		p.Model = model
	}
	if effort != nil {
		p.Effort = effort
	}
	all[role] = p
	b, _ := json.Marshal(all)
	if err := s.user.Set(boardPicksPref, b, nil); err != nil {
		log.Printf("board: remembering the %s's pick: %v", role, err)
	}
}

// authorOf is the role whose work a reviewing role reads.
var authorOf = map[string]string{rolePlanReviewer: rolePlanner, roleReviewer: roleBuilder}

// pickFor is the model and effort role's agent starts on: the ticket's own,
// else the last picked for the role on any ticket. A reviewer not picked on
// the ticket is of another family than the author it reads, so the work gets
// a second family's eyes.
func pickFor(t store.Ticket, role string, models []agent.Model, last map[string]boardPick) (model, effort string) {
	l := last[role]
	model, picked := t.Model[role]
	if !picked {
		if l.Model != nil {
			model = *l.Model
		}
		if author, ok := authorOf[role]; ok && (l.Model == nil || familyIn(models, model) == familyIn(models, t.Model[author])) {
			model = otherModelIn(models, t.Model[author])
		}
	}
	effort, picked = t.Effort[role]
	if !picked && l.Effort != nil {
		effort = *l.Effort
	}
	return model, effort
}

func (s *Server) rolePick(t store.Ticket, role string) (model, effort string) {
	return pickFor(t, role, s.modelsFor(""), s.boardPicks())
}

// setPick records a role's model and effort on the ticket, nil leaving one as
// it is.
func setPick(t *store.Ticket, role string, model, effort *string) {
	if model != nil {
		if t.Model == nil {
			t.Model = map[string]string{}
		}
		t.Model[role] = *model
	}
	if effort != nil {
		if t.Effort == nil {
			t.Effort = map[string]string{}
		}
		t.Effort[role] = *effort
	}
}

// boardSessionPicked records a model or effort switched in a ticket agent's
// own session as its role's, on the ticket and for the tickets to come.
func (s *Server) boardSessionPicked(session string, model, effort *string) {
	if s.board == nil || !s.drivesBoard() {
		return
	}
	s.boardMu.Lock()
	defer s.boardMu.Unlock()
	for _, t := range s.board.List() {
		for role, id := range t.Sessions {
			if id != session {
				continue
			}
			s.board.Update(t.ID, func(x *store.Ticket) error {
				setPick(x, role, model, effort)
				return nil
			})
			s.rememberPick(role, model, effort)
			return
		}
	}
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

// familyIn is the family of a model as the list names it.
func familyIn(models []agent.Model, id string) string {
	for _, m := range models {
		if m.ID == id {
			return familyOf(cmp.Or(m.Model, m.ID))
		}
	}
	return familyOf(id)
}

// otherModelIn is a model of another family than the author's: Fable for an
// Opus author, Opus for a Fable one, as the model list names it. Before the
// list is known, the family's own name stands in.
func otherModelIn(models []agent.Model, author string) string {
	want := "fable"
	if familyIn(models, author) == "fable" {
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
	case r.Phase == "guide":
		return "", store.TicketError("write the reader's guide first, with `dv ticket guide`")
	case role == roleReviewer && r.Phase == "review" && len(r.Findings) == 0,
		role == roleReviewer && r.Phase == "recheck" && !slices.ContainsFunc(r.Findings, func(f store.Finding) bool { return f.Recheck == "fails" }):
		if missing := unanswered(r.Findings, func(f store.Finding) string { return f.Recheck }); r.Phase == "recheck" && missing != "" {
			return "", store.TicketError(missing + " has no recheck yet")
		}
		_, err := s.board.Update(t.ID, func(x *store.Ticket) error {
			x.Rounds[len(x.Rounds)-1].Phase = "guide"
			x.Status = "Writing your review guide"
			return nil
		})
		if err != nil {
			return "", err
		}
		// Caught up, the walkthrough stands; its places moved with the merge.
		if old, _ := s.board.Doc(t.ID, "guide"); r.Kind == "main" && strings.TrimSpace(old) != "" {
			return guideUpdatePrompt(s.baseRef(false), old), nil
		}
		return guidePrompt(s.baseRef(false)), nil
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
// review running: the plan's in Plan, a round of the code's in Build.
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
	} else if !reviewing(t) {
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
// showParent gives a sub-ticket its background: the ticket it is a part of,
// which holds the problem, the edge cases and the decisions it doesn't repeat,
// and what the other parts carry.
func (s *Server) showParent(b *strings.Builder, t store.Ticket) {
	if t.Parent == "" {
		return
	}
	p, err := s.board.Get(t.Parent)
	if err != nil {
		return
	}
	text, _ := s.board.Doc(p.ID, "ticket")
	fmt.Fprintf(b, "# The parent ticket, %s: %s\n\nThis ticket is one part of it. Its problem, edge cases and decisions hold for this part too, where this ticket doesn't say otherwise.\n\n%s\n\n", p.ID, p.Title, cmp.Or(strings.TrimSpace(text), p.Request))
	var others []string
	for _, c := range s.board.List() {
		if c.Parent == p.ID && c.ID != t.ID {
			others = append(others, fmt.Sprintf("- %s: %s (%s)", c.ID, c.Title, c.Stage))
		}
	}
	if len(others) > 0 {
		fmt.Fprintf(b, "Its other parts, which this one leaves to them:\n%s\n\n", strings.Join(others, "\n"))
	}
}

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
		// A part made before its request was left empty holds its ticket there.
		if t.Request != "" && t.Parent == "" {
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
		s.showParent(&b, t)
		doc("ticket")
	}
	if which == "" || which == "plan" {
		doc("plan")
	}
	if which == "" {
		if mockups := s.board.Mockups(id); len(mockups) > 0 {
			b.WriteString("# Mock-ups of the interface (HTML pages; read them for how the UI should look and behave)\n\n")
			for _, p := range mockups {
				fmt.Fprintf(&b, "- %s\n", p)
			}
			b.WriteString("\n")
		}
		if t.Question != nil {
			fmt.Fprintf(&b, "# Open question from the builder\n\n%s\n\n", t.Question.Text)
		}
		if t.ScopeQuestion != nil {
			fmt.Fprintf(&b, "# Open question from the planner\n\n%s\n\n", t.ScopeQuestion.Text)
		}
		for _, q := range t.Answered {
			during := map[string]string{rolePlanner: "planning"}[q.Asker]
			fmt.Fprintf(&b, "Answered during %s (%s): %s\n  %s\n", cmp.Or(during, "build"), sourceLine(q), q.Text, q.Answer)
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
		if r := lastRound(t); reviewing(t) {
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
	told := map[string][2]string{} // by ticket, the reason told of and its notice
	first := true
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
		s.tellBoard(told, first)
		first = false
	}
}

// boardNotice is the one notice the board raises: it needs the reader, and
// opening it opens the board.
func boardNotice(t store.Ticket, why string) notify.Notice {
	return notify.Notice{ID: noticeID(), Kind: notify.Done, Board: true, Title: "The board needs your attention", Where: t.ID + " · " + t.Title, Body: why}
}

// needsReader is why the ticket waits on the reader, as the board tells of
// it, "" for nothing; an agent's question is told of as a request.
func needsReader(v ticketView) string {
	switch {
	case v.Archived:
		return ""
	case v.Problem != "":
		return v.Problem
	case v.Ready && !v.Working && v.Stage == "scope":
		return "The ticket is ready to approve"
	case v.Ready && !v.Working && v.Stage == "plan":
		return "The plan is ready to approve"
	case v.Stage == "review" && !v.Merging:
		return "Ready for your review"
	}
	return ""
}

// tellBoard raises a notice for each ticket newly waiting on the reader, and
// settles the one told of once that no longer holds. quiet only notes what
// waits already, as dv starts: the board shows it, and a restart is no news.
func (s *Server) tellBoard(told map[string][2]string, quiet bool) {
	if s.notices == nil {
		return
	}
	now := map[string]bool{}
	for _, v := range s.ticketViews() {
		why := needsReader(v)
		if why == "" {
			continue
		}
		now[v.ID] = true
		if told[v.ID][0] == why {
			continue
		}
		if old := told[v.ID][1]; old != "" {
			s.notices.Settle(old, "")
		}
		if quiet {
			told[v.ID] = [2]string{why, ""}
			continue
		}
		n := boardNotice(v.Ticket, why)
		told[v.ID] = [2]string{why, n.ID}
		s.notices.Raise(n)
	}
	for id, t := range told {
		if !now[id] {
			if t[1] != "" {
				s.notices.Settle(t[1], "")
			}
			delete(told, id)
		}
	}
}

// stallNotice is the notice for a ticket's agent whose turn ended short of
// handing the ticket on: the stage's own agent, the ticket not ready, and no
// one else to wait on. An agent talked to after handing over has no notice.
func (s *Server) stallNotice(b boardSession, session string) (notify.Notice, bool) {
	t, err := s.board.Get(b.Ticket)
	if err != nil || t.Archived || t.Sessions[ownerOf(t)] != session || t.Ready {
		return notify.Notice{}, false
	}
	if t.Stage != "scope" && t.Stage != "plan" && t.Stage != "build" {
		return notify.Notice{}, false
	}
	why := fmt.Sprintf("The %s stopped without handing the ticket on", roleName[ownerOf(t)])
	if s.agent.Failed(session) {
		why = fmt.Sprintf("The %s stopped on an error", roleName[ownerOf(t)])
	}
	return boardNotice(t, why), true
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

// checksDue is a push too recent for a pull request with no checks listed to
// be taken as having no CI: GitHub takes a while to register a workflow's
// runs. A branch with workflows of its own is given longer.
func checksDue(t store.Ticket) bool {
	if t.PushedAt.IsZero() {
		return false
	}
	grace := 2 * time.Minute
	if fi, err := os.Stat(filepath.Join(t.Worktree, ".github", "workflows")); t.Worktree != "" && err == nil && fi.IsDir() {
		grace = 10 * time.Minute
	}
	return time.Since(t.PushedAt) < grace
}

func (s *Server) followPRs() {
	for _, t := range s.board.List() {
		// Approved with no pull request: one a catch-up left unopened, before
		// dv opened it on the approval standing. Opening it is what approving
		// asked for.
		if t.Merging && t.PR == 0 && t.Stage == "review" && !t.Archived && s.repo.Origin() != nil {
			s.boardMu.Lock()
			if _, err := s.publish(t); err != nil {
				s.board.Update(t.ID, func(x *store.Ticket) error { x.Problem, x.Merging = err.Error(), false; return nil })
			}
			s.boardMu.Unlock()
			continue
		}
		if t.PR == 0 || t.Stage == "merged" || t.Archived {
			continue
		}
		pr, err := s.repo.ViewPR(t.PR)
		if err != nil {
			continue
		}
		// None listed just after a push is checks not registered yet, not no CI.
		if pr.Checks == "" && checksDue(t) {
			pr.Checks = "pending"
		}
		s.boardMu.Lock()
		t, _ = s.board.Update(t.ID, func(x *store.Ticket) error {
			x.CI, x.PRURL = pr.Checks, cmp.Or(pr.URL, x.PRURL)
			if pr.State == "merged" {
				x.Stage, x.Merging = "merged", false
			}
			if x.Merging && pr.Checks == "failing" {
				x.Merging, x.Problem = false, "CI failed, so it wasn't merged"
			}
			return nil
		})
		// The trunk moving on while it waited, conflicting or merely ahead,
		// is caught up with as it was at approving.
		if t.Merging && (pr.Conflicts || pr.Checks != "pending" && pr.Checks != "failing") {
			if c, behind, err := s.catchUp(t, true); behind || err != nil {
				if err != nil {
					s.board.Update(t.ID, func(x *store.Ticket) error {
						x.Merging, x.Problem = false, "could not catch up: "+err.Error()
						return nil
					})
				}
				t = c
			} else if err := s.repo.MergePR(t.PR); err != nil {
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
