package costs

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Claude Code keeps one transcript per conversation,
// <config>/projects/<folder>/<id>.jsonl, and the transcripts of the agents
// that conversation started below <config>/projects/<folder>/<id>/
// (subagents/…, workflows/…). Every answer is a line of type "assistant" with
// the model and the usage of the request that produced it; an answer split
// over several content blocks is written once per block with the same
// message id, so answers are counted by id.
//
// The transcript does not record everything a run spends: titles, compaction
// and other helper requests are billed but never written there. What a run
// spent in all is the "cost-state" line Claude Code appends when it exits, by
// model and in dollars, for that run alone. Where the run's own answers come
// to less than that, the difference is added over the hours the run answered
// in, so a finished run adds up to exactly what Claude Code says it cost. A
// run that is still going, or that was killed, counts its answers only.

// claudeMsg is one answer.
type claudeMsg struct {
	at     int64
	model  string
	t      claudeTokens
	cost   float64
	priced bool
}

// claudeRunModel is one model's total in a run's cost-state.
type claudeRunModel struct {
	cost, input, output, cacheRead, cacheWrite float64
}

// claudeRun is one run of the CLI on the conversation: when it started, the
// newest answer before it exited, and what it says it spent.
type claudeRun struct {
	start, end int64
	models     map[string]claudeRunModel
}

// claudeFile is a transcript as read so far.
type claudeFile struct {
	size, mod int64
	offset    int64 // the end of the last complete line read
	parsed    bool
	lastAt    int64
	cwd       string
	titles    [3]string // custom, ai, last prompt
	msgs      map[string]claudeMsg
	runs      map[int64]claudeRun
}

func (f *claudeFile) drop() {
	*f = claudeFile{size: f.size, mod: f.mod}
}

// claudeLine is the part of a transcript line the ledger reads.
type claudeLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	RequestID string `json:"requestId"`
	UUID      string `json:"uuid"`
	Cwd       string `json:"cwd"`
	Message   struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			Input         int64 `json:"input_tokens"`
			CacheCreation int64 `json:"cache_creation_input_tokens"`
			CacheRead     int64 `json:"cache_read_input_tokens"`
			Output        int64 `json:"output_tokens"`
			Creation      *struct {
				FiveMin int64 `json:"ephemeral_5m_input_tokens"`
				OneHour int64 `json:"ephemeral_1h_input_tokens"`
			} `json:"cache_creation"`
			ServerTools *struct {
				WebSearch int64 `json:"web_search_requests"`
			} `json:"server_tool_use"`
			Speed string `json:"speed"`
		} `json:"usage"`
	} `json:"message"`
	StartTime  int64 `json:"startTime"`
	ModelUsage map[string]struct {
		Input         float64 `json:"inputTokens"`
		Output        float64 `json:"outputTokens"`
		CacheRead     float64 `json:"cacheReadInputTokens"`
		CacheCreation float64 `json:"cacheCreationInputTokens"`
		CostUSD       float64 `json:"costUSD"`
	} `json:"modelUsage"`
	CustomTitle string `json:"customTitle"`
	AITitle     string `json:"aiTitle"`
	LastPrompt  string `json:"lastPrompt"`
}

var (
	markAssistant = []byte(`"type":"assistant"`)
	markCostState = []byte(`"type":"cost-state"`)
	markTitle     = []byte(`-title"`)
	markPrompt    = []byte(`"type":"last-prompt"`)
)

// load brings the file up to date with the disk: the lines appended since the
// last read, or all of it when it was rewritten or never read.
func (f *claudeFile) load(path string, st fileState) error {
	if f.parsed && st.size == f.size && st.mod == f.mod {
		return nil
	}
	if !f.parsed || st.size < f.offset {
		f.drop()
		f.msgs = map[string]claudeMsg{}
		f.runs = map[int64]claudeRun{}
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Seek(f.offset, io.SeekStart); err != nil {
		return err
	}
	r := bufio.NewReaderSize(file, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			// A line without its newline is still being written; it is read
			// whole next time.
			break
		}
		f.offset += int64(len(line))
		f.parseLine(line)
	}
	f.size, f.mod, f.parsed = st.size, st.mod, true
	return nil
}

