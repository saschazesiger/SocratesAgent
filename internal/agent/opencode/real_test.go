//go:build linux

package opencode

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// The real-binary tests are off unless SOCRATES_OPENCODE_REAL=1. This one
// runs the installed opencode on a free model, in a data folder of its own:
//
//	SOCRATES_OPENCODE_REAL=1            run it
//	SOCRATES_OPENCODE_REAL_DIR=<dir>    the data folder (default: a temp dir)
//	SOCRATES_OPENCODE_REAL_MODEL=<p/m>  the model (default: OpenRouter's free
//	                                    nemotron)
//	SOCRATES_OPENCODE_REAL_AUTH=<json>  OPENCODE_AUTH_CONTENT of account "a",
//	                                    an API key for the model's provider
//	SOCRATES_OPENCODE_REAL_ZEN=<p/m>    also try this Zen free model, whose
//	                                    quota may be used up: a limit must
//	                                    then end the turn as limited
//
// It never touches ~/.local/share/opencode or ~/.config/opencode: every
// process gets the layout's paths.
func TestRealOpenCode(t *testing.T) {
	model := firstNonEmpty(os.Getenv("SOCRATES_OPENCODE_REAL_MODEL"), "openrouter/nvidia/nemotron-3.5-lightning:free")
	auth := os.Getenv("SOCRATES_OPENCODE_REAL_AUTH")
	data := os.Getenv("SOCRATES_OPENCODE_REAL_DIR")
	if data == "" {
		data = t.TempDir()
	}
	work, base := realOpenCode(t, data)
	envA := append(append([]string{}, base...), "OPENCODE_AUTH_CONTENT="+auth)
	if auth == "" {
		envA = base
	}
	r := &rig{t: t, data: data, work: work, env: envA}
	spec := r.spec("")
	spec.Model, spec.Account = model, "a"
	start := time.Now()
	if err := r.start(spec); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Logf("started in %s, pid %d, session %s", time.Since(start), r.a.PID(), r.conversation())
	sid := r.conversation()
	waitTurn := func(n int, d time.Duration) string {
		t.Helper()
		eventually(t, d, "turn", func() bool { return len(r.rec.turns()) >= n })
		return r.rec.turns()[n-1]
	}

	// 1. A plain answer, with usage and the commands for the palette.
	r.send("Reply with exactly: PONG")
	if o := waitTurn(1, 3*time.Minute); o != agent.OutcomeCompleted {
		t.Fatalf("turn 1: %s %s", o, r.dump())
	}
	if !strings.Contains(strings.ToUpper(r.lastText()), "PONG") {
		t.Fatalf("turn 1 text %q", r.lastText())
	}
	r.rec.view(func(p *agent.Projection, _ []agent.Event) {
		if p.LastTurn.Usage == nil || p.LastTurn.Usage.Input == 0 {
			t.Fatalf("no usage: %+v", p.LastTurn)
		}
		t.Logf("turn 1 usage %+v cost %v; %d commands", *p.LastTurn.Usage, p.LastTurn.CostUSD, len(p.Palette()))
	})

	// 2. A tool, and a message queued behind it.
	r.send("Use the bash tool to run exactly `echo hello-from-bash`, then reply with the single word DONE.")
	queued := r.send("Reply with exactly: SECOND")
	if o := waitTurn(3, 3*time.Minute); o != agent.OutcomeCompleted {
		t.Fatalf("turns 2-3: %v %s", r.rec.turns(), r.dump())
	}
	if r.rec.count(agent.TypeMessageState, func(ev agent.Event) bool { return ev.MessageID == queued && ev.State == agent.MessageQueued }) != 1 {
		t.Fatalf("the second message was not queued")
	}
	bash := r.findPart(func(p *agent.Part) bool {
		return p.Kind == agent.PartTool && strings.Contains(p.Text, "hello-from-bash")
	})
	if bash == nil || bash.State != agent.PartDone {
		t.Fatalf("no finished bash part with its output: %s", r.dump())
	}
	t.Logf("bash part content %s", bash.Content)

	// 3. A file write: a diff part.
	r.send("Use the write tool to create the file notes.txt containing exactly the line 4711. Then reply OK.")
	if o := waitTurn(4, 3*time.Minute); o != agent.OutcomeCompleted {
		t.Fatalf("turn 4: %s %s", o, r.dump())
	}
	if _, err := os.Stat(filepath.Join(work, "notes.txt")); err != nil {
		t.Fatalf("notes.txt: %v %s", err, r.dump())
	}
	if d := r.findPart(func(p *agent.Part) bool { return p.Kind == agent.PartDiff }); d == nil {
		t.Logf("no diff part for the write (the model may have used another tool): %s", r.dump())
	} else {
		t.Logf("diff %s", d.Content)
	}
	writeMsg := ""
	r.rec.view(func(p *agent.Projection, _ []agent.Event) {
		for _, m := range p.Messages {
			if m.Role == agent.RoleUser && strings.Contains(m.Text, "notes.txt") {
				writeMsg = m.ID
			}
		}
	})

	// 4. An interrupt during a long command.
	r.send("Use the bash tool to run exactly `sleep 60`.")
	eventually(t, 3*time.Minute, "the sleep to run", func() bool {
		return r.findPart(func(p *agent.Part) bool {
			return p.Kind == agent.PartTool && p.State == agent.PartRunning && strings.Contains(string(p.Content), "sleep 60")
		}) != nil
	})
	if err := r.a.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	if o := waitTurn(5, time.Minute); o != agent.OutcomeInterrupted {
		t.Fatalf("turn 5: %s %s", o, r.dump())
	}

	// 5. Park, resume on a new process: the model remembers.
	if err := r.a.Park(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.start(spec2(spec, sid)); err != nil {
		t.Fatalf("resume: %v", err)
	}
	r.send("What number is in notes.txt? Answer from memory, without tools, with the number only.")
	if o := waitTurn(1, 3*time.Minute); o != agent.OutcomeCompleted || !strings.Contains(r.lastText(), "4711") {
		t.Fatalf("resumed: %s %q %s", o, r.lastText(), r.dump())
	}

	// 6. Revert the write and take it back.
	if writeMsg != "" {
		ctx := context.Background()
		if err := r.a.Command(ctx, "revert", writeMsg); err != nil {
			t.Fatalf("revert: %v", err)
		}
		if _, err := os.Stat(filepath.Join(work, "notes.txt")); err == nil {
			t.Errorf("notes.txt is still there after the revert")
		}
		if err := r.a.Command(ctx, "unrevert", ""); err != nil {
			t.Fatalf("unrevert: %v", err)
		}
		if _, err := os.Stat(filepath.Join(work, "notes.txt")); err != nil {
			t.Errorf("notes.txt did not come back: %v", err)
		}
	}

	// 7. Another account: a bogus key fails, which proves the process used
	// it; back on the first account the conversation goes on.
	if auth != "" {
		bogus := bogusKeys(t, auth)
		ctx := context.Background()
		if err := r.a.SwitchAccount(ctx, "b", append(append([]string{}, base...), "OPENCODE_AUTH_CONTENT="+bogus)); err != nil {
			t.Fatalf("switch to b: %v", err)
		}
		r.send("Reply with exactly: PONG")
		if o := waitTurn(2, 3*time.Minute); o != agent.OutcomeFailed && o != agent.OutcomeLimited {
			t.Fatalf("account b answered with a bogus key: %s %s", o, r.dump())
		}
		if err := r.a.SwitchAccount(ctx, "a", envA); err != nil {
			t.Fatalf("switch back to a: %v", err)
		}
		r.send("What number was in notes.txt? Number only, no tools.")
		if o := waitTurn(3, 3*time.Minute); o != agent.OutcomeCompleted || !strings.Contains(r.lastText(), "4711") {
			t.Fatalf("back on a: %s %q %s", o, r.lastText(), r.dump())
		}
	}
	_ = r.a.Park(context.Background())

	// 8. The history rebuilt from the session.
	rs := spec2(spec, sid)
	rs.Rebuild = true
	rs.StateDir = filepath.Join(data, "chats", "rebuilt", "agent")
	if err := r.start(rs); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	users := 0
	r.rec.view(func(p *agent.Projection, _ []agent.Event) {
		for _, m := range p.Messages {
			if m.Role == agent.RoleUser {
				users++
			}
		}
	})
	t.Logf("rebuilt %d user messages, %d turns", users, len(r.rec.turns()))
	if users < 6 {
		t.Fatalf("rebuilt only %d user messages: %s", users, r.dump())
	}

	// 9. A Zen free model whose quota is used up stops as limited.
	if zen := os.Getenv("SOCRATES_OPENCODE_REAL_ZEN"); zen != "" {
		if err := r.a.SetModel(context.Background(), zen, ""); err != nil {
			t.Fatal(err)
		}
		r.send("Reply with exactly: PONG")
		o := waitTurn(len(r.rec.turns())+1, 3*time.Minute)
		t.Logf("zen %s: %s", zen, o)
		if o != agent.OutcomeCompleted && o != agent.OutcomeLimited {
			t.Fatalf("zen: %s %s", o, r.dump())
		}
	}
	pid := r.a.PID()
	_ = r.a.Stop()
	if alive(pid) {
		t.Fatalf("opencode %d outlived Stop", pid)
	}
}

// A turn on a model that does not exist fails with its error said once,
// although OpenCode says it again right after session.idle, and the message
// queued behind it runs. It needs no provider and no quota.
func TestRealOpenCodeSaysAFailedTurnsErrorOnce(t *testing.T) {
	data := t.TempDir()
	work, base := realOpenCode(t, data)
	r := &rig{t: t, data: data, work: work, env: base}
	spec := r.spec("")
	spec.Model, spec.Account = "opencode/no-such-model-socrates", "a"
	if err := r.start(spec); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = r.a.Stop() }()
	r.send("Reply with exactly: PONG")
	r.send("Reply with exactly: SECOND")
	eventually(t, 2*time.Minute, "two turns", func() bool { return len(r.rec.turns()) >= 2 })
	// The repeat comes milliseconds after the turn's end.
	time.Sleep(time.Second)
	if got := r.rec.turns(); got[0] != agent.OutcomeFailed || got[1] != agent.OutcomeFailed {
		t.Fatalf("turns %v %s", got, r.dump())
	}
	if n := r.rec.count(agent.TypeError, nil); n != 2 {
		t.Fatalf("%d errors for two failed turns: %s", n, r.dump())
	}
}

