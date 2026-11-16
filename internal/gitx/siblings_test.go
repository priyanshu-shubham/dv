package gitx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBranchCommits(t *testing.T) {
	repo := tempRepo(t, map[string]string{"a.txt": "a\n"})
	commitAll(t, repo, "first")
	trunk := repo.Head().Branch

	// On the trunk, the latest on HEAD stand in.
	if base, cs := repo.BranchCommits(10); base != "" || len(cs) != 1 || cs[0].Subject != "first" {
		t.Fatalf("on the trunk: %q %+v", base, cs)
	}

	git(t, repo, "checkout", "-qb", "side")
	write(t, repo, "b.txt", "b\n")
	commitAll(t, repo, "add b\n\nWhy b is wanted.\nSecond line.")
	write(t, repo, "c.txt", "c\n")
	commitAll(t, repo, "add c")
	base, cs := repo.BranchCommits(10)
	if base != trunk || len(cs) != 2 || cs[0].Subject != "add c" || cs[1].Body != "Why b is wanted.\nSecond line." {
		t.Fatalf("on a branch: %q %+v", base, cs)
	}

	tips := repo.BranchTips()
	if tips["side"].Subject != "add c" || tips[trunk].Subject != "first" || tips["side"].When == "" {
		t.Fatalf("tips: %+v", tips)
	}

	s, err := repo.ResolveScope("custom", cs[1].SHA)
	if err != nil || s.Commit == nil || s.Commit.Subject != "add b" || s.Label != cs[1].Short {
		t.Fatalf("one commit: %+v %v", s, err)
	}
	if s, _ := repo.ResolveScope("head", ""); s.Commit == nil || s.Commit.Subject != "add c" {
		t.Fatalf("last commit: %+v", s)
	}
	if s, _ := repo.ResolveScope("branch", ""); s.Commit != nil {
		t.Fatal("a range has a commit of its own")
	}
}

func TestLogPage(t *testing.T) {
	repo := tempRepo(t, map[string]string{"a.txt": "0\n"})
	commitAll(t, repo, "commit 0")
	trunk := repo.Head().Branch
	for i := 1; i <= 4; i++ {
		write(t, repo, "a.txt", strings.Repeat("x", i)+"\n")
		commitAll(t, repo, "commit "+string(rune('0'+i)))
	}
	git(t, repo, "checkout", "-qb", "side", "HEAD~2")

	first, more, err := repo.LogPage(trunk, 0, 3, "")
	if err != nil || !more || len(first) != 3 || first[0].Subject != "commit 4" {
		t.Fatalf("first page: %+v %v %v", first, more, err)
	}
	rest, more, _ := repo.LogPage(trunk, 3, 3, "")
	if more || len(rest) != 2 || rest[1].Subject != "commit 0" {
		t.Fatalf("second page: %+v %v", rest, more)
	}
	if found, _, _ := repo.LogPage(trunk, 0, 10, "COMMIT 3"); len(found) != 1 || found[0].Subject != "commit 3" {
		t.Fatalf("search: %+v", found)
	}

	// From side, which left at commit 2, the trunk's last two are not here.
	missing := repo.Missing(trunk, []string{first[0].SHA, first[1].SHA, first[2].SHA})
	if len(missing) != 2 || !missing[first[0].SHA] || !missing[first[1].SHA] {
		t.Fatalf("missing: %v", missing)
	}

	for _, bad := range []string{"--all", "nope"} {
		if _, _, err := repo.LogPage(bad, 0, 3, ""); err == nil {
			t.Fatalf("%s: no error", bad)
		}
	}
}

func TestSiblings(t *testing.T) {
	repo := tempRepo(t, map[string]string{"a.txt": "a\n"})
	commitAll(t, repo, "first")
	trunk := repo.Head().Branch
	dir := t.TempDir()
	side, loose := filepath.Join(dir, "side"), filepath.Join(dir, "loose")
	git(t, repo, "worktree", "add", "-q", "-b", "side", side)
	git(t, repo, "worktree", "add", "-q", "--detach", loose)

	if b := BranchAt(repo.Root); b != trunk {
		t.Fatalf("main checkout: %q, want %q", b, trunk)
	}
	if b := BranchAt(side); b != "side" {
		t.Fatalf("worktree: %q", b)
	}
	if b := BranchAt(loose); b != "" {
		t.Fatalf("detached: %q", b)
	}

	wt, err := Open(side)
	if err != nil {
		t.Fatal(err)
	}
	others := wt.OtherWorktrees()
	if len(others) != 2 || others[0].Path != repo.Root || others[0].Branch != trunk || others[1].Branch != "" || others[1].SHA == "" {
		t.Fatalf("others: %+v", others)
	}

	// A worktree whose folder went is no sibling.
	os.RemoveAll(loose)
	if others := wt.OtherWorktrees(); len(others) != 1 {
		t.Fatalf("after removing one: %+v", others)
	}

	write(t, wt, "b.txt", "b\n")
	commitAll(t, wt, "side")
	if n := repo.Count(trunk, "side"); n != 1 {
		t.Fatalf("ahead: %d", n)
	}
	write(t, repo, "a.txt", "dirty\n")
	if err := repo.Merge("side"); err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("merge into a dirty checkout: %v", err)
	}
	git(t, repo, "checkout", "-q", "--", "a.txt")
	if err := repo.Merge("side"); err != nil || repo.Count(trunk, "side") != 0 {
		t.Fatalf("merge: %v", err)
	}
	if err := repo.DeleteBranch("side"); err == nil {
		t.Fatal("deleted a branch checked out in a worktree")
	}

	// A conflict is undone, the files named.
	write(t, wt, "a.txt", "theirs\n")
	commitAll(t, wt, "theirs")
	write(t, repo, "a.txt", "ours\n")
	commitAll(t, repo, "ours")
	err = repo.Merge("side")
	if err == nil || !strings.Contains(err.Error(), "a.txt") {
		t.Fatalf("conflicting merge: %v", err)
	}
	if _, e := os.Stat(filepath.Join(repo.GitDir, "MERGE_HEAD")); e == nil {
		t.Fatal("the merge was left under way")
	}
	if b, _ := os.ReadFile(filepath.Join(repo.Root, "a.txt")); string(b) != "ours\n" {
		t.Fatalf("a.txt after the undone merge: %q", b)
	}
}
