package agent

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"dv/internal/codex"
	"dv/internal/permit"
)

// codexThread is a Codex thread the Agent view shows or dv runs. What it holds
// comes from the app-server: the turns read back, then - while dv has the
// thread loaded - the notifications of each turn as it runs. A thread loaded
// elsewhere, in a terminal, is read again whenever its rollout file grows.
type codexThread struct {
	side *codexSide
	id   string

	// op keeps a thread's sends, steers and settings in order. It is held over
	// calls to the app-server, which mu never is: the reader that delivers
	// their replies also delivers notifications, which take mu.
	op sync.Mutex

	mu       sync.Mutex
	meta     codex.Thread // as last read: name, rollout path, model
	loaded   bool         // dv's app-server has it, so dv can run turns in it
	history  bool         // turns have been read
	whole    bool         // back to the thread's start
	turns    []codex.Turn
	active   string // the turn running now
	settings *codex.Settings
	asked    askedSettings
	pending  bool // asked changed during a turn, to apply once it ends
	held     []*heldMessage
	context  *Context
	status   string
	err      string
	blocks   map[string]*Block // replies streaming in, by item
	order    []string
	todos    map[string]*todoList
	commands map[string][]Item // /compact sent from dv, by the turn it came after
	plans    map[string]bool   // turns run in plan mode
	asks     map[string]context.CancelFunc
	planAsk  context.CancelFunc
	version  int
	subs     map[*Sub]bool
	watching bool
	mark     fileMark
	lastUsed time.Time
}

// askedSettings are what was picked in dv; nil is not picked. A model of ""
// is the user's default.
type askedSettings struct{ model, mode, effort *string }

// heldMessage is one sent while a turn runs, held back for a moment in which
// it can still be taken back, then steered into the turn.
type heldMessage struct {
	uuid, text string
	images     int
	input      []map[string]any
	timer      *time.Timer
}

type fileMark struct {
	size int64
	mod  time.Time
}

// holdFor is how long a message sent mid-turn waits before it is steered in:
// the page's window for taking a message back.
const holdFor = 2 * time.Second

var errTerminal = errors.New("this session is open in a terminal; dv follows it, but only the terminal can talk to it")

func newCodexThread(side *codexSide, id string) *codexThread {
	return &codexThread{
		side: side, id: id, blocks: map[string]*Block{}, todos: map[string]*todoList{},
		commands: map[string][]Item{}, plans: map[string]bool{}, asks: map[string]context.CancelFunc{},
		subs: map[*Sub]bool{}, lastUsed: time.Now(),
	}
}

func (t *codexThread) call(method string, params, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return t.side.client.Call(ctx, method, params, out)
}

// signal tells the pages following the thread that it changed.
func (t *codexThread) signal() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for s := range t.subs {
		select {
		case s.Changed <- struct{}{}:
		default:
		}
	}
}

func (t *codexThread) running() string {
	t.mu.Lock()
	loaded := t.loaded
	t.mu.Unlock()
	switch {
	case loaded:
		return "dv"
	case t.side.client.HeldElsewhere(t.id):
		return "terminal"
	}
	return ""
}

// ---- reading the thread

type threadResponse struct {
	Thread   codex.Thread    `json:"thread"`
	Model    string          `json:"model"`
	Effort   *string         `json:"reasoningEffort"`
	Approval json.RawMessage `json:"approvalPolicy"`
	Reviewer string          `json:"approvalsReviewer"`
	Sandbox  struct {
		Type string `json:"type"`
	} `json:"sandbox"`
	Cwd string `json:"cwd"`
}

func (r *threadResponse) settings() *codex.Settings {
	s := &codex.Settings{Cwd: r.Cwd, Model: r.Model, Effort: r.Effort, ApprovalPolicy: r.Approval, Reviewer: r.Reviewer}
	s.Sandbox.Type = r.Sandbox.Type
	return s
}

// historyPage is how many turns are read at once. A page opens on the latest,
// with the rest read when asked for.
const historyPage = 30

// readHistory reads the thread's turns, the latest page or all of them, unless
// what is held already does.
func (t *codexThread) readHistory(all bool) {
	t.mu.Lock()
	if t.history && (t.whole || !all) {
		t.mu.Unlock()
		return
	}
	t.mu.Unlock()

	var meta struct {
		Thread codex.Thread `json:"thread"`
	}
	metaErr := t.call("thread/read", map[string]any{"threadId": t.id}, &meta)
	var turns []codex.Turn
	whole := true
	cursor := ""
	for {
		var page struct {
			Data []codex.Turn `json:"data"`
			Next *string      `json:"nextCursor"`
		}
		params := map[string]any{"threadId": t.id, "limit": historyPage, "itemsView": "full", "sortDirection": "desc"}
		if cursor != "" {
			params["cursor"] = cursor
		}
		// A thread never sent to has no rollout, and nothing to list.
		if t.call("thread/turns/list", params, &page) != nil {
			break
		}
		turns = append(turns, page.Data...)
		if page.Next == nil || *page.Next == "" {
			break
		}
		if !all {
			whole = false
			break
		}
		cursor = *page.Next
	}
	slices.Reverse(turns)

	t.mu.Lock()
	if metaErr == nil {
		t.meta = meta.Thread
	}
	t.turns = mergeTurns(turns, t.turns, t.active)
	t.history, t.whole = true, whole
	t.mark = rolloutMark(t.meta.Path)
	t.version++
	t.mu.Unlock()
	t.signal()
}

