package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dv/internal/store"
)

// commentRepo is a repository with a.go committed, then changed: line 2
// replaced, and gone.go deleted.
func commentRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	git := func(args ...string) {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, body string) {
		p := filepath.Join(root, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q")
	git("config", "user.email", "t@t")
	git("config", "user.name", "t")
	write("sub/a.go", "one\ntwo\nthree\n")
	write("gone.go", "old\n")
	git("add", ".")
	git("commit", "-qm", "init")
	write("sub/a.go", "one\nTWO\nthree\nfour\n")
	os.Remove(filepath.Join(root, "gone.go"))
	return root
}

func threadsIn(t *testing.T, root string) []*store.Thread {
	t.Helper()
	st, err := store.Open(root, filepath.Base(root))
	if err != nil {
		t.Fatal(err)
	}
	return st.Threads()
}

func TestCommentLandsOnTheLines(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	root := commentRepo(t)
	if err := comment(root, []string{"sub/a.go:2-3", "Why", "upper case?"}, nil); err != nil {
		t.Fatal(err)
	}
	ts := threadsIn(t, root)
	if len(ts) != 1 {
		t.Fatalf("got %d threads", len(ts))
	}
	th := ts[0]
	if th.File != "sub/a.go" || th.Side != "new" || th.StartLine != 2 || th.EndLine != 3 {
		t.Fatalf("thread at %s %s:%d-%d", th.File, th.Side, th.StartLine, th.EndLine)
	}
	if strings.Join(th.Quote, "|") != "TWO|three" || th.Scope != "Uncommitted" || th.BaseSHA == "" {
		t.Fatalf("quote %q, scope %q, base %q", th.Quote, th.Scope, th.BaseSHA)
	}
	if c := th.Comments[0]; c.Body != "Why upper case?" || c.Author != "Claude" {
		t.Fatalf("comment %q by %q", c.Body, c.Author)
	}
	if b, _ := os.ReadFile(filepath.Join(root, ".git", "info", "exclude")); !strings.Contains(string(b), "/.dv/") {
		t.Fatal(".dv was not excluded from git")
	}
}

func TestCommentReadsItsPlace(t *testing.T) {
	root := commentRepo(t)
	sub := filepath.Join(root, "sub")
	// From the folder below, a flag after the file, the message on stdin.
	if err := comment(sub, []string{"a.go", "-author", "Codex", "-"}, strings.NewReader("## Whole file\n\nfine\n")); err != nil {
		t.Fatal(err)
	}
	if err := comment(root, []string{"-old", "gone.go:1", "was needed"}, nil); err != nil {
		t.Fatal(err)
	}
	on := map[string]*store.Thread{}
	for _, th := range threadsIn(t, root) {
		on[th.File] = th
	}
	if th := on["sub/a.go"]; th == nil || th.StartLine != 0 || th.Comments[0].Author != "Codex" || th.Comments[0].Body != "## Whole file\n\nfine" {
		t.Fatalf("file comment: %+v", th)
	}
	if th := on["gone.go"]; th == nil || th.Side != "old" || strings.Join(th.Quote, "") != "old" {
		t.Fatalf("old-side comment: %+v", th)
	}
}

func TestCommentRefusesWhatIsNotThere(t *testing.T) {
	root := commentRepo(t)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"sub/a.go:9", "x"}, "sub/a.go has only 4 lines"},
		{[]string{"-old", "sub/a.go:4", "x"}, "sub/a.go has only 3 lines at HEAD"},
		{[]string{"gone.go:1", "x"}, "gone.go is deleted; pass -old"},
		{[]string{"nope.go", "x"}, "no such file: nope.go"},
		{[]string{"sub/a.go:3-2", "x"}, "ends before it starts"},
		{[]string{"sub/a.go:1", " "}, "the comment is empty"},
	} {
		err := comment(root, c.args, strings.NewReader(""))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: got %v, want %q", c.args, err, c.want)
		}
	}
	if n := len(threadsIn(t, root)); n != 0 {
		t.Fatalf("%d threads left by refused comments", n)
	}
}
