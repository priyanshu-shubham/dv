package gitx

// Line-level diff. dv computes diffs itself rather than parsing `git diff`
// output because the UI wants both complete file sides in one payload: that is
// what makes "expand context" free, lets the viewer show an unchanged file
// beneath its hunks, and keeps syntax highlighting correct across hunk borders.
//
// The strategy is the one git calls --patience: split on lines that occur
// exactly once on each side, recurse between those anchors, and run a real
// Myers search only on the leftover blocks. Anchoring this way is both faster
// on large files and produces the diffs people expect — a moved function reads
// as moved rather than as a mess of interleaved deletions.

// OpKind is the disposition of one aligned run of lines.
type OpKind uint8

const (
	OpEqual OpKind = iota
	OpDelete
	OpInsert
)

// Op is a run of consecutive lines sharing a disposition. OldStart and NewStart
// are 0-based indices into the respective line slices.
type Op struct {
	Kind     OpKind `json:"k"`
	OldStart int    `json:"os"`
	OldLen   int    `json:"ol"`
	NewStart int    `json:"ns"`
	NewLen   int    `json:"nl"`
}

const (
	// maxDiffLines: past this, the two sides are reported as a wholesale
	// replacement rather than burning seconds on a generated file.
	maxDiffLines = 100000
	// maxTraceD caps the Myers edit distance; the trace it keeps is O(d^2).
	maxTraceD = 1500
	// maxDepth stops pathological patience recursion.
	maxDepth = 48
)

// DiffLines aligns two line slices. The ops cover both inputs completely:
// walking the old runs reproduces a, the new runs reproduce b.
func DiffLines(a, b []string) []Op {
	e := &emitter{}
	if len(a) > maxDiffLines || len(b) > maxDiffLines {
		e.replace(0, len(a), 0, len(b))
	} else {
		diffRange(e, a, b, 0, 0, 0)
	}
	if len(e.ops) == 0 {
		e.ops = append(e.ops, Op{Kind: OpEqual})
	}
	return e.ops
}

// emitter accumulates ops, merging runs of the same kind as they arrive.
type emitter struct{ ops []Op }

func (e *emitter) add(kind OpKind, os, ol, ns, nl int) {
	if ol == 0 && nl == 0 {
		return
	}
	if n := len(e.ops); n > 0 {
		p := &e.ops[n-1]
		if p.Kind == kind && p.OldStart+p.OldLen == os && p.NewStart+p.NewLen == ns {
			p.OldLen += ol
			p.NewLen += nl
			return
		}
	}
	e.ops = append(e.ops, Op{Kind: kind, OldStart: os, OldLen: ol, NewStart: ns, NewLen: nl})
}

func (e *emitter) replace(ao, alen, bo, blen int) {
	e.add(OpDelete, ao, alen, bo, 0)
	e.add(OpInsert, ao+alen, 0, bo, blen)
}

// diffRange aligns a[..] against b[..], where ao/bo are the offsets of those
// slices within the original files.
func diffRange(e *emitter, a, b []string, ao, bo, depth int) {
	// Trimming the shared head and tail first is what keeps an edit to one line
	// of a large file cheap: the expensive search only sees the changed middle.
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		p++
	}
	if p > 0 {
		e.add(OpEqual, ao, p, bo, p)
		a, b, ao, bo = a[p:], b[p:], ao+p, bo+p
	}
	s := 0
	for s < len(a) && s < len(b) && a[len(a)-1-s] == b[len(b)-1-s] {
		s++
	}
	sufAO, sufBO := ao+len(a)-s, bo+len(b)-s
	a, b = a[:len(a)-s], b[:len(b)-s]

	switch {
	case len(a) == 0 && len(b) == 0:
	case len(a) == 0:
		e.add(OpInsert, ao, 0, bo, len(b))
	case len(b) == 0:
		e.add(OpDelete, ao, len(a), bo, 0)
	default:
		anchors := patienceAnchors(a, b)
		if len(anchors) == 0 || depth >= maxDepth {
			if ops := myersTrace(a, b); ops != nil {
				for _, op := range ops {
					e.add(op.Kind, op.OldStart+ao, op.OldLen, op.NewStart+bo, op.NewLen)
				}
			} else {
				e.replace(ao, len(a), bo, len(b))
			}
		} else {
			pa, pb := 0, 0
			for _, an := range anchors {
				diffRange(e, a[pa:an[0]], b[pb:an[1]], ao+pa, bo+pb, depth+1)
				e.add(OpEqual, ao+an[0], 1, bo+an[1], 1)
				pa, pb = an[0]+1, an[1]+1
			}
			diffRange(e, a[pa:], b[pb:], ao+pa, bo+pb, depth+1)
		}
	}

	if s > 0 {
		e.add(OpEqual, sufAO, s, sufBO, s)
	}
}

