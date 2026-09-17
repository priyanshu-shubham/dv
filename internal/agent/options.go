package agent

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"dv/internal/permit"
)

// Model is one of the models a session can be switched to.
type Model struct {
	ID          string   `json:"id"`              // what --model takes; "" leaves it to the user's settings
	Model       string   `json:"model,omitempty"` // the model ID stands for today
	Label       string   `json:"label"`
	Description string   `json:"description,omitempty"`
	Efforts     []string `json:"efforts,omitempty"` // the effort levels it takes, if any
	Effort      string   `json:"effort,omitempty"`  // the one the user's settings give it
}

// Options are what a session in dv starts with and can pick from.
type Options struct {
	Models  []Model `json:"models"`
	Model   string  `json:"model,omitempty"` // the model a session starts on, from the user's settings
	Mode    string  `json:"mode,omitempty"`  // the permission mode it starts in
	Context Context `json:"-"`               // that model's window
	// The slash commands a session dv runs takes: the terminal's own that work
	// without it, and the user's and the repository's commands and skills.
	Commands []Command `json:"-"`
}

type Command struct {
	Name        string `json:"name"` // without its slash
	Description string `json:"description,omitempty"`
	Hint        string `json:"argumentHint,omitempty"` // what to write after it
}

// Context is how full a session's context window is.
type Context struct {
	Used int `json:"used"`
	Max  int `json:"max"`
	// Compact is where Claude Code compacts the conversation; 0 when it does not.
	Compact int `json:"compact,omitempty"`
}

// Usage is how much of a Claude plan's rate limits the account has used.
type Usage struct {
	Session *Limit `json:"session,omitempty"` // the five-hour window
	Week    *Limit `json:"week,omitempty"`
}

type Limit struct {
	Percent  float64 `json:"percent"`
	ResetsAt string  `json:"resetsAt,omitempty"`
}

// Aliases, for when Claude Code could not be asked: each is the latest of its
// kind, whichever that is.
var fallbackModels = []Model{{ID: "", Label: "Default model"}, {ID: "fable", Label: "Fable"}, {ID: "opus", Label: "Opus"}, {ID: "sonnet", Label: "Sonnet"}, {ID: "haiku", Label: "Haiku"}}

// optionsFor is how old an answer may be before Claude Code is asked again,
// so a change to the user's settings shows up without restarting dv.
const optionsFor = 5 * time.Minute

// Options reports the models and starting permission mode as Claude Code sees
// them, or nil models while it is still being asked the first time.
func (m *Manager) Options() Options {
	m.mu.Lock()
	defer m.mu.Unlock()
	// A setting changed in the terminal - the model, the compaction window - is
	// asked about now rather than when the last answer grows old.
	if time.Since(m.settingsLooked) > 2*time.Second {
		m.settingsLooked = time.Now()
		if at := settingsAt(m.root); !at.Equal(m.settingsAt) {
			m.settingsAt, m.optsAt = at, time.Time{}
		}
	}
	if !m.probing && time.Since(m.optsAt) > optionsFor && Available() {
		m.probing = true
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			o, u, err := probe(ctx, m.root)
			m.mu.Lock()
			m.probing, m.optsAt = false, time.Now()
			if err == nil {
				m.opts, m.usage, m.usageAt = o, u, time.Now()
			} else if m.opts.Models == nil {
				m.opts = Options{Models: fallbackModels}
			}
			following := slices.Collect(maps.Keys(m.follows))
			procs := slices.Collect(maps.Values(m.procs))
			m.mu.Unlock()
			// Their context windows are known now, or have changed; a session
			// dv runs is asked for its own.
			for _, p := range procs {
				if m.runs(p.id) {
					go p.refresh()
				}
			}
			for _, id := range following {
				m.signal(id, false)
			}
		}()
	}
	return m.opts
}

// Usage reports the plan's limits as last heard, nil for an account without
// any. Once that is a minute old it asks again: through a session dv runs when
// there is one, which is a line on a pipe, or else - less often - through a
// Claude Code started for the purpose.
func (m *Manager) Usage() *Usage {
	m.mu.Lock()
	defer m.mu.Unlock()
	age := time.Since(m.usageAt)
	if m.usageAsked || age < time.Minute || !Available() {
		return m.usage
	}
	var via *proc
	for _, p := range m.procs {
		p.mu.Lock()
		if p.cmd != nil {
			via = p
		}
		p.mu.Unlock()
	}
	if via == nil && age < optionsFor {
		return m.usage
	}
	m.usageAsked = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		req := map[string]any{"subtype": "get_usage", "skip_behaviors": true}
		var raw json.RawMessage
		var err error
		if via != nil {
			raw, err = via.request(ctx, req)
		} else {
			var got map[string]json.RawMessage
			got, err = ask(ctx, m.root, req)
			raw = got["get_usage"]
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		m.usageAsked, m.usageAt = false, time.Now()
		if err == nil {
			m.usage = usageFrom(raw)
		}
	}()
	return m.usage
}

