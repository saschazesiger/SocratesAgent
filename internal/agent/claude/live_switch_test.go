//go:build !windows

package claude

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// liveSecond is the config directory of a second signed-in account
// (SOCRATES_CLAUDE_LIVE_CONFIG2), for the live tests of an account switch.
func liveSecond(t *testing.T) string {
	t.Helper()
	conf := os.Getenv("SOCRATES_CLAUDE_LIVE_CONFIG2")
	if conf == "" {
		t.Skip("set SOCRATES_CLAUDE_LIVE_CONFIG2 to a second account's CLAUDE_CONFIG_DIR")
	}
	return conf
}

// liveOn starts the adapter on the account of config directory conf, with
// the model SOCRATES_CLAUDE_LIVE_MODEL (haiku when unset).
func liveOn(t *testing.T, e env, conf, resume string) (*Adapter, *rec) {
	t.Helper()
	model := os.Getenv("SOCRATES_CLAUDE_LIVE_MODEL")
	if model == "" {
		model = "haiku"
	}
	home, _ := os.UserHomeDir()
	spec := agent.StartSpec{ChatID: "live", Workdir: e.work, StateDir: e.state, Model: model, ResumeID: resume,
		Env: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "CLAUDE_CONFIG_DIR=" + conf}}
	a := New(Options{})
	r := newRec()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := a.Start(ctx, spec, r); err != nil {
		t.Fatalf("start on %s: %v", conf, err)
	}
	t.Cleanup(func() { _ = a.Stop() })
	return a, r
}

// lastUsage is the usage of the newest turn.ended.
func lastUsage(t *testing.T, evs []agent.Event) agent.Usage {
	t.Helper()
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Type == agent.TypeTurnEnded {
			if evs[i].Outcome != agent.OutcomeCompleted {
				t.Fatalf("turn %s: %s", evs[i].Outcome, evs[i].Text)
			}
			if evs[i].Usage == nil {
				t.Fatal("the turn has no usage")
			}
			return *evs[i].Usage
		}
	}
	t.Fatal("no turn ended")
	return agent.Usage{}
}

// A switch to another account of the same organization resumes the
// conversation on a new process; the prompt cache the first account wrote
// is read by the second, so the switch costs no context rewrite. It needs
// two accounts of one organization.
func TestLiveASwitchKeepsThePromptCache(t *testing.T) {
	e := liveEnv(t)
	second := liveSecond(t)
	// Context worth caching: the adapter's own sources, read by the model.
	src, _ := filepath.Glob("*.go")
	for _, f := range src {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(e.work, filepath.Base(f)+".txt"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	a, r := liveOn(t, e, e.conf, "")
	send(t, a, "m1", "Read claude.go.txt, state.go.txt and tools.go.txt in the working directory with the Read tool "+
		"(all of each), then reply with one sentence about what they do.")
	u1 := lastUsage(t, liveWait(t, r, 1, 5*time.Minute))
	send(t, a, "m2", "Reply with the single word SAME.")
	u2 := lastUsage(t, liveWait(t, r, 2, 3*time.Minute))
	t.Logf("first account, building: read %d write %d context %d", u1.CacheRead, u1.CacheWrite, u1.Context)
	t.Logf("first account, next turn: read %d write %d context %d", u2.CacheRead, u2.CacheWrite, u2.Context)
	var conv string
	r.view(func(p *agent.Projection) { conv = p.ConversationID })
	if err := a.Park(context.Background()); err != nil {
		t.Fatal(err)
	}
	// SOCRATES_CLAUDE_LIVE_PAUSE (a duration) waits before the switch, to
	// see how long the cache outlives the last request.
	if d, err := time.ParseDuration(os.Getenv("SOCRATES_CLAUDE_LIVE_PAUSE")); err == nil {
		t.Logf("waiting %v before the switch", d)
		time.Sleep(d)
	}
	b, r2 := liveOn(t, e, second, conv)
	send(t, b, "m3", "Reply with the single word OTHER.")
	u3 := lastUsage(t, liveWait(t, r2, 1, 3*time.Minute))
	t.Logf("second account, after the switch: read %d write %d context %d", u3.CacheRead, u3.CacheWrite, u3.Context)
	if u3.Context < 20000 {
		t.Fatalf("too little context to tell: %d", u3.Context)
	}
	if u3.CacheWrite > u3.Context/4 {
		t.Fatalf("the switch rewrote the cache: write %d of a context of %d", u3.CacheWrite, u3.Context)
	}
}
