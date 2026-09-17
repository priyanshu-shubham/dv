package agent

import (
	"cmp"
	"encoding/base64"
	"encoding/json"
	"errors"
	"mime"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"dv/internal/codex"
	"dv/internal/gitx"
)

// A Codex thread is read through the app-server rather than from its files,
// and drawn with the rows Claude's transcripts use: its commands as Bash, its
// patches as edits, its plan as the plan Claude proposes.

// todoList is a turn's plan as Codex last updated it, and the item it came after.
type todoList struct {
	after string
	steps []map[string]string
}

// codexItems is a thread's turns as the conversation is drawn. commands are
// the /compact and /review messages sent from dv, which leave no message in the
// thread, by the turn they came after; reasons are why Codex asked to run a
// command, which it says only when it asks, by the command's id.
func codexItems(root string, turns []codex.Turn, todos map[string]*todoList, commands map[string][]Item, reasons map[string]string) []Item {
	items := append([]Item{}, commands[""]...)
	before := ""
	for _, turn := range turns {
		at := ""
		if turn.StartedAt != nil {
			at = time.Unix(*turn.StartedAt, 0).UTC().Format(time.RFC3339)
		}
		prompted := false
		list := todos[turn.ID]
		placed := list == nil
		place := func() {
			if !placed {
				items = append(items, todoItem(turn.ID, list))
				placed = true
			}
		}
		if list != nil && list.after == "" {
			place()
		}
		// A /review turn's messages are its reviewer's: the prompt Codex wrote
		// it, its findings as JSON, then those findings again as the reply kept.
		review := slices.ContainsFunc(turn.Items, func(it codex.Item) bool { return it.Type == "enteredReviewMode" })
		reviewing, found := false, ""
		for _, it := range turn.Items {
			switch it.Type {
			case "enteredReviewMode":
				reviewing = true
			case "exitedReviewMode":
				reviewing, found = false, strings.TrimSpace(it.Review)
			}
			if review && (it.Type == "userMessage" || it.Type == "agentMessage" && (reviewing || strings.TrimSpace(it.Text) == found)) {
				continue
			}
			items = append(items, codexItem(root, it, at, &prompted, before, reasons[it.ID])...)
			if list != nil && it.ID == list.after {
				place()
			}
		}
		place()
		switch turn.Status {
		case "interrupted":
			items = append(items, Item{Key: turn.ID + ":interrupted", Kind: "note", At: at, Text: "Interrupted"})
		case "failed":
			msg := "The turn failed"
			if turn.Error != nil && turn.Error.Message != "" {
				msg = turn.Error.Message
			}
			items = append(items, Item{Key: turn.ID + ":failed", Kind: "note", At: at, Text: msg, Error: true})
		}
		if turn.Status != "inProgress" && turn.DurationMs != nil && *turn.DurationMs > 0 {
			items = append(items, Item{Key: turn.ID + ":worked", Kind: "worked", At: at, Took: *turn.DurationMs})
		}
		items = append(items, commands[turn.ID]...)
		before = turn.ID
	}
	return items
}

// foldReviewers puts the turn a /review's reviewer ran, as turns are read, back
// inside the review. A thread read back keeps it as a turn of its own, left
// interrupted, just before the review: the one turn out of order, as Codex's
// ids begin with the time they were made.
func foldReviewers(turns []codex.Turn) []codex.Turn {
	for i := 0; i+1 < len(turns); i++ {
		reviewer, review := turns[i], turns[i+1]
		at := slices.IndexFunc(review.Items, func(it codex.Item) bool { return it.Type == "enteredReviewMode" })
		if reviewer.ID <= review.ID || at < 0 {
			continue
		}
		turns[i+1].Items = slices.Concat(review.Items[:at+1], reviewer.Items, review.Items[at+1:])
		turns = slices.Delete(turns, i, i+1)
	}
	return turns
}

func todoItem(turn string, list *todoList) Item {
	input, _ := json.Marshal(map[string]any{"todos": list.steps})
	return Item{Key: "todos:" + turn, Kind: "tool", Tool: "TodoWrite", ToolID: "todos:" + turn, Input: input, Result: &Result{}}
}

