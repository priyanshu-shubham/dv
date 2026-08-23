package symindex

import (
	"path/filepath"
	"regexp"
	"strings"
)

// rule matches a definition on a single line. group is the capture holding the
// symbol name. Rules for a language are ordered most-specific first, and the
// first match on a line wins.
type rule struct {
	re    *regexp.Regexp
	kind  string
	group int
}

func r(kind, pattern string) rule { return rule{re: regexp.MustCompile(pattern), kind: kind, group: 1} }
func rg(kind, pattern string, group int) rule {
	return rule{re: regexp.MustCompile(pattern), kind: kind, group: group}
}

var (
	goRules = []rule{
		r("method", `^func\s+\([^)]*\)\s+(\w+)`),
		r("func", `^func\s+(\w+)`),
		r("type", `^type\s+(\w+)`),
		r("const", `^const\s+(\w+)`),
		r("var", `^var\s+(\w+)`),
	}

	jsRules = []rule{
		r("func", `^\s*(?:export\s+)?(?:default\s+)?(?:async\s+)?function\s*\*?\s*(\w+)`),
		r("class", `^\s*(?:export\s+)?(?:default\s+)?(?:abstract\s+)?class\s+(\w+)`),
		r("type", `^\s*(?:export\s+)?(?:declare\s+)?(?:interface|type|enum|namespace)\s+(\w+)`),
		// const Foo = (…) => / = function / = async (…) =>
		r("func", `^\s*(?:export\s+)?(?:const|let|var)\s+(\w+)\s*(?::[^=]+)?=\s*(?:async\s*)?(?:function\b|\(|<|\w+\s*=>)`),
		r("var", `^(?:export\s+)?(?:const|let|var)\s+(\w+)`),
		// Class members and object methods, which are always indented.
		r("method", `^\s+(?:static\s+)?(?:async\s+)?(?:get\s+|set\s+)?(\w+)\s*\([^)]*\)\s*(?::[^{]+)?\{`),
	}

	pyRules = []rule{
		r("class", `^\s*class\s+(\w+)`),
		r("func", `^\s*(?:async\s+)?def\s+(\w+)`),
		r("var", `^(\w+)\s*(?::[^=]+)?=\s*\S`),
	}

	rustRules = []rule{
		r("func", `^\s*(?:pub(?:\([^)]*\))?\s+)?(?:const\s+|async\s+|unsafe\s+|extern\s+"[^"]*"\s+)*fn\s+(\w+)`),
		r("type", `^\s*(?:pub(?:\([^)]*\))?\s+)?(?:struct|enum|trait|union|type)\s+(\w+)`),
		r("impl", `^\s*impl(?:<[^>]*>)?\s+(?:\w+(?:<[^>]*>)?\s+for\s+)?(\w+)`),
		r("macro", `^\s*macro_rules!\s+(\w+)`),
		r("const", `^\s*(?:pub(?:\([^)]*\))?\s+)?(?:const|static)\s+(\w+)`),
		r("mod", `^\s*(?:pub\s+)?mod\s+(\w+)`),
	}

	rubyRules = []rule{
		r("class", `^\s*(?:class|module)\s+([\w:]+)`),
		r("func", `^\s*def\s+(?:self\.)?([\w?!=\[\]]+)`),
	}

	jvmRules = []rule{
		r("class", `^\s*(?:@\w+\s+)*(?:public|private|protected|internal|open|sealed|static|final|abstract|data|\s)*(?:class|interface|enum|record|object|trait)\s+(\w+)`),
		r("func", `^\s*(?:@\w+\s+)*(?:public|private|protected|internal|open|override|suspend|static|final|inline|\s)*fun\s+(?:<[^>]+>\s*)?(?:\w+\.)?(\w+)`),
		r("method", `^\s+(?:@\w+\s+)*(?:public|private|protected|static|final|synchronized|abstract|native|default|\s)+[\w<>\[\],.?\s]+\s+(\w+)\s*\([^;]*\)\s*(?:throws [\w,\s.]+)?\{`),
	}

	cRules = []rule{
		r("macro", `^\s*#\s*define\s+(\w+)`),
		r("type", `^\s*(?:typedef\s+)?(?:struct|class|enum|union|namespace)\s+(\w+)`),
		r("func", `^[\w][\w\s\*&:<>,\[\]]*?(\w+)\s*\([^;]*\)\s*(?:const\s*)?(?:noexcept\s*)?\{`),
	}

	phpRules = []rule{
		r("class", `^\s*(?:abstract\s+|final\s+)?(?:class|interface|trait|enum)\s+(\w+)`),
		r("func", `^\s*(?:public|private|protected|static|abstract|final|\s)*function\s+&?(\w+)`),
	}

	shRules = []rule{
		r("func", `^\s*(?:function\s+)?([\w\-]+)\s*\(\)\s*\{`),
		r("func", `^\s*function\s+([\w\-]+)`),
	}

	sqlRules = []rule{
		rg("table", `(?i)^\s*create\s+(?:or\s+replace\s+)?(?:temp\s+|temporary\s+)?(table|view|index|function|procedure|trigger|type)\s+(?:if\s+not\s+exists\s+)?[`+"`"+`"\[]?([\w.]+)`, 2),
	}

	protoRules = []rule{
		r("type", `^\s*(?:message|service|enum)\s+(\w+)`),
		r("method", `^\s*rpc\s+(\w+)`),
	}

	makeRules = []rule{
		r("target", `^([\w][\w.\-/]*)\s*:(?:[^=]|$)`),
	}

	mdRules = []rule{
		r("heading", `^#{1,4}\s+(.+?)\s*#*$`),
	}

	tfRules = []rule{
		rg("resource", `^\s*(?:resource|module|variable|output|data|provider|locals)\s+"([^"]+)"`, 1),
	}

	cssRules = []rule{
		r("mixin", `^\s*@(?:mixin|function)\s+([\w\-]+)`),
		r("var", `^\s*\$([\w\-]+)\s*:`),
		r("rule", `^([.#][\w\-]+(?:[\s,>][^{]*)?)\s*\{`),
	}

	elixirRules = []rule{
		r("module", `^\s*defmodule\s+([\w.]+)`),
		r("func", `^\s*def(?:p|macro|macrop)?\s+([\w?!]+)`),
	}

	luaRules = []rule{
		r("func", `^\s*(?:local\s+)?function\s+([\w.:]+)`),
		r("func", `^\s*(?:local\s+)?([\w.]+)\s*=\s*function`),
	}

	swiftRules = []rule{
		r("func", `^\s*(?:public|private|internal|fileprivate|open|static|class|override|final|\s)*func\s+(\w+)`),
		r("class", `^\s*(?:public|private|internal|fileprivate|open|final|\s)*(?:class|struct|enum|protocol|extension|actor)\s+(\w+)`),
	}
)

