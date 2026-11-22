package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

const boardName = "board.json"

var ErrNoTicket = errors.New("no such ticket")

type TicketError string

func (e TicketError) Error() string { return string(e) }

// Stages are a ticket's columns, in order.
var Stages = []string{"inbox", "scope", "plan", "build", "agent-review", "review", "merged"}

// Ticket is one piece of work on a repository's board. What its agents write -
// the full ticket and the plan - are Markdown files in its folder, not fields.
type Ticket struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Request string   `json:"request,omitempty"` // the words it was created with
	Labels  []string `json:"labels,omitempty"`
	// Images are the file names of the pictures it was given, in its folder.
	Images []string `json:"images,omitempty"`
	// Archived is put out of the board's way, at whatever stage, until restored.
	Archived bool   `json:"archived,omitempty"`
	Stage    string `json:"stage"`
	Started  string `json:"started"`           // the first stage it entered after the inbox
	Reached  string `json:"reached,omitempty"` // the furthest stage, once it has been sent back
	// Ready is the stage's agent saying its work is there to approve; any move
	// clears it.
	Ready bool `json:"ready,omitempty"`
	// Split is its scope approved as sub-tickets, which carry the work.
	Split  bool   `json:"split,omitempty"`
	Status string `json:"status,omitempty"`
	Parent string `json:"parent,omitempty"`
	// After is the tickets this one waits on, by ID; one merged stops counting.
	After []string `json:"after,omitempty"`
	// Sessions holds the agent session of each role: scope, planner, builder,
	// reviewer.
	Sessions map[string]string `json:"sessions,omitempty"`
	Model    map[string]string `json:"model,omitempty"` // by role, "" for the session's default
	Branch   string            `json:"branch,omitempty"`
	Worktree string            `json:"worktree,omitempty"`
	PR       int               `json:"pr,omitempty"`
	CI       string            `json:"ci,omitempty"` // passing | failing | pending, "" for no checks
	// Merging is the reader's approval: dv merges once CI passes.
	Merging bool `json:"merging,omitempty"`
	// Problem is what dv last failed to do for the ticket, until it next
	// succeeds or the ticket moves.
	Problem  string     `json:"problem,omitempty"`
	Question *Question  `json:"question,omitempty"`
	Answered []Question `json:"answered,omitempty"`
	Rounds   []Round    `json:"rounds,omitempty"`
	// PlanReview is another model's review of the plan, which the planner
	// answers before the reader sees the plan. NoPlanReview skips it.
	PlanReview   *Round `json:"planReview,omitempty"`
	NoPlanReview bool   `json:"noPlanReview,omitempty"`
	// EarlierPlanReviews are the plan's reviews before the current one,
	// oldest first.
	EarlierPlanReviews []Round `json:"earlierPlanReviews,omitempty"`
	// Templates are the reviews the next round runs.
	Templates []string  `json:"templates,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Question is what the builder found the plan doesn't settle.
type Question struct {
	Text   string    `json:"text"`
	Answer string    `json:"answer,omitempty"`
	By     string    `json:"by,omitempty"`     // planner | you
	Source string    `json:"source,omitempty"` // what the planner's answer rests on
	At     time.Time `json:"at"`
}

// Round is one pass of agent review.
type Round struct {
	Templates []string  `json:"templates"`
	Model     string    `json:"model,omitempty"`
	Session   string    `json:"session,omitempty"` // the reviewer's, fresh each round
	Findings  []Finding `json:"findings"`
	// Phase is who the round waits on: review, fix, recheck, done.
	Phase string    `json:"phase"`
	At    time.Time `json:"at"`
}

type Finding struct {
	ID      string `json:"id"`
	Text    string `json:"text"`
	Verdict string `json:"verdict,omitempty"` // fixed | rejected
	Reason  string `json:"reason,omitempty"`
	Recheck string `json:"recheck,omitempty"` // holds | fails
	Note    string `json:"note,omitempty"`    // the reviewer's word on its recheck
}

type boardDoc struct {
	Format  int       `json:"format"`
	Prefix  string    `json:"prefix"`
	Next    int       `json:"next"`
	Tickets []*Ticket `json:"tickets"`
}

// SetAsidePlanReview keeps the current plan review among the earlier ones, for
// a new one to take its place.
func (t *Ticket) SetAsidePlanReview() {
	if t.PlanReview != nil {
		t.EarlierPlanReviews = append(t.EarlierPlanReviews, *t.PlanReview)
		t.PlanReview = nil
	}
}

// Board owns a repository's tickets. Like the notes it lives in the common
// dir, so every worktree of the repository sees one board.
type Board struct {
	mu     sync.Mutex
	file   jsonFile
	doc    boardDoc
	prefix string
}

// OpenBoard loads the board of the repository at root; prefix starts its
// ticket IDs until the board has one of its own. It creates nothing.
func OpenBoard(root, commonDir, prefix string) (*Board, error) {
	dir := filepath.Join(root, dirName, "board")
	if commonDir != "" {
		dir = filepath.Join(commonDir, "dv", "board")
	}
	b := &Board{file: jsonFile{path: filepath.Join(dir, boardName)}, doc: emptyBoard(), prefix: TicketPrefix(prefix)}
	if err := b.sync(); err != nil {
		return nil, err
	}
	return b, nil
}

func emptyBoard() boardDoc { return boardDoc{Format: format, Next: 1, Tickets: []*Ticket{}} }

var notLetter = regexp.MustCompile(`[^A-Za-z0-9]+`)

// TicketPrefix makes a repository's name into the start of its ticket IDs.
func TicketPrefix(name string) string {
	p := strings.ToUpper(notLetter.ReplaceAllString(name, ""))
	if len(p) > 6 {
		p = p[:6]
	}
	return cmpOr(p, "T")
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (b *Board) sync() error {
	doc := emptyBoard()
	changed, err := b.file.load(&doc)
	if err != nil || !changed {
		return err
	}
	if doc.Tickets == nil {
		doc.Tickets = []*Ticket{}
	}
	b.doc = doc
	return nil
}

// Dir holds the board file and a folder of documents per ticket.
func (b *Board) Dir() string { return filepath.Dir(b.file.path) }

// DocPath is where a ticket's document is kept: "ticket" or "plan".
func (b *Board) DocPath(id, doc string) string {
	return filepath.Join(b.Dir(), id, doc+".md")
}

// SaveDoc writes a ticket's document, keeping the text it replaces as a
// version, so what was there before a revision can still be read.
func (b *Board) SaveDoc(id, doc, text string) error {
	text = strings.TrimSpace(text) + "\n"
	path := b.DocPath(id, doc)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	old, err := os.ReadFile(path)
	if err == nil && string(old) != text {
		dir := filepath.Join(b.Dir(), id, "versions", doc)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		n := len(b.versionFiles(id, doc)) + 1
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%04d.md", n)), old, 0o644); err != nil {
			return err
		}
	}
	return os.WriteFile(path, []byte(text), 0o644)
}

func (b *Board) versionFiles(id, doc string) []string {
	files, _ := filepath.Glob(filepath.Join(b.Dir(), id, "versions", doc, "*.md"))
	slices.Sort(files)
	return files
}

// DocVersion is one text a ticket's document had, and when it was replaced.
type DocVersion struct {
	Text     string    `json:"text"`
	Replaced time.Time `json:"replaced"`
}

// DocVersions are the texts a ticket's document had before its current one,
// oldest first.
func (b *Board) DocVersions(id, doc string) []DocVersion {
	out := []DocVersion{}
	for _, f := range b.versionFiles(id, doc) {
		data, err := os.ReadFile(f)
		fi, serr := os.Stat(f)
		if err != nil || serr != nil {
			continue
		}
		out = append(out, DocVersion{Text: string(data), Replaced: fi.ModTime().UTC()})
	}
	return out
}

// Doc reads a ticket's document, "" when none was written.
func (b *Board) Doc(id, doc string) (string, error) {
	data, err := os.ReadFile(b.DocPath(id, doc))
	if os.IsNotExist(err) {
		return "", nil
	}
	return string(data), err
}

var imageExt = map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}

// ImageType is the media type of a ticket's image by its name, "" for none.
func ImageType(name string) string {
	for t, ext := range imageExt {
		if strings.HasSuffix(name, ext) {
			return t
		}
	}
	return ""
}

// ImagePath is where a ticket's image is kept; name is one of its Images.
func (b *Board) ImagePath(id, name string) string {
	return filepath.Join(b.Dir(), id, "images", filepath.Base(name))
}

// AddImage keeps a picture with a ticket and returns its name.
func (b *Board) AddImage(id, mediaType string, data []byte) (string, error) {
	ext, ok := imageExt[mediaType]
	if !ok {
		return "", TicketError("a ticket takes PNG, JPEG, GIF and WebP images, not " + mediaType)
	}
	name := newID("img") + ext
	path := b.ImagePath(id, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	if _, err := b.Update(id, func(t *Ticket) error { t.Images = append(t.Images, name); return nil }); err != nil {
		os.Remove(path)
		return "", err
	}
	return name, nil
}

// RemoveImage takes a picture off a ticket.
func (b *Board) RemoveImage(id, name string) error {
	if _, err := b.Update(id, func(t *Ticket) error {
		i := slices.Index(t.Images, name)
		if i < 0 {
			return TicketError(id + " has no image " + name)
		}
		t.Images = slices.Delete(t.Images, i, i+1)
		return nil
	}); err != nil {
		return err
	}
	return os.Remove(b.ImagePath(id, name))
}

// Version moves whenever the board file does, whoever changed it. The
// documents have their own stamps.
func (b *Board) Version() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sync()
	return b.file.stamp
}

func (b *Board) List() []Ticket {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sync()
	out := make([]Ticket, len(b.doc.Tickets))
	for i, t := range b.doc.Tickets {
		out[i] = t.clone()
	}
	return out
}

func (b *Board) Get(id string) (Ticket, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.sync(); err != nil {
		return Ticket{}, err
	}
	t := b.find(id)
	if t == nil {
		return Ticket{}, fmt.Errorf("%w: %s", ErrNoTicket, id)
	}
	return t.clone(), nil
}

// Add files a ticket and gives it the board's next ID. It starts in the inbox
// unless a stage is given.
func (b *Board) Add(t Ticket) (Ticket, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.sync(); err != nil {
		return Ticket{}, err
	}
	t.Title = strings.TrimSpace(t.Title)
	t.Request = strings.TrimSpace(t.Request)
	t.Labels = cleanLabels(t.Labels)
	if t.Stage == "" {
		t.Stage = "inbox"
	}
	if t.Started == "" {
		t.Started = t.Stage
	}
	if t.Parent != "" && b.find(t.Parent) == nil {
		return Ticket{}, fmt.Errorf("%w: %s", ErrNoTicket, t.Parent)
	}
	if err := b.check(&t); err != nil {
		return Ticket{}, err
	}
	if b.doc.Prefix == "" {
		b.doc.Prefix = b.prefix
	}
	t.ID = fmt.Sprintf("%s-%d", b.doc.Prefix, b.doc.Next)
	b.doc.Next++
	now := time.Now().UTC().Truncate(time.Second)
	t.CreatedAt, t.UpdatedAt = now, now
	b.doc.Tickets = append(b.doc.Tickets, &t)
	return t.clone(), b.file.save(b.doc)
}

// Update changes a ticket by change, which sees the ticket as it is on disk
// now; an error from it leaves the ticket as it was.
func (b *Board) Update(id string, change func(*Ticket) error) (Ticket, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.sync(); err != nil {
		return Ticket{}, err
	}
	i := slices.IndexFunc(b.doc.Tickets, func(t *Ticket) bool { return t.ID == id })
	if i < 0 {
		return Ticket{}, fmt.Errorf("%w: %s", ErrNoTicket, id)
	}
	t := b.doc.Tickets[i].clone()
	if err := change(&t); err != nil {
		return Ticket{}, err
	}
	t.Title = strings.TrimSpace(t.Title)
	t.Labels = cleanLabels(t.Labels)
	if err := b.check(&t); err != nil {
		return Ticket{}, err
	}
	t.UpdatedAt = time.Now().UTC().Truncate(time.Second)
	b.doc.Tickets[i] = &t
	return t.clone(), b.file.save(b.doc)
}

// Delete removes a ticket and its documents. Its sub-tickets stay, without a
// parent.
func (b *Board) Delete(id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.sync(); err != nil {
		return err
	}
	i := slices.IndexFunc(b.doc.Tickets, func(t *Ticket) bool { return t.ID == id })
	if i < 0 {
		return fmt.Errorf("%w: %s", ErrNoTicket, id)
	}
	b.doc.Tickets = slices.Delete(b.doc.Tickets, i, i+1)
	for _, t := range b.doc.Tickets {
		if t.Parent == id {
			t.Parent = ""
		}
		t.After = slices.DeleteFunc(t.After, func(a string) bool { return a == id })
	}
	if err := b.file.save(b.doc); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(b.Dir(), id))
}

func (b *Board) find(id string) *Ticket {
	for _, t := range b.doc.Tickets {
		if t.ID == id {
			return t
		}
	}
	return nil
}

func (b *Board) check(t *Ticket) error {
	switch {
	case t.Title == "":
		return TicketError("a ticket needs a title")
	case !slices.Contains(Stages, t.Stage):
		return TicketError(fmt.Sprintf("stage %q is not one of %s", t.Stage, strings.Join(Stages, ", ")))
	case !slices.Contains(Stages, t.Started):
		return TicketError(fmt.Sprintf("stage %q is not one of %s", t.Started, strings.Join(Stages, ", ")))
	}
	for _, a := range t.After {
		if a == t.ID {
			return TicketError("a ticket can't wait on itself")
		}
		if b.find(a) == nil {
			return fmt.Errorf("%w: %s", ErrNoTicket, a)
		}
	}
	return nil
}

// cleanLabels trims labels and drops blanks and repeats, ignoring case.
func cleanLabels(labels []string) []string {
	var out []string
	for _, l := range labels {
		l = strings.Join(strings.Fields(l), " ")
		if l != "" && !slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, l) }) {
			out = append(out, l)
		}
	}
	return out
}

// StageIndex is a stage's place in Stages, -1 for none.
func StageIndex(stage string) int { return slices.Index(Stages, stage) }

func (t *Ticket) clone() Ticket {
	c := *t
	c.Labels = slices.Clone(t.Labels)
	c.Images = slices.Clone(t.Images)
	c.After = slices.Clone(t.After)
	c.Templates = slices.Clone(t.Templates)
	c.Answered = slices.Clone(t.Answered)
	if t.Sessions != nil {
		c.Sessions = make(map[string]string, len(t.Sessions))
		for k, v := range t.Sessions {
			c.Sessions[k] = v
		}
	}
	if t.Model != nil {
		c.Model = make(map[string]string, len(t.Model))
		for k, v := range t.Model {
			c.Model[k] = v
		}
	}
	if t.Question != nil {
		q := *t.Question
		c.Question = &q
	}
	if t.PlanReview != nil {
		r := *t.PlanReview
		r.Templates = slices.Clone(r.Templates)
		r.Findings = slices.Clone(r.Findings)
		c.PlanReview = &r
	}
	c.EarlierPlanReviews = nil
	for _, r := range t.EarlierPlanReviews {
		r.Templates = slices.Clone(r.Templates)
		r.Findings = slices.Clone(r.Findings)
		c.EarlierPlanReviews = append(c.EarlierPlanReviews, r)
	}
	c.Rounds = make([]Round, len(t.Rounds))
	for i, r := range t.Rounds {
		r.Templates = slices.Clone(r.Templates)
		r.Findings = slices.Clone(r.Findings)
		c.Rounds[i] = r
	}
	if len(c.Rounds) == 0 {
		c.Rounds = nil
	}
	return c
}
