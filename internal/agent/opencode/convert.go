//go:build !windows

package opencode

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// The content shapes this adapter writes (AGENT-HOST.md §5, OpenCode).

// ToolContent is a tool part's content. The part's text is the tool's
// output (streamed while a shell command runs), or its error.
type ToolContent struct {
	Tool       string          `json:"tool"`
	CallID     string          `json:"call_id,omitempty"`
	Title      string          `json:"title,omitempty"`
	Input      json.RawMessage `json:"input,omitempty"`
	Error      string          `json:"error,omitempty"`
	StartedAt  int64           `json:"started_at,omitempty"`
	EndedAt    int64           `json:"ended_at,omitempty"`
	DurationMS int64           `json:"duration_ms,omitempty"`
	// Exit is a shell command's exit code.
	Exit *int `json:"exit,omitempty"`
	// SessionID and Agent are a subagent's (the task tool): its parts are
	// the parts whose parent is this one.
	SessionID string `json:"session_id,omitempty"`
	Agent     string `json:"agent,omitempty"`
}

// DiffContent is a diff part's content: what one edit tool call changed.
type DiffContent struct {
	ToolPart string     `json:"tool_part"`
	Files    []DiffFile `json:"files"`
}

// DiffFile is one file of a diff, as a unified patch.
type DiffFile struct {
	Path      string `json:"path"`
	Patch     string `json:"patch"`
	Additions int    `json:"additions,omitempty"`
	Deletions int    `json:"deletions,omitempty"`
	Status    string `json:"status,omitempty"`
}

// TodoContent is a todo part's content: the whole list as todowrite set it.
// The newest todo part is the list as it stands.
type TodoContent struct {
	Todos []Todo `json:"todos"`
}

// Todo is one item of the list.
type Todo struct {
	Content  string `json:"content"`
	Status   string `json:"status"`
	Priority string `json:"priority,omitempty"`
}

// FileContent is an image or file part's content.
type FileContent struct {
	Mime     string `json:"mime,omitempty"`
	Filename string `json:"filename,omitempty"`
	// URL is a file:// URL; a data URL is not repeated into the journal.
	URL string `json:"url,omitempty"`
}

// converted is one OpenCode part as the parts it becomes.
type converted struct {
	kind    string
	state   string
	text    string
	content json.RawMessage
	// diff is the diff part an edit tool also becomes, nil for others.
	diff *converted
	// child is the subagent session a task tool runs.
	child string
}

// diffSuffix makes a tool part's diff part id.
const diffSuffix = "-diff"

// convertPart maps one part of an assistant message. ok is false for parts
// that are not shown (step boundaries, snapshots, patches, retries).
func convertPart(p ocPart) (converted, bool) {
	switch p.Type {
	case partText, partReasoning:
		if p.Type == partText && (p.Synthetic || p.Ignored) {
			return converted{}, false
		}
		kind, state := agent.PartText, agent.PartStreaming
		if p.Type == partReasoning {
			kind = agent.PartReasoning
		}
		if p.Time != nil && p.Time.End > 0 {
			state = agent.PartDone
		}
		return converted{kind: kind, state: state, text: p.Text}, true
	case partTool:
		return convertTool(p), true
	case partFile:
		kind := agent.PartFile
		if strings.HasPrefix(p.Mime, "image/") {
			kind = agent.PartImage
		}
		fc := FileContent{Mime: p.Mime, Filename: p.Filename}
		if strings.HasPrefix(p.URL, "file://") {
			fc.URL = p.URL
		}
		raw, _ := json.Marshal(fc)
		return converted{kind: kind, state: agent.PartDone, content: raw}, true
	}
	return converted{}, false
}