// lang bundles a rule set with the comment syntax needed to tell code from
// prose. Indexing a comment is how "inflight" ends up pointing at the sentence
// that mentions it rather than the line that declares it.
type lang struct {
	rules  []rule
	line   []string    // line-comment markers
	blocks [][2]string // block-comment delimiters, open/close
	cont   string      // continuation marker for wrapped block comments (" *")
}

var (
	cLike    = lang{line: []string{"//"}, blocks: [][2]string{{"/*", "*/"}}, cont: "*"}
	hashLike = lang{line: []string{"#"}}
)

// withRules copies a comment syntax and attaches a rule set.
func (l lang) withRules(rs []rule) *lang { l.rules = rs; return &l }

// langFor picks the rule set and comment syntax for a path, returning nil for
// files that hold no definitions worth indexing.
func langFor(path string) *lang {
	base := strings.ToLower(filepath.Base(path))
	if base == "makefile" || strings.HasSuffix(base, ".mk") || base == "gnumakefile" {
		return hashLike.withRules(makeRules)
	}
	switch strings.ToLower(filepath.Ext(base)) {
	case ".go":
		return cLike.withRules(goRules)
	case ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".mts", ".vue", ".svelte":
		return cLike.withRules(jsRules)
	case ".py", ".pyi":
		return (lang{line: []string{"#"}, blocks: [][2]string{{`"""`, `"""`}, {`'''`, `'''`}}}).withRules(pyRules)
	case ".rs":
		return cLike.withRules(rustRules)
	case ".rb", ".rake":
		return (lang{line: []string{"#"}, blocks: [][2]string{{"=begin", "=end"}}}).withRules(rubyRules)
	case ".java", ".kt", ".kts", ".scala", ".cs", ".groovy":
		return cLike.withRules(jvmRules)
	case ".c", ".h", ".cc", ".cpp", ".cxx", ".hpp", ".hh", ".m", ".mm":
		return cLike.withRules(cRules)
	case ".php":
		return (lang{line: []string{"//", "#"}, blocks: [][2]string{{"/*", "*/"}}, cont: "*"}).withRules(phpRules)
	case ".sh", ".bash", ".zsh":
		return hashLike.withRules(shRules)
	case ".sql":
		return (lang{line: []string{"--"}, blocks: [][2]string{{"/*", "*/"}}}).withRules(sqlRules)
	case ".proto":
		return cLike.withRules(protoRules)
	case ".md", ".markdown":
		return (&lang{rules: mdRules}) // headings are the content; nothing to strip
	case ".tf", ".hcl":
		return (lang{line: []string{"#", "//"}, blocks: [][2]string{{"/*", "*/"}}}).withRules(tfRules)
	case ".css", ".scss", ".sass", ".less":
		return (lang{line: []string{"//"}, blocks: [][2]string{{"/*", "*/"}}, cont: "*"}).withRules(cssRules)
	case ".ex", ".exs":
		return hashLike.withRules(elixirRules)
	case ".lua":
		return (lang{line: []string{"--"}, blocks: [][2]string{{"--[[", "]]"}}}).withRules(luaRules)
	case ".swift":
		return cLike.withRules(swiftRules)
	}
	return nil
}

