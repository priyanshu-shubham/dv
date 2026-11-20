package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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

// priceProviders are models.dev's names for where dv's served models come from.
var priceProviders = map[string]string{ModelPrefix: "openai", GeminiPrefix: "google"}

type priceList struct {
	path   string
	client *http.Client

	mu      sync.Mutex
	rates   map[string]rate // by "<provider>/<model id>"
	fetched time.Time
	askedAt time.Time
	asking  bool
}

type savedPrices struct {
	Fetched time.Time       `json:"fetched"`
	Rates   map[string]rate `json:"rates"`
}

func newPriceList(path string) *priceList {
	p := &priceList{path: path, client: &http.Client{Timeout: time.Minute}}
	if b, err := os.ReadFile(path); err == nil {
		var s savedPrices
		if json.Unmarshal(b, &s) == nil {
			p.rates, p.fetched = s.Rates, s.Fetched
		}
	}
	return p
}

// current is the list as it is now; one a day old, or never fetched, is
// fetched again behind it.
func (p *priceList) current() map[string]rate {
	p.mu.Lock()
	defer p.mu.Unlock()
	due := time.Since(p.fetched) >= 24*time.Hour && time.Since(p.askedAt) >= 10*time.Minute
	if due && !p.asking {
		p.asking, p.askedAt = true, time.Now()
		go func() {
			rates, err := p.fetch()
			p.mu.Lock()
			defer p.mu.Unlock()
			p.asking = false
			if err != nil {
				return
			}
			p.rates, p.fetched = rates, time.Now()
			if b, err := json.Marshal(savedPrices{p.fetched, rates}); err == nil {
				os.WriteFile(p.path, b, 0o600)
			}
		}()
	}
	return p.rates
}

func (p *priceList) fetch() (map[string]rate, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, pricesURL, nil)
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching model prices: %s", resp.Status)
	}
	var all map[string]struct {
		Models map[string]struct {
			Cost *rate `json:"cost"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&all); err != nil {
		return nil, err
	}
	rates := map[string]rate{}
	for _, provider := range priceProviders {
		for id, m := range all[provider].Models {
			if m.Cost != nil {
				rates[provider+"/"+id] = *m.Cost
			}
		}
	}
	if len(rates) == 0 {
		return nil, fmt.Errorf("fetching model prices: none for %v", priceProviders)
	}
	return rates, nil
}

// rateOf is a served model's rate: its own, else that of the longest id it
// is a variant of (gemini-3.1-pro-preview-customtools, say).
func rateOf(rates map[string]rate, model string) (rate, bool) {
	for prefix, provider := range priceProviders {
		id, ok := strings.CutPrefix(model, prefix)
		if !ok {
			continue
		}
		if r, ok := rates[provider+"/"+id]; ok {
			return r, true
		}
		best, found := "", rate{}
		for key, r := range rates {
			base, ok := strings.CutPrefix(key, provider+"/")
			if ok && strings.HasPrefix(id, base+"-") && len(base) > len(best) {
				best, found = base, r
			}
		}
		return found, best != ""
	}
	return rate{}, false
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
		if !Served(model) {
			continue
		}
		if rates == nil {
			if g == nil {
				return 0, false
			}
			if rates = g.prices.current(); rates == nil {
				return 0, false
			}
		}
		r, known := rateOf(rates, model)
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
