package store

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Two worktrees' dvs open the same notes, from their common dir.
func TestNotesAreSharedThroughTheCommonDir(t *testing.T) {
	common := filepath.Join(t.TempDir(), ".git")
	a, err := OpenNotes(t.TempDir(), common)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := OpenNotes(t.TempDir(), common)
	if _, err := os.Stat(filepath.Join(common, "dv")); !os.IsNotExist(err) {
		t.Fatal("opening the notes made their folder")
	}
	added, err := a.Add(Note{Title: "  Retry drops a message \n", Priority: "high", Labels: []string{"relay"}}, "Claude")
	if err != nil {
		t.Fatal(err)
	}
	got := b.List()
	if len(got) != 1 || got[0].Title != "Retry drops a message" || got[0].Status != "open" || got[0].Author != "Claude" {
		t.Fatalf("b lists %+v", got)
	}
	done := "done"
	if _, err := b.Update(added.ID, NotePatch{Status: &done}); err != nil {
		t.Fatal(err)
	}
	if got := a.List(); got[0].Status != "done" || got[0].Priority != "high" {
		t.Fatalf("a lists %+v after b's update", got)
	}
	if err := a.Delete(added.ID); err != nil {
		t.Fatal(err)
	}
	if got := b.List(); len(got) != 0 {
		t.Fatalf("b still lists %+v", got)
	}
}

func TestNotesOutsideGitAreInDv(t *testing.T) {
	root := t.TempDir()
	n, _ := OpenNotes(root, "")
	if want := filepath.Join(root, dirName, notesName); n.Path() != want {
		t.Fatalf("path %s, want %s", n.Path(), want)
	}
}

func TestNoteLabelsKeepOneSpelling(t *testing.T) {
	n, _ := OpenNotes(t.TempDir(), "")
	n.Add(Note{Title: "one", Labels: []string{"Relay"}}, "you")
	got, err := n.Add(Note{Title: "two", Labels: []string{"relay", " flaky  test ", "", "RELAY", "flaky test"}}, "you")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Relay", "flaky test"}; !slices.Equal(got.Labels, want) {
		t.Fatalf("labels %q, want %q", got.Labels, want)
	}
}

func TestNotesRefuseWhatTheyCannotShow(t *testing.T) {
	n, _ := OpenNotes(t.TempDir(), "")
	for _, bad := range []Note{{Title: " "}, {Title: "x", Priority: "urgent"}, {Title: "x", Status: "closed"}} {
		if _, err := n.Add(bad, "you"); err == nil {
			t.Errorf("added %+v", bad)
		}
	}
	ok, _ := n.Add(Note{Title: "x"}, "you")
	empty := ""
	if _, err := n.Update(ok.ID, NotePatch{Title: &empty}); err == nil {
		t.Error("a note's title was emptied")
	}
	if _, err := n.Update("n_nope", NotePatch{}); err == nil {
		t.Error("updated a note that is not there")
	}
}