// probe asks Claude Code what a session would start with, and how much of the
// plan is used.
func probe(ctx context.Context, cwd string) (Options, *Usage, error) {
	got, err := ask(ctx, cwd,
		map[string]any{"subtype": "get_settings"},
		map[string]any{"subtype": "get_context_usage", "detail": "summary"},
		map[string]any{"subtype": "get_usage", "skip_behaviors": true},
	)
	if err != nil {
		return Options{}, nil, err
	}
	var init initResponse
	if json.Unmarshal(got["initialize"], &init) != nil {
		return Options{}, nil, errors.New("Claude Code would not start")
	}
	// An older Claude Code refuses what it does not know, and still offers its models.
	var s settingsResponse
	json.Unmarshal(got["get_settings"], &s)
	o := offer(&init, &s)
	o.Context = contextOf(got["get_context_usage"])
	o.Context.Used = 0
	return o, usageFrom(got["get_usage"]), nil
}

// ask starts a Claude Code with no session, initializes it and puts control
// requests to it, returning the answers by subtype - nil for one it refused.
// It answers them before any message is sent, so no model is called.
func ask(ctx context.Context, cwd string, reqs ...map[string]any) (map[string]json.RawMessage, error) {
	cmd := exec.Command("claude", "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose")
	cmd.Dir = cwd
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
	exited := make(chan struct{})
	go func() {
		cmd.Wait()
		close(exited)
	}()
	defer func() {
		stdin.Close()
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			killGroup(cmd)
		}
	}()
	go func() {
		select {
		case <-ctx.Done():
			killGroup(cmd)
		case <-exited:
		}
	}()

	reqs = append([]map[string]any{{"subtype": "initialize"}}, reqs...)
	for _, r := range reqs {
		b, _ := json.Marshal(map[string]any{"type": "control_request", "request_id": "dv-" + r["subtype"].(string), "request": r})
		stdin.Write(append(b, '\n'))
	}
	got := map[string]json.RawMessage{}
	rd := bufio.NewReaderSize(stdout, 1<<16)
	for len(got) < len(reqs) {
		line, err := rd.ReadBytes('\n')
		var m struct {
			Type     string `json:"type"`
			Response struct {
				Subtype   string          `json:"subtype"`
				RequestID string          `json:"request_id"`
				Response  json.RawMessage `json:"response"`
			} `json:"response"`
		}
		if json.Unmarshal(line, &m) == nil && m.Type == "control_response" {
			if sub, ok := strings.CutPrefix(m.Response.RequestID, "dv-"); ok {
				got[sub] = nil
				if m.Response.Subtype == "success" {
					got[sub] = m.Response.Response
				}
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, errors.New("Claude Code exited without answering")
		}
	}
	return got, nil
}

type initResponse struct {
	Models   []offered `json:"models"`
	Mode     string    `json:"current_permission_mode"`
	Commands []Command `json:"commands"`
}

type offered struct {
	Value         string   `json:"value"`
	ResolvedModel string   `json:"resolvedModel"`
	DisplayName   string   `json:"displayName"`
	Description   string   `json:"description"`
	Efforts       []string `json:"supportedEffortLevels"`
}

type settingsResponse struct {
	Effective struct {
		Effort string `json:"effortLevel"`
		Models map[string]struct {
			Effort string `json:"effortLevel"`
		} `json:"modelSettings"`
	} `json:"effective"`
	// What is in force: the model, and the effort it runs at.
	Applied struct {
		Model  string `json:"model"`
		Effort string `json:"effort"`
	} `json:"applied"`
}

