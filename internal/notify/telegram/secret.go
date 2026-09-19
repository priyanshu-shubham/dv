package telegram

import "dv/internal/secret"

// sealedAs binds a sealed token to what it is, so it opens as nothing else.
const sealedAs = "dv telegram token"

func (t *Telegram) keyPath() string { return secret.KeyPath(t.keyDir) }

// key is this computer's (package secret), made the first time one is needed
// when create is set. Callers hold t.mu.
func (t *Telegram) key(create bool) ([]byte, error) {
	if t.secret != nil {
		return t.secret, nil
	}
	key, err := secret.Key(t.keyDir, create)
	if err == nil {
		t.secret = key
	}
	return key, err
}

func seal(key []byte, token string) (string, error) { return secret.Seal(key, token, sealedAs) }

func unseal(key []byte, sealed string) (string, error) { return secret.Open(key, sealed, sealedAs) }
