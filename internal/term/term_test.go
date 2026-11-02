package term

import "testing"

func TestAttachingAgainSendsOnlyWhatWasMissed(t *testing.T) {
	tm := &Terminal{watchers: map[*Watcher]struct{}{}, epoch: "e"}
	tm.printed([]byte("hello "), false)
	w, back, resumed, mark, _ := tm.Attach(Mark{})
	if string(back) != "hello " || resumed || mark != (Mark{"e", 6}) {
		t.Fatalf("first: %q %v %+v", back, resumed, mark)
	}
	tm.printed([]byte("world"), false)
	if e := <-w.C; string(e.Output) != "world" {
		t.Fatalf("watched: %q", e.Output)
	}
	w.Close()

	_, back, resumed, mark, _ = tm.Attach(mark)
	if string(back) != "world" || !resumed || mark.At != 11 {
		t.Fatalf("again: %q %v %+v", back, resumed, mark)
	}
	// Counted by another dv, or from beyond what is there, the place means nothing here.
	for _, from := range []Mark{{"other", 6}, {"e", 12}} {
		if _, back, resumed, _, _ = tm.Attach(from); string(back) != "hello world" || resumed {
			t.Fatalf("%+v: %q %v", from, back, resumed)
		}
	}
}

func TestAttachingAfterTheStartWasTrimmedSendsAll(t *testing.T) {
	tm := &Terminal{watchers: map[*Watcher]struct{}{}, epoch: "e"}
	line := make([]byte, keep/2)
	for i := range line {
		line[i] = 'x'
	}
	line[len(line)-1] = '\n'
	for range 5 {
		tm.printed(line, false)
	}
	if _, back, resumed, _, _ := tm.Attach(Mark{"e", 1}); resumed || len(back) != len(tm.out) {
		t.Fatalf("%d of %d, resumed %v", len(back), len(tm.out), resumed)
	}
}

func TestPlainTail(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"\x1b[31mFAIL\x1b[0m\tdv/term\r\nok\n\n", "FAIL\tdv/term\nok"},
		{"10%\r50%\r100%\ndone", "100%\ndone"},
		{"\x1b]0;title\x07\x1b(Bab\bc  \n", "ac"},
		{"1\n2\n3\n4", "3\n4"},
	} {
		if got := plainTail([]byte(c.in), 2); got != c.want {
			t.Errorf("%q: %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCommandTailLeavesOutThePrompt(t *testing.T) {
	prompt := "\x1b[32m~/dv\x1b[0m main\r\n❯ "
	typed := len(prompt + "make")
	out := []byte(prompt + "make\r\nbuilt\r\nok\r\n\r\n" + prompt)
	if n := promptLines(out[:typed]); n != 2 {
		t.Fatalf("prompt lines: %d", n)
	}
	if got := commandTail(out[typed:], 2, 1); got != "ok" {
		t.Fatalf("tail: %q", got)
	}
	// Nothing printed, or the whole screen taken: nothing to quote.
	for _, after := range []string{"\r\n" + prompt, "\r\n\x1b[?1049hvim\x1b[?1049l" + prompt} {
		if got := commandTail([]byte(after), 2, 5); got != "" {
			t.Fatalf("%q: %q", after, got)
		}
	}
}

func TestToldOnce(t *testing.T) {
	var told []Finished
	m := &Manager{onFinish: func(f Finished) { told = append(told, f) }}
	tm := &Terminal{id: "a", m: m, shell: "/bin/zsh", notify: true}
	tm.finished(Finished{Command: "make"})()
	tm.finished(Finished{Command: "make"})()
	if len(told) != 1 || told[0].Name != "zsh" || tm.notify {
		t.Fatalf("%+v, notify %v", told, tm.notify)
	}
}
