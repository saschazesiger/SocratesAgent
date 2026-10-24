//go:build linux

package server

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agent/claude"
	_ "github.com/saschazesiger/SocratesAgent/internal/agent/codex"
	_ "github.com/saschazesiger/SocratesAgent/internal/agent/opencode"
	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// The three agents' adapters are in this test binary - which is also every
// host it launches - with their scripted CLIs (e2e/fakebin) on the PATH.
// Like the browser suite, the binary keeps the agent terminals for the sheet
// and PreStart (AgentTerminalsEnv, set in TestMain), so that the tests
// written for Claude Code, Codex and OpenCode terminals still make terminals.

var (
	fakeAgentsOnce sync.Once
	fakeAgentsDir  string
	fakeAgentsErr  error
)

// withFakeAgents puts fakeclaude, fakecodex and fakeopencode on the PATH as
// claude, codex and opencode, built once for the whole run.
func withFakeAgents(t *testing.T) {
	t.Helper()
	fakeAgentsOnce.Do(func() {
		fakeAgentsDir, fakeAgentsErr = os.MkdirTemp("", "socrates-server-agents-")
		if fakeAgentsErr != nil {
			return
		}
		_, file, _, _ := runtime.Caller(0)
		root := filepath.Join(filepath.Dir(file), "..", "..")
		for name, pkg := range map[string]string{"claude": "fakeclaude", "codex": "fakecodex", "opencode": "fakeopencode"} {
			build := exec.Command("go", "build", "-o", filepath.Join(fakeAgentsDir, name), "./e2e/fakebin/"+pkg)
			build.Dir = root
			if out, err := build.CombinedOutput(); err != nil {
				fakeAgentsErr = err
				t.Logf("build %s: %s", pkg, out)
				return
			}
		}
	})
	if fakeAgentsErr != nil {
		t.Fatal(fakeAgentsErr)
	}
	t.Setenv("PATH", fakeAgentsDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	// fakecodex keeps its rollouts where CODEX_HOME says, and wants it said.
	t.Setenv("CODEX_HOME", filepath.Join(t.TempDir(), "codex"))
}

// createAgentChat makes a chat of one of the three agents in a folder of its
// own.
func (c *chatEnv) createAgentChat(t *testing.T, harness string, extra map[string]any) string {
	t.Helper()
	work := filepath.Join(c.data, "work-"+harness)
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"client_id": "create-" + harness + "-" + t.Name() + "-" + randomTestID(), "harness": harness,
		"workdir_mode": "custom", "workdir": work}
	for k, v := range extra {
		body[k] = v
	}
	code, made := c.post(t, "/api/chats", body)
	if code != 201 {
		t.Fatalf("create a %s chat: %d %v", harness, code, made)
	}
	return made["session"].(map[string]any)["id"].(string)
}

func randomTestID() string { return termux.NewID() }

// lastSaid is the newest finished text the agent wrote.
func lastSaid(events []agent.Event) string {
	for i := len(events) - 1; i >= 0; i-- {
		if ev := events[i]; ev.Type == agent.TypePart && ev.Kind == agent.PartText && ev.State == agent.PartDone &&
			ev.ParentPartID == "" {
			return ev.Text
		}
	}
	return ""
}

// The host's word for ultracode is the Claude adapter's.
func TestTheUltracodeVariableIsTheAdapters(t *testing.T) {
	if agenthost.UltracodeEnv != claude.UltracodeEnv {
		t.Fatalf("%s and %s", agenthost.UltracodeEnv, claude.UltracodeEnv)
	}
}

