package gateway

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// GeminiPrefix marks a model the gateway serves with the user's Gemini API
// key: gemini/gemini-4-hydrogen.
const GeminiPrefix = "gemini/"

// GeminiKeyName is the secret the Gemini API key is saved as.
const GeminiKeyName = "GEMINI_API_KEY"

const geminiBackend = "https://generativelanguage.googleapis.com/v1beta"

var errNoGeminiKey = errors.New("there is no Gemini API key: save one as " + GeminiKeyName + " in Settings, Secrets")

// geminiUpstream is Gemini's Interactions API, used stateless (store:false):
// every request carries the whole conversation, as Claude Code sends it.
// It is the Gemini API's with a key, or Vertex AI's, the same but for where
// it is and how it is signed in to (vertex.go).
type geminiUpstream struct {
	base       string
	vertexHost string // where Vertex is, for tests
	client     *http.Client
	key        func() string
	route      func() GeminiRoute
	login      *googleLogin

	mu        sync.Mutex
	models    []Model
	modelsFor string // the access's id the list is of
	askedFor  string
	askedAt   time.Time
	err       error // the last asking's
	asking    bool
}

func newGeminiUpstream(key func() string) *geminiUpstream {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = 5 * time.Minute
	return &geminiUpstream{base: geminiBackend, client: &http.Client{Transport: t}, key: key, login: &googleLogin{}}
}

// geminiThinking are the thinking levels of the models Google's docs list
// them for; another takes low to high. A model's variants (…-customtools)
// take its own.
var geminiThinking = map[string]struct {
	levels []string
	def    string
}{
	"gemini-4-carbon":        {[]string{"low", "medium", "high"}, "medium"},
	"gemini-4-argon":         {[]string{"low", "medium", "high"}, "medium"},
	"gemini-4-neon":          {[]string{"low", "medium", "high", "xhigh"}, "high"},
	"gemini-4-hydrogen":      {[]string{"low", "medium", "high", "xhigh"}, "high"},
	"gemini-3.8-flash":       {[]string{"low", "medium", "high"}, "medium"},
	"gemini-3.6-flash":       {[]string{"minimal", "low", "medium", "high"}, "medium"},
	"gemini-3.5-flash-lite":  {[]string{"minimal", "low", "medium", "high"}, "minimal"},
	"gemini-3.1-pro-preview": {[]string{"low", "medium", "high"}, "high"},
	"gemini-3.1-flash-lite":  {[]string{"minimal", "low", "medium", "high"}, "minimal"},
	"gemini-3-flash-preview": {[]string{"minimal", "low", "medium", "high"}, "high"},
}

func geminiLevels(slug string) (levels []string, def string) {
	best := ""
	for name := range geminiThinking {
		if (slug == name || strings.HasPrefix(slug, name+"-")) && len(name) > len(best) {
			best = name
		}
	}
	if best == "" {
		return []string{"low", "medium", "high"}, ""
	}
	t := geminiThinking[best]
	return t.levels, t.def
}

// knownModels are the models last heard of for the route there is now,
// without waiting: a list gone stale, or of another key or project, is
// asked for again behind it.
func (c *geminiUpstream) knownModels() []Model {
	a := c.access()
	c.mu.Lock()
	defer c.mu.Unlock()
	if a.err != nil {
		return nil
	}
	id := a.id()
	since := time.Since(c.askedAt)
	due := id != c.askedFor || since >= 10*time.Minute || (c.err != nil && since >= time.Minute)
	if due && !c.asking {
		c.asking, c.askedFor, c.askedAt = true, id, time.Now()
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			var models []Model
			var err error
			if a.vertex {
				models, err = c.fetchVertexModels(ctx, a)
			} else {
				models, err = c.fetchModels(ctx, a.key)
			}
			c.mu.Lock()
			defer c.mu.Unlock()
			c.asking, c.err = false, err
			if err == nil {
				c.models, c.modelsFor = models, id
			}
		}()
	}
	if c.modelsFor != id {
		return nil
	}
	return c.models
}

