package agent

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"dv/internal/gitx"
)

// hunk is one piece of Claude Code's structuredPatch, unified-diff shaped.
type hunk struct {
	OldStart int      `json:"oldStart"`
	OldLines int      `json:"oldLines"`
	NewStart int      `json:"newStart"`
	NewLines int      `json:"newLines"`
	Lines    []string `json:"lines"`
}

// EditDiff is the change one tool call made to a file.
type EditDiff struct {
	Path   string         `json:"path"`
	InRepo bool           `json:"inRepo"`
	Diff   *gitx.FileDiff `json:"diff"`
	// Partial means the transcript kept only the changed hunks, so the lines
	// between them are not known and cannot be expanded.
	Partial bool `json:"partial,omitempty"`
}

var errNoEdit = errors.New("the transcript has no change recorded for this call")

// findEdit reads a transcript for the result of tool call id and builds the
// diff it made. The whole file before the edit is usually recorded with it;
// when it is not, the hunks are all there is.
func findEdit(path, root, id string) (*EditDiff, error) {
	line, err := resultLine(path, id)
	return editOf(line, err, root)
}

// editOf is the diff recorded on a result's line, as resultLine found it.
func editOf(line []byte, err error, root string) (*EditDiff, error) {
	if errors.Is(err, errNoResult) {
		return nil, errNoEdit
	} else if err != nil {
		return nil, err
	}
	var r struct {
		Result json.RawMessage `json:"toolUseResult"`
	}
	if json.Unmarshal(line, &r) != nil {
		return nil, errNoEdit
	}
	return editFrom(r.Result, root)
}

func editFrom(raw json.RawMessage, root string) (*EditDiff, error) {
	var r struct {
		Type       string  `json:"type"` // Write's create | update
		FilePath   string  `json:"filePath"`
		OldString  string  `json:"oldString"`
		NewString  string  `json:"newString"`
		ReplaceAll bool    `json:"replaceAll"`
		Content    *string `json:"content"`
		Original   *string `json:"originalFile"`
		Patch      []hunk  `json:"structuredPatch"`
	}
	if err := json.Unmarshal(raw, &r); err != nil || r.FilePath == "" {
		return nil, errNoEdit
	}
	ed := &EditDiff{Path: r.FilePath}
	if rel, err := filepath.Rel(root, r.FilePath); err == nil && filepath.IsLocal(rel) {
		ed.Path, ed.InRepo = filepath.ToSlash(rel), true
	}
	entry := gitx.FileEntry{Path: ed.Path, Status: "M"}

	switch {
	case r.Type == "create" && r.Content != nil:
		entry.Status = "A"
		ed.Diff = gitx.DiffContent(entry, nil, []byte(*r.Content))
	case r.Original != nil && r.Content != nil:
		ed.Diff = gitx.DiffContent(entry, []byte(*r.Original), []byte(*r.Content))
	case r.Original != nil:
		after, ok := applyHunks(*r.Original, r.Patch)
		if !ok && r.OldString != "" && strings.Contains(*r.Original, r.OldString) {
			n := 1
			if r.ReplaceAll {
				n = -1
			}
			after, ok = strings.Replace(*r.Original, r.OldString, r.NewString, n), true
		}
		if ok {
			ed.Diff = gitx.DiffContent(entry, []byte(*r.Original), []byte(after))
		}
	}
	if ed.Diff == nil && len(r.Patch) > 0 {
		ed.Diff, ed.Partial = hunksOnly(entry, r.Patch), true
	}
	if ed.Diff == nil {
		return nil, errNoEdit
	}
	return ed, nil
}

// applyHunks replays a patch onto the file it was made against, or reports
// that the file does not match it.
func applyHunks(before string, hunks []hunk) (string, bool) {
	old := strings.Split(before, "\n")
	var out []string
	at := 0
	for _, h := range hunks {
		start := h.OldStart - 1
		if h.OldLines == 0 {
			start = h.OldStart // an insertion names the line it follows
		}
		if start < at || start > len(old) {
			return "", false
		}
		out = append(out, old[at:start]...)
		at = start
		for _, l := range h.Lines {
			if l == "" {
				continue
			}
			switch l[0] {
			case ' ', '-':
				if at >= len(old) || old[at] != l[1:] {
					return "", false
				}
				if l[0] == ' ' {
					out = append(out, old[at])
				}
				at++
			case '+':
				out = append(out, l[1:])
			}
		}
	}
	return strings.Join(append(out, old[at:]...), "\n"), true
}

// hunksOnly lays the hunks out at their own line numbers, with the lines
// between them left blank: enough for the rows, the numbers and comments.
func hunksOnly(entry gitx.FileEntry, hunks []hunk) *gitx.FileDiff {
	fd := &gitx.FileDiff{FileEntry: entry, Lang: gitx.LangFor(entry.Path), OldLines: []string{}, NewLines: []string{}}
	add := func(kind gitx.OpKind, ol, nl int) {
		if n := len(fd.Ops); n > 0 && fd.Ops[n-1].Kind == kind {
			fd.Ops[n-1].OldLen += ol
			fd.Ops[n-1].NewLen += nl
		} else {
			fd.Ops = append(fd.Ops, gitx.Op{Kind: kind, OldStart: len(fd.OldLines), OldLen: ol, NewStart: len(fd.NewLines), NewLen: nl})
		}
	}
	for _, h := range hunks {
		// Both sides skip the same unchanged lines to reach a hunk; if the
		// numbers disagree the rows still show, a little misnumbered.
		gap := max(0, min(h.OldStart-1-len(fd.OldLines), h.NewStart-1-len(fd.NewLines)))
		if gap > 0 {
			add(gitx.OpEqual, gap, gap)
			blank := make([]string, gap)
			fd.OldLines = append(fd.OldLines, blank...)
			fd.NewLines = append(fd.NewLines, blank...)
		}
		for _, l := range h.Lines {
			if l == "" {
				continue
			}
			switch l[0] {
			case ' ':
				add(gitx.OpEqual, 1, 1)
				fd.OldLines = append(fd.OldLines, l[1:])
				fd.NewLines = append(fd.NewLines, l[1:])
			case '-':
				add(gitx.OpDelete, 1, 0)
				fd.OldLines = append(fd.OldLines, l[1:])
				fd.Deletions++
			case '+':
				add(gitx.OpInsert, 0, 1)
				fd.NewLines = append(fd.NewLines, l[1:])
				fd.Additions++
			}
		}
	}
	return fd
}
