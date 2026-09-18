package permit

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Option is one answer to a request, as the terminal offers it: yes, each
// suggestion it comes with, then no. The page and the phone offer the same.
type Option struct {
	Label      string `json:"label"`
	Allow      bool   `json:"allow"`
	Suggestion *int   `json:"suggestion,omitempty"`
	Mode       string `json:"mode,omitempty"`  // the mode a yes switches to
	Where      string `json:"where,omitempty"` // where the rule it adds is kept
	Title      string `json:"title,omitempty"` // the rules in full, where the label cuts them short
}

// what phrases a request: the verb, and what it acts on. path marks a target
// that is a file, many a Codex patch changing several.
func (r *Request) what() (verb, target string, path, many bool) {
	var in struct {
		FilePath     string   `json:"file_path"`
		NotebookPath string   `json:"notebook_path"`
		Command      string   `json:"command"`
		URL          string   `json:"url"`
		Query        string   `json:"query"`
		PlanFilePath string   `json:"planFilePath"`
		Files        []string `json:"files"`
	}
	json.Unmarshal(r.Input, &in)
	if len(in.Files) > 1 {
		return "edit", fmt.Sprintf("%d files", len(in.Files)), false, true
	}
	var p *Preview
	if len(r.Previews) > 0 {
		p = r.Previews[0]
	}
	switch r.Tool {
	case "Edit":
		if p != nil {
			return "edit", p.Path, true, false
		}
		return "edit", in.FilePath, true, false
	case "Write":
		if p != nil && p.Diff != nil && p.Diff.Status == "A" {
			return "create", p.Path, true, false
		}
		if p != nil {
			return "overwrite", p.Path, true, false
		}
		return "overwrite", in.FilePath, true, false
	case "NotebookEdit":
		return "edit a notebook", in.NotebookPath, true, false
	case "Bash", "PowerShell":
		first, _, _ := strings.Cut(in.Command, "\n")
		return "run", first, false, false
	case "WebFetch":
		return "fetch", in.URL, false, false
	case "WebSearch":
		return "search the web for", in.Query, false, false
	case "ExitPlanMode":
		return "start on this plan", in.PlanFilePath, true, false
	case "AskUserQuestion":
		return "ask you", "", false, false
	}
	if m := mcpTool.FindStringSubmatch(r.Tool); m != nil {
		return "use", m[1] + ": " + m[2], false, false
	}
	return "use", r.Tool, false, false
}

var mcpTool = regexp.MustCompile(`^mcp__(.+?)__(.+)$`)

func (r *Request) headline() string {
	verb, target, path, _ := r.what()
	switch {
	case r.Tool == "Bash" || r.Tool == "PowerShell":
		return "run a command"
	case target == "" || r.Tool == "ExitPlanMode":
		return verb
	case path:
		return verb + " " + target[strings.LastIndexByte(target, '/')+1:]
	}
	return verb + " " + target
}

func (r *Request) options() []Option {
	plan := r.Tool == "ExitPlanMode"
	var offers []Option
	for i, raw := range r.Suggestions {
		o := offer(raw, plan)
		if o.Label == "" {
			continue
		}
		o.Allow, o.Suggestion = true, &i
		offers = append(offers, o)
	}
	var out []Option
	// A plan's yes always says how edits go from there.
	if !plan || len(offers) == 0 {
		out = append(out, Option{Label: "Yes", Allow: true})
	}
	return append(append(out, offers...), Option{Label: "No"})
}

var kept = map[string]string{
	"session":         "this session",
	"localSettings":   ".claude/settings.local.json",
	"projectSettings": ".claude/settings.json",
	"userSettings":    "~/.claude/settings.json",
	"codexRules":      "Codex's rules",
}

// offer is the option the terminal shows for one of Claude Code's
// suggestions; one dv cannot put into words has no label, and is left out.
func offer(raw json.RawMessage, plan bool) Option {
	var s struct {
		Type        string `json:"type"`
		Mode        string `json:"mode"`
		Destination string `json:"destination"`
		Behavior    string `json:"behavior"`
		Rules       []struct {
			ToolName    string `json:"toolName"`
			RuleContent string `json:"ruleContent"`
		} `json:"rules"`
		Directories []string `json:"directories"`
	}
	json.Unmarshal(raw, &s)
	where := kept[s.Destination]
	switch {
	case s.Type == "setMode" && plan:
		label := "Yes, in " + s.Mode + " mode"
		switch s.Mode {
		case "acceptEdits":
			label = "Yes, and accept edits"
		case "default":
			label = "Yes, and ask before edits"
		}
		return Option{Label: label, Mode: s.Mode}
	case s.Type == "setMode":
		label := "Yes, and switch to " + s.Mode + " mode"
		if s.Mode == "acceptEdits" {
			label = "Yes, allow all edits this session"
		}
		return Option{Label: label, Mode: s.Mode, Where: where}
	case s.Type == "addRules" && s.Behavior == "allow" && len(s.Rules) > 0:
		rules, short := make([]string, len(s.Rules)), make([]string, len(s.Rules))
		for i, r := range s.Rules {
			switch {
			case r.ToolName == "Bash" && r.RuleContent != "":
				rules[i] = r.RuleContent
			case r.RuleContent != "":
				rules[i] = r.ToolName + "(" + r.RuleContent + ")"
			default:
				rules[i] = r.ToolName
			}
			short[i] = shortRule(rules[i])
		}
		return Option{Label: "Yes, and don't ask again for " + strings.Join(short, ", "), Where: where, Title: strings.Join(rules, "\n")}
	case s.Type == "addDirectories" && len(s.Directories) > 0:
		return Option{Label: "Yes, and always allow access to " + strings.Join(s.Directories, ", "), Where: where}
	}
	return Option{}
}

// ruleChars keeps an answer on one line: a rule can be a whole script, and the
// answer says what it allows, not the script. The title keeps all of it.
const ruleChars = 56

func shortRule(rule string) string {
	first, _, more := strings.Cut(rule, "\n")
	line := strings.TrimSpace(first)
	if r := []rune(line); len(r) > ruleChars {
		return strings.TrimRight(string(r[:ruleChars-1]), " \t") + "…"
	}
	if more {
		return line + " …"
	}
	return line
}
