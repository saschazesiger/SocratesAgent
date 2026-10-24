//go:build linux

package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// homeDigest is a session's digest as Home's poll hands it over.
func (c *chatEnv) homeDigest(t *testing.T, id string) map[string]any {
	t.Helper()
	res, body := c.do(t, c.client, "GET", "/api/home", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("home: %d", res.StatusCode)
	}
	d, _ := body["digests"].(map[string]any)[id].(map[string]any)
	return d
}

// waitDigest waits for a chat's digest on Home to satisfy cond.
func (c *chatEnv) waitDigest(t *testing.T, id, what string, cond func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		d := c.homeDigest(t, id)
		if d != nil && cond(d) {
			return d
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s: %v", what, d)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A chat's line and question on Home come from its journal: what it asks,
// with the ask's kind and options, and once answered what it said.
func TestAChatIsReadForHomeFromItsJournal(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	id := c.create(t)
	sub := c.dialEvents(t)
	c.send(t, id, "m1", "/askmany")
	d := c.waitDigest(t, id, "the question", func(d map[string]any) bool { return d["question"] != nil })
	q := d["question"].(map[string]any)
	items := q["items"].([]any)
	first := items[0].(map[string]any)
	if q["source"] != questionAsk || d["state"] != agent.ActivityWaiting || d["summary"] != first["text"] ||
		d["phase"] != digestReady || len(first["options"].([]any)) == 0 {
		t.Fatalf("the reading of an asking chat: %v", d)
	}
	// The stream told it too, before the activity that says it waits.
	asks, waits := -1, -1
	sub.await("the question and the waiting", 10*time.Second, func() bool {
		for i, f := range sub.ctrl {
			if f["t"] == "digest" && f["id"] == id && asks < 0 {
				if dg, _ := f["digest"].(map[string]any); dg != nil && dg["question"] != nil {
					asks = i
				}
			}
			if f["t"] != "activity" || waits >= 0 {
				continue
			}
			if all, _ := f["sessions"].(map[string]any); all != nil {
				if a, _ := all[id].(map[string]any); a != nil && a["state"] == "waiting" {
					waits = i
				}
			}
		}
		return asks >= 0 && waits >= 0
	})
	if asks > waits {
		t.Fatalf("the activity (frame %d) came before the question (frame %d)", waits, asks)
	}

	var ask string
	_ = c.srv.chats.chats[id].view(func(p *agent.Projection) { ask = p.OpenAsks()[0].ID })
	code, body := c.post(t, "/api/chats/"+id+"/asks/"+ask+"/answer", map[string]any{
		"client_id": "a1", "answer": map[string]any{"general_note": "go"}, "device": "test"})
	if code != http.StatusOK {
		t.Fatalf("answer: %d %v", code, body)
	}
	d = c.waitDigest(t, id, "the line after the answer", func(d map[string]any) bool {
		return d["question"] == nil && d["state"] == agent.ActivityIdle
	})
	if !strings.HasPrefix(d["summary"].(string), "you answered:") {
		t.Fatalf("the line after the answer: %v", d)
	}
}

// writeChatJournal gives a chat row a journal of turns turns, as a host
// would have written it, and no host.
func writeChatJournal(t testing.TB, st *store.Store, data, id string, turns int) {
	t.Helper()
	if err := st.CreateSession(&store.Session{ID: id, Title: "chat " + id, Harness: agent.FakeHarness,
		Workdir: data, WorkdirMode: "custom", Kind: store.KindChat, State: store.StateRunning}); err != nil {
		t.Fatal(err)
	}
	j, _, err := agent.OpenJournal(agenthost.JournalPath(data, id))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	words := strings.Repeat("All of it works now, and here is why. ", 12)
	for n := range turns {
		u, a, tr := fmt.Sprintf("u%d", n), fmt.Sprintf("a%d", n), fmt.Sprintf("t%d", n)
		if _, err := j.Append(
			agent.Event{Type: agent.TypeMessage, MessageID: u, Role: agent.RoleUser, State: agent.MessageDelivered,
				ClientID: "c" + u, Text: "please do step " + u},
			agent.Event{Type: agent.TypeTurnStarted, TurnID: tr, MessageID: u},
			agent.Event{Type: agent.TypeMessage, MessageID: a, Role: agent.RoleAssistant, State: agent.MessageStreaming, TurnID: tr},
			agent.Event{Type: agent.TypePart, MessageID: a, PartID: a + "r", Kind: agent.PartReasoning, State: agent.PartDone, Text: words},
			agent.Event{Type: agent.TypePart, MessageID: a, PartID: a + "b", Kind: agent.PartTool, State: agent.PartDone,
				Text: strings.Repeat("ok\n", 300), Content: raw(map[string]any{"tool": "Bash", "summary": "go test"})},
			agent.Event{Type: agent.TypePart, MessageID: a, PartID: a + "t", Kind: agent.PartText, State: agent.PartDone,
				Text: "Step " + u + " is done.\n\n" + words},
			agent.Event{Type: agent.TypeMessageState, MessageID: a, State: agent.MessageDone},
			agent.Event{Type: agent.TypeTurnEnded, TurnID: tr, Outcome: agent.OutcomeCompleted, CostUSD: 0.01,
				Usage: &agent.Usage{Input: 100, Output: 50}},
		); err != nil {
			t.Fatal(err)
		}
	}
}

// heapInUse is the live heap after a collection.
func heapInUse() uint64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapInuse
}

// A server with many chats starts from their briefs: the first start reads
// every journal once and keeps a brief of each, the next reads none, and
// neither keeps a history in memory that nothing uses (AGENT-HOST.md §10).
func TestManyChatsStartFromTheirBriefs(t *testing.T) {
	const chats, turns = 120, 40
	e := signedIn(t)
	data := e.srv.dataDir
	for i := range chats {
		writeChatJournal(t, e.store, data, fmt.Sprintf("%032x", i+1), turns)
	}
	before := heapInUse()
	start := func() (*Server, time.Duration) {
		srv, err := New(e.store, data)
		if err != nil {
			t.Fatal(err)
		}
		began := time.Now()
		srv.chats.start()
		took := time.Since(began)
		t.Cleanup(func() { srv.chats.close(); srv.push.stop() })
		return srv, took
	}
	first, cold := start()
	if n := first.chats.loaded(); n != 0 {
		t.Fatalf("%d chats keep their history after the first start", n)
	}
	first.chats.close()
	second, warm := start()
	held := heapInUse()
	if n := second.chats.loaded(); n != 0 {
		t.Fatalf("%d chats keep their history after a start from briefs", n)
	}
	if n := second.chats.folds.Load(); n != 0 {
		t.Fatalf("a start from briefs read %d journals", n)
	}
	t.Logf("%d chats of %d turns: the first start %s, from the briefs %s; heap %d KB before, %d KB after",
		chats, turns, cold.Round(time.Millisecond), warm.Round(time.Millisecond), before>>10, held>>10)

	// What a brief says is what the journal says.
	id := fmt.Sprintf("%032x", 7)
	b, ok := second.chats.briefOfChat(id)
	if !ok || b.Summary.Activity != agent.ActivityIdle || b.Line != "Step u39 is done." || b.Seq != turns*8 {
		t.Fatalf("brief %+v", b)
	}
	readings := second.chats.readings()
	if r := readings[id]; r.Summary != "Step u39 is done." || r.State != agent.ActivityIdle || len(readings) != chats {
		t.Fatalf("reading %+v of %d", r, len(readings))
	}
}

// A chat whose history is not in memory - any chat after a restart, or one
// nobody has used for a while - is pointed to another folder by its brief,
// and stays usable: the look at whether a turn runs reads no history the
// server does not hold.
func TestAChatWithoutItsHistoryIsPointedElsewhere(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	id := fmt.Sprintf("%032x", 98)
	writeChatJournal(t, c.store, c.data, id, 2)
	c.restart(t, 0, nil)
	if n := c.srv.chats.loaded(); n != 0 {
		t.Fatalf("%d chats in memory after the start", n)
	}
	here := t.TempDir()
	raw, _ := json.Marshal(map[string]string{"workdir": here})
	res, body := c.do(t, c.client, "PUT", "/api/sessions/"+id+"/workdir", string(raw))
	if session, _ := body["session"].(map[string]any); res.StatusCode != http.StatusOK || session["workdir"] != here {
		t.Fatalf("pointed to %s: %d %v", here, res.StatusCode, body)
	}
	if res, body := c.do(t, c.client, "GET", "/api/chats/"+id, ""); res.StatusCode != http.StatusOK || body["chat"] == nil {
		t.Fatalf("the chat after it was pointed elsewhere: %d %v", res.StatusCode, body)
	}
}

// A chat's history is read when it is wanted and let go when nobody has
// wanted it for a while; a journal that moved on while there was no server
// is read again rather than believed from its brief.
func TestAChatsHistoryComesAndGoes(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	id := fmt.Sprintf("%032x", 99)
	writeChatJournal(t, c.store, c.data, id, 3)
	c.restart(t, 0, nil)
	if n := c.srv.chats.loaded(); n != 0 {
		t.Fatalf("%d chats in memory after the start", n)
	}
	res, body := c.do(t, c.client, "GET", "/api/chats/"+id+"?history=1", "")
	if res.StatusCode != http.StatusOK || body["messages"] == nil || c.srv.chats.loaded() != 1 {
		t.Fatalf("history: %d, %d in memory", res.StatusCode, c.srv.chats.loaded())
	}
	c.srv.chats.unloadAfter = 0
	c.srv.chats.unloadIdle(time.Now())
	if n := c.srv.chats.loaded(); n != 0 {
		t.Fatalf("%d chats in memory after the janitor", n)
	}
	// Something the chat said while no server ran.
	j, _, err := agent.OpenJournal(agenthost.JournalPath(c.data, id))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append(agent.Event{Type: agent.TypeNotice, Code: "late", Text: "Said late."}); err != nil {
		t.Fatal(err)
	}
	j.Close()
	c.restart(t, 0, nil)
	if s := c.srv.chats.chats[id].summary(); s.LastSeq != 3*8+1 {
		t.Fatalf("the brief was believed over the journal: %+v", s)
	}
	if _, err := os.Stat(briefPath(c.data, id)); err != nil {
		t.Fatal(err)
	}
}