// Ultracode reaches a Claude chat's CLI when the chat asked for it, and a
// change from the chat's ⋮ reaches it from the next message on.
func TestUltracodeReachesAClaudeChat(t *testing.T) {
	withFakeAgents(t)
	c := newChatEnv(t, time.Hour)
	on := c.createAgentChat(t, "claude", map[string]any{"ultracode": true})
	off := c.createAgentChat(t, "claude", nil)
	argv := func(id, client string, turns int) string {
		t.Helper()
		c.send(t, id, client, "/argv")
		return lastSaid(c.waitJournal(t, id, 20*time.Second, "the argv", turnsEnded(turns)))
	}
	if got := argv(on, "a1", 1); !strings.Contains(got, `"ultracode":true`) {
		t.Fatalf("with ultracode: %s", got)
	}
	if got := argv(off, "b1", 1); !strings.Contains(got, `"ultracode":false`) {
		t.Fatalf("without: %s", got)
	}
	code, body := c.post(t, "/api/chats/"+off+"/options", map[string]any{"ultracode": true})
	if code != 200 || body["session"].(map[string]any)["options"].(map[string]any)["ultracode"] != true {
		t.Fatalf("options: %d %v", code, body)
	}
	c.waitJournal(t, off, 20*time.Second, "the park", func(evs []agent.Event) bool {
		return count(evs, func(ev agent.Event) bool { return ev.Type == agent.TypeProcess && ev.State == agent.ProcessParked }) > 0
	})
	if got := argv(off, "b2", 2); !strings.Contains(got, `"ultracode":true`) {
		t.Fatalf("after the change: %s", got)
	}
	// Ultracode is Claude Code's alone.
	codex := c.createAgentChat(t, "codex", nil)
	if code, _ := c.post(t, "/api/chats/"+codex+"/options", map[string]any{"ultracode": true}); code != 400 {
		t.Fatalf("ultracode on a codex chat: %d", code)
	}
}

// A chat whose whole folder is lost gets its history back from the CLI's own
// transcript when it is opened, for each of the three agents: the server
// writes the row's conversation into host.json, and the host rebuilds the
// journal before the conversation goes on. The turns it gets back are not
// news: no push, no unread mark, until a new turn ends.
func TestALostChatFolderIsRebuiltFromTheTranscript(t *testing.T) {
	withFakeAgents(t)
	for _, harness := range []string{"claude", "codex", "opencode"} {
		t.Run(harness, func(t *testing.T) {
			c := newChatEnv(t, time.Hour)
			id := c.createAgentChat(t, harness, nil)
			c.send(t, id, "m1", "remember the first words")
			c.waitJournal(t, id, 30*time.Second, "the first turn", turnsEnded(1))
			deadline := time.Now().Add(10 * time.Second)
			for row, _ := c.store.GetSession(id); row.CLISessionID == ""; row, _ = c.store.GetSession(id) {
				if time.Now().After(deadline) {
					t.Fatal("the conversation was never recorded")
				}
				time.Sleep(20 * time.Millisecond)
			}
			c.restart(t, 0, func() {
				c.stopHosts(t)
				if err := os.RemoveAll(agenthost.ChatDir(c.data, id)); err != nil {
					t.Fatal(err)
				}
			})
			if s := c.srv.chats.chats[id].summary(); s.LastSeq != 0 {
				t.Fatalf("the lost folder still has events: %+v", s)
			}
			// Its account is at a limit now, which a turn the rebuild gets
			// back - completed long ago - says nothing of.
			row, err := c.store.GetSession(id)
			if err != nil {
				t.Fatal(err)
			}
			acct := readChatOptions(row.Options).Account
			until := time.Now().Add(time.Hour).UnixMilli()
			if err := c.store.SetAccountLimitedUntil(acct, until); err != nil {
				t.Fatal(err)
			}
			sent := catch(c.srv.push)
			if err := c.srv.push.add(pushSub{Subscription: testSubscription("https://push.example.net/a")}); err != nil {
				t.Fatal(err)
			}
			// Opening the chat brings its history back, with nothing sent.
			st := c.stream(t, id, 0)
			defer st.conn.CloseNow()
			events := c.waitJournal(t, id, 30*time.Second, "the rebuilt history", func(evs []agent.Event) bool {
				return count(evs, func(ev agent.Event) bool {
					return ev.Type == agent.TypeNotice && (ev.Code == agent.NoticeRebuilt || ev.Code == agent.NoticeHistoryUnavailable)
				}) > 0
			})
			rebuilt := count(events, func(ev agent.Event) bool { return ev.Type == agent.TypeNotice && ev.Code == agent.NoticeRebuilt })
			users := 0
			for _, ev := range events {
				if ev.Type == agent.TypeMessage && ev.Role == agent.RoleUser && strings.Contains(ev.Text, "remember the first words") {
					users++
				}
			}
			if rebuilt != 1 || users != 1 {
				t.Fatalf("rebuilt %d, the first message %d times:\n%v", rebuilt, users, events)
			}
			last := events[len(events)-1].Seq
			for deadline := time.Now().Add(10 * time.Second); c.srv.chats.chats[id].summary().LastSeq < last; {
				if time.Now().After(deadline) {
					t.Fatal("the server never read the rebuilt history")
				}
				time.Sleep(20 * time.Millisecond)
			}
			select {
			case s := <-sent:
				t.Fatalf("the rebuilt history was pushed: %+v", s.note)
			case <-time.After(700 * time.Millisecond):
			}
			if a, err := c.store.GetAccount(acct); err != nil || a.LimitedUntil != until {
				t.Fatalf("the rebuilt history moved the account's limit: %+v %v", a, err)
			}
			if a := c.srv.manager.ActivityOf(id); a.Unread || a.State != termux.StateIdle {
				t.Fatalf("after the rebuild the chat is %+v", a)
			}
			// And the conversation goes on where it was.
			c.send(t, id, "m2", "and now?")
			after := c.waitJournal(t, id, 30*time.Second, "the next turn", func(evs []agent.Event) bool {
				return count(evs, func(ev agent.Event) bool { return ev.Type == agent.TypeTurnEnded }) >= 2
			})
			if lastSaid(after) == "" {
				t.Fatalf("the agent said nothing after the rebuild:\n%v", after)
			}
			if done := pushOfKind(t, sent, pushFinished, 10*time.Second); !strings.Contains(done.Body, "and now?") {
				t.Fatalf("the push of the new turn: %+v", done)
			}
		})
	}
}

