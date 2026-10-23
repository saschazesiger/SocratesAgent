package costs

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// OpenCode keeps its conversations in one SQLite database,
// <data>/opencode/opencode.db, and prices every answer itself: an assistant
// message carries its cost, its tokens, its model and when it was made. An
// agent's conversation is a session whose parent_id is the conversation that
// started it, so its answers are counted under that one.

type openCodeMessage struct {
	Role    string  `json:"role"`
	Cost    float64 `json:"cost"`
	ModelID string  `json:"modelID"`
	Tokens  struct {
		Input  float64 `json:"input"`
		Output float64 `json:"output"`
		Reason float64 `json:"reasoning"`
		Cache  struct {
			Read  float64 `json:"read"`
			Write float64 `json:"write"`
		} `json:"cache"`
	} `json:"tokens"`
	Time struct {
		Created int64 `json:"created"`
	} `json:"time"`
}

// scanOpenCode reads one OpenCode database. It is read only when the file or
// its write-ahead log changed since the last pass.
func (sc *Scanner) scanOpenCode(root Root, seen map[string]string) []Conversation {
	path := filepath.Join(root.OpenCodeDir, "opencode.db")
	db, ok := statFile(path)
	if !ok {
		return nil
	}
	states := []fileState{db}
	if wal, ok := statFile(path + "-wal"); ok {
		states = append(states, wal)
	}
	whole := fingerprint(states)
	if sc.opencode[path] == whole {
		return nil
	}
	convs, err := readOpenCode(path, root.Profile)
	if err != nil {
		return nil
	}
	sc.opencode[path] = whole
	var out []Conversation
	for _, c := range convs {
		if seen[c.Key()] != c.Fingerprint {
			out = append(out, c)
		}
	}
	return out
}

func readOpenCode(path, profile string) ([]Conversation, error) {
	conn, err := sql.Open("sqlite", "file:"+(&url.URL{Path: path}).EscapedPath()+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	type session struct{ parent, dir, title string }
	sessions := map[string]session{}
	rows, err := conn.Query(`SELECT id, COALESCE(parent_id, ''), directory, title FROM session`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var s session
		if err := rows.Scan(&id, &s.parent, &s.dir, &s.title); err != nil {
			rows.Close()
			return nil, err
		}
		sessions[id] = s
	}
	rows.Close()
	rootOf := func(id string) string {
		for range 64 {
			s, ok := sessions[id]
			if !ok || s.parent == "" {
				return id
			}
			id = s.parent
		}
		return id
	}

	spends := map[string][]spend{}
	rows, err = conn.Query(`SELECT session_id, data FROM message WHERE data LIKE '%"role":"assistant"%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var sid, data string
		if err := rows.Scan(&sid, &data); err != nil {
			return nil, err
		}
		var m openCodeMessage
		if json.Unmarshal([]byte(data), &m) != nil || m.Role != "assistant" || m.Time.Created <= 0 {
			continue
		}
		if m.Cost == 0 && m.Tokens.Input+m.Tokens.Output+m.Tokens.Cache.Read+m.Tokens.Cache.Write == 0 {
			continue
		}
		conv := rootOf(sid)
		spends[conv] = append(spends[conv], spend{
			at: m.Time.Created, model: firstOf(strings.TrimPrefix(m.ModelID, "~"), "unknown"),
			cost: m.Cost, priced: true, requests: 1,
			input: m.Tokens.Input, output: m.Tokens.Output + m.Tokens.Reason,
			cacheRead: m.Tokens.Cache.Read, cacheWrite: m.Tokens.Cache.Write,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []Conversation
	for id, list := range spends {
		c := Conversation{Harness: "opencode", ID: id, Profile: profile,
			Cwd: sessions[id].dir, Title: sessions[id].title}
		c.Rows, c.FirstAt, c.LastAt = hourly(list)
		var total float64
		for _, s := range list {
			total += s.cost
		}
		c.Fingerprint = fmt.Sprintf("v%s:%d:%d:%.6f", Version, len(list), c.LastAt, total)
		out = append(out, c)
	}
	return out, nil
}
