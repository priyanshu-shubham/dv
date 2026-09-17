package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Thread is a Codex conversation as the app-server lists and reads it.
type Thread struct {
	ID        string  `json:"id"`
	Name      *string `json:"name"`
	Preview   string  `json:"preview"`
	Cwd       string  `json:"cwd"`
	Path      *string `json:"path"` // its rollout file, once a turn has been run
	Model     string  `json:"model"`
	Effort    *string `json:"reasoningEffort"`
	CreatedAt int64   `json:"createdAt"`
	UpdatedAt int64   `json:"updatedAt"`
	Ephemeral bool    `json:"ephemeral"`
	Parent    *string `json:"parentThreadId"`
	Status    struct {
		Type  string   `json:"type"` // notLoaded | idle | active | systemError
		Flags []string `json:"activeFlags"`
	} `json:"status"`
	GitInfo *struct {
		Branch *string `json:"branch"`
	} `json:"gitInfo"`
	Turns []Turn `json:"turns"`
}

// Turn is one exchange: a message, and everything done in answer to it.
type Turn struct {
	ID         string `json:"id"`
	Status     string `json:"status"` // inProgress | completed | interrupted | failed
	Items      []Item `json:"items"`
	Error      *Error `json:"error"`
	StartedAt  *int64 `json:"startedAt"`
	Completed  *int64 `json:"completedAt"`
	DurationMs *int64 `json:"durationMs"`
}

// Item is anything in a turn. The fields set depend on Type.
type Item struct {
	Type string `json:"type"`
	ID   string `json:"id"`

	// userMessage
	ClientID string          `json:"clientId"`
	Content  json.RawMessage `json:"content"` // input parts, or reasoning's raw text

	// agentMessage, plan
	Text  string `json:"text"`
	Phase string `json:"phase"`

	// reasoning
	Summary []string `json:"summary"`

	// commandExecution
	Command          string          `json:"command"`
	CommandActions   []CommandAction `json:"commandActions"`
	AggregatedOutput *string         `json:"aggregatedOutput"`
	ExitCode         *int            `json:"exitCode"`
	DurationMs       *int64          `json:"durationMs"`
	Status           string          `json:"status"`

	// fileChange
	Changes []Change `json:"changes"`

	// mcpToolCall, dynamicToolCall, collabAgentToolCall
	Server    string          `json:"server"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
	Result    json.RawMessage `json:"result"`
	Error     *Error          `json:"error"`
	Prompt    *string         `json:"prompt"`
	Receivers []string        `json:"receiverThreadIds"`
	States    map[string]struct {
		Status  string  `json:"status"`
		Message *string `json:"message"`
	} `json:"agentsStates"`

	// webSearch
	Query  string `json:"query"`
	Action *struct {
		Type    string   `json:"type"`
		Query   *string  `json:"query"`
		Queries []string `json:"queries"`
		URL     *string  `json:"url"`
	} `json:"action"`

	// imageView
	Path string `json:"path"`

	// imageGeneration; Result is then the picture, in base64
	SavedPath     *string `json:"savedPath"`
	RevisedPrompt *string `json:"revisedPrompt"`
	Failure       *struct {
		Type     string `json:"type"` // usageLimitExceeded
		ResetsAt *int64 `json:"resetsAt"`
	} `json:"failure"`

	// enteredReviewMode, exitedReviewMode
	Review string `json:"review"`
}

// Input is one part of a message: text, a picture, a skill.
type Input struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	URL  string `json:"url,omitempty"`
	Path string `json:"path,omitempty"`
	Name string `json:"name,omitempty"`
}

// Inputs reads a user message's parts.
func (it *Item) Inputs() []Input {
	var in []Input
	json.Unmarshal(it.Content, &in)
	return in
}

// ReasoningText is reasoning's raw text, where the model shares it.
func (it *Item) ReasoningText() []string {
	var s []string
	json.Unmarshal(it.Content, &s)
	return s
}

type CommandAction struct {
	Type    string  `json:"type"` // read | listFiles | search | unknown
	Command string  `json:"command"`
	Name    string  `json:"name"`
	Path    *string `json:"path"`
	Query   *string `json:"query"`
}

// Change is one file a fileChange item changes. Diff is the file's content for
// an add or delete, and the hunks of a unified diff for an update.
type Change struct {
	Path string `json:"path"`
	Kind struct {
		Type     string  `json:"type"` // add | delete | update
		MovePath *string `json:"move_path"`
	} `json:"kind"`
	Diff string `json:"diff"`
}

// Settings are what a thread's next turn runs with.
type Settings struct {
	Cwd            string          `json:"cwd"`
	Model          string          `json:"model"`
	Effort         *string         `json:"effort"`
	ApprovalPolicy json.RawMessage `json:"approvalPolicy"`
	Reviewer       string          `json:"approvalsReviewer"`
	Sandbox        struct {
		Type string `json:"type"` // readOnly | workspaceWrite | dangerFullAccess | externalSandbox
	} `json:"sandboxPolicy"`
	Collaboration *struct {
		Mode string `json:"mode"` // plan | default
	} `json:"collaborationMode"`
}

// Approval is the approval policy when it is one of the named ones.
func (s *Settings) Approval() string {
	var name string
	json.Unmarshal(s.ApprovalPolicy, &name)
	return name
}

// TokenUsage is how much of the context a thread's last request filled.
type TokenUsage struct {
	Last struct {
		Total int `json:"totalTokens"`
	} `json:"last"`
	Window *int `json:"modelContextWindow"`
}

// Model is one the app-server offers.
type Model struct {
	ID          string `json:"id"`
	Model       string `json:"model"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
	Hidden      bool   `json:"hidden"`
	IsDefault   bool   `json:"isDefault"`
	Default     string `json:"defaultReasoningEffort"`
	Efforts     []struct {
		Effort string `json:"reasoningEffort"`
	} `json:"supportedReasoningEfforts"`
}

// RateLimits are the account's usage windows.
type RateLimits struct {
	Primary   *RateWindow `json:"primary"`
	Secondary *RateWindow `json:"secondary"`
}

type RateWindow struct {
	UsedPercent float64 `json:"usedPercent"`
	Minutes     *int64  `json:"windowDurationMins"`
	ResetsAt    *int64  `json:"resetsAt"`
}

// Skill is one the app-server finds for a folder.
type Skill struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Short       *string `json:"shortDescription"`
	Path        string  `json:"path"`
	Enabled     bool    `json:"enabled"`
}

// Home is where Codex keeps its state: $CODEX_HOME, or ~/.codex.
func Home() string {
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex")
}

// HeldElsewhere reports whether a process other than this dv's app-server has
// the thread loaded, going by its writer lock.
func (c *Client) HeldElsewhere(thread string) bool {
	c.mu.Lock()
	let, ours := c.ours[thread]
	running := c.cmd != nil
	c.mu.Unlock()
	// The app-server unloads a thread a minute or so after it is let go.
	if ours && running && (let.IsZero() || time.Since(let) < 2*time.Minute) || strings.ContainsAny(thread, `/\`) {
		return false
	}
	return heldElsewhere(filepath.Join(Home(), "thread-writer-locks", thread+".lock"))
}

// IsActiveWriter reports an error resuming a thread something else has loaded.
func IsActiveWriter(err error) bool {
	return err != nil && strings.Contains(err.Error(), "active writer")
}

// IsNoRollout reports an error resuming a thread that was never sent to.
func IsNoRollout(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no rollout")
}
