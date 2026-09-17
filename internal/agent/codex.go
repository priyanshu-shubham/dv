package agent

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"dv/internal/codex"
	"dv/internal/permit"
	"dv/internal/store"
)

// codexSide is the Agent view's Codex sessions in one folder: the threads
// Codex has there, the ones dv runs, and what a new one would start with.
type codexSide struct {
	root   string
	broker *permit.Broker
	saved  *store.Sessions
	client *codex.Client

	mu        sync.Mutex
	threads   map[string]*codexThread
	listed    []codex.Thread
	listedAt  time.Time
	listing   bool
	opts      Options
	optsAt    time.Time
	probing   bool
	skillList []codex.Skill
	usage     *Usage
	usageAt   time.Time
	asking    bool
}

func newCodexSide(root string, broker *permit.Broker, saved *store.Sessions) *codexSide {
	return &codexSide{root: root, broker: broker, saved: saved, client: codex.Shared(), threads: map[string]*codexThread{}}
}

// thread is the thread under id, made ready to hear from the app-server.
func (c *codexSide) thread(id string) *codexThread {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.threads[id]
	if t == nil {
		t = newCodexThread(c, id)
		c.threads[id] = t
		c.client.Handle(id, t)
	}
	return t
}

// has reports whether id is a Codex thread in this folder, asking Codex when
// the list has not said; sure is false when Codex could not be asked.
func (c *codexSide) has(id string) (mine, sure bool) {
	c.mu.Lock()
	_, ours := c.threads[id]
	listed := slices.ContainsFunc(c.listed, func(t codex.Thread) bool { return t.ID == id })
	c.mu.Unlock()
	switch {
	case ours || listed:
		return true, true
	case !codex.Available():
		return false, true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var r struct {
		Thread codex.Thread `json:"thread"`
	}
	err := c.client.Call(ctx, "thread/read", map[string]any{"threadId": id}, &r)
	var rpc *codex.Error
	if err != nil && !errors.As(err, &rpc) {
		return false, false
	}
	return err == nil && within(r.Thread.Cwd, c.root), true
}

// listFor is how long a listing stands before Codex is asked again.
const listFor = 2 * time.Second

// list is the threads Codex has in the folder, newest first. The first time it
// waits for Codex; after that it answers with what it has and asks again apart.
func (c *codexSide) list() []codex.Thread {
	c.mu.Lock()
	fresh := time.Since(c.listedAt) < listFor
	first := c.listedAt.IsZero()
	if fresh || c.listing || !codex.Available() {
		defer c.mu.Unlock()
		return c.listed
	}
	c.listing = true
	c.mu.Unlock()
	if first {
		c.relist()
	} else {
		go c.relist()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.listed
}

func (c *codexSide) relist() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var r struct {
		Data []codex.Thread `json:"data"`
	}
	err := c.client.Call(ctx, "thread/list", map[string]any{"cwd": c.root, "limit": 200, "sortKey": "updated_at"}, &r)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.listing, c.listedAt = false, time.Now()
	if err == nil {
		c.listed = slices.DeleteFunc(r.Data, func(t codex.Thread) bool { return t.Parent != nil || t.Ephemeral })
	}
}

// forgetList has the next listing ask Codex, as after a rename.
func (c *codexSide) forgetList() {
	c.mu.Lock()
	c.listedAt = time.Time{}.Add(time.Nanosecond)
	c.mu.Unlock()
}

// sessions are the folder's Codex threads as rows of the session list.
func (c *codexSide) sessions() []Session {
	listed := c.list()
	c.mu.Lock()
	threads := maps.Clone(c.threads)
	c.mu.Unlock()
	out := []Session{}
	seen := map[string]bool{}
	for _, th := range listed {
		seen[th.ID] = true
		row := Session{ID: th.ID, Agent: "codex", Cwd: th.Cwd, Updated: time.Unix(th.UpdatedAt, 0), Title: deref(th.Name), Prompt: cleanPrompt(th.Preview)}
		if th.GitInfo != nil {
			row.Branch = deref(th.GitInfo.Branch)
		}
		if t := threads[th.ID]; t != nil {
			c.fill(&row, t)
		} else if c.client.HeldElsewhere(th.ID) {
			row.Running = "terminal"
		}
		out = append(out, row)
	}
	// Made here and not sent to, which Codex does not list until it is.
	for id, t := range threads {
		if seen[id] {
			continue
		}
		t.mu.Lock()
		keep := t.loaded || c.saved.Has(id)
		row := Session{ID: id, Agent: "codex", Cwd: c.root, Updated: t.lastUsed, Title: deref(t.meta.Name)}
		t.mu.Unlock()
		if keep {
			c.fill(&row, t)
			out = append(out, row)
		}
	}
	return out
}

// fill adds to a row what dv knows of a thread it has seen.
func (c *codexSide) fill(row *Session, t *codexThread) {
	row.Running = t.running()
	row.Last, row.LastBy = t.last()
	t.mu.Lock()
	defer t.mu.Unlock()
	row.Busy = row.Running == "dv" && t.active != ""
	row.Status = t.status
	row.Context = t.context
	if t.meta.Name != nil {
		row.Title = *t.meta.Name
	}
}

// cleanPrompt is a message without what dv sent along with it.
func cleanPrompt(s string) string {
	s, _, _ = strings.Cut(s, "<dv-context>")
	return strings.TrimSpace(s)
}

// title names a thread for a prompt shown away from it.
func (c *codexSide) title(id string) string {
	c.mu.Lock()
	t := c.threads[id]
	listed := slices.IndexFunc(c.listed, func(t codex.Thread) bool { return t.ID == id })
	name := ""
	if listed >= 0 {
		name = cmp.Or(deref(c.listed[listed].Name), cleanPrompt(c.listed[listed].Preview))
	}
	c.mu.Unlock()
	if t != nil {
		t.mu.Lock()
		name = cmp.Or(deref(t.meta.Name), name, cleanPrompt(t.meta.Preview))
		t.mu.Unlock()
	}
	return name
}

// activity is what the threads dv runs or has open are doing.
func (c *codexSide) activity(open []string) []Activity {
	c.mu.Lock()
	threads := slices.Collect(maps.Values(c.threads))
	c.mu.Unlock()
	var out []Activity
	for _, t := range threads {
		running := t.running()
		if running != "dv" && !slices.Contains(open, t.id) {
			continue
		}
		a := Activity{ID: t.id, Agent: "codex", Running: running, Title: c.title(t.id)}
		if text, by := t.last(); by == "agent" {
			a.Last = text
		}
		t.mu.Lock()
		a.Busy, a.Updated = running == "dv" && t.active != "", t.lastUsed
		if t.meta.UpdatedAt > 0 {
			a.Updated = time.Unix(t.meta.UpdatedAt, 0)
		}
		t.mu.Unlock()
		out = append(out, a)
	}
	return out
}

// running reports whether dv runs the thread.
func (c *codexSide) running(id string) bool {
	c.mu.Lock()
	t := c.threads[id]
	c.mu.Unlock()
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.loaded
}

// create starts a thread in the folder. Nothing runs in it until it is sent to.
func (c *codexSide) create() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var r threadResponse
	if err := c.client.Call(ctx, "thread/start", map[string]any{"cwd": c.root}, &r); err != nil {
		return "", err
	}
	t := c.thread(r.Thread.ID)
	c.client.Keep(t.id, true)
	mode := c.options().Mode
	t.mu.Lock()
	t.loaded, t.settings, t.meta, t.history, t.whole = true, r.settings(), r.Thread, true, true
	// The page offered the session in the mode the config reads as; Codex starts
	// a folder it does not trust read-only whatever the config, so it is asked for.
	if mode != "" && modeOf(t.settings) != mode {
		t.asked.mode = &mode
	}
	t.mu.Unlock()
	if err := c.saved.SetAgent(t.id, "codex"); err != nil {
		return "", err
	}
	return t.id, nil
}

