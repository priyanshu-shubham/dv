package chat

import (
	"fmt"
	"strings"
	"testing"
)

func TestBranchFor(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{"Fix the login bug", "fix-the-login-bug"},
		{"Refactor the notification centre so that providers can be added", "refactor-the-notification-centre-so"},
		{"```go\nfunc main() {}\n```", "go"},
		{"🚀✨", "session-"},
		{"Ünïcode wörds and 42 more", "ünïcode-wörds-and-42-more"},
	} {
		if got := branchFor(c.text); !strings.HasPrefix(got, c.want) || len(got) > 40 {
			t.Errorf("branchFor(%q) = %q, want %q", c.text, got, c.want)
		}
	}
}

func TestSplit(t *testing.T) {
	md := "intro\n\n```go\n" + strings.Repeat("x := 1\n", 30) + "```\nafter"
	parts := Split(md, 100)
	for i, p := range parts {
		if len(p) > 100 || strings.Count(p, "```")%2 != 0 {
			t.Errorf("part %d, %d bytes, has its code block open:\n%s", i, len(p), p)
		}
	}
	if joined := strings.Join(parts, "\n"); strings.Count(joined, "x := 1") != 30 || !strings.HasSuffix(joined, "after") {
		t.Errorf("parts lose text:\n%s", joined)
	}
	if got := fmt.Sprint(Split("", 100)); got != "[]" {
		t.Errorf("nothing, split: %s", got)
	}
}

func TestEscape(t *testing.T) {
	if got := Escape("a*b_c`d~e[f]g\\h"); got != "a\\*b\\_c\\`d\\~e\\[f\\]g\\\\h" {
		t.Errorf("escaped %q", got)
	}
	if got := fenced("a ``` b"); got != "~~~\na ``` b\n~~~" {
		t.Errorf("fenced %q", got)
	}
}