// scanState carries comment and string state across the lines of one file.
type scanState struct {
	block int  // index into lang.blocks, or -1
	quote byte // open string delimiter, or 0
}

func newScanState() scanState { return scanState{block: -1} }

// strip returns the code on a line: comments removed, and with blankStrings
// set, string contents blanked too. State carries across calls so block
// comments and backtick strings that span lines stay understood.
//
// The quote tracking is what keeps a "https://..." literal from truncating its
// own line at the "//".
func (l *lang) strip(text string, st *scanState, blankStrings bool) string {
	if len(l.line) == 0 && len(l.blocks) == 0 && !blankStrings {
		return text
	}
	var b strings.Builder
	b.Grow(len(text))
	for i := 0; i < len(text); {
		if st.block >= 0 {
			cl := l.blocks[st.block][1]
			j := strings.Index(text[i:], cl)
			if j < 0 {
				break
			}
			i += j + len(cl)
			st.block = -1
			continue
		}
		if st.quote != 0 {
			c := text[i]
			if c == '\\' && st.quote != '`' && i+1 < len(text) {
				if !blankStrings {
					b.WriteString(text[i : i+2])
				}
				i += 2
				continue
			}
			if c == st.quote {
				st.quote = 0
				b.WriteByte(c)
			} else if blankStrings {
				b.WriteByte(' ')
			} else {
				b.WriteByte(c)
			}
			i++
			continue
		}
		if k := l.blockAt(text, i); k >= 0 {
			st.block = k
			i += len(l.blocks[k][0])
			continue
		}
		if l.lineAt(text, i) {
			break
		}
		c := text[i]
		if c == '"' || c == '\'' || c == '`' {
			st.quote = c
		}
		b.WriteByte(c)
		i++
	}
	// Only backticks (Go raw strings, JS templates) legitimately span lines;
	// anything else left open is a quote we misread, so do not leak it onward.
	if st.quote != 0 && st.quote != '`' {
		st.quote = 0
	}
	return b.String()
}

func (l *lang) blockAt(text string, i int) int {
	for k, pair := range l.blocks {
		if strings.HasPrefix(text[i:], pair[0]) {
			return k
		}
	}
	return -1
}

func (l *lang) lineAt(text string, i int) bool {
	for _, m := range l.line {
		if strings.HasPrefix(text[i:], m) {
			return true
		}
	}
	return false
}

// isCont reports whether a line is the " * ..." continuation of a block
// comment, which carries no marker of its own for strip to find. Whether that
// line is really inside a comment is not knowable without state, but a line
// starting with "*" is prose often enough - and code rarely enough - to treat
// as one.
func (l *lang) isCont(text string) bool {
	if l.cont == "" {
		return false
	}
	t := strings.TrimSpace(text)
	return strings.HasPrefix(t, l.cont) && !strings.HasPrefix(t, "*/")
}