// optionsFor is how long what a session starts with stands before it is asked again.
const codexOptionsFor = 5 * time.Minute

// options are the models, starting mode and commands a Codex session has, or
// no models while Codex is asked the first time.
func (c *codexSide) options() Options {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.probing && time.Since(c.optsAt) > codexOptionsFor && codex.Available() {
		c.probing = true
		go c.probe()
	}
	return c.opts
}

func (c *codexSide) skills() []codex.Skill {
	c.options()
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.skillList
}

func (c *codexSide) probe() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var models struct {
		Data []codex.Model `json:"data"`
	}
	var config struct {
		Config struct {
			Model    *string `json:"model"`
			Effort   *string `json:"model_reasoning_effort"`
			Approval *string `json:"approval_policy"`
			Sandbox  *string `json:"sandbox_mode"`
		} `json:"config"`
	}
	var skills struct {
		Data []struct {
			Skills []codex.Skill `json:"skills"`
		} `json:"data"`
	}
	err := errors.Join(
		c.client.Call(ctx, "model/list", map[string]any{}, &models),
		c.client.Call(ctx, "config/read", map[string]any{"cwd": c.root}, &config),
	)
	c.client.Call(ctx, "skills/list", map[string]any{"cwds": []string{c.root}}, &skills)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.probing, c.optsAt = false, time.Now()
	if err != nil {
		if c.opts.Models == nil {
			c.opts = Options{Models: []Model{{ID: "", Label: "Default model"}}, Mode: "acceptEdits"}
		}
		return
	}
	cfg := config.Config
	o := Options{Model: deref(cfg.Model), Mode: configMode(deref(cfg.Approval), deref(cfg.Sandbox))}
	if o.Model == "" {
		for _, m := range models.Data {
			if m.IsDefault {
				o.Model = m.Model
			}
		}
	}
	mine := Model{ID: "", Model: o.Model, Label: o.Model}
	others := []Model{}
	for _, m := range models.Data {
		if m.Hidden {
			continue
		}
		efforts := make([]string, len(m.Efforts))
		for i, e := range m.Efforts {
			efforts[i] = e.Effort
		}
		if m.Model == o.Model {
			mine.Label, mine.Description, mine.Efforts, mine.Effort = m.DisplayName, m.Description, efforts, m.Default
			if e := deref(cfg.Effort); slices.Contains(efforts, e) {
				mine.Effort = e
			}
			continue
		}
		others = append(others, Model{ID: m.ID, Model: m.Model, Label: m.DisplayName, Description: m.Description, Efforts: efforts, Effort: m.Default})
	}
	o.Models = append([]Model{mine}, others...)
	o.Commands = []Command{{Name: "compact", Description: "Summarise the conversation so far to free up context"}}
	c.skillList = nil
	for _, d := range skills.Data {
		for _, s := range d.Skills {
			if !s.Enabled {
				continue
			}
			c.skillList = append(c.skillList, s)
			o.Commands = append(o.Commands, Command{Name: s.Name, Description: cut(cmp.Or(deref(s.Short), s.Description), 200)})
		}
	}
	c.opts = o
}

