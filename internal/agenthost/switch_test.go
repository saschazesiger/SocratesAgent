//go:build linux

package agenthost

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/paths"
	"github.com/saschazesiger/SocratesAgent/internal/vault"
)

// switchRig is a chat whose spec names an account, with the data directory's
// key in place to seal each account's key for it.
type switchRig struct {
	*rig
	key *vault.Key
}

func newSwitchRig(t *testing.T) *switchRig {
	t.Helper()
	r := newRig(t, time.Hour)
	key, err := vault.LoadOrCreateKeyFile(paths.New(r.data).SecretsKey())
	if err != nil {
		t.Fatal(err)
	}
	return &switchRig{rig: r, key: key}
}

func (r *switchRig) sealed(apiKey string) string {
	r.t.Helper()
	sealed, err := SealEnv(r.key, r.chat, []string{agent.FakeKeyEnv + "=" + apiKey})
	if err != nil {
		r.t.Fatal(err)
	}
	return sealed
}

func (r *switchRig) launchOn(account, apiKey, reason string) {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	spec := Spec{ChatID: r.chat, Harness: agent.FakeHarness, Workdir: r.data, ParkAfterMS: r.park.Milliseconds(),
		Account: account, Env: []string{"PATH=" + os.Getenv("PATH")}, SwitchReason: reason}
	if apiKey != "" {
		spec.SecretEnv = r.sealed(apiKey)
	}
	if err := r.l.Launch(ctx, spec); err != nil {
		r.t.Fatalf("launch: %v", err)
	}
}

func (cl *client) switchTo(clientID, account, apiKey string, r *switchRig, when string, cont bool) {
	cl.t.Helper()
	cmd := Command{Op: OpSwitchAccount, ClientID: clientID, Account: account, Env: []string{"PATH=" + os.Getenv("PATH")},
		Reason: "testing", When: when, Continue: cont}
	if apiKey != "" {
		cmd.SecretEnv = r.sealed(apiKey)
	}
	if err := cl.c.Send(cmd); err != nil {
		cl.t.Fatal(err)
	}
}

func (cl *client) reply(clientID, text string) string {
	cl.t.Helper()
	cl.send(clientID, text)
	cl.ack(clientID, 10*time.Second)
	cl.until(10*time.Second, isType(agent.TypeTurnEnded))
	return lastReply(cl.events)
}

func dividers(events []agent.Event) []agent.Event {
	var out []agent.Event
	for _, ev := range events {
		if ev.Type == agent.TypeAccountSwitched {
			out = append(out, ev)
		}
	}
	return out
}

func turnAccounts(events []agent.Event) []string {
	var out []string
	for _, ev := range events {
		if ev.Type == agent.TypeTurnStarted {
			out = append(out, ev.Account)
		}
	}
	return out
}

