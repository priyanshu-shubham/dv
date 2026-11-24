package agent

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"dv/internal/acp"
	"dv/internal/permit"
)

// agyRun is an Antigravity session dv runs: an `agy acp` process holding one
// ACP session, which dv puts prompts to and writes the transcript of.
type agyRun struct {
	p   *proc
	log *agyLog
	cmd *exec.Cmd
	// ready is closed once the session is open, or failed to: setupErr says.
	ready    chan struct{}
	setupErr error

	conn *acp.Conn

	mu       sync.Mutex
	sid      string
	hs       agyHandshake
	options  []configOption
	commands []string
	prompts  int // session/prompt calls not yet answered
	turnAt   time.Time
	calls    map[string]*agyCall
	// The reply being written: its message id, how many of its blocks the
	// transcript has, and the block still streaming in.
	msg     string
	written int
	open    *Block
	used    int64
	size    int64
	quiet   *time.Timer
	signIn  bool // agy printed a sign-in link
	stopped bool
}

// agySessionLine is the transcript line keeping agy's own id for the session,
// which a resume opens it by.
const agySessionLine = "agy-session"

// agySessionID is the agy session a transcript was last written for.
func agySessionID(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	id := ""
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.Contains(line, agySessionLine) {
			continue
		}
		var r struct {
			Type string `json:"type"`
			ID   string `json:"agySessionId"`
		}
		if json.Unmarshal([]byte(line), &r) == nil && r.Type == agySessionLine && r.ID != "" {
			id = r.ID
		}
	}
	return id
}

// startAgyLocked runs agy for the session. agy unpacks itself on each start,
// which can take a while, so the session is opened apart; what is sent
// meanwhile waits for it. Callers hold p.mu.
func (p *proc) startAgyLocked() error {
	if p.cmd != nil {
		return nil
	}
	bin := agyPath()
	if bin == "" {
		return errors.New("the Antigravity CLI, agy, is not installed: see antigravity.google/docs/install")
	}
	log, err := openAgyLog(p.cwd, p.id, p.wrote)
	if err != nil {
		return err
	}
	cmd := exec.Command(bin, "acp")
	cmd.Dir = p.cwd
	cmd.Env = agyEnv()
	if p.env != nil {
		cmd.Env = append(cmd.Env, p.env()...)
	}
	ownGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return err
	}
	stderr := &tailBuffer{max: 4096}
	cmd.Stderr = stderr
	cmd.WaitDelay = outlives
	if err := cmd.Start(); err != nil {
		stdin.Close()
		return err
	}
	a := &agyRun{p: p, log: log, cmd: cmd, ready: make(chan struct{}), calls: map[string]*agyCall{}}
	a.conn = acp.New(stdin, stdout, a)
	log.setMode(p.mode)
	p.agy = a
	p.cmd, p.stdin, p.exited, p.stopped, p.err = cmd, stdin, make(chan struct{}), false, ""
	p.pid.Store(int64(cmd.Process.Pid))
	p.lastUsed = time.Now()
	p.ran = p.lastUsed
	p.status = "starting"
	go a.wait(stderr)
	go a.setup()
	return nil
}

func (a *agyRun) call(ctx context.Context, method string, params, out any) error {
	return a.conn.Call(ctx, method, params, out)
}

func (a *agyRun) wait(stderr *tailBuffer) {
	err := a.cmd.Wait()
	<-a.conn.Done()
	p := a.p
	a.mu.Lock()
	a.stopped = true
	if a.quiet != nil {
		a.quiet.Stop()
	}
	signIn := a.signIn
	a.mu.Unlock()
	a.finish(nil)
	p.mu.Lock()
	defer func() {
		p.mu.Unlock()
		p.changed()
	}()
	if p.cmd != a.cmd {
		return
	}
	defer close(p.exited)
	if !p.stopped && p.err == "" {
		switch {
		case signIn:
			p.err = errAgySignIn.Error()
		case err != nil && !errors.Is(err, exec.ErrWaitDelay):
			p.err = cmp.Or(strings.TrimSpace(stderr.String()), "Antigravity stopped: "+err.Error())
		}
	}
	p.cmd, p.stdin, p.busy, p.blocks, p.status, p.queued, p.agy = nil, nil, false, nil, "", nil, nil
	p.pid.Store(0)
}