// codexItem is one thread item as the page's rows. prompted says whether the
// turn's first message has been drawn: only that one can be rewound to, since
// a thread goes back a turn at a time.
func codexItem(root string, it codex.Item, at string, prompted *bool, before, reason string) []Item {
	// Codex keeps no time for a call, only for its turn, which would make one
	// still running look to have run since the turn began.
	tool := func(name string, input any) Item {
		raw, _ := json.Marshal(input)
		return Item{Key: it.ID, Kind: "tool", Tool: name, ToolID: it.ID, Input: trimInput(raw)}
	}
	done := it.Status != "inProgress" && it.Status != ""
	failed := it.Status == "failed" || it.Status == "declined"
	switch it.Type {
	case "userMessage":
		text, images, skill := codexInputs(it.Inputs())
		p := Item{Key: it.ID, Kind: "prompt", At: at, Text: text, Images: images}
		switch {
		case skill != "":
			p.Kind, p.Text = "command", strings.TrimSpace("/"+skill+" "+text)
		case strings.HasPrefix(text, "<bash-input>"):
			p.Kind, p.Text, p.Result = "shell", strings.TrimSpace(firstGroup(bashInput, text)), shellOutput(text)
		}
		if !*prompted {
			p.UUID, p.Before = cmp.Or(it.ClientID, it.ID), before
			*prompted = true
		}
		return []Item{p}

	case "agentMessage":
		if strings.TrimSpace(it.Text) == "" {
			return nil
		}
		return []Item{{Key: it.ID, Kind: "text", At: at, Text: it.Text}}

	case "reasoning":
		text := strings.Join(it.Summary, "\n\n")
		if strings.TrimSpace(text) == "" {
			text = strings.Join(it.ReasoningText(), "\n\n")
		}
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return []Item{{Key: it.ID, Kind: "thinking", At: at, Text: text}}

	case "plan":
		row := tool("ExitPlanMode", map[string]string{"plan": it.Text})
		row.Result = &Result{}
		return []Item{row}

	case "commandExecution":
		// Run with ! in Codex's own terminal, it is the reader's, as dv's are.
		if it.Source == "userShell" {
			s := Item{Key: it.ID, Kind: "shell", At: at, Text: commandText(it)}
			if done && it.AggregatedOutput != nil {
				s.Result = &Result{Text: cut(strings.TrimRight(*it.AggregatedOutput, "\n"), 4*maxResult)}
			}
			return []Item{s}
		}
		row := tool("Bash", map[string]string{"command": commandText(it), "description": reason})
		if done {
			out := ""
			if it.AggregatedOutput != nil {
				out = *it.AggregatedOutput
			}
			failed = failed || it.ExitCode != nil && *it.ExitCode != 0
			row.Result = &Result{Text: cut(out, maxResult), IsError: failed, Detail: &Detail{Lines: lineCount(out)}}
			if it.DurationMs != nil {
				row.Result.Took = *it.DurationMs
			}
			if it.Status == "declined" {
				row.Result.Text = "Declined"
			}
		}
		return []Item{row}

	case "fileChange":
		rows := make([]Item, 0, len(it.Changes))
		for i, ch := range it.Changes {
			name := "Edit"
			if ch.Kind.Type == "add" {
				name = "Write"
			}
			row := tool(name, map[string]string{"file_path": changePath(ch)})
			row.Key = changeID(it.ID, i)
			row.ToolID = row.Key
			switch {
			case failed:
				row.Result = &Result{IsError: true, Text: it.Status}
			case done:
				row.Result = &Result{Edited: changeStats(root, ch)}
			}
			rows = append(rows, row)
		}
		return rows

	case "mcpToolCall":
		row := tool("mcp__"+it.Server+"__"+it.Tool, it.Arguments)
		if done {
			row.Result = &Result{Text: cut(mcpText(it), maxResult), IsError: failed || it.Error != nil}
		}
		return []Item{row}

	case "dynamicToolCall":
		row := tool(it.Tool, it.Arguments)
		if done {
			row.Result = &Result{Text: cut(mcpText(it), maxResult), IsError: failed}
		}
		return []Item{row}

	case "collabAgentToolCall":
		input := map[string]string{"description": agentVerb(it.Tool)}
		if it.Prompt != nil {
			input["prompt"] = *it.Prompt
		}
		row := tool("Agent", input)
		if done {
			row.Result = &Result{Text: cut(agentStates(it), maxResult), IsError: failed}
		}
		return []Item{row}

	case "webSearch":
		query := it.Query
		if a := it.Action; a != nil {
			switch {
			case a.Query != nil && *a.Query != "":
				query = *a.Query
			case a.URL != nil:
				query = *a.URL
			}
		}
		row := tool("WebSearch", map[string]string{"query": query})
		row.Result = &Result{}
		return []Item{row}

	case "imageView":
		row := tool("Read", map[string]string{"file_path": it.Path})
		row.Result = &Result{Detail: &Detail{Kind: "image"}}
		return []Item{row}

	case "imageGeneration":
		input := map[string]string{"prompt": deref(it.RevisedPrompt)}
		if it.SavedPath != nil {
			input["file_path"] = *it.SavedPath
		}
		row := tool("ImageGeneration", input)
		switch {
		case it.Failure != nil || it.Status == "failed":
			row.Result = &Result{IsError: true, Text: imageFailure(it)}
		case it.SavedPath != nil || len(it.Result) > 2:
			row.Result = &Result{Detail: &Detail{Kind: "image"}}
		}
		return []Item{row}

	case "contextCompaction":
		return []Item{{Key: it.ID, Kind: "compact", At: at, Compacted: &Compacted{}}}

	case "enteredReviewMode":
		return []Item{{Key: it.ID, Kind: "note", At: at, Text: "Reviewing " + it.Review}}

	case "exitedReviewMode":
		if strings.TrimSpace(it.Review) == "" {
			return nil
		}
		return []Item{{Key: it.ID, Kind: "text", At: at, Text: it.Review}}
	}
	return nil
}

