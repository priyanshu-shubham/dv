package agent

import (
	"bufio"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"dv/internal/permit"
	"dv/internal/store"
)

// Block is part of a reply still streaming in: the page draws it until the
// transcript has it.
type Block struct {
	msg   string
	index int
	Kind  string `json:"kind"` // text | thinking | tool
	Text  string `json:"text,omitempty"`
	Tool  string `json:"tool,omitempty"`
}

// proc is a session dv runs: `claude -p` reading stream-json on stdin and
// writing it to stdout, kept running between turns so each message is not a
// fresh start.
type proc struct {
	id, cwd string
	broker  *permit.Broker
	changed func()        // its live state moved
	wrote   func()        // it put something in the transcript
	sent    func()        // a message went to it
	ended   func()        // a turn is over
	named   func() string // the name its transcript last gave it, for a resume
	// suggests says whether to have Claude Code guess the next message after
	// each turn; read at each start.
	suggests func() bool
	// picked keeps a model chosen with /model, which Claude Code keeps "for
	// this session only": this process, not the next.
	picked func(model, effort string)
	// fellBack is told of a model Claude Code moved off, after the message it
	// refused, if one did.
	fellBack func(at string, sw store.ModelSwitch)

	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	exited chan struct{}
	// ran is when this process started: work it was told of before then was
	// another process's, and went with it.
	ran  time.Time
	busy bool
	// Whether Claude Code says when a turn is over. A result does not: the
	// next message can already be on its way when the last one's arrives.
	states  bool
	queued  []Queued // messages sent while busy, until a turn takes them up
	stopped bool     // the process was stopped on purpose, so its exit is no error
	// Whether the next start continues a transcript, and at which message when
	// the conversation was rewound.
	resume   bool
	resumeAt string
	// model is the one asked for, "" for the user's default; using is the one
	// the process reports. mode is the permission mode the session is in, which
	// Claude Code also changes itself, as when a plan is approved.
	model string
	using string
	mode  string
	// effort likewise; neither the stream nor the transcript says the effort in
	// force, or how full the context is, so Claude Code is asked after a turn.
	effort      string
	effortUsing string
	context     *Context
	title       string // a name for a session not yet started
	status      string
	err         string
	cost        float64
	blocks      []*Block
	msg         string // the API message streaming now
	replies     map[string]chan reply
	asks        map[string]context.CancelFunc
	lastUsed    time.Time
	since       time.Time // when the turn going on began
	nextReq     int
	planCall    string // an EnterPlanMode call not yet answered
	suggestion  string // the next message Claude Code guessed, until one is sent
	// Turn ends still to keep the model in force at, after a /model: two,
	// as one sent while Claude works may only be taken up at the second.
	modelCmd int
	apiErr      string // the API error Claude Code last said, which its transcript holds

	wmu sync.Mutex // one write to stdin at a time
}

type reply struct {
	body json.RawMessage
	err  error
}

// Queued is a message sent while Claude works, waiting for the step it is on.
// Its uuid is the one it goes into the transcript under.
type Queued struct {
	UUID   string `json:"uuid"`
	Text   string `json:"text"`
	Images int    `json:"images,omitempty"`
}

func (p *proc) args() []string {
	args := []string{
		"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--include-partial-messages", "--permission-prompt-tool", "stdio",
	}
	if p.resume {
		args = append(args, "--resume", p.id)
		if p.resumeAt != "" {
			args = append(args, "--resume-session-at", p.resumeAt)
		}
	} else {
		args = append(args, "--session-id", p.id)
	}
	if p.model != "" {
		args = append(args, "--model", p.model)
	}
	if p.mode != "" {
		args = append(args, "--permission-mode", p.mode)
	}
	if p.effort != "" {
		args = append(args, "--effort", p.effort)
	}
	if p.suggests != nil && p.suggests() {
		args = append(args, "--prompt-suggestions")
	}
	// The name other sessions send to. A headless resume does not take back the
	// one the transcript keeps, as the terminal does, so it is passed again.
	name := p.title
	if p.resume && p.named != nil {
		name = cmp.Or(p.named(), p.title)
	}
	if name != "" {
		args = append(args, "--name", name)
	}
	return args
}

