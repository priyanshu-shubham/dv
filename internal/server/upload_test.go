package server

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreateFree(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "report.pdf"), []byte("old"), 0o644)
	os.WriteFile(filepath.Join(dir, "report-2.pdf"), []byte("old"), 0o644)
	f, name, err := createFree(dir, "report.pdf")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if name != "report-3.pdf" {
		t.Fatalf("got %q, want report-3.pdf", name)
	}
	if f, name, _ := createFree(dir, ".env"); name != ".env" {
		t.Fatalf("got %q, want .env", name)
	} else {
		f.Close()
	}
	if _, name, _ := createFree(dir, ".env"); name != ".env-2" {
		t.Fatalf("got %q, want .env-2", name)
	}
}

func TestUploadName(t *testing.T) {
	for in, want := range map[string]string{
		"a.txt":             "a.txt",
		`C:\Users\me\a.txt`: "a.txt",
		"../../etc/passwd":  "passwd",
		"..":                "",
		"":                  "",
		"/":                 "",
	} {
		if got := uploadName(in); got != want {
			t.Errorf("uploadName(%q) = %q, want %q", in, got, want)
		}
	}
}
