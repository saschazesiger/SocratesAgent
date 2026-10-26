//go:build !windows

package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

func init() {
	if os.Getenv(FakeEnv) == "1" {
		EnableFake()
	}
}

// EnableFake registers the scripted agent.
func EnableFake() {
	Register(FakeHarness, func() Adapter { return &fakeAdapter{} })
}

// fakeAdapter is an in-process agent driven by the text of each message, for
// tests and the e2e suite. It behaves like a real one where durability is
// concerned: deltas stream, a turn runs on while nobody is connected, messages
// sent during a turn are queued, it keeps a conversation on disk that a
// resume continues, and its "CLI" is a real child process started through
// Spawn, so that a dying host takes it along.
//
// The script, one command per message:
//
//	/stream N [ms]   N text deltas, ms apart (default 10)
//	/tool            a tool call with streamed output, then a line of text
//	/ask             an ask; the turn waits for the answer
//	/askmany         an ask with five questions of every kind (REBUILD §5.1)
//	/asknext         the same, answered as the next message; the turn ends
//	/plan            a plan, and an ask to approve it; the turn waits
//	/md [ms]         reasoning, then a long Markdown answer streamed in chunks
//	/tools           tool calls: a read, a command, an edit with its diff, an error
//	/bigtool         a command that prints a hundred long lines
//	/todo [ms]       a task list worked through step by step
//	/workflow [ms]   a workflow of phases with agents nested in it
//	/dividers        a notice, an account switch, a model change and an error
//	/subagent        a subagent tool part with parts nested inside it
//	/sleep S         a turn that works for S seconds
//	/bg S            a turn that leaves a background task running for S seconds
//	/bgagent S       the same with a background subagent
//	/limit           the account's limit stops the turn
//	/refused         the provider refuses the account's sign-in (an error of code auth)
//	/account         which account the CLI runs on, and whether it has a key
//	/crash           the CLI child dies in the middle of the turn
//	/orphan          a tool process that escapes the CLI's process group
//	anything else    "you said: …" with the turn number of the conversation
type fakeAdapter struct {
	mu    sync.Mutex
	out   Emitter
	spec  StartSpec
	conv  string
	turns int
	// history is the conversation's transcript, for a rebuild.
	history []fakeHistory
	model   string
	effort  string
	queue   []Outgoing
	busy    bool
	cancel  context.CancelFunc
	waits   Waits
	bg      int
	bgAgent int
	child   *exec.Cmd
	ending  bool
	wake    chan struct{}
	quit    chan struct{}
}

// fakeState is the conversation the fake keeps on disk, its stand-in for a
// CLI's own transcript.
type fakeState struct {
	Turns   int           `json:"turns"`
	History []fakeHistory `json:"history,omitempty"`
}

// fakeHistory is one turn of the fake's transcript, what a rebuild reads.
type fakeHistory struct {
	User    string `json:"user"`
	Reply   string `json:"reply"`
	Outcome string `json:"outcome"`
}

func (f *fakeAdapter) Start(ctx context.Context, spec StartSpec, out Emitter) error {
	f.out, f.spec = out, spec
	f.model, f.effort = spec.Model, spec.Effort
	f.wake, f.quit = make(chan struct{}, 1), make(chan struct{})
	if spec.ResumeID != "" {
		raw, err := os.ReadFile(f.statePath(spec.ResumeID))
		if err != nil {
			// What the real CLIs do with an id they do not know.
			return fmt.Errorf("no conversation found with id %s: %w", spec.ResumeID, ErrConversationNotFound)
		}
		var st fakeState
		if err := json.Unmarshal(raw, &st); err != nil {
			return fmt.Errorf("the conversation %s is damaged: %w", spec.ResumeID, err)
		}
		f.conv, f.turns, f.history = spec.ResumeID, st.Turns, st.History
		if spec.Rebuild {
			out.Emit(f.rebuilt()...)
		}
	} else {
		f.conv = "fake-" + fakeID()
		if err := f.save(); err != nil {
			return err
		}
	}
	if err := f.startChild(); err != nil {
		return err
	}
	out.Emit(Event{Type: TypeConversation, ConversationID: f.conv}, Event{Type: TypeAgentInfo, Content: fakeInfo})
	if spec.First != nil {
		f.queue = append(f.queue, *spec.First)
	}
	go f.work()
	f.poke()
	return nil
}