func (f *claudeFile) parseLine(raw []byte) {
	assistant := bytes.Contains(raw, markAssistant)
	costState := !assistant && bytes.Contains(raw, markCostState)
	titled := !assistant && !costState && bytes.HasPrefix(raw, []byte(`{"type":"`)) &&
		(bytes.Contains(raw[:min(len(raw), 40)], markTitle) || bytes.HasPrefix(raw, append([]byte(`{`), markPrompt...)))
	if !assistant && !costState && !titled {
		return
	}
	var line claudeLine
	if json.Unmarshal(raw, &line) != nil {
		return
	}
	switch line.Type {
	case "custom-title":
		if t := strings.TrimSpace(line.CustomTitle); t != "" {
			f.titles[0] = t
		}
	case "ai-title":
		if t := strings.TrimSpace(line.AITitle); t != "" {
			f.titles[1] = t
		}
	case "last-prompt":
		if f.titles[2] == "" {
			f.titles[2] = strings.TrimSpace(line.LastPrompt)
		}
	case "cost-state":
		run := claudeRun{start: line.StartTime, end: f.lastAt, models: map[string]claudeRunModel{}}
		for model, u := range line.ModelUsage {
			run.models[claudeLabel(model)] = claudeRunModel{cost: u.CostUSD, input: u.Input, output: u.Output,
				cacheRead: u.CacheRead, cacheWrite: u.CacheCreation}
		}
		if run.start > 0 {
			f.runs[run.start] = run
		}
	case "assistant":
		u := line.Message.Usage
		if u == nil || line.Message.Model == "" || line.Message.Model == "<synthetic>" {
			return
		}
		at := parseTime(line.Timestamp)
		if at <= 0 {
			return
		}
		if f.cwd == "" {
			f.cwd = line.Cwd
		}
		f.lastAt = max(f.lastAt, at)
		t := claudeTokens{input: u.Input, output: u.Output, cacheRead: u.CacheRead, fast: u.Speed == "fast"}
		if u.Creation != nil && u.Creation.FiveMin+u.Creation.OneHour > 0 {
			t.write5m, t.write1h = u.Creation.FiveMin, u.Creation.OneHour
		} else {
			t.write5m = u.CacheCreation
		}
		if u.ServerTools != nil {
			t.webSearches = u.ServerTools.WebSearch
		}
		id := line.Message.ID
		if id == "" {
			id = line.RequestID
		}
		if id == "" {
			id = line.UUID
		}
		// The blocks of one answer repeat its usage; the last one written
		// has the final count.
		if old, ok := f.msgs[id]; ok && old.t.output > t.output {
			return
		}
		cost, priced := claudeCost(line.Message.Model, t)
		f.msgs[id] = claudeMsg{at: at, model: claudeLabel(line.Message.Model), t: t, cost: cost, priced: priced}
	}
}

// claudeLabel is the name a model is counted under: the one-million context
// variant and a dated snapshot are the same model at the same price.
func claudeLabel(model string) string {
	if id := claudeModel(model); id != "" {
		return id
	}
	return model
}

func parseTime(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

// scanClaude reads every conversation under one Claude Code directory.
func (sc *Scanner) scanClaude(root Root, seen map[string]string) []Conversation {
	mains, _ := filepath.Glob(filepath.Join(root.ClaudeDir, "projects", "*", "*.jsonl"))
	var out []Conversation
	for _, main := range mains {
		id := strings.TrimSuffix(filepath.Base(main), ".jsonl")
		mainState, ok := statFile(main)
		if !ok || id == "" {
			continue
		}
		states := []fileState{mainState}
		agents, err := filepath.EvalSymlinks(strings.TrimSuffix(main, ".jsonl"))
		if err != nil {
			agents = strings.TrimSuffix(main, ".jsonl")
		}
		_ = filepath.WalkDir(agents, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if !d.IsDir() && strings.HasSuffix(path, ".jsonl") {
				if st, ok := statFile(path); ok {
					states = append(states, st)
				}
			}
			return nil
		})
		conv := Conversation{Harness: "claude", ID: id, Profile: root.Profile}
		conv.Fingerprint = fingerprint(states)
		if seen[conv.Key()] == conv.Fingerprint {
			continue
		}
		sc.readClaude(&conv, states)
		out = append(out, conv)
	}
	return out
}

