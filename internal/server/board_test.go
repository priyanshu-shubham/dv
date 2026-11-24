package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"dv/internal/agent"
	"dv/internal/store"
)

// boardServer is a dv on a fresh repository whose tickets' sessions don't
// exist: telling one fails, after the ticket has moved, so what moves can be
// checked without running an agent.
func boardServer(t *testing.T) *Server {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", "-b", "main", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	user, err := store.OpenUserPrefs()
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir, user, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

const (
	builderID  = "00000000-0000-0000-0000-00000000000b"
	plannerID  = "00000000-0000-0000-0000-00000000000p"
	reviewerID = "00000000-0000-0000-0000-00000000000r"
)

func say(t *testing.T, s *Server, id, role, verb string, args ...string) {
	t.Helper()
	// The error is only the absent session not being told.
	s.report(id, agentReport{Role: role, Verb: verb, Args: args})
}

func sayFlag(t *testing.T, s *Server, id, role, verb, flag string, args ...string) {
	t.Helper()
	s.report(id, agentReport{Role: role, Verb: verb, Args: args, Flag: flag})
}

func TestReviewRoundGoesBackAndForthUntilEveryFindingHolds(t *testing.T) {
	s := boardServer(t)
	tk, _ := s.board.Add(store.Ticket{Title: "Pin the exit code"})
	s.board.Update(tk.ID, func(x *store.Ticket) error {
		x.Stage, x.Worktree, x.Branch = "build", t.TempDir(), "b"
		x.Sessions = map[string]string{roleBuilder: builderID, roleReviewer: reviewerID}
		x.Rounds = []store.Round{{Templates: ReviewTemplates, Phase: "review", Session: reviewerID}}
		return nil
	})
	round := func() store.Round {
		got, _ := s.board.Get(tk.ID)
		return got.Rounds[len(got.Rounds)-1]
	}

	if _, err := s.report(tk.ID, agentReport{Role: roleBuilder, Verb: "verdict", Args: []string{"F1", "fixed"}}); err == nil {
		t.Fatal("the builder gave a verdict while the reviewer was reviewing")
	}
	say(t, s, tk.ID, roleReviewer, "finding", "a cancelled task reports failed")
	say(t, s, tk.ID, roleReviewer, "finding", "the name hides a fallback")
	say(t, s, tk.ID, roleReviewer, "ready")
	if r := round(); r.Phase != "fix" || len(r.Findings) != 2 {
		t.Fatalf("after the review: %+v", r)
	}

	say(t, s, tk.ID, roleBuilder, "verdict", "F1", "fixed", "reports cancelled now")
	if _, err := s.report(tk.ID, agentReport{Role: roleBuilder, Verb: "ready"}); err == nil {
		t.Fatal("the builder handed back with F2 unanswered")
	}
	if _, err := s.report(tk.ID, agentReport{Role: roleBuilder, Verb: "verdict", Args: []string{"F2", "rejected"}}); err == nil {
		t.Fatal("a rejection went without its reason")
	}
	say(t, s, tk.ID, roleBuilder, "verdict", "f2", "rejected", "the name says both")
	say(t, s, tk.ID, roleBuilder, "ready")
	if r := round(); r.Phase != "recheck" {
		t.Fatalf("after the fixes: %+v", r)
	}

	say(t, s, tk.ID, roleReviewer, "recheck", "F1", "fails", "still failed on exit 2")
	say(t, s, tk.ID, roleReviewer, "recheck", "F2", "holds", "fair")
	say(t, s, tk.ID, roleReviewer, "ready")
	r := round()
	if r.Phase != "fix" || r.Findings[0].Verdict != "" || r.Findings[1].Verdict != "rejected" {
		t.Fatalf("a failed recheck didn't go back to the builder alone: %+v", r)
	}

	say(t, s, tk.ID, roleBuilder, "verdict", "F1", "fixed", "exit 2 too")
	say(t, s, tk.ID, roleBuilder, "ready")
	say(t, s, tk.ID, roleReviewer, "recheck", "F1", "holds", "ok")
	say(t, s, tk.ID, roleReviewer, "ready")
	got, _ := s.board.Get(tk.ID)
	if got.Stage != "build" || round().Phase != "guide" {
		t.Fatalf("a round with every finding holding didn't ask for the reader's guide: %s %+v", got.Stage, round())
	}
}

func TestReviewerGuideCoversTheBranchBeforeTheReaderHasIt(t *testing.T) {
	s := boardServer(t)
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", s.repo.Root, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	write := func(name, text string) { os.WriteFile(filepath.Join(s.repo.Root, name), []byte(text), 0o644) }
	write("a.go", "package a\n")
	write("caller.go", "package a\n")
	git("add", ".")
	git("commit", "-qm", "base")
	git("checkout", "-qb", "feat")
	write("a.go", "package a\n\nfunc A() {}\n")
	write("a_test.go", "package a\n")
	git("add", ".")
	git("commit", "-qm", "change")

	tk, _ := s.board.Add(store.Ticket{Title: "A"})
	s.board.Update(tk.ID, func(x *store.Ticket) error {
		x.Stage, x.Worktree, x.Branch = "build", s.repo.Root, "feat"
		x.Sessions = map[string]string{roleReviewer: reviewerID}
		x.Rounds = []store.Round{{Phase: "guide", Session: reviewerID}}
		return nil
	})
	guide := func(body string) error {
		_, err := s.report(tk.ID, agentReport{Role: roleReviewer, Verb: "guide", Body: body})
		return err
	}
	user := `{"part":"users","title":"A is exported","file":"a.go","start":3,"say":"Callers can now call A."}`
	caller := `{"part":"internals","title":"Its caller","file":"caller.go","start":1,"say":"Still builds.","context":true}`
	test := `{"part":"tests","title":"No test yet","file":"a_test.go","start":1,"say":"Empty.","proves":"nothing","misses":"A"}`
	if err := guide(`{"steps":[` + user + `,` + caller + `]}`); err == nil || !strings.Contains(err.Error(), "a_test.go") {
		t.Fatalf("a walkthrough leaving out a changed file: %v", err)
	}
	if err := guide(`{"steps":[` + user + `,` + caller + `,` + test + `],"skim":[{"file":"nope.go","why":"?"}]}`); err == nil || !strings.Contains(err.Error(), "nope.go") {
		t.Fatalf("a walkthrough naming a file there isn't: %v", err)
	}
	if err := guide(`{"steps":[` + test + `,` + user + `]}`); err == nil || !strings.Contains(err.Error(), "in order") {
		t.Fatalf("a walkthrough with its parts out of order: %v", err)
	}
	if err := guide(`{"steps":[` + user + `,` + caller + `,` + test + `]}`); err != nil {
		t.Fatal(err)
	}
	got, _ := s.board.Get(tk.ID)
	if got.Stage != "review" || got.GuideRound != 1 || got.Rounds[0].Phase != "done" {
		t.Fatalf("after the guide: %+v", got)
	}
}

func TestSuggestedFollowUpIsTheReadersToFile(t *testing.T) {
	s := boardServer(t)
	tk, _ := s.board.Add(store.Ticket{Title: "Upload speed", Labels: []string{"CAS"}})
	say(t, s, tk.ID, roleReviewer, "suggest", "Cancelled recording loses rows")
	if got, _ := s.board.Get(tk.ID); len(got.Suggestions) != 0 {
		t.Fatal("a suggestion with no request was kept")
	}
	s.report(tk.ID, agentReport{Role: roleReviewer, Verb: "suggest", Args: []string{"Cancelled recording loses rows"}, Body: "## Problem\nrows go"})
	s.report(tk.ID, agentReport{Role: roleBuilder, Verb: "suggest", Args: []string{"Rename the flag"}, Body: "it reads oddly"})
	act := func(sid string, file bool) {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(fmt.Sprintf(`{"file":%v}`, file)))
		r.SetPathValue("id", tk.ID)
		r.SetPathValue("sid", sid)
		w := httptest.NewRecorder()
		s.handleSuggestion(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", sid, w.Code, w.Body)
		}
	}
	edit := func(body string) int {
		r := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(body))
		r.SetPathValue("id", tk.ID)
		r.SetPathValue("sid", "S1")
		w := httptest.NewRecorder()
		s.handleEditSuggestion(w, r)
		return w.Code
	}
	if code := edit(`{"title":"  ","request":"x"}`); code != http.StatusBadRequest {
		t.Fatalf("a follow-up reworded with no title: %d", code)
	}
	if code := edit(`{"title":"Cancelled upload loses recorded sizes","request":"Rows recorded before a cancel are lost."}`); code != http.StatusOK {
		t.Fatalf("rewording: %d", code)
	}
	act("S1", true)
	act("S2", false)
	if got, _ := s.board.Get(tk.ID); len(got.Suggestions) != 0 {
		t.Fatalf("left: %+v", got.Suggestions)
	}
	var filed []store.Ticket
	for _, x := range s.board.List() {
		if x.From == tk.ID {
			filed = append(filed, x)
		}
	}
	if len(filed) != 1 || filed[0].Title != "Cancelled upload loses recorded sizes" || filed[0].Request != "Rows recorded before a cancel are lost." || filed[0].Stage != "inbox" || filed[0].Labels[0] != "CAS" {
		t.Fatalf("filed: %+v", filed)
	}
}

