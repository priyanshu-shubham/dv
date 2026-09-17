package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dv/internal/codex"
	"dv/internal/permit"
)

// codexTurn decodes a turn written the way the app-server sends it.
func codexTurn(t *testing.T, raw string) codex.Turn {
	t.Helper()
	var turn codex.Turn
	if err := json.Unmarshal([]byte(raw), &turn); err != nil {
		t.Fatal(err)
	}
	return turn
}

func TestCodexItems(t *testing.T) {
	first := codexTurn(t, `{"id":"turn1","status":"completed","startedAt":1789655837,"items":[
		{"type":"userMessage","id":"m1","clientId":"c1","content":[{"type":"text","text":"change poem.txt"},{"type":"image","url":"data:image/png;base64,iVBORw0KGgo="}]},
		{"type":"reasoning","id":"r1","summary":["Looking at the file"],"content":[]},
		{"type":"commandExecution","id":"x1","command":"/usr/bin/zsh -lc 'rg --files | head'","commandActions":[{"type":"unknown","command":"rg --files | head"}],"aggregatedOutput":"a.txt\npoem.txt\n","exitCode":0,"status":"completed"},
		{"type":"fileChange","id":"f1","status":"completed","changes":[
			{"path":"/repo/poem.txt","kind":{"type":"update","move_path":null},"diff":"@@ -4,3 +4,3 @@\n four\n-five\n+FIVE\n six\n"},
			{"path":"/repo/new.txt","kind":{"type":"add"},"diff":"hi\nthere\n"}]},
		{"type":"agentMessage","id":"a1","text":"Done.","phase":"final_answer"}]}`)
	second := codexTurn(t, `{"id":"turn2","status":"interrupted","items":[
		{"type":"userMessage","id":"m2","clientId":null,"content":[{"type":"skill","name":"review","path":"/skills/review"},{"type":"text","text":"the diff"}]},
		{"type":"userMessage","id":"m3","content":[{"type":"text","text":"steered in"}]},
		{"type":"commandExecution","id":"x2","command":"sleep 30","status":"inProgress"}]}`)
	todos := map[string]*todoList{"turn1": {after: "r1", steps: []map[string]string{{"content": "read", "status": "completed"}}}}
	items := codexItems("/repo", []codex.Turn{first, second}, todos, nil, nil)

	var got []string
	for _, it := range items {
		got = append(got, it.Kind+":"+it.Key)
	}
	want := "prompt:m1 thinking:r1 tool:todos:turn1 tool:x1 tool:f1:0 tool:f1:1 text:a1 command:m2 prompt:m3 tool:x2 note:turn2:interrupted"
	if strings.Join(got, " ") != want {
		t.Fatalf("items:\n got %s\nwant %s", strings.Join(got, " "), want)
	}
	byKey := map[string]Item{}
	for _, it := range items {
		byKey[it.Key] = it
	}
	if p := byKey["m1"]; p.UUID != "c1" || p.Before != "" || p.Images != 1 || p.At == "" {
		t.Errorf("first prompt = %+v", p)
	}
	if p := byKey["m2"]; p.Text != "/review the diff" || p.UUID != "m2" || p.Before != "turn1" {
		t.Errorf("skill prompt = %+v", p)
	}
	if p := byKey["m3"]; p.UUID != "" {
		t.Errorf("a steered message cannot be rewound to, got uuid %q", p.UUID)
	}
	if x := byKey["x1"]; x.Tool != "Bash" || !strings.Contains(string(x.Input), `"rg --files | head"`) || x.Result == nil || x.Result.IsError || x.Result.Detail.Lines != 2 {
		t.Errorf("command = %+v %s", x, x.Input)
	}
	if x := byKey["x2"]; x.Result != nil {
		t.Errorf("a running command has no result yet: %+v", x.Result)
	}
	if e := byKey["f1:0"].Result.Edited; e == nil || e.Path != "poem.txt" || !e.InRepo || e.Adds != 1 || e.Dels != 1 || e.Created {
		t.Errorf("update = %+v", e)
	}
	if e := byKey["f1:1"]; e.Tool != "Write" || e.Result.Edited == nil || !e.Result.Edited.Created || e.Result.Edited.Adds != 2 {
		t.Errorf("add = %+v", e)
	}
}

