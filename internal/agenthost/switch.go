package agenthost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// Switching a chat to another account (REBUILD.md §4.3, AGENT-HOST.md §4.3).
//
// The conversation stays; the CLI under it changes. A turn that runs is
// interrupted gracefully first and waited for - spike S1 found that a CLI
// killed mid-tool may run the call again when it resumes elsewhere - then
// the CLI is parked and a new one started with the other account's
// environment on the same conversation id, and every message it had not
// taken up goes to the new one. An adapter that switches by itself
// (agent.AccountSwitcher: OpenCode, whose queue lives in the adapter) is
// asked to instead. The divider is journaled before the new CLI can start a
// turn, so the journal says which account every turn after it ran on, and
// with Continue the new account is told to go on with the turn the limit
// stopped.

// switchWait bounds waiting for an interrupted turn to end before the CLI is
// parked anyway; parking ends it either way.
const switchWait = 30 * time.Second

// pendingSwitch is a switch_account that waits for the agent to be done.
// It is acknowledged as pending at once, so the person's request is
// answered, and finally once it is made or replaced: until then it stays in
// the server's outbox, and a host that dies meanwhile is followed by one
// the server starts on the account it names (AGENT-HOST.md §4.3).
type pendingSwitch struct {
	cmd Command
	env []string
}

func (h *host) currentAccount() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.account
}

func (h *host) hasPending() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pending != nil
}

// isPending reports whether clientID is the switch that waits.
func (h *host) isPending(clientID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return clientID != "" && h.pending != nil && h.pending.cmd.ClientID == clientID
}

// settle acknowledges a switch that waited for good, to every client: the
// server takes it out of its outbox. A client that does not take the frame
// now gets it again as a repeat, when it sends the switch again.
func (h *host) settle(cmd Command) {
	if cmd.ClientID == "" {
		return
	}
	raw, _ := json.Marshal(Ack{T: FrameAck, ClientID: cmd.ClientID, Op: cmd.Op})
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.conns {
		select {
		case c.replies <- raw:
		case <-c.gone:
		default:
		}
	}
}

// noteBootAccount journals the divider for a chat the server moved to another
// account while no host ran: the spec names an account the journal did not
// run on last. A chat whose journal knows no account yet has nothing to
// divide.
func (h *host) noteBootAccount() {
	var journaled string
	h.rec.View(func(p *agent.Projection) { journaled = p.Account })
	if journaled == "" || h.spec.Account == "" || journaled == h.spec.Account {
		return
	}
	h.record(agent.Event{Type: agent.TypeAccountSwitched, From: journaled, To: h.spec.Account, Reason: h.spec.SwitchReason})
}

// switchCmd is the switch_account op, with the command lock held.
func (h *host) switchCmd(cmd Command) any {
	if cmd.Account == "" {
		return fail(cmd, CodeBadRequest, "a switch needs the account to switch to")
	}
	if cmd.When != "" && cmd.When != WhenNow && cmd.When != WhenAfterTurn {
		return fail(cmd, CodeBadRequest, "a switch happens now or after the turn, not "+cmd.When)
	}
	if h.adapter == nil {
		return fail(cmd, CodeNotRunning, "the agent is not running")
	}
	// The account's whole environment comes with the switch: the one the
	// host runs on now holds the other account's key.
	if len(cmd.Env) == 0 {
		return fail(cmd, CodeBadRequest, "a switch needs the account's environment")
	}
	secret, err := h.openSecretEnv(cmd.SecretEnv)
	if err != nil {
		return fail(cmd, CodeBadRequest, err.Error())
	}
	// Ultracode stays as the chat has it, whatever account the CLI runs on.
	env := withUltracode(joinEnv(cmd.Env, secret), h.spec.Ultracode)
	var seq int64
	if cmd.ClientID != "" {
		// Args says whether the switch waits for the turn: one made now
		// stops it, as a Stop does (Turn.Stopped).
		events, err := h.rec.EmitDurable(agent.Event{Type: agent.TypeCommand, ClientID: cmd.ClientID, Op: cmd.Op,
			To: cmd.Account, Reason: cmd.Reason, Args: cmd.When})
		if err != nil {
			return fail(cmd, CodeFailed, "the switch could not be journaled: "+err.Error())
		}
		seq = events[0].Seq
	}
	h.mu.Lock()
	// The newest choice is the one that stands.
	replaced := h.pending
	h.pending = nil
	h.mu.Unlock()
	if replaced != nil {
		h.settle(replaced.cmd)
	}
	if cmd.When == WhenAfterTurn && h.busy() {
		h.mu.Lock()
		h.pending = &pendingSwitch{cmd: cmd, env: env}
		h.mu.Unlock()
		return Ack{T: FrameAck, ClientID: cmd.ClientID, Op: cmd.Op, Seq: seq, Pending: true}
	}
	ctx, cancel := context.WithTimeout(context.Background(), switchWait+parkWait+startWait)
	defer cancel()
	if err := h.doSwitch(ctx, cmd, env); err != nil {
		return fail(cmd, CodeFailed, err.Error())
	}
	return Ack{T: FrameAck, ClientID: cmd.ClientID, Op: cmd.Op, Seq: seq}
}

