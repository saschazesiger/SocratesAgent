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
)

// Codex writes a rollout per thread, <CODEX_HOME>/sessions/YYYY/MM/DD/
// rollout-…-<thread>.jsonl (and archived_sessions/ once archived). An agent a
// conversation spawns is a thread of its own with a rollout of its own, and
// its records name the conversation it belongs to as session_id - so a
// conversation is every record with its id, whichever file it is in. Each
// request is a token_usage_record with its usage; the model is the one the
// latest turn_context before it named. A rollout from a Codex that predates
// those records has token_count events instead, whose last_token_usage is the
// same thing.

// codexRec is one request.
type codexRec struct {
	at                                int64
	conv, response, model             string
	input, cached, cacheWrite, output float64
}

// codexFile is a rollout as read so far. convs survives an eviction, so a
// pass still knows which conversations a file belongs to without reading it.
type codexFile struct {
	size, mod int64
	offset    int64
	parsed    bool
	model     string // the model of the turn being read
	cwd       string
	conv      string // session_meta's conversation, for token_count events
	hasRecs   bool   // the file has token_usage_record lines
	convs     map[string]bool
	recs      []codexRec
}

type codexLine struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Payload   struct {
		Type       string      `json:"type"`
		ID         string      `json:"id"`
		SessionID  string      `json:"session_id"`
		ThreadID   string      `json:"thread_id"`
		ResponseID string      `json:"response_id"`
		Model      string      `json:"model"`
		Cwd        string      `json:"cwd"`
		Usage      *codexUsage `json:"usage"`
		Info       *struct {
			Last *codexUsage `json:"last_token_usage"`
		} `json:"info"`
	} `json:"payload"`
}

type codexUsage struct {
	Input      float64 `json:"input_tokens"`
	Cached     float64 `json:"cached_input_tokens"`
	CacheWrite float64 `json:"cache_write_input_tokens"`
	Output     float64 `json:"output_tokens"`
}

var (
	markUsageRecord = []byte(`"type":"token_usage_record"`)
	markTurnContext = []byte(`"type":"turn_context"`)
	markSessionMeta = []byte(`"type":"session_meta"`)
	markTokenCount  = []byte(`"type":"token_count"`)
)

func (f *codexFile) load(path string, st fileState) error {
	if f.parsed && st.size == f.size && st.mod == f.mod {
		return nil
	}
	if !f.parsed || st.size < f.offset {
		*f = codexFile{convs: map[string]bool{}}
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
			break
		}
		f.offset += int64(len(line))
		f.parseLine(line)
	}
	f.size, f.mod, f.parsed = st.size, st.mod, true
	return nil
}

func (f *codexFile) parseLine(raw []byte) {
	record := bytes.Contains(raw, markUsageRecord)
	turn := !record && bytes.Contains(raw, markTurnContext)
	meta := !record && !turn && bytes.Contains(raw, markSessionMeta)
	count := !record && !turn && !meta && !f.hasRecs && bytes.Contains(raw, markTokenCount)
	if !record && !turn && !meta && !count {
		return
	}
	var line codexLine
	if json.Unmarshal(raw, &line) != nil {
		return
	}
	p := line.Payload
	switch {
	case line.Type == "session_meta":
		f.conv = firstOf(p.SessionID, p.ID)
		if f.cwd == "" {
			f.cwd = p.Cwd
		}
	case line.Type == "turn_context":
		if p.Model != "" {
			f.model = p.Model
		}
		if f.cwd == "" {
			f.cwd = p.Cwd
		}
	case line.Type == "token_usage_record" && p.Usage != nil:
		if !f.hasRecs {
			// The records are the whole truth; counts read before the first
			// of them would be the same requests twice.
			f.hasRecs = true
			f.recs = f.recs[:0]
		}
		f.add(line.Timestamp, firstOf(p.SessionID, p.ThreadID, f.conv), p.ResponseID, *p.Usage)
	case line.Type == "event_msg" && p.Type == "token_count" && p.Info != nil && p.Info.Last != nil:
		f.add(line.Timestamp, f.conv, "", *p.Info.Last)
	}
}

func (f *codexFile) add(ts, conv, response string, u codexUsage) {
	at := parseTime(ts)
	if at <= 0 || conv == "" {
		return
	}
	f.convs[conv] = true
	f.recs = append(f.recs, codexRec{at: at, conv: conv, response: response, model: f.model,
		input: u.Input, cached: u.Cached, cacheWrite: u.CacheWrite, output: u.Output})
}

func firstOf(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// scanCodex reads every rollout under one CODEX_HOME.
func (sc *Scanner) scanCodex(root Root, seen map[string]string) []Conversation {
	byConv := map[string][]fileState{}
	for _, dir := range []string{"sessions", "archived_sessions"} {
		// WalkDir does not enter a root that is a link, and a home whose
		// sessions live elsewhere is one.
		base, err := filepath.EvalSymlinks(filepath.Join(root.CodexHome, dir))
		if err != nil {
			continue
		}
		_ = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
				return nil
			}
			st, ok := statFile(path)
			if !ok {
				return nil
			}
			f := sc.codex[path]
			if f == nil {
				f = &codexFile{}
				sc.codex[path] = f
			}
			// A file whose conversations are known and that has not changed
			// is not opened: its size and time are the fingerprint.
			if !(f.convs != nil && st.size == f.size && st.mod == f.mod) {
				if f.load(path, st) != nil {
					return nil
				}
			}
			for conv := range f.convs {
				byConv[conv] = append(byConv[conv], st)
			}
			return nil
		})
	}
	var out []Conversation
	for id, states := range byConv {
		conv := Conversation{Harness: "codex", ID: id, Profile: root.Profile, Fingerprint: fingerprint(states)}
		if seen[conv.Key()] == conv.Fingerprint {
			continue
		}
		seenResponse := map[string]bool{}
		var spends []spend
		for _, st := range states {
			f := sc.codex[st.path]
			if !f.parsed {
				if f.load(st.path, st) != nil {
					continue
				}
			}
			if conv.Cwd == "" && f.conv == id {
				conv.Cwd = f.cwd
			}
			for _, r := range f.recs {
				if r.conv != id {
					continue
				}
				if r.response != "" {
					if seenResponse[r.response] {
						continue
					}
					seenResponse[r.response] = true
				}
				cost, priced := CodexCost(r.model, r.input, r.cached, r.cacheWrite, r.output)
				spends = append(spends, spend{at: r.at, model: firstOf(r.model, "unknown"), cost: cost,
					priced: priced, requests: 1, input: max(0, r.input-r.cached-r.cacheWrite),
					output: r.output, cacheRead: r.cached, cacheWrite: r.cacheWrite})
			}
			if conv.Cwd == "" {
				conv.Cwd = f.cwd
			}
		}
		conv.Rows, conv.FirstAt, conv.LastAt = hourly(spends)
		out = append(out, conv)
	}
	return out
}
