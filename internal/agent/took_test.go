package agent

import (
	"strconv"
	"strings"
	"testing"

	"dv/internal/codex"
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

	ms := int64(4200)
	turn := codex.Turn{ID: "t", Status: "completed", DurationMs: &ms, Items: []codex.Item{{Type: "agentMessage", ID: "a", Text: "ok"}}}
	if items := codexItems("/repo", []codex.Turn{turn}, nil, nil); items[len(items)-1].Kind != "worked" || items[len(items)-1].Took != 4200 {
		t.Errorf("Codex turn: %+v", items)
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