// refreshTail reads the latest turns again, for a thread written elsewhere.
func (t *codexThread) refreshTail() {
	var page struct {
		Data []codex.Turn `json:"data"`
	}
	if t.call("thread/turns/list", map[string]any{"threadId": t.id, "limit": 3, "itemsView": "full", "sortDirection": "desc"}, &page) != nil {
		return
	}
	slices.Reverse(page.Data)
	t.mu.Lock()
	t.turns = mergeTurns(page.Data, t.turns, t.active)
	t.version++
	t.mu.Unlock()
	t.signal()
}

// mergeTurns lays turns just read over those held: a turn read replaces the
// one held under its id, except the turn running now, whose notifications are
// ahead of what the rollout has; one held and not read is newer, and stays.
func mergeTurns(read, held []codex.Turn, active string) []codex.Turn {
	byID := map[string]int{}
	out := slices.Clone(held)
	for i, turn := range out {
		byID[turn.ID] = i
	}
	var fresh []codex.Turn
	for _, turn := range read {
		if i, ok := byID[turn.ID]; ok {
			if turn.ID != active {
				out[i] = turn
			}
			continue
		}
		fresh = append(fresh, turn)
	}
	if len(fresh) == 0 {
		return out
	}
	// Read turns missing from what is held go before any held that were not
	// read: they are older, as when earlier turns are read in.
	if len(out) > 0 && len(read) > 0 {
		if _, ok := byID[read[len(read)-1].ID]; ok {
			return append(fresh, out...)
		}
	}
	return append(out, fresh...)
}

func rolloutMark(path *string) fileMark {
	if path == nil {
		return fileMark{}
	}
	fi, err := os.Stat(*path)
	if err != nil {
		return fileMark{}
	}
	return fileMark{fi.Size(), fi.ModTime()}
}

// watch follows a thread dv has not loaded while pages look at it: its
// rollout growing is a turn written elsewhere, and a new rollout path is the
// thread rewound there.
func (t *codexThread) watch() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	last := ""
	for n := 0; ; n++ {
		<-tick.C
		t.mu.Lock()
		if len(t.subs) == 0 {
			t.watching = false
			t.mu.Unlock()
			return
		}
		loaded, path, mark := t.loaded, t.meta.Path, t.mark
		t.mu.Unlock()
		// Whether a terminal holds it changes how its last turn reads.
		if running := t.running(); running != last {
			last = running
			t.mu.Lock()
			t.version++
			t.mu.Unlock()
			t.signal()
		}
		if loaded {
			continue
		}
		if now := rolloutMark(path); now != mark {
			t.mu.Lock()
			t.mark = now
			t.mu.Unlock()
			t.refreshTail()
		}
		if n%10 == 9 {
			var meta struct {
				Thread codex.Thread `json:"thread"`
			}
			if t.call("thread/read", map[string]any{"threadId": t.id}, &meta) == nil {
				t.mu.Lock()
				moved := path != nil && meta.Thread.Path != nil && *meta.Thread.Path != *path
				t.meta = meta.Thread
				if moved {
					t.history, t.turns = false, nil
				}
				t.mu.Unlock()
				if moved {
					t.readHistory(false)
				}
			}
		}
	}
}

// follow adds a page following the thread.
func (t *codexThread) follow(s *Sub, all bool) func() {
	t.mu.Lock()
	t.subs[s] = true
	start := !t.watching
	t.watching = true
	t.mu.Unlock()
	if start {
		go t.watch()
	}
	go func() {
		t.readHistory(all)
		t.signal()
	}()
	return func() {
		t.mu.Lock()
		delete(t.subs, s)
		t.mu.Unlock()
	}
}

// update is what has changed since the page following with s last asked.
func (t *codexThread) update(s *Sub) Update {
	running := t.running()
	t.mu.Lock()
	defer t.mu.Unlock()
	var u Update
	if s.version != t.version {
		s.version = t.version
		turns := t.turns
		// Read from elsewhere, the turn a terminal is still running reads back as interrupted.
		if n := len(turns); running == "terminal" && n > 0 && turns[n-1].Status == "interrupted" {
			turns = slices.Clone(turns)
			turns[n-1].Status = "inProgress"
		}
		u.Reset, u.Items = s.diff(codexItems(t.side.root, turns, t.todos, t.commands))
	}
	if t.history && !t.whole {
		u.Earlier = &Earlier{Next: "all"}
	}
	u.Live = t.liveLocked(running)
	return u
}

func (t *codexThread) liveLocked(running string) Live {
	l := Live{
		Agent: "codex", Found: len(t.turns) > 0, Running: running, LastModel: t.meta.Model,
		Context: t.context, Status: t.status, Error: t.err, Pending: t.pending,
	}
	if t.asked.model != nil {
		l.Model = *t.asked.model
	}
	if t.asked.effort != nil {
		l.Effort = *t.asked.effort
	}
	if s := t.settings; s != nil {
		l.Using = s.Model
		if s.Effort != nil {
			l.EffortUsing = *s.Effort
		}
	}
	l.Mode = t.modeLocked()
	switch running {
	case "dv":
		l.Busy = t.active != ""
	case "terminal":
		// Read from elsewhere, a turn still going reads back as interrupted.
		if n := len(t.turns); n > 0 && (t.turns[n-1].Status == "inProgress" || t.turns[n-1].Status == "interrupted") {
			l.Busy = time.Since(t.mark.mod) < time.Minute
		}
	}
	if n := len(t.turns); l.Busy && n > 0 && t.turns[n-1].StartedAt != nil {
		l.Since = time.Unix(*t.turns[n-1].StartedAt, 0).UTC().Format(time.RFC3339)
	}
	for _, h := range t.held {
		l.Queued = append(l.Queued, Queued{UUID: h.uuid, Text: h.text, Images: h.images})
	}
	for _, id := range t.order {
		if b := t.blocks[id]; b != nil && b.Text != "" {
			l.Blocks = append(l.Blocks, *b)
		}
	}
	return l
}

