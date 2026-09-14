package gitx

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func write(t *testing.T, r *Repo, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(r.Root, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, r *Repo, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.Root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func version(t *testing.T, r *Repo) string {
	t.Helper()
	v, err := r.Version()
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestVersionMovesWithTheRepository(t *testing.T) {
	repo := tempRepo(t, map[string]string{"a.txt": "one\n", "b.txt": "two\n"})
	commitAll(t, repo, "base")

	v := version(t, repo)
	if again := version(t, repo); again != v {
		t.Fatalf("version changed with nothing touched: %s -> %s", v, again)
	}

	steps := []struct {
		name string
		do   func()
	}{
		{"edit", func() { write(t, repo, "a.txt", "one\nmore\n") }},
		// status reports a.txt as modified both times; only its content moved.
		{"second edit to a modified file", func() { write(t, repo, "a.txt", "one\nmore\nstill more\n") }},
		{"new untracked file", func() { write(t, repo, "c.txt", "three\n") }},
		{"stage", func() { git(t, repo, "add", "a.txt") }},
		{"commit", func() { git(t, repo, "commit", "-q", "-m", "next") }},
	}
	for _, s := range steps {
		s.do()
		next := version(t, repo)
		if next == v {
			t.Errorf("%s: version did not change", s.name)
		}
		v = next
	}
}

func TestFilesRevFollowsContent(t *testing.T) {
	repo := tempRepo(t, map[string]string{"a.txt": "one\n", "b.txt": "two\n"})
	commitAll(t, repo, "base")
	write(t, repo, "a.txt", "one\nmore\n")
	write(t, repo, "b.txt", "two\nmore\n")

	revs := func(kind string) map[string]string {
		t.Helper()
		sc, err := repo.ResolveScope(kind, "")
		if err != nil {
			t.Fatal(err)
		}
		files, err := repo.Files(sc)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]string{}
		for _, f := range files {
			if f.Rev == "" {
				t.Errorf("%s: empty rev", f.Path)
			}
			m[f.Path] = f.Rev
		}
		return m
	}

	before := revs("working")
	write(t, repo, "a.txt", "one\nmore\nstill more\n")
	after := revs("working")
	if after["a.txt"] == before["a.txt"] {
		t.Error("a.txt was edited but kept its rev")
	}
	if after["b.txt"] != before["b.txt"] {
		t.Error("b.txt was not touched but its rev changed")
	}

	// The staged side is the index, so editing the working tree leaves it be.
	git(t, repo, "add", "a.txt")
	staged := revs("staged")
	write(t, repo, "a.txt", "rewritten\n")
	if revs("staged")["a.txt"] != staged["a.txt"] {
		t.Error("staged rev moved with an unstaged edit")
	}
}

func TestStatusPaths(t *testing.T) {
	z := "# branch.oid 1234\x00# branch.head main\x00" +
		"1 .M N... 100644 100644 100644 aaa aaa src/with space.go\x00" +
		"2 R. N... 100644 100644 100644 bbb bbb R100 new name.go\x00old name.go\x00" +
		"u UU N... 100644 100644 100644 100644 c1 c2 c3 conflict.txt\x00" +
		"? untracked dir/file.txt\x00"
	want := []string{"src/with space.go", "new name.go", "conflict.txt", "untracked dir/file.txt"}
	if got := statusPaths(z); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}