// failed is why the route's models couldn't be listed, nil where they were
// or haven't been asked for yet - or, on Vertex, why it can't be used.
func (c *geminiUpstream) failed() error {
	a := c.access()
	if a.vertex && a.err != nil {
		return a.err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if a.err != nil || c.askedFor != a.id() {
		return nil
	}
	return c.err
}

var (
	geminiVersion = regexp.MustCompile(`^gemini-(\d+(?:\.\d+)?)`)
	// Models the list has that don't answer a coding agent: speech, pictures,
	// video, embeddings, and dated snapshots of those it has by name.
	notForCode = regexp.MustCompile(`tts|image|live|transcribe|embedding|robotics|omni|banana|audio|computer-use|-latest$|-\d{2}-\d{4}$|-\d{3}$`)
)

func (c *geminiUpstream) fetchModels(ctx context.Context, key string) ([]Model, error) {
	var out []Model
	page := ""
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/models?pageSize=1000&pageToken="+page, nil)
		req.Header.Set("x-goog-api-key", key)
		resp, err := c.client.Do(req)
		if err != nil {
			return nil, err
		}
		var list struct {
			Models []struct {
				Name    string   `json:"name"`
				Display string   `json:"displayName"`
				Input   int      `json:"inputTokenLimit"`
				Methods []string `json:"supportedGenerationMethods"`
			} `json:"models"`
			Next string `json:"nextPageToken"`
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			_, msg := geminiFailure(b)
			return nil, fmt.Errorf("listing Gemini's models: %s", cmp.Or(msg, resp.Status))
		}
		if err := json.Unmarshal(b, &list); err != nil {
			return nil, err
		}
		for _, m := range list.Models {
			slug := strings.TrimPrefix(m.Name, "models/")
			v := geminiVersion.FindStringSubmatch(slug)
			if v == nil || notForCode.MatchString(slug) || (len(m.Methods) > 0 && !slices.Contains(m.Methods, "generateContent")) {
				continue
			}
			// 2.x is deprecated or closed to new projects.
			if n, _ := strconv.ParseFloat(v[1], 64); n < 3 {
				continue
			}
			out = append(out, geminiModel(slug, cmp.Or(m.Display, slug), m.Input))
		}
		if page = list.Next; page == "" {
			break
		}
	}
	geminiNewestFirst(out)
	return out, nil
}

func geminiModel(slug, label string, window int) Model {
	levels, def := geminiLevels(slug)
	m := Model{Slug: slug, Label: label, Window: window, Levels: levels, Default: def}
	for _, l := range levels {
		if slices.Contains(claudeEfforts, l) {
			m.Efforts = append(m.Efforts, l)
		}
	}
	return m
}

// geminiNewestFirst sorts as Codex's list is.
func geminiNewestFirst(models []Model) {
	version := func(m Model) float64 {
		n, _ := strconv.ParseFloat(geminiVersion.FindStringSubmatch(m.Slug)[1], 64)
		return n
	}
	slices.SortStableFunc(models, func(a, b Model) int {
		return cmp.Or(cmp.Compare(version(b), version(a)), strings.Compare(a.Slug, b.Slug))
	})
}

// serve answers a Messages request with a Gemini model.
func (c *geminiUpstream) serve(w http.ResponseWriter, r *http.Request, body []byte, m *masker) {
	var req anthropicRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	slug := strings.TrimPrefix(req.Model, GeminiPrefix)
	levels, _ := geminiLevels(slug)
	payload, names := geminiRequest(&req, slug, levels)
	if strings.HasSuffix(r.URL.Path, "/count_tokens") {
		json.NewEncoder(w).Encode(map[string]int{"input_tokens": estimateTokens(payload)})
		return
	}

	resp, err := c.send(r.Context(), payload)
	if err != nil {
		if errors.As(err, new(accessError)) {
			apiError{http.StatusForbidden, "permission_error", err.Error(), true}.write(w)
		} else if !errors.Is(err, context.Canceled) {
			apiError{http.StatusBadGateway, "api_error", "Gemini: " + err.Error(), false}.write(w)
		}
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		geminiRelayError(w, resp)
		return
	}

	out := newReply(w, req.Stream, m)
	s := &geminiStream{model: req.Model, names: names, out: out.out, steps: map[int]*geminiStep{}}
	for _, sg := range req.Safeguards {
		if sg.Type == "dangerous_tool_use" {
			s.judge = func(calls []toolCall) map[string]any {
				return judged(r.Context(), sg, &req, calls, func(ctx context.Context, ask string) (string, error) {
					return c.verdicts(ctx, slug, levels, ask)
				})
			}
		}
	}
	s.run(resp.Body)
	out.close()
}