// modeLocked is the mode the next turn runs in: one picked in dv, else the
// thread's own settings read as one of dv's modes.
func (t *codexThread) modeLocked() string {
	if t.asked.mode != nil {
		return *t.asked.mode
	}
	if t.settings != nil {
		return modeOf(t.settings)
	}
	return t.side.options().Mode
}

// items is the whole conversation, for the rewind picker.
func (t *codexThread) items() []Item {
	t.readHistory(true)
	t.mu.Lock()
	defer t.mu.Unlock()
	return codexItems(t.side.root, t.turns, t.todos, t.commands)
}

// last is the thread's latest words, for its row.
func (t *codexThread) last() (text, by string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, turn := range slices.Backward(t.turns) {
		for _, it := range slices.Backward(turn.Items) {
			switch it.Type {
			case "agentMessage":
				if s := strings.Join(strings.Fields(it.Text), " "); s != "" {
					return cut(s, lastWords), "agent"
				}
			case "userMessage":
				s, _, _ := codexInputs(it.Inputs())
				s, _, _ = strings.Cut(s, "<dv-context>")
				if s = strings.Join(strings.Fields(s), " "); s != "" {
					return cut(s, lastWords), "you"
				}
			}
		}
	}
	return "", ""
}

// ---- notifications

func (t *codexThread) Notify(method string, params json.RawMessage) {
	var p struct {
		TurnID    string            `json:"turnId"`
		Turn      *codex.Turn       `json:"turn"`
		Item      *codex.Item       `json:"item"`
		ItemID    string            `json:"itemId"`
		Delta     string            `json:"delta"`
		Settings  *codex.Settings   `json:"threadSettings"`
		Usage     *codex.TokenUsage `json:"tokenUsage"`
		Name      *string           `json:"threadName"`
		Error     *codex.Error      `json:"error"`
		WillRetry bool              `json:"willRetry"`
		RequestID json.RawMessage   `json:"requestId"`
		Plan      []struct {
			Step   string `json:"step"`
			Status string `json:"status"`
		} `json:"plan"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	t.mu.Lock()
	wrote, moved := false, true
	var after func()
	switch method {
	case "turn/started":
		if p.Turn != nil {
			t.active, t.err = p.Turn.ID, ""
			t.turnLocked(p.Turn.ID).Status = "inProgress"
			wrote = true
		}
	case "turn/completed":
		if p.Turn != nil {
			turn := t.turnLocked(p.Turn.ID)
			turn.Status, turn.Error, turn.Completed, turn.DurationMs = p.Turn.Status, p.Turn.Error, p.Turn.Completed, p.Turn.DurationMs
			if t.active == p.Turn.ID {
				t.active = ""
			}
			t.blocks, t.order, t.status = map[string]*Block{}, nil, ""
			id := p.Turn.ID
			after = func() { t.turnEnded(id) }
			wrote = true
		}
	case "item/started", "item/completed":
		if p.Item == nil {
			break
		}
		t.itemLocked(p.TurnID, *p.Item)
		if method == "item/completed" {
			delete(t.blocks, p.Item.ID)
		}
		if p.Item.Type == "contextCompaction" {
			t.status = map[bool]string{true: "compacting"}[method == "item/started"]
		}
		wrote = true
	case "item/agentMessage/delta", "item/plan/delta":
		t.streamLocked(p.ItemID, "text", p.Delta)
	case "item/reasoning/summaryTextDelta", "item/reasoning/textDelta":
		t.streamLocked(p.ItemID, "thinking", p.Delta)
	case "item/reasoning/summaryPartAdded":
		t.streamLocked(p.ItemID, "thinking", "\n\n")
	case "turn/plan/updated":
		list := t.todos[p.TurnID]
		if list == nil {
			list = &todoList{}
			if items := t.turnLocked(p.TurnID).Items; len(items) > 0 {
				list.after = items[len(items)-1].ID
			}
			t.todos[p.TurnID] = list
		}
		list.steps = nil
		for _, s := range p.Plan {
			status := map[string]string{"inProgress": "in_progress", "completed": "completed"}[s.Status]
			list.steps = append(list.steps, map[string]string{"content": s.Step, "status": cmp.Or(status, "pending")})
		}
		wrote = true
	case "thread/tokenUsage/updated":
		if u := p.Usage; u != nil && u.Window != nil && *u.Window > 0 {
			t.context = &Context{Used: u.Last.Total, Max: *u.Window}
		}
	case "thread/settings/updated":
		t.settings = p.Settings
	case "thread/name/updated":
		t.meta.Name = p.Name
		t.side.forgetList()
	case "thread/compacted":
		t.status = ""
	case "error":
		if p.Error != nil && !p.WillRetry {
			t.err = p.Error.Message
		}
	case "serverRequest/resolved":
		if cancel := t.asks[strings.Trim(string(p.RequestID), `"`)]; cancel != nil {
			cancel()
		}
	case "thread/closed":
		t.loaded = false
	case "thread/reverted":
		t.history = false
		after = func() { t.readHistory(true) }
	default:
		moved = false
	}
	if wrote {
		t.version++
	}
	t.mu.Unlock()
	if moved {
		t.signal()
	}
	if after != nil {
		go after()
	}
}

