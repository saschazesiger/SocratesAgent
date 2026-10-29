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
// after it, it stores the diff of the user message's steps so far in that
// message, summary.diffs (from their first snapshot to the step's last),
// and says so with message.updated (read from 1.19.5's source:
// SessionProcessor at step-finish, SessionSummary.summarize). The adapter
// shows a step's files that no edit call of the turn showed already, with
// their diff from the summary, as a diff part of its own (StepTool). A file
// no summary has a diff for is named without one, and gets its lines when a
// later summary brings them.

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
	// workdir is the chat's folder, which paths are shown from; root and
	// cwd are the worktree and the folder OpenCode works in, as its
	// assistant messages name them (workdir until one does).
	workdir, root, cwd string
	// edited are the files edit calls of the turn showed in a diff of
	// their own, absolute.
	edited []string
	// pending are the patch parts not shown yet: each waits for the
	// summary OpenCode stores after it. partial are those shown with a
	// file that had no diff yet.
	pending []ocPart
	partial []ocPart
	// diffs are the newest diff of every file the summaries of the turn's
	// user messages (its own, a compaction's) listed.
	diffs []ocSnapshotDiff
}

// place takes the worktree and the folder from an assistant message.
func (c *turnChanges) place(p *ocPath) {
	if p == nil {
		return
	}
	if p.Root != "" {
		c.root = p.Root
	}
	if p.Cwd != "" {
		c.cwd = p.Cwd
	}
}

// edit keeps the files an edit call showed; a relative name is the folder
// OpenCode works in's.
func (c *turnChanges) edit(paths []string) {
	for _, p := range paths {
		p = c.abs(c.cwd, p)
		if !slices.Contains(c.edited, p) {
			c.edited = append(c.edited, p)
		}
	}
}

// summary takes a summary's diffs, newer than any it had for those files.
func (c *turnChanges) summary(diffs []ocSnapshotDiff) {
	for _, d := range diffs {
		if i := slices.IndexFunc(c.diffs, func(o ocSnapshotDiff) bool { return o.File == d.File }); i >= 0 {
			c.diffs[i] = d
		} else {
			c.diffs = append(c.diffs, d)
		}
	}
}

// abs is name as an absolute path, a relative one taken from dir (the
// workdir when dir is not known).
func (c *turnChanges) abs(dir, name string) string {
	if filepath.IsAbs(name) {
		return filepath.Clean(name)
	}
	if dir == "" {
		dir = c.workdir
	}
	return filepath.Join(dir, name)
}

// files is what a step changed (files, absolute) that no edit call
// showed, each with its diff from the summaries; complete is false when a
// file has none yet and is only named.
func (c *turnChanges) files(files []string) (out []DiffFile, complete bool) {
	complete = true
	for _, file := range files {
		file = filepath.Clean(file)
		if slices.Contains(c.edited, file) {
			continue
		}
		shown := relativeTo(c.workdir, file)
		i := slices.IndexFunc(c.diffs, func(d ocSnapshotDiff) bool { return d.File != "" && c.abs(c.root, d.File) == file })
		if i < 0 {
			complete = false
			out = append(out, DiffFile{Path: shown})
			continue
		}
		d := c.diffs[i]
		out = append(out, DiffFile{Path: shown, Patch: d.Patch, Additions: d.Additions, Deletions: d.Deletions, Status: d.Status})
	}
	return out, complete
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

// summaryLocked takes the diff a user message of the running turn (its own,
// or a compaction's), or of the turn that just ended, holds, and shows the
// steps that waited for it.
func (a *Adapter) summaryLocked(m ocMessage) {
	diffs, ok := summaryDiffs(m.Summary)
	if !ok {
		return
	}
	for _, t := range []*turnState{a.turn, a.ended} {
		if t != nil && a.turnsUserLocked(t, m.ID) {
			t.changes.summary(diffs)
			a.showChangesLocked(t)
			return
		}
	}
}

// turnsUserLocked says whether a user message is one of the turn's: the
// one it answers, or one an assistant message of it answers (a compaction's,
// the one an adopted turn took up).
func (a *Adapter) turnsUserLocked(t *turnState, id string) bool {
	if id == "" {
		return false
	}
	if id == t.user {
		return true
	}
	return slices.ContainsFunc(t.assistants, func(as string) bool { return a.assistants[as] != nil && a.assistants[as].parent == id })
}

// showChangesLocked writes the pending steps' changes, and those of steps
// written with a file that had no diff yet again, when a summary brought it.
func (a *Adapter) showChangesLocked(t *turnState) {
	steps := append(t.changes.partial, t.changes.pending...)
	t.changes.pending, t.changes.partial = nil, nil
	for _, p := range steps {
		files, complete := t.changes.files(p.Files)
		if !complete {
			t.changes.partial = append(t.changes.partial, p)
		}
		if len(files) > 0 {
			a.emitPartLocked(p.ID, p.MessageID, "", stepDiff(files))
		}
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
