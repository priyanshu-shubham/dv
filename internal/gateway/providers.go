package gateway

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Provider is a model endpoint the user added: its models are served as
// <ID>/<model>, through the API it speaks.
type Provider struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"` // as an OpenAI SDK takes it, or for anthropic, as ANTHROPIC_BASE_URL does
	API  string `json:"api"` // APIChat, APIResponses or APIAnthropic
	Key  string `json:"key,omitempty"`
	// KeyHelper is a shell command that prints the key, for one that runs
	// out: run again once KeyTTL seconds have passed, or the key is refused.
	KeyHelper string   `json:"keyHelper,omitempty"`
	KeyTTL    int      `json:"keyTTL,omitempty"`
	Models    []string `json:"models,omitempty"` // named by hand, for an endpoint that lists none
	Window    int      `json:"window,omitempty"`
}

const (
	APIChat      = "chat"
	APIResponses = "responses"
	APIAnthropic = "anthropic"
)

// OpenAI is the user's OpenAI API key's provider, there while there is a key.
const (
	OpenAIPrefix  = "openai/"
	OpenAIKeyName = "OPENAI_API_KEY"
)

var openAI = Provider{ID: "openai", Name: "OpenAI", URL: "https://api.openai.com/v1", API: APIResponses, Key: OpenAIKeyName}

var (
	providerID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	reservedID = []string{"codex", "gemini", "openai", "claude", "anthropic"}
)

// Check says what is wrong with p as the user gave it, nil if nothing.
func (p Provider) Check() error {
	switch {
	case !providerID.MatchString(p.ID):
		return errors.New("a provider's id is lowercase letters, digits, - and _")
	case slices.Contains(reservedID, p.ID):
		return fmt.Errorf("%s is dv's own; name the provider otherwise", p.ID)
	case !strings.HasPrefix(p.URL, "http://") && !strings.HasPrefix(p.URL, "https://"):
		return errors.New("the URL starts with http:// or https://")
	case p.API != APIChat && p.API != APIResponses && p.API != APIAnthropic:
		return errors.New("the API is chat, responses or anthropic")
	}
	return nil
}

var (
	providerSource  atomic.Pointer[func() []Provider]
	openAIOffSource atomic.Pointer[func() bool]
)

// UseProviders has the gateway read the user's providers from list.
func UseProviders(list func() []Provider) { providerSource.Store(&list) }

// SwitchOpenAI has the gateway read from off whether the user switched OpenAI
// off, which leaves its models unlisted and unanswered whatever key there is.
func SwitchOpenAI(off func() bool) { openAIOffSource.Store(&off) }

func openAIOff() bool {
	f := openAIOffSource.Load()
	return f != nil && (*f)()
}

var errOpenAIOff = errors.New("OpenAI is switched off in dv's Settings, Models")

// providerUpstream answers a provider's models: through the protocol it
// speaks, with the key named, and its models listed from it.
type providerUpstream struct {
	cfg    Provider
	key    func(name string) string
	client *http.Client
	prices *priceList
	codex  func() []Model // the Codex sign-in's, for the levels OpenAI's list doesn't give

	list listing

	helper keyHelper

	mu      sync.Mutex
	refused map[string]bool // parameters the endpoint turned away, by name
}

func (g *Gateway) newProvider(cfg Provider) *providerUpstream {
	cfg.URL = strings.TrimRight(cfg.URL, "/")
	return &providerUpstream{cfg: cfg, key: g.secretOrEnv, client: g.providerClient, prices: g.prices, codex: g.codex.knownModels, refused: map[string]bool{}}
}

// providers are the user's, and OpenAI's while there is a key for it; one
// whose settings are as they were stays as it was, its models known.
func (g *Gateway) providers() []*providerUpstream {
	var cfgs []Provider
	if !openAIOff() && g.secretOrEnv(OpenAIKeyName) != "" {
		cfgs = append(cfgs, openAI)
	}
	if f := providerSource.Load(); f != nil {
		for _, p := range (*f)() {
			if p.Check() == nil {
				cfgs = append(cfgs, p)
			}
		}
	}
	g.provMu.Lock()
	defer g.provMu.Unlock()
	out := make([]*providerUpstream, 0, len(cfgs))
	kept := map[string]*providerUpstream{}
	for _, cfg := range cfgs {
		if _, dup := kept[cfg.ID]; dup {
			continue
		}
		b, _ := json.Marshal(cfg)
		u := g.provs[string(b)]
		if u == nil {
			u = g.newProvider(cfg)
		}
		kept[cfg.ID] = u
		out = append(out, u)
	}
	g.provs = map[string]*providerUpstream{}
	for _, u := range out {
		b, _ := json.Marshal(u.cfg)
		g.provs[string(b)] = u
	}
	return out
}