// codexInputs reads a message: its text, how many pictures, and the skill it
// invokes, if it does.
func codexInputs(in []codex.Input) (text string, images int, skill string) {
	var parts []string
	for _, p := range in {
		switch p.Type {
		case "text":
			parts = append(parts, p.Text)
		case "image", "localImage":
			images++
		case "skill":
			skill = p.Name
		}
	}
	return strings.Join(parts, "\n"), images, skill
}

func changeID(item string, i int) string { return item + ":" + strconv.Itoa(i) }

func changePath(ch codex.Change) string {
	if ch.Kind.MovePath != nil && *ch.Kind.MovePath != "" {
		return *ch.Kind.MovePath
	}
	return ch.Path
}

func changeStats(root string, ch codex.Change) *Edited {
	e := &Edited{Path: changePath(ch)}
	if rel, err := filepath.Rel(root, e.Path); err == nil && filepath.IsLocal(rel) {
		e.Path, e.InRepo = filepath.ToSlash(rel), true
	}
	switch ch.Kind.Type {
	case "add":
		e.Created, e.Adds = true, lineCount(ch.Diff)
	case "delete":
		e.Dels = lineCount(ch.Diff)
	default:
		for _, h := range parseHunks(ch.Diff) {
			for _, l := range h.Lines {
				switch {
				case strings.HasPrefix(l, "+"):
					e.Adds++
				case strings.HasPrefix(l, "-"):
					e.Dels++
				}
			}
		}
	}
	return e
}

// shellWrapped is how Codex records a command: the user's shell, told to run it.
var shellWrapped = regexp.MustCompile(`^\S*sh -l?c (.+)$`)

// commandText is the command as it was written, out of its shell wrapper.
func commandText(it codex.Item) string {
	if m := shellWrapped.FindStringSubmatch(strings.TrimSpace(it.Command)); m != nil {
		if s, ok := unquoteShell(m[1]); ok {
			return s
		}
	}
	if len(it.CommandActions) > 0 {
		parts := make([]string, len(it.CommandActions))
		for i, a := range it.CommandActions {
			parts[i] = a.Command
		}
		return strings.Join(parts, " && ")
	}
	return it.Command
}

// unquoteShell undoes one argument's quoting, as a POSIX shell reads it.
func unquoteShell(s string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(s); {
		switch c := s[i]; c {
		case '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return "", false
			}
			b.WriteString(s[i+1 : i+1+j])
			i += j + 2
		case '"':
			i++
			for ; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) && strings.IndexByte("\"\\$`", s[i+1]) >= 0 {
					i++
				}
				b.WriteByte(s[i])
			}
			if i >= len(s) {
				return "", false
			}
			i++
		case '\\':
			if i+1 < len(s) {
				b.WriteByte(s[i+1])
			}
			i += 2
		case ' ', '\t':
			return "", false // more than one argument
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String(), true
}

