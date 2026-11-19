package gateway

import (
	"math"
	"regexp"
	"slices"
	"strings"
)

// Text shaped as a vendor's key, or long and random enough to be one.
var (
	keyShapes = regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{20,}|sk-[A-Za-z0-9_-]{20,}|(?:ghp|gho|ghs|ghu)_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,}|glpat-[A-Za-z0-9_-]{20,}|xox[abposr]-[A-Za-z0-9-]{10,}|(?:AKIA|ASIA)[A-Z0-9]{16}|AIza[0-9A-Za-z_-]{35}|npm_[A-Za-z0-9]{36}|eyJ[\w-]{10,}\.[\w-]{10,}\.[\w-]{10,}|-----BEGIN [A-Z ]*PRIVATE KEY-----`)
	longTokens = regexp.MustCompile(`[A-Za-z0-9_\-+/]{32,}={0,2}`)
	hexOnly    = regexp.MustCompile(`^[0-9a-fA-F]+$`)
)

// Unmasked is what in text looks like a secret and isn't masked yet: for the
// page to ask about before the text goes to a model.
func (g *Gateway) Unmasked(text string) []string {
	g.refreshEnv()
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if !seen[s] && string(g.mask.mask([]byte(s))) == s {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, s := range keyShapes.FindAllString(text, -1) {
		add(s)
	}
	// A key is one or two in a text; a lock file's integrity hashes, test
	// fixtures and encoded data are many, and are not.
	var loose []string
	for _, at := range longTokens.FindAllStringIndex(text, -1) {
		s := text[at[0]:at[1]]
		if len(s) <= 100 && random(s) && !digestBefore.MatchString(text[max(0, at[0]-8):at[0]]) {
			loose = append(loose, s)
		}
	}
	if len(loose) <= 6 {
		for _, s := range loose {
			add(s)
		}
	}
	g.held.mu.Lock()
	defer g.held.mu.Unlock()
	g.loadAllowedLocked()
	return slices.DeleteFunc(out, func(v string) bool {
		_, ok := g.held.allowed[digest(v)]
		return ok
	})
}

// digestBefore is the start of a Subresource Integrity hash, base64 that
// package-lock.json and the like are full of.
var digestBefore = regexp.MustCompile(`sha(?:1|256|384|512)-$`)

// random says a token reads as a key, not a word, a path or a hash: letters
// and digits mixed, and close to as unpredictable as such text gets. A git
// hash or a checksum is hex, and named as what it is far more often.
func random(s string) bool {
	if hexOnly.MatchString(s) || strings.Count(s, "/") > 2 {
		return false
	}
	letters, digits := 0, 0
	counts := map[rune]int{}
	for _, r := range s {
		counts[r]++
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z':
			letters++
		}
	}
	if letters == 0 || digits == 0 {
		return false
	}
	entropy := 0.0
	for _, c := range counts {
		p := float64(c) / float64(len(s))
		entropy -= p * math.Log2(p)
	}
	return entropy >= 4.0
}