// realOpenCode skips the test unless SOCRATES_OPENCODE_REAL=1 and opencode
// is on PATH. It makes the working directory in data (a git repository) and
// returns it, and the environment for the process: data as its home, and the
// network as the test has it (a proxy and its CA), nothing else of the
// test's environment.
func realOpenCode(t *testing.T, data string) (string, []string) {
	t.Helper()
	if os.Getenv("SOCRATES_OPENCODE_REAL") != "1" {
		t.Skip("set SOCRATES_OPENCODE_REAL=1 to run against the installed opencode")
	}
	bin, err := exec.LookPath("opencode")
	if err != nil {
		t.Skip("no opencode on PATH")
	}
	work := filepath.Join(data, "work")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	if git, err := exec.LookPath("git"); err == nil {
		cmd := exec.Command(git, "init", "-q", work)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, out)
		}
	}
	base := []string{"PATH=" + filepath.Dir(bin) + string(os.PathListSeparator) + os.Getenv("PATH"), "HOME=" + data}
	for _, key := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "NO_PROXY", "no_proxy",
		"SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS"} {
		if v, ok := os.LookupEnv(key); ok {
			base = append(base, key+"="+v)
		}
	}
	return work, base
}

// bogusKeys is auth with every API key spoiled.
func bogusKeys(t *testing.T, auth string) string {
	t.Helper()
	var entries map[string]map[string]any
	if err := json.Unmarshal([]byte(auth), &entries); err != nil {
		t.Fatalf("SOCRATES_OPENCODE_REAL_AUTH: %v", err)
	}
	for _, e := range entries {
		e["key"] = "bogus-key"
	}
	raw, _ := json.Marshal(entries)
	return string(raw)
}

