package gateway

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
)

// ModelPrefix marks a model the gateway serves from the user's Codex
// (ChatGPT) sign-in: codex/gpt-6-sol.
const ModelPrefix = "codex/"

const codexBackend = "https://chatgpt.com/backend-api/codex"

// The backend serves its newest models only to a client naming a Codex this
// recent, when the installed one can't be asked.
const fallbackCodexVersion = "0.170.0"

var errNotSignedIn = errors.New("Codex is not signed in with ChatGPT here: run `codex login`")

type codexUpstream struct {
	base   string
	client *http.Client
	home   string

	versionOnce sync.Once
	version     string

	refresh sync.Mutex

	mu         sync.Mutex
	models     []CodexModel
	modelsAt   time.Time
	modelsErr  error
	asking     bool
	turnStates map[string]turnState
}

type turnState struct {
	value string
	at    time.Time
}

// CodexModel is a model the Codex sign-in offers.
type CodexModel struct {
	Slug    string
	Label   string
	Window  int
	Efforts []string // those Claude Code can ask for
	Levels  []string // all the model takes
	Default string
}

func newCodexUpstream() *codexUpstream {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(h, ".codex")
		}
	}
	// HTTP/1.1, as Codex asks: shared HTTP/2 streams were reset mid-reply.
	t := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		MaxIdleConns:        16,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		TLSClientConfig:     &tls.Config{NextProtos: []string{"http/1.1"}},
	}
	t.Protocols = new(http.Protocols)
	t.Protocols.SetHTTP1(true)
	return &codexUpstream{base: codexBackend, client: &http.Client{Transport: t}, home: home, turnStates: map[string]turnState{}}
}

// codexVersion is the Codex dv says it is: the newest of the installed one,
// the one its model cache was last fetched by, and a floor, since the
// backend answers an older one's request for a newer model as not found.
func (c *codexUpstream) codexVersion() string {
	c.versionOnce.Do(func() {
		c.version = fallbackCodexVersion
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "codex", "--version").Output(); err == nil {
			if f := strings.Fields(string(out)); len(f) > 0 {
				c.version = newerVersion(c.version, f[len(f)-1])
			}
		}
		var cache struct {
			Version string `json:"client_version"`
		}
		if b, err := os.ReadFile(filepath.Join(c.home, "models_cache.json")); err == nil && json.Unmarshal(b, &cache) == nil {
			c.version = newerVersion(c.version, cache.Version)
		}
	})
	return c.version
}

func newerVersion(a, b string) string {
	parse := func(v string) []int {
		var out []int
		for _, p := range strings.Split(strings.TrimPrefix(v, "v"), ".") {
			n, err := strconv.Atoi(p)
			if err != nil {
				return nil
			}
			out = append(out, n)
		}
		return out
	}
	pa, pb := parse(a), parse(b)
	if pb == nil {
		return a
	}
	if pa == nil || slices.Compare(pb, pa) > 0 {
		return b
	}
	return a
}

type codexAuth struct {
	Tokens *struct {
		Access  string `json:"access_token"`
		Account string `json:"account_id"`
	} `json:"tokens"`
}