// fail ends a session that could not go on, saying why.
func (a *agyRun) fail(err error) {
	p := a.p
	p.mu.Lock()
	if p.agy == a {
		p.err = err.Error()
	}
	cmd := p.cmd
	p.mu.Unlock()
	p.changed()
	if cmd == a.cmd {
		killGroup(a.cmd)
	}
}

// setup opens the session: agy's own id for it, kept in the transcript, is
// resumed - without the history replayed, which the transcript has - and a
// session agy no longer has goes on in a new one.
func (a *agyRun) setup() {
	defer close(a.ready)
	p := a.p
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	hs, err := handshake(ctx, a.call)
	if err == nil {
		err = a.openSession(ctx, hs)
	}
	p.mu.Lock()
	p.status = ""
	p.mu.Unlock()
	if err != nil {
		a.setupErr = err
		a.fail(err)
		return
	}
	p.changed()
}

func (a *agyRun) openSession(ctx context.Context, hs agyHandshake) error {
	p := a.p
	params := map[string]any{"cwd": p.cwd, "mcpServers": []any{}}
	var r struct {
		SessionID     string         `json:"sessionId"`
		ConfigOptions []configOption `json:"configOptions"`
	}
	resumed := false
	if sid := agySessionID(a.log.path); sid != "" && hs.AgentCapabilities.SessionCapabilities.Resume != nil {
		params["sessionId"] = sid
		err := a.call(ctx, "session/resume", params, &r)
		if agySignInNeeded(err) {
			return errAgySignIn
		}
		if err == nil {
			r.SessionID, resumed = sid, true
		} else {
			a.log.note("Antigravity no longer has this conversation (" + err.Error() + "), so it goes on in a new one, which doesn't know what was said above.")
		}
		delete(params, "sessionId")
	}
	if !resumed {
		if hs.AgentCapabilities.Meta.Antigravity.SystemPrompt.Append && p.instructions != nil {
			if s := p.instructions(); s != "" {
				params["_meta"] = map[string]any{"antigravity": map[string]any{"systemPrompt": map[string]any{"append": s}}}
			}
		}
		err := a.call(ctx, "session/new", params, &r)
		if agySignInNeeded(err) {
			return errAgySignIn
		}
		if err != nil {
			return fmt.Errorf("Antigravity couldn't start a session: %w", err)
		}
		a.log.appendMeta(map[string]any{"type": agySessionLine, "agySessionId": r.SessionID})
	}
	a.mu.Lock()
	a.sid, a.hs = r.SessionID, hs
	a.setOptionsLocked(r.ConfigOptions)
	a.mu.Unlock()
	agyHeard(r.ConfigOptions)

	p.mu.Lock()
	model, effort, mode := p.model, p.effort, p.mode
	p.mu.Unlock()
	return a.apply(ctx, &model, &effort, &mode)
}

// apply puts the session on a model, thinking level and mode, where agy
// offers them; nil leaves one as it is.
func (a *agyRun) apply(ctx context.Context, model, effort, mode *string) error {
	set := func(category, id, value string) error {
		a.mu.Lock()
		o, ok := optionFor(a.options, category, id)
		sid := a.sid
		a.mu.Unlock()
		if !ok || value == "" || o.current() == value {
			return nil
		}
		if !o.offers(value) {
			if category == modelCategory {
				return fmt.Errorf("Antigravity doesn't offer %s to this Google account", value)
			}
			return nil
		}
		var r struct {
			ConfigOptions []configOption `json:"configOptions"`
		}
		if err := a.call(ctx, "session/set_config_option", map[string]any{"sessionId": sid, "configId": o.ID, "value": value}, &r); err != nil {
			return err
		}
		if len(r.ConfigOptions) > 0 {
			a.mu.Lock()
			a.setOptionsLocked(r.ConfigOptions)
			a.mu.Unlock()
		}
		return nil
	}
	if model != nil {
		if err := set(modelCategory, "model", strings.TrimPrefix(*model, AgyPrefix)); err != nil {
			return err
		}
	}
	if effort != nil {
		set(thinkingCategory, "thinking_level", *effort)
	}
	if mode != nil {
		set(modeCategory, "mode", agyMode(*mode, a.modes()))
		a.log.setMode(*mode)
	}
	a.mu.Lock()
	a.reportLocked()
	a.mu.Unlock()
	a.p.changed()
	return nil
}

// agyMode is the mode agy's session goes in for one of dv's. agy asks about
// every action in its default and dv answers by the mode; only its own
// review, in auto, and planning are agy's to do.
func agyMode(mode string, offered []string) string {
	want := "default"
	switch mode {
	case "auto", AutoAskEdits:
		want = "auto"
	case "plan":
		want = "plan"
	}
	if slices.Contains(offered, want) {
		return want
	}
	return "default"
}

