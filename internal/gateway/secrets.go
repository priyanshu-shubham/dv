package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"dv/internal/secret"
)

// SecretInfo is what the page is shown of a secret: never its value.
type SecretInfo struct {
	Name string `json:"name"`
	Hint string `json:"hint"`
}

type secretsFile struct {
	Secrets []sealedSecret `json:"secrets"`
}

type sealedSecret struct {
	Name   string `json:"name"`
	Sealed string `json:"sealed"`
	Hint   string `json:"hint"`
}

func sealedAs(name string) string { return "dv masked secret " + name }

func (g *Gateway) read() (secretsFile, error) {
	var f secretsFile
	b, err := os.ReadFile(g.path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	return f, json.Unmarshal(b, &f)
}

func (g *Gateway) load() error {
	f, err := g.read()
	if err != nil {
		return err
	}
	values := map[string]string{}
	if len(f.Secrets) > 0 {
		key, err := secret.Key(g.keyDir, false)
		if err != nil {
			return err
		}
		for _, s := range f.Secrets {
			v, err := secret.Open(key, s.Sealed, sealedAs(s.Name))
			if err != nil {
				return fmt.Errorf("secret %s does not open with this computer's key: %w", s.Name, err)
			}
			values[s.Name] = v
		}
	}
	g.mask.set(values)
	return nil
}

// Secrets are the names of those masked, and a hint of each.
func (g *Gateway) Secrets() ([]SecretInfo, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	f, err := g.read()
	out := []SecretInfo{}
	for _, s := range f.Secrets {
		out = append(out, SecretInfo{s.Name, s.Hint})
	}
	return out, err
}

// SetSecret masks value as name from the next request on, replacing one of
// that name.
func (g *Gateway) SetSecret(name, value string) error {
	value = strings.TrimSpace(value)
	if !NameRe.MatchString(name) {
		return errors.New("a name is letters, digits and _, up to 64 of them")
	}
	if len(value) < MinSecret {
		return fmt.Errorf("a secret is at least %d characters, so ordinary text isn't taken for one", MinSecret)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	f, err := g.read()
	if err != nil {
		return err
	}
	key, err := secret.Key(g.keyDir, true)
	if err != nil {
		return err
	}
	sealed, err := secret.Seal(key, value, sealedAs(name))
	if err != nil {
		return err
	}
	f.Secrets = slices.DeleteFunc(f.Secrets, func(s sealedSecret) bool { return s.Name == name })
	f.Secrets = append(f.Secrets, sealedSecret{name, sealed, hint(value)})
	return g.write(f)
}

func (g *Gateway) DeleteSecret(name string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	f, err := g.read()
	if err != nil {
		return err
	}
	f.Secrets = slices.DeleteFunc(f.Secrets, func(s sealedSecret) bool { return s.Name == name })
	return g.write(f)
}

func (g *Gateway) write(f secretsFile) error {
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := secret.WritePrivate(g.path, b, true); err != nil {
		return err
	}
	return g.load()
}

// hint shows the end of a long secret, for telling two apart; a short one's
// end would give away too much of it.
func hint(v string) string {
	if len(v) < 16 {
		return fmt.Sprintf("%d characters", len(v))
	}
	return "…" + v[len(v)-4:]
}
