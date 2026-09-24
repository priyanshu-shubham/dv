package main

import (
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dv/internal/gitx"
	"dv/internal/store"
)

func notesIn(t *testing.T, dir string) []store.Note {
	t.Helper()
	repo, err := gitx.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	n, err := store.OpenNotes(repo.Root, repo.CommonDir)
	if err != nil {
		t.Fatal(err)
	}
	return n.List()
}

// A note written in a linked worktree is on the main checkout's list.
func TestNoteIsTheRepositorysNotTheWorktrees(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	root := commentRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	if out, err := exec.Command("git", "-C", root, "worktree", "add", "-q", "-b", "side", wt).CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, out)
	}
	if err := note(wt, []string{"Retry drops", "-p", "HIGH", "-l", "relay, flaky", "the last message"}, nil); err != nil {
		t.Fatal(err)
	}
	got := notesIn(t, root)
	if len(got) != 1 {
		t.Fatalf("the main checkout lists %d notes", len(got))
	}
	n := got[0]
	if n.Title != "Retry drops the last message" || n.Priority != "high" || n.Author != "Claude" || !slices.Equal(n.Labels, []string{"relay", "flaky"}) {
		t.Fatalf("note %+v", n)
	}
}

func TestNoteReadsItsTitleAndBody(t *testing.T) {
	root := commentRepo(t)
	if err := note(root, nil, strings.NewReader("\nREADME skips Node\n\nmake build needs it.\n")); err != nil {
		t.Fatal(err)
	}
	n := notesIn(t, root)[0]
	if n.Title != "README skips Node" || n.Body != "make build needs it." || n.Labels != nil {
		t.Fatalf("note %+v", n)
	}
}

func TestNoteRefusesAPriorityItHasNot(t *testing.T) {
	root := commentRepo(t)
	if err := note(root, []string{"-p", "urgent", "x"}, nil); err == nil {
		t.Fatal("took priority urgent")
	}
	if len(notesIn(t, root)) != 0 {
		t.Fatal("kept a refused note")
	}
}
