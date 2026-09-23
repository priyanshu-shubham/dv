package gitx

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSwitch(t *testing.T) {
	repo := tempRepo(t, map[string]string{"a.txt": "a\n"})
	commitAll(t, repo, "first")
	trunk := repo.Head().Branch
	git(t, repo, "checkout", "-qb", "side")
	write(t, repo, "b.txt", "b\n")
	commitAll(t, repo, "side")
	git(t, repo, "checkout", "-q", trunk)
	refused := func(branch, why string) {
		t.Helper()
		if err := repo.Switch(branch); err == nil || !strings.Contains(err.Error(), why) {
			t.Fatalf("switch to %s: %v, want %q", branch, err, why)
		}
		if repo.Head().Branch != trunk {
			t.Fatal("switched anyway")
		}
	}

	write(t, repo, "a.txt", "mine\n")
	refused("side", "uncommitted")
	git(t, repo, "add", "a.txt")
	refused("side", "uncommitted")
	git(t, repo, "reset", "-q", "--hard")

	// An untracked file the branch has, ignored or not, would be written over.
	write(t, repo, "b.txt", "mine\n")
	refused("side", "b.txt")
	os.WriteFile(filepath.Join(repo.GitDir, "info", "exclude"), []byte("b.txt\n"), 0o644)
	refused("side", "b.txt")
	os.Remove(filepath.Join(repo.Root, "b.txt"))

	refused("nope", "no branch")
	refused(trunk, "checked out already")

	wt := filepath.Join(t.TempDir(), "wt")
	git(t, repo, "worktree", "add", "-q", wt, "side")
	if !slices.Equal(repo.Elsewhere(), []string{"side"}) {
		t.Fatalf("elsewhere: %v", repo.Elsewhere())
	}
	refused("side", "checked out in")
	git(t, repo, "worktree", "remove", wt)

	// Untracked files that clash with nothing come along.
	write(t, repo, "c.txt", "c\n")
	if err := repo.Switch("side"); err != nil || repo.Head().Branch != "side" {
		t.Fatalf("switch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo.Root, "c.txt")); err != nil {
		t.Fatal("the untracked file was lost")
	}

	// Commits on a detached HEAD would be left behind.
	git(t, repo, "checkout", "-q", "--detach")
	if why := repo.SwitchBlocker(); why != "" {
		t.Fatalf("detached on a branch's commit: %s", why)
	}
	write(t, repo, "d.txt", "d\n")
	commitAll(t, repo, "loose")
	if why := repo.SwitchBlocker(); !strings.Contains(why, "no branch") {
		t.Fatalf("detached with loose commits: %q", why)
	}

	// A new branch keeps them, and takes uncommitted changes along.
	write(t, repo, "d.txt", "changed\n")
	if err := repo.CreateBranch("side"); err == nil {
		t.Fatal("made a branch over an existing one")
	}
	if err := repo.CreateBranch("-x"); err == nil {
		t.Fatal("made a branch git would read as a flag")
	}
	if err := repo.CreateBranch("rescue"); err != nil || repo.Head().Branch != "rescue" {
		t.Fatalf("create: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(repo.Root, "d.txt")); string(b) != "changed\n" {
		t.Fatal("the uncommitted change was lost")
	}
}

func TestTrackARemoteBranch(t *testing.T) {
	up := tempRepo(t, map[string]string{"a.txt": "a\n"})
	commitAll(t, up, "first")
	git(t, up, "checkout", "-qb", "fix/login")
	write(t, up, "b.txt", "b\n")
	commitAll(t, up, "fix")
	repo := tempRepo(t, map[string]string{"a.txt": "a\n"})
	commitAll(t, repo, "first")
	git(t, repo, "remote", "add", "origin", up.Root)
	if err := repo.Fetch(); err != nil {
		t.Fatal(err)
	}
	// The remote's trunk has a local branch already, so only its fix is offered.
	if got := repo.RemoteRefs(); !slices.Equal(got, []RemoteRef{{"origin/fix/login", "fix/login"}}) {
		t.Fatalf("remote refs %v", got)
	}

	write(t, repo, "b.txt", "mine\n")
	if err := repo.Track("origin/fix/login"); err == nil || !strings.Contains(err.Error(), "b.txt") {
		t.Fatalf("track over an untracked file: %v", err)
	}
	os.Remove(filepath.Join(repo.Root, "b.txt"))
	if err := repo.Track("origin/nope"); err == nil {
		t.Fatal("tracked a branch no remote has")
	}
	if err := repo.Track("origin/fix/login"); err != nil || repo.Head().Branch != "fix/login" {
		t.Fatalf("track: %v, on %s", err, repo.Head().Branch)
	}
	if up, _ := repo.run("rev-parse", "--abbrev-ref", "fix/login@{upstream}"); strings.TrimSpace(up) != "origin/fix/login" {
		t.Fatalf("upstream %q", up)
	}
	if len(repo.RemoteRefs()) != 0 {
		t.Fatalf("a branch with a local one still offered: %v", repo.RemoteRefs())
	}
}
