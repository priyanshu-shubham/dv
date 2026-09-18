package telegram

import (
	"path/filepath"
	"testing"
)

func TestKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dv", "secret.key")
	first, err := newKey(path)
	if err != nil {
		t.Fatal(err)
	}
	// Another dv making one at the same moment gets the one there.
	second, err := newKey(path)
	if err != nil || string(second) != string(first) {
		t.Fatalf("a second key %q, %v; the first %q", second, err, first)
	}

	a := &Telegram{keyDir: filepath.Dir(path)}
	key, err := a.key(false)
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := seal(key, "123:abc")
	if token, err := unseal(key, sealed); err != nil || token != "123:abc" {
		t.Fatalf("unsealed %q, %v", token, err)
	}
	other := make([]byte, 32)
	if _, err := unseal(other, sealed); err == nil {
		t.Fatal("opened with another key")
	}
}