func (g *Gateway) provider(model string) (*providerUpstream, bool) {
	id, _, ok := strings.Cut(model, "/")
	if !ok {
		return nil, false
	}
	for _, p := range g.providers() {
		if p.cfg.ID == id {
			return p, true
		}
	}
	return nil, false
}

func (p *providerUpstream) prefix() string { return p.cfg.ID + "/" }

func (p *providerUpstream) apiKey() string {
	if p.cfg.KeyHelper != "" {
		k, _ := p.helper.key(p.cfg, "")
		return k
	}
	if p.cfg.Key == "" {
		return ""
	}
	return p.key(p.cfg.Key)
}

// keyMissing is why there is no key for a provider that takes one, nil
// where there is.
func (p *providerUpstream) keyMissing() error {
	if p.cfg.KeyHelper != "" {
		_, err := p.helper.key(p.cfg, "")
		return err
	}
	if p.cfg.Key != "" && p.apiKey() == "" {
		return fmt.Errorf("there is no %s: save it in Settings, Secrets", p.cfg.Key)
	}
	return nil
}

// refreshKey has a helper's key made again, after the endpoint refused the
// one sent with sent; false where there is no helper to ask.
func (p *providerUpstream) refreshKey(sent http.Header) bool {
	if p.cfg.KeyHelper == "" {
		return false
	}
	_, err := p.helper.key(p.cfg, strings.TrimPrefix(sent.Get("Authorization"), "Bearer "))
	return err == nil
}

// listID is what the provider's model list is of: listed again when its
// URL or key changes, but not each time a helper's key is made anew.
func (p *providerUpstream) listID() string {
	if p.cfg.KeyHelper != "" {
		return p.cfg.URL + "\x00helper"
	}
	return p.cfg.URL + "\x00" + p.apiKey()
}

func (p *providerUpstream) failures() failures {
	signIn := "check the provider's URL in Settings, Models"
	switch {
	case p.cfg.KeyHelper != "":
		signIn = "check its key helper in Settings, Models"
	case p.cfg.Key != "":
		signIn = "check " + p.cfg.Key + " in Settings, Secrets"
	}
	return failures{p.cfg.Name, signIn}
}

func (p *providerUpstream) authorize(h http.Header) {
	if k := p.apiKey(); k != "" {
		h.Set("Authorization", "Bearer "+k)
		if p.cfg.API == APIAnthropic {
			h.Set("x-api-key", k)
		}
	}
}

func (p *providerUpstream) knownModels() []Model {
	return p.list.known(p.listID(), p.fetchModels)
}

// failed is why the provider's models couldn't be listed, nil while they are
// being asked for, or were.
func (p *providerUpstream) failed() error {
	if err := p.keyMissing(); err != nil {
		return err
	}
	return p.list.failed(p.listID())
}

func (p *providerUpstream) levels(slug string) []string {
	for _, m := range p.knownModels() {
		if m.Slug == slug {
			return m.Levels
		}
	}
	return nil
}

func (p *providerUpstream) serve(w http.ResponseWriter, r *http.Request, body []byte, m *masker) {
	if err := p.keyMissing(); err != nil {
		apiError{http.StatusForbidden, "permission_error", p.cfg.Name + ": " + err.Error(), true}.write(w)
		return
	}
	switch p.cfg.API {
	case APIChat:
		p.serveChat(w, r, body, m)
	case APIResponses:
		p.serveResponses(w, r, body, m)
	default:
		p.serveAnthropic(w, r, body, m)
	}
}

// listedEntry is a model as the lists of OpenAI, Anthropic, vLLM, OpenRouter,
// LiteLLM and DeepSeek have one: each says what it knows of it its own way.
type listedEntry struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Display string `json:"display_name"`
	Context int    `json:"context_window"`
	Length  int    `json:"context_length"`
	Input   int    `json:"max_input_tokens"`
	MaxLen  int    `json:"max_model_len"`
	Effort  *struct {
		Levels  []string `json:"supported_levels"`
		Default string   `json:"default_level"`
	} `json:"effort"`
}

var (
	// Models a list has that don't answer a coding agent.
	notChat = regexp.MustCompile(`(?i)embed|tts|whisper|dall-e|gpt-image|image|moderation|rerank|transcribe|audio|realtime|speech|sora`)
	// OpenAI's list has every snapshot, and models only its other APIs take.
	openAIChat   = regexp.MustCompile(`^(gpt-|o\d|codex-|chatgpt-)`)
	openAINotFor = regexp.MustCompile(`-\d{4}-\d{2}-\d{2}$|-\d{4}$|instruct|search|deep-research|computer-use|babbage|davinci|-chat-latest$|gpt-3\.5|gpt-4-|gpt-4$`)
)

