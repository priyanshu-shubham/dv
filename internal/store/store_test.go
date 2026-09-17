package store

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func thread(file string) *Thread { return &Thread{File: file, Side: "new", StartLine: 1, EndLine: 1} }

func TestOpeningCreatesNothing(t *testing.T) {
	root := t.TempDir()
	if _, err := Open(root, "r"); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenViewed(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, dirName)); !os.IsNotExist(err) {
		t.Fatalf("opening made %s", dirName)
	}
}

// Two stores on one file stand in for a running dv and `dv reset`, or an agent
// editing the file: neither may save over what the other wrote.
func TestAnotherWriterIsLoadedNotSavedOver(t *testing.T) {
	root := t.TempDir()
	a, _ := Open(root, "r")
	b, _ := Open(root, "r")

	if _, err := a.AddThread(thread("one.go"), "first", "me"); err != nil {
		t.Fatal(err)
	}
	before := a.Version()
	if got := b.Threads(); len(got) != 1 {
		t.Fatalf("b sees %d threads, want the one a wrote", len(got))
	}
	if n, err := b.Reset(); err != nil || n != 1 {
		t.Fatalf("reset: %d, %v", n, err)
	}
	if a.Version() == before {
		t.Fatal("version did not move when the file was reset")
	}
	if got := a.Threads(); len(got) != 0 {
		t.Fatalf("a still has %d threads after the reset", len(got))
	}
	if _, err := a.AddThread(thread("two.go"), "second", "me"); err != nil {
		t.Fatal(err)
	}
	got := b.Threads()
	if len(got) != 1 || got[0].File != "two.go" {
		t.Fatalf("after the reset and one new thread, b has %+v", got)
	}
}

func TestViewedMarks(t *testing.T) {
	root := t.TempDir()
	v, _ := OpenViewed(root)
	if err := v.Mark("Uncommitted", []string{"b.go", "a.go", "b.go"}, true); err != nil {
		t.Fatal(err)
	}
	if err := v.Mark("main...HEAD", []string{"a.go"}, true); err != nil {
		t.Fatal(err)
	}
	if got := v.Paths("Uncommitted"); !slices.Equal(got, []string{"a.go", "b.go"}) {
		t.Fatalf("Uncommitted: %v", got)
	}
	if got := v.Count(); got != 3 {
		t.Fatalf("count %d, want 3", got)
	}

	version := v.Version()
	if err := v.Mark("Uncommitted", []string{"a.go"}, true); err != nil {
		t.Fatal(err)
	}
	if v.Version() != version {
		t.Fatal("marking an already viewed file rewrote the file")
	}

	other, _ := OpenViewed(root)
	if err := other.Mark("Uncommitted", []string{"a.go"}, false); err != nil {
		t.Fatal(err)
	}
	if got := v.Paths("Uncommitted"); !slices.Equal(got, []string{"b.go"}) {
		t.Fatalf("after another writer unmarked a.go: %v", got)
	}

	if n, err := v.Reset(); err != nil || n != 2 {
		t.Fatalf("reset: %d, %v", n, err)
	}
	if _, err := os.Stat(filepath.Join(root, dirName, viewedName)); !os.IsNotExist(err) {
		t.Fatal("reset left the file behind")
	}
	if got := other.Paths("main...HEAD"); len(got) != 0 {
		t.Fatalf("other still has %v after the reset", got)
	}
}

func TestSessionsOpenMostRecentFirstAndSurviveReopening(t *testing.T) {
	root := t.TempDir()
	s, _ := OpenSessions(root)
	for _, id := range []string{"a", "b"} {
		if changed, err := s.Set(id, true); err != nil || !changed {
			t.Fatalf("open %s: %v %v", id, changed, err)
		}
	}
	if changed, _ := s.Set("a", true); changed {
		t.Fatal("opening an open session changed the set")
	}
	again, _ := OpenSessions(root)
	if got := again.IDs(); !slices.Equal(got, []string{"b", "a"}) {
		t.Fatalf("reopened set %v", got)
	}
	again.Set("b", false)
	if s.Has("b") || !s.Has("a") {
		t.Fatalf("after closing b elsewhere: %v", s.IDs())
	}
}

func TestFindServerInAFolderAndNotPastARepository(t *testing.T) {
	folder := t.TempDir()
	repo := filepath.Join(folder, "repo")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	os.MkdirAll(filepath.Join(folder, "notes", "deep"), 0o755)
	if _, err := Announce(folder, "http://127.0.0.1:1"); err != nil {
		t.Fatal(err)
	}
	if s, ok := FindServer(filepath.Join(folder, "notes", "deep")); !ok || s.URL != "http://127.0.0.1:1" {
		t.Fatalf("from inside the folder: %+v, %v", s, ok)
	}
	if _, ok := FindServer(filepath.Join(repo, "sub")); ok {
		t.Fatal("a repository inside the folder reached the folder's dv")
	}
}