// A prepared chat's scheduled start ("Start automatically") sends its draft
// as the first message, once, and says so on the phone; a prepared Claude
// chat keeps the Ultracode it was prepared with, and another agent does not.
func TestAPreparedChatStartsByTheClockWithItsDraft(t *testing.T) {
	withFakeAgents(t)
	t.Setenv(AgentTerminalsEnv, "")
	c := newChatEnv(t, time.Hour)
	sent := catch(c.srv.push)
	if err := c.srv.push.add(pushSub{Subscription: testSubscription("https://push.example.net/a")}); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(c.data, "work-prepared")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(time.Hour).UnixMilli()
	code, body := c.post(t, "/api/sessions", map[string]any{"client_id": "prep-claude", "prestart": true, "harness": "claude",
		"workdir_mode": "custom", "workdir": work, "prompt": "/argv", "start_at": at, "ultracode": true})
	if code != 201 {
		t.Fatalf("prepare: %d %v", code, body)
	}
	row := body["session"].(map[string]any)
	id := row["id"].(string)
	if row["kind"] != "chat" || row["options"].(map[string]any)["ultracode"] != true {
		t.Fatalf("prepared %v", row)
	}
	// Switched to Codex, it would lose Ultracode; back to Claude Code with it
	// asked for again, it has it.
	if res, made := c.do(t, c.client, "PATCH", "/api/sessions/"+id+"/prestart", `{"harness":"codex"}`); res.StatusCode != 200 ||
		made["session"].(map[string]any)["options"].(map[string]any)["ultracode"] != nil {
		t.Fatalf("to codex: %d %v", res.StatusCode, made)
	}
	if res, made := c.do(t, c.client, "PATCH", "/api/sessions/"+id+"/prestart", `{"harness":"claude","ultracode":true}`); res.StatusCode != 200 ||
		made["session"].(map[string]any)["options"].(map[string]any)["ultracode"] != true {
		t.Fatalf("back to claude: %d %v", res.StatusCode, made)
	}
	if n := c.srv.sched.runDue(context.Background(), time.UnixMilli(at+1)); n != 1 {
		t.Fatalf("the scheduler started %d sessions", n)
	}
	started := pushOfKind(t, sent, pushStarted, 10*time.Second)
	if !strings.HasPrefix(started.Title, "Started: ") || started.ID != id {
		t.Fatalf("the announcement %+v", started)
	}
	events := c.waitJournal(t, id, 20*time.Second, "the first turn", turnsEnded(1))
	users := count(events, func(ev agent.Event) bool { return ev.Type == agent.TypeMessage && ev.Role == agent.RoleUser })
	if users != 1 || !strings.Contains(lastSaid(events), `"ultracode":true`) {
		t.Fatalf("%d user messages, the agent said %q", users, lastSaid(events))
	}
	// A second run of the clock starts nothing again.
	if n := c.srv.sched.runDue(context.Background(), time.UnixMilli(at+2)); n != 0 {
		t.Fatalf("started again: %d", n)
	}
}