func (a *agyRun) modes() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	o, _ := optionFor(a.options, modeCategory, "mode")
	var out []string
	for _, c := range o.choices() {
		out = append(out, c.Value)
	}
	return out
}

func (a *agyRun) setOptionsLocked(opts []configOption) {
	if len(opts) > 0 {
		a.options = opts
	}
}

// reportLocked shows the model and thinking level in force.
func (a *agyRun) reportLocked() {
	p := a.p
	m, _ := optionFor(a.options, modelCategory, "model")
	t, _ := optionFor(a.options, thinkingCategory, "thinking_level")
	p.mu.Lock()
	if v := m.current(); v != "" {
		p.using = AgyPrefix + v
	}
	p.effortUsing = t.current()
	p.mu.Unlock()
}

func (a *agyRun) modelID() string {
	a.p.mu.Lock()
	defer a.p.mu.Unlock()
	return cmp.Or(a.p.using, a.p.model, AgyPrefix+"default")
}

// send puts a message to the session: the transcript has it at once, and a
// turn running takes it in, as agy steers one with a prompt sent meanwhile.
func (a *agyRun) send(uuid, text string, images []Image) error {
	if err := a.log.prompt(uuid, text, images); err != nil {
		return err
	}
	a.mu.Lock()
	a.endReplyLocked()
	if a.prompts == 0 {
		a.turnAt = time.Now()
	}
	a.prompts++
	a.mu.Unlock()
	go a.prompt(text, images)
	return nil
}

func (a *agyRun) prompt(text string, images []Image) {
	<-a.ready
	if a.setupErr != nil {
		a.done("", a.setupErr)
		return
	}
	a.mu.Lock()
	sid, imagesOK := a.sid, a.hs.AgentCapabilities.PromptCapabilities.Image
	a.mu.Unlock()
	var blocks []map[string]any
	if text != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": text})
	}
	for _, img := range images {
		if imagesOK {
			blocks = append(blocks, map[string]any{"type": "image", "mimeType": img.MediaType, "data": base64.StdEncoding.EncodeToString(img.Data)})
		}
	}
	if len(images) > 0 && !imagesOK {
		a.log.note("This agy takes no pictures, so the message went without them.")
	}
	var r struct {
		StopReason string `json:"stopReason"`
	}
	err := a.call(context.Background(), "session/prompt", map[string]any{"sessionId": sid, "prompt": blocks}, &r)
	a.done(r.StopReason, err)
}

// done hears a prompt answered. The turn is over once every prompt sent in it
// has been.
func (a *agyRun) done(stop string, err error) {
	a.mu.Lock()
	a.prompts = max(a.prompts-1, 0)
	last := a.prompts == 0
	a.mu.Unlock()
	switch {
	case err != nil && errors.Is(err, acp.ErrClosed):
	case agySignInNeeded(err):
		a.fail(errAgySignIn)
	case err != nil:
		a.log.note("Antigravity: " + err.Error())
	case stop == "cancelled":
		a.log.append(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "[Request interrupted by user]"}})
	case stop == "refusal":
		a.log.note("Antigravity stopped here, as it does when the account's quota is used up or a reply is refused. agy's /usage says when the limits reset.")
	case stop == "max_tokens":
		a.log.note("Antigravity stopped: the reply reached its length limit.")
	case stop == "max_turn_requests":
		a.log.note("Antigravity stopped: the turn reached its limit of requests.")
	}
	if last {
		a.finish(err)
	}
}

// finish ends the turn going on: what streamed is written, and how long it
// went on.
func (a *agyRun) finish(err error) {
	a.mu.Lock()
	a.endReplyLocked()
	at := a.turnAt
	a.turnAt = time.Time{}
	a.mu.Unlock()
	if !at.IsZero() && err == nil {
		a.log.append(map[string]any{"type": "system", "subtype": "turn_duration", "durationMs": time.Since(at).Milliseconds()})
	}
	p := a.p
	p.mu.Lock()
	was := p.busy
	p.busy, p.queued, p.lastUsed = false, nil, time.Now()
	p.mu.Unlock()
	p.changed()
	if was && p.ended != nil {
		p.ended()
	}
}

