package symindex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Match is one line containing a hit. Spans are [start, end) byte offsets of
// the matched text within Text, so the client can highlight them.
type Match struct {
	File  string   `json:"file"`
	Line  int      `json:"line"`
	Text  string   `json:"text"`
	Spans [][2]int `json:"spans"`
}

// SearchOpts mirrors the toggles in the search panel.
type SearchOpts struct {
	Query      string
	Regex      bool
	CaseSens   bool
	WholeWord  bool
	Glob       string
	MaxMatches int
}

// SearchResult is what the panel renders, grouped by file on the client.
type SearchResult struct {
	Matches   []Match `json:"matches"`
	Truncated bool    `json:"truncated"`
	Engine    string  `json:"engine"` // rg | builtin
}

var rgPath = func() string {
	p, err := exec.LookPath("rg")
	if err != nil {
		return ""
	}
	return p
}()

// Search runs a repository-wide text search, preferring ripgrep when it is on
// PATH and falling back to an in-process scan so the tool works without it.
func (ix *Index) Search(opts SearchOpts) (*SearchResult, error) {
	if strings.TrimSpace(opts.Query) == "" {
		return &SearchResult{Matches: []Match{}}, nil
	}
	if opts.MaxMatches <= 0 {
		opts.MaxMatches = 500
	}
	if rgPath != "" {
		res, err := ix.searchRG(opts)
		if err == nil {
			return res, nil
		}
		// A ripgrep failure is usually a bad user regex; report it rather than
		// silently producing different results from the built-in engine.
		if _, bad := err.(*badPattern); bad {
			return nil, err
		}
	}
	return ix.searchBuiltin(opts)
}

type badPattern struct{ msg string }

func (e *badPattern) Error() string { return e.msg }

func (ix *Index) searchRG(opts SearchOpts) (*SearchResult, error) {
	args := []string{"--json", "--line-number", "--no-heading", "--color", "never", "--max-columns", "400"}
	if opts.CaseSens {
		args = append(args, "--case-sensitive")
	} else {
		args = append(args, "--smart-case")
	}
	if opts.Glob != "" {
		args = append(args, "--glob", opts.Glob)
	}
	for d := range skipDirs {
		args = append(args, "--glob", "!"+d+"/")
	}
	args = append(args, "--regexp", pattern(opts), "--", ".")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, rgPath, args...)
	cmd.Dir = ix.root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		// Exit 1 just means "no matches"; anything else with stderr is a real
		// problem, most often an invalid pattern.
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 && stderr.Len() == 0 {
			return &SearchResult{Matches: []Match{}, Engine: "rg"}, nil
		}
		if stderr.Len() > 0 {
			return nil, &badPattern{msg: strings.TrimSpace(stderr.String())}
		}
		return nil, err
	}

	res := &SearchResult{Matches: []Match{}, Engine: "rg"}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		var ev struct {
			Type string `json:"type"`
			Data struct {
				Path       struct{ Text string } `json:"path"`
				Lines      struct{ Text string } `json:"lines"`
				LineNumber int                   `json:"line_number"`
				Submatches []struct {
					Start int `json:"start"`
					End   int `json:"end"`
				} `json:"submatches"`
			} `json:"data"`
		}
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil || ev.Type != "match" {
			continue
		}
		path := strings.TrimPrefix(ev.Data.Path.Text, "./")
		if skipPath(path) {
			continue
		}
		m := Match{File: path, Line: ev.Data.LineNumber, Text: strings.TrimRight(ev.Data.Lines.Text, "\r\n")}
		for _, sm := range ev.Data.Submatches {
			m.Spans = append(m.Spans, [2]int{sm.Start, sm.End})
		}
		res.Matches = append(res.Matches, m)
		if len(res.Matches) >= opts.MaxMatches {
			res.Truncated = true
			break
		}
	}
	return res, nil
}

func (ix *Index) searchBuiltin(opts SearchOpts) (*SearchResult, error) {
	re, err := compile(opts)
	if err != nil {
		return nil, &badPattern{msg: err.Error()}
	}
	paths, err := ix.lister.TrackedFiles()
	if err != nil {
		return nil, err
	}
	var globRe *regexp.Regexp
	if opts.Glob != "" {
		globRe = globToRegexp(opts.Glob)
	}

	res := &SearchResult{Matches: []Match{}, Engine: "builtin"}
	for _, p := range paths {
		if skipPath(p) {
			continue
		}
		if globRe != nil && !globRe.MatchString(p) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(ix.root, p))
		if err != nil || len(b) > maxIndexedBytes || isBinary(b) {
			continue
		}
		line := 0
		sc := bufio.NewScanner(bytes.NewReader(b))
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			line++
			text := sc.Text()
			locs := re.FindAllStringIndex(text, 20)
			if locs == nil {
				continue
			}
			m := Match{File: p, Line: line, Text: text}
			for _, l := range locs {
				m.Spans = append(m.Spans, [2]int{l[0], l[1]})
			}
			res.Matches = append(res.Matches, m)
			if len(res.Matches) >= opts.MaxMatches {
				res.Truncated = true
				return res, nil
			}
		}
	}
	return res, nil
}

// pattern is the query as the regular expression both engines run, so they
// agree on what whole word means. A literal query is held to a word boundary
// only at an end that is itself a word character: `Get(` must start a word,
// but a boundary after its `(` would turn away `Get(ctx)`. ripgrep's own
// --word-regexp wants a non-word character on both sides, which is stricter
// still.
func pattern(opts SearchOpts) string {
	if opts.Regex {
		if opts.WholeWord {
			return `\b(?:` + opts.Query + `)\b`
		}
		return opts.Query
	}
	pat := regexp.QuoteMeta(opts.Query)
	if opts.WholeWord {
		if isWordByte(opts.Query[0]) {
			pat = `\b` + pat
		}
		if isWordByte(opts.Query[len(opts.Query)-1]) {
			pat += `\b`
		}
	}
	return pat
}

func compile(opts SearchOpts) (*regexp.Regexp, error) {
	pat := pattern(opts)
	// Smart case: an all-lowercase query is case-insensitive, matching rg.
	if !opts.CaseSens && strings.ToLower(opts.Query) == opts.Query {
		pat = "(?i)" + pat
	}
	return regexp.Compile(pat)
}

// globToRegexp handles the simple `*.go` / `src/**/*.ts` forms the panel offers.
func globToRegexp(glob string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("(?i)")
	if !strings.Contains(glob, "/") {
		b.WriteString("(^|/)")
	} else {
		b.WriteString("^")
	}
	for i := 0; i < len(glob); i++ {
		switch {
		case strings.HasPrefix(glob[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 2
		case glob[i] == '*':
			b.WriteString("[^/]*")
		case glob[i] == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(glob[i])))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil
	}
	return re
}