func (c *codexUpstream) readAuth() (token, account string, exp time.Time, err error) {
	// Codex rewrites the file in place as it refreshes, so a read in the
	// middle finds it cut short: that is read again, not taken as signed out.
	for try := 0; ; try++ {
		b, err := os.ReadFile(filepath.Join(c.home, "auth.json"))
		if errors.Is(err, os.ErrNotExist) {
			return "", "", exp, errNotSignedIn
		}
		if err != nil {
			return "", "", exp, err
		}
		var a codexAuth
		if json.Unmarshal(b, &a) != nil {
			if try < 5 {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			return "", "", exp, errors.New("Codex's auth.json does not read as JSON")
		}
		if a.Tokens == nil || a.Tokens.Access == "" {
			return "", "", exp, errNotSignedIn
		}
		return a.Tokens.Access, a.Tokens.Account, jwtExpiry(a.Tokens.Access), nil
	}
}

func jwtExpiry(tok string) time.Time {
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
		return time.Time{}
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	json.Unmarshal(b, &claims)
	if claims.Exp == 0 {
		return time.Time{}
	}
	return time.Unix(claims.Exp, 0)
}

// auth is the sign-in's token, refreshed first when it is about to run out.
// Codex refreshes it: a refresh token works once, so one dv spent would sign
// the user's Codex out.
func (c *codexUpstream) auth(ctx context.Context, force bool) (string, string, error) {
	tok, acct, exp, err := c.readAuth()
	if err != nil {
		return "", "", err
	}
	if !force && (exp.IsZero() || time.Until(exp) > 5*time.Minute) {
		return tok, acct, nil
	}
	c.refresh.Lock()
	defer c.refresh.Unlock()
	if t2, a2, exp2, err := c.readAuth(); err == nil && t2 != tok && time.Until(exp2) > 5*time.Minute {
		return t2, a2, nil // another request had it refreshed
	}
	if err := refreshThroughCodex(ctx); err != nil {
		return "", "", fmt.Errorf("Codex could not refresh its sign-in: %w", err)
	}
	tok, acct, _, err = c.readAuth()
	return tok, acct, err
}

func (c *codexUpstream) sign(h http.Header, tok, acct string) {
	ver := c.codexVersion()
	h.Set("Authorization", "Bearer "+tok)
	if acct != "" {
		h.Set("chatgpt-account-id", acct)
	}
	h.Set("originator", "codex_cli_rs")
	h.Set("version", ver)
	h.Set("User-Agent", "codex_cli_rs/"+ver+" ("+osName()+"; "+archName()+") dv")
}

func osName() string {
	switch runtime.GOOS {
	case "darwin":
		return "Mac OS"
	case "windows":
		return "Windows"
	}
	return "Linux"
}

func archName() string {
	if runtime.GOARCH == "amd64" {
		return "x86_64"
	}
	return runtime.GOARCH
}

// Models are those the sign-in offers, asked again every few minutes.
func (c *codexUpstream) Models(ctx context.Context) ([]CodexModel, error) {
	c.mu.Lock()
	if time.Since(c.modelsAt) < 10*time.Minute {
		defer c.mu.Unlock()
		return c.models, c.modelsErr
	}
	c.mu.Unlock()
	models, err := c.fetchModels(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.modelsAt = time.Now()
	if err != nil && c.models != nil {
		return c.models, nil
	}
	c.models, c.modelsErr = models, err
	return models, err
}

// knownModels are the models last heard of, without waiting: a list gone
// stale is asked for again behind it.
func (c *codexUpstream) knownModels() []CodexModel {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.modelsAt) >= 10*time.Minute && !c.asking {
		c.asking = true
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			c.Models(ctx)
			c.mu.Lock()
			c.asking = false
			c.mu.Unlock()
		}()
	}
	return c.models
}

