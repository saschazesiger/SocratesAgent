//go:build !windows

package claude

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agent/askuser"
)

// liveAskCase is a task in which the agent has to turn to its person: what
// the repository holds, what it is told, and what an answer says.
type liveAskCase struct {
	name, prompt, answer string
	files                map[string]string
	// steps is whether the person has something to do before answering.
	steps bool
}

var liveAskCases = []liveAskCase{
	{
		name:   "login",
		prompt: "Deploy the shop to staging with ./deploy.sh and tell me when it is live.",
		answer: "Done, I logged in.",
		files: map[string]string{
			"deploy.sh": "#!/bin/sh\nif [ ! -f ./.stagingctl-token ]; then\n" +
				"  echo \"ERROR: stagingctl: not logged in (token missing).\" >&2\n" +
				"  echo \"Run: stagingctl login --sso   (opens the company SSO page in a browser; needs your personal account and 2FA)\" >&2\n" +
				"  echo \"Then re-run ./deploy.sh\" >&2\n  exit 3\nfi\necho deployed\n",
			"README.md": "# Shop\n\nDeploy to staging with `./deploy.sh`.\n",
		},
		steps: true,
	},
	{
		name:   "unsure",
		prompt: "Setz das Timeout in config.py auf den Wert, den wir im Meeting letzte Woche vereinbart haben, und committe es.",
		answer: "45 seconds.",
		files:  map[string]string{"config.py": "REQUEST_TIMEOUT_SECONDS = 30\nRETRIES = 3\n"},
	},
	{
		name: "decide",
		prompt: "Run the tests with `python3 test_pricing.py` and fix what fails. " +
			"If the fix needs a product decision, ask me before changing behaviour.",
		answer: "Keep the total-based rounding and fix the test.",
		files: map[string]string{
			"pricing.py": "def total(prices, discount_percent):\n    subtotal = sum(prices)\n" +
				"    return round(subtotal * (1 - discount_percent / 100), 2)\n",
			"test_pricing.py": "from pricing import total\n\n\ndef test_discount_rounds_per_item():\n" +
				"    # Finance says: discount is applied per item and each item is rounded down to the cent.\n" +
				"    assert total([0.99, 0.99, 0.99], 15) == 2.49\n\n\n" +
				"if __name__ == \"__main__\":\n    test_discount_rounds_per_item()\n    print(\"ok\")\n",
		},
	},
}

