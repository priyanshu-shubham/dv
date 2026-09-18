package telegram

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

// The token is kept sealed with a key made for this computer and kept apart
// from dv's settings, so a copy of those - in a backup, a dotfiles repository,
// pasted somewhere - carries no working token. Anything running as the user
// can still read both.

// sealedAs binds a sealed value to what it is, so it opens as nothing else.
var sealedAs = []byte("dv telegram token")

func (t *Telegram) keyPath() string { return filepath.Join(t.keyDir, "secret.key") }

// key is this computer's, made the first time one is needed when create is
// set. Callers hold t.mu.
func (t *Telegram) key(create bool) ([]byte, error) {
	if t.secret != nil {
		return t.secret, nil
	}
	text, err := os.ReadFile(t.keyPath())
	if errors.Is(err, fs.ErrNotExist) && create {
		text, err = newKey(t.keyPath())
	}
	if err != nil {
		return nil, err
	}
	key, err := hex.DecodeString(string(text))
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("%s is not a key dv made", t.keyPath())
	}
	t.secret = key
	return key, nil
}

// newKey writes a new key at path, unless another dv got there first, whose
// key it then is.
func newKey(path string) ([]byte, error) {
	key := make([]byte, 32)
	rand.Read(key)
	text := []byte(hex.EncodeToString(key))
	err := writePrivate(path, text, false)
	if errors.Is(err, fs.ErrExist) {
		return os.ReadFile(path)
	}
	return text, err
}

func seal(key []byte, token string) (string, error) {
	aead, err := gcm(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	rand.Read(nonce)
	return base64.StdEncoding.EncodeToString(aead.Seal(nonce, nonce, []byte(token), sealedAs)), nil
}

func unseal(key []byte, sealed string) (string, error) {
	aead, err := gcm(key)
	if err != nil {
		return "", err
	}
	b, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil || len(b) < aead.NonceSize() {
		return "", errors.New("the token is not sealed as dv seals it")
	}
	token, err := aead.Open(nil, b[:aead.NonceSize()], b[aead.NonceSize():], sealedAs)
	return string(token), err
}

func gcm(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