func (c *codexUpstream) fetchModels(ctx context.Context) ([]CodexModel, error) {
	tok, acct, err := c.auth(ctx, false)
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/models?client_version="+c.codexVersion(), nil)
	c.sign(req.Header, tok, acct)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the Codex model list answered %s", resp.Status)
	}
	var list struct {
		Models []listedModel `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	slices.SortStableFunc(list.Models, func(a, b listedModel) int { return a.Priority - b.Priority })
	var out []CodexModel
	for _, m := range list.Models {
		if m.Visibility != "list" {
			continue
		}
		cm := CodexModel{Slug: m.Slug, Label: m.Display, Window: m.Window, Default: m.Default}
		for _, l := range m.Levels {
			cm.Levels = append(cm.Levels, l.Effort)
			if slices.Contains(claudeEfforts, l.Effort) {
				cm.Efforts = append(cm.Efforts, l.Effort)
			}
		}
		out = append(out, cm)
	}
	return out, nil
}

type listedModel struct {
	Slug       string `json:"slug"`
	Display    string `json:"display_name"`
	Visibility string `json:"visibility"`
	Window     int    `json:"context_window"`
	Default    string `json:"default_reasoning_level"`
	Levels     []struct {
		Effort string `json:"effort"`
	} `json:"supported_reasoning_levels"`
	Priority int `json:"priority"`
}

func (c *codexUpstream) levelsOf(ctx context.Context, slug string) []string {
	models, _ := c.Models(ctx)
	for _, m := range models {
		if m.Slug == slug {
			return m.Levels
		}
	}
	return nil
}

// cacheKey names a conversation to the backend, which finds its cached
// prompt by it. A subagent shares its session's id but not its prompt, and
// two alike running at once are told apart by their agent's id.
func cacheKey(session, agent, instructions string) string {
	sum := sha256.Sum256([]byte(agent + "\x00" + instructions))
	if session == "" {
		return "dv-" + hex.EncodeToString(sum[:])[:24]
	}
	return session + "-" + hex.EncodeToString(sum[:])[:8]
}

func sessionOf(r *http.Request, req *anthropicRequest) string {
	if s := r.Header.Get("X-Claude-Code-Session-Id"); s != "" {
		return s
	}
	if req.Metadata != nil {
		var u struct {
			Session string `json:"session_id"`
		}
		if json.Unmarshal([]byte(req.Metadata.UserID), &u) == nil {
			return u.Session
		}
	}
	return ""
}

// turnState is what the backend's first reply of a turn said, sent back with
// the turn's later requests so it serves them as that turn.
func (c *codexUpstream) turnState(key string, continues bool) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, s := range c.turnStates {
		if time.Since(s.at) > time.Hour {
			delete(c.turnStates, k)
		}
	}
	if !continues {
		delete(c.turnStates, key)
		return ""
	}
	return c.turnStates[key].value
}

func (c *codexUpstream) heardTurnState(key, value string) {
	if value == "" {
		return
	}
	c.mu.Lock()
	c.turnStates[key] = turnState{value, time.Now()}
	c.mu.Unlock()
}

// serve answers a Messages request with a Codex model.
func (c *codexUpstream) serve(w http.ResponseWriter, r *http.Request, body []byte, m *masker) {
	var req anthropicRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	slug := strings.TrimPrefix(req.Model, ModelPrefix)
	key := cacheKey(sessionOf(r, &req), r.Header.Get("X-Claude-Code-Agent-Id"), instructions(req.System, slug))
	payload, names := codexRequest(&req, slug, key, c.levelsOf(r.Context(), slug))
	if strings.HasSuffix(r.URL.Path, "/count_tokens") {
		json.NewEncoder(w).Encode(map[string]int{"input_tokens": estimateTokens(payload)})
		return
	}

	resp, err := c.post(r.Context(), payload, key, continuesTurn(&req))
	if err != nil {
		if errors.Is(err, errNotSignedIn) {
			apiError{http.StatusForbidden, "permission_error", err.Error(), true}.write(w)
		} else if !errors.Is(err, context.Canceled) {
			apiError{http.StatusBadGateway, "api_error", "Codex: " + err.Error(), false}.write(w)
		}
		return
	}
	defer resp.Body.Close()
	c.heardTurnState(key, resp.Header.Get("x-codex-turn-state"))
	if resp.StatusCode != http.StatusOK {
		relayError(w, resp)
		return
	}

	var out sink
	var col *collector
	var held *heldStream
	if req.Stream {
		held = newHeldStream(w)
		out = held
	} else {
		col = &collector{}
		out = col
	}
	if m.active() {
		out = &unmasking{m: m, next: out, held: map[int]*heldDelta{}}
	}
	s := &codexStream{model: req.Model, names: names, out: out, blocks: map[int]*openBlock{}}
	s.run(resp.Body)
	if held != nil {
		held.close()
		return
	}
	if col.failed != nil {
		col.failed.write(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(col.message())
}

// estimateTokens is about how many tokens a request is, for count_tokens:
// its words and schemas, not the bytes of its pictures.
func estimateTokens(payload map[string]any) int {
	b, _ := json.Marshal(payload)
	return len(dataURL.ReplaceAll(b, nil))/4 + 85*bytes.Count(b, []byte(`"input_image"`))
}

var dataURL = regexp.MustCompile(`data:[a-z/+.-]+;base64,[A-Za-z0-9+/=]+`)

// post sends a request, with Codex's sign-in refreshed and the request sent
// again on a 401, and without its replayed reasoning where the backend can't
// read that: sealed for another sign-in, as after `codex login` to another
// account.
func (c *codexUpstream) post(ctx context.Context, payload map[string]any, key string, continues bool) (*http.Response, error) {
	state := c.turnState(key, continues)
	refreshed, stripped := false, false
	for {
		data, _ := json.Marshal(payload)
		tok, acct, err := c.auth(ctx, refreshed)
		if err != nil {
			return nil, err
		}
		body, err := zstdOf(data)
		if err != nil {
			return nil, err
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/responses", bytes.NewReader(body))
		c.sign(req.Header, tok, acct)
		req.Header.Set("Content-Encoding", "zstd")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("OpenAI-Beta", "responses=experimental")
		req.Header.Set("session-id", key)
		req.Header.Set("thread-id", key)
		if state != "" {
			req.Header.Set("x-codex-turn-state", state)
		}
		resp, err := c.client.Do(req)
		if err != nil {
			return nil, err
		}
		switch {
		case resp.StatusCode == http.StatusUnauthorized && !refreshed:
			resp.Body.Close()
			refreshed = true
			continue
		case resp.StatusCode == http.StatusBadRequest && !stripped:
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
			resp.Body.Close()
			if foreignReasoning.Match(b) {
				payload, stripped = withoutReasoning(payload), true
				continue
			}
			resp.Body = io.NopCloser(bytes.NewReader(b))
		}
		return resp, nil
	}
}

var zstdEncoder, _ = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithWindowSize(2<<20), zstd.WithEncoderCRC(false))

// zstdOf is a request body as Codex sends one: a long conversation sent
// plain was refused where the same compressed went through.
func zstdOf(data []byte) ([]byte, error) { return zstdEncoder.EncodeAll(data, nil), nil }

// relayError passes the backend's refusal on as Anthropic's API words one,
// so Claude Code retries what it would retry, compacts on an overflow, and
// shows the rest.
func relayError(w http.ResponseWriter, resp *http.Response) {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	var e struct {
		Error struct {
			Type     string  `json:"type"`
			Code     string  `json:"code"`
			Message  string  `json:"message"`
			ResetsAt float64 `json:"resets_at"`
			ResetsIn float64 `json:"resets_in_seconds"`
			PlanType string  `json:"plan_type"`
		} `json:"error"`
		Detail string `json:"detail"`
	}
	json.Unmarshal(b, &e)
	code := cmp.Or(e.Error.Code, e.Error.Type)
	msg := cmp.Or(e.Error.Message, e.Detail, strings.TrimSpace(string(b)), resp.Status)
	if code == "usage_limit_reached" {
		msg = "your ChatGPT plan's Codex usage limit is reached"
		if e.Error.ResetsAt > 0 {
			msg += "; it resets " + time.Unix(int64(e.Error.ResetsAt), 0).Format("Mon 15:04")
		}
	}
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		w.Header().Set("Retry-After", ra)
	}
	classify(resp.StatusCode, code, msg).write(w)
}

func writeError(w http.ResponseWriter, status int, kind, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": map[string]any{"type": kind, "message": msg}})
}
