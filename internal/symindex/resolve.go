package symindex

import (
	"bufio"
	"bytes"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Candidate is one ranked answer to "where is this defined?".
type Candidate struct {
	Symbol
	Score int    `json:"score"`
	Why   string `json:"why,omitempty"` // "same file", "same directory", "nearby"
}

// Resolution is what a double-click on an identifier gets back. Source says
// how the answers were found, so the UI can be honest about whether it is
// showing indexed definitions or its best guess from a scan:
//
//	index - exact-name definitions from the symbol table
//	scan  - lines that declare the name but sit below the index's granularity,
//	        which is where struct fields, parameters and locals turn up
//	none  - nothing that looks like a definition anywhere
type Resolution struct {
	Name   string      `json:"name"`
	Source string      `json:"source"`
	Defs   []Candidate `json:"defs"`
}

const maxScanMatches = 400

// Resolve finds where name is defined, ranked by how close each definition is
// to the file the reader is standing in. Matching is exact and whole-word
// throughout: a go-to-definition that lands on MaxInflightLogChunks because
// the reader clicked "inflight" is worse than no answer at all.
//
// Definitions in the reader's own language come first, and alone: a Status in
// the JS is no answer to one clicked in Go, and would stand between the reader
// and the jump. Only when that language has none are the others offered, as a
// class in CSS is to a name clicked in JSX.
func (ix *Index) Resolve(name, from string, limit int) Resolution {
	res := Resolution{Name: name, Source: "none", Defs: []Candidate{}}
	if !isIdentifier(name) {
		return res
	}

	var indexed []Candidate
	for _, s := range ix.Lookup(name) {
		indexed = append(indexed, score(s, from, 0))
	}
	fam := family(from)
	ours := func(defs []Candidate) []Candidate {
		if fam == nil {
			return defs
		}
		var out []Candidate
		for _, d := range defs {
			if family(d.File) == fam {
				out = append(out, d)
			}
		}
		return out
	}
	if defs := ours(indexed); len(defs) > 0 {
		res.Defs, res.Source = defs, "index"
	} else if scanned := ix.scanDefs(name, from); len(ours(scanned)) > 0 {
		res.Defs, res.Source = ours(scanned), "scan"
	} else if len(indexed) > 0 {
		res.Defs, res.Source = indexed, "index"
	} else if len(scanned) > 0 {
		res.Defs, res.Source = scanned, "scan"
	}

	sort.SliceStable(res.Defs, func(i, j int) bool {
		if res.Defs[i].Score != res.Defs[j].Score {
			return res.Defs[i].Score > res.Defs[j].Score
		}
		if res.Defs[i].File != res.Defs[j].File {
			return res.Defs[i].File < res.Defs[j].File
		}
		return res.Defs[i].Line < res.Defs[j].Line
	})
	if limit > 0 && len(res.Defs) > limit {
		res.Defs = res.Defs[:limit]
	}
	return res
}

// Lookup returns every indexed definition with exactly this name.
func (ix *Index) Lookup(name string) []Symbol {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	var out []Symbol
	for _, s := range ix.syms {
		if s.Name == name {
			out = append(out, s)
		}
	}
	return out
}

// scanDefs is the second pass, for names the index does not carry: struct
// fields, parameters, locals. It searches for the whole word and then keeps
// only the lines that declare it, so usages and prose drop out.
//
// Deciding whether a line is prose needs the lines above it - a name inside a
// /* */ block has no marker of its own - so the candidate files are re-read and
// walked in order rather than judged one line at a time.
func (ix *Index) scanDefs(name, from string) []Candidate {
	res, err := ix.Search(SearchOpts{Query: name, WholeWord: true, CaseSens: true, MaxMatches: maxScanMatches})
	if err != nil || res == nil {
		return nil
	}
	byFile := map[string][]int{}
	var order []string
	for _, m := range res.Matches {
		if langFor(m.File) == nil {
			continue
		}
		if _, seen := byFile[m.File]; !seen {
			order = append(order, m.File)
		}
		byFile[m.File] = append(byFile[m.File], m.Line)
	}

	sh := newDeclShapes(name)
	var out []Candidate
	for _, file := range order {
		for _, d := range ix.declsIn(file, name, byFile[file], sh) {
			out = append(out, score(d.Symbol, from, d.bonus))
		}
	}
	return out
}

// decl is a scan hit before it is scored.
type decl struct {
	Symbol
	bonus int
}

// declsIn walks one file, tracking comment and string state, and reports which
// of the given lines actually declare name.
func (ix *Index) declsIn(file, name string, lines []int, sh declShapes) []decl {
	l := langFor(file)
	b, err := os.ReadFile(filepath.Join(ix.root, file))
	if err != nil || len(b) > maxIndexedBytes || isBinary(b) {
		return nil
	}
	want := make(map[int]bool, len(lines))
	for _, n := range lines {
		want[n] = true
	}

	var out []decl
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	st := newScanState()
	n := 0
	for sc.Scan() {
		n++
		raw := sc.Text()
		// Every line has to go through strip, matched or not, or the block
		// state is wrong by the time an interesting line arrives.
		code := l.strip(raw, &st, true)
		if !want[n] || l.isCont(raw) || !containsWord(code, name) {
			continue
		}
		kind, bonus := sh.match(code)
		if kind == "" {
			continue
		}
		out = append(out, decl{
			Symbol: Symbol{Name: name, Kind: kind, File: file, Line: n, Text: strings.TrimSpace(raw)},
			bonus:  bonus,
		})
	}
	return out
}

// score combines what a definition is with where it is. Proximity outweighs
// kind on purpose: two functions of the same name in different packages are
// told apart by which one the reader's file could actually be calling, not by
// which one is a func and which a var.
func score(s Symbol, from string, bonus int) Candidate {
	prox, why := proximity(from, s.File)
	n := bonus + kindWeight(s.Kind) + prox
	// A file named after the symbol is usually where it lives - Button.tsx,
	// user_service.rb, parser.go.
	if base := strings.ToLower(stem(s.File)); base == strings.ToLower(s.Name) {
		n += 60
	}
	return Candidate{Symbol: s, Score: n, Why: why}
}

// proximity ranks a file by how near it is to the one the reader is in.
func proximity(from, file string) (int, string) {
	if from == "" {
		return 0, ""
	}
	if from == file {
		return 600, "same file"
	}
	fromDir, fileDir := path.Dir(from), path.Dir(file)
	if fromDir == fileDir {
		return 400, "same directory"
	}
	// Shared leading path is a decent proxy for "same package, or one the
	// reader's package plausibly imports".
	n := sharedSegments(fromDir, fileDir)
	if n == 0 {
		return 0, ""
	}
	if n > 4 {
		n = 4
	}
	return 50 * n, "nearby"
}

func sharedSegments(a, b string) int {
	as, bs := strings.Split(a, "/"), strings.Split(b, "/")
	n := 0
	for n < len(as) && n < len(bs) && as[n] == bs[n] {
		n++
	}
	return n
}

func stem(p string) string {
	base := path.Base(p)
	if i := strings.IndexByte(base, '.'); i > 0 {
		base = base[:i]
	}
	return base
}

// kindWeight nudges the definitions people usually mean above incidental ones.
func kindWeight(kind string) int {
	switch kind {
	case "func", "class", "type", "struct", "interface", "method", "module", "impl":
		return 120
	case "const", "var", "macro", "resource", "table", "mixin", "mod", "field":
		return 80
	case "param", "local", "rule":
		return 40
	case "heading", "target":
		return 10
	default:
		return 60
	}
}

// declShapes builds the patterns that say "this line declares name" rather than
// merely mentioning it. They are deliberately generic across languages: the
// index already knows every top-level form precisely, so this pass only has to
// recognise the smaller declarations it skips.
type declShapes struct {
	keyword *regexp.Regexp
	assign  *regexp.Regexp
	typed   *regexp.Regexp
	colon   *regexp.Regexp
	param   *regexp.Regexp
}

var keywordKind = map[string]string{
	"func": "func", "fn": "func", "function": "func", "def": "func", "defp": "func", "sub": "func",
	"class": "type", "struct": "type", "type": "type", "interface": "type", "enum": "type",
	"trait": "type", "record": "type", "object": "type", "protocol": "type", "actor": "type",
	"typedef": "type", "union": "type", "extension": "type",
	"mod": "module", "module": "module", "namespace": "module", "package": "module",
	"var": "var", "let": "var", "const": "var", "static": "var", "final": "var", "val": "var",
}

func newDeclShapes(name string) declShapes {
	q := regexp.QuoteMeta(name)
	m := func(p string) *regexp.Regexp { return regexp.MustCompile(p) }
	return declShapes{
		// let x / const x / var *x / def x - a declaring keyword right before it.
		keyword: m(`(?:^|[\s(,;{])(func|fn|function|def|defp|sub|class|struct|type|interface|enum|trait|record|object|protocol|actor|typedef|union|extension|mod|module|namespace|package|var|let|const|static|final|val)\s+(?:\*|&)?` + q + `\b`),
		// x := 0 / x = 0 / x += 1, at the start of the line.
		assign: m(`^\s*(?:\*|&)?` + q + `\s*(?::=|[-+|&^]?=[^=]|=$)`),
		// A Go struct field or C declaration: name, then a type, then nothing.
		typed: m("^\\s*" + q + "\\s+(?:\\*|&|\\[\\]|\\[\\d+\\]|map\\[|chan\\s|<-|\\.\\.\\.)*[\\w\\.\\*\\[\\]<>{}]+\\s*(?:`[^`]*`)?\\s*[,;]?\\s*$"),
		// TypeScript, Python, Swift, Rust: name: Type, optionally with modifiers.
		// The [^=] keeps Go's := out; that is an assignment, handled below.
		colon: m(`^\s*(?:(?:pub(?:\([^)]*\))?|public|private|protected|internal|readonly|static|final|var|let|weak|lazy)\s+)*` + q + `\s*[?!]?\s*:\s*[^=\s]`),
		// A parameter: preceded by ( or , and followed by its type.
		param: m(`[(,]\s*(?:\*|&|\.\.\.)?` + q + `\s*(?::\s*[\w\[\]<>\.\|\s]+|\s+[\w\*\[\]<>\.]+)\s*[,)=]`),
	}
}

func (s declShapes) match(code string) (kind string, bonus int) {
	if m := s.keyword.FindStringSubmatch(code); m != nil {
		return keywordKind[m[1]], 200
	}
	if s.typed.MatchString(code) {
		return "field", 170
	}
	if s.colon.MatchString(code) {
		return "field", 160
	}
	if s.assign.MatchString(code) {
		return "local", 120
	}
	if s.param.MatchString(code) {
		return "param", 90
	}
	return "", 0
}

func containsWord(s, word string) bool {
	for i := 0; ; {
		j := strings.Index(s[i:], word)
		if j < 0 {
			return false
		}
		j += i
		before := j == 0 || !isWordByte(s[j-1])
		after := j+len(word) == len(s) || !isWordByte(s[j+len(word)])
		if before && after {
			return true
		}
		i = j + 1
	}
}

func isWordByte(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentifier(s string) bool {
	if s == "" || len(s) > 120 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isWordByte(s[i]) && s[i] != '$' {
			return false
		}
	}
	return !(s[0] >= '0' && s[0] <= '9')
}
