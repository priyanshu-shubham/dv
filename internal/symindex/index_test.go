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