// patienceAnchors finds lines occurring exactly once on each side and returns
// the longest subset of them that is increasing in both, which is the alignment
// backbone the recursion splits on.
func patienceAnchors(a, b []string) [][2]int {
	type rec struct{ countA, countB, posB int }
	index := make(map[string]*rec, len(a))
	for _, s := range a {
		r := index[s]
		if r == nil {
			r = &rec{}
			index[s] = r
		}
		r.countA++
	}
	for j, s := range b {
		if r := index[s]; r != nil {
			r.countB++
			r.posB = j
		}
	}

	var pairs [][2]int
	for i, s := range a {
		if r := index[s]; r != nil && r.countA == 1 && r.countB == 1 {
			pairs = append(pairs, [2]int{i, r.posB})
		}
	}
	if len(pairs) < 2 {
		return pairs
	}

	// Longest increasing subsequence over the b positions, by patience sorting:
	// piles[t] is the index (into pairs) of the smallest tail for length t+1.
	piles := make([]int, 0, len(pairs))
	prev := make([]int, len(pairs))
	for i := range prev {
		prev[i] = -1
	}
	for i, pr := range pairs {
		lo, hi := 0, len(piles)
		for lo < hi {
			mid := (lo + hi) / 2
			if pairs[piles[mid]][1] < pr[1] {
				lo = mid + 1
			} else {
				hi = mid
			}
		}
		if lo > 0 {
			prev[i] = piles[lo-1]
		}
		if lo == len(piles) {
			piles = append(piles, i)
		} else {
			piles[lo] = i
		}
	}

	out := make([][2]int, len(piles))
	for i, k := len(piles)-1, piles[len(piles)-1]; i >= 0; i, k = i-1, prev[k] {
		out[i] = pairs[k]
	}
	return out
}

// myersTrace runs Myers' O(ND) search, keeping the per-step frontier so the
// edit path can be recovered. It returns nil when the edit distance exceeds
// maxTraceD, which is the caller's cue to fall back to a wholesale replacement.
func myersTrace(a, b []string) []Op {
	n, m := len(a), len(b)
	off := n + m
	v := make([]int, 2*off+2)
	limit := off
	if limit > maxTraceD {
		limit = maxTraceD
	}

	trace := make([][]int, 0, 32)
	found := -1
	for d := 0; d <= limit && found < 0; d++ {
		snapshot := make([]int, 2*d+1)
		copy(snapshot, v[off-d:off+d+1])
		trace = append(trace, snapshot)

		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				x = v[off+k+1]
			} else {
				x = v[off+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[off+k] = x
			if x >= n && y >= m {
				found = d
				break
			}
		}
	}
	if found < 0 {
		return nil
	}

	// Walk the trace backwards, recording each edit and diagonal move, then
	// flip the result into forward order.
	type step struct {
		kind OpKind
		ai   int
		bi   int
	}
	var rev []step
	x, y := n, m
	for d := found; d >= 0; d-- {
		vs := trace[d]
		k := x - y
		prevX, prevY := 0, 0
		if d > 0 {
			var prevK int
			if k == -d || (k != d && vs[k-1+d] < vs[k+1+d]) {
				prevK = k + 1
			} else {
				prevK = k - 1
			}
			prevX = vs[prevK+d]
			prevY = prevX - prevK
		}
		for x > prevX && y > prevY {
			x--
			y--
			rev = append(rev, step{OpEqual, x, y})
		}
		if d > 0 {
			if x == prevX {
				y--
				rev = append(rev, step{OpInsert, x, y})
			} else {
				x--
				rev = append(rev, step{OpDelete, x, y})
			}
		}
	}

	e := &emitter{}
	for i := len(rev) - 1; i >= 0; i-- {
		st := rev[i]
		switch st.kind {
		case OpEqual:
			e.add(OpEqual, st.ai, 1, st.bi, 1)
		case OpDelete:
			e.add(OpDelete, st.ai, 1, st.bi, 0)
		case OpInsert:
			e.add(OpInsert, st.ai, 0, st.bi, 1)
		}
	}
	return e.ops
}