func TestBuilderQuestionGoesToThePlannerAndBack(t *testing.T) {
	s := boardServer(t)
	tk, _ := s.board.Add(store.Ticket{Title: "Retry dropped dispatches"})
	s.board.Update(tk.ID, func(x *store.Ticket) error {
		x.Stage, x.Started = "build", "scope"
		x.Sessions = map[string]string{roleBuilder: builderID, rolePlanner: plannerID}
		return nil
	})

	if _, err := s.report(tk.ID, agentReport{Role: rolePlanner, Verb: "answer", Args: []string{"yes"}, Flag: "you"}); err == nil {
		t.Fatal("the planner answered a question nobody asked")
	}
	say(t, s, tk.ID, roleBuilder, "ask", "what happens to the first copy?")
	got, _ := s.board.Get(tk.ID)
	if got.Stage != "plan" || got.Reached != "build" || got.Question == nil {
		t.Fatalf("the question didn't send the ticket back to Plan: %+v", got)
	}
	if _, err := s.report(tk.ID, agentReport{Role: rolePlanner, Verb: "answer", Args: []string{"ask the worker"}}); err == nil {
		t.Fatal("an answer went without its source")
	}

	sayFlag(t, s, tk.ID, rolePlanner, "answer", "decision 2", "ask the worker on reconnect")
	got, _ = s.board.Get(tk.ID)
	if got.Stage != "build" || got.Question != nil || len(got.Answered) != 1 {
		t.Fatalf("the answer didn't hand the ticket back to the builder: %+v", got)
	}
	if a := got.Answered[0]; a.By != "planner" || a.Source != "decision 2" || a.Text != "what happens to the first copy?" {
		t.Fatalf("answered %+v", a)
	}
}

func TestAnyStageFilesAFollowUpInTheInbox(t *testing.T) {
	s := boardServer(t)
	tk, _ := s.board.Add(store.Ticket{Title: "Speed up uploads", Labels: []string{"CAS"}})
	s.board.Update(tk.ID, func(x *store.Ticket) error {
		x.Stage, x.Started = "plan", "scope"
		x.Sessions = map[string]string{rolePlanner: plannerID}
		return nil
	})

	if _, err := s.report(tk.ID, agentReport{Role: rolePlanner, Verb: "split", Args: []string{"part"}}); err == nil || !strings.Contains(err.Error(), "dv ticket file") {
		t.Fatalf("the planner's split wasn't pointed to a follow-up: %v", err)
	}
	if _, err := s.report(tk.ID, agentReport{Role: rolePlanner, Verb: "file", Args: []string{"Bound", "metadata", "memory"}, Body: "## Problem\nheld manifests grow\n"}); err != nil {
		t.Fatal(err)
	}
	var f store.Ticket
	for _, x := range s.board.List() {
		if x.From == tk.ID {
			f = x
		}
	}
	if f.Stage != "inbox" || f.Title != "Bound metadata memory" || f.Request != "## Problem\nheld manifests grow" || f.Parent != "" || len(f.After) != 0 || len(f.Labels) != 1 {
		t.Fatalf("the follow-up: %+v", f)
	}
	if got, _ := s.board.Get(tk.ID); got.Stage != "plan" || got.Split {
		t.Fatalf("filing a follow-up changed its source: %+v", got)
	}
	s.board.Delete(tk.ID)
	if got, _ := s.board.Get(f.ID); got.From != "" {
		t.Fatalf("the follow-up still came from a deleted ticket: %+v", got)
	}
}

func TestScoperKeepsAMockupWithTheTicket(t *testing.T) {
	s := boardServer(t)
	tk, _ := s.board.Add(store.Ticket{Title: "Settings page", Stage: "scope"})
	page := "<!doctype html><html><body>Settings</body></html>"

	if _, err := s.report(tk.ID, agentReport{Role: roleBuilder, Verb: "mockup", Args: []string{"x"}, Body: page}); err == nil {
		t.Fatal("the builder kept a mock-up")
	}
	if _, err := s.report(tk.ID, agentReport{Role: roleScope, Verb: "mockup", Args: []string{"x"}, Body: "just words"}); err == nil {
		t.Fatal("a mock-up that isn't a page was kept")
	}
	for range 2 {
		if _, err := s.report(tk.ID, agentReport{Role: roleScope, Verb: "mockup", Args: []string{"Empty", "state"}, Body: page}); err != nil {
			t.Fatal(err)
		}
	}
	got := s.board.Mockups(tk.ID)
	if len(got) != 1 || filepath.Base(got[0]) != "empty-state.html" {
		t.Fatalf("mock-ups %v", got)
	}
	if b, _ := os.ReadFile(got[0]); string(b) != page {
		t.Fatalf("kept %q", b)
	}
}

