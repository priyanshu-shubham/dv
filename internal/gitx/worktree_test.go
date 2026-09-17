package gitx

import (
	"path/filepath"
	"testing"
)

func TestStatus(t *testing.T) {
	upstream := tempRepo(t, map[string]string{"a.txt": "a\n", "b.txt": "b\n"})
	commitAll(t, upstream, "first")
	clone := filepath.Join(t.TempDir(), "clone")
	git(t, upstream, "clone", "-q", upstream.Root, clone)
	repo, err := Open(clone)
	if err != nil {
		t.Fatal(err)
	}
	git(t, repo, "config", "user.email", "t@dv")
	git(t, repo, "config", "user.name", "dv")
	branch := repo.Head().Branch

	// One commit here the upstream lacks, and one there this lacks.
	write(t, repo, "c.txt", "c\n")
	git(t, repo, "add", "c.txt")
	git(t, repo, "commit", "-qm", "here")
	write(t, upstream, "d.txt", "d\n")
	commitAll(t, upstream, "there")
	git(t, repo, "fetch", "-q")

	write(t, repo, "a.txt", "changed\n")
	git(t, repo, "add", "a.txt")
	write(t, repo, "b.txt", "changed\n")
	write(t, repo, "new.txt", "new\n")
	s, err := repo.Status()
	if err != nil {
		t.Fatal(err)
	}
	want := Status{Branch: branch, Upstream: "origin/" + branch, Ahead: 1, Behind: 1, Staged: 1, Unstaged: 1, Untracked: 1}
	if *s != want {
		t.Fatalf("status %+v, want %+v", *s, want)
	}

	git(t, repo, "checkout", "-q", "--detach")
	if s, _ := repo.Status(); len(s.Branch) != 7 || s.Upstream != "" {
		t.Fatalf("detached: %+v", s)
	}
}

func TestDescribeRemote(t *testing.T) {
	for raw, want := range map[string][2]string{
		"https://github.com/you/dv.git":           {"github.com/you/dv", "https://github.com/you/dv"},
		"https://token@gitlab.com/group/sub/x":    {"gitlab.com/group/sub/x", "https://gitlab.com/group/sub/x"},
		"git@github.com:you/dv.git":               {"github.com/you/dv", "https://github.com/you/dv"},
		"ssh://git@git.example.com:2222/team/app": {"git.example.com/team/app", "https://git.example.com/team/app"},
		"/srv/git/app.git":                        {"/srv/git/app.git", ""},
	} {
		label, web := describeRemote(raw)
		if label != want[0] || web != want[1] {
			t.Errorf("%s: %q %q, want %q %q", raw, label, web, want[0], want[1])
		}
	}
}