// configMode reads the user's Codex config as the mode a new session starts in.
func configMode(approval, sandbox string) string {
	switch {
	case sandbox == "read-only" && approval == "untrusted":
		return "default"
	case sandbox == "danger-full-access":
		return "fullAccess"
	}
	return "acceptEdits"
}

// usageNow is the account's rate limits as last heard, asked again once a
// minute old.
func (c *codexSide) usageNow() *Usage {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.asking || time.Since(c.usageAt) < time.Minute || !codex.Available() || !c.client.Running() && !c.usageAt.IsZero() {
		return c.usage
	}
	c.asking = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var r struct {
			Limits codex.RateLimits `json:"rateLimits"`
		}
		err := c.client.Call(ctx, "account/rateLimits/read", nil, &r)
		c.mu.Lock()
		defer c.mu.Unlock()
		c.asking, c.usageAt = false, time.Now()
		if err == nil {
			c.usage = usageOf(r.Limits)
		}
	}()
	return c.usage
}

// spent has the next look at the limits ask again, after a turn used some.
func (c *codexSide) spent() {
	c.mu.Lock()
	c.usageAt = time.Time{}.Add(time.Nanosecond)
	c.mu.Unlock()
}

func usageOf(l codex.RateLimits) *Usage {
	u := &Usage{}
	for _, w := range []*codex.RateWindow{l.Primary, l.Secondary} {
		if w == nil {
			continue
		}
		limit := &Limit{Percent: w.UsedPercent}
		if w.ResetsAt != nil {
			limit.ResetsAt = time.Unix(*w.ResetsAt, 0).UTC().Format(time.RFC3339)
		}
		switch {
		case w.Minutes != nil && *w.Minutes <= 5*60:
			u.Session = limit
		default:
			u.Week = limit
		}
	}
	if u.Session == nil && u.Week == nil {
		return nil
	}
	return u
}

