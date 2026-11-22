package store

import (
	"os"
	"path/filepath"
	"testing"
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
