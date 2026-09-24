package gitx

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMediaIsStampedAndOpenedOnEitherSide(t *testing.T) {
	cache := t.TempDir()
	MediaCache = func() (string, error) { return cache, nil }
	repo := tempRepo(t, map[string]string{"shot.png": "\x89PNG\x00old"})
	commitAll(t, repo, "base")
	write(t, repo, "shot.png", "\x89PNG\x00new!")

	sc, err := repo.ResolveScope("working", "")
	if err != nil {
		t.Fatal(err)
	}
	files, _ := repo.Files(sc)
	if len(files) != 1 || files[0].Media != "image/png" {
		t.Fatalf("listed %+v; want shot.png as image/png", files)
	}

	read := func(old bool) string {
		t.Helper()
		f, _, err := repo.OpenAt("shot.png", sc, old)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		b, _ := io.ReadAll(f)
		return string(b)
	}
	if got := read(false); got != "\x89PNG\x00new!" {
		t.Fatalf("new side %q", got)
	}
	if got := read(true); got != "\x89PNG\x00old" {
		t.Fatalf("old side %q", got)
	}
	// Kept for the ranges asked for next, and served from there.
	kept, _ := os.ReadDir(cache)
	if len(kept) != 1 {
		t.Fatalf("the cache holds %d files, want the old side's one", len(kept))
	}
	copied := filepath.Join(cache, kept[0].Name())
	os.WriteFile(copied, []byte("from the cache"), 0o600)
	if got := read(true); got != "from the cache" {
		t.Fatalf("old side read again from git: %q", got)
	}
	ClearMediaCache()
	if _, err := os.Stat(copied); err != nil {
		t.Fatal("a copy just used was cleared")
	}
	old := time.Now().Add(-2 * mediaKeep)
	os.Chtimes(copied, old, old)
	ClearMediaCache()
	if _, err := os.Stat(copied); !os.IsNotExist(err) {
		t.Fatal("a copy unused for longer than it is kept was not cleared")
	}

	disk, at, err := repo.StampAt("shot.png", sc, false)
	if err != nil || at != "" {
		t.Fatalf("stamp on disk: %q at %q, %v", disk, at, err)
	}
	head, at, err := repo.StampAt("shot.png", sc, true)
	if err != nil || at != "HEAD" || head == disk {
		t.Fatalf("stamp at HEAD: %q at %q, %v", head, at, err)
	}
	write(t, repo, "shot.png", "\x89PNG\x00newer")
	if again, _, _ := repo.StampAt("shot.png", sc, false); again == disk {
		t.Fatal("the stamp held still through an edit")
	}
	if _, _, err := repo.StampAt("gone.png", sc, true); err == nil {
		t.Fatal("stamped a file that is not there")
	}
}
