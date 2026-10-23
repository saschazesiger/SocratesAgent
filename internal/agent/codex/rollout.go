package codex

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// strippedTypes are the rollout items that carry encrypted, account-bound
// model state.
var strippedTypes = map[string]bool{"reasoning": true, "compaction": true}

// StripRollout copies the rollout at src as a new thread, newID, without its
// reasoning and compaction items, next to it in the same sessions folder, and
// returns the copy's path and how many items it left out. The original is
// read, never written.
func StripRollout(src, oldID, newID string) (string, int, error) {
	if oldID == "" || !strings.Contains(filepath.Base(src), oldID) {
		return "", 0, fmt.Errorf("%s is not the rollout of %s", src, oldID)
	}
	f, err := os.Open(src)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	var out bytes.Buffer
	removed := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 256<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		kept, n, err := stripLine(line)
		if err != nil {
			return "", 0, err
		}
		removed += n
		if kept == nil {
			continue
		}
		out.Write(bytes.ReplaceAll(kept, []byte(oldID), []byte(newID)))
		out.WriteByte('\n')
	}
	if err := sc.Err(); err != nil {
		return "", 0, err
	}
	dst := filepath.Join(filepath.Dir(src), strings.ReplaceAll(filepath.Base(src), oldID, newID))
	if _, err := os.Stat(dst); err == nil {
		return "", 0, fmt.Errorf("%s exists already", dst)
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, out.Bytes(), 0o600); err != nil {
		return "", 0, err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return "", 0, err
	}
	return dst, removed, nil
}

// stripLine is one rollout line without encrypted items: nil for a line that
// is one, the line with them filtered out of a compaction's replacement
// history, else the line as it was.
func stripLine(line []byte) ([]byte, int, error) {
	var rec struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(line, &rec); err != nil {
		return nil, 0, errors.New("the rollout has a line that is not JSON")
	}
	switch rec.Type {
	case "response_item":
		var p struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(rec.Payload, &p)
		if strippedTypes[p.Type] {
			return nil, 1, nil
		}
	case "compacted":
		var whole map[string]json.RawMessage
		var payload map[string]json.RawMessage
		if json.Unmarshal(line, &whole) != nil || json.Unmarshal(rec.Payload, &payload) != nil {
			return line, 0, nil
		}
		var history []json.RawMessage
		if json.Unmarshal(payload["replacement_history"], &history) != nil {
			return line, 0, nil
		}
		kept := history[:0]
		n := 0
		for _, h := range history {
			var p struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(h, &p)
			if strippedTypes[p.Type] {
				n++
				continue
			}
			kept = append(kept, h)
		}
		if n == 0 {
			return line, 0, nil
		}
		payload["replacement_history"], _ = json.Marshal(kept)
		whole["payload"], _ = json.Marshal(payload)
		out, err := json.Marshal(whole)
		return out, n, err
	}
	return line, 0, nil
}

// newThreadID is a UUIDv7, the shape of codex's own thread ids.
func newThreadID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	var ms [8]byte
	binary.BigEndian.PutUint64(ms[:], uint64(time.Now().UnixMilli()))
	copy(b[:6], ms[2:])
	b[6] = b[6]&0x0f | 0x70
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
