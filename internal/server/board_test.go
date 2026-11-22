package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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
		x.Stage, x.Worktree, x.Branch = "agent-review", t.TempDir(), "b"
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
	if got.Stage != "review" || round().Phase != "done" {
		t.Fatalf("a round with every finding holding didn't reach the reader: %s %+v", got.Stage, round())
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
