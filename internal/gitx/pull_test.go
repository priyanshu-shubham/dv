package gitx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFastForward(t *testing.T) {
	upstream := tempRepo(t, map[string]string{"a.txt": "a\n"})
	commitAll(t, upstream, "first")
	trunk := upstream.Head().Branch
	dir := filepath.Join(t.TempDir(), "clone")
	git(t, upstream, "clone", "-q", upstream.Root, dir)
	repo, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	git(t, repo, "config", "user.email", "t@dv")
	git(t, repo, "config", "user.name", "dv")
	behind := func(files ...string) {
		t.Helper()
		for _, f := range files {
			write(t, upstream, f, f+"\n")
			commitAll(t, upstream, f)
		}
	}
	sameAsUpstream := func() bool {
		want, _ := upstream.run("rev-parse", "HEAD")
		got, _ := repo.run("rev-parse", "refs/heads/"+trunk)
		return got == want
	}

	behind("b.txt", "c.txt")
	if n, err := repo.FastForward(trunk); err != nil || n != 2 {
		t.Fatalf("checked out: %d, %v", n, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "c.txt")); err != nil {
		t.Fatal("the checkout was not moved along")
	}
	if n, err := repo.FastForward(trunk); err != nil || n != 0 {
		t.Fatalf("up to date: %d, %v", n, err)
	}

	// Uncommitted changes to a file the pull would change stop it.
	write(t, upstream, "a.txt", "theirs\n")
	commitAll(t, upstream, "theirs")
	write(t, repo, "a.txt", "mine\n")
	if _, err := repo.FastForward(trunk); err == nil || sameAsUpstream() {
		t.Fatal("pulled over uncommitted changes")
	}
	git(t, repo, "checkout", "--", "a.txt")

	// Not checked out, the trunk moves without the checkout.
	git(t, repo, "checkout", "-qb", "side")
	behind("d.txt")
	if n, err := repo.FastForward(trunk); err != nil || n != 2 || !sameAsUpstream() {
		t.Fatalf("not checked out: %d, %v", n, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "d.txt")); err == nil {
		t.Fatal("the other branch's checkout changed")
	}

	// Checked out in another worktree, it is merged there.
	wt := filepath.Join(t.TempDir(), "wt")
	git(t, repo, "worktree", "add", "-q", wt, trunk)
	behind("e.txt")
	if n, err := repo.FastForward(trunk); err != nil || n != 1 || !sameAsUpstream() {
		t.Fatalf("in a worktree: %d, %v", n, err)
	}
	if _, err := os.Stat(filepath.Join(wt, "e.txt")); err != nil {
		t.Fatal("the worktree was not moved along")
	}
	git(t, repo, "worktree", "remove", wt)

	git(t, repo, "checkout", "-q", trunk)
	write(t, repo, "mine.txt", "mine\n")
	commitAll(t, repo, "mine")
	behind("f.txt")
	if _, err := repo.FastForward(trunk); err == nil || !strings.Contains(err.Error(), "diverged") {
		t.Fatalf("diverged: %v", err)
	}
	if _, err := repo.FastForward("side"); err == nil || !strings.Contains(err.Error(), "tracks no remote") {
		t.Fatalf("no upstream: %v", err)
	}
}