func TestAReviewShowsOnlyItsFindings(t *testing.T) {
	kinds := func(turns []codex.Turn) string {
		var got []string
		for _, it := range codexItems("/repo", turns, nil, nil, nil) {
			got = append(got, it.Kind+":"+it.Key)
		}
		return strings.Join(got, " ")
	}
	// As it arrives, the reviewer's work comes inside the review's turn.
	live := codexTurn(t, `{"id":"01a0-2","status":"completed","items":[
		{"type":"enteredReviewMode","id":"e1","review":"current changes"},
		{"type":"userMessage","id":"m1","content":[{"type":"text","text":"Review the current code changes"}]},
		{"type":"commandExecution","id":"x1","command":"git diff","aggregatedOutput":"","exitCode":0,"status":"completed"},
		{"type":"agentMessage","id":"j1","text":"{\"findings\": []}"},
		{"type":"exitedReviewMode","id":"r1","review":"Looks correct."},
		{"type":"agentMessage","id":"a1","text":"Looks correct.\n"}]}`)
	if got, want := kinds([]codex.Turn{live}), "note:e1 tool:x1 text:r1"; got != want {
		t.Errorf("live:\n got %s\nwant %s", got, want)
	}

	// Read back, it is a turn of its own, before the review but made after it.
	before := codexTurn(t, `{"id":"01a0-1","status":"completed","items":[{"type":"agentMessage","id":"a0","text":"Hi."}]}`)
	reviewer := codexTurn(t, `{"id":"01a0-3","status":"interrupted","items":[
		{"type":"userMessage","id":"m1","content":[{"type":"text","text":"Review the current code changes"}]},
		{"type":"commandExecution","id":"x1","command":"git diff","aggregatedOutput":"","exitCode":0,"status":"completed"}]}`)
	review := codexTurn(t, `{"id":"01a0-2","status":"completed","items":[
		{"type":"enteredReviewMode","id":"e1","review":"current changes"},
		{"type":"exitedReviewMode","id":"r1","review":"Looks correct."},
		{"type":"agentMessage","id":"a1","text":"Looks correct."}]}`)
	if got, want := kinds(foldReviewers([]codex.Turn{before, reviewer, review})), "text:a0 note:e1 tool:x1 text:r1"; got != want {
		t.Errorf("read back:\n got %s\nwant %s", got, want)
	}
}

func TestCodexEdit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "poem.txt")
	after := "one\ntwo\nthree\nfour\nFIVE\nsix\nseven\neight\nNINE\nten\n"
	os.WriteFile(path, []byte(after), 0o644)
	ch := codex.Change{Path: path, Diff: "@@ -4,3 +4,3 @@\n four\n-five\n+FIVE\n six\n@@ -8,3 +8,3 @@\n eight\n-nine\n+NINE\n ten\n"}
	ch.Kind.Type = "update"

	ed, err := codexEdit(dir, ch, true)
	if err != nil {
		t.Fatal(err)
	}
	if ed.Partial || ed.Path != "poem.txt" || ed.Diff.Additions != 2 || ed.Diff.Deletions != 2 || len(ed.Diff.OldLines) < 10 {
		t.Fatalf("applied edit = partial %v, %+v", ed.Partial, ed.Diff)
	}
	if ed.Diff.OldLines[4] != "five" || ed.Diff.NewLines[4] != "FIVE" {
		t.Errorf("sides: %q / %q", ed.Diff.OldLines[4], ed.Diff.NewLines[4])
	}

	// Before it is applied, the file on disk is the old side.
	os.WriteFile(path, []byte(strings.NewReplacer("FIVE", "five", "NINE", "nine").Replace(after)), 0o644)
	ed, err = codexEdit(dir, ch, false)
	if err != nil || ed.Partial || ed.Diff.NewLines[8] != "NINE" {
		t.Fatalf("preview = %v %+v", err, ed)
	}

	// Changed again since, the hunks are all there is.
	os.WriteFile(path, []byte("rewritten\n"), 0o644)
	ed, err = codexEdit(dir, ch, true)
	if err != nil || !ed.Partial || ed.Diff.Additions != 2 {
		t.Fatalf("partial = %v %+v", err, ed)
	}
}

