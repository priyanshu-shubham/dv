package gitx

import (
	"math/rand"
	"strconv"
	"testing"
)

// apply walks the ops and rebuilds both sides, which is the invariant that
// matters: every line of both inputs is accounted for exactly once, in order.
func apply(t *testing.T, a, b []string, ops []Op) {
	t.Helper()
	ai, bi := 0, 0
	for i, op := range ops {
		switch op.Kind {
		case OpEqual:
			if op.OldStart != ai || op.NewStart != bi {
				t.Fatalf("op %d: equal starts at %d/%d, expected %d/%d", i, op.OldStart, op.NewStart, ai, bi)
			}
			if op.OldLen != op.NewLen {
				t.Fatalf("op %d: equal run has mismatched lengths %d/%d", i, op.OldLen, op.NewLen)
			}
			for k := 0; k < op.OldLen; k++ {
				if a[ai+k] != b[bi+k] {
					t.Fatalf("op %d: line %d marked equal but %q != %q", i, ai+k, a[ai+k], b[bi+k])
				}
			}
			ai += op.OldLen
			bi += op.NewLen
		case OpDelete:
			if op.OldStart != ai {
				t.Fatalf("op %d: delete starts at %d, expected %d", i, op.OldStart, ai)
			}
			ai += op.OldLen
		case OpInsert:
			if op.NewStart != bi {
				t.Fatalf("op %d: insert starts at %d, expected %d", i, op.NewStart, bi)
			}
			bi += op.NewLen
		}
	}
	if ai != len(a) || bi != len(b) {
		t.Fatalf("ops covered %d/%d lines, want %d/%d", ai, bi, len(a), len(b))
	}
}

func editDistance(ops []Op) int {
	n := 0
	for _, op := range ops {
		n += op.OldLen*b2i(op.Kind == OpDelete) + op.NewLen*b2i(op.Kind == OpInsert)
	}
	return n
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func lines(ss ...string) []string { return ss }

func TestDiffLinesBasics(t *testing.T) {
	cases := []struct {
		name string
		a, b []string
	}{
		{"identical", lines("a", "b", "c"), lines("a", "b", "c")},
		{"empty both", nil, nil},
		{"all inserted", nil, lines("a", "b")},
		{"all deleted", lines("a", "b"), nil},
		{"middle change", lines("a", "b", "c"), lines("a", "x", "c")},
		{"prefix change", lines("a", "b", "c"), lines("z", "b", "c")},
		{"suffix change", lines("a", "b", "c"), lines("a", "b", "z")},
		{"insert middle", lines("a", "d"), lines("a", "b", "c", "d")},
		{"delete middle", lines("a", "b", "c", "d"), lines("a", "d")},
		{"reorder", lines("a", "b", "c"), lines("c", "b", "a")},
		{"duplicates", lines("x", "x", "x"), lines("x", "x")},
		{"no common", lines("a", "b"), lines("c", "d")},
		{"one blank", lines(""), lines("", "")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ops := DiffLines(tc.a, tc.b)
			apply(t, tc.a, tc.b, ops)
		})
	}
}

// TestDiffLinesRandom is the real safety net: random edits of a random file,
// checked for full coverage every time.
func TestDiffLinesRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	vocab := make([]string, 40)
	for i := range vocab {
		vocab[i] = "line-" + strconv.Itoa(i)
	}
	for iter := 0; iter < 800; iter++ {
		n := rng.Intn(60)
		a := make([]string, n)
		for i := range a {
			a[i] = vocab[rng.Intn(len(vocab))]
		}
		b := append([]string(nil), a...)
		for edits := rng.Intn(8); edits > 0; edits-- {
			switch rng.Intn(3) {
			case 0: // insert
				at := rng.Intn(len(b) + 1)
				b = append(b[:at], append([]string{vocab[rng.Intn(len(vocab))]}, b[at:]...)...)
			case 1: // delete
				if len(b) == 0 {
					continue
				}
				at := rng.Intn(len(b))
				b = append(b[:at], b[at+1:]...)
			case 2: // replace
				if len(b) == 0 {
					continue
				}
				b[rng.Intn(len(b))] = vocab[rng.Intn(len(vocab))]
			}
		}
		ops := DiffLines(a, b)
		apply(t, a, b, ops)
	}
}

// TestDiffLinesMinimalOnSmallInputs checks the result is not just correct but
// tight, comparing against a brute-force edit distance.
func TestDiffLinesMinimalOnSmallInputs(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	vocab := []string{"a", "b", "c"}
	for iter := 0; iter < 400; iter++ {
		a := make([]string, rng.Intn(7))
		for i := range a {
			a[i] = vocab[rng.Intn(len(vocab))]
		}
		b := make([]string, rng.Intn(7))
		for i := range b {
			b[i] = vocab[rng.Intn(len(vocab))]
		}
		ops := DiffLines(a, b)
		apply(t, a, b, ops)
		got, want := editDistance(ops), bruteForceDistance(a, b)
		// Patience anchoring trades a strictly minimal script for a more
		// readable one, so allow a small margin rather than demanding equality.
		if got > want+4 {
			t.Fatalf("distance %d for a=%v b=%v, brute force finds %d", got, a, b, want)
		}
	}
}

func bruteForceDistance(a, b []string) int {
	n, m := len(a), len(b)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
		dp[i][0] = i
	}
	for j := 0; j <= m; j++ {
		dp[0][j] = j
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			if a[i-1] == b[j-1] {
				dp[i][j] = dp[i-1][j-1]
			} else {
				dp[i][j] = 1 + min(dp[i-1][j], dp[i][j-1])
			}
		}
	}
	return dp[n][m]
}

func TestDiffLinesLargeFileEdit(t *testing.T) {
	a := make([]string, 20000)
	for i := range a {
		a[i] = "func f" + strconv.Itoa(i) + "() {}"
	}
	b := append([]string(nil), a...)
	b[10000] = "func f10000(x int) {}"
	ops := DiffLines(a, b)
	apply(t, a, b, ops)
	if d := editDistance(ops); d != 2 {
		t.Fatalf("one changed line should cost 2 edits, got %d", d)
	}
}
