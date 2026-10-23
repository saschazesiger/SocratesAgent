//go:build !windows

package claude

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// tailEvery is how often a running workflow's agent transcripts are read.
const tailEvery = 500 * time.Millisecond

// tail reads the transcripts of a running workflow's agents for their tool
// calls. The stream carries only each agent's state and last tool
// (task_progress.workflow_progress); the calls themselves are only in
// <transcriptDir>/agent-<id>.jsonl (09-claude-subagents-workflows.md §3).
type tail struct {
	dir     string
	partID  string
	offsets map[string]int64
	stop    chan struct{}
	stopped bool
}

// startTail follows a workflow's transcript directory until it ends.
func (s *state) startTail(partID, dir string) {
	if s.tails[partID] != nil {
		return
	}
	t := &tail{dir: dir, partID: partID, offsets: map[string]int64{}, stop: make(chan struct{})}
	s.tails[partID] = t
	go t.run(s.a)
}

// stopTail ends a workflow's tail after one last read.
func (s *state) stopTail(partID string) {
	if t := s.tails[partID]; t != nil && !t.stopped {
		t.stopped = true
		close(t.stop)
	}
	delete(s.tails, partID)
}

func (s *state) stopTails() {
	for id := range s.tails {
		s.stopTail(id)
	}
}

func (t *tail) run(a *Adapter) {
	tick := time.NewTicker(tailEvery)
	defer tick.Stop()
	for {
		select {
		case <-t.stop:
			t.scan(a)
			return
		case <-tick.C:
			t.scan(a)
		}
	}
}

// scan reads what every agent transcript grew by, in whole lines.
func (t *tail) scan(a *Adapter) {
	files, _ := filepath.Glob(filepath.Join(t.dir, "agent-*.jsonl"))
	sort.Strings(files)
	for _, path := range files {
		agentID := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "agent-"), ".jsonl")
		a.mu.Lock()
		parent := a.st.wfAgents[agentID]
		a.mu.Unlock()
		if parent == "" {
			// The workflow has not reported this agent yet; its lines wait
			// for the part they belong in.
			continue
		}
		lines := t.read(path)
		if len(lines) == 0 {
			continue
		}
		a.mu.Lock()
		for _, l := range lines {
			a.st.agentLine(l, parent)
		}
		a.mu.Unlock()
	}
}

func (t *tail) read(path string) []transcriptLine {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if _, err := f.Seek(t.offsets[path], io.SeekStart); err != nil {
		return nil
	}
	raw, err := io.ReadAll(f)
	if err != nil {
		return nil
	}
	end := bytes.LastIndexByte(raw, '\n')
	if end < 0 {
		return nil
	}
	t.offsets[path] += int64(end + 1)
	var out []transcriptLine
	for _, line := range bytes.Split(raw[:end], []byte{'\n'}) {
		var l transcriptLine
		if json.Unmarshal(line, &l) == nil {
			out = append(out, l)
		}
	}
	return out
}

// agentLine maps one line of a workflow agent's transcript: its tool calls
// and their results, nested in the agent's part.
func (s *state) agentLine(l transcriptLine, parent string) {
	if l.UUID != "" {
		if s.seen[l.UUID] {
			return
		}
		s.seen[l.UUID] = true
	}
	var msg message
	if json.Unmarshal(l.Message, &msg) != nil {
		return
	}
	for _, b := range msg.blocks() {
		switch {
		case l.Type == "assistant" && b.Type == "tool_use":
			s.toolUse(b, parent)
		case l.Type == "user" && b.Type == "tool_result":
			s.toolResult(b, l.ToolUseResult, parent)
		}
	}
}