func TestCommandText(t *testing.T) {
	for cmd, want := range map[string]string{
		`/usr/bin/zsh -lc 'touch made-by-shell.txt'`:                   "touch made-by-shell.txt",
		`/bin/bash -lc "printf 'ok' > notes.txt"`:                      "printf 'ok' > notes.txt",
		`/usr/bin/zsh -lc 'echo '"'"'quoted'"'"' && ls'`:               "echo 'quoted' && ls",
		`/usr/bin/zsh -lc "pwd && rg --files -g '"'!.*'"' | head -80"`: "pwd && rg --files -g '!.*' | head -80",
		`sleep 30`: "sleep 30",
	} {
		if got := commandText(codex.Item{Command: cmd}); got != want {
			t.Errorf("commandText(%s) = %q, want %q", cmd, got, want)
		}
	}
}

func TestMergeTurns(t *testing.T) {
	turn := func(id, status string) codex.Turn { return codex.Turn{ID: id, Status: status} }
	ids := func(ts []codex.Turn) string {
		var s []string
		for _, t := range ts {
			s = append(s, t.ID+"="+t.Status)
		}
		return strings.Join(s, " ")
	}
	held := []codex.Turn{turn("t2", "completed"), turn("t3", "inProgress")}
	// The running turn keeps what its notifications said; older turns read in go first.
	got := mergeTurns([]codex.Turn{turn("t1", "completed"), turn("t2", "completed"), turn("t3", "interrupted")}, held, "t3")
	if ids(got) != "t1=completed t2=completed t3=inProgress" {
		t.Errorf("merged %s", ids(got))
	}
	// Read after the turn ended, the rollout's word stands, and a new turn goes last.
	got = mergeTurns([]codex.Turn{turn("t3", "completed"), turn("t4", "completed")}, held, "")
	if ids(got) != "t2=completed t3=completed t4=completed" {
		t.Errorf("merged %s", ids(got))
	}
}

func TestCodexModes(t *testing.T) {
	for _, mode := range []string{"default", "acceptEdits", "plan", "auto"} {
		approval, sandbox, reviewer := codexPolicy(mode)
		s := &codex.Settings{ApprovalPolicy: json.RawMessage(`"` + approval + `"`), Reviewer: reviewer}
		s.Sandbox.Type = sandbox["type"].(string)
		if mode == "plan" {
			s.Collaboration = &struct {
				Mode string `json:"mode"`
			}{"plan"}
		}
		if got := modeOf(s); got != mode {
			t.Errorf("%s reads back as %s", mode, got)
		}
	}
}