func (t *codexThread) Exited() {
	t.mu.Lock()
	if t.active != "" {
		t.err = "Codex stopped"
		t.turnLocked(t.active).Status = "interrupted"
	}
	for _, cancel := range t.asks {
		cancel()
	}
	t.loaded, t.active, t.pending = false, "", t.asked != askedSettings{}
	t.blocks, t.order, t.status = map[string]*Block{}, nil, ""
	t.version++
	t.mu.Unlock()
	t.signal()
}

// turnLocked is the turn under id, added at the end if it is new.
func (t *codexThread) turnLocked(id string) *codex.Turn {
	for i := range t.turns {
		if t.turns[i].ID == id {
			return &t.turns[i]
		}
	}
	now := time.Now().Unix()
	t.turns = append(t.turns, codex.Turn{ID: id, Status: "inProgress", StartedAt: &now})
	return &t.turns[len(t.turns)-1]
}

func (t *codexThread) itemLocked(turnID string, it codex.Item) {
	turn := t.turnLocked(turnID)
	for i := range turn.Items {
		if turn.Items[i].ID == it.ID {
			turn.Items[i] = it
			return
		}
	}
	turn.Items = append(turn.Items, it)
}

func (t *codexThread) streamLocked(item, kind, delta string) {
	b := t.blocks[item]
	if b == nil {
		b = &Block{Kind: kind}
		t.blocks[item] = b
		t.order = append(t.order, item)
	}
	b.Text += delta
}

// turnEnded settles what waited on a turn: its items as the rollout has them,
// settings picked while it ran, messages sent meanwhile, and a plan to approve.
func (t *codexThread) turnEnded(turn string) {
	var page struct {
		Data []codex.Turn `json:"data"`
	}
	if t.call("thread/turns/list", map[string]any{"threadId": t.id, "limit": 1, "itemsView": "full", "sortDirection": "desc"}, &page) == nil && len(page.Data) == 1 && page.Data[0].ID == turn {
		t.mu.Lock()
		held := t.turnLocked(turn)
		// The rollout can lag the notifications by a moment; the fuller one wins.
		if len(page.Data[0].Items) >= len(held.Items) {
			held.Items = page.Data[0].Items
			t.version++
		}
		if held.DurationMs == nil && page.Data[0].DurationMs != nil {
			held.DurationMs = page.Data[0].DurationMs
			t.version++
		}
		t.mu.Unlock()
		t.signal()
	}
	t.side.spent()

	t.op.Lock()
	t.mu.Lock()
	apply := t.pending && t.loaded && t.active == ""
	t.mu.Unlock()
	if apply {
		t.applySettings()
	}
	t.op.Unlock()
	t.flushAll()

	t.mu.Lock()
	plan := ""
	if t.plans[turn] && t.active == "" {
		for _, it := range t.turnLocked(turn).Items {
			if it.Type == "plan" {
				plan = it.Text
			}
		}
	}
	t.mu.Unlock()
	if strings.TrimSpace(plan) != "" {
		t.askPlan(plan)
	}
}

// ---- running turns

// load makes dv's app-server the thread's writer, if it is not already.
func (t *codexThread) load() error {
	t.mu.Lock()
	loaded := t.loaded
	t.mu.Unlock()
	if loaded {
		return nil
	}
	if t.side.client.HeldElsewhere(t.id) {
		return errTerminal
	}
	var r threadResponse
	err := t.call("thread/resume", map[string]any{"threadId": t.id, "excludeTurns": true}, &r)
	switch {
	case codex.IsActiveWriter(err):
		return errTerminal
	case codex.IsNoRollout(err):
		return errors.New("nothing was sent in this session before dv stopped, so Codex cannot pick it up; start a new one")
	case err != nil:
		return err
	}
	t.side.client.Keep(t.id, true)
	t.mu.Lock()
	t.loaded, t.settings, t.meta, t.err = true, r.settings(), r.Thread, ""
	t.pending = t.asked != askedSettings{}
	t.mu.Unlock()
	t.signal()
	return nil
}

// send starts a turn with a message, or holds it to steer into the one running.
func (t *codexThread) send(uuid, text string, images []Image) (string, error) {
	t.op.Lock()
	defer t.op.Unlock()
	t.cancelPlan()
	if err := t.load(); err != nil {
		return "", err
	}
	t.mu.Lock()
	t.lastUsed = time.Now()
	t.mu.Unlock()
	if strings.TrimSpace(text) == "/compact" {
		if err := t.call("thread/compact/start", map[string]any{"threadId": t.id}, nil); err != nil {
			return "", err
		}
		t.mu.Lock()
		after := ""
		if n := len(t.turns); n > 0 {
			after = t.turns[n-1].ID
		}
		t.commands[after] = append(t.commands[after], Item{Key: uuid, Kind: "command", Text: "/compact", UUID: uuid})
		t.status = "compacting"
		t.version++
		t.mu.Unlock()
		t.signal()
		return uuid, nil
	}
	input := t.input(text, images)
	t.mu.Lock()
	if t.active != "" {
		h := &heldMessage{uuid: uuid, text: text, images: len(images), input: input}
		h.timer = time.AfterFunc(holdFor, func() { t.flush(uuid) })
		t.held = append(t.held, h)
		t.mu.Unlock()
		t.signal()
		return uuid, nil
	}
	t.mu.Unlock()
	return uuid, t.start(uuid, input)
}