// startLocked runs the process if it is not running. Callers hold p.mu.
func (p *proc) startLocked() error {
	if p.cmd != nil {
		return nil
	}
	cmd := exec.Command("claude", p.args()...)
	cmd.Dir = p.cwd
	// Without the first, Claude Code keeps no copies of the files it edits for
	// a session driven this way, and rewinding the code has nothing to go back
	// to. The second has it say when a turn is over.
	cmd.Env = append(os.Environ(), "CLAUDE_CODE_ENABLE_SDK_FILE_CHECKPOINTING=1", "CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS=1")
	ownGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr := &tailBuffer{max: 4096}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("the `claude` CLI is not on PATH")
		}
		return err
	}
	p.cmd, p.stdin, p.exited, p.stopped, p.err, p.states = cmd, stdin, make(chan struct{}), false, "", false
	p.replies, p.asks = map[string]chan reply{}, map[string]context.CancelFunc{}
	p.lastUsed = time.Now()
	p.ran = p.lastUsed
	read := make(chan struct{})
	go p.read(stdout, read)
	go p.wait(cmd, read, p.exited, stderr)

	// A note the reader wrote when allowing a call can only reach Claude beside
	// the call's result, which this hook is how dv hears of.
	go func() {
		p.request(context.Background(), map[string]any{
			"subtype": "initialize",
			"hooks":   map[string]any{"PostToolUse": []any{map[string]any{"matcher": nil, "hookCallbackIds": []string{"note"}}}},
		})
		p.refresh()
	}()
	return nil
}

func (p *proc) turnOver() {
	model, effort := p.refresh()
	p.mu.Lock()
	keep := p.modelCmd > 0 && model != ""
	if keep {
		p.modelCmd--
		if effort != "" {
			effort = p.effort // a model without effort levels would not start with one
		}
	}
	p.mu.Unlock()
	// The model in force after a /model is the one it set or, where it named
	// none there is, the one it left: either way, one that starts.
	if keep && p.picked != nil {
		p.picked(model, effort)
	}
	p.ended()
}

// refresh asks Claude Code how full the context is and what effort it runs at,
// and reports the model and effort in force.
func (p *proc) refresh() (model, effort string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	used, err := p.request(ctx, map[string]any{"subtype": "get_context_usage", "detail": "summary"})
	var s settingsResponse
	raw, err2 := p.request(ctx, map[string]any{"subtype": "get_settings"})
	p.mu.Lock()
	if c := contextOf(used); err == nil && c.Max > 0 {
		p.context = &c
	}
	if err2 == nil && json.Unmarshal(raw, &s) == nil {
		p.effortUsing = s.Applied.Effort
	}
	p.mu.Unlock()
	p.changed()
	return s.Applied.Model, s.Applied.Effort
}

func (p *proc) wait(cmd *exec.Cmd, read, exited chan struct{}, stderr *tailBuffer) {
	// Wait closes stdout, so everything the process wrote is read first.
	<-read
	err := cmd.Wait()
	defer close(exited)
	p.mu.Lock()
	if p.cmd != cmd {
		p.mu.Unlock()
		return
	}
	if err != nil && !p.stopped {
		p.err = strings.TrimSpace(stderr.String())
		if p.err == "" {
			p.err = "Claude Code stopped: " + err.Error()
		}
	}
	for _, cancel := range p.asks {
		cancel()
	}
	for _, ch := range p.replies {
		deliver(ch, reply{err: errors.New("Claude Code stopped")})
	}
	p.cmd, p.stdin, p.busy, p.blocks, p.status, p.queued = nil, nil, false, nil, "", nil
	p.mu.Unlock()
	p.changed()
}

// stop ends the process; the session stays on disk to be resumed.
func (p *proc) stop() {
	p.mu.Lock()
	cmd, stdin, exited := p.cmd, p.stdin, p.exited
	p.stopped = true
	p.mu.Unlock()
	if cmd == nil {
		return
	}
	// Closing stdin is how a stream-json session is told it is over; one that
	// does not finish promptly goes the hard way, with anything it started.
	stdin.Close()
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		killGroup(cmd)
		<-exited
	}
}

func (p *proc) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	p.mu.Lock()
	stdin := p.stdin
	p.mu.Unlock()
	if stdin == nil {
		return errors.New("Claude Code is not running")
	}
	p.wmu.Lock()
	defer p.wmu.Unlock()
	_, err = stdin.Write(append(b, '\n'))
	return err
}