func (p *providerUpstream) modelsURLs() []string {
	base := p.cfg.URL
	if p.cfg.API != APIAnthropic {
		return []string{base + "/models"}
	}
	urls := []string{base + "/v1/models", base + "/models"}
	// DeepSeek's Anthropic API lists its models only beside its OpenAI one.
	if parent, ok := strings.CutSuffix(base, "/anthropic"); ok {
		urls = append(urls, parent+"/models", parent+"/v1/models")
	}
	return urls
}

func (p *providerUpstream) fetchModels(ctx context.Context) ([]Model, error) {
	var listed []listedEntry
	var lastErr error
	for _, u := range p.modelsURLs() {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		p.authorize(req.Header)
		if p.cfg.API == APIAnthropic {
			req.Header.Set("anthropic-version", "2023-06-01")
		}
		resp, err := p.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		var list struct {
			Data []listedEntry `json:"data"`
		}
		if resp.StatusCode != http.StatusOK || json.Unmarshal(b, &list) != nil {
			_, msg := providerFailure(b)
			lastErr = fmt.Errorf("listing %s's models: %s", p.cfg.Name, cmp.Or(msg, resp.Status))
			if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
				break
			}
			continue
		}
		listed, lastErr = list.Data, nil
		break
	}
	if lastErr != nil && len(p.cfg.Models) == 0 {
		return nil, lastErr
	}
	official := p.cfg.ID == openAI.ID
	info := p.prices.info()
	var out []Model
	seen := map[string]bool{}
	add := func(e listedEntry) {
		if e.ID == "" || seen[e.ID] {
			return
		}
		seen[e.ID] = true
		known, hasInfo := infoOf(info, p.rateKey(e.ID))
		// models.dev's name is the maker's, which a proxy's alias or a local
		// build of the model may not be.
		label := e.ID
		if official {
			label = cmp.Or(known.Name, e.ID)
		}
		m := Model{Slug: e.ID, Label: cmp.Or(e.Display, e.Name, label)}
		m.Window = cmp.Or(p.cfg.Window, e.Context, e.Length, e.Input, e.MaxLen, known.Window, defaultWindow)
		switch {
		case e.Effort != nil && len(e.Effort.Levels) > 0:
			m.Levels, m.Default = e.Effort.Levels, e.Effort.Default
		case official:
			m.Levels, m.Default = p.openAILevels(e.ID, known, hasInfo)
		default:
			m.Levels = []string{"low", "medium", "high"}
		}
		for _, l := range m.Levels {
			if slices.Contains(claudeEfforts, l) {
				m.Efforts = append(m.Efforts, l)
			}
		}
		out = append(out, m)
	}
	// Models named by hand are the whole list, taking what the endpoint's
	// listing says of each: OpenRouter's lists hundreds.
	if len(p.cfg.Models) > 0 {
		for _, id := range p.cfg.Models {
			id = strings.TrimSpace(id)
			if i := slices.IndexFunc(listed, func(e listedEntry) bool { return e.ID == id }); i >= 0 {
				add(listed[i])
			} else {
				add(listedEntry{ID: id})
			}
		}
		return out, nil
	}
	for _, e := range listed {
		if notChat.MatchString(e.ID) || official && (!openAIChat.MatchString(e.ID) || openAINotFor.MatchString(e.ID)) {
			continue
		}
		add(e)
	}
	if official {
		slices.SortStableFunc(out, func(a, b Model) int { return strings.Compare(b.Slug, a.Slug) })
	}
	return out, nil
}

// defaultWindow is a model's whose window nothing tells.
const defaultWindow = 128_000

// openAILevels are an OpenAI model's reasoning levels: the Codex sign-in's
// for one it has too, else none for a model that doesn't reason.
func (p *providerUpstream) openAILevels(slug string, known modelInfo, hasInfo bool) ([]string, string) {
	for _, m := range p.codex() {
		if m.Slug == slug {
			return m.Levels, m.Default
		}
	}
	if hasInfo && !known.Reasoning || !hasInfo && strings.HasPrefix(slug, "gpt-4") {
		return nil, ""
	}
	return []string{"low", "medium", "high"}, ""
}

// rateKey is where models.dev keeps what a provider's model costs: OpenAI's
// own under openai, another's under whoever makes a model of that name.
func (p *providerUpstream) rateKey(slug string) string {
	if p.cfg.ID == openAI.ID {
		return "openai/" + slug
	}
	return anyProvider + bareModel(slug)
}

// bareModel is a model's name without the vendor or route a proxy puts
// ahead of it: deepseek/deepseek-chat is deepseek-chat.
func bareModel(slug string) string {
	return strings.ToLower(slug[strings.LastIndex(slug, "/")+1:])
}

