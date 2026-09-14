package symindex

import (
	"sort"
	"strings"
)

// PathHit is one quick-open result. Matches index into Path, for highlighting.
type PathHit struct {
	Path    string `json:"path"`
	Matches []int  `json:"matches"`
}

// nameBonus puts any match inside the file name above every match that needed
// the folders too; fuzzyScore itself tops out at 10000.
const nameBonus = 20000

// RankPaths is quick open: the paths matching every space-separated term of q,
// best first, at most limit of them, along with how many matched in all.
func RankPaths(paths []string, q string, limit int) ([]PathHit, int) {
	terms := strings.Fields(q)
	out := []PathHit{}
	if len(terms) == 0 {
		return out, 0
	}
	type ranked struct {
		hit   PathHit
		score int
	}
	var all []ranked
next:
	for _, p := range paths {
		total := 0
		var marks []int
		for _, t := range terms {
			s, m, ok := scorePath(t, p)
			if !ok {
				continue next
			}
			total += s
			marks = append(marks, m...)
		}
		all = append(all, ranked{PathHit{p, marks}, total})
	}
	sort.Slice(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if a.score != b.score {
			return a.score > b.score
		}
		if len(a.hit.Path) != len(b.hit.Path) {
			return len(a.hit.Path) < len(b.hit.Path)
		}
		return a.hit.Path < b.hit.Path
	})
	for _, r := range all[:min(limit, len(all))] {
		out = append(out, r.hit)
	}
	return out, len(all)
}

// scorePath matches one term against a path. The name is what people type, so
// it is tried on its own first; the whole path is the fallback, which is what
// lets "srvhand" find server/handlers.go. A term with a slash in it is plainly
// about the folders, and goes straight to the whole path.
func scorePath(term, p string) (int, []int, bool) {
	if !strings.Contains(term, "/") {
		base := strings.LastIndexByte(p, '/') + 1
		if s, m, ok := fuzzyScore(term, p[base:]); ok {
			for i := range m {
				m[i] += base
			}
			return s + nameBonus, m, true
		}
	}
	return fuzzyScore(term, p)
}
