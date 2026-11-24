package gitx

import (
	"slices"
	"strings"
	"testing"
)

// TestAStretchAcrossAMergeOfTheTrunkIsTheBranchsOwn: read from a commit
// before the trunk was merged in, the branch's changes are its own - its
// commits and how it settled the merge - not what the trunk brought.
func TestAStretchAcrossAMergeOfTheTrunkIsTheBranchsOwn(t *testing.T) {
	repo := tempRepo(t, map[string]string{"shared.txt": "one\ntwo\nthree\nfour\nfive\n", "clash.txt": "a\n"})
	commitAll(t, repo, "start")
	git(t, repo, "branch", "-M", "main")
	git(t, repo, "checkout", "-q", "-b", "feature")
	write(t, repo, "feature.txt", "mine\n")
	write(t, repo, "shared.txt", "ONE\ntwo\nthree\nfour\nfive\n")
	write(t, repo, "clash.txt", "feature's\n")
	commitAll(t, repo, "feature work")
	from, _ := repo.run("rev-parse", "HEAD")

	git(t, repo, "checkout", "-q", "main")
	write(t, repo, "trunk.txt", "theirs\n")
	write(t, repo, "shared.txt", "one\ntwo\nthree\nfour\nFIVE\n")
	write(t, repo, "clash.txt", "main's\n")
	commitAll(t, repo, "main moves on")

	git(t, repo, "checkout", "-q", "feature")
	repo.run("merge", "-q", "--no-edit", "main") // clash.txt conflicts, settled here
	write(t, repo, "clash.txt", "both\n")
	commitAll(t, repo, "merge main")
	write(t, repo, "feature.txt", "mine, more\n")
	commitAll(t, repo, "more feature work")

	files := func(own bool) []string {
		t.Helper()
		sc, err := repo.ResolveScope("custom", strings.TrimSpace(from)+"..feature")
		if err != nil {
			t.Fatal(err)
		}
		if own {
			repo.OwnChanges(sc)
		}
		got, err := repo.Files(sc)
		if err != nil {
			t.Fatal(err)
		}
		var paths []string
		for _, f := range got {
			paths = append(paths, f.Path)
		}
		slices.Sort(paths)
		return paths
	}
	if got := files(false); !slices.Contains(got, "trunk.txt") {
		t.Fatalf("the direct comparison should show the trunk's file: %v", got)
	}
	// shared.txt merged cleanly, so the trunk's edit to it is no change of the
	// branch's; clash.txt is how the branch settled the conflict.
	if got := files(true); !slices.Equal(got, []string{"clash.txt", "feature.txt"}) {
		t.Fatalf("the branch's own changes: %v", got)
	}
}
