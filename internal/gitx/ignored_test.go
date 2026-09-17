package gitx

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestIgnoredListsFoldersWhole(t *testing.T) {
	r := tempRepo(t, map[string]string{
		".gitignore":                "node_modules/\n*.log\ndist\n",
		"src/main.go":               "package main\n",
		"src/old.go":                "package main\n",
		"node_modules/a/b/index.js": "x",
		"node_modules/top.js":       "x",
		"dist/out.js":               "x",
		"src/logs/1.log":            "x",
		"loose/deep/z.log":          "x",
		"loose/keep.txt":            "x",
		"app.log":                   "x",
		".dv/comments.json":         "{}",
	})
	os.WriteFile(filepath.Join(r.Root, ".git/info/exclude"), []byte(".dv/\n"), 0o644)
	git(t, r, "add", ".gitignore", "src")
	commitAll(t, r, "init")
	// A rename's porcelain entry carries a second path.
	git(t, r, "mv", "src/old.go", "src/new.go")

	sc, err := r.ResolveScope("working", "")
	if err != nil {
		t.Fatal(err)
	}
	list := func(open ...string) []string {
		t.Helper()
		got, err := r.Ignored(sc, open)
		if err != nil {
			t.Fatal(err)
		}
		sort.Strings(got)
		return got
	}

	want := []string{"app.log", "dist/", "loose/deep/z.log", "node_modules/", "src/logs/1.log"}
	if got := list(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ignored %q; want %q", got, want)
	}

	// Opened a level at a time; what is not inside an ignored folder is refused.
	want = []string{"app.log", "dist/", "loose/deep/z.log", "node_modules/", "node_modules/a/", "node_modules/a/b/", "node_modules/a/b/index.js", "node_modules/top.js", "src/logs/1.log"}
	if got := list("node_modules", "node_modules/a", "node_modules/a/b", "src", "node_modules/../src", "../x", ".git"); !reflect.DeepEqual(got, want) {
		t.Fatalf("opened %q; want %q", got, want)
	}

	staged, _ := r.ResolveScope("staged", "")
	if got, err := r.Ignored(staged, []string{"node_modules"}); err != nil || got != nil {
		t.Fatalf("the index has no ignored files; got %q, %v", got, err)
	}
}

func TestIgnoredInAFolder(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.txt", "node_modules/x/index.js", "web/.venv/lib.py", ".dv/comments.json"} {
		p := filepath.Join(root, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, nil, 0o644)
	}
	r, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	sc, _ := r.ResolveScope("auto", "")
	got, err := r.Ignored(sc, []string{"node_modules"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"node_modules/", "web/.venv/", "node_modules/x/"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ignored %q; want %q", got, want)
	}
}