// busy is whether the agent is working on something a switch after the turn
// waits for: a turn, or a message it has not taken up - unless what it has
// not taken up is held behind a limit (the adapters hold their queue after a
// limited turn), which waits for the switch.
func (h *host) busy() bool {
	busy := false
	h.rec.View(func(p *agent.Projection) {
		busy = p.Turn != nil || (len(p.Undelivered()) > 0 && !heldByLimit(p))
	})
	return busy
}

// heldByLimit reports whether the agent stopped at its account's limit and
// has done nothing since.
func heldByLimit(p *agent.Projection) bool {
	return p.Turn == nil && p.LastTurn != nil && p.LastTurn.Outcome == agent.OutcomeLimited
}

// heldFor is agent.StartSpec.Held for an adapter starting on account: the
// last turn, when the limit ended it on that very account and the agent has
// done nothing since. On any other account nothing is held: a switch is what
// lets the hold go. The journal names the account each turn ran on (the host
// stamps turn.started); a turn that names none ran on a host that had none.
func heldFor(p *agent.Projection, account string) string {
	if !heldByLimit(p) || p.LastTurn.Account != account {
		return ""
	}
	return p.LastTurn.ID
}

// owesContinue reports whether a switch's Continue still has a turn to carry
// on: the last one stopped (by the limit, or by the switch) and the agent has
// taken nothing up since. A Continue sent twice - a second tap, another
// device, an automatic switch on top of one by hand - finds the first one
// running or done, and says nothing.
func owesContinue(p *agent.Projection) bool {
	if p.Turn != nil || len(p.Undelivered()) > 0 || p.LastTurn == nil {
		return false
	}
	return p.LastTurn.Outcome == agent.OutcomeLimited || p.LastTurn.Outcome == agent.OutcomeInterrupted
}

// runPending makes the switch that waits for the agent once it is done. It
// gives way to a command being handled, and is tried again after every turn
// and on the idle loop's tick.
func (h *host) runPending() {
	if !h.hasPending() {
		return
	}
	if !h.cmdMu.TryLock() {
		return
	}
	defer h.cmdMu.Unlock()
	if h.adapter == nil || h.isEnding() || h.busy() {
		return
	}
	h.mu.Lock()
	p := h.pending
	h.pending = nil
	h.mu.Unlock()
	if p == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), switchWait+parkWait+startWait)
	defer cancel()
	if err := h.doSwitch(ctx, p.cmd, p.env); err != nil {
		log.Printf("the switch to %s after the turn failed: %v", p.cmd.Account, err)
	}
	h.settle(p.cmd)
}

