package gitx

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestFileAtReadsEachSide(t *testing.T) {
	r := tempRepo(t, map[string]string{"a.txt": "committed\n", "b.txt": "gone\n"})
	commitAll(t, r, "init")
	write(t, r, "a.txt", "staged\n")
	git(t, r, "add", "a.txt")
	write(t, r, "a.txt", "on disk\n")
	if err := os.Remove(filepath.Join(r.Root, "b.txt")); err != nil {
		t.Fatal(err)
	}

	working, err := r.ResolveScope("working", "")
	if err != nil {
		t.Fatal(err)
	}
	staged, err := r.ResolveScope("staged", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		path  string
		scope *Scope
		old   bool
		lines []string
		at    string
	}{
		{"no scope is the working tree", "a.txt", nil, false, []string{"on disk"}, ""},
		{"working, new side", "a.txt", working, false, []string{"on disk"}, ""},
		{"working, old side", "a.txt", working, true, []string{"committed"}, "HEAD"},
		{"staged, new side", "a.txt", staged, false, []string{"staged"}, "index"},
		{"a deleted file keeps its old side", "b.txt", working, true, []string{"gone"}, "HEAD"},
	} {
		lines, at, err := r.FileAt(c.path, c.scope, c.old)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if !reflect.DeepEqual(lines, c.lines) || at != c.at {
			t.Errorf("%s: got %q at %q, want %q at %q", c.name, lines, at, c.lines, c.at)
		}
	}
	if _, _, err := r.FileAt("b.txt", working, false); err == nil {
		t.Error("a deleted file has no new side, but reading it succeeded")
	}
	if working.OldAt != "HEAD" || working.NewAt != "" || staged.NewAt != "index" {
		t.Errorf("side labels: working %q/%q, staged new %q", working.OldAt, working.NewAt, staged.NewAt)
	}
}

func TestSideFilesListsTheNewSide(t *testing.T) {
	r := tempRepo(t, map[string]string{"a.txt": "a\n", "dir/b.txt": "b\n"})
	commitAll(t, r, "init")
	write(t, r, "new.txt", "untracked\n")
	git(t, r, "rm", "-q", "--cached", "a.txt")

	for kind, want := range map[string][]string{
		"head":    {"a.txt", "dir/b.txt"},
		"staged":  {"dir/b.txt"},
		"working": {"a.txt", "dir/b.txt", "new.txt"}, // a.txt is still on disk, now untracked
	} {
		sc, err := r.ResolveScope(kind, "")
		if err != nil {
			t.Fatal(err)
		}
		got, err := r.SideFiles(sc)
		if err != nil {
			t.Fatal(err)
		}
		sort.Strings(got) // git lists tracked files before untracked ones
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %q, want %q", kind, got, want)
		}
	}
}
