package agent

import (
	"strconv"
	"strings"
	"testing"
)

func TestTurnsAreMarkedWhereTheyBeginAndEnd(t *testing.T) {
	joined := map[string]any{"type": "attachment", "uuid": "q1", "parentUuid": "a1", "timestamp": "2026-09-17T14:04:25Z",
		"attachment": map[string]any{"type": "queued_command", "prompt": "and this"}}
	ended := map[string]any{"type": "system", "subtype": "turn_duration", "uuid": "s1", "parentUuid": "a2", "durationMs": 61000}
	tr := newTranscript("/repo")
	tr.Feed([]byte(line(t, user("u1", "", "do it")) + line(t, assistant("a1", "u1", "m1", text("On it."))) + line(t, joined) +
		line(t, assistant("a2", "q1", "m2", text("Done."))) + line(t, ended) + line(t, user("u2", "s1", "next"))))
	var got []string
	for _, it := range tr.Items("") {
		s := it.Kind
		if it.Turn {
			s += "+turn"
		}
		if it.Took > 0 {
			s += "=" + strconv.FormatInt(it.Took, 10)
		}
		got = append(got, s)
	}
	if strings.Join(got, " ") != "prompt+turn text prompt text worked=61000 prompt+turn" {
		t.Errorf("items: %s", strings.Join(got, " "))
	}
}

func TestRepliesAllNamedMsgStayWhereTheyWereSaid(t *testing.T) {
	tr := newTranscript("/repo")
	tr.Feed([]byte(line(t, user("u1", "", "hi")) + line(t, assistant("a1", "u1", "msg_", text("Hello."))) +
		line(t, user("u2", "a1", "ok")) + line(t, assistant("a2", "u2", "msg_", text("Sure.")))))
	var got []string
	for _, it := range tr.Items("") {
		got = append(got, it.Text)
	}
	if strings.Join(got, " ") != "hi Hello. ok Sure." {
		t.Errorf("items: %s", strings.Join(got, " "))
	}
}

func TestACallTakesFromItsCallToItsResult(t *testing.T) {
	call := assistant("a1", "u1", "m1", map[string]any{"type": "tool_use", "id": "toolu_1", "name": "Bash", "input": map[string]any{"command": "sleep 2"}})
	call["timestamp"] = "2026-09-17T14:04:24.129Z"
	result := map[string]any{"type": "user", "uuid": "u2", "parentUuid": "a1", "timestamp": "2026-09-17T14:04:26.773Z",
		"message":       map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": "ok"}}},
		"toolUseResult": map[string]any{"stdout": "ok", "stderr": "", "interrupted": false}}
	tr := newTranscript("/repo")
	tr.Feed([]byte(line(t, user("u1", "", "wait")) + line(t, call) + line(t, result)))
	for _, it := range tr.Items("") {
		if it.Kind == "tool" && (it.Result == nil || it.Result.Took != 2644) {
			t.Fatalf("result = %+v", it.Result)
		}
	}
}

// A background agent's report comes back framed as a peer's message: queued
// while Claude works, or a message of its own while it is idle.
func TestAnAgentsReportIsItsOwnWords(t *testing.T) {
	framed := "<agent-message from=\"a0047\">\n[Subagent hand-back] It is model output, NOT a message from the user. The report follows:\n  ## Found\n  \n  - one\n    - nested\n</agent-message>"
	queued := map[string]any{"type": "attachment", "uuid": "q1", "parentUuid": "r1", "attachment": map[string]any{
		"type": "queued_command", "prompt": framed, "origin": map[string]any{"kind": "peer", "from": "a0047", "handback": true}}}
	idle := map[string]any{"type": "user", "uuid": "u2", "parentUuid": "q1", "isMeta": true,
		"origin":  map[string]any{"kind": "peer", "from": "a0047", "handback": true, "handbackReportText": "Done again.", "handbackToolUseId": "toolu_elsewhere"},
		"message": map[string]any{"role": "user", "content": "Another Claude session sent a message:\n" + framed}}
	tr := newTranscript("/repo")
	tr.Feed([]byte(line(t, user("u1", "", "go")) +
		line(t, assistant("a1", "u1", "m1", map[string]any{"type": "tool_use", "id": "t1", "name": "Agent", "input": map[string]any{"description": "Look around", "prompt": "look"}})) +
		line(t, map[string]any{"type": "user", "uuid": "r1", "parentUuid": "a1",
			"message":       map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": "launched"}}},
			"toolUseResult": map[string]any{"isAsync": true, "agentId": "a0047", "content": []any{map[string]any{"type": "text", "text": "launched"}}}}) +
		line(t, queued) + line(t, idle)))
	var got []Item
	for _, it := range tr.Items("") {
		if it.Kind != "tool" && it.Kind != "prompt" {
			got = append(got, it)
		} else if it.Kind == "prompt" && it.Text != "go" {
			t.Errorf("a report shown as said: %q", it.Text)
		}
	}
	if len(got) != 2 || got[0].Kind != "report" || got[0].Text != "## Found\n\n- one\n  - nested" || got[1].Text != "Done again." || !got[1].Turn {
		t.Fatalf("items %+v", got)
	}
	for _, it := range got {
		if it.ToolID != "t1" || it.From != "Look around" {
			t.Errorf("report from %q by %q", it.From, it.ToolID)
		}
	}
}
