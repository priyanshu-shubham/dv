package gitx

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParsePR(t *testing.T) {
	for name, c := range map[string]struct {
		json string
		want PR
	}{
		"draft, one pending": {
			`{"number":7,"title":"t","url":"u","state":"OPEN","isDraft":true,"reviewDecision":"",
			  "statusCheckRollup":[{"status":"COMPLETED","conclusion":"SUCCESS"},{"status":"IN_PROGRESS","conclusion":""}]}`,
			PR{Number: 7, Title: "t", URL: "u", State: "draft", Checks: "pending"},
		},
		"a failure outweighs what follows": {
			`{"number":8,"state":"OPEN","reviewDecision":"APPROVED",
			  "statusCheckRollup":[{"status":"COMPLETED","conclusion":"FAILURE"},{"status":"QUEUED"},{"state":"SUCCESS"}]}`,
			PR{Number: 8, State: "open", Checks: "failing", Review: "approved"},
		},
		"merged, skipped and commit statuses pass": {
			`{"number":9,"state":"MERGED","statusCheckRollup":[{"status":"COMPLETED","conclusion":"SKIPPED"},{"state":"SUCCESS"}]}`,
			PR{Number: 9, State: "merged", Checks: "passing"},
		},
		"no checks": {`{"number":10,"state":"CLOSED","reviewDecision":"CHANGES_REQUESTED"}`, PR{Number: 10, State: "closed", Review: "changes"}},
	} {
		got := parsePR([]byte(c.json))
		if got == nil || *got != c.want {
			t.Errorf("%s: %+v, want %+v", name, got, c.want)
		}
	}
	if parsePR([]byte(`{}`)) != nil {
		t.Error("a PR with no number")
	}
}

// A trunk here that has moved on - even past the branch, as merging it in
// locally does - changes neither what a pull request holds nor the branch's
// diff once its pull request is known.
func TestPRDiffIsGitHubs(t *testing.T) {
	repo := tempRepo(t, map[string]string{"a.txt": "a\n"})
	commitAll(t, repo, "first")
	trunk := repo.Head().Branch
	base := strings.TrimSpace(must(repo.run("rev-parse", "HEAD")))
	git(t, repo, "checkout", "-qb", "feat")
	write(t, repo, "f.txt", "f\n")
	commitAll(t, repo, "feat")
	git(t, repo, "checkout", "-q", trunk)
	write(t, repo, "m.txt", "m\n")
	commitAll(t, repo, "local only")
	git(t, repo, "merge", "-q", "--no-edit", "feat")
	git(t, repo, "update-ref", prRef(7, "head"), "feat")
	git(t, repo, "update-ref", prRef(7, "base"), base)

	paths := func(kind, rev string) []string {
		t.Helper()
		s, err := repo.ResolveScope(kind, rev)
		if err != nil {
			t.Fatal(err)
		}
		files, err := repo.Files(s)
		if err != nil {
			t.Fatal(err)
		}
		var ps []string
		for _, f := range files {
			ps = append(ps, f.Path)
		}
		return ps
	}
	if got := paths("pr", "7"); !slices.Equal(got, []string{"f.txt"}) {
		t.Errorf("#7 holds %v", got)
	}
	if _, err := repo.ResolveScope("pr", "8"); err == nil {
		t.Error("an unfetched pull request resolved")
	}

	git(t, repo, "checkout", "-q", "feat")
	if got := paths("branch", ""); len(got) != 0 {
		t.Fatalf("against the local trunk the branch holds %v", got)
	}
	key := repo.Root + "\x00feat"
	prMu.Lock()
	prCache[key] = prAnswer{&PR{Number: 7, State: "open", Base: trunk, BaseSHA: base}, time.Now()}
	prMu.Unlock()
	t.Cleanup(func() { prMu.Lock(); delete(prCache, key); prMu.Unlock() })
	if got := paths("branch", ""); !slices.Equal(got, []string{"f.txt"}) {
		t.Errorf("against #7's base the branch holds %v", got)
	}
}

func TestLocateCheckouts(t *testing.T) {
	repo := tempRepo(t, map[string]string{"a.txt": "a\n"})
	commitAll(t, repo, "first")
	other := filepath.Join(t.TempDir(), "wt")
	git(t, repo, "worktree", "add", "-q", "-b", "feat", other)
	git(t, repo, "checkout", "-qb", "pr-9")
	git(t, repo, "config", "branch.pr-9.remote", "https://github.com/someone/r.git")
	git(t, repo, "config", "branch.pr-9.merge", "refs/pull/9/head")
	git(t, repo, "branch", "-q", "lone")

	prs := []*PR{
		{Number: 7, Head: "feat", URL: "https://github.com/o/r/pull/7"},
		{Number: 9, Head: "fix", URL: "https://github.com/o/r/pull/9", fork: true, headRepo: "someone/r"},
		{Number: 10, Head: "lone", URL: "https://github.com/o/r/pull/10"},
		{Number: 11, Head: "feat", URL: "https://github.com/o/r/pull/11", fork: true, headRepo: "someone/r"},
	}
	repo.locate(prs)
	real, _ := filepath.EvalSymlinks(other)
	if prs[0].Here || prs[0].Elsewhere != real {
		t.Errorf("#7: here %v, elsewhere %q", prs[0].Here, prs[0].Elsewhere)
	}
	if !prs[1].Here {
		t.Error("#9, checked out by its pull ref, is not here")
	}
	for _, pr := range prs[2:] {
		if pr.Here || pr.Elsewhere != "" {
			t.Errorf("#%d is checked out: here %v, elsewhere %q", pr.Number, pr.Here, pr.Elsewhere)
		}
	}
}

func must(s string, err error) string {
	if err != nil {
		panic(err)
	}
	return s
}