func TestPlannersProductQuestionGoesToTheScoperAndBack(t *testing.T) {
	s := boardServer(t)
	tk, _ := s.board.Add(store.Ticket{Title: "Upload speed"})
	s.board.Update(tk.ID, func(x *store.Ticket) error {
		x.Stage, x.Started, x.Reached = "build", "scope", "build"
		x.Sessions = map[string]string{roleScope: "s", rolePlanner: plannerID, roleBuilder: builderID}
		return nil
	})
	get := func() store.Ticket { got, _ := s.board.Get(tk.ID); return got }

	say(t, s, tk.ID, roleBuilder, "ask", "may the flag be renamed?")
	say(t, s, tk.ID, rolePlanner, "ask", "is a CLI flag wanted at all?")
	if got := get(); got.Stage != "scope" || got.ScopeQuestion == nil || got.Question == nil || got.Reached != "build" {
		t.Fatalf("the planner's question while answering the builder's: %+v", got)
	}
	if _, err := s.report(tk.ID, agentReport{Role: rolePlanner, Verb: "ask", Args: []string{"another"}}); err == nil {
		t.Fatal("the planner asked from Scope")
	}
	sayFlag(t, s, tk.ID, roleScope, "answer", "you", "yes, --upload-workers")
	got := get()
	if got.Stage != "plan" || got.ScopeQuestion != nil || got.Question == nil {
		t.Fatalf("the answer didn't bring the ticket back to the planner, with the builder's question still open: %+v", got)
	}
	if a := got.Answered[0]; a.Asker != rolePlanner || a.By != "you" || a.Answer != "yes, --upload-workers" {
		t.Fatalf("answered %+v", a)
	}
	sayFlag(t, s, tk.ID, rolePlanner, "answer", "you", "rename it")
	if got := get(); got.Stage != "build" {
		t.Fatalf("the builder's answer didn't resume the build: %s", got.Stage)
	}

	unscoped, _ := s.board.Add(store.Ticket{Title: "Begun at Plan", Stage: "plan", Started: "plan"})
	if _, err := s.report(unscoped.ID, agentReport{Role: rolePlanner, Verb: "ask", Args: []string{"which?"}}); err == nil || !strings.Contains(err.Error(), "AskUserQuestion") {
		t.Fatalf("an unscoped ticket's planner wasn't told to ask the reader: %v", err)
	}
}