// fakeUltracodeEnv is the Claude adapter's ultracode switch
// (claude.UltracodeEnv), which the fake reports so a test sees the chat's
// option reach the CLI on whatever account it runs.
const fakeUltracodeEnv = "SOCRATES_CLAUDE_ULTRACODE"

// accountLine says which account the CLI was started on, and whether a key
// came with it - its length, never the key itself - and ultracode with it.
func (f *fakeAdapter) accountLine() string {
	key, _ := lookupEnv(f.spec.Env, FakeKeyEnv)
	line := fmt.Sprintf("account %s, no key", f.spec.Account)
	if key != "" {
		line = fmt.Sprintf("account %s, a key of %d characters", f.spec.Account, len(key))
	}
	if on, _ := lookupEnv(f.spec.Env, fakeUltracodeEnv); on == "1" {
		line += ", ultracode"
	}
	return line
}

// startChild starts the process that stands in for the CLI. It is a real
// child of the host so that its life can be watched like a CLI's.
func (f *fakeAdapter) startChild() error {
	if f.spec.Spawn == nil {
		return nil
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		return nil
	}
	cmd := exec.Command(sleep, "1000000")
	cmd.Env = f.spec.Env
	if err := f.spec.Spawn(cmd); err != nil {
		return fmt.Errorf("start the fake CLI: %w", err)
	}
	f.child = cmd
	go func() {
		err := cmd.Wait()
		f.mu.Lock()
		ending := f.ending
		f.mu.Unlock()
		if !ending {
			f.out.Exited(fmt.Errorf("the fake CLI exited: %v", err))
		}
	}()
	return nil
}

func (f *fakeAdapter) statePath(conv string) string {
	return filepath.Join(f.spec.StateDir, "fake", conv+".json")
}

// rebuilt is the transcript as journal events, closed with notice{rebuilt}.
func (f *fakeAdapter) rebuilt() []Event {
	var events []Event
	for i, h := range f.history {
		id := fmt.Sprintf("%s_%d", f.conv, i)
		state, msgState := PartDone, MessageDone
		if h.Outcome != OutcomeCompleted {
			state, msgState = PartInterrupted, MessageInterrupted
		}
		events = append(events,
			Event{Type: TypeMessage, MessageID: "u_" + id, Role: RoleUser, State: MessageDelivered, Text: h.User},
			Event{Type: TypeTurnStarted, TurnID: "t_" + id, MessageID: "u_" + id},
			Event{Type: TypeMessage, MessageID: "a_" + id, Role: RoleAssistant, State: MessageStreaming, TurnID: "t_" + id},
			Event{Type: TypePart, MessageID: "a_" + id, PartID: "p_" + id, Kind: PartText, State: state, Text: h.Reply},
			Event{Type: TypeMessageState, MessageID: "a_" + id, State: msgState},
			Event{Type: TypeTurnEnded, TurnID: "t_" + id, Outcome: h.Outcome})
	}
	return append(events, Event{Type: TypeNotice, Code: NoticeRebuilt,
		Text: fmt.Sprintf("The chat history was rebuilt from the fake's transcript (%d turns).", len(f.history))})
}