// readClaude fills a conversation's ledger from its transcripts; states[0]
// is the conversation's own, the rest its agents'.
func (sc *Scanner) readClaude(conv *Conversation, states []fileState) {
	// A transcript that has not changed for a while is let go once it is
	// read, so the first pass over months of them does not hold them all at
	// once.
	defer func() {
		cutoff := time.Now().Add(-sc.keep).UnixNano()
		for _, st := range states {
			if f := sc.claude[st.path]; f != nil && f.mod < cutoff {
				f.drop()
			}
		}
	}()
	msgs := map[string]claudeMsg{}
	var main *claudeFile
	for i, st := range states {
		f := sc.claude[st.path]
		if f == nil {
			f = &claudeFile{}
			sc.claude[st.path] = f
		}
		if f.load(st.path, st) != nil {
			continue
		}
		if i == 0 {
			main = f
		}
		for id, m := range f.msgs {
			if old, ok := msgs[id]; !ok || m.t.output > old.t.output {
				msgs[id] = m
			}
		}
	}
	if main == nil {
		return
	}
	conv.Cwd = main.cwd
	for _, t := range main.titles {
		if t != "" {
			conv.Title = t
			break
		}
	}

	spends := make([]spend, 0, len(msgs))
	for _, m := range msgs {
		spends = append(spends, spend{
			at: m.at, model: m.model, cost: m.cost, priced: m.priced, requests: 1,
			input: float64(m.t.input), output: float64(m.t.output), cacheRead: float64(m.t.cacheRead),
			cacheWrite: float64(m.t.write5m + m.t.write1h),
		})
	}
	for _, run := range main.runs {
		spends = append(spends, runCorrection(run, msgs)...)
	}
	conv.Rows, conv.FirstAt, conv.LastAt = hourly(spends)
}

// runCorrection is what a finished run spent beyond the answers its
// transcripts hold, spread over the hours it answered in.
func runCorrection(run claudeRun, msgs map[string]claudeMsg) []spend {
	// The run's own answers are the ones from its start to its last answer
	// before it exited, with a minute's slack for an agent finishing late.
	from, to := run.start, max(run.end, run.start)+60_000
	var out []spend
	for model, total := range run.models {
		var window []claudeMsg
		var have claudeRunModel
		for _, m := range msgs {
			if m.model != model || m.at < from || m.at > to {
				continue
			}
			window = append(window, m)
			have.cost += m.cost
			have.input += float64(m.t.input)
			have.output += float64(m.t.output)
			have.cacheRead += float64(m.t.cacheRead)
			have.cacheWrite += float64(m.t.write5m + m.t.write1h)
		}
		missing := claudeRunModel{
			cost:       total.cost - have.cost,
			input:      max(0, total.input-have.input),
			output:     max(0, total.output-have.output),
			cacheRead:  max(0, total.cacheRead-have.cacheRead),
			cacheWrite: max(0, total.cacheWrite-have.cacheWrite),
		}
		if missing.cost < 1e-6 {
			continue
		}
		if len(window) == 0 {
			out = append(out, spend{at: max(run.end, run.start), model: model, priced: true,
				cost: missing.cost, input: missing.input, output: missing.output,
				cacheRead: missing.cacheRead, cacheWrite: missing.cacheWrite})
			continue
		}
		// Spread by what each answer cost, or by its size when none of them
		// had a price.
		var size float64
		for _, m := range window {
			size += float64(m.t.output + 1)
		}
		weight := func(m claudeMsg) float64 {
			if have.cost > 0 {
				return m.cost / have.cost
			}
			return float64(m.t.output+1) / size
		}
		for _, m := range window {
			w := weight(m)
			out = append(out, spend{at: m.at, model: model, priced: true,
				cost: missing.cost * w, input: missing.input * w, output: missing.output * w,
				cacheRead: missing.cacheRead * w, cacheWrite: missing.cacheWrite * w})
		}
	}
	return out
}