// cancel stops the turn, and what agy is running in it.
func (a *agyRun) cancel() error {
	a.mu.Lock()
	sid := a.sid
	a.mu.Unlock()
	if sid == "" {
		return nil
	}
	return a.conn.Notify("session/cancel", map[string]any{"sessionId": sid})
}

// ---- what agy sends

func (a *agyRun) Stray(line []byte) {
	if strings.HasPrefix(string(line), agySignInLink) {
		a.mu.Lock()
		a.signIn = true
		a.mu.Unlock()
		a.fail(errAgySignIn)
	}
}

func (a *agyRun) Request(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "session/request_permission":
		return a.permission(ctx, params)
	}
	return nil, &acp.Error{Code: -32601, Message: "dv does not offer " + method}
}

type agyUpdate struct {
	Kind       string          `json:"sessionUpdate"`
	Content    json.RawMessage `json:"content"`
	ToolCallID string          `json:"toolCallId"`
	Title      *string         `json:"title"`
	ToolKind   *string         `json:"kind"`
	Status     *string         `json:"status"`
	RawInput   json.RawMessage `json:"rawInput"`
	RawOutput  json.RawMessage `json:"rawOutput"`
	Locations  []struct {
		Path string `json:"path"`
	} `json:"locations"`
	Meta    json.RawMessage `json:"_meta"`
	Entries []struct {
		Content string `json:"content"`
		Status  string `json:"status"`
	} `json:"entries"`
	Used          int64          `json:"used"`
	Size          int64          `json:"size"`
	ConfigOptions []configOption `json:"configOptions"`
	Commands      []struct {
		Name string `json:"name"`
	} `json:"availableCommands"`
}

func (a *agyRun) Notify(method string, params json.RawMessage) {
	if method != "session/update" {
		return
	}
	var n struct {
		Update agyUpdate `json:"update"`
	}
	if json.Unmarshal(params, &n) != nil {
		return
	}
	u := &n.Update
	switch u.Kind {
	case "agent_message_chunk", "agent_thought_chunk":
		var c struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		json.Unmarshal(u.Content, &c)
		if c.Text == "" {
			return
		}
		kind := "text"
		if u.Kind == "agent_thought_chunk" {
			kind = "thinking"
		}
		a.working()
		a.stream(kind, c.Text)
	case "tool_call", "tool_call_update":
		a.working()
		a.tool(u)
	case "plan":
		a.working()
		a.plan(u)
	case "usage_update":
		a.usage(u.Used, u.Size)
	case "config_option_update":
		a.mu.Lock()
		a.setOptionsLocked(u.ConfigOptions)
		a.reportLocked()
		a.mu.Unlock()
		a.p.changed()
	case "available_commands_update":
		var names []string
		for _, c := range u.Commands {
			names = append(names, c.Name)
		}
		a.mu.Lock()
		a.commands = names
		a.mu.Unlock()
	}
}

// working marks the session busy when agy goes on with no prompt open, as it
// does when a command it left running ends.
func (a *agyRun) working() {
	a.mu.Lock()
	if a.quiet != nil {
		a.quiet.Stop()
		a.quiet = nil
	}
	if a.prompts == 0 && a.turnAt.IsZero() {
		a.turnAt = time.Now()
	}
	a.mu.Unlock()
	p := a.p
	p.mu.Lock()
	start := !p.busy
	if start {
		p.busy, p.since = true, time.Now()
	}
	p.mu.Unlock()
	if start {
		p.changed()
	}
}

// usage keeps how full the context is. Each of agy's replies ends with one,
// which is the end of a reply it made with no prompt open, unless more
// follows straight after.
func (a *agyRun) usage(used, size int64) {
	a.mu.Lock()
	a.used, a.size = used, size
	if a.prompts == 0 && !a.turnAt.IsZero() {
		if a.quiet != nil {
			a.quiet.Stop()
		}
		a.quiet = time.AfterFunc(3*time.Second, func() {
			a.mu.Lock()
			still := a.prompts == 0 && !a.stopped
			a.quiet = nil
			a.mu.Unlock()
			if still {
				a.finish(nil)
			}
		})
	}
	a.mu.Unlock()
	if size > 0 {
		p := a.p
		p.mu.Lock()
		p.context = &Context{Used: int(used), Max: int(size)}
		p.mu.Unlock()
		p.changed()
		agyHeardWindow(a.modelID(), int(size))
	}
}