// input is a message as Codex takes it: a skill it names, its text, its pictures.
func (t *codexThread) input(text string, images []Image) []map[string]any {
	var in []map[string]any
	if name, rest, _ := strings.Cut(strings.TrimPrefix(text, "/"), " "); strings.HasPrefix(text, "/") {
		for _, s := range t.side.skills() {
			if s.Name == name {
				in = append(in, map[string]any{"type": "skill", "name": s.Name, "path": s.Path})
				text = strings.TrimSpace(rest)
				break
			}
		}
	}
	if text != "" || len(in) == 0 {
		in = append(in, map[string]any{"type": "text", "text": text})
	}
	for _, img := range images {
		in = append(in, map[string]any{"type": "image", "url": "data:" + img.MediaType + ";base64," + base64.StdEncoding.EncodeToString(img.Data)})
	}
	return in
}

// start runs a turn. Callers hold op.
func (t *codexThread) start(uuid string, input []map[string]any) error {
	params := t.settingsParams()
	params["threadId"], params["input"], params["clientUserMessageId"] = t.id, input, uuid
	var r struct {
		Turn codex.Turn `json:"turn"`
	}
	if err := t.call("turn/start", params, &r); err != nil {
		return err
	}
	t.mu.Lock()
	t.active, t.pending, t.err = r.Turn.ID, false, ""
	t.turnLocked(r.Turn.ID)
	t.plans[r.Turn.ID] = t.modeLocked() == "plan"
	t.version++
	t.mu.Unlock()
	t.signal()
	return nil
}

// steer adds to the running turn, or starts one if it has ended. Callers hold op.
func (t *codexThread) steer(uuid string, input []map[string]any) error {
	t.mu.Lock()
	active := t.active
	t.mu.Unlock()
	if active != "" {
		err := t.call("turn/steer", map[string]any{"threadId": t.id, "expectedTurnId": active, "input": input, "clientUserMessageId": uuid}, nil)
		if err == nil {
			return nil
		}
	}
	return t.start(uuid, input)
}

// flush steers a held message in once its moment to be taken back is over.
func (t *codexThread) flush(uuid string) {
	t.op.Lock()
	defer t.op.Unlock()
	t.mu.Lock()
	i := slices.IndexFunc(t.held, func(h *heldMessage) bool { return h.uuid == uuid })
	if i < 0 {
		t.mu.Unlock()
		return
	}
	h := t.held[i]
	t.held = slices.Delete(t.held, i, i+1)
	t.mu.Unlock()
	if err := t.steer(h.uuid, h.input); err != nil {
		t.fail(err)
	}
	t.signal()
}

// flushAll sends what was held once the turn it waited on ends: the first
// starts a turn, the rest steer into it.
func (t *codexThread) flushAll() {
	t.op.Lock()
	defer t.op.Unlock()
	t.mu.Lock()
	held := t.held
	t.held = nil
	t.mu.Unlock()
	for _, h := range held {
		h.timer.Stop()
		if err := t.steer(h.uuid, h.input); err != nil {
			t.fail(err)
		}
	}
	if len(held) > 0 {
		t.signal()
	}
}

func (t *codexThread) fail(err error) {
	t.mu.Lock()
	t.err = err.Error()
	t.mu.Unlock()
	t.signal()
}

// unqueue takes back a message still held.
func (t *codexThread) unqueue(uuid string) bool {
	t.mu.Lock()
	i := slices.IndexFunc(t.held, func(h *heldMessage) bool { return h.uuid == uuid })
	if i >= 0 {
		t.held[i].timer.Stop()
		t.held = slices.Delete(t.held, i, i+1)
	}
	t.mu.Unlock()
	if i >= 0 {
		t.signal()
	}
	return i >= 0
}

func (t *codexThread) interrupt() error {
	t.mu.Lock()
	active := t.active
	t.mu.Unlock()
	if active == "" {
		return nil
	}
	return t.call("turn/interrupt", map[string]any{"threadId": t.id, "turnId": active}, nil)
}

// configure picks the model, mode or effort. Between turns it applies at once;
// during one, from the next.
func (t *codexThread) configure(model, mode, effort *string) error {
	t.op.Lock()
	defer t.op.Unlock()
	opts := t.side.options()
	t.mu.Lock()
	if model != nil {
		t.asked.model = model
		// Codex would carry the thread's effort over to the new model; the page
		// shows the model's own, so that is what is asked for unless one was
		// picked that the model takes.
		if i := slices.IndexFunc(opts.Models, func(o Model) bool { return o.ID == *model }); i >= 0 && effort == nil {
			if m := opts.Models[i]; t.asked.effort == nil || !slices.Contains(m.Efforts, *t.asked.effort) {
				t.asked.effort = nil
				if m.Effort != "" {
					e := m.Effort
					t.asked.effort = &e
				}
			}
		}
	}
	if mode != nil {
		t.asked.mode = mode
	}
	if effort != nil {
		t.asked.effort = effort
	}
	now := t.loaded && t.active == ""
	t.pending = !now
	t.mu.Unlock()
	t.signal()
	if now {
		return t.applySettings()
	}
	return nil
}