// doSwitch moves the CLI to cmd.Account, with the command lock held.
func (h *host) doSwitch(ctx context.Context, cmd Command, env []string) error {
	from := h.currentAccount()
	if cmd.Account == from {
		// The host already runs on it: a launch that started on the account
		// a switch waiting in the server's outbox named, or the switch sent
		// again. What is left is the divider, if the journal has not got
		// one, and the continue.
		var journaled string
		h.rec.View(func(p *agent.Projection) { journaled = p.Account })
		if journaled != "" && journaled != cmd.Account {
			h.record(agent.Event{Type: agent.TypeAccountSwitched, From: journaled, To: cmd.Account, Reason: cmd.Reason})
		}
		h.mu.Lock()
		h.env = env
		h.mu.Unlock()
		owed := false
		h.rec.View(func(p *agent.Projection) { owed = owesContinue(p) })
		if cmd.Continue && owed {
			h.sendContinue(ctx, cmd)
		}
		return nil
	}
	log.Printf("switching from account %s to %s", from, cmd.Account)
	h.interruptAndWait(ctx)
	divider := agent.Event{Type: agent.TypeAccountSwitched, From: from, To: cmd.Account, Reason: cmd.Reason}
	if sw, ok := h.adapter.(agent.AccountSwitcher); ok {
		// The adapter parks and starts itself, and runs what it holds on the
		// new account at once: the divider goes first. When the new process
		// does not come up, the adapter says so (Exited) and the host ends;
		// the next one starts on the new account.
		h.record(divider)
		h.setAccount(cmd.Account, env)
		if err := sw.SwitchAccount(ctx, cmd.Account, env); err != nil {
			h.record(agent.Event{Type: agent.TypeError, Code: "switch_failed", Text: err.Error()})
			return err
		}
		h.cliPID.Store(int64(h.adapter.PID()))
	} else {
		// What runs in the background dies with the old process; the agent
		// on the new one is asked to start it again.
		var stopped []string
		h.rec.View(func(p *agent.Projection) { stopped = runningWork(p) })
		h.parkAdapter(ctx, len(stopped) > 0)
		h.record(divider)
		if err := h.startAdapter(ctx, cmd.Account, env); err != nil {
			// The other account could not start the agent (signed out, a
			// key that is refused): the chat goes on where it was, and says
			// both.
			h.mu.Lock()
			back := h.env
			h.mu.Unlock()
			h.record(agent.Event{Type: agent.TypeError, Code: "switch_failed",
				Text: fmt.Sprintf("The agent could not start on the other account: %v", err)},
				agent.Event{Type: agent.TypeAccountSwitched, From: cmd.Account, To: from,
					Reason: "The switch did not work, so the chat stays on the account it was on."})
			if berr := h.startAdapter(ctx, from, back); berr != nil {
				h.failStart(berr)
				return err
			}
			h.resendOwed(ctx)
			h.sendGoOn(ctx, cmd, stopped, false)
			return err
		}
		h.setAccount(cmd.Account, env)
		h.resendOwed(ctx)
		h.sendGoOn(ctx, cmd, stopped, cmd.Continue)
		return nil
	}
	if cmd.Continue {
		h.sendContinue(ctx, cmd)
	}
	return nil
}

// maxRestarts is how many pieces of stopped work a restart message names.
const maxRestarts = 12

// runningWork is the agent's background work as the journal shows it
// running, a line each for the agent to start it again: a workflow (with
// its run, which it resumes from), a subagent sent to the background, a
// shell left running. A foreground call is the running turn's, which the
// Continue carries on.
func runningWork(p *agent.Projection) []string {
	var out []string
	for _, m := range p.Messages {
		for _, part := range m.Parts {
			if part.Kind != agent.PartTool || len(out) >= maxRestarts {
				continue
			}
			var c struct {
				Tool    string `json:"tool"`
				Summary string `json:"summary"`
				Command string `json:"command"`
				Agent   *struct {
					Status      string `json:"status"`
					Background  bool   `json:"background"`
					Description string `json:"description"`
				} `json:"agent"`
				Workflow *struct {
					Status string `json:"status"`
					Name   string `json:"name"`
					RunID  string `json:"run_id"`
				} `json:"workflow"`
				Task *struct {
					Status string `json:"status"`
				} `json:"task"`
			}
			if json.Unmarshal(part.Content, &c) != nil {
				continue
			}
			switch {
			case c.Workflow != nil && c.Workflow.Status == "running":
				line := fmt.Sprintf("- the workflow %q", firstOf(c.Workflow.Name, c.Summary, "without a name"))
				if c.Workflow.RunID != "" {
					line += " (run " + c.Workflow.RunID + ")"
				}
				out = append(out, line)
			case c.Agent != nil && c.Agent.Status == "running" && c.Agent.Background:
				out = append(out, fmt.Sprintf("- the background subagent %q", firstOf(c.Agent.Description, c.Summary, "without a description")))
			case c.Task != nil && c.Task.Status == "running":
				out = append(out, fmt.Sprintf("- the background command `%s`", firstOf(c.Command, c.Summary, c.Tool)))
			}
		}
	}
	for _, sc := range p.Schedules {
		if len(out) >= maxRestarts {
			break
		}
		out = append(out, scheduleLine(sc))
	}
	return out
}

