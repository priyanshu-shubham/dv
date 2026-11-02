package term

import "testing"

func TestAttachingAgainSendsOnlyWhatWasMissed(t *testing.T) {
	tm := &Terminal{watchers: map[*Watcher]struct{}{}, epoch: "e"}
	tm.printed([]byte("hello "))
	w, back, resumed, mark, _ := tm.Attach(Mark{})
	if string(back) != "hello " || resumed || mark != (Mark{"e", 6}) {
		t.Fatalf("first: %q %v %+v", back, resumed, mark)
	}
	tm.printed([]byte("world"))
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
		tm.printed(line)
	}
	if _, back, resumed, _, _ := tm.Attach(Mark{"e", 1}); resumed || len(back) != len(tm.out) {
		t.Fatalf("%d of %d, resumed %v", len(back), len(tm.out), resumed)
	}
}
