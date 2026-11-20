package gateway

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

// pricesURL lists what models cost by the token, kept up to date by its
// community; Claude Code knows only Claude's prices and guesses the rest.
const pricesURL = "https://models.dev/api.json"

// rate is in dollars per million tokens, as models.dev gives it.
type rate struct {
	Input      float64  `json:"input"`
	Output     float64  `json:"output"`
	CacheRead  *float64 `json:"cache_read,omitempty"`
	CacheWrite *float64 `json:"cache_write,omitempty"`
}

// priceProviders are models.dev's names for where dv's own served models
// come from; a provider the user added is priced under anyProvider.
var priceProviders = map[string]string{ModelPrefix: "openai", GeminiPrefix: "google", OpenAIPrefix: "openai"}

// anyProvider keys a model by its name alone, as whoever makes it prices it:
// a proxy's deepseek-chat costs what DeepSeek's does.
const anyProvider = "*/"

// makers are models.dev's providers whose word on a model of theirs goes
// before a reseller's.
var makers = []string{"openai", "anthropic", "google", "deepseek", "mistral", "xai", "alibaba", "moonshotai", "zhipuai", "minimax", "meta"}

// modelInfo is what models.dev says of a model besides its price.
type modelInfo struct {
	Name      string `json:"name,omitempty"`
	Window    int    `json:"window,omitempty"`
	Reasoning bool   `json:"reasoning,omitempty"`
}

type priceList struct {
	path   string
	client *http.Client

	mu      sync.Mutex
	rates   map[string]rate // by "<provider>/<model id>"
	about   map[string]modelInfo
	fetched time.Time
	askedAt time.Time
	asking  bool
}

type savedPrices struct {
	Fetched time.Time            `json:"fetched"`
	Rates   map[string]rate      `json:"rates"`
	Info    map[string]modelInfo `json:"info"`
}

func newPriceList(path string) *priceList {
	p := &priceList{path: path, client: &http.Client{Timeout: time.Minute}}
	if b, err := os.ReadFile(path); err == nil {
		var s savedPrices
		if json.Unmarshal(b, &s) == nil {
			p.rates, p.about, p.fetched = s.Rates, s.Info, s.Fetched
		}
	}
	return p
}

// current is the list as it is now; one a day old, or never fetched, is
// fetched again behind it.
func (p *priceList) current() map[string]rate {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refreshLocked()
	return p.rates
}

// info is what models.dev says of each model, as current is.
func (p *priceList) info() map[string]modelInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refreshLocked()
	return p.about
}

func (p *priceList) refreshLocked() {
	// A list saved before it kept models' windows is as good as none.
	due := (time.Since(p.fetched) >= 24*time.Hour || p.about == nil) && time.Since(p.askedAt) >= 10*time.Minute
	if !due || p.asking || p.client == nil {
		return
	}
	p.asking, p.askedAt = true, time.Now()
	go func() {
		rates, about, err := p.fetch()
		p.mu.Lock()
		defer p.mu.Unlock()
		p.asking = false
		if err != nil {
			return
		}
		p.rates, p.about, p.fetched = rates, about, time.Now()
		if b, err := json.Marshal(savedPrices{p.fetched, rates, about}); err == nil {
			os.WriteFile(p.path, b, 0o600)
		}
	}()
}

