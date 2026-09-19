// Package secret keeps a chat app's credentials - a bot's token, a relay's -
// sealed with a key made for this computer and kept apart from dv's settings,
// so a copy of those - in a backup, a dotfiles repository, pasted somewhere -
// carries nothing that works. Anything running as the user can still read
// both.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// KeyPath is where this computer's key is kept, in dir.
func KeyPath(dir string) string { return filepath.Join(dir, "secret.key") }

// Key is this computer's, made the first time one is needed when create is set.
func Key(dir string, create bool) ([]byte, error) {
	path := KeyPath(dir)
	text, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) && create {
		text, err = newKey(path)
	}
	if err != nil {
		return nil, err
	}
	key, err := hex.DecodeString(string(text))
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("%s is not a key dv made", path)
	}
	return key, nil
}

// newKey writes a new key at path, unless another dv got there first, whose
// key it then is.
func newKey(path string) ([]byte, error) {
	key := make([]byte, 32)
	rand.Read(key)
	text := []byte(hex.EncodeToString(key))
	err := WritePrivate(path, text, false)
	if errors.Is(err, fs.ErrExist) {
		return os.ReadFile(path)
	}
	return text, err
}

// Seal seals value with key, as what it is - "dv telegram token" - so that it
// opens as nothing else.
func Seal(key []byte, value, what string) (string, error) {
	aead, err := gcm(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	rand.Read(nonce)
	return base64.StdEncoding.EncodeToString(aead.Seal(nonce, nonce, []byte(value), []byte(what))), nil
}

func Open(key []byte, sealed, what string) (string, error) {
	aead, err := gcm(key)
	if err != nil {
		return "", err
	}
	b, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil || len(b) < aead.NonceSize() {
		return "", errors.New("that is not sealed as dv seals it")
	}
	value, err := aead.Open(nil, b[:aead.NonceSize()], b[aead.NonceSize():], []byte(what))
	return string(value), err
}

func gcm(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// WritePrivate writes a file only its owner can read, whole or not at all.
// Not to replace one, it fails with fs.ErrExist if the file is there.
func WritePrivate(path string, b []byte, replace bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if !replace {
		return os.Link(tmp.Name(), path)
	}
	return os.Rename(tmp.Name(), path)
}