// stream adds to the reply streaming in: a block of its kind, or a new one
// after the last is written.
func (a *agyRun) stream(kind, text string) {
	p := a.p
	a.mu.Lock()
	if a.open != nil && a.open.Kind == kind {
		p.mu.Lock()
		a.open.Text += text
		p.mu.Unlock()
	} else {
		a.writeOpenLocked()
		if a.msg == "" {
			a.msg, a.written = "msg_agy_"+newUUID(), 0
		}
		b := &Block{msg: a.msg, index: a.written, Kind: kind, Text: text}
		p.mu.Lock()
		p.blocks = append(p.blocks, b)
		p.mu.Unlock()
		a.open = b
	}
	a.mu.Unlock()
	p.changed()
}

func (a *agyRun) usageLocked() *agyUsage {
	if a.used == 0 {
		return nil
	}
	return &agyUsage{input: int(a.used)}
}

// writeOpenLocked writes the block streaming in, if any.
func (a *agyRun) writeOpenLocked() {
	b := a.open
	if b == nil {
		return
	}
	a.open = nil
	p := a.p
	p.mu.Lock()
	text := b.Text
	if strings.TrimSpace(text) == "" {
		p.blocks = slices.DeleteFunc(p.blocks, func(x *Block) bool { return x == b })
	}
	p.mu.Unlock()
	if strings.TrimSpace(text) == "" {
		return
	}
	block := map[string]any{"type": "text", "text": text}
	if b.Kind == "thinking" {
		block = map[string]any{"type": "thinking", "thinking": text, "signature": ""}
	}
	if a.log.assistant(a.msg, a.modelID(), block, a.usageLocked()) == nil {
		a.written++
	}
}

// endReplyLocked writes what streamed, and has what comes next begin a new
// reply.
func (a *agyRun) endReplyLocked() {
	a.writeOpenLocked()
	a.msg, a.written = "", 0
}

// writeBlockLocked adds a block to the reply, as the call it makes.
func (a *agyRun) writeBlockLocked(block map[string]any) {
	a.writeOpenLocked()
	if a.msg == "" {
		a.msg, a.written = "msg_agy_"+newUUID(), 0
	}
	if a.log.assistant(a.msg, a.modelID(), block, a.usageLocked()) == nil {
		a.written++
	}
	a.msg, a.written = "", 0
}

func (a *agyRun) plan(u *agyUpdate) {
	var todos []map[string]string
	for _, e := range u.Entries {
		status := e.Status
		if status != "completed" && status != "in_progress" {
			status = "pending"
		}
		todos = append(todos, map[string]string{"content": e.Content, "status": status, "activeForm": e.Content})
	}
	id := "agy_plan_" + newUUID()
	a.mu.Lock()
	a.writeBlockLocked(map[string]any{"type": "tool_use", "id": id, "name": "TodoWrite", "input": map[string]any{"todos": todos}})
	a.mu.Unlock()
	a.log.result(id, "The plan is updated.", false, nil)
}

// ---- tool calls

func (a *agyRun) tool(u *agyUpdate) {
	if u.ToolCallID == "" {
		return
	}
	a.mu.Lock()
	c := a.calls[u.ToolCallID]
	if c == nil {
		c = &agyCall{id: u.ToolCallID, input: map[string]any{}}
		a.calls[u.ToolCallID] = c
	}
	c.merge(u)
	a.announceLocked(c)
	var res *agyResult
	if !c.done && (c.status == "completed" || c.status == "failed") {
		c.done = true
		res = c.result()
	}
	a.mu.Unlock()
	if res != nil {
		a.log.result(c.id, res.text, res.failed, res.record)
	}
}

// announceLocked writes the call into the transcript the first time it is
// heard of, having kept the file it edits as it stood before.
func (a *agyRun) announceLocked(c *agyCall) {
	c.snapshot()
	if c.written {
		return
	}
	c.written = true
	tool, input := c.claude()
	c.tool = tool
	a.writeBlockLocked(map[string]any{"type": "tool_use", "id": c.id, "name": tool, "input": input})
}