func (f *fakeAdapter) save() error {
	raw, _ := json.Marshal(fakeState{Turns: f.turns, History: f.history})
	path := f.statePath(f.conv)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

func (f *fakeAdapter) poke() {
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

func (f *fakeAdapter) Send(_ context.Context, msg Outgoing) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ending {
		return errors.New("the fake CLI is stopping")
	}
	f.queue = append(f.queue, msg)
	if f.busy {
		f.out.Emit(Event{Type: TypeMessageState, MessageID: msg.MessageID, State: MessageQueued})
	}
	f.poke()
	return nil
}

func (f *fakeAdapter) Interrupt(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cancel != nil {
		f.cancel()
	}
	return nil
}

func (f *fakeAdapter) Answer(_ context.Context, askID string, answer json.RawMessage) error {
	return f.waits.Answer(askID, answer)
}

func (f *fakeAdapter) SetModel(_ context.Context, model, effort string) error {
	f.mu.Lock()
	f.model, f.effort = model, effort
	f.mu.Unlock()
	f.out.Emit(Event{Type: TypeModelChanged, Model: model, Effort: effort})
	return nil
}

func (f *fakeAdapter) Command(_ context.Context, name, _ string) error {
	switch name {
	case "compact":
		f.out.Emit(Event{Type: TypeNotice, Code: "compacted", Text: "The conversation was compacted."})
	case "context":
		f.out.Emit(Event{Type: TypeUsage, Usage: &Usage{Context: 12000, Window: 200000}},
			Event{Type: TypeNotice, Code: "context", Text: "6% of the context window is in use."})
	default:
		return ErrUnknownCommand
	}
	return nil
}

func (f *fakeAdapter) Park(context.Context) error {
	f.end(syscall.SIGTERM)
	return nil
}

func (f *fakeAdapter) Stop() error {
	f.end(syscall.SIGKILL)
	return nil
}

func (f *fakeAdapter) end(sig syscall.Signal) {
	f.mu.Lock()
	if f.ending {
		f.mu.Unlock()
		return
	}
	f.ending = true
	if f.cancel != nil {
		f.cancel()
	}
	child := f.child
	f.mu.Unlock()
	close(f.quit)
	if child != nil && child.Process != nil {
		_ = syscall.Kill(-child.Process.Pid, sig)
	}
}

func (f *fakeAdapter) Background() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bg
}

func (f *fakeAdapter) PID() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.child == nil || f.child.Process == nil {
		return 0
	}
	return f.child.Process.Pid
}

// work runs the queued messages one turn at a time.
func (f *fakeAdapter) work() {
	for {
		select {
		case <-f.quit:
			return
		case <-f.wake:
		}
		for {
			f.mu.Lock()
			if f.ending || len(f.queue) == 0 {
				f.busy = false
				f.mu.Unlock()
				break
			}
			msg := f.queue[0]
			f.queue = f.queue[1:]
			ctx, cancel := context.WithCancel(context.Background())
			f.busy, f.cancel = true, cancel
			f.turns++
			turn := f.turns
			_ = f.save()
			f.mu.Unlock()
			f.turn(ctx, msg, turn)
			cancel()
			f.mu.Lock()
			f.cancel = nil
			f.mu.Unlock()
		}
	}
}

// fakeTurn is one turn as it is being written.
type fakeTurn struct {
	f      *fakeAdapter
	id     string
	msgID  string
	partID string
	user   string
	text   strings.Builder
	usage  Usage
	// opened is whether the text part exists yet: it is made by the first
	// thing the turn says, so that parts the turn makes before it (reasoning,
	// tools) come first in the message.
	opened bool
}

func (t *fakeTurn) say(s string) {
	if !t.opened {
		t.opened = true
		t.f.out.Emit(Event{Type: TypePart, MessageID: t.msgID, PartID: t.partID, Kind: PartText, State: PartStreaming})
	}
	t.text.WriteString(s)
	t.f.out.Delta(t.msgID, t.partID, s)
}

func (t *fakeTurn) end(outcome, detail string) {
	state, msgState := PartDone, MessageDone
	if outcome != OutcomeCompleted {
		state, msgState = PartInterrupted, MessageInterrupted
	}
	t.usage.Output = int64(len(t.text.String()))
	t.usage.Context, t.usage.Window = 12000+t.usage.Input+t.usage.Output, 200000
	t.f.mu.Lock()
	t.f.history = append(t.f.history, fakeHistory{User: t.user, Reply: t.text.String(), Outcome: outcome})
	_ = t.f.save()
	t.f.mu.Unlock()
	t.f.out.Emit(
		Event{Type: TypePart, MessageID: t.msgID, PartID: t.partID, Kind: PartText, State: state, Text: t.text.String()},
		Event{Type: TypeMessageState, MessageID: t.msgID, State: msgState},
		Event{Type: TypeTurnEnded, TurnID: t.id, Outcome: outcome, Usage: &t.usage, CostUSD: 0.0001, Text: detail},
	)
}

