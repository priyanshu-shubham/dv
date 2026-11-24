package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"dv/internal/acp"
	"dv/internal/store"
)

// The models agy offers the user's Google account are known only from a
// session: the model option's choices, and each model's thinking levels once
// it is picked. dv keeps what it last heard, from a session opened for the
// purpose now and then, and from every session dv runs.

// AgyOff says whether the user switched Antigravity's models out of the
// picker. Set once, as dv starts.
var AgyOff func() bool

type agyModel struct {
	Value   string   `json:"value"`
	Name    string   `json:"name"`
	Efforts []string `json:"efforts,omitempty"`
	Effort  string   `json:"effort,omitempty"`
	// Window is the context agy last said the model has, which it says only
	// as a session runs.
	Window int `json:"window,omitempty"`
}

type agyCatalog struct {
	Checked time.Time  `json:"checked"`
	Version string     `json:"version,omitempty"`
	Models  []agyModel `json:"models"`
}

// AgyInfo is how Antigravity stands, for Settings.
type AgyInfo struct {
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	// Probing while agy is being asked; SignIn when it has no Google sign-in.
	Probing bool      `json:"probing,omitempty"`
	SignIn  bool      `json:"signIn,omitempty"`
	Error   string    `json:"error,omitempty"`
	Models  int       `json:"models"`
	Checked time.Time `json:"checked,omitzero"`
}

var agyState struct {
	sync.Mutex
	loaded  bool
	cat     agyCatalog
	probing bool
	signIn  bool
	err     string
}

// agyFresh is how long the models last heard stand before agy is asked again.
const agyFresh = 24 * time.Hour

func agyCatalogPath() (string, error) {
	dir, err := store.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "agy", "models.json"), nil
}

func loadAgyLocked() {
	if agyState.loaded {
		return
	}
	agyState.loaded = true
	if path, err := agyCatalogPath(); err == nil {
		if b, err := os.ReadFile(path); err == nil {
			json.Unmarshal(b, &agyState.cat)
		}
	}
}

func saveAgyLocked() {
	path, err := agyCatalogPath()
	if err != nil {
		return
	}
	b, _ := json.MarshalIndent(agyState.cat, "", "  ")
	if os.MkdirAll(filepath.Dir(path), 0o700) == nil {
		os.WriteFile(path, b, 0o600)
	}
}

func agyOff() bool { return AgyOff != nil && AgyOff() }

// AgyModels are Antigravity's models for the picker, asking agy again apart
// when what was heard is old. None while it is switched off or not installed.
func AgyModels() []Model {
	if agyOff() || !AgyAvailable() {
		return nil
	}
	agyState.Lock()
	defer agyState.Unlock()
	loadAgyLocked()
	if time.Since(agyState.cat.Checked) > agyFresh && !agyState.probing && !agyState.signIn && agyState.err == "" {
		agyState.probing = true
		go probeAgy()
	}
	var out []Model
	for _, m := range agyState.cat.Models {
		desc := "Antigravity, with your Google sign-in"
		if m.Window > 0 {
			desc += fmt.Sprintf(" · %dk context", m.Window/1000)
		}
		out = append(out, Model{
			ID: AgyPrefix + m.Value, Model: AgyPrefix + m.Value, Label: m.Name,
			Description: desc, Efforts: m.Efforts, Effort: m.Effort, Source: "Antigravity",
		})
	}
	return out
}

// AgyStatus is how Antigravity stands; recheck asks agy again.
func AgyStatus(recheck bool) AgyInfo {
	info := AgyInfo{Installed: AgyAvailable()}
	if !info.Installed {
		return info
	}
	agyState.Lock()
	defer agyState.Unlock()
	loadAgyLocked()
	if recheck && !agyState.probing && !agyOff() {
		agyState.probing, agyState.err, agyState.signIn = true, "", false
		go probeAgy()
	}
	info.Version, info.Models, info.Checked = agyState.cat.Version, len(agyState.cat.Models), agyState.cat.Checked
	info.Probing, info.SignIn, info.Error = agyState.probing, agyState.signIn, agyState.err
	return info
}

// agyHeard keeps the models a session's options list, and the thinking
// levels of the one it is on.
func agyHeard(opts []configOption) {
	model, ok := optionFor(opts, modelCategory, "model")
	if !ok {
		return
	}
	thinking, _ := optionFor(opts, thinkingCategory, "thinking_level")
	agyState.Lock()
	defer agyState.Unlock()
	loadAgyLocked()
	agyState.cat.Models = mergeModels(agyState.cat.Models, model, thinking)
	agyState.signIn, agyState.err = false, ""
	saveAgyLocked()
}

// agyHeardWindow keeps the context a model has, as a session on it reports.
func agyHeardWindow(model string, size int) {
	agyState.Lock()
	defer agyState.Unlock()
	loadAgyLocked()
	for i, m := range agyState.cat.Models {
		if AgyPrefix+m.Value == model && m.Window != size {
			agyState.cat.Models[i].Window = size
			saveAgyLocked()
		}
	}
}

