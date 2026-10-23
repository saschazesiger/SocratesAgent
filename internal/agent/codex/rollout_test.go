package codex

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// realRollout is a codex 0.164 rollout cut down to the lines that matter:
// the meta, a user message, reasoning with encrypted content, an answer, and
// a compaction whose replacement history carries reasoning too.
const realRollout = `{"timestamp":"2026-10-23T11:26:13.253Z","ordinal":0,"type":"session_meta","payload":{"session_id":"01a16882-ecec-7a83-a9a8-df12d28b4c19","id":"01a16882-ecec-7a83-a9a8-df12d28b4c19","cwd":"/w"}}
{"timestamp":"2026-10-23T11:26:13.272Z","ordinal":1,"type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}
{"timestamp":"2026-10-23T11:26:13.295Z","ordinal":11,"type":"response_item","payload":{"type":"message","id":"msg_u","role":"user","content":[{"type":"input_text","text":"SAY first"}]}}
{"timestamp":"2026-10-23T11:26:13.315Z","ordinal":14,"type":"response_item","payload":{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"**Thinking** about pong"}],"content":null,"encrypted_content":"gAAAA-mock"}}
{"timestamp":"2026-10-23T11:26:13.316Z","ordinal":15,"type":"event_msg","payload":{"type":"item_completed","thread_id":"01a16882-ecec-7a83-a9a8-df12d28b4c19","turn_id":"t1","item":{"type":"AgentMessage","id":"msg_1","content":[{"type":"Text","text":"pong"}]}}}
{"timestamp":"2026-10-23T11:26:13.317Z","ordinal":16,"type":"response_item","payload":{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"pong","annotations":[]}]}}
{"timestamp":"2026-10-23T11:26:14.000Z","ordinal":17,"type":"response_item","payload":{"type":"compaction","encrypted_content":"gAAAA-compact"}}
{"timestamp":"2026-10-23T11:26:14.001Z","ordinal":18,"type":"compacted","payload":{"message":"summary","replacement_history":[{"type":"message","role":"user","content":[{"type":"input_text","text":"SAY first"}]},{"type":"reasoning","id":"rs_2","encrypted_content":"gAAAA-2"}]}}
{"timestamp":"2026-10-23T11:26:18.539Z","ordinal":35,"type":"event_msg","payload":{"type":"task_complete","turn_id":"t1"}}
`

func TestStripRolloutCopiesWithoutEncryptedItems(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions", "2026", "10", "23")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := "01a16882-ecec-7a83-a9a8-df12d28b4c19"
	src := filepath.Join(dir, "rollout-2026-10-23T11-26-13-"+old+".jsonl")
	if err := os.WriteFile(src, []byte(realRollout), 0o600); err != nil {
		t.Fatal(err)
	}
	id := newThreadID()
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(id) {
		t.Fatalf("not a UUIDv7: %s", id)
	}
	dst, removed, err := StripRollout(src, old, id, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "rollout-2026-10-23T11-26-13-"+id+".jsonl"); dst != want {
		t.Fatalf("copy at %s, want %s", dst, want)
	}
	if removed != 3 {
		t.Fatalf("removed %d items, want 3", removed)
	}
	raw, _ := os.ReadFile(dst)
	got := string(raw)
	if strings.Contains(got, "encrypted_content") || strings.Contains(got, old) {
		t.Fatalf("the copy still has encrypted content or the old id:\n%s", got)
	}
	for _, keep := range []string{`"SAY first"`, `"pong"`, `"replacement_history"`, `"session_id":"` + id, "task_complete"} {
		if !strings.Contains(got, keep) {
			t.Fatalf("the copy lost %s:\n%s", keep, got)
		}
	}
	if orig, _ := os.ReadFile(src); string(orig) != realRollout {
		t.Fatal("the original was changed")
	}
	if _, _, err := StripRollout(src, old, id, ""); err == nil {
		t.Fatal("a second copy under the same id overwrote the first")
	}
	// Cut before the refused turn t2: nothing of it reaches the copy.
	refused := `{"timestamp":"2026-10-23T11:30:00.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"t2"}}
{"timestamp":"2026-10-23T11:30:00.001Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"SAY two"}]}}
`
	if err := os.WriteFile(src, []byte(realRollout+refused), 0o600); err != nil {
		t.Fatal(err)
	}
	cut, _, err := StripRollout(src, old, newThreadID(), "t2")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(cut)
	if strings.Contains(string(raw), "SAY two") || !strings.Contains(string(raw), "task_complete") {
		t.Fatalf("the cut copy:\n%s", raw)
	}
	if _, _, err := StripRollout(src, "another-id", newThreadID(), ""); err == nil {
		t.Fatal("a rollout of another thread was copied")
	}
}

func TestPrepareHomeLinksTheSharedFolders(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "agents", "codex")
	home := filepath.Join(root, "accounts", "a")
	// An account that already had its own sessions, from before sharing.
	if err := os.MkdirAll(filepath.Join(home, "sessions", "2026", "10"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "sessions", "2026", "10", "r.jsonl"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nowhere", filepath.Join(home, "archived_sessions")); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := PrepareHome(home, shared); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range SharedDirs {
		dest, err := os.Readlink(filepath.Join(home, name))
		if err != nil || dest != filepath.Join("..", "..", "agents", "codex", name) {
			t.Fatalf("%s links to %q (%v)", name, dest, err)
		}
	}
	if raw, err := os.ReadFile(filepath.Join(shared, "sessions", "2026", "10", "r.jsonl")); err != nil || string(raw) != "x" {
		t.Fatalf("the account's rollout was not moved: %v", err)
	}
	if raw, err := os.ReadFile(filepath.Join(home, "sessions", "2026", "10", "r.jsonl")); err != nil || string(raw) != "x" {
		t.Fatalf("the rollout is not reachable through the link: %v", err)
	}
}
