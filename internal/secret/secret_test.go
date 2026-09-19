package secret

import (
	"path/filepath"
	"testing"
)

func TestKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dv")
	first, err := newKey(KeyPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	// Another dv making one at the same moment gets the one there.
	second, err := newKey(KeyPath(dir))
	if err != nil || string(second) != string(first) {
		t.Fatalf("a second key %q, %v; the first %q", second, err, first)
	}

	key, err := Key(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := Seal(key, "123:abc", "dv telegram token")
	if token, err := Open(key, sealed, "dv telegram token"); err != nil || token != "123:abc" {
		t.Fatalf("opened %q, %v", token, err)
	}
	if _, err := Open(make([]byte, 32), sealed, "dv telegram token"); err == nil {
		t.Fatal("opened with another key")
	}
	if _, err := Open(key, sealed, "dv relay token"); err == nil {
		t.Fatal("opened as something else")
	}
}