func TestCodexImageGeneration(t *testing.T) {
	dir := t.TempDir()
	png := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP4z8DwHwAFBQIBpbKktgAAAABJRU5ErkJggg=="
	turn := codexTurn(t, `{"id":"t","status":"completed","items":[
		{"type":"imageGeneration","id":"g1","status":"generating","result":"","revisedPrompt":"a red square"},
		{"type":"imageGeneration","id":"g2","status":"completed","result":"`+png+`","revisedPrompt":"a red square","savedPath":"`+filepath.Join(dir, "gone.png")+`"},
		{"type":"imageGeneration","id":"g3","status":"failed","result":"","failure":{"type":"usageLimitExceeded","limitId":"images","resetsAt":null}}]}`)
	items := codexItems(dir, []codex.Turn{turn}, nil, nil, nil)
	if len(items) != 3 || items[0].Tool != "ImageGeneration" || items[0].Result != nil {
		t.Fatalf("still making it: %+v", items)
	}
	if r := items[1].Result; r == nil || r.IsError || r.Detail == nil || r.Detail.Kind != "image" || !strings.Contains(string(items[1].Input), "a red square") {
		t.Errorf("made: %+v %s", r, items[1].Input)
	}
	if r := items[2].Result; r == nil || !r.IsError || r.Text != "The image limit is reached" {
		t.Errorf("failed: %+v", r)
	}

	// Saved where it no longer is, the picture comes from the thread itself.
	side := &codexSide{root: dir, threads: map[string]*codexThread{}}
	th := newCodexThread(side, "thread")
	th.history, th.whole, th.turns = true, true, []codex.Turn{turn}
	side.threads["thread"] = th
	img, err := side.image("thread", "g2")
	if err != nil || img.MediaType != "image/png" || len(img.Data) == 0 {
		t.Fatalf("image = %v %+v", err, img)
	}
}

func TestAPatchIsAskedAboutFileByFile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "poem.txt"), []byte("one\ntwo\n"), 0o644)
	turn := codexTurn(t, `{"id":"turn","status":"inProgress","items":[{"type":"fileChange","id":"f","status":"inProgress","changes":[
		{"path":"`+filepath.Join(dir, "poem.txt")+`","kind":{"type":"update"},"diff":"@@ -1,2 +1,2 @@\n one\n-two\n+TWO\n"},
		{"path":"`+filepath.Join(dir, "new.txt")+`","kind":{"type":"add"},"diff":"hi\n"}]}]}`)
	side := &codexSide{root: dir, broker: permit.New(dir), threads: map[string]*codexThread{}}
	th := newCodexThread(side, "thread")
	th.turns = []codex.Turn{turn}

	decided := make(chan any, 1)
	go func() {
		decided <- th.approveChange(context.Background(), json.RawMessage(`{"turnId":"turn","itemId":"f"}`))
	}()
	var req *permit.Request
	for req == nil {
		if w := side.broker.Waiting(); len(w) > 0 {
			req = w[0]
		}
		time.Sleep(time.Millisecond)
	}
	if len(req.Previews) != 2 || req.Previews[0].Path != "poem.txt" || req.Previews[0].Diff.NewLines[1] != "TWO" || req.Previews[1].Diff.Status != "A" {
		t.Fatalf("previews: %+v", req.Previews)
	}
	if string(req.Input) != `{"files":["`+filepath.Join(dir, "poem.txt")+`","`+filepath.Join(dir, "new.txt")+`"]}` {
		t.Errorf("input: %s", req.Input)
	}
	if len(req.Suggestions) != 1 || !strings.Contains(string(req.Suggestions[0]), `"rules":[{"ruleContent":"poem.txt","toolName":"Edit"},{"ruleContent":"new.txt","toolName":"Edit"}]`) {
		t.Errorf("a yes for the session is for these files: %s", req.Suggestions)
	}
	side.broker.Answer(req.ID, permit.Answer{Allow: true})
	if d := <-decided; d.(map[string]any)["decision"] != "accept" {
		t.Errorf("decision: %v", d)
	}
}

func TestInputImage(t *testing.T) {
	in := []codex.Input{{Type: "text", Text: "look"}, {Type: "image", URL: "data:image/png;base64,iVBORw0KGgo="}}
	img, err := inputImage(in, 0)
	if err != nil || img.MediaType != "image/png" || len(img.Data) != 8 {
		t.Fatalf("image = %v %+v", err, img)
	}
	if _, err := inputImage(in, 1); err == nil {
		t.Error("there is no second picture")
	}
}