// signatureRefused is Google turning away a thought signature it didn't
// give, or can't read: "Corrupted thought signature", "Invalid thought
// signature".
var signatureRefused = regexp.MustCompile(`(?i)(corrupted|invalid) thought[ _]signature`)

// send posts a turn, and where its thoughts' signatures are refused, posts
// it again without them.
func (c *geminiUpstream) send(ctx context.Context, payload map[string]any) (*http.Response, error) {
	resp, err := c.post(ctx, payload)
	if err != nil || resp.StatusCode != http.StatusBadRequest {
		return resp, err
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	resp.Body.Close()
	if !signatureRefused.Match(b) {
		resp.Body = io.NopCloser(bytes.NewReader(b))
		return resp, nil
	}
	input, _ := payload["input"].([]map[string]any)
	payload["input"] = slices.DeleteFunc(slices.Clone(input), func(it map[string]any) bool { return it["type"] == "thought" })
	return c.post(ctx, payload)
}

func (c *geminiUpstream) post(ctx context.Context, payload map[string]any) (*http.Response, error) {
	a := c.access()
	if a.err != nil {
		return nil, accessError{a.err}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.interactions(a), bytes.NewReader(data))
	if err := c.authorize(req, a); err != nil {
		return nil, accessError{err}
	}
	req.Header.Set("Content-Type", "application/json")
	if payload["stream"] == true {
		req.Header.Set("Accept", "text/event-stream")
	}
	return c.client.Do(req)
}

// geminiFailure reads an error as the API words one: its code a word now,
// a number with the status beside it in Google's older shape.
func geminiFailure(b []byte) (code, msg string) {
	var e struct {
		Error struct {
			Code    json.RawMessage `json:"code"`
			Status  string          `json:"status"`
			Message string          `json:"message"`
		} `json:"error"`
	}
	json.Unmarshal(b, &e)
	json.Unmarshal(e.Error.Code, &code)
	return cmp.Or(code, strings.ToLower(e.Error.Status)), e.Error.Message
}

func geminiRelayError(w http.ResponseWriter, resp *http.Response) {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	code, msg := geminiFailure(b)
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		w.Header().Set("Retry-After", ra)
	}
	geminiFailures.classify(resp.StatusCode, code, cmp.Or(msg, strings.TrimSpace(string(b)), resp.Status)).write(w)
}

// verdicts has the model judge auto mode's calls (safeguard.go), its answer
// held to the verdicts' schema.
func (c *geminiUpstream) verdicts(ctx context.Context, slug string, levels []string, ask string) (string, error) {
	payload := map[string]any{
		"model":              slug,
		"system_instruction": judgeInstructions,
		"input":              []map[string]any{{"type": "user_input", "content": []map[string]any{{"type": "text", "text": ask}}}},
		"store":              false,
		"response_format":    map[string]any{"type": "text", "mime_type": "application/json", "schema": verdictSchema},
		"generation_config":  map[string]any{"thinking_level": lowestEffort(levels), "thinking_summaries": "none"},
	}
	resp, err := c.post(ctx, payload)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		_, msg := geminiFailure(b)
		return "", fmt.Errorf("%s: %s", resp.Status, cmp.Or(msg, strings.TrimSpace(string(b))))
	}
	var done struct {
		Steps []struct {
			Type    string `json:"type"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(b, &done); err != nil {
		return "", err
	}
	var text strings.Builder
	for _, st := range done.Steps {
		if st.Type == "model_output" {
			for _, p := range st.Content {
				text.WriteString(p.Text)
			}
		}
	}
	return text.String(), nil
}
