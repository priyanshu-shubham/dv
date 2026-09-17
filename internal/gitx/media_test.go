package gitx

import (
	"io"
	"testing"
)

func TestMediaIsStampedAndOpenedOnEitherSide(t *testing.T) {
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