// send starts a turn, or joins the one running: Claude Code folds a message
// sent while it works into what it is doing.
func (p *proc) send(id, text string, images []Image) (string, error) {
	var content any = text
	if len(images) > 0 {
		blocks := make([]map[string]any, 0, len(images)+1)
		for _, img := range images {
			blocks = append(blocks, map[string]any{"type": "image", "source": map[string]any{
				"type": "base64", "media_type": img.MediaType, "data": base64.StdEncoding.EncodeToString(img.Data),
			}})
		}
		if text != "" {
			blocks = append(blocks, map[string]any{"type": "text", "text": text})
		}
		content = blocks
	}
	p.mu.Lock()
	if err := p.startLocked(); err != nil {
		p.mu.Unlock()
		return "", err
	}
	// The rewind is made by this message being written after the message it
	// resumed at; a later start must continue from here instead.
	p.resumeAt, p.suggestion = "", ""
	if f := strings.Fields(text); len(f) == 2 && f[0] == "/model" {
		p.modelCmd = 2
	}
	if p.busy {
		// It waits for the step Claude is on, unseen in the transcript till then.
		p.queued = append(p.queued, Queued{id, text, len(images)})
	} else {
		p.blocks, p.since = nil, time.Now()
	}
	p.busy, p.err, p.lastUsed = true, "", time.Now()
	p.mu.Unlock()
	p.changed()
	err := p.write(map[string]any{
		"type":               "user",
		"uuid":               id,
		"message":            map[string]any{"role": "user", "content": content},
		"parent_tool_use_id": nil,
		"session_id":         "",
	})
	if err != nil {
		return "", err
	}
	p.mu.Lock()
	p.resume = true // there is a transcript now, which a restart continues
	p.mu.Unlock()
	p.sent()
	return id, nil
}

// unqueue forgets a queued message, taken up or taken back.
func (p *proc) unqueue(id string) {
	p.mu.Lock()
	n := len(p.queued)
	p.queued = slices.DeleteFunc(p.queued, func(q Queued) bool { return q.UUID == id })
	gone := len(p.queued) < n
	p.mu.Unlock()
	if gone {
		p.changed()
	}
}

// request sends Claude Code a control request and waits for its reply.
func (p *proc) request(ctx context.Context, req map[string]any) (json.RawMessage, error) {
	p.mu.Lock()
	if p.cmd == nil {
		p.mu.Unlock()
		return nil, errors.New("Claude Code is not running")
	}
	p.nextReq++
	id := "dv-" + strconv.Itoa(p.nextReq)
	ch := make(chan reply, 1)
	p.replies[id] = ch
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.replies, id)
		p.mu.Unlock()
	}()

	if err := p.write(map[string]any{"type": "control_request", "request_id": id, "request": req}); err != nil {
		return nil, err
	}
	select {
	case r := <-ch:
		return r.body, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *proc) respond(id string, body any) {
	p.write(map[string]any{
		"type":     "control_response",
		"response": map[string]any{"subtype": "success", "request_id": id, "response": body},
	})
}

func (p *proc) read(stdout io.Reader, done chan struct{}) {
	defer close(done)
	rd := bufio.NewReaderSize(stdout, 1<<16)
	for {
		line, err := rd.ReadBytes('\n')
		if len(line) > 1 {
			p.handle(line)
		}
		if err != nil {
			return
		}
	}
}

