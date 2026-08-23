package symindex

import "strings"

// fuzzyScore matches pattern against target the way a command palette does:
// every pattern character must appear in order, and matches that land on word
// boundaries, on a prefix, or contiguously score higher. The returned indices
// are the matched positions in target, for highlighting.
func fuzzyScore(pattern, target string) (int, []int, bool) {
	if pattern == "" {
		return 0, nil, false
	}
	if len(pattern) > len(target) {
		return 0, nil, false
	}

	lowerT := strings.ToLower(target)
	lowerP := strings.ToLower(pattern)

	// Exact and prefix matches are common enough to be worth short-circuiting,
	// both because they are fast and because they must outrank everything else.
	if target == pattern {
		return 10000, seq(len(pattern)), true
	}
	if lowerT == lowerP {
		return 9000, seq(len(pattern)), true
	}
	if strings.HasPrefix(lowerT, lowerP) {
		return 5000 - len(target), seq(len(pattern)), true
	}

	if idx := strings.Index(lowerT, lowerP); idx >= 0 {
		// Contiguous substring: strong, and stronger at a word boundary.
		score := 3000 - idx*4 - len(target)
		if isBoundary(target, idx) {
			score += 800
		}
		out := make([]int, len(pattern))
		for i := range out {
			out[i] = idx + i
		}
		return score, out, true
	}

	// Subsequence walk. Greedy left-to-right is good enough here and stays
	// linear; the boundary and adjacency bonuses do the ranking work.
	matches := make([]int, 0, len(lowerP))
	ti := 0
	score := 1000 - len(target)
	prev := -2
	for pi := 0; pi < len(lowerP); pi++ {
		c := lowerP[pi]
		found := -1
		for ; ti < len(lowerT); ti++ {
			if lowerT[ti] == c {
				found = ti
				break
			}
		}
		if found < 0 {
			return 0, nil, false
		}
		if found == prev+1 {
			score += 25
		} else {
			score -= (found - prev) * 2
		}
		if isBoundary(target, found) {
			score += 60
		}
		if found == 0 {
			score += 40
		}
		matches = append(matches, found)
		prev = found
		ti++
	}
	return score, matches, true
}

// isBoundary reports whether i starts a word: the first character, one after a
// separator, or the capital in a camelCase hump.
func isBoundary(s string, i int) bool {
	if i == 0 {
		return true
	}
	p, c := s[i-1], s[i]
	if p == '_' || p == '-' || p == '.' || p == '/' || p == ' ' || p == ':' {
		return true
	}
	return p >= 'a' && p <= 'z' && c >= 'A' && c <= 'Z'
}

func seq(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}
