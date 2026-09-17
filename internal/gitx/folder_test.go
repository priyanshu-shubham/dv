package gitx

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestFolderOutsideGit(t *testing.T) {
	root := t.TempDir()
	for name, body := range map[string]string{
		"notes.md":                "# notes\n",
		"sub/a.txt":               "a\n",
		".dv/comments.json":       "{}",
		"node_modules/x/index.js": "x",
	} {
		p := filepath.Join(root, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	os.Symlink(filepath.Join(root, "sub"), filepath.Join(root, "link"))

	r, err := Open(filepath.Join(root, "sub"))
	if err != nil {
		t.Fatal(err)
	}
	if r.IsGit() || r.Root != filepath.Join(root, "sub") {
		t.Fatalf("opened %+v; want the folder itself, without git", r)
	}

	r, _ = Open(root)
	files, err := r.TrackedFiles()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"notes.md", "sub/a.txt"}; !reflect.DeepEqual(files, want) {
		t.Fatalf("listed %q; want %q", files, want)
	}

	sc, err := r.ResolveScope("auto", "")
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := r.Files(sc); err != nil || len(changed) != 0 {
		t.Fatalf("a folder has no changes; got %v, %v", changed, err)
	}
	if side, err := r.SideFiles(sc); err != nil || !reflect.DeepEqual(side, files) {
		t.Fatalf("tree %q, %v; want %q", side, err, files)
	}
	if lines, at, err := r.FileAt("notes.md", sc, false); err != nil || at != "" || lines[0] != "# notes" {
		t.Fatalf("read %q at %q: %v", lines, at, err)
	}

	v := version(t, r)
	write(t, r, "sub/a.txt", "a changed\n")
	if version(t, r) == v {
		t.Fatal("version held still through an edit")
	}
	v = version(t, r)
	write(t, r, "new.txt", "")
	if version(t, r) == v {
		t.Fatal("version held still through a new file")
	}
}