func (f *fakeAdapter) turn(ctx context.Context, msg Outgoing, n int) {
	t := &fakeTurn{f: f, id: "t_" + fakeID(), msgID: "a_" + fakeID(), user: msg.Text, usage: Usage{Input: int64(len(msg.Text))}}
	t.partID = t.msgID + "_text"
	f.out.Emit(
		Event{Type: TypeMessageState, MessageID: msg.MessageID, State: MessageDelivered},
		Event{Type: TypeTurnStarted, TurnID: t.id, MessageID: msg.MessageID},
		Event{Type: TypeMessage, MessageID: t.msgID, Role: RoleAssistant, State: MessageStreaming, TurnID: t.id, Model: f.model},
	)
	word, args, _ := strings.Cut(strings.TrimSpace(msg.Text), " ")
	fields := strings.Fields(args)
	switch word {
	case "/stream":
		count, every := fakeInt(fields, 0, 20), time.Duration(fakeInt(fields, 1, 10))*time.Millisecond
		for i := 1; i <= count; i++ {
			if !fakeWait(ctx, every) {
				t.end(OutcomeInterrupted, "")
				return
			}
			t.say(fmt.Sprintf("w%d ", i))
		}
	case "/tool":
		if !f.tool(ctx, t) {
			t.end(OutcomeInterrupted, "")
			return
		}
		t.say("The tool finished.")
	case "/ask":
		answer, ok := f.ask(ctx, t)
		if !ok {
			t.end(OutcomeInterrupted, "")
			return
		}
		t.say("you answered: " + string(answer))
	case "/asknext":
		f.out.Emit(Event{Type: TypeAsk, AskID: "ask_" + fakeID(), TurnID: t.id, Via: ViaMessage, Payload: fakeAskPayload})
		t.say("Questions delivered; the answers arrive as the next message.")
	case "/askmany":
		answer, ok := f.askWith(ctx, t, fakeAskPayload)
		if !ok {
			t.end(OutcomeInterrupted, "")
			return
		}
		t.say("you answered: " + string(answer))
	case "/plan":
		if !f.plan(ctx, t) {
			t.end(OutcomeInterrupted, "")
			return
		}
	case "/md":
		if !f.markdown(ctx, t, time.Duration(fakeInt(fields, 0, 12))*time.Millisecond) {
			t.end(OutcomeInterrupted, "")
			return
		}
	case "/tools":
		if !f.tools(ctx, t) {
			t.end(OutcomeInterrupted, "")
			return
		}
		t.say("Done with the tools.")
	case "/bigtool":
		lines := make([]string, 100)
		for i := range lines {
			lines[i] = fmt.Sprintf("line %d of what the command printed, long enough to make it a lot", i+1)
		}
		if !f.toolPart(ctx, t, t.msgID+"_big", "", PartTool, "Bash", map[string]any{"command": "seq 100", "description": "Print a lot"},
			lines, PartDone, nil) {
			t.end(OutcomeInterrupted, "")
			return
		}
		t.say("Printed it all.")
	case "/todo":
		if !f.todo(ctx, t, time.Duration(fakeInt(fields, 0, 700))*time.Millisecond) {
			t.end(OutcomeInterrupted, "")
			return
		}
		t.say("All three tasks are done.")
	case "/workflow":
		if !f.workflow(ctx, t, time.Duration(fakeInt(fields, 0, 400))*time.Millisecond) {
			t.end(OutcomeInterrupted, "")
			return
		}
		t.say("The workflow finished.")
	case "/dividers":
		f.dividers(t)
		t.say("Every kind of line was written.")
	case "/subagent":
		f.subagent(t)
		t.say("The subagent finished.")
	case "/sleep":
		if !fakeWait(ctx, time.Duration(fakeFloat(fields, 0, 1)*float64(time.Second))) {
			t.end(OutcomeInterrupted, "")
			return
		}
		t.say("slept")
	case "/bg":
		seconds := fakeFloat(fields, 0, 1)
		f.background(time.Duration(seconds*float64(time.Second)), false)
		t.say("started a background task")
	case "/bgagent":
		seconds := fakeFloat(fields, 0, 1)
		f.background(time.Duration(seconds*float64(time.Second)), true)
		t.say("started a background subagent")
	case "/limit":
		f.out.Emit(
			Event{Type: TypeRateLimit, Window: "five_hour", Used: 1, ResetsAt: time.Now().Add(2 * time.Hour).Unix()},
			Event{Type: TypeError, Code: "rate_limit", Text: "The usage limit of this account is reached."},
		)
		t.end(OutcomeLimited, "The usage limit of this account is reached.")
		return
	case "/refused":
		f.out.Emit(Event{Type: TypeError, Code: ErrorAuth, Text: "The provider refused the sign-in (401)."})
		t.end(OutcomeFailed, "The provider refused the sign-in (401).")
		return
	case "/account":
		t.say(f.accountLine())
	case "/crash":
		t.say("about to crash")
		f.crash()
		<-ctx.Done()
		return
	case "/orphan":
		if err := f.orphan(); err != nil {
			t.say("could not start the tool process: " + err.Error())
		} else {
			t.say("started a tool process")
		}
	default:
		reply := fmt.Sprintf("you said: %s (turn %d of %s)", msg.Text, n, f.conv)
		if len(msg.Attachments) > 0 {
			names := make([]string, 0, len(msg.Attachments))
			for _, a := range msg.Attachments {
				names = append(names, a.Name)
			}
			reply += fmt.Sprintf(" with %d attachment(s): %s", len(names), strings.Join(names, ", "))
		}
		for _, chunk := range fakeChunks(reply, 6) {
			if !fakeWait(ctx, 5*time.Millisecond) {
				t.end(OutcomeInterrupted, "")
				return
			}
			t.say(chunk)
		}
	}
	t.end(OutcomeCompleted, "")
}