// item is what a tool id names, for an edit, output or image: an item in the
// thread's turns - or in those of an agent it started, whose calls the page
// asks for under the session - and for a file change, which of its changes.
func (c *codexSide) item(id, tool string) (codex.Item, int, error) {
	c.thread(id).readHistory(false)
	itemID, n := tool, -1
	if i := strings.LastIndexByte(tool, ':'); i > 0 {
		fmt.Sscan(tool[i+1:], &n)
		itemID = tool[:i]
	}
	c.mu.Lock()
	threads := slices.Collect(maps.Values(c.threads))
	c.mu.Unlock()
	for _, t := range threads {
		t.mu.Lock()
		for _, turn := range t.turns {
			for _, it := range turn.Items {
				if it.ID == itemID || it.ID == tool {
					t.mu.Unlock()
					return it, n, nil
				}
			}
		}
		t.mu.Unlock()
	}
	return codex.Item{}, 0, fmt.Errorf("no call %s in this session", tool)
}

func (c *codexSide) edit(id, tool string) (*EditDiff, error) {
	it, n, err := c.item(id, tool)
	if err != nil {
		return nil, err
	}
	if n < 0 || n >= len(it.Changes) {
		return nil, errNoEdit
	}
	return codexEdit(c.root, it.Changes[n], true)
}

func (c *codexSide) output(id, tool string) (*Output, error) {
	it, _, err := c.item(id, tool)
	if err != nil {
		return nil, err
	}
	return codexOutput(it), nil
}

func (c *codexSide) image(id, tool string) (*Image, error) {
	it, _, err := c.item(id, tool)
	if err != nil {
		return nil, err
	}
	path := it.Path
	if it.SavedPath != nil {
		path = *it.SavedPath
	}
	if filepath.IsAbs(path) {
		if img, err := imageFile(path); err == nil {
			return img, nil
		}
	}
	// A picture Codex made and did not save, or has moved since, is still in the thread.
	var data string
	if json.Unmarshal(it.Result, &data) == nil && data != "" {
		if b, err := base64.StdEncoding.DecodeString(data); err == nil {
			if t := http.DetectContentType(b); ImageTypes[t] {
				return &Image{MediaType: t, Data: b}, nil
			}
		}
	}
	return nil, errors.New("the picture is not where Codex left it")
}

func (c *codexSide) promptImage(id, message string, n int) (*Image, error) {
	t := c.thread(id)
	t.readHistory(true)
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, turn := range t.turns {
		for _, it := range turn.Items {
			if it.Type == "userMessage" && cmp.Or(it.ClientID, it.ID) == message || it.ID == message {
				return inputImage(it.Inputs(), n)
			}
		}
	}
	return nil, fmt.Errorf("no message %s in this session", message)
}

// agentThread is the thread a collab call started or wrote to.
func (c *codexSide) agentThread(id, call string) *codexThread {
	it, _, err := c.item(id, call)
	if err != nil || len(it.Receivers) == 0 {
		return nil
	}
	return c.thread(it.Receivers[0])
}

// reap lets go of threads dv has loaded and nothing has used for a while, so
// the app-server can stop. They load again on the next message.
func (c *codexSide) reap(after time.Duration) {
	c.mu.Lock()
	threads := slices.Collect(maps.Values(c.threads))
	c.mu.Unlock()
	for _, t := range threads {
		t.mu.Lock()
		idle := t.loaded && t.active == "" && len(t.asks) == 0 && len(t.held) == 0 && len(t.subs) == 0 && time.Since(t.lastUsed) > after
		t.mu.Unlock()
		if idle {
			t.release()
		}
	}
}

func (c *codexSide) close() {
	c.mu.Lock()
	threads := slices.Collect(maps.Values(c.threads))
	c.mu.Unlock()
	var wg sync.WaitGroup
	for _, t := range threads {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t.release()
		}()
	}
	wg.Wait()
}