// The real model, told what Socrates tells every chat, turns to its person
// with ask_user - not with a question in its answer - when only the person
// can do something, when it cannot know a value, and when a fix is a
// decision; and every call stands on its own: it carries what the agent
// found, and what the person has to do first as steps.
//
// SOCRATES_CLAUDE_LIVE_MODEL picks the model (haiku by default), and
// SOCRATES_CLAUDE_LIVE_CASES the cases (all by default, comma separated).
func TestLiveAskUserStandsOnItsOwn(t *testing.T) {
	e := liveEnv(t)
	model := os.Getenv("SOCRATES_CLAUDE_LIVE_MODEL")
	if model == "" {
		model = "haiku"
	}
	only := os.Getenv("SOCRATES_CLAUDE_LIVE_CASES")
	for _, c := range liveAskCases {
		if only != "" && !strings.Contains(","+only+",", ","+c.name+",") {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			ce := e
			ce.work = filepath.Join(e.work, c.name)
			ce.state = filepath.Join(ce.work, ".state")
			if err := os.MkdirAll(ce.work, 0o755); err != nil {
				t.Fatal(err)
			}
			for name, body := range c.files {
				if err := os.WriteFile(filepath.Join(ce.work, name), []byte(body), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			git := exec.Command("sh", "-c", "git init -q -b main && git config user.email t@t && git config user.name t && "+
				"git add -A && git commit -qm init")
			git.Dir = ce.work
			if out, err := git.CombinedOutput(); err != nil {
				t.Fatalf("git: %v %s", err, out)
			}
			a := New(Options{})
			r := newRec()
			spec := ce.spec("")
			spec.Model, spec.Effort = model, ""
			if err := a.Start(t.Context(), spec, r); err != nil {
				t.Fatalf("start: %v", err)
			}
			t.Cleanup(func() { _ = a.Stop() })
			send(t, a, "m1", c.prompt)

			var ask *agent.Event
			deadline := time.Now().Add(4 * time.Minute)
			for ask == nil && time.Now().Before(deadline) {
				r.mu.Lock()
				for _, ev := range r.events {
					if ev.Type == agent.TypeAsk {
						ask = &ev
						break
					}
				}
				ended := count(r.events, agent.TypeTurnEnded)
				r.mu.Unlock()
				if ask == nil && ended > 0 {
					r.view(func(p *agent.Projection) {
						var said []string
						if m := lastAssistant(p); m != nil {
							for _, part := range m.Parts {
								if part.Kind == agent.PartText || part.Kind == agent.PartNotice {
									said = append(said, part.Text)
								}
							}
						}
						t.Fatalf("the turn ended without ask_user; the agent said:\n%s", strings.Join(said, "\n---\n"))
					})
				}
				time.Sleep(300 * time.Millisecond)
			}
			if ask == nil {
				t.Fatal("no ask in 4 minutes")
			}
			pretty, _ := json.MarshalIndent(json.RawMessage(ask.Payload), "", "  ")
			t.Logf("%s asked:\n%s", c.name, pretty)
			p := askuser.Decode(ask.Payload)
			if p == nil {
				t.Fatal("the payload does not decode")
			}
			all := strings.ToLower(string(ask.Payload))
			for _, back := range []string{"as above", "see above", "mentioned above", "wie oben", "siehe oben"} {
				if strings.Contains(all, back) {
					t.Errorf("the call points back at the chat (%q)", back)
				}
			}
			found := p.Context != "" || len(p.Sections) > 0
			for _, q := range p.Questions {
				found = found || q.Context != "" || len(q.Sections) > 0
			}
			if !found {
				t.Error("the call carries no context of what the agent found")
			}
			if c.steps {
				steps := len(p.Steps)
				for _, q := range p.Questions {
					steps += len(q.Steps)
				}
				if steps == 0 {
					t.Error("the person has to log in first, and the call has no steps")
				}
			}
			if c.name == "login" {
				_ = os.WriteFile(filepath.Join(ce.work, ".stagingctl-token"), []byte("t\n"), 0o600)
			}
			answer(t, a, *ask, c.answer)
			// Whatever else it asks on the way is answered alike.
			done := map[string]bool{ask.AskID: true}
			deadline = time.Now().Add(4 * time.Minute)
			for time.Now().Before(deadline) {
				r.mu.Lock()
				ended := count(r.events, agent.TypeTurnEnded)
				var next *agent.Event
				for _, ev := range r.events {
					if ev.Type == agent.TypeAsk && !done[ev.AskID] {
						next = &ev
						break
					}
				}
				r.mu.Unlock()
				if ended > 0 {
					break
				}
				if next != nil {
					done[next.AskID] = true
					pretty, _ := json.MarshalIndent(json.RawMessage(next.Payload), "", "  ")
					t.Logf("%s asked again:\n%s", c.name, pretty)
					answer(t, a, *next, c.answer)
				}
				time.Sleep(300 * time.Millisecond)
			}
			liveWait(t, r, 1, 4*time.Minute)
		})
	}
}

// answer answers every question of an ask with the same words (a confirm
// with yes).
func answer(t *testing.T, a *Adapter, ask agent.Event, words string) {
	t.Helper()
	answers := map[string]any{}
	if p := askuser.Decode(ask.Payload); p != nil {
		for _, q := range p.Questions {
			one := map[string]any{"other": words}
			if q.Kind == askuser.KindConfirm {
				one["selected"] = []string{"yes"}
			}
			answers[q.ID] = one
		}
	}
	raw, _ := json.Marshal(map[string]any{"answers": answers})
	if err := a.Answer(t.Context(), ask.AskID, raw); err != nil {
		t.Fatal(err)
	}
}