// A chat moves A -> B -> A on one conversation: each CLI is started with its
// own account's key, the divider is journaled each time, every turn says the
// account it ran on, and the key is in none of the files the host keeps.
func TestASwitchMovesTheChatBetweenAccountsOnOneConversation(t *testing.T) {
	r := newSwitchRig(t)
	r.launchOn("acct-a", "sk-aaaa", "")
	cl := r.dial(0)
	if got := cl.reply("m1", "/account"); got != "account acct-a, a key of 7 characters" {
		t.Fatalf("on A: %q", got)
	}
	conv := agent.Fold(cl.events).ConversationID

	cl.switchTo("s1", "acct-b", "sk-bbbbbbbb", r, WhenNow, false)
	cl.ack("s1", 20*time.Second)
	if got := cl.reply("m2", "/account"); got != "account acct-b, a key of 11 characters" {
		t.Fatalf("on B: %q", got)
	}
	if got := cl.reply("m3", "hello"); !strings.HasPrefix(got, "you said: hello (turn 3 of "+conv+")") {
		t.Fatalf("the conversation did not go on under B: %q", got)
	}
	cl.switchTo("s2", "acct-a", "sk-aaaa", r, "", false)
	cl.ack("s2", 20*time.Second)
	if got := cl.reply("m4", "/account"); got != "account acct-a, a key of 7 characters" {
		t.Fatalf("back on A: %q", got)
	}

	// The same switch sent again is not made again.
	delete(cl.acks, "s2")
	cl.switchTo("s2", "acct-a", "sk-aaaa", r, "", false)
	if again := cl.ack("s2", 10*time.Second); !again.Dup {
		t.Fatalf("a repeated switch was acted on: %+v", again)
	}

	events := r.journal()
	got := dividers(events)
	if len(got) != 2 || got[0].From != "acct-a" || got[0].To != "acct-b" || got[1].From != "acct-b" ||
		got[1].To != "acct-a" || got[0].Reason != "testing" {
		t.Fatalf("dividers %+v", got)
	}
	if accounts := strings.Join(turnAccounts(events), ","); accounts != "acct-a,acct-b,acct-b,acct-a" {
		t.Fatalf("the turns ran on %s", accounts)
	}
	if p := agent.Fold(events); p.ConversationID != conv || p.Account != "acct-a" {
		t.Fatalf("conversation %s account %s, want %s on acct-a", p.ConversationID, p.Account, conv)
	}
	for _, name := range []string{specName, agent.JournalFile, logName} {
		raw, err := os.ReadFile(filepath.Join(ChatDir(r.data, r.chat), name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "sk-aaaa") || strings.Contains(string(raw), "sk-bbbbbbbb") {
			t.Fatalf("%s holds a key:\n%s", name, raw)
		}
	}
}

// A switch while a turn runs interrupts it gracefully first; with Continue
// the new account is told to go on.
func TestASwitchDuringATurnInterruptsItAndContinues(t *testing.T) {
	r := newSwitchRig(t)
	r.launchOn("acct-a", "", "")
	cl := r.dial(0)
	cl.send("m1", "/sleep 30")
	cl.until(10*time.Second, isType(agent.TypeTurnStarted))
	cl.switchTo("s1", "acct-b", "", r, WhenNow, true)
	cl.ack("s1", 30*time.Second)
	end := cl.until(10*time.Second, func(ev agent.Event) bool {
		return ev.Type == agent.TypeTurnEnded && ev.Outcome == agent.OutcomeCompleted
	})
	events := r.journal()
	var order []string
	for _, ev := range events {
		switch {
		case ev.Type == agent.TypeTurnEnded:
			order = append(order, "end:"+ev.Outcome)
		case ev.Type == agent.TypeAccountSwitched:
			order = append(order, "switched")
		case ev.Type == agent.TypeMessage && ev.Role == agent.RoleUser:
			order = append(order, "user:"+ev.Text)
		}
	}
	want := "user:/sleep 30,end:interrupted,switched,user:" + ContinueText + ",end:completed"
	if strings.Join(order, ",") != want {
		t.Fatalf("order %s, want %s", strings.Join(order, ","), want)
	}
	if got := lastReply(events); !strings.HasPrefix(got, "you said: "+ContinueText) || end.TurnID == "" {
		t.Fatalf("the continue: %q", got)
	}
	if accounts := strings.Join(turnAccounts(events), ","); accounts != "acct-a,acct-b" {
		t.Fatalf("the turns ran on %s", accounts)
	}
	// The turn the switch stopped was stopped by somebody, as a Stop is.
	if before := agent.Fold(events[:indexOf(events, agent.TypeAccountSwitched)]); before.LastTurn == nil || !before.LastTurn.Stopped {
		t.Fatal("the turn the switch stopped is not marked as stopped")
	}
}

func indexOf(events []agent.Event, typ string) int {
	for i, ev := range events {
		if ev.Type == typ {
			return i
		}
	}
	return len(events)
}

// A switch after the turn waits for the agent to be done: the turn is not
// interrupted.
func TestASwitchAfterTheTurnWaitsForIt(t *testing.T) {
	r := newSwitchRig(t)
	r.launchOn("acct-a", "", "")
	cl := r.dial(0)
	cl.send("m1", "/sleep 1")
	cl.until(10*time.Second, isType(agent.TypeTurnStarted))
	cl.switchTo("s1", "acct-b", "", r, WhenAfterTurn, false)
	// Taken, and said to wait: the server keeps it to send again.
	if first := cl.ack("s1", 5*time.Second); !first.Pending {
		t.Fatalf("the switch waiting for the turn was acknowledged as made: %+v", first)
	}
	delete(cl.acks, "s1")
	cl.switchTo("s1", "acct-b", "", r, WhenAfterTurn, false)
	if again := cl.ack("s1", 5*time.Second); !again.Dup || !again.Pending {
		t.Fatalf("the repeat of a waiting switch: %+v", again)
	}
	delete(cl.acks, "s1")
	cl.until(10*time.Second, isType(agent.TypeAccountSwitched))
	if final := cl.ack("s1", 10*time.Second); final.Pending {
		t.Fatalf("the switch made was not acknowledged for good: %+v", final)
	}
	events := r.journal()
	p := agent.Fold(events)
	if p.LastTurn == nil || p.LastTurn.Outcome != agent.OutcomeCompleted || p.Account != "acct-b" || p.LastTurn.Stopped {
		t.Fatalf("last turn %+v, account %s", p.LastTurn, p.Account)
	}
	if got := cl.reply("m2", "/account"); got != "account acct-b, no key" {
		t.Fatalf("after the switch: %q", got)
	}
}

// A switch to the account the chat is already on - a second tap on
// "Continue with", another device, an automatic switch on top of one by
// hand - sends no second Continue: the first one runs, or ran.
func TestASecondContinueToTheSameAccountSaysNothing(t *testing.T) {
	r := newSwitchRig(t)
	r.launchOn("acct-a", "", "")
	cl := r.dial(0)
	cl.reply("m1", "/limit")
	cl.switchTo("s1", "acct-b", "", r, WhenNow, true)
	cl.ack("s1", 30*time.Second)
	cl.switchTo("s2", "acct-b", "", r, WhenNow, true)
	cl.ack("s2", 30*time.Second)
	cl.until(10*time.Second, func(ev agent.Event) bool {
		return ev.Type == agent.TypeTurnEnded && ev.Outcome == agent.OutcomeCompleted
	})
	cl.switchTo("s3", "acct-b", "", r, WhenNow, true)
	cl.ack("s3", 30*time.Second)
	time.Sleep(300 * time.Millisecond)
	continues := 0
	for _, ev := range r.journal() {
		if ev.Type == agent.TypeMessage && ev.Role == agent.RoleUser && ev.Text == ContinueText {
			continues++
		}
	}
	if continues != 1 {
		t.Fatalf("%d continues", continues)
	}
}

// A switch carries the whole environment of the account it goes to: one
// without is refused, never run on the environment - and the key - of the
// account the chat is on.
func TestASwitchWithoutAnEnvironmentIsRefused(t *testing.T) {
	r := newSwitchRig(t)
	r.launchOn("acct-a", "sk-aaaa", "")
	cl := r.dial(0)
	if err := cl.c.Send(Command{Op: OpSwitchAccount, ClientID: "s1", Account: "acct-b"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(10 * time.Second)
	for {
		select {
		case f := <-cl.frames:
			if f.Fail != nil {
				if f.Fail.Code != CodeBadRequest {
					t.Fatalf("refused with %+v", f.Fail)
				}
				if got := cl.reply("m1", "/account"); got != "account acct-a, a key of 7 characters" {
					t.Fatalf("after the refusal: %q", got)
				}
				return
			}
			if f.Ack != nil && f.Ack.ClientID == "s1" {
				t.Fatalf("a switch without an environment was taken: %+v", f.Ack)
			}
		case <-deadline:
			t.Fatal("no answer to the switch")
		}
	}
}

// A chat the server moved to another account while its host was parked gets
// its divider from the next host, before anything runs on the new account.
func TestAHostStartedOnAnotherAccountJournalsTheDivider(t *testing.T) {
	r := newSwitchRig(t)
	r.launchOn("acct-a", "", "")
	cl := r.dial(0)
	cl.reply("m1", "hello")
	if err := cl.c.Send(Command{Op: OpPark}); err != nil {
		t.Fatal(err)
	}
	r.waitDead(10 * time.Second)
	r.launchOn("acct-b", "", "The limit of acct-a was reached.")
	again := r.dial(0)
	if got := again.reply("m2", "/account"); got != "account acct-b, no key" {
		t.Fatalf("after the relaunch: %q", got)
	}
	got := dividers(r.journal())
	if len(got) != 1 || got[0].From != "acct-a" || got[0].To != "acct-b" || got[0].Reason != "The limit of acct-a was reached." {
		t.Fatalf("dividers %+v", got)
	}
}

// A sealed key is bound to its chat: one made for another chat is refused,
// and the chat stays where it was.
func TestASecretOfAnotherChatIsRefused(t *testing.T) {
	r := newSwitchRig(t)
	r.launchOn("acct-a", "", "")
	cl := r.dial(0)
	other, err := SealEnv(r.key, "another-chat", []string{agent.FakeKeyEnv + "=sk-x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := cl.c.Send(Command{Op: OpSwitchAccount, ClientID: "s1", Account: "acct-b", Env: []string{"PATH=" + os.Getenv("PATH")},
		SecretEnv: other}); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(10 * time.Second)
	for {
		select {
		case f := <-cl.frames:
			if f.Fail != nil {
				if f.Fail.Code != CodeBadRequest {
					t.Fatalf("refused with %+v", f.Fail)
				}
				if got := cl.reply("m1", "/account"); got != "account acct-a, no key" {
					t.Fatalf("after the refusal: %q", got)
				}
				return
			}
			if f.Ack != nil && f.Ack.ClientID == "s1" {
				t.Fatalf("a foreign secret was taken: %+v", f.Ack)
			}
		case <-deadline:
			t.Fatal("no answer to the switch")
		}
	}
}

// Ultracode is the chat's option, not the account's: it goes on top of the
// account's environment at the start and again with every switch, whose
// environment does not carry it.
func TestUltracodeStaysOnAcrossASwitch(t *testing.T) {
	r := newSwitchRig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := r.l.Launch(ctx, Spec{ChatID: r.chat, Harness: agent.FakeHarness, Workdir: r.data,
		ParkAfterMS: r.park.Milliseconds(), Account: "acct-a", Env: []string{"PATH=" + os.Getenv("PATH")},
		SecretEnv: r.sealed("sk-aaaa"), Ultracode: true}); err != nil {
		t.Fatalf("launch: %v", err)
	}
	cl := r.dial(0)
	if got := cl.reply("m1", "/account"); got != "account acct-a, a key of 7 characters, ultracode" {
		t.Fatalf("on A: %q", got)
	}
	cl.switchTo("s1", "acct-b", "", r, WhenNow, false)
	cl.ack("s1", 20*time.Second)
	if got := cl.reply("m2", "/account"); got != "account acct-b, no key, ultracode" {
		t.Fatalf("on B: %q", got)
	}
}
