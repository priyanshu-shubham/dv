package gateway

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/zalando/go-keyring"
)

// Codex keeps its sign-in where config.toml's cli_auth_credentials_store
// says: "file" (the default) is auth.json, "keyring" the system's keyring,
// "auto" the keyring and else auth.json, "ephemeral" its memory alone. With
// [features] secret_auth_storage on (the default on Windows) the keyring
// settings mean Codex's own encrypted storage, which nothing else reads.
type codexStore struct {
	mode      string
	encrypted bool
}

var (
	storeSetting  = regexp.MustCompile(`^\s*cli_auth_credentials_store\s*=\s*"(\w+)"`)
	secretStorage = regexp.MustCompile(`^\s*secret_auth_storage\s*=\s*(true|false)`)
	tomlSection   = regexp.MustCompile(`^\s*\[([^\]]*)\]`)
)

func readCodexStore(home string) codexStore {
	s := codexStore{mode: "file", encrypted: runtime.GOOS == "windows"}
	f, err := os.Open(filepath.Join(home, "config.toml"))
	if err != nil {
		return s
	}
	defer f.Close()
	section := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if m := tomlSection.FindStringSubmatch(line); m != nil {
			section = strings.TrimSpace(m[1])
			continue
		}
		if m := storeSetting.FindStringSubmatch(line); m != nil && section == "" {
			s.mode = m[1]
		}
		if m := secretStorage.FindStringSubmatch(line); m != nil && section == "features" {
			s.encrypted = m[1] == "true"
		}
	}
	return s
}

// keyringAccount is the account Codex files its sign-in under: its home's
// canonical path, hashed.
func keyringAccount(home string) string {
	if abs, err := filepath.Abs(home); err == nil {
		home = abs
	}
	if real, err := filepath.EvalSymlinks(home); err == nil {
		home = real
	}
	sum := sha256.Sum256([]byte(home))
	return "cli|" + hex.EncodeToString(sum[:])[:16]
}

// loginBytes is Codex's sign-in as it keeps it, auth.json's JSON.
func (c *codexUpstream) loginBytes() ([]byte, error) {
	s := readCodexStore(c.home)
	keyed := s.mode == "keyring" || s.mode == "auto"
	switch {
	case s.mode == "ephemeral":
		return nil, errors.New("Codex keeps its sign-in in memory only (cli_auth_credentials_store = \"ephemeral\"), out of dv's reach")
	case keyed && s.encrypted && s.mode == "keyring":
		return nil, errors.New("Codex keeps its sign-in in its own encrypted storage, which dv can't read: set cli_auth_credentials_store = \"file\" in Codex's config.toml")
	case keyed && !s.encrypted:
		v, err := keyring.Get("Codex Auth", keyringAccount(c.home))
		if err == nil {
			return []byte(v), nil
		}
		if s.mode == "keyring" {
			if errors.Is(err, keyring.ErrNotFound) {
				return nil, errNotSignedIn
			}
			return nil, fmt.Errorf("Codex's sign-in is in the system keyring, which dv could not read: %w", err)
		}
	}
	b, err := os.ReadFile(filepath.Join(c.home, "auth.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, errNotSignedIn
	}
	return b, err
}
