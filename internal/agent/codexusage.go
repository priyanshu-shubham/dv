package agent

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
)

// ClientVersion is the dv version Codex is told it is talking to.
var ClientVersion = "dev"

var codexUsage struct {
	sync.Mutex
	usage  *Usage
	next   time.Time
	asking bool
}

// CodexUsage reports the Codex sign-in's plan limits as last heard, nil
// without a ChatGPT sign-in. The gateway's replies carry none, so a Codex
// app-server is started to ask, at most once a minute and only while
// something asks here: a page open on the Agent view.
func CodexUsage() *Usage {
	c := &codexUsage
	c.Lock()
	defer c.Unlock()
	if c.asking || time.Now().Before(c.next) {
		return c.usage
	}
	if _, err := exec.LookPath("codex"); err != nil {
		c.usage, c.next = nil, time.Now().Add(optionsFor)
		return nil
	}
	c.asking = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		raw, err := readCodexLimits(ctx)
		c.Lock()
		defer c.Unlock()
		c.asking = false
		if err != nil {
			// Not signed in with ChatGPT, or no network: not worth an app-server a minute.
			c.usage, c.next = nil, time.Now().Add(optionsFor)
			return
		}
		c.usage, c.next = codexUsageFrom(raw), time.Now().Add(time.Minute)
	}()
	return c.usage
}

func readCodexLimits(ctx context.Context) (json.RawMessage, error) {
	cmd := exec.Command("codex", "app-server")
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}
	// npm's codex is a script that starts the real one, which a kill of the
	// script alone would leave running.
	ownGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer func() {
		stdin.Close()
		killGroup(cmd)
		cmd.Wait()
	}()
	stop := context.AfterFunc(ctx, func() { killGroup(cmd) })
	defer stop()

	enc := json.NewEncoder(stdin)
	for _, msg := range []map[string]any{
		{"id": 1, "method": "initialize", "params": map[string]any{"clientInfo": map[string]any{"name": "dv", "title": "dv", "version": ClientVersion}}},
		{"method": "initialized"},
		{"id": 2, "method": "account/rateLimits/read"},
	} {
		if err := enc.Encode(msg); err != nil {
			return nil, err
		}
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(nil, 4<<20)
	for sc.Scan() {
		var m struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil || m.ID == nil {
			continue
		}
		if m.Error != nil {
			return nil, errors.New(m.Error.Message)
		}
		if *m.ID == 2 {
			return m.Result, nil
		}
	}
	return nil, cmp.Or(ctx.Err(), sc.Err(), errors.New("Codex stopped without answering"))
}

type codexWindow struct {
	UsedPercent float64 `json:"usedPercent"`
	Minutes     *int64  `json:"windowDurationMins"`
	ResetsAt    *int64  `json:"resetsAt"`
}

type codexLimits struct {
	ID        string       `json:"limitId"`
	Name      *string      `json:"limitName"`
	Primary   *codexWindow `json:"primary"`
	Secondary *codexWindow `json:"secondary"`
}

func (w *codexWindow) limit() *Limit {
	l := &Limit{Percent: w.UsedPercent}
	if w.Minutes != nil {
		l.Window = *w.Minutes
	}
	if w.ResetsAt != nil {
		l.ResetsAt = time.Unix(*w.ResetsAt, 0).UTC().Format(time.RFC3339)
	}
	return l
}

func codexUsageFrom(raw json.RawMessage) *Usage {
	var r struct {
		Plan *codexLimits           `json:"rateLimits"`
		ByID map[string]codexLimits `json:"rateLimitsByLimitId"`
	}
	if json.Unmarshal(raw, &r) != nil || r.Plan == nil {
		return nil
	}
	u := &Usage{}
	for _, w := range []*codexWindow{r.Plan.Primary, r.Plan.Secondary} {
		if w == nil {
			continue
		}
		if w.Minutes != nil && *w.Minutes <= 5*60 {
			u.Session = w.limit()
		} else {
			u.Week = w.limit()
		}
	}
	// A model with limits of its own: of its windows, the nearer to running out.
	for id, l := range r.ByID {
		if id == r.Plan.ID || l.Name == nil || *l.Name == "" {
			continue
		}
		w := l.Primary
		if w == nil || l.Secondary != nil && l.Secondary.UsedPercent > w.UsedPercent {
			w = l.Secondary
		}
		if w != nil {
			m := w.limit()
			m.Model = *l.Name
			u.Models = append(u.Models, m)
		}
	}
	slices.SortFunc(u.Models, func(a, b *Limit) int { return strings.Compare(a.Model, b.Model) })
	if u.Session == nil && u.Week == nil && u.Models == nil {
		return nil
	}
	return u
}