func (p *priceList) fetch() (map[string]rate, map[string]modelInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, pricesURL, nil)
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("fetching model prices: %s", resp.Status)
	}
	var all map[string]struct {
		Models map[string]listedPrice `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&all); err != nil {
		return nil, nil, err
	}
	rates, about := map[string]rate{}, map[string]modelInfo{}
	put := func(key string, m listedPrice) {
		if m.Cost != nil {
			rates[key] = *m.Cost
		}
		about[key] = modelInfo{Name: m.Name, Window: cmp.Or(m.Limit.Input, m.Limit.Context), Reasoning: m.Reasoning}
	}
	for _, provider := range priceProviders {
		for id, m := range all[provider].Models {
			put(provider+"/"+id, m)
		}
	}
	names := slices.Sorted(maps.Keys(all))
	slices.SortStableFunc(names, func(a, b string) int { return cmp.Compare(makerRank(a), makerRank(b)) })
	for _, provider := range names {
		for id, m := range all[provider].Models {
			if key := anyProvider + bareModel(id); about[key] == (modelInfo{}) {
				put(key, m)
			}
		}
	}
	if len(rates) == 0 {
		return nil, nil, fmt.Errorf("fetching model prices: none for %v", priceProviders)
	}
	return rates, about, nil
}

type listedPrice struct {
	Name      string `json:"name"`
	Reasoning bool   `json:"reasoning"`
	Cost      *rate  `json:"cost"`
	Limit     struct {
		Context int `json:"context"`
		Input   int `json:"input"`
	} `json:"limit"`
}

func makerRank(provider string) int {
	if i := slices.Index(makers, provider); i >= 0 {
		return i
	}
	return len(makers)
}

// rateOf is the rate kept under key: its own, else that of the longest id
// it is a variant of (gemini-3.1-pro-preview-customtools, say).
func rateOf(rates map[string]rate, key string) (rate, bool) { return variantOf(rates, key) }

func infoOf(about map[string]modelInfo, key string) (modelInfo, bool) { return variantOf(about, key) }

func variantOf[T any](m map[string]T, key string) (T, bool) {
	if v, ok := m[key]; ok {
		return v, true
	}
	provider, id, _ := strings.Cut(key, "/")
	var found T
	best := ""
	for k, v := range m {
		base, ok := strings.CutPrefix(k, provider+"/")
		if ok && strings.HasPrefix(id, base+"-") && len(base) > len(best) {
			best, found = base, v
		}
	}
	return found, best != ""
}

// rateKey is where a model dv serves is priced, false for one it doesn't.
func (g *Gateway) rateKey(model string) (string, bool) {
	for prefix, provider := range priceProviders {
		if id, ok := strings.CutPrefix(model, prefix); ok {
			return provider + "/" + id, true
		}
	}
	if p, ok := g.provider(model); ok {
		return p.rateKey(strings.TrimPrefix(model, p.prefix())), true
	}
	return "", false
}

// ModelUsage is a model's share of a session's tokens, as Claude Code's
// result message tells it.
type ModelUsage struct {
	Input      int     `json:"inputTokens"`
	Output     int     `json:"outputTokens"`
	CacheRead  int     `json:"cacheReadInputTokens"`
	CacheWrite int     `json:"cacheCreationInputTokens"`
	Cost       float64 `json:"costUSD"`
}

// SessionCost is total, Claude Code's cost for a session, with what it
// guessed for the models dv serves replaced by what they cost; ok is false
// while one of those has no price known. Long-context surcharges aren't
// counted: usage comes summed, not by request.
func SessionCost(total float64, models map[string]ModelUsage) (cost float64, ok bool) {
	g := active.Load()
	var rates map[string]rate
	cost = total
	for model, u := range models {
		if g == nil {
			if Served(model) {
				return 0, false
			}
			continue
		}
		key, served := g.rateKey(model)
		if !served {
			continue
		}
		if rates == nil {
			if rates = g.prices.current(); rates == nil {
				return 0, false
			}
		}
		r, known := rateOf(rates, key)
		if !known {
			return 0, false
		}
		read, write := r.Input, r.Input
		if r.CacheRead != nil {
			read = *r.CacheRead
		}
		if r.CacheWrite != nil {
			write = *r.CacheWrite
		}
		cost += (float64(u.Input)*r.Input + float64(u.Output)*r.Output +
			float64(u.CacheRead)*read + float64(u.CacheWrite)*write) / 1e6
		cost -= u.Cost
	}
	return max(cost, 0), true
}