// applySettings sends what was picked. Callers hold op.
func (t *codexThread) applySettings() error {
	params := t.settingsParams()
	if len(params) == 0 {
		return nil
	}
	params["threadId"] = t.id
	err := t.call("thread/settings/update", params, nil)
	t.mu.Lock()
	t.pending = err != nil
	t.mu.Unlock()
	t.signal()
	return err
}

// settingsParams are the picks as a turn or a settings update takes them.
func (t *codexThread) settingsParams() map[string]any {
	opts := t.side.options()
	t.mu.Lock()
	defer t.mu.Unlock()
	p := map[string]any{}
	model := ""
	if t.settings != nil {
		model = t.settings.Model
	}
	if m := t.asked.model; m != nil {
		model = cmp.Or(*m, opts.Model, model)
		p["model"] = model
	}
	var effort any
	if t.settings != nil && t.settings.Effort != nil {
		effort = *t.settings.Effort
	}
	if e := t.asked.effort; e != nil {
		p["effort"], effort = *e, *e
	}
	if m := t.asked.mode; m != nil {
		approval, sandbox, reviewer := codexPolicy(*m)
		p["approvalPolicy"], p["sandboxPolicy"], p["approvalsReviewer"] = approval, sandbox, reviewer
		if model = cmp.Or(model, opts.Model); model != "" {
			kind := "default"
			if *m == "plan" {
				kind = "plan"
			}
			p["collaborationMode"] = map[string]any{"mode": kind, "settings": map[string]any{"model": model, "reasoning_effort": effort, "developer_instructions": nil}}
		}
	}
	return p
}

// codexPolicy is one of dv's modes as Codex's settings. Asking before edits
// keeps the sandbox read-only, so edits come as patches to approve, with their
// diffs; plan mode reads freely and changes nothing.
func codexPolicy(mode string) (approval string, sandbox map[string]any, reviewer string) {
	readOnly := map[string]any{"type": "readOnly", "networkAccess": false}
	workspace := map[string]any{"type": "workspaceWrite", "writableRoots": []string{}, "networkAccess": false, "excludeTmpdirEnvVar": false, "excludeSlashTmp": false}
	switch mode {
	case "default":
		return "untrusted", readOnly, "user"
	case "plan":
		return "on-request", readOnly, "user"
	case "auto":
		return "on-request", workspace, "auto_review"
	}
	return "on-request", workspace, "user"
}

// modeOf reads Codex's settings as the nearest of dv's modes.
func modeOf(s *codex.Settings) string {
	switch {
	case s.Collaboration != nil && s.Collaboration.Mode == "plan":
		return "plan"
	case s.Reviewer == "auto_review" || s.Reviewer == "guardian_subagent":
		return "auto"
	case s.Sandbox.Type == "readOnly":
		return "default"
	case s.Sandbox.Type == "dangerFullAccess" || s.Sandbox.Type == "externalSandbox":
		return "fullAccess"
	}
	return "acceptEdits"
}

func (t *codexThread) rename(title string) error {
	if t.running() == "terminal" {
		return errors.New("this session is open in a terminal; rename it there")
	}
	if err := t.call("thread/name/set", map[string]any{"threadId": t.id, "name": title}, nil); err != nil {
		return err
	}
	t.mu.Lock()
	t.meta.Name = &title
	t.mu.Unlock()
	t.side.forgetList()
	t.signal()
	return nil
}

