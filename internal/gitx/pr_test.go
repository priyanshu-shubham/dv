package gitx

import "testing"

func TestParsePR(t *testing.T) {
	for name, c := range map[string]struct {
		json string
		want PR
	}{
		"draft, one pending": {
			`{"number":7,"title":"t","url":"u","state":"OPEN","isDraft":true,"reviewDecision":"",
			  "statusCheckRollup":[{"status":"COMPLETED","conclusion":"SUCCESS"},{"status":"IN_PROGRESS","conclusion":""}]}`,
			PR{Number: 7, Title: "t", URL: "u", State: "draft", Checks: "pending"},
		},
		"a failure outweighs what follows": {
			`{"number":8,"state":"OPEN","reviewDecision":"APPROVED",
			  "statusCheckRollup":[{"status":"COMPLETED","conclusion":"FAILURE"},{"status":"QUEUED"},{"state":"SUCCESS"}]}`,
			PR{Number: 8, State: "open", Checks: "failing", Review: "approved"},
		},
		"merged, skipped and commit statuses pass": {
			`{"number":9,"state":"MERGED","statusCheckRollup":[{"status":"COMPLETED","conclusion":"SKIPPED"},{"state":"SUCCESS"}]}`,
			PR{Number: 9, State: "merged", Checks: "passing"},
		},
		"no checks": {`{"number":10,"state":"CLOSED","reviewDecision":"CHANGES_REQUESTED"}`, PR{Number: 10, State: "closed", Review: "changes"}},
	} {
		got := parsePR([]byte(c.json))
		if got == nil || *got != c.want {
			t.Errorf("%s: %+v, want %+v", name, got, c.want)
		}
	}
	if parsePR([]byte(`{}`)) != nil {
		t.Error("a PR with no number")
	}
}
