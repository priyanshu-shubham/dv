package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBoardIsSharedAndNumbersTickets(t *testing.T) {
	common := filepath.Join(t.TempDir(), ".git")
	a, err := OpenBoard(t.TempDir(), common, "my-repo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(common, "dv")); !os.IsNotExist(err) {
		t.Fatal("opening the board made its folder")
	}
	first, err := a.Add(Ticket{Title: " Abort pulls over budget ", Request: "stop them early"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != "MYREPO-1" || first.Stage != "inbox" || first.Started != "inbox" || first.Title != "Abort pulls over budget" {
		t.Fatalf("added %+v", first)
	}
	// A worktree's dv, named after its own folder, keeps the board's prefix.
	b, _ := OpenBoard(t.TempDir(), common, "my-repo-wt")
	second, err := b.Add(Ticket{Title: "Second", Stage: "plan"})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != "MYREPO-2" || second.Started != "plan" {
		t.Fatalf("b added %+v", second)
	}
	if got := a.List(); len(got) != 2 {
		t.Fatalf("a lists %d tickets", len(got))
	}
}

// TestTheBoardStampsAStagesStart: entering a stage, however it happens, restarts
// its clock; other changes don't. Leaving the Inbox the first time is when the
// work began, kept through later moves.
func TestTheBoardStampsAStagesStart(t *testing.T) {
	b, _ := OpenBoard(t.TempDir(), "", "r")
	tk, _ := b.Add(Ticket{Title: "One"})
	if tk.StageAt.IsZero() || !tk.StartedAt.IsZero() {
		t.Fatalf("filed: %v, %v", tk.StageAt, tk.StartedAt)
	}
	back := func(x *Ticket) {
		x.StageAt = x.StageAt.Add(-time.Hour)
		if !x.StartedAt.IsZero() {
			x.StartedAt = x.StartedAt.Add(-time.Hour)
		}
	}
	b.Update(tk.ID, func(x *Ticket) error { back(x); x.Stage = "scope"; return nil })
	scoped, _ := b.Get(tk.ID)
	if time.Since(scoped.StageAt) > time.Minute || scoped.StartedAt != scoped.StageAt {
		t.Fatalf("into Scope: %v, %v", scoped.StageAt, scoped.StartedAt)
	}
	b.Update(tk.ID, func(x *Ticket) error { back(x); x.Status = "working"; return nil })
	same, _ := b.Get(tk.ID)
	if time.Since(same.StageAt) < 59*time.Minute {
		t.Fatal("a change within the stage restarted its clock")
	}
	b.Update(tk.ID, func(x *Ticket) error { x.Stage = "plan"; return nil })
	planned, _ := b.Get(tk.ID)
	if time.Since(planned.StageAt) > time.Minute || time.Since(planned.StartedAt) < 59*time.Minute {
		t.Fatalf("into Plan: %v, began %v", planned.StageAt, planned.StartedAt)
	}
}

func TestBoardUpdateKeepsTicketOnError(t *testing.T) {
	b, _ := OpenBoard(t.TempDir(), "", "r")
	tk, _ := b.Add(Ticket{Title: "One"})
	if _, err := b.Update(tk.ID, func(t *Ticket) error { t.Stage = "nowhere"; return nil }); err == nil {
		t.Fatal("moved to a stage that doesn't exist")
	}
	if _, err := b.Update(tk.ID, func(t *Ticket) error {
		t.Sessions = map[string]string{"scope": "s1"}
		return TicketError("no")
	}); err == nil {
		t.Fatal("the change's error was lost")
	}
	got, _ := b.Get(tk.ID)
	if got.Stage != "inbox" || got.Sessions != nil {
		t.Fatalf("a failed update left %+v", got)
	}
	// What Get hands out is a copy.
	got.Sessions = map[string]string{"x": "y"}
	if again, _ := b.Get(tk.ID); again.Sessions != nil {
		t.Fatal("changing a copy changed the board")
	}
}

func TestSaveDocKeepsWhatItReplaces(t *testing.T) {
	b, _ := OpenBoard(t.TempDir(), "", "r")
	tk, _ := b.Add(Ticket{Title: "One"})
	for _, text := range []string{"first", "first", "second\n", "third"} {
		if err := b.SaveDoc(tk.ID, "plan", text); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := b.Doc(tk.ID, "plan"); got != "third\n" {
		t.Fatalf("the plan is %q", got)
	}
	var old []string
	for _, v := range b.DocVersions(tk.ID, "plan") {
		old = append(old, v.Text)
	}
	// Saving the same text again keeps no version of it.
	if len(old) != 2 || old[0] != "first\n" || old[1] != "second\n" {
		t.Fatalf("versions %q", old)
	}
	if len(b.DocVersions(tk.ID, "ticket")) != 0 {
		t.Fatal("the ticket has versions it never had")
	}
}

func TestTicketInTheOldAgentReviewStageLoadsInBuild(t *testing.T) {
	common := filepath.Join(t.TempDir(), ".git")
	b, err := OpenBoard(t.TempDir(), common, "repo")
	if err != nil {
		t.Fatal(err)
	}
	tk, _ := b.Add(Ticket{Title: "Under review", Stage: "build"})
	path := filepath.Join(b.Dir(), boardName)
	data, _ := os.ReadFile(path)
	old := strings.Replace(string(data), `"stage": "build"`, `"stage": "agent-review"`, 1)
	if old == string(data) {
		t.Fatalf("no stage to change in %s", data)
	}
	os.WriteFile(path, []byte(old), 0o644)

	again, err := OpenBoard(t.TempDir(), common, "repo")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := again.Get(tk.ID); err != nil || got.Stage != "build" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestBoardDeleteLetsGoOfIt(t *testing.T) {
	b, _ := OpenBoard(t.TempDir(), "", "r")
	parent, _ := b.Add(Ticket{Title: "Parent"})
	child, err := b.Add(Ticket{Title: "Child", Parent: parent.ID, After: []string{parent.ID}})
	if err != nil {
		t.Fatal(err)
	}
	doc := b.DocPath(parent.ID, "plan")
	os.MkdirAll(filepath.Dir(doc), 0o755)
	os.WriteFile(doc, []byte("plan"), 0o644)
	if err := b.Delete(parent.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := b.Get(child.ID)
	if got.Parent != "" || len(got.After) != 0 {
		t.Fatalf("the child still points at its parent: %+v", got)
	}
	if text, _ := b.Doc(parent.ID, "plan"); text != "" {
		t.Fatal("the parent's documents stayed")
	}
	if _, err := b.Add(Ticket{Title: "Waits on nothing", After: []string{"R-99"}}); err == nil {
		t.Fatal("waited on a ticket that doesn't exist")
	}
}

func TestATicketTakesOnlyTheKnownPriorities(t *testing.T) {
	b, _ := OpenBoard(t.TempDir(), "", "r")
	tk, err := b.Add(Ticket{Title: "Fix the login", Priority: "urgent"})
	if err != nil || tk.Priority != "urgent" {
		t.Fatalf("added at urgent: %+v, %v", tk, err)
	}
	if _, err := b.Add(Ticket{Title: "x", Priority: "p0"}); err == nil {
		t.Fatal("took an unknown priority")
	}
	if _, err := b.Update(tk.ID, func(x *Ticket) error { x.Priority = "someday"; return nil }); err == nil {
		t.Fatal("changed to an unknown priority")
	}
	if got, _ := b.Update(tk.ID, func(x *Ticket) error { x.Priority = ""; return nil }); got.Priority != "" {
		t.Fatalf("back to normal: %q", got.Priority)
	}
}
