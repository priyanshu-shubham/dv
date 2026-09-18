package server

import "testing"

func TestWithContext(t *testing.T) {
	note := viaNote("Telegram")
	for _, c := range []struct{ text, want string }{
		{"fix it\n", "fix it\n\n<dv-context>\n" + note + "\n</dv-context>"},
		{
			"fix it\n\n<dv-context>\n<file path=\"a.go\" />\n</dv-context>",
			"fix it\n\n<dv-context>\n<file path=\"a.go\" />\n" + note + "\n</dv-context>",
		},
	} {
		if got := withContext(c.text, note); got != c.want {
			t.Errorf("%q:\n got %q\nwant %q", c.text, got, c.want)
		}
	}
}