// tool is one tool call: running, three lines of output, done.
func (f *fakeAdapter) tool(ctx context.Context, t *fakeTurn) bool {
	id := t.msgID + "_tool"
	input := json.RawMessage(`{"name":"Bash","input":{"command":"echo hello"}}`)
	f.out.Emit(Event{Type: TypePart, MessageID: t.msgID, PartID: id, Kind: PartTool, State: PartRunning, Content: input})
	var output strings.Builder
	for i := 1; i <= 3; i++ {
		if !fakeWait(ctx, 20*time.Millisecond) {
			return false
		}
		line := fmt.Sprintf("line %d\n", i)
		output.WriteString(line)
		f.out.Delta(t.msgID, id, line)
	}
	done := json.RawMessage(`{"name":"Bash","input":{"command":"echo hello"},"exit_code":0}`)
	f.out.Emit(Event{Type: TypePart, MessageID: t.msgID, PartID: id, Kind: PartTool, State: PartDone, Content: done, Text: output.String()})
	return true
}

// subagent is a Task-like tool part with a subagent's text and tool call
// nested inside it.
func (f *fakeAdapter) subagent(t *fakeTurn) {
	group := t.msgID + "_task"
	f.out.Emit(Event{Type: TypePart, MessageID: t.msgID, PartID: group, Kind: PartTool, State: PartRunning,
		Content: json.RawMessage(`{"name":"Task","input":{"description":"look around"}}`)})
	f.out.Emit(Event{Type: TypePart, MessageID: t.msgID, PartID: group + "_text", ParentPartID: group,
		Kind: PartText, State: PartDone, Text: "Looking around."})
	f.out.Emit(Event{Type: TypePart, MessageID: t.msgID, PartID: group + "_ls", ParentPartID: group,
		Kind: PartTool, State: PartDone, Text: "a\nb\n", Content: json.RawMessage(`{"name":"Bash","input":{"command":"ls"}}`)})
	f.out.Emit(Event{Type: TypePart, MessageID: t.msgID, PartID: group, Kind: PartTool, State: PartDone,
		Content: json.RawMessage(`{"name":"Task","input":{"description":"look around"}}`), Text: "Found a and b."})
}