func convertTool(p ocPart) converted {
	st := p.State
	if st == nil {
		st = &ocToolState{Status: toolPending}
	}
	var meta struct {
		Output    *string         `json:"output"`
		Exit      *int            `json:"exit"`
		SessionID string          `json:"sessionId"`
		Diff      string          `json:"diff"`
		FileDiff  *ocFileDiff     `json:"filediff"`
		Files     []ocPatchFile   `json:"files"`
		Exists    *bool           `json:"exists"`
		Todos     json.RawMessage `json:"todos"`
	}
	if len(st.Metadata) > 0 {
		_ = json.Unmarshal(st.Metadata, &meta)
	}
	var input struct {
		FilePath     string          `json:"filePath"`
		Content      *string         `json:"content"`
		SubagentType string          `json:"subagent_type"`
		Description  string          `json:"description"`
		Todos        json.RawMessage `json:"todos"`
	}
	if len(st.Input) > 0 {
		_ = json.Unmarshal(st.Input, &input)
	}
	state := agent.PartRunning
	text := ""
	switch st.Status {
	case toolCompleted:
		state, text = agent.PartDone, st.Output
	case toolError:
		state, text = agent.PartError, st.Error
	default:
		if meta.Output != nil {
			text = *meta.Output
		}
	}
	if p.Tool == "todowrite" {
		todos := input.Todos
		if len(meta.Todos) > 0 {
			todos = meta.Todos
		}
		tc := TodoContent{Todos: []Todo{}}
		_ = json.Unmarshal(todos, &tc.Todos)
		raw, _ := json.Marshal(tc)
		if state == agent.PartRunning {
			text = ""
		}
		return converted{kind: agent.PartTodo, state: state, content: raw}
	}
	tc := ToolContent{Tool: p.Tool, CallID: p.CallID, Title: st.Title, StartedAt: st.Time.Start}
	if len(st.Input) > 0 && string(st.Input) != "{}" {
		tc.Input = st.Input
	}
	if st.Status == toolError {
		tc.Error = st.Error
	}
	if st.Time.End > 0 {
		tc.EndedAt = st.Time.End
		if st.Time.Start > 0 {
			tc.DurationMS = st.Time.End - st.Time.Start
		}
	}
	if st.Status == toolCompleted {
		tc.Exit = meta.Exit
	}
	c := converted{kind: agent.PartTool, state: state, text: text}
	if p.Tool == "task" {
		tc.SessionID, tc.Agent = meta.SessionID, input.SubagentType
		if tc.Title == "" {
			tc.Title = input.Description
		}
		c.child = meta.SessionID
	}
	c.content, _ = json.Marshal(tc)
	if st.Status == toolCompleted {
		if files := editFiles(p.Tool, input.FilePath, input.Content, meta.Diff, meta.FileDiff, meta.Files, meta.Exists); len(files) > 0 {
			raw, _ := json.Marshal(DiffContent{ToolPart: p.ID, Files: files})
			c.diff = &converted{kind: agent.PartDiff, state: agent.PartDone, content: raw}
		}
	}
	return c
}

type ocFileDiff struct {
	File      string `json:"file"`
	Patch     string `json:"patch"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

type ocPatchFile struct {
	FilePath     string `json:"filePath"`
	RelativePath string `json:"relativePath"`
	Type         string `json:"type"`
	Patch        string `json:"patch"`
	Additions    int    `json:"additions"`
	Deletions    int    `json:"deletions"`
}

// editFiles is what an edit tool changed: edit's filediff, apply_patch's
// files, or, for write, which keeps no diff, the new file's content when it
// made one.
func editFiles(tool, path string, content *string, diff string, fd *ocFileDiff, files []ocPatchFile, exists *bool) []DiffFile {
	switch {
	case len(files) > 0:
		out := make([]DiffFile, 0, len(files))
		for _, f := range files {
			name := f.RelativePath
			if name == "" {
				name = f.FilePath
			}
			out = append(out, DiffFile{Path: name, Patch: f.Patch, Additions: f.Additions, Deletions: f.Deletions, Status: f.Type})
		}
		return out
	case fd != nil && fd.Patch != "":
		return []DiffFile{{Path: firstNonEmpty(fd.File, path), Patch: fd.Patch, Additions: fd.Additions, Deletions: fd.Deletions}}
	case diff != "":
		add, del := countChanges(diff)
		return []DiffFile{{Path: path, Patch: diff, Additions: add, Deletions: del}}
	case tool == "write" && content != nil && exists != nil && !*exists:
		patch, add := newFilePatch(path, *content)
		return []DiffFile{{Path: path, Patch: patch, Additions: add, Status: "added"}}
	}
	return nil
}

// newFilePatch is the unified diff that creates a file with content.
func newFilePatch(path, content string) (string, int) {
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	if content == "" {
		lines = nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "--- /dev/null\n+++ %s\n@@ -0,0 +1,%d @@\n", path, len(lines))
	for _, l := range lines {
		b.WriteString("+")
		b.WriteString(l)
		b.WriteString("\n")
	}
	return b.String(), len(lines)
}

// countChanges counts a unified diff's added and removed lines.
func countChanges(patch string) (add, del int) {
	for _, l := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"):
		case strings.HasPrefix(l, "+"):
			add++
		case strings.HasPrefix(l, "-"):
			del++
		}
	}
	return add, del
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// userText is what a user message says: its text parts that are not
// synthetic, joined.
func userText(parts []ocPart) string {
	var texts []string
	for _, p := range parts {
		if p.Type == partText && !p.Synthetic && !p.Ignored && strings.TrimSpace(p.Text) != "" {
			texts = append(texts, p.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// userAttachments are a user message's file parts that point at a file.
func userAttachments(parts []ocPart) []agent.Attachment {
	var out []agent.Attachment
	for _, p := range parts {
		if p.Type != partFile {
			continue
		}
		a := agent.Attachment{Name: p.Filename, Mime: p.Mime}
		if path, ok := strings.CutPrefix(p.URL, "file://"); ok {
			a.Path = path
		}
		out = append(out, a)
	}
	return out
}
