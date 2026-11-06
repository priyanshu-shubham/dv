package logs

import (
	"bytes"
	"strings"
	"testing"
)

func TestPumpKeepsLines(t *testing.T) {
	lines, next = nil, 1
	var term bytes.Buffer
	pumping.Add(1)
	pump(strings.NewReader("\n  \x1b[1mdv hub\x1b[0m — 3 folders\n\nfirst\nunfinished"), &term, true)

	if !strings.Contains(term.String(), "\x1b[1m") {
		t.Error("the terminal should get what was written as it was")
	}
	got, n := Since(0)
	want := []string{"  dv hub — 3 folders", "first", "unfinished"}
	if len(got) != len(want) || n != 4 {
		t.Fatalf("got %d lines, next %d; want %d, next 4", len(got), n, len(want))
	}
	for i, l := range got {
		if l.Text != want[i] || !l.Err || l.N != i+1 {
			t.Errorf("line %d = %+v, want %q from stderr", i, l, want[i])
		}
	}
	if after, _ := Since(2); len(after) != 1 || after[0].Text != "unfinished" {
		t.Errorf("Since(2) = %+v", after)
	}
}

func TestKeepLetsGoOfTheOldest(t *testing.T) {
	lines, next = nil, 1
	for range maxLines * 2 {
		keep("x", false)
	}
	got, n := Since(0)
	if len(got) > maxLines+maxLines/4 || got[len(got)-1].N != maxLines*2 || n != maxLines*2+1 {
		t.Errorf("kept %d lines, last %d, next %d", len(got), got[len(got)-1].N, n)
	}
}