// agyOption is one of the answers agy offers to what it asks; its kind says
// what it does, its id is agy's own.
type agyOption struct {
	ID   string `json:"optionId"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// permission puts what agy asks to the user, or answers it by the session's
// mode: edits are let through in Accept edits, and everything else asked.
func (a *agyRun) permission(ctx context.Context, params json.RawMessage) (any, error) {
	var r struct {
		ToolCall agyUpdate   `json:"toolCall"`
		Options  []agyOption `json:"options"`
	}
	if err := json.Unmarshal(params, &r); err != nil {
		return nil, &acp.Error{Code: -32602, Message: err.Error()}
	}
	pick := func(kinds ...string) any {
		for _, k := range kinds {
			for _, o := range r.Options {
				if o.Kind == k {
					return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": o.ID}}
				}
			}
		}
		return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}
	}
	cancelled := map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}
	p := a.p

	// A question, single choice: its title asks it, each option is an answer.
	if strings.HasPrefix(r.ToolCall.ToolCallID, "interaction_") {
		question := strings.TrimSpace(deref(r.ToolCall.Title))
		var options []map[string]string
		for _, o := range r.Options {
			options = append(options, map[string]string{"label": o.Name})
		}
		input, _ := json.Marshal(map[string]any{"questions": []any{map[string]any{
			"question": question, "header": "Question", "options": options, "multiSelect": false,
		}}})
		call := "agy_" + r.ToolCall.ToolCallID
		a.working()
		a.mu.Lock()
		a.writeBlockLocked(map[string]any{"type": "tool_use", "id": call, "name": "AskUserQuestion", "input": json.RawMessage(input)})
		a.mu.Unlock()
		ans := p.broker.Put(ctx, &permit.Request{Session: p.id, By: "Antigravity", Tool: "AskUserQuestion", Input: input})
		if ans == nil || !ans.Allow {
			a.log.result(call, "The user declined to answer.", true, nil)
			return cancelled, nil
		}
		picked := ans.Answers[question]
		a.log.result(call, fmt.Sprintf("User has answered your questions: %q=%q.", question, picked), false, nil)
		for _, o := range r.Options {
			if o.Name == picked {
				return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": o.ID}}, nil
			}
		}
		// Written rather than picked: agy takes only its own answers, so the
		// words go to it as a message.
		if picked != "" {
			a.steer(permit.Declined(picked))
		}
		return cancelled, nil
	}

	a.working()
	a.mu.Lock()
	c := a.calls[r.ToolCall.ToolCallID]
	if c == nil {
		c = &agyCall{id: r.ToolCall.ToolCallID, input: map[string]any{}}
		a.calls[c.id] = c
	}
	c.merge(&r.ToolCall)
	a.announceLocked(c)
	tool, input := c.claude()
	edit := c.isEdit()
	a.mu.Unlock()

	p.mu.Lock()
	mode := p.mode
	p.mu.Unlock()
	if edit && mode == "acceptEdits" {
		return pick("allow_once", "allow_always"), nil
	}
	raw, _ := json.Marshal(input)
	req := &permit.Request{Session: p.id, By: "Antigravity", Tool: tool, Input: raw}
	always := slices.ContainsFunc(r.Options, func(o agyOption) bool { return o.Kind == "allow_always" })
	if always {
		rule := map[string]any{"toolName": tool}
		if s := ruleOf(tool, input); s != "" {
			rule["ruleContent"] = s
		}
		s, _ := json.Marshal(map[string]any{"type": "addRules", "behavior": "allow", "destination": "session", "rules": []any{rule}})
		req.Suggestions = []json.RawMessage{s}
	}
	ans := p.broker.Put(ctx, req)
	if ans == nil {
		return cancelled, nil
	}
	note := strings.TrimSpace(ans.Note)
	switch {
	case ans.Allow:
		if note != "" {
			a.steer(permit.Allowed(tool, note))
		}
		if ans.Suggestion != nil {
			return pick("allow_always", "allow_once"), nil
		}
		return pick("allow_once", "allow_always"), nil
	case note != "":
		a.steer(permit.Declined(note))
		return pick("reject_once", "reject_always"), nil
	default:
		// A bare no stops the turn, as it stops Claude.
		go func() {
			time.Sleep(100 * time.Millisecond)
			a.cancel()
		}()
		return pick("reject_once", "reject_always"), nil
	}
}

// steer sends agy what the user wrote with an answer, which it hears as a
// message within the turn.
func (a *agyRun) steer(text string) {
	a.mu.Lock()
	if a.prompts == 0 {
		a.turnAt = time.Now()
	}
	a.prompts++
	a.mu.Unlock()
	go a.prompt(text, nil)
}

// ruleOf is what an always-allow covers, as the page words it.
func ruleOf(tool string, input map[string]any) string {
	switch tool {
	case "Bash":
		s, _ := input["command"].(string)
		return s
	case "Edit", "Write", "Read":
		s, _ := input["file_path"].(string)
		return s
	case "WebFetch":
		s, _ := input["url"].(string)
		return s
	}
	return ""
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
