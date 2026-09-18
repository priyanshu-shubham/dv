package notify

import (
	"slices"
	"sync"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time       { return c.t }
func (c *clock) pass(d time.Duration) { c.t = c.t.Add(d) }
func minute() time.Duration           { return time.Minute }
func ids(ns []Notice) (out []string) {
	for _, n := range ns {
		out = append(out, n.ID)
	}
	return out
}

// phone is a provider that keeps what it is told.
type phone struct {
	mu      sync.Mutex
	sent    []string
	settled []string
}

func (p *phone) Send(n Notice) {
	p.mu.Lock()
	p.sent = append(p.sent, n.ID)
	p.mu.Unlock()
}

func (p *phone) Settle(n Notice) {
	p.mu.Lock()
	p.settled = append(p.settled, n.ID+": "+n.Outcome)
	p.mu.Unlock()
}

func setup() (*Center, *clock, *phone, *Folder) {
	clk := &clock{time.Unix(1_000_000, 0)}
	c := newCenter(minute, clk.now)
	p := &phone{}
	c.Add(p)
	return c, clk, p, c.Folder("alpha", "Alpha")
}

func TestSentOnAfterAMinuteUnused(t *testing.T) {
	c, clk, p, f := setup()
	_, stop := c.Watch("tab")
	defer stop()
	c.Report("tab", true, true, 0)

	clk.pass(30 * time.Second)
	f.Raise(Notice{ID: "a", Kind: Ask})
	if n := c.Notices(); len(n) != 1 || n[0].Loud || n[0].Folder != "alpha" {
		t.Fatalf("raised with the reader at a page: %+v", n)
	}
	clk.pass(59 * time.Second)
	c.step()
	if len(p.sent) != 0 {
		t.Fatalf("sent on before a minute: %v", p.sent)
	}
	clk.pass(time.Second)
	c.step()
	if !slices.Equal(p.sent, []string{"a"}) || !c.Notices()[0].Loud {
		t.Fatalf("after a minute unused: sent %v, %+v", p.sent, c.Notices())
	}
	c.step()
	if len(p.sent) != 1 {
		t.Fatalf("sent twice: %v", p.sent)
	}
	f.Settle("a", "No longer waiting")
	if !slices.Equal(p.settled, []string{"a: No longer waiting"}) || len(c.Notices()) != 0 {
		t.Fatalf("settled %v, open %v", p.settled, ids(c.Notices()))
	}
}

func TestUsingDvPutsOffARequest(t *testing.T) {
	c, clk, p, f := setup()
	f.Raise(Notice{ID: "a", Kind: Ask})
	if !c.Notices()[0].Loud {
		t.Fatal("no page is open, yet it came quietly")
	}
	clk.pass(50 * time.Second)
	c.Report("tab", true, true, 0) // the reader is back, and has not answered yet
	clk.pass(50 * time.Second)
	c.step()
	if len(p.sent) != 0 {
		t.Fatalf("sent on while the reader used dv: %v", p.sent)
	}
	clk.pass(10 * time.Second)
	c.step()
	if len(p.sent) != 1 {
		t.Fatalf("not sent a minute after the reader left: %v", p.sent)
	}
}

func TestATurnsEndIsSeenByUsingDv(t *testing.T) {
	c, clk, p, f := setup()
	f.Raise(Notice{ID: "d", Kind: Done})
	f.Raise(Notice{ID: "a", Kind: Ask})
	clk.pass(5 * time.Second)
	c.Report("tab", true, true, 2*time.Second)
	if got := ids(c.Notices()); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("open after the reader used dv: %v", got)
	}
	// Used before it came, it was not seen.
	f.Raise(Notice{ID: "e", Kind: Done})
	clk.pass(time.Second)
	c.Report("tab", true, true, 3*time.Second)
	if got := ids(c.Notices()); !slices.Equal(got, []string{"a", "e"}) {
		t.Fatalf("open: %v", got)
	}
	if len(p.settled) != 0 {
		t.Fatalf("told a provider of a notice it never had: %v", p.settled)
	}
}

func TestRightAwayWhenAsked(t *testing.T) {
	clk := &clock{time.Unix(1_000_000, 0)}
	c := newCenter(func() time.Duration { return 0 }, clk.now)
	p := &phone{}
	c.Add(p)
	c.Report("tab", true, true, 0)
	c.Folder("", "dv").Raise(Notice{ID: "a", Kind: Ask})
	if len(p.sent) != 1 {
		t.Fatalf("not sent straight away: %v", p.sent)
	}
}

