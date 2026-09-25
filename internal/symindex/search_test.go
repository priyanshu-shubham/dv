package symindex

import (
	"slices"
	"strings"
	"testing"
)

// TestSearchEnginesAgree holds ripgrep to the files the builtin engine reads:
// folders like build/ and .github/ that aren't ignored are in; anything not
// listed, such as .git/ or a submodule's files, is out, and so is a file too
// big to read.
func TestSearchEnginesAgree(t *testing.T) {
	ix := build(t, map[string]string{
		"internal/build/store.go":     "package build\n\nfunc EmitSettled() {}\n",
		".github/workflows/ci.yml":    "run: go test # func EmitSettled\n",
		".git/hooks/pre-commit.local": "func EmitSettled\n",
		"sub/module.go":               "func EmitSettled() {}\n",
		"big.txt":                     "func EmitSettled\n" + strings.Repeat("x", maxIndexedBytes),
	})
	ix.lister = fakeLister{[]string{"internal/build/store.go", ".github/workflows/ci.yml", "big.txt"}}

	engines := map[string]func(SearchOpts) (*SearchResult, error){"builtin": ix.searchBuiltin}
	if rgPath != "" {
		engines["rg"] = ix.searchRG
	}
	for engine, search := range engines {
		res, err := search(SearchOpts{Query: "func EmitSettled", MaxMatches: 10})
		var got []string
		if err == nil {
			for _, m := range res.Matches {
				got = append(got, m.File)
			}
			slices.Sort(got)
		}
		if want := []string{".github/workflows/ci.yml", "internal/build/store.go"}; !slices.Equal(got, want) {
			t.Errorf("%s: %q, %v; want %q", engine, got, err, want)
		}
	}
	if hits := ix.Query("EmitSettled", "", 5); len(hits) == 0 || hits[0].File != "internal/build/store.go" {
		t.Errorf("symbols: %+v; want internal/build/store.go", hits)
	}
}

func TestSearchPattern(t *testing.T) {
	ix := build(t, map[string]string{"a.go": `func (f *fakeRoots) GetRootInfo(_ context.Context) {
x := r.GetRootInfo(ctx, id)
y := r.GetRootInfo()
z := r.GetRootInfo( ctx)
w := r.GetRootInfos(ctx)
v := r.XGetRootInfo(ctx)
`})

	cases := []struct {
		name  string
		opts  SearchOpts
		lines []int
	}{
		{"whole word", SearchOpts{Query: "GetRootInfo", WholeWord: true}, []int{1, 2, 3, 4}},
		{"whole word ending in a bracket", SearchOpts{Query: "GetRootInfo(", WholeWord: true}, []int{1, 2, 3, 4}},
		{"whole word starting with a bracket", SearchOpts{Query: "(ctx", WholeWord: true}, []int{2, 5, 6}},
		{"whole word regex", SearchOpts{Query: `Get\w+Info`, Regex: true, WholeWord: true}, []int{1, 2, 3, 4}},
		{"literal brackets", SearchOpts{Query: "Info()"}, []int{3}},
		{"smart case", SearchOpts{Query: "getrootinfo("}, []int{1, 2, 3, 4, 6}},
	}
	engines := map[string]func(SearchOpts) (*SearchResult, error){"builtin": ix.searchBuiltin}
	if rgPath != "" {
		engines["rg"] = ix.searchRG
	}
	for engine, search := range engines {
		for _, c := range cases {
			c.opts.MaxMatches = 100
			res, err := search(c.opts)
			if err != nil {
				t.Fatalf("%s, %s: %v", engine, c.name, err)
			}
			var got []int
			for _, m := range res.Matches {
				got = append(got, m.Line)
			}
			slices.Sort(got)
			if !slices.Equal(got, c.lines) {
				t.Errorf("%s, %s: lines %v, want %v", engine, c.name, got, c.lines)
			}
		}
	}
}