// agyWindow is the context a model has, 0 until a session on it has said.
func agyWindow(model string) int {
	agyState.Lock()
	defer agyState.Unlock()
	loadAgyLocked()
	for _, m := range agyState.cat.Models {
		if AgyPrefix+m.Value == model {
			return m.Window
		}
	}
	return 0
}

// mergeModels is the list a model option offers, each keeping the thinking
// levels heard before, and the current one taking those now offered.
func mergeModels(was []agyModel, model, thinking configOption) []agyModel {
	var out []agyModel
	for _, c := range model.choices() {
		m := agyModel{Value: c.Value, Name: c.Name}
		if i := slices.IndexFunc(was, func(w agyModel) bool { return w.Value == c.Value }); i >= 0 {
			m.Efforts, m.Effort, m.Window = was[i].Efforts, was[i].Effort, was[i].Window
		}
		if c.Value == model.current() && thinking.ID != "" {
			m.Efforts, m.Effort = nil, thinking.current()
			for _, t := range thinking.choices() {
				m.Efforts = append(m.Efforts, t.Value)
			}
		}
		if m.Name == "" {
			m.Name = c.Value
		}
		out = append(out, m)
	}
	return out
}

// probeAgy opens a session in an empty folder of its own to hear the models
// and, picking each in turn, their thinking levels. No prompt is sent, so no
// model is called.
func probeAgy() {
	cat, err := askAgy()
	agyState.Lock()
	defer agyState.Unlock()
	agyState.probing = false
	agyState.signIn = errors.Is(err, errAgySignIn)
	agyState.err = ""
	if err != nil && !agyState.signIn {
		agyState.err = err.Error()
	}
	if err == nil {
		agyState.cat = cat
	} else {
		// Not asked again until Settings asks, or dv restarts.
		agyState.cat.Checked = time.Now()
	}
	saveAgyLocked()
}

type agyRefuser struct{ signIn chan struct{} }

func (r agyRefuser) Request(ctx context.Context, method string, params json.RawMessage) (any, error) {
	if method == "session/request_permission" {
		return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}, nil
	}
	return nil, &acp.Error{Code: -32601, Message: "dv does not offer " + method}
}

func (r agyRefuser) Notify(string, json.RawMessage) {}

func (r agyRefuser) Stray(line []byte) {
	if len(line) >= len(agySignInLink) && string(line[:len(agySignInLink)]) == agySignInLink {
		select {
		case r.signIn <- struct{}{}:
		default:
		}
	}
}

func askAgy() (agyCatalog, error) {
	cat := agyCatalog{Checked: time.Now()}
	dir, err := os.MkdirTemp("", "dv-agy-")
	if err != nil {
		return cat, err
	}
	defer os.RemoveAll(dir)
	cmd := exec.Command(agyPath(), "acp")
	cmd.Dir, cmd.Env = dir, agyEnv()
	ownGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return cat, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return cat, err
	}
	if err := cmd.Start(); err != nil {
		return cat, err
	}
	defer func() {
		stdin.Close()
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			killGroup(cmd)
			<-done
		}
	}()
	ref := agyRefuser{signIn: make(chan struct{}, 1)}
	conn := acp.New(stdin, stdout, ref)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	go func() {
		select {
		case <-ref.signIn:
			cancel()
		case <-ctx.Done():
		}
	}()
	hs, err := handshake(ctx, conn.Call)
	if ctx.Err() == context.Canceled {
		return cat, errAgySignIn
	}
	if err != nil {
		return cat, err
	}
	cat.Version = hs.AgentInfo.Version
	var r struct {
		SessionID     string         `json:"sessionId"`
		ConfigOptions []configOption `json:"configOptions"`
	}
	if err := conn.Call(ctx, "session/new", map[string]any{"cwd": dir, "mcpServers": []any{}}, &r); err != nil {
		if agySignInNeeded(err) || ctx.Err() == context.Canceled {
			return cat, errAgySignIn
		}
		return cat, err
	}
	model, ok := optionFor(r.ConfigOptions, modelCategory, "model")
	if !ok {
		return cat, errors.New("agy offered no models")
	}
	thinking, _ := optionFor(r.ConfigOptions, thinkingCategory, "thinking_level")
	cat.Models = mergeModels(nil, model, thinking)
	first := model.current()
	for i, m := range cat.Models {
		if m.Value == first {
			continue
		}
		var set struct {
			ConfigOptions []configOption `json:"configOptions"`
		}
		if conn.Call(ctx, "session/set_config_option", map[string]any{"sessionId": r.SessionID, "configId": model.ID, "value": m.Value}, &set) != nil {
			continue
		}
		if t, ok := optionFor(set.ConfigOptions, thinkingCategory, "thinking_level"); ok {
			cat.Models[i].Effort = t.current()
			for _, c := range t.choices() {
				cat.Models[i].Efforts = append(cat.Models[i].Efforts, c.Value)
			}
		}
	}
	if hs.AgentCapabilities.SessionCapabilities.Close != nil {
		conn.Call(ctx, "session/close", map[string]any{"sessionId": r.SessionID}, nil)
	}
	return cat, nil
}