// rewind takes the conversation back to before the turn a prompt started.
// Codex keeps no copies of the files it changes, so the code stays as it is.
func (t *codexThread) rewind(prompt string) error {
	t.readHistory(true)
	t.mu.Lock()
	turn := ""
	for _, tr := range t.turns {
		for _, it := range tr.Items {
			if it.Type == "userMessage" && cmp.Or(it.ClientID, it.ID) == prompt {
				turn = tr.ID
			}
			if it.Type == "userMessage" {
				break
			}
		}
	}
	t.mu.Unlock()
	if turn == "" {
		return errors.New("that message is not the start of a turn Codex can go back to")
	}
	t.op.Lock()
	defer t.op.Unlock()
	t.cancelPlan()
	if err := t.load(); err != nil {
		return err
	}
	if err := t.interrupt(); err != nil {
		return err
	}
	for range 50 {
		t.mu.Lock()
		idle := t.active == ""
		t.mu.Unlock()
		if idle {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := t.call("thread/revert", map[string]any{"threadId": t.id, "beforeTurnId": turn}, nil); err != nil {
		return err
	}
	t.mu.Lock()
	t.history, t.turns = false, nil
	t.mu.Unlock()
	t.readHistory(true)
	return nil
}

// release lets the thread go, as closing a session does: whatever runs in it
// stops, and a terminal can pick it up once the app-server unloads it.
func (t *codexThread) release() {
	t.cancelPlan()
	t.mu.Lock()
	loaded := t.loaded
	for _, cancel := range t.asks {
		cancel()
	}
	for _, h := range t.held {
		h.timer.Stop()
	}
	t.held = nil
	t.mu.Unlock()
	if !loaded {
		return
	}
	t.interrupt()
	t.call("thread/unsubscribe", map[string]any{"threadId": t.id}, nil)
	t.side.client.Keep(t.id, false)
	t.mu.Lock()
	t.loaded, t.active = false, ""
	t.version++
	t.mu.Unlock()
	t.signal()
}

// ---- asking the reader

func (t *codexThread) Request(id, method string, params json.RawMessage) (any, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t.mu.Lock()
	t.asks[id] = cancel
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		delete(t.asks, id)
		t.mu.Unlock()
	}()
	switch method {
	case "item/commandExecution/requestApproval":
		return t.approveCommand(ctx, params), nil
	case "item/fileChange/requestApproval":
		return t.approveChange(ctx, params), nil
	case "item/tool/requestUserInput":
		return t.askQuestions(ctx, params), nil
	case "item/permissions/requestApproval":
		return t.approvePermissions(ctx, params), nil
	case "mcpServer/elicitation/request":
		return t.answerElicitation(ctx, params), nil
	}
	return nil, errors.New("dv does not handle " + method)
}

// put asks the reader, and afterwards passes on a note they wrote.
func (t *codexThread) put(ctx context.Context, req *permit.Request) *permit.Answer {
	req.Session, req.Via = t.id, "codex"
	return t.side.broker.Put(ctx, req)
}

// tell passes a note from an answer on to the turn, which reads it once the
// call it was about is done.
func (t *codexThread) tell(allowed bool, note string) {
	note = strings.TrimSpace(note)
	if note == "" {
		return
	}
	text := "The user declined this in dv and said: " + note
	if allowed {
		text = "The user allowed this in dv, with a note: " + note
	}
	go func() {
		t.op.Lock()
		defer t.op.Unlock()
		t.steer(newUUID(), []map[string]any{{"type": "text", "text": text}})
	}()
}

func (t *codexThread) approveCommand(ctx context.Context, params json.RawMessage) any {
	var p struct {
		codex.Item
		Reason    *string           `json:"reason"`
		Decisions []json.RawMessage `json:"availableDecisions"`
	}
	json.Unmarshal(params, &p)
	command := commandText(p.Item)
	input, _ := json.Marshal(map[string]string{"command": command, "description": deref(p.Reason)})
	req := &permit.Request{Tool: "Bash", Input: input}
	var offered []json.RawMessage
	for _, d := range p.Decisions {
		var name string
		var amend struct {
			Exec *struct {
				Rule []string `json:"execpolicy_amendment"`
			} `json:"acceptWithExecpolicyAmendment"`
			Network *struct {
				Rule struct {
					Host string `json:"host"`
				} `json:"network_policy_amendment"`
			} `json:"applyNetworkPolicyAmendment"`
		}
		json.Unmarshal(d, &name)
		json.Unmarshal(d, &amend)
		var s map[string]any
		switch {
		case name == "acceptForSession":
			s = rulesSuggestion("Bash", command, "session")
		case amend.Exec != nil:
			s = rulesSuggestion("Bash", strings.Join(amend.Exec.Rule, " ")+":*", "codexRules")
		case amend.Network != nil:
			s = rulesSuggestion("Network", amend.Network.Rule.Host, "codexRules")
		default:
			continue
		}
		raw, _ := json.Marshal(s)
		req.Suggestions = append(req.Suggestions, raw)
		offered = append(offered, d)
	}
	a := t.put(ctx, req)
	return map[string]any{"decision": t.decision(a, offered)}
}

func rulesSuggestion(tool, rule, where string) map[string]any {
	return map[string]any{"type": "addRules", "behavior": "allow", "destination": where, "rules": []map[string]string{{"toolName": tool, "ruleContent": rule}}}
}

// decision is an answer as Codex takes it. A bare no stops the turn, as it does
// for Claude; a no with a note lets it go on, told why.
func (t *codexThread) decision(a *permit.Answer, offered []json.RawMessage) any {
	switch {
	case a == nil:
		return "cancel"
	case a.Allow:
		t.tell(true, a.Note)
		if i := a.Suggestion; i != nil && *i >= 0 && *i < len(offered) {
			return offered[*i]
		}
		return "accept"
	case strings.TrimSpace(a.Note) != "":
		t.tell(false, a.Note)
		return "decline"
	}
	return "cancel"
}

func (t *codexThread) approveChange(ctx context.Context, params json.RawMessage) any {
	var p struct {
		TurnID string `json:"turnId"`
		ItemID string `json:"itemId"`
	}
	json.Unmarshal(params, &p)
	t.mu.Lock()
	var changes []codex.Change
	for _, it := range t.turnLocked(p.TurnID).Items {
		if it.ID == p.ItemID {
			changes = it.Changes
		}
	}
	t.mu.Unlock()
	req := &permit.Request{Tool: "Edit"}
	if len(changes) == 1 && changes[0].Kind.Type == "add" {
		req.Tool = "Write"
	}
	files := make([]string, len(changes))
	var rules []map[string]string
	for i, ch := range changes {
		files[i] = changePath(ch)
		name := files[i]
		if ed, err := codexEdit(t.side.root, ch, false); err == nil {
			req.Previews = append(req.Previews, &permit.Preview{Path: ed.Path, InRepo: ed.InRepo, Diff: ed.Diff})
			name = ed.Path
		}
		rules = append(rules, map[string]string{"toolName": "Edit", "ruleContent": name})
	}
	input := map[string]any{"files": files}
	if len(files) == 1 {
		input = map[string]any{"file_path": files[0]}
	}
	req.Input, _ = json.Marshal(input)
	// Codex's yes for the session stops it asking about these files, not every edit.
	sug, _ := json.Marshal(map[string]any{"type": "addRules", "behavior": "allow", "destination": "session", "rules": rules})
	req.Suggestions = []json.RawMessage{sug}
	return map[string]any{"decision": t.decision(t.put(ctx, req), []json.RawMessage{json.RawMessage(`"acceptForSession"`)})}
}

func (t *codexThread) askQuestions(ctx context.Context, params json.RawMessage) any {
	var p struct {
		Questions []struct {
			ID       string `json:"id"`
			Header   string `json:"header"`
			Question string `json:"question"`
			Options  []struct {
				Label       string `json:"label"`
				Description string `json:"description"`
			} `json:"options"`
		} `json:"questions"`
	}
	json.Unmarshal(params, &p)
	type option struct {
		Label       string `json:"label"`
		Description string `json:"description,omitempty"`
	}
	type question struct {
		Question    string   `json:"question"`
		Header      string   `json:"header"`
		Options     []option `json:"options"`
		MultiSelect bool     `json:"multiSelect"`
	}
	qs := make([]question, len(p.Questions))
	for i, q := range p.Questions {
		qs[i] = question{Question: q.Question, Header: q.Header, Options: []option{}}
		for _, o := range q.Options {
			qs[i].Options = append(qs[i].Options, option(o))
		}
	}
	input, _ := json.Marshal(map[string]any{"questions": qs})
	a := t.put(ctx, &permit.Request{Tool: "AskUserQuestion", Input: input})
	answers := map[string]any{}
	if a != nil && a.Allow {
		for _, q := range p.Questions {
			said, ok := a.Answers[q.Question]
			if !ok {
				continue
			}
			var note struct {
				Notes string `json:"notes"`
			}
			json.Unmarshal(a.Annotations[q.Question], &note)
			if n := strings.TrimSpace(note.Notes); n != "" {
				said += " (" + n + ")"
			}
			answers[q.ID] = map[string]any{"answers": []string{said}}
		}
	}
	return map[string]any{"answers": answers}
}

func (t *codexThread) approvePermissions(ctx context.Context, params json.RawMessage) any {
	var p struct {
		Reason      *string         `json:"reason"`
		Permissions json.RawMessage `json:"permissions"`
	}
	json.Unmarshal(params, &p)
	input, _ := json.Marshal(map[string]any{"reason": deref(p.Reason), "permissions": p.Permissions})
	sug, _ := json.Marshal(map[string]any{"type": "addDirectories", "directories": []string{"what it asks for, for the rest of the session"}, "destination": "session"})
	a := t.put(ctx, &permit.Request{Tool: "Permissions", Input: input, Suggestions: []json.RawMessage{sug}})
	if a == nil || !a.Allow {
		return map[string]any{"permissions": map[string]any{}}
	}
	scope := "turn"
	if a.Suggestion != nil {
		scope = "session"
	}
	t.tell(true, a.Note)
	return map[string]any{"permissions": p.Permissions, "scope": scope}
}

func (t *codexThread) answerElicitation(ctx context.Context, params json.RawMessage) any {
	var p struct {
		Server string `json:"serverName"`
	}
	json.Unmarshal(params, &p)
	a := t.put(ctx, &permit.Request{Tool: "mcp__" + p.Server + "__elicitation", Input: params})
	if a == nil || !a.Allow {
		return map[string]any{"action": "decline"}
	}
	return map[string]any{"action": "accept", "content": map[string]any{}}
}

// askPlan puts a plan finished in plan mode to the reader, as Claude's
// ExitPlanMode is. Yes leaves plan mode and has Codex carry it out; no with a
// note stays in plan mode and sends the note.
func (t *codexThread) askPlan(plan string) {
	ctx, cancel := context.WithCancel(context.Background())
	t.mu.Lock()
	if t.planAsk != nil {
		t.planAsk()
	}
	t.planAsk = cancel
	t.mu.Unlock()
	input, _ := json.Marshal(map[string]string{"plan": plan})
	go func() {
		defer cancel()
		a := t.put(ctx, &permit.Request{Tool: "ExitPlanMode", Input: input, Suggestions: permit.PlanModes})
		if a == nil {
			return
		}
		note := strings.TrimSpace(a.Note)
		switch {
		case a.Allow:
			var set struct{ Mode string }
			if i := a.Suggestion; i != nil && *i >= 0 && *i < len(permit.PlanModes) {
				json.Unmarshal(permit.PlanModes[*i], &set)
			}
			mode := cmp.Or(set.Mode, "default")
			if err := t.configure(nil, &mode, nil); err != nil {
				t.fail(err)
				return
			}
			if _, err := t.send(newUUID(), cmp.Or(note, "Go ahead with the plan."), nil); err != nil {
				t.fail(err)
			}
		case note != "":
			if _, err := t.send(newUUID(), note, nil); err != nil {
				t.fail(err)
			}
		}
	}()
}

func (t *codexThread) cancelPlan() {
	t.mu.Lock()
	cancel := t.planAsk
	t.planAsk = nil
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
