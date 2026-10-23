//go:build !windows

package claude

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// The live tests run the real claude binary. They cost money and need a
// signed-in config directory, so they run only with
//
//	SOCRATES_CLAUDE_LIVE=1 SOCRATES_CLAUDE_LIVE_CONFIG=<CLAUDE_CONFIG_DIR> [SOCRATES_CLAUDE_LIVE_DIR=<work root>]
//
// and use the cheapest model.
func liveEnv(t *testing.T) env {
	t.Helper()
	if os.Getenv("SOCRATES_CLAUDE_LIVE") != "1" {
		t.Skip("set SOCRATES_CLAUDE_LIVE=1 and SOCRATES_CLAUDE_LIVE_CONFIG to run against the real claude")
	}
	conf := os.Getenv("SOCRATES_CLAUDE_LIVE_CONFIG")
	if conf == "" {
		t.Fatal("SOCRATES_CLAUDE_LIVE_CONFIG is not set")
	}
	root := os.Getenv("SOCRATES_CLAUDE_LIVE_DIR")
	if root == "" {
		root = t.TempDir()
	}
	work, err := os.MkdirTemp(root, "live-")
	if err != nil {
		t.Fatal(err)
	}
	work, _ = filepath.EvalSymlinks(work)
	home, _ := os.UserHomeDir()
	return env{conf: conf, work: work, state: filepath.Join(work, ".state"),
		vars: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "CLAUDE_CONFIG_DIR=" + conf, "ANTHROPIC_API_KEY=ambient"}}
}

func liveStart(t *testing.T, e env, resume string) (*Adapter, *rec) {
	t.Helper()
	a := New(Options{})
	r := newRec()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	spec := e.spec(resume)
	spec.Effort = ""
	if err := a.Start(ctx, spec, r); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = a.Stop() })
	return a, r
}

func liveWait(t *testing.T, r *rec, n int, within time.Duration) []agent.Event {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		evs := append([]agent.Event(nil), r.events...)
		r.mu.Unlock()
		if count(evs, agent.TypeTurnEnded) >= n {
			return evs
		}
		time.Sleep(200 * time.Millisecond)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	t.Fatalf("timed out after %v waiting for %d turns:\n%s", within, n, dump(r.events))
	return nil
}

func TestLiveTurnToolParkResume(t *testing.T) {
	e := liveEnv(t)
	a, r := liveStart(t, e, "")
	send(t, a, "m1", "Run the Bash command `echo live-ok` once, then reply with the single word DONE.")
	evs := liveWait(t, r, 1, 3*time.Minute)
	var cost float64
	for _, ev := range evs {
		if ev.Type == agent.TypeTurnEnded {
			cost = ev.CostUSD
			if ev.Outcome != agent.OutcomeCompleted {
				t.Fatalf("outcome %q: %s", ev.Outcome, ev.Text)
			}
		}
	}
	var conv string
	r.view(func(p *agent.Projection) {
		conv = p.ConversationID
		m := lastAssistant(p)
		tools := partsOf(m, agent.PartTool)
		found := false
		for _, tp := range tools {
			if strings.Contains(tp.Text, "live-ok") && tp.State == agent.PartDone {
				found = true
			}
		}
		if !found {
			t.Fatalf("no Bash part with the output:\n%s", dump(evs))
		}
		if len(partsOf(m, agent.PartText)) == 0 {
			t.Fatal("no text")
		}
		t.Logf("account info: %s", p.Agent)
		for _, rp := range partsOf(m, agent.PartReasoning) {
			t.Logf("reasoning part: %q %s", rp.Text, rp.Content)
		}
	})
	t.Logf("turn cost $%.4f, conversation %s", cost, conv)
	if err := a.Park(context.Background()); err != nil {
		t.Fatal(err)
	}
	b, r2 := liveStart(t, e, conv)
	send(t, b, "m2", "What word did the command print? Reply with that word only.")
	evs = liveWait(t, r2, 1, 3*time.Minute)
	var cost2 float64
	for _, ev := range evs {
		if ev.Type == agent.TypeTurnEnded {
			cost2 = ev.CostUSD
		}
	}
	// total_cost_usd goes on from the first turn's; the resumed turn's cost
	// must be only its own: the two add up to the transcript's total.
	if err := b.Park(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, total := transcriptState(findTranscript(e.conf, conv))
	t.Logf("resumed turn cost $%.5f, conversation total $%.5f", cost2, total)
	if d := total - cost - cost2; cost2 <= 0 || d > 1e-6 || d < -1e-6 {
		t.Fatalf("the resumed turn was booked $%.5f after a first turn of $%.5f; total $%.5f", cost2, cost, total)
	}
	r2.view(func(p *agent.Projection) {
		text := partsOf(lastAssistant(p), agent.PartText)
		if len(text) == 0 || !strings.Contains(text[len(text)-1].Text, "live-ok") {
			t.Fatalf("the resumed conversation forgot:\n%s", dump(evs))
		}
	})
	bad := New(Options{})
	err := bad.Start(context.Background(), e.spec("00000000-0000-4000-8000-0000000000aa"), newRec())
	if !errors.Is(err, agent.ErrConversationNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
}

func TestLivePlanModeAndASubagent(t *testing.T) {
	e := liveEnv(t)
	a, r := liveStart(t, e, "")
	if err := a.Command(context.Background(), "plan", ""); err != nil {
		t.Fatal(err)
	}
	send(t, a, "m1", "Plan (do not execute) creating hi.txt with the word hi, in two lines, then call ExitPlanMode.")
	deadline := time.Now().Add(3 * time.Minute)
	var ask agent.Event
	for time.Now().Before(deadline) && ask.AskID == "" {
		r.mu.Lock()
		for _, ev := range r.events {
			if ev.Type == agent.TypeAsk {
				ask = ev
			}
		}
		r.mu.Unlock()
		time.Sleep(200 * time.Millisecond)
	}
	if ask.AskID == "" {
		t.Fatal("no plan card")
	}
	if err := a.Answer(context.Background(), ask.AskID, json.RawMessage(`{"decision":"approve"}`)); err != nil {
		t.Fatal(err)
	}
	liveWait(t, r, 1, 3*time.Minute)
	if _, err := os.Stat(filepath.Join(e.work, "hi.txt")); err != nil {
		t.Fatalf("the approved plan was not carried out: %v", err)
	}
	send(t, a, "m2", "Use the Agent tool (subagent_type general-purpose, model haiku) with the prompt "+
		"'Run the Bash command `echo sub-live` and reply with its output only.', wait for it, then reply DONE.")
	evs := liveWait(t, r, 2, 4*time.Minute)
	deadline = time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) && a.Background() > 0 {
		time.Sleep(200 * time.Millisecond)
	}
	r.view(func(p *agent.Projection) {
		for _, m := range p.Messages {
			for _, g := range partsOf(m, agent.PartTool) {
				c := content(t, g)
				if agentTool(c.Tool) && len(g.Children) > 0 {
					t.Logf("subagent %+v with %d nested parts", c.Agent, len(g.Children))
					return
				}
			}
		}
		t.Fatalf("no subagent group with nested parts:\n%s", dump(evs))
	})
}
