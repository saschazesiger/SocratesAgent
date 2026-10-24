//go:build linux

package claude

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/paths"
	"github.com/saschazesiger/SocratesAgent/internal/vault"
)

// The test binary stands in for `socrates agent-host` (TestMain), so the
// adapter runs inside a real host process here.
func init() {
	if len(os.Args) > 1 && os.Args[1] == "agent-host" {
		runtime.LockOSThread()
	}
}

type hostRun struct {
	t      *testing.T
	c      *agenthost.Conn
	events []agent.Event
}

func (h *hostRun) until(what string, match func(agent.Event) bool) agent.Event {
	h.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		f, err := h.c.Next()
		if err != nil {
			h.t.Fatalf("waiting for %s: %v", what, err)
		}
		if f.Fail != nil {
			h.t.Fatalf("refused while waiting for %s: %+v", what, f.Fail)
		}
		if f.Event == nil {
			continue
		}
		h.events = append(h.events, *f.Event)
		if match(*f.Event) {
			return *f.Event
		}
	}
	h.t.Fatalf("timed out waiting for %s", what)
	return agent.Event{}
}

func (h *hostRun) reply(clientID, text string) string {
	h.t.Helper()
	if err := h.c.Send(agenthost.Command{Op: agenthost.OpSend, ClientID: clientID, Text: text}); err != nil {
		h.t.Fatal(err)
	}
	h.until("the turn of "+clientID, func(ev agent.Event) bool { return ev.Type == agent.TypeTurnEnded })
	m := lastAssistant(agent.Fold(h.events))
	if m == nil {
		return ""
	}
	var texts []string
	for _, p := range partsOf(m, agent.PartText) {
		texts = append(texts, p.Text)
	}
	return strings.Join(texts, "")
}

// A chat moves A -> B -> A through a real host: the conversation id stays, the
// divider is journaled each time, and each CLI runs on its account's config
// dir, an API-key account's key as ANTHROPIC_API_KEY.
func TestASwitchThroughTheHostKeepsTheConversation(t *testing.T) {
	data := t.TempDir()
	work := filepath.Join(data, "work")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	key, err := vault.LoadOrCreateKeyFile(paths.New(data).SecretsKey())
	if err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(data, "agents", "claude")
	dirA, dirB := filepath.Join(data, "accounts", "a-a"), filepath.Join(data, "accounts", "a-b")
	for _, dir := range []string{dirA, dirB} {
		if err := PrepareConfigDir(dir, shared); err != nil {
			t.Fatal(err)
		}
	}
	base := []string{"PATH=" + filepath.Dir(fakeBin) + ":/usr/bin:/bin", "HOME=" + data}
	chat := "claudeswitch"
	l := &agenthost.Launcher{DataDir: data, Binary: os.Args[0], Logf: t.Logf}
	t.Cleanup(func() { l.Stop(context.Background(), chat) })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := l.Launch(ctx, agenthost.Spec{ChatID: chat, Harness: Harness, Workdir: work, Account: "a-a",
		Env: append(append([]string{}, base...), "CLAUDE_CONFIG_DIR="+dirA), ParkAfterMS: 60000}); err != nil {
		t.Fatalf("launch: %v", err)
	}
	c, err := agenthost.Dial(context.Background(), agenthost.SocketPath(data, chat), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	h := &hostRun{t: t, c: c}
	h.reply("m1", "remember 4711")
	conv := agent.Fold(h.events).ConversationID
	if conv == "" {
		t.Fatal("no conversation")
	}
	sealed, err := agenthost.SealEnv(key, chat, []string{APIKeyEnv + "=sk-b-key"})
	if err != nil {
		t.Fatal(err)
	}
	switchTo := func(clientID, account, dir, secret string) {
		t.Helper()
		if err := c.Send(agenthost.Command{Op: agenthost.OpSwitchAccount, ClientID: clientID, Account: account,
			Env: append(append([]string{}, base...), "CLAUDE_CONFIG_DIR="+dir), SecretEnv: secret}); err != nil {
			t.Fatal(err)
		}
		h.until("the divider to "+account, func(ev agent.Event) bool {
			return ev.Type == agent.TypeAccountSwitched && ev.To == account
		})
	}
	switchTo("s1", "a-b", dirB, sealed)
	env := h.reply("m2", "/env")
	if !strings.Contains(env, "CLAUDE_CONFIG_DIR="+dirB) || !strings.Contains(env, "ANTHROPIC_API_KEY=sk-b-key") ||
		strings.Contains(env, APIKeyEnv) {
		t.Fatalf("on B the CLI had:\n%s", env)
	}
	switchTo("s2", "a-a", dirA, "")
	env = h.reply("m3", "/env")
	if !strings.Contains(env, "CLAUDE_CONFIG_DIR="+dirA) || strings.Contains(env, "ANTHROPIC_API_KEY") {
		t.Fatalf("back on A the CLI had:\n%s", env)
	}
	p := agent.Fold(h.events)
	if p.ConversationID != conv {
		t.Fatalf("the conversation changed: %s -> %s", conv, p.ConversationID)
	}
	var accounts []string
	for _, ev := range h.events {
		if ev.Type == agent.TypeTurnStarted {
			accounts = append(accounts, ev.Account)
		}
	}
	if strings.Join(accounts, ",") != "a-a,a-b,a-a" {
		t.Fatalf("the turns ran on %v", accounts)
	}
	// One transcript, in the shared folder, under both accounts.
	matches, _ := filepath.Glob(filepath.Join(shared, "projects", "*", conv+".jsonl"))
	if len(matches) != 1 {
		t.Fatalf("transcripts %v", matches)
	}
	for _, name := range []string{"host.json", "events.jsonl", "host.log"} {
		raw, _ := os.ReadFile(filepath.Join(agenthost.ChatDir(data, chat), name))
		if name != "events.jsonl" && strings.Contains(string(raw), "sk-b-key") {
			t.Fatalf("%s holds the key", name)
		}
	}
}
