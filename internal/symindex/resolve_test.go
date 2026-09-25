package symindex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeLister lets the tests drive an Index over a temp directory.
type fakeLister struct{ files []string }

func (f fakeLister) TrackedFiles() ([]string, error) { return f.files, nil }
func (f fakeLister) SkippedDirs() []string           { return nil }

func build(t *testing.T, files map[string]string) *Index {
	t.Helper()
	root := t.TempDir()
	var names []string
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	ix := New(root, fakeLister{names})
	ix.Build()
	return ix
}

func names(defs []Candidate) []string {
	out := make([]string, len(defs))
	for i, d := range defs {
		out[i] = d.File + ":" + itoa(d.Line)
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestCommentsAreNotDefinitions(t *testing.T) {
	ix := build(t, map[string]string{
		"a.go": strings.Join([]string{
			"package a",
			"",
			"// func Ghost does not exist; this line only talks about it.",
			"/*",
			"func Buried() {}",
			"*/",
			"func Real() {}",
		}, "\n"),
	})

	for _, ghost := range []string{"Ghost", "Buried"} {
		if got := ix.Lookup(ghost); len(got) != 0 {
			t.Errorf("indexed %s from a comment: %+v", ghost, got)
		}
	}
	if got := ix.Lookup("Real"); len(got) != 1 {
		t.Errorf("Real: want 1 definition, got %+v", got)
	}
}

func TestProtoDefinitions(t *testing.T) {
	ix := build(t, map[string]string{
		"users.proto": strings.Join([]string{
			`syntax = "proto3";`,
			"message User {",
			"  string id = 1;",
			"  Status status = 2;",
			"  message Address { string line = 1; }",
			"}",
			"enum Status {",
			"  STATUS_UNSPECIFIED = 0;",
			"  STATUS_ACTIVE = 1 [deprecated = true];",
			"}",
			"service Users {",
			"  rpc GetUser(GetUserRequest) returns (User);",
			"}",
		}, "\n"),
	})
	for name, kind := range map[string]string{
		"User": "type", "Address": "type", "Status": "type", "Users": "type",
		"GetUser": "method", "STATUS_UNSPECIFIED": "const", "STATUS_ACTIVE": "const",
	} {
		if got := ix.Lookup(name); len(got) != 1 || got[0].Kind != kind {
			t.Errorf("%s: want one %s, got %+v", name, kind, got)
		}
	}
	for _, field := range []string{"id", "status", "line"} {
		if got := ix.Lookup(field); len(got) != 0 {
			t.Errorf("indexed the field %s: %+v", field, got)
		}
	}
}

func TestCommentMarkerInsideStringDoesNotTruncate(t *testing.T) {
	ix := build(t, map[string]string{
		"a.go": "package a\n\nvar Endpoint = \"https://example.com/x\" // trailing\n",
	})
	got := ix.Lookup("Endpoint")
	if len(got) != 1 {
		t.Fatalf("want Endpoint indexed once, got %+v", got)
	}
}

func TestResolveIsWholeWordOnly(t *testing.T) {
	ix := build(t, map[string]string{
		"a.go": strings.Join([]string{
			"package a",
			"",
			"const MaxInflightLogChunks = 32",
			"",
			"type server struct {",
			"\tinflight int",
			"}",
			"",
			"func (s *server) bump() { s.inflight++ }",
		}, "\n"),
	})

	res := ix.Resolve("inflight", "a.go", 10)
	if len(res.Defs) == 0 {
		t.Fatal("inflight resolved to nothing")
	}
	for _, d := range res.Defs {
		if strings.Contains(d.Text, "MaxInflightLogChunks") {
			t.Errorf("inflight matched MaxInflightLogChunks: %+v", d)
		}
	}
	if res.Defs[0].Line != 6 {
		t.Errorf("want the struct field on line 6 first, got %v", names(res.Defs))
	}
	if res.Source != "scan" {
		t.Errorf("want source scan, got %q", res.Source)
	}
}

func TestResolveSkipsBlockCommentedCode(t *testing.T) {
	ix := build(t, map[string]string{
		"a.go": strings.Join([]string{
			"package a",
			"",
			"/*",
			"func Buried() {}",
			"var buried int",
			"*/",
		}, "\n"),
	})
	for _, name := range []string{"Buried", "buried"} {
		if res := ix.Resolve(name, "", 10); len(res.Defs) != 0 {
			t.Errorf("%s resolved inside a block comment: %+v", name, res.Defs)
		}
	}
}

func TestResolveAlwaysReturnsAList(t *testing.T) {
	ix := build(t, map[string]string{"a.go": "package a\n"})
	if res := ix.Resolve("Nowhere", "", 10); res.Defs == nil {
		t.Error("Defs is nil; the client has to special-case that")
	}
}

func TestResolveSkipsUsagesAndProse(t *testing.T) {
	ix := build(t, map[string]string{
		"a.go": strings.Join([]string{
			"package a",
			"",
			"// inflight counts the requests we have not answered yet.",
			"func run() {",
			"\tinflight := 0",        // 5: declaration
			"\tinflight++",           // 6: usage
			"\tuse(inflight)",        // 7: usage
			"\tlog(\"inflight=%d\")", // 8: string
			"}",
		}, "\n"),
	})

	res := ix.Resolve("inflight", "a.go", 10)
	if len(res.Defs) != 1 {
		t.Fatalf("want only the declaration, got %v", names(res.Defs))
	}
	if res.Defs[0].Line != 5 {
		t.Errorf("want line 5, got %v", names(res.Defs))
	}
}

func TestResolveRanksByProximity(t *testing.T) {
	body := "package p\n\nfunc Handle() {}\n"
	ix := build(t, map[string]string{
		"internal/api/handler.go": body,
		"internal/api/other.go":   body,
		"internal/db/store.go":    body,
		"cmd/tool/main.go":        body,
	})

	res := ix.Resolve("Handle", "internal/api/other.go", 10)
	want := []string{
		"internal/api/other.go:3",   // same file
		"internal/api/handler.go:3", // same directory
		"internal/db/store.go:3",    // shares internal/
		"cmd/tool/main.go:3",        // unrelated
	}
	got := names(res.Defs)
	if len(got) != len(want) {
		t.Fatalf("want %d definitions, got %v", len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ranking:\n got %v\nwant %v", got, want)
		}
	}
	if res.Defs[0].Why != "same file" || res.Defs[1].Why != "same directory" {
		t.Errorf("proximity labels: %q, %q", res.Defs[0].Why, res.Defs[1].Why)
	}
}

func TestResolveKeepsToTheLanguage(t *testing.T) {
	ix := build(t, map[string]string{
		"server/status.go": "package server\n\nfunc Status() {}\n",
		"web/status.js":    "export function Status() {}\n",
		"web/app.tsx":      "export function Badge() {}\n",
		"web/badge.ts":     "export function Badge() {}\n",
		"lib/only.py":      "def only_here():\n    pass\n",
		"server/limits.go": "package server\n\ntype T struct {\n\tlimit int\n}\n",
		"web/limits.js":    "export function limit() {}\n",
	})
	for _, c := range []struct {
		name, from, source string
		want               []string
	}{
		{"Status", "server/main.go", "index", []string{"server/status.go:3"}},
		{"Badge", "web/other.jsx", "index", []string{"web/badge.ts:1", "web/app.tsx:1"}},
		// None in its own language, so the others'.
		{"only_here", "server/main.go", "index", []string{"lib/only.py:1"}},
		// Prose names code in any language.
		{"Status", "README.md", "index", []string{"server/status.go:3", "web/status.js:1"}},
		// A field in Go outranks a function the index has in JS.
		{"limit", "server/main.go", "scan", []string{"server/limits.go:4"}},
	} {
		res := ix.Resolve(c.name, c.from, 10)
		got := names(res.Defs)
		if res.Source != c.source || strings.Join(got, " ") != strings.Join(c.want, " ") {
			t.Errorf("%s from %s: %s %v, want %s %v", c.name, c.from, res.Source, got, c.source, c.want)
		}
	}
}

func TestResolveNoOriginStillWorks(t *testing.T) {
	ix := build(t, map[string]string{"a.go": "package a\n\nfunc Handle() {}\n"})
	res := ix.Resolve("Handle", "", 10)
	if len(res.Defs) != 1 || res.Source != "index" {
		t.Fatalf("got %+v", res)
	}
	if res.Defs[0].Why != "" {
		t.Errorf("want no proximity note without an origin, got %q", res.Defs[0].Why)
	}
}

func TestResolveRejectsNonIdentifiers(t *testing.T) {
	ix := build(t, map[string]string{"a.go": "package a\n"})
	for _, s := range []string{"", "a b", "1abc", "x-y", strings.Repeat("a", 200)} {
		if res := ix.Resolve(s, "", 10); res.Source != "none" || len(res.Defs) != 0 {
			t.Errorf("%q resolved to %+v", s, res)
		}
	}
}

func TestDeclShapes(t *testing.T) {
	cases := []struct {
		code string
		kind string
	}{
		{"\tinflight int", "field"},
		{"\tinflight []byte", "field"},
		{"\tinflight *sync.Mutex", "field"},
		{"\tinflight int `json:\"inflight\"`", "field"},
		{"\tinflight := 0", "local"},
		{"\tinflight = 0", "local"},
		{"var inflight int64", "var"},
		{"let inflight = 3", "var"},
		{"func run(inflight int) {}", "param"},
		{"def run(inflight: int):", "param"},
		{"  inflight: number;", "field"},
		{"\tinflight++", ""},
		{"\treturn inflight", ""},
		{"\tif inflight > max {", ""},
		{"\tatomic.AddInt64(&s.inflight, 1)", ""},
		{"\tuse(inflight)", ""},
		{"\tif inflight == 0 {", ""},
	}
	sh := newDeclShapes("inflight")
	for _, c := range cases {
		kind, _ := sh.match(c.code)
		if kind != c.kind {
			t.Errorf("%q: want kind %q, got %q", c.code, c.kind, kind)
		}
	}
}

func TestStripComments(t *testing.T) {
	goLang := langFor("x.go")
	py := langFor("x.py")
	cases := []struct {
		l    *lang
		in   string
		want string
	}{
		{goLang, `var u = "https://a.b/c" // note`, `var u = "https://a.b/c" `},
		{goLang, `// all comment`, ``},
		{goLang, `code() /* inline */ + more()`, `code()  + more()`},
		{py, `x = 1  # note`, `x = 1  `},
	}
	for _, c := range cases {
		st := newScanState()
		if got := c.l.strip(c.in, &st, false); got != c.want {
			t.Errorf("strip(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	// String bodies blank out, but the name outside them survives.
	st := newScanState()
	got := goLang.strip(`inflight := "inflight mode"`, &st, true)
	if !containsWord(got, "inflight") || strings.Count(got, "inflight") != 1 {
		t.Errorf("blanked strings: %q", got)
	}
}

func TestStripBlockSpansLines(t *testing.T) {
	l := langFor("x.go")
	st := newScanState()
	lines := []string{"before()", "/* open", "func Hidden() {}", "still */ after()"}
	var out []string
	for _, ln := range lines {
		out = append(out, strings.TrimSpace(l.strip(ln, &st, false)))
	}
	want := []string{"before()", "", "", "after()"}
	for i := range want {
		if out[i] != want[i] {
			t.Fatalf("line %d: got %q, want %q (all: %q)", i, out[i], want[i], out)
		}
	}
}