// offer turns what Claude Code offers into the picker's list, led by the model
// the user's settings start on, as "". Claude Code's own "default" is the model
// it recommends whatever the settings say, so it is only kept when nothing
// else in the list is that model.
func offer(init *initResponse, s *settingsResponse) Options {
	o := Options{Model: s.Applied.Model, Mode: init.Mode}
	if o.Model == "" {
		// An older Claude Code, with only its recommendation to go on.
		for _, c := range init.Models {
			if c.Value == "default" {
				o.Model = c.ResolvedModel
			}
		}
	}
	// The effort a model runs at unless told otherwise: its own setting, the
	// general one, or for the model in force, what it says it runs at.
	effort := func(c offered) string {
		e := cmp.Or(s.Effective.Models[strings.TrimSuffix(c.ResolvedModel, "[1m]")].Effort, s.Effective.Effort)
		if c.ResolvedModel == s.Applied.Model && s.Applied.Effort != "" {
			e = s.Applied.Effort
		}
		if !slices.Contains(c.Efforts, e) {
			return ""
		}
		return e
	}
	mine := Model{ID: "", Model: o.Model, Label: o.Model}
	others := []Model{}
	for _, c := range init.Models {
		// "Opus 5 with 1M context · Best for everyday, complex tasks" names the
		// model where the display name says only "Opus (1M context)".
		label, desc, ok := strings.Cut(c.Description, " · ")
		if !ok {
			label, desc = c.DisplayName, c.Description
		}
		switch {
		case c.ResolvedModel == o.Model:
			mine.Label, mine.Description, mine.Efforts, mine.Effort = label, desc, c.Efforts, effort(c)
		case c.Value == "default" && slices.ContainsFunc(init.Models, func(x offered) bool {
			return x.Value != "default" && x.ResolvedModel == c.ResolvedModel
		}):
		default:
			others = append(others, Model{ID: c.Value, Model: c.ResolvedModel, Label: label, Description: desc, Efforts: c.Efforts, Effort: effort(c)})
		}
	}
	o.Models = append([]Model{mine}, others...)
	for _, c := range init.Commands {
		if notInDV[c.Name] {
			continue
		}
		if c.Name == "clear" {
			c.Description, c.Hint = "Start a new session, as + does", ""
		}
		// A skill's description runs to a paragraph; a suggestion shows a line.
		c.Description = cut(c.Description, 200)
		o.Commands = append(o.Commands, c)
	}
	return o
}

// notInDV are commands Claude Code offers that have no place in dv: /color
// tints the terminal's prompt, /fast is not for a process dv runs, and the
// rest are Claude Code's own plumbing. /clear stays, as the page's + - sent,
// it would move Claude Code on to a session the page is not showing.
var notInDV = map[string]bool{"color": true, "fast": true, "__remote-workflow": true, "workflow-launch-exec": true}

func contextOf(raw json.RawMessage) Context {
	var c struct {
		Used      int  `json:"totalTokens"`
		Max       int  `json:"maxTokens"`
		Threshold int  `json:"autoCompactThreshold"`
		Auto      bool `json:"isAutoCompactEnabled"`
	}
	json.Unmarshal(raw, &c)
	if !c.Auto {
		c.Threshold = 0
	}
	return Context{c.Used, c.Max, c.Threshold}
}

func usageFrom(raw json.RawMessage) *Usage {
	type window struct {
		Utilization *float64 `json:"utilization"`
		ResetsAt    string   `json:"resets_at"`
	}
	var u struct {
		Available bool `json:"rate_limits_available"`
		Limits    struct {
			Session *window `json:"five_hour"`
			Week    *window `json:"seven_day"`
		} `json:"rate_limits"`
	}
	if json.Unmarshal(raw, &u) != nil || !u.Available {
		return nil
	}
	limit := func(w *window) *Limit {
		if w == nil || w.Utilization == nil {
			return nil
		}
		return &Limit{*w.Utilization, w.ResetsAt}
	}
	return &Usage{limit(u.Limits.Session), limit(u.Limits.Week)}
}

// settingsAt is when Claude Code's settings last changed: the user's, and the
// repository's shared and local ones.
func settingsAt(root string) time.Time {
	paths := []string{filepath.Join(root, ".claude", "settings.json"), filepath.Join(root, ".claude", "settings.local.json")}
	if cfg, err := permit.ConfigDir(); err == nil {
		paths = append(paths, filepath.Join(cfg, "settings.json"))
	}
	var at time.Time
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil && fi.ModTime().After(at) {
			at = fi.ModTime()
		}
	}
	return at
}

// windowFor is the context a session on model has, as best dv can tell without
// asking the session: the user's default model's, for that model, else the
// window every model has, compacted as far short of its end. Until Claude Code
// has been asked, it is not known at all.
func windowFor(model string, o Options) Context {
	c := o.Context
	if c.Max == 0 && o.Models == nil {
		return Context{}
	}
	if c.Max == 0 {
		return Context{Max: 200_000}
	}
	if model == "" || strings.TrimSuffix(model, "[1m]") != strings.TrimSuffix(o.Model, "[1m]") {
		// A window set in the settings smaller than every model's holds for all.
		max := min(c.Max, 200_000)
		if c.Compact > 0 {
			c.Compact = max - (c.Max - c.Compact)
		}
		c.Max = max
	}
	return c
}