// mcpText is what a tool call came back with, as text.
func mcpText(it codex.Item) string {
	if it.Error != nil {
		return it.Error.Message
	}
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	json.Unmarshal(it.Result, &res)
	var parts []string
	for _, c := range res.Content {
		if c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	if len(parts) == 0 && len(it.Result) > 0 && string(it.Result) != "null" {
		return string(it.Result)
	}
	return strings.Join(parts, "\n")
}

func imageFailure(it codex.Item) string {
	f := it.Failure
	if f == nil || f.Type != "usageLimitExceeded" {
		return "The image could not be made"
	}
	if f.ResetsAt != nil {
		return "The image limit is reached until " + time.Unix(*f.ResetsAt, 0).UTC().Format(time.RFC1123)
	}
	return "The image limit is reached"
}

var agentVerbs = map[string]string{"spawnAgent": "Started an agent", "sendInput": "Wrote to an agent", "resumeAgent": "Resumed an agent", "wait": "Waited for agents", "closeAgent": "Closed an agent"}

func agentVerb(tool string) string { return cmp.Or(agentVerbs[tool], tool) }

func agentStates(it codex.Item) string {
	var parts []string
	for _, st := range it.States {
		if st.Message != nil && *st.Message != "" {
			parts = append(parts, *st.Message)
		} else {
			parts = append(parts, st.Status)
		}
	}
	return strings.Join(parts, "\n\n")
}

// parseHunks reads the hunks of a unified diff, skipping any file headers.
func parseHunks(diff string) []hunk {
	var hunks []hunk
	for line := range strings.SplitSeq(diff, "\n") {
		if strings.HasPrefix(line, "@@") {
			h := hunk{}
			if m := hunkHead.FindStringSubmatch(line); m != nil {
				h.OldStart, h.OldLines = atoiOr(m[1], 0), atoiOr(m[2], 1)
				h.NewStart, h.NewLines = atoiOr(m[3], 0), atoiOr(m[4], 1)
			}
			hunks = append(hunks, h)
			continue
		}
		if len(hunks) == 0 || line == "" || line[0] == '\\' {
			continue
		}
		if c := line[0]; c == ' ' || c == '+' || c == '-' {
			hunks[len(hunks)-1].Lines = append(hunks[len(hunks)-1].Lines, line)
		}
	}
	return hunks
}

var hunkHead = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

func atoiOr(s string, or int) int {
	if s == "" {
		return or
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return or
	}
	return n
}

// reversed is the patch that undoes hunks.
func reversed(hunks []hunk) []hunk {
	out := make([]hunk, len(hunks))
	for i, h := range hunks {
		r := hunk{OldStart: h.NewStart, OldLines: h.NewLines, NewStart: h.OldStart, NewLines: h.OldLines, Lines: make([]string, len(h.Lines))}
		for j, l := range h.Lines {
			switch l[0] {
			case '+':
				l = "-" + l[1:]
			case '-':
				l = "+" + l[1:]
			}
			r.Lines[j] = l
		}
		out[i] = r
	}
	return out
}

// codexEdit is the diff a change made. Codex keeps only the hunks, so the file
// around them comes from the file as it is now, when undoing the hunks from it
// works; otherwise the hunks are all there is.
func codexEdit(root string, ch codex.Change, applied bool) (*EditDiff, error) {
	path := changePath(ch)
	ed := &EditDiff{Path: path}
	if rel, err := filepath.Rel(root, path); err == nil && filepath.IsLocal(rel) {
		ed.Path, ed.InRepo = filepath.ToSlash(rel), true
	}
	entry := gitx.FileEntry{Path: ed.Path, Status: "M"}
	switch ch.Kind.Type {
	case "add":
		entry.Status = "A"
		ed.Diff = gitx.DiffContent(entry, nil, []byte(ch.Diff))
	case "delete":
		entry.Status = "D"
		ed.Diff = gitx.DiffContent(entry, []byte(ch.Diff), nil)
	default:
		hunks := parseHunks(ch.Diff)
		if len(hunks) == 0 {
			return nil, errNoEdit
		}
		// Before the change is made, the file on disk is its old side.
		src := ch.Path
		if applied {
			src = path
		}
		if cur, err := os.ReadFile(src); err == nil {
			if applied {
				if before, ok := applyHunks(string(cur), reversed(hunks)); ok {
					ed.Diff = gitx.DiffContent(entry, []byte(before), cur)
				}
			} else if after, ok := applyHunks(string(cur), hunks); ok {
				ed.Diff = gitx.DiffContent(entry, cur, []byte(after))
			}
		}
		if ed.Diff == nil {
			ed.Diff, ed.Partial = hunksOnly(entry, hunks), true
		}
	}
	return ed, nil
}

// codexOutput is a call's whole result, for its row opened.
func codexOutput(it codex.Item) *Output {
	switch it.Type {
	case "commandExecution":
		out := ""
		if it.AggregatedOutput != nil {
			out = *it.AggregatedOutput
		}
		return &Output{Stdout: out}
	case "mcpToolCall", "dynamicToolCall":
		return &Output{Text: mcpText(it)}
	case "collabAgentToolCall":
		return &Output{Text: agentStates(it)}
	}
	return &Output{}
}

// imageFile is a picture Codex looked at or made, read from where it is.
func imageFile(path string) (*Image, error) {
	t := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if !ImageTypes[t] {
		return nil, errors.New("not an image dv shows")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return &Image{MediaType: t, Data: b}, nil
}

// inputImage is the nth picture sent with a message.
func inputImage(in []codex.Input, n int) (*Image, error) {
	for _, p := range in {
		if p.Type != "image" && p.Type != "localImage" {
			continue
		}
		if n--; n >= 0 {
			continue
		}
		if p.Type == "localImage" {
			return imageFile(p.Path)
		}
		meta, data, ok := strings.Cut(strings.TrimPrefix(p.URL, "data:"), ",")
		media, isBase64 := strings.CutSuffix(meta, ";base64")
		if !ok || !isBase64 || !ImageTypes[media] {
			return nil, errors.New("the picture is not kept with the message")
		}
		b, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return nil, err
		}
		return &Image{MediaType: media, Data: b}, nil
	}
	return nil, errors.New("no such picture in the message")
}