// scheduleLine is a turn the agent had scheduled, for it to schedule again:
// when, and what it was going to be told.
func scheduleLine(sc agent.Schedule) string {
	var line string
	switch {
	case sc.Kind == agent.ScheduleWakeup && sc.At > 0:
		line = "- the wakeup you scheduled for " + time.UnixMilli(sc.At).UTC().Format("2006-01-02 15:04 UTC")
	case sc.Kind == agent.ScheduleWakeup:
		line = "- the wakeup you scheduled"
	case sc.Recurring:
		line = fmt.Sprintf("- the recurring scheduled task `%s`", sc.Cron)
	default:
		line = fmt.Sprintf("- the scheduled task `%s`, which had not run yet", sc.Cron)
	}
	if sc.Prompt != "" {
		line += fmt.Sprintf(", with the prompt %q", sc.Prompt)
	}
	return line
}

func firstOf(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// restartText asks the agent on the new process to start again the
// background work the switch stopped, and to go on where it left off when
// the switch says so.
func restartText(stopped []string, goOn bool) string {
	text := "Switching the account restarted you, which stopped background work that was still running or scheduled:\n" +
		strings.Join(stopped, "\n") + "\n\nStart it again (schedule again what was scheduled) so that it finishes as it would have. A workflow resumes " +
		"from its run (Workflow with its script file and resumeFromRunId), which keeps what its agents had already finished."
	if goOn {
		text += " Then continue where you left off."
	}
	return text
}

// sendGoOn is what the new process is told after a switch: the background
// work to start again, and the Continue; the Continue alone when nothing was
// stopped. It goes as one message of the person's, under the switch's
// client_id, so a switch sent twice says it once.
func (h *host) sendGoOn(ctx context.Context, cmd Command, stopped []string, goOn bool) {
	switch {
	case len(stopped) > 0:
		h.sendText(ctx, cmd, restartText(stopped, goOn))
	case goOn:
		h.sendContinue(ctx, cmd)
	}
}

func (h *host) setAccount(account string, env []string) {
	h.mu.Lock()
	h.account, h.env = account, env
	h.mu.Unlock()
}

// interruptAndWait ends a running turn gracefully and waits for its end to be
// journaled, for at most switchWait.
func (h *host) interruptAndWait(ctx context.Context) {
	running := func() bool {
		running := false
		h.rec.View(func(p *agent.Projection) { running = p.Turn != nil })
		return running
	}
	if !running() {
		return
	}
	if err := h.adapter.Interrupt(ctx); err != nil {
		log.Printf("interrupt for an account switch: %v", err)
	}
	deadline := time.Now().Add(switchWait)
	for running() && time.Now().Before(deadline) && ctx.Err() == nil {
		time.Sleep(20 * time.Millisecond)
	}
}

// parkAdapter stops the CLI for a switch, and closes a turn the park cut
// short. An adapter that can be released is not waited for while background
// work of it runs: that work ends either way, and restart says the agent is
// asked to start it again.
func (h *host) parkAdapter(ctx context.Context, restart bool) {
	old := h.adapter
	h.setAdapter(nil)
	h.cliPID.Store(0)
	background := 0
	h.rec.View(func(p *agent.Projection) { background = p.Tasks })
	pctx, cancel := context.WithTimeout(ctx, parkWait)
	park := old.Park
	if r, ok := old.(agent.Releaser); ok {
		park = r.Release
	}
	if err := park(pctx); err != nil {
		log.Printf("the agent did not park cleanly for a switch: %v", err)
		_ = old.Stop()
	}
	cancel()
	// The background work of the old process ended with it, and the new
	// one will not count it: nothing of it may run on in the chat.
	var events []agent.Event
	h.rec.View(func(p *agent.Projection) {
		left, closed := leftovers(p)
		events = append(events, left...)
		if background > 0 || closed > 0 || len(p.Schedules) > 0 {
			text := "The switch restarted the agent on the other account, so the background work that was running stopped."
			if restart {
				text = "The switch restarted the agent on the other account, which stopped the background work that was running; the agent is asked to start it again."
			}
			events = append(events, agent.Event{Type: agent.TypeNotice, Code: agent.NoticeBackgroundStopped, Text: text})
		}
		if p.Turn != nil {
			events = append(events, agent.Event{Type: agent.TypeTurnEnded, TurnID: p.Turn.ID,
				Outcome: agent.OutcomeInterrupted, Text: "The turn was stopped to switch the account."})
		}
	})
	if len(events) > 0 {
		h.record(events...)
	}
}

// startAdapter starts a new adapter on account with env, resuming the chat's
// conversation, and makes it the host's.
func (h *host) startAdapter(ctx context.Context, account string, env []string) error {
	factory, ok := agent.Lookup(h.spec.Harness)
	if !ok {
		return fmt.Errorf("there is no adapter for %q", h.spec.Harness)
	}
	var resume, model, effort, held string
	h.rec.View(func(p *agent.Projection) {
		resume, model, effort = p.ConversationID, p.Model, p.Effort
		// Back on the account a limit stopped (a switch that failed), the
		// new adapter holds what the old one held.
		held = heldFor(p, account)
	})
	if resume == "" {
		resume = h.spec.Conversation
	}
	if model == "" {
		model, effort = h.spec.Model, h.spec.Effort
	}
	spec := agent.StartSpec{
		ChatID: h.opts.ChatID, Workdir: h.spec.Workdir, StateDir: filepath.Join(h.dir, stateDir), DataDir: h.opts.DataDir,
		Env: env, Account: account, Model: model, Effort: effort, ResumeID: resume, Held: held, Spawn: h.spawn,
		Binary: h.spec.Binary, AskCommand: h.askCommand(), Instructions: h.spec.Instructions, MCPServers: h.chatMCP(env),
	}
	sctx, cancel := context.WithTimeout(ctx, startWait)
	defer cancel()
	ad := factory()
	err := ad.Start(sctx, spec, emitter{h})
	if err != nil && resume != "" && errors.Is(err, agent.ErrConversationNotFound) {
		// The other account's CLI does not find the conversation: as at a
		// start, a fresh one is the way on, and the chat says so.
		h.record(agent.Event{Type: agent.TypeNotice, Code: agent.NoticeConversationLost,
			Text: "The agent no longer has this conversation, so a new one was started. " +
				"The messages above stay here, but the agent does not remember them."})
		spec.ResumeID = ""
		ad = factory()
		err = ad.Start(sctx, spec, emitter{h})
	}
	if err != nil {
		return err
	}
	h.setAdapter(ad)
	h.cliPID.Store(int64(ad.PID()))
	h.record(agent.Event{Type: agent.TypeProcess, State: agent.ProcessReady, PID: ad.PID()})
	return nil
}

// resendOwed hands a new adapter what the old one left: messages it never
// took up, and answers that never reached it. On the account a limit stopped
// they go through the same hold as on the old adapter (StartSpec.Held).
func (h *host) resendOwed(ctx context.Context) {
	var pending []agent.Outgoing
	var unsent []agent.Ask
	h.rec.View(func(p *agent.Projection) {
		for _, m := range p.Undelivered() {
			pending = append(pending, outgoing(m))
		}
		for _, a := range p.UnsentAnswers() {
			unsent = append(unsent, *a)
		}
	})
	for _, msg := range pending {
		if err := h.adapter.Send(ctx, msg); err != nil {
			h.record(agent.Event{Type: agent.TypeError, Code: "send_failed", MessageID: msg.MessageID, Text: err.Error()})
		}
	}
	for i := range unsent {
		h.deliverAsMessage(ctx, &unsent[i], unsent[i].Answer, unsent[i].Deliver != agent.ViaMessage)
	}
	h.touch()
}

// sendContinue tells the agent on the new account to go on, as a message of
// the person's: the turn the limit stopped continues there. Its client_id
// is the switch's own, marked, so a switch sent twice says it once.
func (h *host) sendContinue(ctx context.Context, cmd Command) { h.sendText(ctx, cmd, ContinueText) }

// sendText sends text as the switch's message of the person's.
func (h *host) sendText(ctx context.Context, cmd Command, text string) {
	clientID := ""
	if cmd.ClientID != "" {
		clientID = cmd.ClientID + ":continue"
		var seen bool
		h.rec.View(func(p *agent.Projection) { _, seen = p.Seen(clientID) })
		if seen {
			return
		}
	}
	msg := agent.Event{Type: agent.TypeMessage, MessageID: newID("m_"), Role: agent.RoleUser,
		State: agent.MessagePending, ClientID: clientID, Text: text}
	if _, err := h.rec.EmitDurable(msg); err != nil {
		log.Printf("journal: %v", err)
		return
	}
	out := agent.Outgoing{MessageID: msg.MessageID, ClientID: clientID, Text: text, AfterTurn: h.afterTurn(msg.MessageID)}
	if err := h.adapter.Send(ctx, out); err != nil {
		h.record(agent.Event{Type: agent.TypeError, Code: "send_failed", MessageID: msg.MessageID, Text: err.Error()})
	}
}
