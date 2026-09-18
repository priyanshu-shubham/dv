package store

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLocalToken(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Setenv("LocalAppData", t.TempDir())
	} else {
		t.Setenv("XDG_DATA_HOME", t.TempDir())
	}
	req, _ := http.NewRequest("GET", "/", nil)
	MarkLocal(req)
	if req.Header.Get(LocalHeader) != "" {
		t.Fatal("marked with a token never made")
	}
	a, err := LocalToken()
	if err != nil || len(a) != 64 {
		t.Fatalf("made %q, %v", a, err)
	}
	if b, _ := LocalToken(); b != a {
		t.Fatalf("made again: %q, then %q", a, b)
	}
	MarkLocal(req)
	if req.Header.Get(LocalHeader) != a {
		t.Fatalf("marked with %q", req.Header.Get(LocalHeader))
	}
	path, _ := localPath()
	if fi, err := os.Stat(path); err != nil || runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("%v, %v", fi, err)
	}
	if left, _ := filepath.Glob(path + ".*"); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
}