// providerFailure reads an error as OpenAI's API, Anthropic's, or a server
// with neither's words one.
func providerFailure(b []byte) (code, msg string) {
	var e struct {
		Error json.RawMessage `json:"error"`
		Msg   string          `json:"message"`
		Det   json.RawMessage `json:"detail"`
	}
	json.Unmarshal(b, &e)
	var inner struct {
		Type    string          `json:"type"`
		Code    json.RawMessage `json:"code"`
		Message string          `json:"message"`
	}
	if json.Unmarshal(e.Error, &inner) != nil {
		json.Unmarshal(e.Error, &inner.Message)
	}
	json.Unmarshal(inner.Code, &code)
	var detail string
	json.Unmarshal(e.Det, &detail)
	return cmp.Or(code, inner.Type), cmp.Or(inner.Message, e.Msg, detail)
}

func (p *providerUpstream) relayError(w http.ResponseWriter, resp *http.Response, b []byte) {
	code, msg := providerFailure(b)
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		w.Header().Set("Retry-After", ra)
	}
	p.failures().classify(resp.StatusCode, code, cmp.Or(msg, strings.TrimSpace(string(b)), resp.Status)).write(w)
}

// ProviderStatus is what Settings says of a provider: where its key is from
// ("saved", "env", none ""), how many models it offers, and why none where
// they couldn't be listed.
type ProviderStatus struct {
	ID     string `json:"id"`
	Key    string `json:"key"`
	Models int    `json:"models"`
	Error  string `json:"error,omitempty"`
}

// Providers says how each provider stands, OpenAI's first, as "openai"
// whether or not there is a key for it.
func (g *Gateway) Providers() []ProviderStatus {
	ups := g.providers()
	out := []ProviderStatus{}
	if !slices.ContainsFunc(ups, func(u *providerUpstream) bool { return u.cfg.ID == openAI.ID }) {
		out = append(out, ProviderStatus{ID: openAI.ID})
	}
	for _, u := range ups {
		st := ProviderStatus{ID: u.cfg.ID, Models: len(u.knownModels())}
		switch {
		case u.cfg.KeyHelper != "":
			st.Key = "helper"
		case u.cfg.Key != "":
			if saved := g.saved.Load(); saved != nil && (*saved)[u.cfg.Key] != "" {
				st.Key = "saved"
			} else if u.apiKey() != "" {
				st.Key = "env"
			}
		}
		if err := u.failed(); err != nil {
			st.Error = err.Error()
		}
		out = append(out, st)
	}
	return out
}

// ProviderModels are each provider's models, by the provider's name, OpenAI's first.
func (g *Gateway) ProviderModels() []ProviderModels {
	var out []ProviderModels
	for _, u := range g.providers() {
		out = append(out, ProviderModels{u.prefix(), u.cfg.Name, u.knownModels()})
	}
	return out
}

type ProviderModels struct {
	Prefix, Name string
	Models       []Model
}

// listing is a list of models fetched behind the asking, again when it is
// stale or of another key or URL.
type listing struct {
	mu       sync.Mutex
	models   []Model
	of       string
	askedFor string
	askedAt  time.Time
	err      error
	asking   bool
}

func (l *listing) known(id string, fetch func(context.Context) ([]Model, error)) []Model {
	l.mu.Lock()
	defer l.mu.Unlock()
	since := time.Since(l.askedAt)
	due := id != l.askedFor || since >= 10*time.Minute || (l.err != nil && since >= time.Minute)
	if due && !l.asking {
		l.asking, l.askedFor, l.askedAt = true, id, time.Now()
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			models, err := fetch(ctx)
			l.mu.Lock()
			defer l.mu.Unlock()
			l.asking, l.err = false, err
			if err == nil {
				l.models, l.of = models, id
			}
		}()
	}
	if l.of != id {
		return nil
	}
	return l.models
}

func (l *listing) failed(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.askedFor != id {
		return nil
	}
	return l.err
}

// dropRefused takes out of payload, for good, the parameters among params a
// 400's msg names: an endpoint that doesn't know one turns the whole request
// away over it.
func (p *providerUpstream) dropRefused(payload map[string]any, msg string, params ...string) bool {
	low := strings.ToLower(msg)
	dropped := false
	for _, k := range params {
		if _, ok := payload[k]; ok && strings.Contains(low, k) {
			delete(payload, k)
			p.mu.Lock()
			p.refused[k] = true
			p.mu.Unlock()
			dropped = true
		}
	}
	return dropped
}

// withoutRefused is payload less what the endpoint turned away before.
func (p *providerUpstream) withoutRefused(payload map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k := range p.refused {
		delete(payload, k)
	}
}