func TestAnswer(t *testing.T) {
	c, _, p, f := setup()
	var got []int
	f.Raise(Notice{ID: "a", Kind: Ask, Choices: []string{"Yes", "No"}, Answer: func(i int) error {
		got = append(got, i)
		f.Settle("a", "No longer waiting") // as the folder hears the request go
		return nil
	}})
	c.step()
	c.step() // not due yet: nothing sent
	if _, err := c.Answer("a", 2, "Telegram"); err != ErrGone {
		t.Fatalf("a choice it does not have: %v", err)
	}
	choice, err := c.Answer("a", 1, "Telegram")
	if err != nil || choice != "No" || !slices.Equal(got, []int{1}) {
		t.Fatalf("answered %q, %v; called with %v", choice, err, got)
	}
	if _, err := c.Answer("a", 0, "Telegram"); err != ErrGone {
		t.Fatalf("answered twice: %v", err)
	}
	if len(p.settled) != 0 {
		t.Fatalf("settled with a provider it was not sent to: %v", p.settled)
	}
}

func TestOutcomeGivenBeforeItSettles(t *testing.T) {
	c, clk, p, f := setup()
	f.Raise(Notice{ID: "a", Kind: Ask})
	clk.pass(time.Minute)
	c.step()
	f.Outcome("a", "Answered in dv")
	f.Settle("a", "No longer waiting")
	if !slices.Equal(p.settled, []string{"a: Answered in dv"}) {
		t.Fatalf("settled %v", p.settled)
	}
}

func TestPresence(t *testing.T) {
	clk := &clock{time.Unix(1_000_000, 0)}
	c := newCenter(func() time.Duration { return time.Hour }, clk.now)
	f := c.Folder("", "dv")
	c.Report("tab", true, false, 0) // said, but not followed: not a page open
	f.Raise(Notice{ID: "a", Kind: Ask})
	_, stop := c.Watch("tab")
	c.Report("tab", true, false, 0)
	f.Raise(Notice{ID: "b", Kind: Ask})
	clk.pass(2 * time.Minute) // its device asleep
	f.Raise(Notice{ID: "c", Kind: Ask})
	stop()
	c.Report("other", false, false, 0)
	_, stop = c.Watch("other")
	defer stop()
	f.Raise(Notice{ID: "d", Kind: Ask})
	var loud []bool
	for _, n := range c.Notices() {
		loud = append(loud, n.Loud)
	}
	if want := []bool{true, false, true, true}; !slices.Equal(loud, want) {
		t.Fatalf("loud %v, want %v", loud, want)
	}
	if len(c.pages) != 1 {
		t.Fatalf("pages kept: %d", len(c.pages))
	}
}

// sessions is a folder's, doing nothing.
type sessions struct{}

func (sessions) List() []Session                                { return []Session{{ID: "s1"}} }
func (sessions) Send(string, Message) (string, error)           { return "m1", nil }
func (sessions) Progress(string, string) Progress               { return Finished }
func (sessions) Start(Message) (string, error)                  { return "s2", nil }
func (sessions) Stop(string) error                              { return nil }
func (sessions) Reply(string) string                            { return "" }
func (sessions) Setup(string) Setup                             { return Setup{} }
func (sessions) Configure(string, *string, *string, bool) error { return nil }

func TestTalkingFromAProvider(t *testing.T) {
	c, _, p, f := setup()
	c.Report("tab", true, true, 0)
	f.Serve(sessions{})
	if got := c.Sessions(); len(got) != 1 || got[0].Folder != "alpha" || got[0].Place != "Alpha" {
		t.Fatalf("sessions %+v", got)
	}
	if _, err := c.Send("beta", "s1", Message{Text: "hi"}); err != ErrNoFolder {
		t.Fatalf("to a folder not open: %v", err)
	}
	if _, err := c.Send("alpha", "s1", Message{Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	// Written to, a session's notices come at once, the reader at a page or
	// not, until its turn ends; another's wait as ever.
	f.Raise(Notice{ID: "a", Kind: Ask, Session: "s1"})
	f.Raise(Notice{ID: "b", Kind: Ask, Session: "s9"})
	f.Raise(Notice{ID: "d", Kind: Done, Session: "s1"})
	f.Raise(Notice{ID: "e", Kind: Done, Session: "s1"})
	if !slices.Equal(p.sent, []string{"a", "d"}) {
		t.Fatalf("sent %v", p.sent)
	}
	if id, err := c.Start("alpha", Message{Text: "do it"}); err != nil || id != "s2" {
		t.Fatalf("started %q, %v", id, err)
	}
	f.Raise(Notice{ID: "f", Kind: Done, Session: "s2"})
	if !slices.Equal(p.sent, []string{"a", "d", "f"}) {
		t.Fatalf("sent %v", p.sent)
	}
	f.Close()
	if len(c.Places()) != 0 || len(c.Sessions()) != 0 {
		t.Fatal("a folder closed is still offered")
	}
}

func TestClosingAFolder(t *testing.T) {
	c, clk, p, f := setup()
	other := c.Folder("beta", "Beta")
	f.Raise(Notice{ID: "a", Kind: Ask})
	other.Raise(Notice{ID: "b", Kind: Ask})
	clk.pass(time.Minute)
	c.step()
	f.Close()
	if got := ids(c.Notices()); !slices.Equal(got, []string{"b"}) || !slices.Equal(p.settled, []string{"a: Closed in dv"}) {
		t.Fatalf("open %v, settled %v", got, p.settled)
	}
}
