//go:build !windows

package opencode

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// What a step changed in the files (research/05 §2.3). OpenCode takes a
// snapshot of the worktree around every step and ends a step that changed
// files with a patch part, {hash, files}: the absolute paths of every file
// it changed, whatever changed it - an edit call, or a shell command. Right
// after it, it stores the turn's diff so far in its user message,
// summary.diffs (the diff from the turn's first snapshot to the step's
// last), and says so with message.updated (read from 1.19.5's source:
// SessionProcessor at step-finish, SessionSummary.summarize). The adapter
// shows a step's files that no edit call of the turn showed already, with
// their diff from the summary, as a diff part of its own (StepTool).

// StepTool is the tool a diff part of a step's changes names: the files a
// step changed that no edit call showed, the ones a shell command changed.
const StepTool = "changes"

// ocSnapshotDiff is one file of summary.diffs, as GET
// /session/{id}/diff?messageID= also lists them: the path relative to the
// worktree.
type ocSnapshotDiff struct {
	File      string `json:"file"`
	Patch     string `json:"patch"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Status    string `json:"status"`
}

// summaryDiffs is a user message's summary.diffs; ok is false when the
// message has none (yet). An assistant message's summary is a boolean.
func summaryDiffs(raw json.RawMessage) ([]ocSnapshotDiff, bool) {
	if len(raw) == 0 || raw[0] != '{' {
		return nil, false
	}
	var s struct {
		Diffs *[]ocSnapshotDiff `json:"diffs"`
	}
	if json.Unmarshal(raw, &s) != nil || s.Diffs == nil {
		return nil, false
	}
	return *s.Diffs, true
}

// turnChanges is what a turn changed in the files, as far as the adapter
// shows it.
type turnChanges struct {
	// edited are the files edit calls of the turn showed in a diff of
	// their own, as the calls named them.
	edited []string
	// pending are the patch parts not shown yet: each waits for the
	// summary OpenCode stores after it.
	pending []ocPart
	// diffs is the newest summary.diffs of the turn's user message;
	// summarized is whether there was one.
	diffs      []ocSnapshotDiff
	summarized bool
}

// patchLocked takes a patch part of the turn's own steps to show once the
// summary that follows it says what changed. A patch part of an earlier
// turn (a catch-up reads the whole session) or of a subagent (the step of
// the task that ran it has the same files) is not shown.
func (a *Adapter) patchLocked(p ocPart) {
	t := a.turn
	if t == nil || p.SessionID != a.session || !slices.Contains(t.assistants, p.MessageID) || a.parts[p.ID] != nil {
		return
	}
	if slices.ContainsFunc(t.changes.pending, func(q ocPart) bool { return q.ID == p.ID }) {
		return
	}
	t.changes.pending = append(t.changes.pending, p)
}

// summaryLocked takes the turn's diff so far from its user message, and
// shows the steps that waited for it.
func (a *Adapter) summaryLocked(m ocMessage) {
	t := a.turn
	if t == nil || t.user == "" || m.ID != t.user {
		return
	}
	diffs, ok := summaryDiffs(m.Summary)
	if !ok {
		return
	}
	t.changes.diffs, t.changes.summarized = diffs, true
	a.showChangesLocked(t)
}

// showChangesLocked writes the pending steps' changes. A step's file the
// summary does not list is one the turn left as it was; without any
// summary (OpenCode failed to make one), a file is named without its diff.
func (a *Adapter) showChangesLocked(t *turnState) {
	pending := t.changes.pending
	t.changes.pending = nil
	for _, p := range pending {
		files := stepFiles(p.Files, t.changes.edited, t.changes.diffs, t.changes.summarized, a.spec.Workdir)
		if len(files) == 0 {
			continue
		}
		a.emitPartLocked(p.ID, p.MessageID, "", stepDiff(files))
	}
}

// stepDiff is the diff part of a step's changes.
func stepDiff(files []DiffFile) converted {
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, f.Path)
	}
	raw, _ := json.Marshal(DiffContent{Tool: StepTool, Title: strings.Join(names, ", "), Files: files})
	return converted{kind: agent.PartDiff, state: agent.PartDone, content: raw}
}

// stepFiles is what a step changed (files, absolute) that no edit call
// showed (edited), each with its diff from the turn's summary.
func stepFiles(files, edited []string, diffs []ocSnapshotDiff, summarized bool, workdir string) []DiffFile {
	var out []DiffFile
	for _, file := range files {
		if slices.ContainsFunc(edited, func(e string) bool { return samePath(file, e) }) {
			continue
		}
		i := slices.IndexFunc(diffs, func(d ocSnapshotDiff) bool { return samePath(file, d.File) })
		switch {
		case i >= 0:
			d := diffs[i]
			out = append(out, DiffFile{Path: d.File, Patch: d.Patch, Additions: d.Additions, Deletions: d.Deletions, Status: d.Status})
		case !summarized:
			out = append(out, DiffFile{Path: relativeTo(workdir, file)})
		}
	}
	return out
}

// samePath says whether name, absolute or relative to some folder above
// the file, is the absolute path file.
func samePath(file, name string) bool {
	if name == "" {
		return false
	}
	if filepath.IsAbs(name) {
		return filepath.Clean(name) == filepath.Clean(file)
	}
	return strings.HasSuffix(filepath.Clean(file), string(filepath.Separator)+filepath.Clean(name))
}

// relativeTo is path as the folder dir sees it, when it is inside it.
func relativeTo(dir, path string) string {
	if dir == "" {
		return path
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	return rel
}
