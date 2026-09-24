package gitx

import (
	"os"
	"path/filepath"
	"testing"
)

// Discarding puts each kind of change back as the last commit has it, staged
// or not.
func TestDiscard(t *testing.T) {
	repo := tempRepo(t, map[string]string{"edited.txt": "a\n", "gone.txt": "b\n", "old.txt": "c\n"})
	commitAll(t, repo, "first")
	write(t, repo, "edited.txt", "mine\n")
	git(t, repo, "add", "edited.txt")
	write(t, repo, "edited.txt", "mine, more\n")
	os.Remove(filepath.Join(repo.Root, "gone.txt"))
	write(t, repo, "new.txt", "new\n")
	write(t, repo, "added.txt", "added\n")
	git(t, repo, "add", "added.txt")
	git(t, repo, "mv", "old.txt", "moved.txt")

	if err := repo.Discard("edited.txt", "gone.txt", "new.txt", "added.txt", "old.txt", "moved.txt"); err != nil {
		t.Fatal(err)
	}
	if out, _ := repo.run("status", "--porcelain"); out != "" {
		t.Fatalf("left changes:\n%s", out)
	}
	if b, _ := os.ReadFile(filepath.Join(repo.Root, "edited.txt")); string(b) != "a\n" {
		t.Fatalf("edited.txt reads %q", b)
	}
	if err := repo.Discard("../outside.txt"); err == nil {
		t.Fatal("discarded a path outside the repository")
	}
}
