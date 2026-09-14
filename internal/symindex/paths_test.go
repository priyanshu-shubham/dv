package symindex

import (
	"reflect"
	"testing"
)

var repoPaths = []string{
	"internal/server/handlers.go",
	"internal/server/server.go",
	"internal/gitx/diff.go",
	"internal/gitx/diff_test.go",
	"web/frontend/src/FileDiff.jsx",
	"docs/diffing.md",
}

func ranked(q string, limit int) ([]string, int) {
	hits, matched := RankPaths(repoPaths, q, limit)
	var out []string
	for _, h := range hits {
		out = append(out, h.Path)
	}
	return out, matched
}

func TestRankPaths(t *testing.T) {
	for _, c := range []struct {
		q    string
		want []string
	}{
		// Any match in a name beats one inside a name, which beats none at all.
		{"diff", []string{"internal/gitx/diff.go", "docs/diffing.md", "internal/gitx/diff_test.go", "web/frontend/src/FileDiff.jsx"}},
		// Only the folders make this one.
		{"srvhand", []string{"internal/server/handlers.go"}},
		// Every term has to match.
		{"gitx diff", []string{"internal/gitx/diff.go", "internal/gitx/diff_test.go"}},
		// A slash is about folders, so the name gets no head start.
		{"server/", []string{"internal/server/server.go", "internal/server/handlers.go"}},
		{"", nil},
		{"zzz", nil},
	} {
		if got, _ := ranked(c.q, 60); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: got %q, want %q", c.q, got, c.want)
		}
	}
}

func TestRankPathsLimitAndMatches(t *testing.T) {
	got, matched := ranked("diff", 2)
	if len(got) != 2 || matched != 4 {
		t.Errorf("limit 2: got %d hits of %d matched, want 2 of 4", len(got), matched)
	}
	hits, _ := RankPaths(repoPaths, "diff", 1)
	if want := []int{14, 15, 16, 17}; !reflect.DeepEqual(hits[0].Matches, want) {
		t.Errorf("matches %v, want %v: they index into the whole path", hits[0].Matches, want)
	}
}