func spec2(spec agent.StartSpec, sid string) agent.StartSpec {
	spec.ResumeID = sid
	return spec
}

func (r *rig) findPart(match func(*agent.Part) bool) *agent.Part {
	var out *agent.Part
	r.rec.view(func(p *agent.Projection, _ []agent.Event) {
		var walk func(parts []*agent.Part)
		walk = func(parts []*agent.Part) {
			for _, part := range parts {
				if match(part) {
					c := *part
					out = &c
				}
				walk(part.Children)
			}
		}
		for _, m := range p.Messages {
			walk(m.Parts)
		}
	})
	return out
}

// dump is the recorded events, compactly, for a failure message.
func (r *rig) dump() string {
	var b strings.Builder
	r.rec.view(func(_ *agent.Projection, events []agent.Event) {
		for _, ev := range events {
			if ev.Type == agent.TypePartDelta {
				continue
			}
			text := ev.Text
			if len(text) > 120 {
				text = text[:120] + "…"
			}
			content := string(ev.Content)
			if len(content) > 160 {
				content = content[:160] + "…"
			}
			b.WriteString("\n  " + ev.Type + " " + ev.Kind + " " + ev.State + " " + ev.Outcome + " " + ev.Code + " " + text + " " + content)
		}
	})
	return b.String()
}