type message struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	Parent    string          `json:"parent_tool_use_id"`
	Model     string          `json:"model"`
	Mode      string          `json:"permissionMode"`
	Status    string          `json:"status"`
	State     string          `json:"state"`
	IsError   bool            `json:"is_error"`
	APIError  bool            `json:"is_api_error_message"`
	Result    string          `json:"result"`
	Errors    []string        `json:"errors"`
	Cost      float64         `json:"total_cost_usd"`
	Suggest   string          `json:"suggestion"` // a prompt_suggestion's
	Command   string          `json:"command_uuid"`
	RequestID string          `json:"request_id"`
	Request   json.RawMessage `json:"request"`
	// On a change of model Claude Code made itself: its notice, which is
	// Raw as other messages carry content of other shapes, and the models.
	Notice   json.RawMessage `json:"content"`
	From     string          `json:"original_model"`
	Fallback string          `json:"fallback_model"`
	Refused  string          `json:"refused_user_message_uuid"`
	Response struct {
		Subtype   string          `json:"subtype"`
		RequestID string          `json:"request_id"`
		Response  json.RawMessage `json:"response"`
		Error     string          `json:"error"`
	} `json:"response"`
	Event struct {
		Type    string `json:"type"`
		Index   int    `json:"index"`
		Message struct {
			ID string `json:"id"`
		} `json:"message"`
		Block struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"content_block"`
		Delta struct {
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
		} `json:"delta"`
	} `json:"event"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// modelChanged keeps Claude Code's word on a model it moved off, which it
// shows in its terminal and writes nowhere else, and shows what answers now.
func (p *proc) modelChanged(m *message) {
	var why string
	json.Unmarshal(m.Notice, &why)
	if why == "" && m.Subtype == "model_refusal_no_fallback" {
		why = "The model's safeguards declined this message, and no other model answered it."
	}
	if why == "" {
		why = fmt.Sprintf("Claude Code moved from %s to %s.", m.From, m.Fallback)
	}
	if m.Fallback != "" {
		p.mu.Lock()
		p.using = m.Fallback
		p.mu.Unlock()
		p.changed()
	}
	if p.fellBack != nil {
		p.fellBack(m.Refused, store.ModelSwitch{From: m.From, To: m.Fallback, Why: why})
	}
}

func (p *proc) handle(line []byte) {
	var m message
	if json.Unmarshal(line, &m) != nil {
		return
	}
	switch m.Type {
	case "system":
		p.mu.Lock()
		switch m.Subtype {
		case "init":
			p.using, p.mode = m.Model, m.Mode
		case "status":
			// Summarising is a request too; it is still compacting until it says otherwise.
			if p.status != "compacting" || m.Status != "requesting" {
				p.status = m.Status
			}
		case "compact_boundary":
			p.status = ""
		case "session_state_changed":
			// A turn can start with no message, as when a background task ends.
			if m.State != "idle" && !p.busy {
				p.since, p.suggestion = time.Now(), ""
			}
			p.states, p.busy = true, m.State != "idle"
			if !p.busy {
				// Idle from now: a long turn is not reaped the moment it ends.
				p.queued, p.lastUsed = nil, time.Now()
			}
		}
		p.mu.Unlock()
		p.changed()
		if m.Subtype == "session_state_changed" && m.State == "idle" {
			// Asked apart from this reader, which has to read the answers.
			go p.turnOver()
		}
		if m.Subtype == "compact_boundary" {
			p.wrote()
		}
		switch m.Subtype {
		case "model_refusal_fallback", "model_fallback", "model_consent_fallback", "model_refusal_no_fallback":
			p.modelChanged(&m)
		}

	case "stream_event":
		if m.Parent != "" {
			return // a subagent's, which the page does not follow
		}
		p.stream(&m)

	case "prompt_suggestion":
		p.mu.Lock()
		p.suggestion = strings.TrimSpace(m.Suggest)
		p.mu.Unlock()
		p.changed()

	case "assistant", "user":
		if m.APIError {
			p.apiErr = textOf(m.Message.Content)
		}
		p.planning(&m)
		p.wrote()

	case "result":
		p.mu.Lock()
		p.busy = p.busy && p.states
		if !p.busy {
			p.queued, p.lastUsed = nil, time.Now()
		}
		p.status, p.cost = "", m.Cost
		if m.Subtype == "error_during_execution" {
			// Interrupted: what streamed is not coming to the transcript.
			p.blocks = nil
		} else if m.IsError && (len(m.Errors) > 0 || m.Result != p.apiErr) {
			// An API error, as "Not logged in" is, is in the transcript already;
			// anything else stays the session's error until the next message.
			p.err = strings.Join(append(m.Errors, m.Result), "\n")
		}
		p.apiErr = ""
		over := !p.states
		p.mu.Unlock()
		p.wrote()
		p.changed()
		if over {
			go p.turnOver()
		}

	case "command_lifecycle":
		if m.State != "queued" {
			p.unqueue(m.Command)
		}

	case "control_request":
		go p.control(m.RequestID, m.Request)

	case "control_cancel_request":
		p.mu.Lock()
		if cancel := p.asks[m.RequestID]; cancel != nil {
			cancel()
		}
		p.mu.Unlock()

	case "control_response":
		p.mu.Lock()
		ch := p.replies[m.Response.RequestID]
		p.mu.Unlock()
		if ch != nil {
			var err error
			if m.Response.Subtype == "error" {
				err = errors.New(m.Response.Error)
			}
			deliver(ch, reply{body: m.Response.Response, err: err})
		}
	}
}

func (p *proc) stream(m *message) {
	ev := &m.Event
	p.mu.Lock()
	switch ev.Type {
	case "message_start":
		p.msg = ev.Message.ID
	case "content_block_start":
		kind := ev.Block.Type
		if kind == "tool_use" || kind == "server_tool_use" {
			kind = "tool"
		}
		p.blocks = append(p.blocks, &Block{msg: p.msg, index: ev.Index, Kind: kind, Tool: ev.Block.Name})
	case "content_block_delta":
		for _, b := range p.blocks {
			if b.msg == p.msg && b.index == ev.Index {
				b.Text += ev.Delta.Text + ev.Delta.Thinking
			}
		}
	default:
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()
	p.changed()
}

// control answers what Claude Code asks of dv.
func (p *proc) control(id string, raw json.RawMessage) {
	var r struct {
		Subtype     string            `json:"subtype"`
		Tool        string            `json:"tool_name"`
		Input       json.RawMessage   `json:"input"`
		Suggestions []json.RawMessage `json:"permission_suggestions"`
	}
	json.Unmarshal(raw, &r)
	switch r.Subtype {
	case "can_use_tool":
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		p.mu.Lock()
		p.asks[id] = cancel
		p.mu.Unlock()
		req := &permit.Request{Session: p.id, Tool: r.Tool, Input: r.Input, Suggestions: r.Suggestions}
		plan := r.Tool == "ExitPlanMode" && len(r.Suggestions) == 0
		if plan {
			req.Suggestions = permit.PlanModes
		}
		a := p.broker.Put(ctx, req)
		p.mu.Lock()
		delete(p.asks, id)
		p.mu.Unlock()
		if a == nil {
			return // Claude Code withdrew the question, or stopped
		}
		// A bare yes to a plan leaves Claude Code asking before edits, which is
		// said outright so the mode shown follows.
		if plan && a.Allow && a.Suggestion == nil {
			asking := 1 // PlanModes' default
			a.Suggestion = &asking
		}
		d := p.broker.Decision(req, a)
		// Over the channel an allow always says what to run with.
		if _, ok := d["updatedInput"]; !ok && d["behavior"] == "allow" {
			d["updatedInput"] = r.Input
		}
		p.respond(id, d)
		// Approving a plan says which mode to go on in.
		if mode := modeSet(d); mode != "" {
			p.mu.Lock()
			p.mode = mode
			p.mu.Unlock()
			p.changed()
		}

	case "hook_callback":
		var hook struct {
			Tool  string          `json:"tool_name"`
			Input json.RawMessage `json:"tool_input"`
		}
		json.Unmarshal(r.Input, &hook)
		var out any = map[string]any{}
		if note := p.broker.TakeNote(p.id, hook.Tool, hook.Input); note != "" {
			out = map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PostToolUse", "additionalContext": note}}
		}
		p.respond(id, out)

	default:
		p.write(map[string]any{
			"type":     "control_response",
			"response": map[string]any{"subtype": "error", "request_id": id, "error": "dv does not handle " + r.Subtype},
		})
	}
}

// planning notes Claude putting itself in plan mode, which the stream says
// nothing else of until the next turn starts.
func textOf(content json.RawMessage) string {
	var blocks []block
	json.Unmarshal(content, &blocks)
	var text []string
	for _, b := range blocks {
		if b.Type == "text" {
			text = append(text, b.Text)
		}
	}
	return strings.Join(text, "\n")
}

func (p *proc) planning(m *message) {
	var blocks []block
	if json.Unmarshal(m.Message.Content, &blocks) != nil {
		return
	}
	for _, b := range blocks {
		switch {
		case b.Type == "tool_use" && b.Name == "EnterPlanMode":
			p.planCall = b.ID
		case b.Type == "tool_result" && b.ToolUseID != "" && b.ToolUseID == p.planCall:
			p.planCall = ""
			if !b.IsError {
				p.mu.Lock()
				p.mode = "plan"
				p.mu.Unlock()
				p.changed()
			}
		}
	}
}

// modeSet is the permission mode an answer to Claude Code switches to, if any.
func modeSet(d map[string]any) string {
	updates, _ := d["updatedPermissions"].([]json.RawMessage)
	for _, u := range updates {
		var s struct{ Type, Mode string }
		if json.Unmarshal(u, &s) == nil && s.Type == "setMode" {
			return s.Mode
		}
	}
	return ""
}

// deliver hands over a reply unless one is already waiting to be taken, as
// when the process exits just after answering.
func deliver(ch chan reply, r reply) {
	select {
	case ch <- r:
	default:
	}
}

// tailBuffer keeps the last bytes written to it: the end of stderr is where a
// failing CLI says why.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	b   []byte
}

func (t *tailBuffer) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, b...)
	if len(t.b) > t.max {
		t.b = t.b[len(t.b)-t.max:]
	}
	return len(b), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}