func TestSubTicketShowsItsParentsBackground(t *testing.T) {
	s := boardServer(t)
	parent, _ := s.board.Add(store.Ticket{Title: "Long-running services", Stage: "scope"})
	s.board.SaveDoc(parent.ID, "ticket", "## Problem\nNothing brings a process back when it exits.")
	api, _ := s.board.Add(store.Ticket{Title: "Services in the API", Parent: parent.ID, Stage: "plan", Started: "plan"})
	s.board.SaveDoc(api.ID, "ticket", "## Goal\nThe parent's behaviour, through the API.")
	s.board.Add(store.Ticket{Title: "Services in the web UI", Parent: parent.ID, After: []string{api.ID}, Stage: "plan", Started: "plan"})

	out, err := s.showTicket(api.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Nothing brings a process back", "The parent's behaviour", "Services in the web UI"} {
		if !strings.Contains(out, want) {
			t.Errorf("show lacks %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "Nothing brings") > strings.Index(out, "The parent's behaviour") {
		t.Error("the background comes after the part it is background to")
	}
}

func TestCodeCommentsFromYourReviewGoBackToBuild(t *testing.T) {
	onlyGit(t)
	s := boardServer(t)
	tk, _ := s.board.Add(store.Ticket{Title: "Greet in other languages"})
	s.board.Update(tk.ID, func(x *store.Ticket) error {
		x.Stage, x.Started, x.Worktree = "review", "plan", s.repo.Root
		x.Sessions = map[string]string{roleBuilder: builderID}
		return nil
	})
	post := func(body string) int {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		r.SetPathValue("id", tk.ID)
		w := httptest.NewRecorder()
		s.handleCodeComments(w, r)
		return w.Code
	}
	if code := post(`{"comments":[]}`); code != http.StatusBadRequest {
		t.Fatalf("no comments: %d", code)
	}
	post(`{"comments":[{"file":"main.go","start":12,"end":12,"side":"new","quote":"\"fr\": \"bonjour\",","text":"Say why"}]}`)
	if got, _ := s.board.Get(tk.ID); got.Stage != "build" {
		t.Fatalf("comments from your review left the ticket in %s", got.Stage)
	}
}

func TestPartsProductQuestionGoesToItsParentsScoper(t *testing.T) {
	s := boardServer(t)
	parent, _ := s.board.Add(store.Ticket{Title: "Long-running services", Stage: "scope"})
	s.board.Update(parent.ID, func(x *store.Ticket) error {
		x.Split, x.Sessions = true, map[string]string{roleScope: "s"}
		return nil
	})
	part, _ := s.board.Add(store.Ticket{Title: "Services in the API", Parent: parent.ID, Stage: "plan", Started: "plan"})
	s.board.Update(part.ID, func(x *store.Ticket) error { x.Sessions = map[string]string{rolePlanner: plannerID}; return nil })
	get := func() store.Ticket { got, _ := s.board.Get(part.ID); return got }

	say(t, s, part.ID, rolePlanner, "ask", "does start rebuild a failed build?")
	if got := get(); got.Stage != "plan" || got.ScopeQuestion == nil {
		t.Fatalf("the part's question: %+v", got)
	}
	if _, err := s.report(part.ID, agentReport{Role: rolePlanner, By: parent.ID, Verb: "write", Args: []string{"plan"}, Body: "x"}); err == nil {
		t.Fatal("a role other than the scoper wrote for a part")
	}
	if _, err := s.report(part.ID, agentReport{Role: roleScope, By: parent.ID, Verb: "write", Args: []string{"ticket"}, Body: "## Decisions\n- Start rebuilds a failed build."}); err != nil {
		t.Fatal(err)
	}
	// The error is only the absent planner not being told.
	s.report(part.ID, agentReport{Role: roleScope, By: parent.ID, Verb: "answer", Args: []string{"yes"}, Flag: "you"})
	if got := get(); got.ScopeQuestion != nil || len(got.Answered) != 1 || got.Status != "" {
		t.Fatalf("the parent's scoper's answer: %+v", got)
	}
	if doc, _ := s.board.Doc(part.ID, "ticket"); !strings.Contains(doc, "Start rebuilds") {
		t.Fatalf("the part's ticket: %q", doc)
	}
}

func TestBuilderWritesThePullRequestAndNothingIsPublishedOnHandOver(t *testing.T) {
	s := boardServer(t)
	tk, _ := s.board.Add(store.Ticket{Title: "Greet in other languages", Stage: "build", Started: "plan"})
	s.board.Update(tk.ID, func(x *store.Ticket) error { x.Sessions = map[string]string{roleBuilder: builderID}; return nil })

	if _, err := s.report(tk.ID, agentReport{Role: rolePlanner, Verb: "pr", Args: []string{"x"}, Body: "y"}); err == nil {
		t.Fatal("the planner wrote the pull request")
	}
	if _, err := s.report(tk.ID, agentReport{Role: roleBuilder, Verb: "pr", Args: []string{"Greet", "in", "es,", "fr", "and", "de"}, Body: "## Summary\n\n- `greet` takes a language.\n"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.board.Get(tk.ID)
	if doc, _ := s.board.Doc(tk.ID, "pr"); got.PRTitle != "Greet in es, fr and de" || !strings.Contains(doc, "takes a language") {
		t.Fatalf("title %q, description %q", got.PRTitle, doc)
	}
	say(t, s, tk.ID, roleBuilder, "ready", "built")
	if got, _ := s.board.Get(tk.ID); got.PR != 0 || got.Stage != "build" || !reviewing(got) {
		t.Fatalf("the hand-over: %+v", got)
	}
}

func TestBoardTellsOnlyOfWhatNeedsTheReader(t *testing.T) {
	s := boardServer(t)
	tk, _ := s.board.Add(store.Ticket{Title: "Upload speed"})
	set := func(f func(x *store.Ticket)) store.Ticket {
		got, _ := s.board.Update(tk.ID, func(x *store.Ticket) error { f(x); return nil })
		return got
	}
	b := boardSession{Ticket: tk.ID, Role: roleBuilder}

	set(func(x *store.Ticket) {
		x.Stage, x.Sessions = "build", map[string]string{roleBuilder: builderID, roleReviewer: reviewerID}
	})
	if n, stalled := s.stallNotice(b, builderID); !stalled || !n.Board || !strings.Contains(n.Body, "builder stopped without handing") {
		t.Fatalf("a builder stopping mid-build: %v %+v", stalled, n)
	}
	set(func(x *store.Ticket) { x.Rounds = []store.Round{{Phase: "review", Session: reviewerID}} })
	if _, stalled := s.stallNotice(b, builderID); stalled {
		t.Fatal("the builder's turn ending once the reviewer has the ticket was told of")
	}
	set(func(x *store.Ticket) { x.Stage, x.Rounds = "review", nil })
	if _, stalled := s.stallNotice(b, builderID); stalled {
		t.Fatal("the builder answering the reader in Your review was told of")
	}

	view := func(x store.Ticket) ticketView { return ticketView{Ticket: x} }
	for why, x := range map[string]store.Ticket{
		"Ready for your review":        {Stage: "review"},
		"The plan is ready to approve": {Stage: "plan", Ready: true},
		"could not push":               {Stage: "build", Problem: "could not push"},
		"":                             {Stage: "build"},
	} {
		if got := needsReader(view(x)); got != why {
			t.Errorf("%+v: %q, not %q", x, got, why)
		}
	}
	if got := needsReader(ticketView{Ticket: store.Ticket{Stage: "plan", Ready: true}, Working: true}); got != "" {
		t.Errorf("a plan being revised on the reader's message: %q", got)
	}
}

// onlyGit leaves git alone on the PATH, so a session dv starts can't run an
// agent.
func onlyGit(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	bin := t.TempDir()
	if err := os.Symlink(git, filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
}

func TestPlanGoesThroughItsReviewBeforeTheReader(t *testing.T) {
	onlyGit(t)
	s := boardServer(t)
	tk, _ := s.board.Add(store.Ticket{Title: "Speed up uploads"})
	worktree := filepath.Join(s.repo.Root, ".dv", "worktrees", tk.ID)
	os.MkdirAll(worktree, 0o755)
	s.board.Update(tk.ID, func(x *store.Ticket) error {
		x.Stage, x.Worktree = "plan", worktree
		x.Sessions = map[string]string{rolePlanner: plannerID}
		return nil
	})
	get := func() store.Ticket {
		got, _ := s.board.Get(tk.ID)
		return got
	}

	say(t, s, tk.ID, rolePlanner, "ready", "plan written")
	got := get()
	if got.Ready || got.PlanReview == nil || got.PlanReview.Phase != "review" || got.Sessions[rolePlanReviewer] == "" {
		t.Fatalf("the plan went to the reader unreviewed: %+v", got)
	}
	if got.PlanReview.Model != "fable" || ownerOf(got) != rolePlanReviewer {
		t.Fatalf("reviewed by %q, owned by %s", got.PlanReview.Model, ownerOf(got))
	}
	if _, err := s.report(tk.ID, agentReport{Role: rolePlanner, Verb: "ready"}); err == nil {
		t.Fatal("the planner handed on past its reviewer")
	}

	say(t, s, tk.ID, rolePlanReviewer, "finding", "hashing is 60% of the profile and no decision touches it")
	say(t, s, tk.ID, rolePlanReviewer, "ready")
	if got := get(); got.PlanReview.Phase != "judge" || got.Ready {
		t.Fatalf("the findings didn't go to the planner: %+v", got.PlanReview)
	}
	if _, err := s.report(tk.ID, agentReport{Role: rolePlanner, Verb: "verdict", Args: []string{"F1", "fixed"}}); err == nil {
		t.Fatal("a plan finding took the code review's verdict")
	}
	if _, err := s.report(tk.ID, agentReport{Role: rolePlanner, Verb: "ready"}); err == nil {
		t.Fatal("the planner handed on with F1 unanswered")
	}
	say(t, s, tk.ID, rolePlanner, "verdict", "F1", "taken", "decision 4 parallelises hashing")
	say(t, s, tk.ID, rolePlanner, "ready", "plan revised")
	got = get()
	if !got.Ready || got.PlanReview.Phase != "done" || got.PlanReview.Findings[0].Verdict != "taken" {
		t.Fatalf("the reviewed plan didn't reach the reader: %+v", got)
	}

	// The reader's comments come back to the planner, but not to a second review.
	s.board.Update(tk.ID, func(x *store.Ticket) error { x.Ready = false; return nil })
	say(t, s, tk.ID, rolePlanner, "ready", "revised again")
	if got := get(); !got.Ready || got.PlanReview.Phase != "done" {
		t.Fatalf("a revision for the reader went back to review: %+v", got.PlanReview)
	}
}

func TestPlanReviewerIsAnotherFamilyAsTheListNamesIt(t *testing.T) {
	// As Claude Code lists them: the default is Opus, under no ID of its own.
	models := []agent.Model{{ID: "", Model: "claude-opus-5-5"}, {ID: "claude-fable-5-2[1m]", Model: "claude-fable-5-2"}, {ID: "sonnet", Model: "claude-sonnet-5-5"}}
	for planner, want := range map[string]string{"": "claude-fable-5-2[1m]", "sonnet": "claude-fable-5-2[1m]", "claude-fable-5-2[1m]": ""} {
		if got := otherModelIn(models, planner); got != want {
			t.Errorf("for a planner on %q the reviewer is %q, not %q", planner, got, want)
		}
	}
	if got := otherModelIn(nil, ""); got != "fable" {
		t.Errorf("before the list is known the reviewer is %q", got)
	}
}

func TestRolePickIsTheTicketsThenTheLastAndReviewersReadAnotherFamily(t *testing.T) {
	models := []agent.Model{{ID: "", Model: "claude-opus-5-5"}, {ID: "fable", Model: "claude-fable-5-2"}, {ID: "codex/gpt"}}
	str := func(s string) *string { return &s }
	last := map[string]boardPick{
		rolePlanner:  {Model: str("fable"), Effort: str("max")},
		roleReviewer: {Model: str("fable")},
	}
	tk := store.Ticket{}
	if m, e := pickFor(tk, rolePlanner, models, last); m != "fable" || e != "max" {
		t.Errorf("the planner starts on %q %q, not the last pick", m, e)
	}
	if m, e := pickFor(tk, roleBuilder, models, last); m != "" || e != "" {
		t.Errorf("a role never picked starts on %q %q, not the defaults", m, e)
	}
	if m, _ := pickFor(tk, roleReviewer, models, last); m != "fable" {
		t.Errorf("the reviewer of an Opus builder is %q, not its last pick", m)
	}
	tk.Model = map[string]string{roleBuilder: "fable"}
	if m, _ := pickFor(tk, roleReviewer, models, last); m != "" {
		t.Errorf("the reviewer of a Fable builder is %q, its own family", m)
	}
	tk.Model[roleReviewer] = "fable"
	if m, _ := pickFor(tk, roleReviewer, models, last); m != "fable" {
		t.Errorf("a reviewer picked on the ticket is %q, not as picked", m)
	}
	tk.Model[rolePlanner], tk.Effort = "codex/gpt", map[string]string{rolePlanner: ""}
	if m, e := pickFor(tk, rolePlanner, models, last); m != "codex/gpt" || e != "" {
		t.Errorf("the ticket's own pick lost to the last one: %q %q", m, e)
	}
}

func TestReaderCanHaveAnUnreviewedPlanReviewed(t *testing.T) {
	onlyGit(t)
	s := boardServer(t)
	tk, _ := s.board.Add(store.Ticket{Title: "Planned before plan review"})
	worktree := filepath.Join(s.repo.Root, ".dv", "worktrees", tk.ID)
	os.MkdirAll(worktree, 0o755)
	s.board.Update(tk.ID, func(x *store.Ticket) error {
		x.Stage, x.Worktree, x.Ready, x.NoPlanReview = "plan", worktree, true, true
		x.Sessions = map[string]string{rolePlanner: plannerID}
		return nil
	})
	s.board.SaveDoc(tk.ID, "plan", "## Decisions\n1. Do it.")
	ts := httptest.NewServer(s.Handler(""))
	defer ts.Close()
	post := func() int {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/board/"+tk.ID+"/plan-review", strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	post()
	got, _ := s.board.Get(tk.ID)
	if got.Ready || got.NoPlanReview || got.PlanReview == nil || got.PlanReview.Phase != "review" {
		t.Fatalf("asking for a review didn't start one: %+v", got)
	}
	if code := post(); code == http.StatusOK {
		t.Fatal("a second review started while the first ran")
	}

	// Asked for again once done, the first is kept beside the second.
	s.board.Update(tk.ID, func(x *store.Ticket) error {
		x.PlanReview.Phase = "done"
		x.PlanReview.Findings = []store.Finding{{ID: "F1", Text: "missing test", Verdict: "taken", Reason: "added"}}
		return nil
	})
	// Starting the reviewer fails here, with no agent to run, after the
	// review is recorded.
	post()
	got, _ = s.board.Get(tk.ID)
	if len(got.EarlierPlanReviews) != 1 || got.EarlierPlanReviews[0].Findings[0].ID != "F1" || got.PlanReview.Phase != "review" || len(got.PlanReview.Findings) != 0 {
		t.Fatalf("the first review wasn't kept beside the second: %+v %+v", got.EarlierPlanReviews, got.PlanReview)
	}
}

func TestPlanReviewCanBeSkipped(t *testing.T) {
	s := boardServer(t)
	tk, _ := s.board.Add(store.Ticket{Title: "Rename a flag"})
	s.board.Update(tk.ID, func(x *store.Ticket) error {
		x.Stage, x.NoPlanReview = "plan", true
		x.Sessions = map[string]string{rolePlanner: plannerID}
		return nil
	})
	say(t, s, tk.ID, rolePlanner, "ready", "small plan")
	if got, _ := s.board.Get(tk.ID); !got.Ready || got.PlanReview != nil {
		t.Fatalf("a ticket without plan review got one: %+v", got)
	}
}

func TestBoardEnvNamesTheSessionsTicketAndRole(t *testing.T) {
	s := boardServer(t)
	tk, _ := s.board.Add(store.Ticket{Title: "One"})
	s.board.Update(tk.ID, func(x *store.Ticket) error {
		x.Sessions = map[string]string{roleScope: builderID}
		return nil
	})
	env := s.boardEnv(builderID)
	want := map[string]bool{"DV_TICKET=" + tk.ID: true, "DV_ROLE=scope": true, "DV_ROOT=" + s.repo.Root: true}
	for _, e := range env {
		delete(want, e)
	}
	if len(want) > 0 {
		t.Fatalf("env %v is missing %v", env, want)
	}
	if s.boardEnv(plannerID) != nil {
		t.Fatal("a session of no ticket got a ticket's env")
	}
}

func TestGoToFileAndSearchLookInATicketsWorktree(t *testing.T) {
	s := boardServer(t)
	tk, _ := s.board.Add(store.Ticket{Title: "Bench uploads"})
	wt := filepath.Join(s.repo.Root, ".dv", "worktrees", tk.ID)
	for _, args := range [][]string{
		{"-C", s.repo.Root, "commit", "-q", "--allow-empty", "-m", "start"},
		{"-C", s.repo.Root, "worktree", "add", "-q", "-b", "bench", wt},
	} {
		if out, err := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	os.WriteFile(filepath.Join(wt, "bench_test.go"), []byte("package x\n\nfunc benchOnBranch() {}\n"), 0o644)
	if out, err := exec.Command("git", "-C", wt, "add", ".").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v %s", err, out)
	}

	get := func(path string) string {
		rec := httptest.NewRecorder()
		s.Handler("").ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return fmt.Sprint(rec.Code, " ", rec.Body.String())
	}
	if got := get("/api/files?q=bench"); strings.Contains(got, "bench_test.go") {
		t.Fatalf("the checkout listed the branch's file: %s", got)
	}
	if got := get("/api/files?q=bench&in=" + tk.ID); !strings.Contains(got, `"path":"bench_test.go"`) {
		t.Fatalf("the worktree's file wasn't listed as its own: %s", got)
	}
	if got := get("/api/search?q=benchOnBranch&in=" + tk.ID); !strings.Contains(got, `"file":"bench_test.go"`) {
		t.Fatalf("the worktree wasn't searched: %s", got)
	}
	// Its uncommitted work, which the checkout can't see.
	if got := get("/api/diff?scope=working&in=" + tk.ID); !strings.Contains(got, `"path":"bench_test.go"`) {
		t.Fatalf("the worktree's uncommitted file wasn't listed: %s", got)
	}
	if got := get("/api/diff?scope=working"); strings.Contains(got, "bench_test.go") {
		t.Fatalf("the checkout listed the worktree's work: %s", got)
	}
	if got := get("/api/files?q=x&in=NOPE-1"); !strings.HasPrefix(got, "404") {
		t.Fatalf("an unknown ticket: %s", got)
	}
}

func TestAFollowUpWaitsInTheInboxUntilItsOriginMerges(t *testing.T) {
	s := boardServer(t)
	origin, _ := s.board.Add(store.Ticket{Title: "Speed up uploads"})
	s.board.Update(origin.ID, func(x *store.Ticket) error { x.Stage, x.Branch = "build", "speed-up-uploads"; return nil })
	say(t, s, origin.ID, roleBuilder, "file", "Bound upload memory")
	var follow store.Ticket
	for _, x := range s.board.List() {
		if x.From == origin.ID {
			follow = x
		}
	}
	blocked := func() []string {
		for _, v := range s.ticketViews() {
			if v.ID == follow.ID {
				return v.Blocked
			}
		}
		return nil
	}
	if got := blocked(); len(got) != 1 || got[0] != origin.ID {
		t.Fatalf("the follow-up doesn't wait for %s: %v", origin.ID, got)
	}
	if err := s.checkWaits(follow.ID, "scope", false); err == nil || !strings.Contains(err.Error(), "isn't merged") {
		t.Fatalf("it left the Inbox ahead of its origin: %v", err)
	}
	if err := s.checkWaits(follow.ID, "scope", true); err != nil {
		t.Fatalf("the reader couldn't start it anyway: %v", err)
	}
	if !strings.Contains(s.originNote(follow), "isn't merged") {
		t.Fatal("an early start isn't told its worktree lacks the origin's changes")
	}

	s.board.Update(origin.ID, func(x *store.Ticket) error { x.Archived = true; return nil })
	if got := blocked(); len(got) != 0 {
		t.Fatalf("an archived origin still holds it: %v", got)
	}
	s.board.Update(origin.ID, func(x *store.Ticket) error { x.Archived, x.Stage = false, "merged"; return nil })
	if got := blocked(); len(got) != 0 || s.checkWaits(follow.ID, "scope", false) != nil {
		t.Fatalf("a merged origin still holds it: %v", got)
	}
}

func TestAPartTakesItsParentsLabels(t *testing.T) {
	s := boardServer(t)
	parent, _ := s.board.Add(store.Ticket{Title: "Long-running services", Labels: []string{"Service", "CLI"}, Stage: "scope"})
	if _, err := s.report(parent.ID, agentReport{Role: roleScope, Verb: "split", Args: []string{"Services in the API"}, Body: "The API side."}); err != nil {
		t.Fatal(err)
	}
	for _, x := range s.board.List() {
		if x.Parent == parent.ID {
			if !slices.Equal(x.Labels, []string{"Service", "CLI"}) {
				t.Fatalf("the part's labels: %q", x.Labels)
			}
			return
		}
	}
	t.Fatal("no part was made")
}

func TestSessionsInARepositoryAreToldToFileSmallTickets(t *testing.T) {
	if got := AgentInstructions(nil, true)(); !strings.Contains(got, "dv ticket file") || !strings.Contains(got, "Keep it small") {
		t.Fatalf("a repository's sessions are told %q", got)
	}
	if got := AgentInstructions(nil, false)(); strings.Contains(got, "dv ticket") {
		t.Fatalf("a plain folder's sessions are told of the board: %q", got)
	}
}

// TestApprovingCatchesUpWithAMovedTrunk: a branch approved after its trunk
// moved on is caught up before it merges. A clean merge dv makes, and a
// reviewer checks what came in; with nothing found the approval stands.
// Conflicts go to the builder, and the reader approves again.
// trunkRepo makes the board's repository one with an origin whose main
// holds shared.txt. land commits a change to main as someone else would;
// ticketOn cuts a ticket's branch from it, with a commit when file is given.
type trunkRepo struct {
	t      *testing.T
	s      *Server
	other  string
	cloned bool
}

func newTrunkRepo(t *testing.T) *trunkRepo {
	onlyGit(t)
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t"} {
		t.Setenv(k, v)
	}
	r := &trunkRepo{t: t, s: boardServer(t), other: t.TempDir()}
	root, origin := r.s.repo.Root, t.TempDir()
	r.git(origin, "init", "-q", "--bare", "-b", "main")
	r.write(filepath.Join(root, "shared.txt"), "one\ntwo\nthree\n")
	r.git(root, "add", ".")
	r.git(root, "commit", "-q", "-m", "start")
	r.git(root, "remote", "add", "origin", origin)
	r.git(root, "push", "-q", "origin", "main")
	r.git(root, "fetch", "-q", "origin")
	r.git(root, "remote", "set-head", "origin", "main")
	r.git(r.other, "clone", "-q", origin, ".")
	return r
}

func (r *trunkRepo) git(dir string, args ...string) string {
	r.t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *trunkRepo) write(path, text string) {
	r.t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *trunkRepo) land(file, text, msg string) {
	r.t.Helper()
	r.write(filepath.Join(r.other, file), text)
	r.git(r.other, "add", ".")
	r.git(r.other, "commit", "-q", "-m", msg)
	r.git(r.other, "push", "-q", "origin", "main")
	// Seen at once, not after the fetch's quiet spell.
	r.git(r.s.repo.Root, "fetch", "-q", "origin")
}

func (r *trunkRepo) ticketOn(name, stage, file, text string) store.Ticket {
	r.t.Helper()
	root := r.s.repo.Root
	tk, _ := r.s.board.Add(store.Ticket{Title: name})
	wt := filepath.Join(root, ".dv", "worktrees", tk.ID)
	r.git(root, "worktree", "add", "-q", "-b", name, wt, "origin/main")
	base := r.git(wt, "rev-parse", "HEAD")
	if file != "" {
		r.write(filepath.Join(wt, file), text)
		r.git(wt, "add", ".")
		r.git(wt, "commit", "-q", "-m", name)
	}
	tk, _ = r.s.board.Update(tk.ID, func(x *store.Ticket) error {
		x.Stage, x.Worktree, x.Branch, x.Base = stage, wt, name, base
		x.Sessions = map[string]string{roleBuilder: builderID}
		return nil
	})
	return tk
}

func TestApprovingCatchesUpWithAMovedTrunk(t *testing.T) {
	r := newTrunkRepo(t)
	s := r.s
	clean := r.ticketOn("feature", "review", "feature.txt", "new\n")
	clashing := r.ticketOn("clash", "review", "shared.txt", "one\nTWO here\nthree\n")
	r.land("shared.txt", "one\ntwo, changed on main\nthree\n", "main moves on")

	// The error is only the reviewer not starting, with no claude here.
	s.moveTicket(clean.ID, "merged", "")
	got, _ := s.board.Get(clean.ID)
	if got.Stage != "build" || got.Merging || got.CatchUp == nil || got.CatchUp.Conflicts || !got.CatchUp.Approved {
		t.Fatalf("approved behind the trunk: %+v", got)
	}
	if r := lastRound(got); r == nil || r.Kind != "main" {
		t.Fatalf("no check of what the trunk brought in: %+v", got.Rounds)
	}
	if b, _ := os.ReadFile(filepath.Join(clean.Worktree, "shared.txt")); !strings.Contains(string(b), "changed on main") {
		t.Fatalf("the trunk wasn't merged in: %q", b)
	}

	// The check finds nothing, and the reviewer updates the walkthrough.
	s.board.SaveDoc(clean.ID, "guide", `{"steps":[{"part":"users","title":"x","file":"feature.txt","start":1,"end":1,"say":"x"}]}`)
	s.board.Update(clean.ID, func(x *store.Ticket) error { x.Rounds[len(x.Rounds)-1].Phase = "guide"; return nil })
	got, _ = s.board.Get(clean.ID)
	if _, err := s.saveGuide(got, `{"steps":[{"part":"users","title":"x","file":"feature.txt","start":1,"end":1,"say":"x"}]}`); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.board.Get(clean.ID); got.Stage != "review" || !got.Merging || got.CatchUp != nil {
		t.Fatalf("caught up cleanly, the approval didn't stand: %+v", got)
	}

	// The error is only the absent builder not being told.
	s.moveTicket(clashing.ID, "merged", "")
	got, _ = s.board.Get(clashing.ID)
	if got.Stage != "build" || got.CatchUp == nil || !got.CatchUp.Conflicts || len(got.Rounds) != 0 {
		t.Fatalf("conflicting with the trunk: %+v", got)
	}
	if out, _ := exec.Command("git", "-C", clashing.Worktree, "status", "--porcelain").CombinedOutput(); len(strings.TrimSpace(string(out))) != 0 {
		t.Fatalf("the worktree was left mid-merge: %s", out)
	}
}

// TestEachStageStartsOnTheLatestTrunk: an agent starting on a ticket has its
// worktree brought up to the trunk and is told what landed since the code was
// last read; a part hears of what landed since its parent was scoped.
func TestEachStageStartsOnTheLatestTrunk(t *testing.T) {
	r := newTrunkRepo(t)
	s := r.s

	planned := r.ticketOn("plan-me", "plan", "", "")
	if _, news := s.startNews(planned, rolePlanner); news != "" {
		t.Fatalf("told of a trunk that didn't move: %q", news)
	}
	r.land("migrations/007.sql", "create table a;\n", "Add migration 007")
	got, news := s.startNews(planned, rolePlanner)
	if !strings.Contains(news, "Add migration 007") || !strings.Contains(news, "dv ticket ask") {
		t.Fatalf("the planner is told %q", news)
	}
	if _, err := os.Stat(filepath.Join(planned.Worktree, "migrations", "007.sql")); err != nil {
		t.Fatal("the worktree wasn't brought up to the trunk")
	}
	if _, again := s.startNews(got, roleBuilder); again != "" {
		t.Fatalf("the builder after it is told again: %q", again)
	}

	parent := r.ticketOn("parent", "scope", "", "")
	if _, err := s.report(parent.ID, agentReport{Role: roleScope, Verb: "split", Args: []string{"Second part"}, Body: "x"}); err != nil {
		t.Fatal(err)
	}
	var part store.Ticket
	for _, x := range s.board.List() {
		if x.Parent == parent.ID {
			part = x
		}
	}
	r.land("first.txt", "first part\n", "First part lands")
	part, err := s.ensureWorktree(part)
	if err != nil {
		t.Fatal(err)
	}
	if _, news := s.startNews(part, rolePlanner); !strings.Contains(news, "First part lands") {
		t.Fatalf("the part's planner is told %q", news)
	}
}

// TestAReviewRoundStartsOnTheLatestTrunk: the trunk is merged into a branch
// before each round of review; conflicts go to the builder, and no round
// starts until it has resolved them.
func TestAReviewRoundStartsOnTheLatestTrunk(t *testing.T) {
	r := newTrunkRepo(t)
	s := r.s
	clean := r.ticketOn("feature", "build", "feature.txt", "new\n")
	clashing := r.ticketOn("clash", "build", "shared.txt", "one\nTWO here\nthree\n")
	r.land("shared.txt", "one\ntwo, changed on main\nthree\n", "main moves on")

	// The error is only the reviewer not starting, with no claude here.
	got, conflicts, _ := s.beginRound(clean)
	if conflicts || len(got.Rounds) != 1 {
		t.Fatalf("a clean merge before review: conflicts %v, rounds %+v", conflicts, got.Rounds)
	}
	if b, _ := os.ReadFile(filepath.Join(clean.Worktree, "shared.txt")); !strings.Contains(string(b), "changed on main") {
		t.Fatalf("the trunk wasn't merged in: %q", b)
	}

	s.board.SaveDoc(clashing.ID, "pr", "Clash\n\nx")
	reply, err := s.ready(clashing, roleBuilder, "done")
	if err != nil || !strings.Contains(reply, "git merge origin/main") {
		t.Fatalf("the builder is told %q, %v", reply, err)
	}
	got, _ = s.board.Get(clashing.ID)
	if got.Stage != "build" || len(got.Rounds) != 0 {
		t.Fatalf("conflicting before review: %+v", got)
	}
	if out := r.git(clashing.Worktree, "status", "--porcelain"); out != "" {
		t.Fatalf("the worktree was left mid-merge: %s", out)
	}

	// Resolved, the round's reviewer hears what landed meanwhile.
	r.git(clashing.Worktree, "merge", "-q", "-s", "ours", "origin/main")
	_, news, conflicts, err := s.refresh(got)
	if err != nil || conflicts || !strings.Contains(news, "main moves on") {
		t.Fatalf("after resolving, the round is told %q (%v, %v)", news, conflicts, err)
	}
}

// TestAnsweringTheReaderWithoutNewCodeNeedsNoReviewer: sent back with only
// questions, a builder that commits nothing hands straight back to the reader;
// new commits get a reviewer. Skipping a round under way ends it.
func TestAnsweringTheReaderWithoutNewCodeNeedsNoReviewer(t *testing.T) {
	r := newTrunkRepo(t)
	s := r.s
	tk := r.ticketOn("feature", "review", "feature.txt", "new\n")
	s.board.SaveDoc(tk.ID, "pr", "Feature\n\nx")

	// The errors are only the absent builder not being told.
	s.moveTicket(tk.ID, "build", "why a txt file?")
	tk, _ = s.board.Get(tk.ID)
	if _, err := s.ready(tk, roleBuilder, "answered"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.board.Get(tk.ID); got.Stage != "review" || len(got.Rounds) != 0 {
		t.Fatalf("answered with no new code: %+v", got)
	}

	s.moveTicket(tk.ID, "build", "rename it")
	r.git(tk.Worktree, "mv", "feature.txt", "feature.md")
	r.git(tk.Worktree, "commit", "-q", "-m", "rename")
	tk, _ = s.board.Get(tk.ID)
	s.ready(tk, roleBuilder, "renamed") // the reviewer doesn't start, with no claude here
	got, _ := s.board.Get(tk.ID)
	if got.Stage != "build" || len(got.Rounds) != 1 {
		t.Fatalf("new code went unreviewed: %+v", got)
	}

	s.moveTicket(tk.ID, "review", "")
	if got, _ = s.board.Get(tk.ID); got.Stage != "review" || got.Rounds[0].Phase != "done" {
		t.Fatalf("skipped to review mid-round: %+v", got.Rounds)
	}

	// Talked over with the reader in their review: nothing new stays there,
	// a commit goes to a reviewer and voids an approval given before it.
	if _, err := s.ready(got, roleBuilder, "talked"); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.board.Get(tk.ID); got.Stage != "review" || len(got.Rounds) != 1 {
		t.Fatalf("nothing new, from their review: %+v", got)
	}
	s.board.Update(tk.ID, func(x *store.Ticket) error { x.Merging = true; return nil })
	r.write(filepath.Join(tk.Worktree, "feature.md"), "changed\n")
	r.git(tk.Worktree, "commit", "-q", "-am", "as discussed")
	got, _ = s.board.Get(tk.ID)
	s.ready(got, roleBuilder, "changed as discussed")
	if got, _ = s.board.Get(tk.ID); got.Stage != "build" || got.Merging || len(got.Rounds) != 2 || got.ReviewedAt != got.HandedAt {
		t.Fatalf("changed in their review: %+v", got)
	}
}

// TestNoChecksJustAfterAPushIsNotNoCI: GitHub lists a push's checks a while
// after it, so a pull request with none listed then waits; longer for a branch
// with workflows of its own.
func TestNoChecksJustAfterAPushIsNotNoCI(t *testing.T) {
	plain, withCI := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(withCI, ".github", "workflows"), 0o755)
	ago := func(d time.Duration) time.Time { return time.Now().Add(-d) }
	for _, c := range []struct {
		wt   string
		at   time.Time
		want bool
	}{
		{plain, time.Time{}, false},
		{plain, ago(10 * time.Second), true},
		{plain, ago(3 * time.Minute), false},
		{withCI, ago(3 * time.Minute), true},
		{withCI, ago(11 * time.Minute), false},
	} {
		if got := checksDue(store.Ticket{Worktree: c.wt, PushedAt: c.at}); got != c.want {
			t.Errorf("pushed %v ago, workflows %v: due %v", time.Since(c.at).Round(time.Second), c.wt == withCI, got)
		}
	}
}

// TestATicketWithNothingToDoClosesOnTheReadersWord: an agent finding the work
// already done says so; the reader closes it as merged, which releases what
// waits for it, or tells the agent what's left.
func TestATicketWithNothingToDoClosesOnTheReadersWord(t *testing.T) {
	s := boardServer(t)
	tk, _ := s.board.Add(store.Ticket{Title: "New secret opens as Edit"})
	tk, _ = s.board.Update(tk.ID, func(x *store.Ticket) error {
		x.Stage = "scope"
		x.Sessions = map[string]string{roleScope: plannerID}
		return nil
	})
	waiting, _ := s.board.Add(store.Ticket{Title: "After it", After: []string{tk.ID}})

	if _, err := s.report(tk.ID, agentReport{Role: rolePlanner, Verb: "nothing", Args: []string{"done"}}); err == nil {
		t.Fatal("a planner said nothing to do of a ticket still in Scope")
	}
	if _, err := s.report(tk.ID, agentReport{Role: roleScope, Verb: "nothing"}); err == nil {
		t.Fatal("nothing to do, with no reason")
	}
	if _, err := s.report(tk.ID, agentReport{Role: roleScope, Verb: "nothing", Args: []string{"#133 fixed both dialogs; its test passes"}}); err != nil {
		t.Fatal(err)
	}
	decide := func(body string) int {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		r.SetPathValue("id", tk.ID)
		w := httptest.NewRecorder()
		s.handleNothing(w, r)
		return w.Code
	}

	// Telling the agent fails here, with no session running; the finding is cleared all the same.
	decide(`{"close":false,"note":"the Secrets page still does it"}`)
	if got, _ := s.board.Get(tk.ID); got.Nothing != nil || got.Stage != "scope" {
		t.Fatalf("disputed: %+v", got)
	}

	s.report(tk.ID, agentReport{Role: roleScope, Verb: "nothing", Args: []string{"#133 fixed both dialogs"}})
	if code := decide(`{"close":true}`); code != http.StatusOK {
		t.Fatalf("closing: %d", code)
	}
	got, _ := s.board.Get(tk.ID)
	if got.Stage != "merged" || got.Nothing == nil || got.Nothing.Why != "#133 fixed both dialogs" {
		t.Fatalf("closed: %+v", got)
	}
	for _, v := range s.ticketViews() {
		if v.ID == waiting.ID && len(v.Blocked) != 0 {
			t.Fatalf("what waits for it is still held: %v", v.Blocked)
		}
	}
}

// TestTheReadersCommentsEachGetAnAnswer: a comment sent on a document waits on
// its author, who answers it by ID before handing on; one on the code is
// answered in its review thread. The reader resolves, or replies again.
func TestTheReadersCommentsEachGetAnAnswer(t *testing.T) {
	s := boardServer(t)
	tk, _ := s.board.Add(store.Ticket{Title: "Speed up uploads"})
	tk, _ = s.board.Update(tk.ID, func(x *store.Ticket) error {
		x.Stage = "plan"
		x.Sessions = map[string]string{rolePlanner: plannerID, roleBuilder: builderID}
		x.Comments = []store.DocComment{{ID: "C1", Doc: "plan", Quote: "use a pool", Thread: []store.CommentPost{{By: "you", Text: "why not a queue?"}}}}
		return nil
	})

	if _, err := s.ready(tk, rolePlanner, ""); err == nil || !strings.Contains(err.Error(), "C1") {
		t.Fatalf("handed on with a comment unanswered: %v", err)
	}
	if _, err := s.replyToComment(tk, roleBuilder, []string{"C1", "answered", "x"}); err == nil {
		t.Fatal("the builder answered a comment on the plan")
	}
	if _, err := s.replyToComment(tk, rolePlanner, []string{"C1", "answered", "A queue reorders; the plan says why."}); err != nil {
		t.Fatal(err)
	}
	tk, _ = s.board.Get(tk.ID)
	if c := tk.Comments[0]; c.Waiting() || len(c.Thread) != 2 || c.Thread[1].Kind != "answered" {
		t.Fatalf("the answer: %+v", c)
	}
	if s.unanswered(tk, rolePlanner) != "" {
		t.Fatal("still waiting once answered")
	}
	if !strings.Contains(replyRule("plan"), "write that into the plan") {
		t.Fatal("an answer that decides something isn't sent into the plan")
	}

	// The reader resolves it, then takes it up again.
	post := func(path, body string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Host = "127.0.0.1" // as dv's guard has it
		s.Handler("").ServeHTTP(rec, req)
		return rec.Code
	}
	if code := post("/api/board/"+tk.ID+"/comments/C1", `{"resolved":true}`); code != 200 {
		t.Fatalf("resolving: %d", code)
	}
	if tk, _ = s.board.Get(tk.ID); !tk.Comments[0].Resolved {
		t.Fatal("not resolved")
	}
	// Past Plan, the planner can't act on a reply.
	s.board.Update(tk.ID, func(x *store.Ticket) error { x.Stage = "build"; return nil })
	if code := post("/api/board/"+tk.ID+"/comments/C1", `{"text":"and memory?"}`); code == 200 {
		t.Fatal("a reply went to the planner after Plan")
	}

	// A comment on the code is answered in its thread.
	th, err := s.store.AddThread(&store.Thread{File: "a.go", Side: "new", StartLine: 3, EndLine: 3}, "this leaks", "you")
	if err != nil {
		t.Fatal(err)
	}
	tk, _ = s.board.Update(tk.ID, func(x *store.Ticket) error { x.CodeThreads = []store.SentThread{{ID: th.ID, Sent: 1}}; return nil })
	if got := s.unanswered(tk, roleBuilder); got != th.ID {
		t.Fatalf("the code comment isn't waiting on the builder: %q", got)
	}
	if _, err := s.replyToComment(tk, roleBuilder, []string{th.ID, "changed", "Closed it in the defer."}); err != nil {
		t.Fatal(err)
	}
	for _, x := range s.store.Threads() {
		if x.ID == th.ID && (len(x.Comments) != 2 || x.Comments[1].Author != roleBuilder || x.Resolved) {
			t.Fatalf("the builder's answer in the thread: %+v", x)
		}
	}
	if s.unanswered(tk, roleBuilder) != "" {
		t.Fatal("still waiting on the builder once answered")
	}
}

func TestATicketWaitsForOthersButNeverInALoop(t *testing.T) {
	s := boardServer(t)
	a, _ := s.board.Add(store.Ticket{Title: "Schema"})
	b, _ := s.board.Add(store.Ticket{Title: "API"})
	c, _ := s.board.Add(store.Ticket{Title: "UI"})
	patch := func(id, body string) (int, string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("PATCH", "/api/board/"+id, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Host = "127.0.0.1"
		s.Handler("").ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	if code, out := patch(c.ID, `{"after":["`+b.ID+`","`+a.ID+`"]}`); code != 200 {
		t.Fatalf("waiting for two: %d %s", code, out)
	}
	if got, _ := s.board.Get(c.ID); len(got.After) != 2 {
		t.Fatalf("waits for %v", got.After)
	}
	patch(b.ID, `{"after":["`+a.ID+`"]}`)
	// A waiting for C would loop: C waits for B, which waits for A.
	if code, out := patch(a.ID, `{"after":["`+c.ID+`"]}`); code != 400 || !strings.Contains(out, a.ID+" can't wait for "+c.ID) {
		t.Fatalf("a loop: %d %s", code, out)
	}
	if code, _ := patch(a.ID, `{"after":["`+a.ID+`"]}`); code == 200 {
		t.Fatal("a ticket waited for itself")
	}
	if code, _ := patch(c.ID, `{"after":["`+b.ID+`"]}`); code != 200 {
		t.Fatal("couldn't stop waiting for one")
	}
	if got, _ := s.board.Get(c.ID); len(got.After) != 1 || got.After[0] != b.ID {
		t.Fatalf("after taking one off: %v", got.After)
	}
}