// ask blocks the turn on a question until it is answered or interrupted.
func (f *fakeAdapter) ask(ctx context.Context, t *fakeTurn) (json.RawMessage, bool) {
	payload := json.RawMessage(`{"title":"Fake question","questions":[{"id":"q1","question":"Pick one",` +
		`"kind":"single","options":[{"id":"a","label":"A"},{"id":"b","label":"B"}]}]}`)
	return f.askWith(ctx, t, payload)
}

// askWith blocks the turn on an ask with this payload.
func (f *fakeAdapter) askWith(ctx context.Context, t *fakeTurn, payload json.RawMessage) (json.RawMessage, bool) {
	return f.askID(ctx, t, "ask_"+fakeID(), payload)
}

// askID is askWith under a given ask id.
func (f *fakeAdapter) askID(ctx context.Context, t *fakeTurn, id string, payload json.RawMessage) (json.RawMessage, bool) {
	answer, err := f.waits.Wait(ctx, f.out, Event{Type: TypeAsk, AskID: id, TurnID: t.id, Payload: payload}, "")
	return answer, err == nil
}

func (f *fakeAdapter) background(d time.Duration, subagent bool) {
	one := 0
	if subagent {
		one = 1
	}
	f.mu.Lock()
	f.bg++
	f.bgAgent += one
	count, agents := f.bg, f.bgAgent
	f.mu.Unlock()
	f.out.Emit(Event{Type: TypeTasks, Count: count, Agents: agents})
	go func() {
		select {
		case <-time.After(d):
		case <-f.quit:
			return
		}
		f.mu.Lock()
		f.bg--
		f.bgAgent -= one
		count, agents := f.bg, f.bgAgent
		f.mu.Unlock()
		f.out.Emit(Event{Type: TypeTasks, Count: count, Agents: agents})
	}()
}

// crash kills the CLI child the way a crash would. Without one, the exit is
// reported directly.
func (f *fakeAdapter) crash() {
	f.mu.Lock()
	child := f.child
	f.mu.Unlock()
	if child != nil && child.Process != nil {
		_ = syscall.Kill(-child.Process.Pid, syscall.SIGKILL)
		return
	}
	f.out.Exited(errors.New("the fake CLI crashed"))
}

// orphan starts a process in a session of its own, outside the CLI's process
// group and parent-death signal, the way a tool's background process escapes.
// Only the chat's scope still holds it.
func (f *fakeAdapter) orphan() error {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		return err
	}
	cmd := exec.Command(sleep, "600")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	if path := filepath.Join(f.spec.StateDir, "fake", "orphan.pid"); os.MkdirAll(filepath.Dir(path), 0o700) == nil {
		_ = os.WriteFile(path, []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func fakeWait(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func fakeInt(fields []string, i, fallback int) int {
	if i < len(fields) {
		if n, err := strconv.Atoi(fields[i]); err == nil && n >= 0 {
			return n
		}
	}
	return fallback
}

func fakeFloat(fields []string, i int, fallback float64) float64 {
	if i < len(fields) {
		if n, err := strconv.ParseFloat(fields[i], 64); err == nil && n >= 0 {
			return n
		}
	}
	return fallback
}

// fakeChunks cuts s into pieces of n runes.
func fakeChunks(s string, n int) []string {
	var out []string
	runes := []rune(s)
	for len(runes) > 0 {
		k := min(n, len(runes))
		out = append(out, string(runes[:k]))
		runes = runes[k:]
	}
	return out
}

func fakeID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
