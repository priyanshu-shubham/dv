package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"dv/internal/secret"
)

// SecretInfo is what the page is shown of a secret: never its value, only a
// glimpse of it to tell it by.
type SecretInfo struct {
	Name    string `json:"name"`
	Preview string `json:"preview"`
	From    string `json:"from,omitempty"` // the .env file it was read from; saved by the user if none
}

// Redact puts each secret's placeholder in place of its value in b, as what
// is sent to a model has it, for the page to show the secret as a chip.
func (g *Gateway) Redact(b []byte) []byte {
	g.refreshEnv()
	return g.mask.mask(b)
}

// RedactText is Redact for words that go out of the page: a notice, a chat
// app's message.
func (g *Gateway) RedactText(s string) string { return string(g.Redact([]byte(s))) }

// SecretValue is a secret's value, for the page to show where asked.
func (g *Gateway) SecretValue(name string) (string, bool) {
	g.mask.mu.RLock()
	defer g.mask.mu.RUnlock()
	v, ok := g.mask.byName[name]
	return v, ok
}

// preview is a glimpse of a secret: its first few characters and its last,
// where it is long enough to spare them.
func preview(v string) string {
	switch {
	case len(v) >= 20:
		return v[:4] + "••••" + v[len(v)-4:]
	case len(v) >= 12:
		return v[:2] + "••••" + v[len(v)-2:]
	}
	return "••••••"
}

type secretsFile struct {
	Secrets []sealedSecret `json:"secrets"`
}

type sealedSecret struct {
	Name   string `json:"name"`
	Sealed string `json:"sealed"`
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
	g.saved.Store(&values)
	g.remask()
	return nil
}

// Secrets are the names of those masked, and a glimpse of each: the user's,
// then those read from .env files.
func (g *Gateway) Secrets() ([]SecretInfo, error) {
	g.refreshEnv()
	g.mu.Lock()
	f, err := g.read()
	g.mu.Unlock()
	out := []SecretInfo{}
	saved := map[string]bool{}
	for _, s := range f.Secrets {
		v, _ := g.SecretValue(s.Name)
		out = append(out, SecretInfo{Name: s.Name, Preview: preview(v)})
		saved[s.Name] = true
	}
	g.mask.mu.RLock()
	masked := maps.Clone(g.mask.byName)
	g.mask.mu.RUnlock()
	var env []SecretInfo
	for name, v := range masked {
		if !saved[name] {
			env = append(env, SecretInfo{Name: name, Preview: preview(v), From: g.envFromOf(name)})
		}
	}
	slices.SortFunc(env, func(a, b SecretInfo) int { return strings.Compare(a.From+a.Name, b.From+b.Name) })
	return append(out, env...), err
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
	f.Secrets = append(f.Secrets, sealedSecret{name, sealed})
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
