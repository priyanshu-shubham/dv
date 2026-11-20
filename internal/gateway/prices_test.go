package gateway

import (
	"math"
	"testing"
	"time"
)

func TestSessionCost(t *testing.T) {
	prev := active.Load()
	t.Cleanup(func() { active.Store(prev) })
	tenth := 0.1
	active.Store(&Gateway{prices: &priceList{fetched: time.Now(), rates: map[string]rate{
		"openai/gpt-6-luna":             {Input: 1, Output: 10, CacheRead: &tenth},
		"google/gemini-3.1-pro-preview": {Input: 2, Output: 12},
	}}})

	cost, ok := SessionCost(5.5, map[string]ModelUsage{
		"claude-opus-5-5":  {Input: 1000, Cost: 0.5},
		"codex/gpt-6-luna": {Input: 1e6, Output: 1e5, CacheRead: 1e6, Cost: 4},
		// A variant is priced as the model it is one of.
		"gemini/gemini-3.1-pro-preview-customtools": {Input: 5e5, Cost: 1},
	})
	// 5.5 less the guesses (4, 1), plus luna's 1+1+0.1 and Gemini's 1.
	if want := 3.6; !ok || math.Abs(cost-want) > 1e-9 {
		t.Fatalf("cost = %v, %v; want %v", cost, ok, want)
	}

	if _, ok := SessionCost(1, map[string]ModelUsage{"codex/gpt-unpriced": {Input: 10}}); ok {
		t.Fatal("a model with no price was priced")
	}
	if cost, ok := SessionCost(2, map[string]ModelUsage{"claude-opus-5-5": {Cost: 2}}); !ok || cost != 2 {
		t.Fatalf("Claude-only cost = %v, %v; want Claude Code's own", cost, ok)
	}
}
