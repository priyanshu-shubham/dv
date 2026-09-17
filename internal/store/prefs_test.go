package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrefsSetDeleteAndPrune(t *testing.T) {
	root := t.TempDir()
	a, err := OpenRepoPrefs(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, dirName)); !os.IsNotExist(err) {
		t.Fatal("opening prefs made .dv")
	}
	before := a.Version()
	if err := a.Set("draft:x", json.RawMessage(`"hello"`), nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Set("ask:gone", json.RawMessage(`{"picked": {}}`), nil); err != nil {
		t.Fatal(err)
	}
	if a.Version() == before {
		t.Fatal("version did not move on a write")
	}

	// Another process on the same file sees the values, and its write keeps them.
	b, _ := OpenRepoPrefs(root)
	if got := string(b.All()["draft:x"]); got != `"hello"` {
		t.Fatalf("b reads draft:x as %s", got)
	}
	keep := func(k string) bool { return !strings.HasPrefix(k, "ask:") }
	if err := b.Set("pathFilter", json.RawMessage(`{"include": "*.go"}`), keep); err != nil {
		t.Fatal(err)
	}
	all := a.All()
	if _, ok := all["ask:gone"]; ok {
		t.Fatal("keep did not drop ask:gone")
	}
	if len(all) != 2 {
		t.Fatalf("after b's write a has %v", all)
	}

	// The same value in another spelling is no write at all.
	v := a.Version()
	if err := a.Set("pathFilter", json.RawMessage(`{ "include":"*.go" }`), nil); err != nil {
		t.Fatal(err)
	}
	if a.Version() != v {
		t.Fatal("an unchanged value was written again")
	}

	if err := a.Set("draft:x", json.RawMessage(`null`), nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Set("pathFilter", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, dirName, prefsName)); !os.IsNotExist(err) {
		t.Fatal("deleting every value left the file")
	}
	if err := a.Set("bad", json.RawMessage(`{`), nil); err == nil {
		t.Fatal("stored a value that is not JSON")
	}
}

func TestFolderSlugs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	f, err := OpenFolders()
	if err != nil {
		t.Fatal(err)
	}
	add := func(path string) Folder {
		t.Helper()
		d, err := f.Add(Folder{Path: path})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	one := add("/code/My Project")
	if one.Slug != "my-project" {
		t.Fatalf("slug %q", one.Slug)
	}
	if again := add("/code/My Project"); again.Slug != one.Slug || len(f.List()) != 1 {
		t.Fatal("adding a folder twice listed it twice")
	}
	if two := add("/other/my-project"); two.Slug != "my-project-2" {
		t.Fatalf("a second folder of the same name got %q", two.Slug)
	}
	if api := add("/srv/api"); api.Slug != "api-2" {
		t.Fatalf("a folder named like the hub's own path got %q", api.Slug)
	}

	// A removed slug is the old tab's URL: it names only its old folder again.
	if err := f.Remove(one.Slug); err != nil {
		t.Fatal(err)
	}
	if other := add("/elsewhere/my-project"); other.Slug == one.Slug {
		t.Fatal("a removed slug went to another folder")
	}
	if back := add("/code/My Project"); back.Slug != one.Slug {
		t.Fatalf("re-added, the folder got %q rather than its old %q", back.Slug, one.Slug)
	}
}
