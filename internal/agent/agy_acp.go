package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// AgyPrefix is what an Antigravity model's id starts with in the picker; the
// rest is agy's own name for it.
const AgyPrefix = "agy/"

// IsAgyModel reports whether a model is one agy runs, in a session of its own.
func IsAgyModel(model string) bool { return strings.HasPrefix(model, AgyPrefix) }

// configOption is one of a session's settings as ACP's session/new and
// session/set_config_option report them: the model, the thinking level, the
// mode.
type configOption struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Category     string          `json:"category"`
	Type         string          `json:"type"`
	CurrentValue json.RawMessage `json:"currentValue"`
	Options      json.RawMessage `json:"options"`
}

type choiceValue struct {
	Value string `json:"value"`
	Name  string `json:"name"`
}

// current is the option's value, as a string.
func (o configOption) current() string {
	var s string
	json.Unmarshal(o.CurrentValue, &s)
	return s
}

// choices are the values a select offers, flattened out of any groups.
func (o configOption) choices() []choiceValue {
	var raw []json.RawMessage
	json.Unmarshal(o.Options, &raw)
	var out []choiceValue
	for _, r := range raw {
		var g struct {
			Value   *string         `json:"value"`
			Name    string          `json:"name"`
			Options json.RawMessage `json:"options"`
		}
		if json.Unmarshal(r, &g) != nil {
			continue
		}
		if g.Value != nil {
			out = append(out, choiceValue{*g.Value, g.Name})
		} else if len(g.Options) > 0 {
			out = append(out, configOption{Options: g.Options}.choices()...)
		}
	}
	return out
}

func (o configOption) offers(value string) bool {
	return slices.ContainsFunc(o.choices(), func(c choiceValue) bool { return c.Value == value })
}

// optionFor finds a session's option by category, or by id where agy leaves
// the category out.
func optionFor(opts []configOption, category, id string) (configOption, bool) {
	for _, o := range opts {
		if o.Category == category {
			return o, true
		}
	}
	for _, o := range opts {
		if o.ID == id {
			return o, true
		}
	}
	return configOption{}, false
}

const (
	modelCategory    = "model"
	thinkingCategory = "thought_level"
	modeCategory     = "mode"
)

// agyVersion reads agy's version out of what initialize says of it:
// "1.5.12", or "agy_acp_server_1.1.1" from older ones.
var agyVersion = regexp.MustCompile(`^(?:agy_acp_server_)?(\d+)\.(\d+)\.(\d+)`)

// agyTooOld reports a version before 1.5, whose ACP server dv does not drive:
// it neither steers a running turn nor resumes without replaying.
func agyTooOld(version string) bool {
	m := agyVersion.FindStringSubmatch(version)
	if m == nil {
		return false
	}
	var major, minor int
	fmt.Sscan(m[1], &major)
	fmt.Sscan(m[2], &minor)
	return major < 1 || major == 1 && minor < 5
}

// agySignInNeeded reports an error that means agy has no Google sign-in to
// use: ACP's "auth required", or agy finding no browser to sign in with.
func agySignInNeeded(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "Authentication required") || strings.Contains(s, "No browser is available") ||
		strings.Contains(s, "authentication required") || strings.Contains(s, "agy acp --login")
}

// errAgySignIn is what a session says when agy has no sign-in to use.
var errAgySignIn = fmt.Errorf("Antigravity isn't signed in: run agy in a terminal and sign in with your Google account, then send again")

// agyEnv is agy's environment: dv's, with a certificate bundle named where
// one isn't, as the Python agy bundles finds none of its own on some Linux
// systems, and output unbuffered so each message comes as it is written.
func agyEnv() []string {
	env := append(os.Environ(), "PYTHONUNBUFFERED=1")
	if os.Getenv("SSL_CERT_FILE") == "" && os.Getenv("SSL_CERT_DIR") == "" {
		for _, f := range []string{os.Getenv("NIX_SSL_CERT_FILE"), "/etc/ssl/certs/ca-bundle.crt", "/etc/ssl/certs/ca-certificates.crt", "/etc/pki/tls/certs/ca-bundle.crt", "/etc/ssl/cert.pem"} {
			if f == "" {
				continue
			}
			if _, err := os.Stat(f); err == nil {
				env = append(env, "SSL_CERT_FILE="+f)
				break
			}
		}
	}
	return env
}

// agySignInLink is the line agy prints on stdout, outside the protocol, when
// it wants a browser to sign in.
const agySignInLink = "Open the following link to authenticate"

// AgyAvailable reports whether there is an agy to run.
func AgyAvailable() bool { return agyPath() != "" }

// agyBinary overrides where agy is found, for tests.
var agyBinary string

func agyPath() string {
	if agyBinary != "" {
		return agyBinary
	}
	if p, err := exec.LookPath("agy"); err == nil {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".local", "bin", "agy")
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

// agyHandshake is what initialize told of agy.
type agyHandshake struct {
	ProtocolVersion   int `json:"protocolVersion"`
	AgentCapabilities struct {
		LoadSession        bool `json:"loadSession"`
		PromptCapabilities struct {
			Image bool `json:"image"`
		} `json:"promptCapabilities"`
		SessionCapabilities struct {
			Resume *struct{} `json:"resume"`
			Close  *struct{} `json:"close"`
		} `json:"sessionCapabilities"`
		Meta struct {
			Antigravity struct {
				SystemPrompt struct {
					Append bool `json:"append"`
				} `json:"systemPrompt"`
			} `json:"antigravity"`
		} `json:"_meta"`
	} `json:"agentCapabilities"`
	AgentInfo struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"agentInfo"`
}

func initializeParams() map[string]any {
	return map[string]any{
		"protocolVersion": 1,
		// No file system or terminal of the client's: agy reads, writes and
		// runs commands itself, and edits come as diffs dv can show.
		"clientCapabilities": map[string]any{
			"fs":       map[string]bool{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
		},
		"clientInfo": map[string]any{"name": "dv", "title": "dv", "version": "1"},
	}
}

// handshake initializes agy and signs it in with the user's Google account,
// which agy keeps from its own sign-in.
func handshake(ctx context.Context, call func(context.Context, string, any, any) error) (agyHandshake, error) {
	var h agyHandshake
	if err := call(ctx, "initialize", initializeParams(), &h); err != nil {
		return h, fmt.Errorf("agy didn't start: %w", err)
	}
	if agyTooOld(h.AgentInfo.Version) {
		return h, fmt.Errorf("Antigravity CLI %s is too old for dv, which needs 1.5 or newer: run agy update", h.AgentInfo.Version)
	}
	if err := call(ctx, "authenticate", map[string]any{"methodId": "oauth-personal"}, nil); agySignInNeeded(err) {
		return h, errAgySignIn
	}
	// Any other refusal is left for session/new to say more clearly.
	return h, nil
}
