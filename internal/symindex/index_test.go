package symindex

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuiltOnUseAndLetGo(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n\nfunc Hello() {}\n"), 0o644)
	ix := New(root, fakeLister{[]string{"a.go"}})
	defer ix.Close()
	if st := ix.Status(); st.Symbols != 0 || st.BuiltAt != "" {
		t.Fatalf("built before any use: %+v", st)
	}
	ix.Ready()
	if len(ix.Lookup("Hello")) != 1 {
		t.Fatalf("Hello not found once ready: %+v", ix.Status())
	}
	ix.drop()
	if st := ix.Status(); st.Symbols != 0 || st.BuiltAt != "" {
		t.Fatalf("kept after going idle: %+v", st)
	}
	// Used again, it is built again before the question is answered.
	ix.Ready()
	if len(ix.Lookup("Hello")) != 1 {
		t.Fatalf("Hello not found once used again: %+v", ix.Status())
	}
}

func TestCatchesUpWithEdits(t *testing.T) {
	ix := build(t, map[string]string{
		"a.go":    "package a\n\nfunc Hello() {}\n",
		"gone.go": "package a\n\nfunc Gone() {}\n",
	})
	os.WriteFile(filepath.Join(ix.root, "a.go"), []byte("package a\n\nfunc Added() {}\n\nfunc Hello() {}\n"), 0o644)
	os.WriteFile(filepath.Join(ix.root, "new.go"), []byte("package a\n\nfunc Fresh() {}\n"), 0o644)
	os.Remove(filepath.Join(ix.root, "gone.go"))
	ix.lister = fakeLister{[]string{"a.go", "new.go"}}

	ix.Ready()
	if got := ix.Lookup("Hello"); len(got) != 1 || got[0].Line != 5 {
		t.Errorf("Hello not moved to line 5: %+v", got)
	}
	for _, name := range []string{"Added", "Fresh"} {
		if len(ix.Lookup(name)) != 1 {
			t.Errorf("%s not picked up", name)
		}
	}
	if got := ix.Lookup("Gone"); len(got) != 0 {
		t.Errorf("kept a deleted file's definition: %+v", got)
	}
	if st := ix.Status(); st.Files != 2 {
		t.Errorf("files: %+v", st)
	}
}

type stillLister struct{ fakeLister }

func (stillLister) Version() (string, error) { return "same", nil }

func TestCatchUpWaitsForTheVersion(t *testing.T) {
	ix := build(t, map[string]string{"a.go": "package a\n\nfunc Hello() {}\n"})
	ix.lister = stillLister{fakeLister{[]string{"a.go"}}}
	ix.Build()
	os.WriteFile(filepath.Join(ix.root, "a.go"), []byte("package a\n\n\nfunc Hello() {}\n"), 0o644)

	ix.Ready()
	if got := ix.Lookup("Hello"); len(got) != 1 || got[0].Line != 3 {
		t.Errorf("looked over the files with the version unmoved: %+v", got)
	}
}

func TestBuildWaitsForOneRunning(t *testing.T) {
	ix := build(t, map[string]string{"a.go": "package a\n\nfunc Hello() {}\n"})
	ix.mu.Lock()
	ix.building, ix.built = true, make(chan struct{})
	done := ix.built
	ix.mu.Unlock()
	waited := make(chan bool)
	go func() {
		ix.Build()
		waited <- true
	}()
	select {
	case <-waited:
		t.Fatal("did not wait for the build running")
	default:
	}
	ix.mu.Lock()
	ix.building = false
	close(done)
	ix.mu.Unlock()
	<-waited
}
