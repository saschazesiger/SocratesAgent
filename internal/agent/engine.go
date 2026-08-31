// Package agent implements the top level orchestration loop: it talks to the
// model over OpenRouter, works at a real terminal on the user's machine - which
// is also how it drives the coding agents - asks the user when it needs a
// decision, and streams every step to the browser.
package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/openrouter"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/term"
)

// ErrBusy is returned when a chat already has a run in flight.
var ErrBusy = errors.New("this chat is still working on the previous message")

// Event is the envelope pushed to the browser over SSE.
type Event struct {
	Type     string          `json:"type"`
	Step     *store.Step     `json:"step,omitempty"`
	StepID   string          `json:"step_id,omitempty"`
	Run      *store.Run      `json:"run,omitempty"`
	Message  *store.Message  `json:"message,omitempty"`
	Chat     *store.Chat     `json:"chat,omitempty"`
	Question *store.Question `json:"question,omitempty"`
}

type runHandle struct {
	id     string
	cancel context.CancelFunc
	seq    atomic.Int64
}

// Engine owns all running conversations.
type Engine struct {
	Store    *store.Store
	Bus      *Bus
	Settings func() config.Settings

	// Terminals owns the interactive sessions. Every piece of work Socrates
	// does happens in one of them.
	Terminals *term.Manager

	mu      sync.Mutex
	active  map[string]*runHandle
	waiters map[string]chan string
	// watching tracks which sessions already have a goroutine mirroring their
	// screen into the process view.
	watching map[string]bool
}

// New creates an engine.
func New(st *store.Store, bus *Bus, settings func() config.Settings, terminals *term.Manager) *Engine {
	return &Engine{
		Store:     st,
		Bus:       bus,
		Settings:  settings,
		Terminals: terminals,
		active:    map[string]*runHandle{},
		waiters:   map[string]chan string{},
		watching:  map[string]bool{},
	}
}

func newID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + hex.EncodeToString(b[:])
}

func (e *Engine) publish(chatID string, ev Event) { e.Bus.Publish(chatID, ev) }

// Busy reports whether a chat has an active run.
func (e *Engine) Busy(chatID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.active[chatID]
	return ok
}

// Stop cancels the active run of a chat.
func (e *Engine) Stop(chatID string) bool {
	e.mu.Lock()
	h, ok := e.active[chatID]
	e.mu.Unlock()
	if !ok {
		return false
	}
	h.cancel()
	return true
}

// Start records the user message and launches the orchestration loop.
func (e *Engine) Start(chatID, text string, auto bool) (*store.Run, error) {
	chat, err := e.Store.GetChat(chatID)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	if _, busy := e.active[chatID]; busy {
		e.mu.Unlock()
		return nil, ErrBusy
	}
	ctx, cancel := context.WithCancel(context.Background())
	run := &store.Run{ID: newID("run"), ChatID: chatID, Status: store.RunRunning, Auto: auto}
	h := &runHandle{id: run.ID, cancel: cancel}
	e.active[chatID] = h
	e.mu.Unlock()

	fail := func(err error) (*store.Run, error) {
		cancel()
		e.mu.Lock()
		delete(e.active, chatID)
		e.mu.Unlock()
		return nil, err
	}

	msg := &store.Message{ID: newID("msg"), ChatID: chatID, RunID: run.ID, Role: "user", Content: text}
	if err := e.Store.AddMessage(msg); err != nil {
		return fail(err)
	}
	if err := e.Store.AppendLLMMessage(chatID, mustJSON(openrouter.Message{Role: "user", Content: text})); err != nil {
		return fail(err)
	}
	if err := e.Store.CreateRun(run); err != nil {
		return fail(err)
	}
	_ = e.Store.TouchChat(chatID)

	e.publish(chatID, Event{Type: "message", Message: msg})
	e.publish(chatID, Event{Type: "run", Run: run})

	go e.loop(ctx, chat, run, h, text)
	return run, nil
}

func (e *Engine) loop(ctx context.Context, chat *store.Chat, run *store.Run, h *runHandle, firstText string) {
	defer func() {
		e.mu.Lock()
		if cur, ok := e.active[chat.ID]; ok && cur == h {
			delete(e.active, chat.ID)
		}
		e.mu.Unlock()
	}()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("agent: panic in run %s: %v", run.ID, r)
			e.finish(run, chat, store.RunFailed, fmt.Sprintf("internal error: %v", r), "")
		}
	}()

	if strings.TrimSpace(chat.Title) == "" {
		go e.generateTitle(chat.ID, firstText)
	}

	settings := e.Settings()
	if strings.TrimSpace(settings.OpenRouter.APIKey) == "" {
		e.addStep(run, "", store.StepError, "Not configured",
			"No OpenRouter API key is set. Open the admin dashboard and add your key.", store.StatusFailed, nil)
		e.finish(run, chat, store.RunFailed, "missing OpenRouter API key", "")
		return
	}

	client := openrouter.New(settings.OpenRouter.BaseURL, settings.OpenRouter.APIKey)
	tools := buildTools(settings)
	temperature := settings.Agent.Temperature

	finalText := ""
	for iter := 0; iter < settings.Agent.MaxIterations; iter++ {
		if ctx.Err() != nil {
			e.finish(run, chat, store.RunCancelled, "", "")
			return
		}
		history, err := e.buildMessages(chat, settings, run)
		if err != nil {
			e.addStep(run, "", store.StepError, "Storage error", err.Error(), store.StatusFailed, nil)
			e.finish(run, chat, store.RunFailed, err.Error(), "")
			return
		}

		reasoning := e.liveStep(run, "", store.StepThinking, "Reasoning")
		answer := e.liveStep(run, "", store.StepText, "")

		res, err := client.Chat(ctx, openrouter.ChatRequest{
			Model:       settings.OpenRouter.ChatModel,
			Messages:    history,
			Tools:       tools,
			Temperature: &temperature,
		}, &openrouter.StreamHandler{
			OnContent:   func(d string) { answer.Append(d) },
			OnReasoning: func(d string) { reasoning.Append(d) },
		})
		reasoning.Finish(store.StatusDone)

		if err != nil {
			answer.Finish(store.StatusDone)
			if ctx.Err() != nil {
				e.finish(run, chat, store.RunCancelled, "", "")
				return
			}
			e.addStep(run, "", store.StepError, "Model request failed", err.Error(), store.StatusFailed, nil)
			e.finish(run, chat, store.RunFailed, err.Error(), "")
			return
		}

		// Persist the assistant turn exactly as the provider returned it.
		assistant := openrouter.Message{Role: "assistant"}
		if res.Content != "" {
			assistant.Content = res.Content
		}
		assistant.ToolCalls = res.ToolCalls
		if err := e.Store.AppendLLMMessage(chat.ID, mustJSON(assistant)); err != nil {
			log.Printf("agent: persist assistant message: %v", err)
		}

		if len(res.ToolCalls) == 0 {
			// The streamed text becomes the visible answer, so it does not
			// need to stay in the process view as well.
			answer.Discard()
			finalText = strings.TrimSpace(res.Content)
			if finalText == "" {
				finalText = "I could not produce an answer this time. Please try rephrasing."
			}
			e.finish(run, chat, store.RunDone, "", finalText)
			return
		}

		answer.Finish(store.StatusDone)

		for _, call := range res.ToolCalls {
			if ctx.Err() != nil {
				e.finish(run, chat, store.RunCancelled, "", "")
				return
			}
			out := e.execTool(ctx, chat, run, call)
			if ctx.Err() != nil {
				e.finish(run, chat, store.RunCancelled, "", "")
				return
			}
			toolMsg := openrouter.Message{Role: "tool", ToolCallID: call.ID, Name: call.Function.Name, Content: out}
			if err := e.Store.AppendLLMMessage(chat.ID, mustJSON(toolMsg)); err != nil {
				log.Printf("agent: persist tool message: %v", err)
			}
		}
	}

	// Iteration budget exhausted.
	e.addStep(run, "", store.StepError, "Iteration limit reached",
		fmt.Sprintf("The agent used all %d steps without finishing. Raise the limit in the admin dashboard or split the task.",
			settings.Agent.MaxIterations), store.StatusFailed, nil)
	if finalText == "" {
		finalText = "I ran out of steps before finishing this task. You can raise the step limit in the admin dashboard or ask me for a smaller piece of the job."
	}
	e.finish(run, chat, store.RunFailed, "iteration limit reached", finalText)
}

// finish closes a run, optionally writing the visible answer.
func (e *Engine) finish(run *store.Run, chat *store.Chat, status, errMsg, answer string) {
	if answer != "" {
		msg := &store.Message{ID: newID("msg"), ChatID: chat.ID, RunID: run.ID, Role: "assistant", Content: answer}
		if err := e.Store.AddMessage(msg); err == nil {
			e.publish(chat.ID, Event{Type: "message", Message: msg})
		}
	}
	if err := e.Store.SetRunStatus(run.ID, status, errMsg); err != nil {
		log.Printf("agent: set run status: %v", err)
	}
	_ = e.Store.TouchChat(chat.ID)
	if r, err := e.Store.GetRun(run.ID); err == nil {
		e.publish(chat.ID, Event{Type: "run", Run: r})
	}
}

// buildMessages assembles the provider payload: a freshly rendered system
// prompt plus the persisted conversation.
func (e *Engine) buildMessages(chat *store.Chat, settings config.Settings, run *store.Run) ([]openrouter.Message, error) {
	raw, err := e.Store.LLMMessages(chat.ID)
	if err != nil {
		return nil, err
	}
	const maxHistory = 400
	if len(raw) > maxHistory {
		raw = raw[len(raw)-maxHistory:]
	}
	msgs := make([]openrouter.Message, 0, len(raw)+1)
	msgs = append(msgs, openrouter.Message{Role: "system", Content: e.systemPrompt(chat, settings, run)})
	for _, r := range raw {
		var m openrouter.Message
		if err := json.Unmarshal(r, &m); err != nil {
			continue
		}
		msgs = append(msgs, m)
	}
	// A history that starts with an orphaned tool result confuses providers.
	for len(msgs) > 1 && msgs[1].Role == "tool" {
		msgs = append(msgs[:1], msgs[2:]...)
	}
	return msgs, nil
}

func (e *Engine) systemPrompt(chat *store.Chat, settings config.Settings, run *store.Run) string {
	var b strings.Builder
	b.WriteString(settings.Agent.SystemPrompt)

	workdir := e.workspaceFor(chat, settings)
	fmt.Fprintf(&b, "\n\n## Working directory\n`%s`. Commands and new terminal sessions start here "+
		"unless you say otherwise, and relative paths are resolved against it.\n", workdir)

	b.WriteString("\n## Programs you can run\n")
	enabled := settings.EnabledTools()
	if len(enabled) == 0 {
		b.WriteString("No coding agents are configured. You still have a shell, so you can do the work " +
			"yourself with ordinary commands - and tell the user that no agents are enabled in the " +
			"admin dashboard.\n")
	}
	for _, t := range enabled {
		fmt.Fprintf(&b, "\n### %s (`%s`)\n%s\n", t.Name, t.ID, strings.TrimSpace(t.Description))
		command, args := t.CommandLine()
		fmt.Fprintf(&b, "Started as: `%s`\n", strings.TrimSpace(command+" "+strings.Join(args, " ")))
		if !t.SkipPermissions {
			b.WriteString("It will ask before it changes anything, so expect permission prompts on the " +
				"screen and answer them.\n")
		}
		if driving := strings.TrimSpace(t.Driving); driving != "" {
			fmt.Fprintf(&b, "How to drive it: %s\n", driving)
		}
	}

	b.WriteString("\n## Driving a program\n" +
		"Open it with terminal_open, wait for it to finish starting, type the brief and submit it, " +
		"then wait for it to go idle and read the screen. If it asks something, answer it: a menu is " +
		"answered with the arrow keys and enter, or by typing the number next to the choice. " +
		"Never assume a keypress worked - the screen comes back with every call, so look at it.\n")

	if sessions := e.sessionSummary(chat); sessions != "" {
		b.WriteString("\n## Open terminal sessions\n")
		b.WriteString(sessions)
	}

	fmt.Fprintf(&b, "\n## Context\nCurrent date: %s.\n", time.Now().Format("2006-01-02"))
	if run != nil && run.Auto {
		b.WriteString("\n## Voice mode\nThe user is in hands free voice mode. Your final answer is read out " +
			"loud: keep it under roughly 120 words, use plain sentences, no markdown, no code blocks, no lists " +
			"of file paths. When you use ask_user, keep every option to a few spoken words.\n")
	}
	return b.String()
}

// sessionSummary lists the sessions of a chat for the system prompt, so the
// orchestrator knows what is already running without having to ask.
func (e *Engine) sessionSummary(chat *store.Chat) string {
	if e.Terminals == nil {
		return ""
	}
	handles := e.Terminals.List(chat.ID)
	if len(handles) == 0 {
		return ""
	}
	var b strings.Builder
	for _, h := range handles {
		state := h.State()
		status := "running"
		if !h.Alive() {
			status = fmt.Sprintf("finished, exit code %d", state.ExitCode)
		}
		fmt.Fprintf(&b, "- `%s` - %s (%s) in %s, %s\n",
			h.ID(), h.Name(), state.Command, orDefault(state.Dir, "."), status)
	}
	return b.String()
}

// workspaceFor returns the directory a chat works in.
func (e *Engine) workspaceFor(chat *store.Chat, settings config.Settings) string {
	if strings.TrimSpace(chat.Workspace) != "" {
		return chat.Workspace
	}
	return filepath.Join(settings.Agent.WorkspaceRoot, chat.ID)
}

func (e *Engine) generateTitle(chatID, text string) {
	settings := e.Settings()
	if strings.TrimSpace(settings.OpenRouter.APIKey) == "" {
		return
	}
	client := openrouter.New(settings.OpenRouter.BaseURL, settings.OpenRouter.APIKey)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	res, err := client.Chat(ctx, openrouter.ChatRequest{
		Model: settings.OpenRouter.TitleModel,
		Messages: []openrouter.Message{
			{Role: "system", Content: "Reply with a title of at most 5 words for the user's request. " +
				"No quotes, no punctuation at the end, same language as the user."},
			{Role: "user", Content: truncate(text, 2000)},
		},
		MaxTokens: 32,
	}, nil)
	title := ""
	if err == nil {
		title = strings.Trim(strings.TrimSpace(res.Content), `"'.`)
	}
	if title == "" {
		title = truncate(strings.TrimSpace(strings.SplitN(text, "\n", 2)[0]), 60)
	}
	if title == "" {
		return
	}
	chat, err := e.Store.GetChat(chatID)
	if err != nil {
		return
	}
	if strings.TrimSpace(chat.Title) != "" {
		return
	}
	if err := e.Store.UpdateChat(chatID, title, chat.Workspace); err != nil {
		return
	}
	chat.Title = title
	e.publish(chatID, Event{Type: "chat", Chat: chat})
}

// ------------------------------------------------------------------ steps

func (e *Engine) nextSeq(run *store.Run) int64 {
	e.mu.Lock()
	h := e.active[run.ChatID]
	e.mu.Unlock()
	if h != nil && h.id == run.ID {
		return h.seq.Add(1)
	}
	seq, err := e.Store.NextStepSeq(run.ID)
	if err != nil {
		return time.Now().UnixMilli()
	}
	return seq
}

// addStep writes a one shot step and pushes it to the browser.
func (e *Engine) addStep(run *store.Run, parent, kind, title, body, status string, detail any) *store.Step {
	st := &store.Step{
		ID:       newID("step"),
		RunID:    run.ID,
		ChatID:   run.ChatID,
		ParentID: parent,
		Seq:      e.nextSeq(run),
		Kind:     kind,
		Title:    title,
		Body:     body,
		Status:   status,
	}
	if detail != nil {
		st.Detail = mustJSON(detail)
	}
	if err := e.Store.PutStep(st); err != nil {
		log.Printf("agent: put step: %v", err)
		return st
	}
	e.publish(run.ChatID, Event{Type: "step", Step: copyStep(st)})
	return st
}

// copyStep is what gets published. The caller keeps its own pointer and may
// well go on writing to it - a terminal step is rewritten every second for as
// long as its session lives - while the browser stream is still serialising
// the previous version.
func copyStep(st *store.Step) *store.Step {
	clone := *st
	return &clone
}

func (e *Engine) updateStep(st *store.Step) {
	if err := e.Store.PutStep(st); err != nil {
		log.Printf("agent: update step: %v", err)
		return
	}
	e.publish(st.ChatID, Event{Type: "step", Step: copyStep(st)})
}

// liveStep is a step whose body grows while the model streams. Writes are
// throttled so the database is not hammered token by token.
type liveStep struct {
	engine  *Engine
	run     *store.Run
	step    *store.Step
	buf     strings.Builder
	created bool
	last    time.Time
	mu      sync.Mutex
}

func (e *Engine) liveStep(run *store.Run, parent, kind, title string) *liveStep {
	return &liveStep{
		engine: e,
		run:    run,
		step: &store.Step{
			ID: newID("step"), RunID: run.ID, ChatID: run.ChatID, ParentID: parent,
			Kind: kind, Title: title, Status: store.StatusRunning,
		},
	}
}

// Append adds streamed text.
func (l *liveStep) Append(delta string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf.WriteString(delta)
	if time.Since(l.last) < 120*time.Millisecond {
		return
	}
	l.flushLocked(store.StatusRunning)
}

// Finish flushes the final text; steps that never received text are discarded.
func (l *liveStep) Finish(status string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if strings.TrimSpace(l.buf.String()) == "" && !l.created {
		return
	}
	l.flushLocked(status)
}

// Discard removes the step again, for text that became a chat message.
func (l *liveStep) Discard() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.created {
		return
	}
	if err := l.engine.Store.DeleteStep(l.step.ID); err != nil {
		log.Printf("agent: delete step: %v", err)
		return
	}
	l.engine.publish(l.step.ChatID, Event{Type: "step_removed", StepID: l.step.ID})
}

func (l *liveStep) flushLocked(status string) {
	body := l.buf.String()
	if strings.TrimSpace(body) == "" && !l.created {
		return
	}
	if !l.created {
		l.created = true
		l.step.Seq = l.engine.nextSeq(l.run)
	}
	l.step.Body = body
	l.step.Status = status
	l.last = time.Now()
	l.engine.updateStep(l.step)
}

// ----------------------------------------------------------- the terminal

// sessionMeta keys stored with a session, so a restart does not lose track of
// what a session is and where it is shown.
const (
	metaTool = "tool"
	metaStep = "step"
	metaRun  = "run"
	metaChat = "chat"
)

// session resolves a session id for a chat. Sessions are scoped to their chat
// so one conversation can never type into another one's terminal.
func (e *Engine) session(chat *store.Chat, id string) (*term.Handle, string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, "The `session` argument was empty. Open a session with terminal_open first."
	}
	if e.Terminals == nil {
		return nil, "Terminal sessions are not available in this installation."
	}
	handle, ok := e.Terminals.Get(id)
	if !ok || handle.ChatID() != chat.ID {
		open := e.sessionSummary(chat)
		if open == "" {
			return nil, fmt.Sprintf("There is no session called %q in this chat, and nothing is open. "+
				"Start one with terminal_open.", id)
		}
		return nil, fmt.Sprintf("There is no session called %q in this chat. Open sessions:\n%s", id, open)
	}
	return handle, ""
}

// toolOfSession returns the configured tool a session was started from, or a
// zero tool with usable defaults for an ad hoc command.
func (e *Engine) toolOfSession(handle *term.Handle) config.Tool {
	settings := e.Settings()
	if tool, ok := settings.Tool(handle.Meta(metaTool)); ok {
		return tool
	}
	return config.Tool{}
}

// resolveDir turns a directory argument into an absolute path. Relative paths
// belong to the chat's workspace; an absolute path is taken as given, because
// Socrates is meant to be able to work anywhere on the machine.
func (e *Engine) resolveDir(chat *store.Chat, settings config.Settings, dir string) string {
	workdir := e.workspaceFor(chat, settings)
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return workdir
	}
	if filepath.IsAbs(dir) {
		return filepath.Clean(dir)
	}
	return filepath.Clean(filepath.Join(workdir, dir))
}

// openTerminal starts a session, shows it in the process view and returns the
// first screen for the model.
func (e *Engine) openTerminal(ctx context.Context, chat *store.Chat, run *store.Run, toolID, command, name, dir string) string {
	if e.Terminals == nil {
		return "Terminal sessions are not available in this installation."
	}
	settings := e.Settings()
	workdir := e.resolveDir(chat, settings, dir)
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		return fmt.Sprintf("Could not create the working directory %s: %v", workdir, err)
	}

	spec := term.Spec{Dir: workdir, Meta: map[string]string{metaChat: chat.ID, metaRun: run.ID}}
	label := strings.TrimSpace(name)
	var tool config.Tool

	switch {
	case strings.TrimSpace(toolID) != "":
		found, ok := settings.Tool(strings.TrimSpace(toolID))
		if !ok || !found.Enabled {
			names := []string{}
			for _, t := range settings.EnabledTools() {
				names = append(names, t.ID)
			}
			if len(names) == 0 {
				return fmt.Sprintf("There is no enabled program called %q, and none are configured. "+
					"Use `command` to run something directly.", toolID)
			}
			return fmt.Sprintf("There is no enabled program called %q. Available: %s.",
				toolID, strings.Join(names, ", "))
		}
		tool = found
		spec.Command, spec.Args = tool.CommandLine()
		spec.Cols, spec.Rows = tool.Cols, tool.Rows
		spec.Meta[metaTool] = tool.ID
		if label == "" {
			label = tool.Name
		}

	case strings.TrimSpace(command) != "":
		shell, args := shellCommand(command)
		spec.Command, spec.Args = shell, args
		if label == "" {
			label = firstLine(command, 60)
		}

	default:
		if label == "" {
			label = "shell"
		}
	}

	handle, err := e.Terminals.Open(ctx, chat.ID, label, spec)
	if err != nil {
		return fmt.Sprintf("Could not start the session: %v", err)
	}

	step := e.addStep(run, "", store.StepTerminal, label, "", store.StatusRunning, map[string]any{
		"session":   handle.ID(),
		"tool":      spec.Meta[metaTool],
		"command":   term.Describe(term.Spec{Command: spec.Command, Args: spec.Args}),
		"workspace": workdir,
	})
	if err := e.Terminals.SetMeta(handle.ID(), metaStep, step.ID); err != nil {
		log.Printf("agent: remember the step of session %s: %v", handle.ID(), err)
	}
	e.watchSession(handle, step)

	// Give the program time to paint its first screen and honour a configured
	// ready pattern when there is one. A full screen program can go quiet a
	// second before it is actually listening, so waiting for silence alone is
	// not enough - terminal_send checks that what it types arrives.
	if pattern := strings.TrimSpace(tool.ReadyPattern); pattern != "" {
		_, _, _ = handle.WaitFor(ctx, pattern, 45*time.Second)
	} else {
		_, _, _ = handle.WaitIdle(ctx, 2500*time.Millisecond, 45*time.Second)
	}

	note := fmt.Sprintf("Started %s as session `%s` in %s.", label, handle.ID(), workdir)
	if driving := strings.TrimSpace(tool.Driving); driving != "" {
		note += "\nHow to drive it: " + driving
	}
	return e.describeSession(ctx, handle, note, false)
}

// describeSession renders a session for the model: what it is, whether it is
// still running, and the screen as a person would see it.
func (e *Engine) describeSession(ctx context.Context, handle *term.Handle, note string, transcript bool) string {
	state, err := handle.Refresh(ctx)
	if err != nil {
		state = handle.State()
	}
	var b strings.Builder
	status := "running"
	if !handle.Alive() {
		status = fmt.Sprintf("exited with code %d", state.ExitCode)
	}
	fmt.Fprintf(&b, "Session `%s` (%s) - %s.\n", handle.ID(), handle.Name(), status)
	if strings.TrimSpace(note) != "" {
		b.WriteString(note)
		b.WriteString("\n")
	}
	b.WriteString("\nScreen:\n```\n")
	screen := state.Screen
	if strings.TrimSpace(screen) == "" {
		screen = "(the screen is empty)"
	}
	b.WriteString(truncateMiddle(screen, 12000))
	b.WriteString("\n```\n")

	if transcript {
		out, err := handle.Output(ctx, 24000)
		if err == nil && strings.TrimSpace(out) != "" {
			b.WriteString("\nEverything it has printed:\n```\n")
			b.WriteString(out)
			b.WriteString("\n```\n")
		}
	}
	return b.String()
}

// closeTerminal ends a session and marks its step finished.
func (e *Engine) closeTerminal(handle *term.Handle) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return e.Terminals.Close(ctx, handle.ID(), 8*time.Second)
}

// watchSession mirrors a session's screen into its step, so the browser shows
// the terminal live and a page refresh restores it.
func (e *Engine) watchSession(handle *term.Handle, step *store.Step) {
	e.mu.Lock()
	if e.watching[handle.ID()] {
		e.mu.Unlock()
		return
	}
	e.watching[handle.ID()] = true
	e.mu.Unlock()

	updates := handle.Watch()
	go func() {
		defer func() {
			handle.Unwatch(updates)
			e.mu.Lock()
			delete(e.watching, handle.ID())
			e.mu.Unlock()
		}()
		// The screen changes far more often than a person can read it, so the
		// step is written at most once a second.
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		var latest term.State
		dirty := false
		write := func() {
			if !dirty {
				return
			}
			dirty = false
			step.Body = latest.Screen
			step.Status = store.StatusRunning
			if !latest.Running {
				step.Status = store.StatusDone
				if latest.ExitCode != 0 {
					step.Status = store.StatusFailed
				}
			}
			step.Detail = mustJSON(map[string]any{
				"session":   handle.ID(),
				"tool":      handle.Meta(metaTool),
				"command":   latest.Command,
				"workspace": latest.Dir,
				"running":   latest.Running,
				"exit_code": latest.ExitCode,
				"cols":      latest.Cols,
				"rows":      latest.Rows,
			})
			e.updateStep(step)
		}
		for {
			select {
			case state, ok := <-updates:
				if !ok {
					write()
					return
				}
				latest = state
				dirty = true
				if !state.Running {
					write()
					return
				}
			case <-ticker.C:
				write()
			}
		}
	}()
}

// AdoptSessions re-attaches the process view to sessions that outlived a
// restart of Socrates.
func (e *Engine) AdoptSessions() {
	if e.Terminals == nil {
		return
	}
	for _, handle := range e.Terminals.List("") {
		stepID := handle.Meta(metaStep)
		if stepID == "" {
			continue
		}
		step, err := e.Store.GetStep(stepID)
		if err != nil {
			continue
		}
		e.watchSession(handle, step)
	}
}

// runShellCommand runs one command to completion in a throwaway session and
// returns what it printed. It is the quick path: no conversation, one result.
func (e *Engine) runShellCommand(ctx context.Context, chat *store.Chat, run *store.Run, command, dir string, timeout time.Duration) (string, error) {
	if e.Terminals == nil {
		return "", fmt.Errorf("terminal sessions are not available in this installation")
	}
	settings := e.Settings()
	workdir := e.resolveDir(chat, settings, dir)
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		return "", fmt.Errorf("create the working directory %s: %w", workdir, err)
	}

	shell, args := shellCommand(command)
	step := e.addStep(run, "", store.StepShell, firstLine(command, 90), "", store.StatusRunning, map[string]any{
		"command":   command,
		"workspace": workdir,
	})

	handle, err := e.Terminals.Open(ctx, chat.ID, firstLine(command, 40), term.Spec{
		Command: shell, Args: args, Dir: workdir,
		Meta: map[string]string{metaChat: chat.ID, metaRun: run.ID, metaStep: step.ID},
	})
	if err != nil {
		step.Status = store.StatusFailed
		step.Body = err.Error()
		e.updateStep(step)
		return "", err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = e.Terminals.Close(closeCtx, handle.ID(), 5*time.Second)
	}()

	finished := e.awaitExit(ctx, handle, step, timeout)
	output, err := handle.Output(ctx, 24000)
	if err != nil {
		output = handle.State().Screen
	}
	state := handle.State()

	step.Body = output
	step.Status = store.StatusDone
	if !finished {
		step.Status = store.StatusInterrupted
	} else if state.ExitCode != 0 {
		step.Status = store.StatusFailed
	}
	step.Detail = mustJSON(map[string]any{
		"command":   command,
		"workspace": workdir,
		"exit_code": state.ExitCode,
		"timed_out": !finished,
	})
	e.updateStep(step)

	var b strings.Builder
	if !finished {
		fmt.Fprintf(&b, "The command was still running after %s and was stopped.\n", timeout)
	} else {
		fmt.Fprintf(&b, "Exit code %d.\n", state.ExitCode)
	}
	if strings.TrimSpace(output) == "" {
		b.WriteString("It printed nothing.")
	} else {
		b.WriteString("\nOutput:\n```\n")
		b.WriteString(output)
		b.WriteString("\n```")
	}
	return b.String(), nil
}

// awaitExit waits for a one shot command to finish, keeping its step up to
// date so that a slow build is not a blank box in the browser for minutes. It
// reports false if the timeout ran out first. Everything here runs on the one
// goroutine that owns the step, so nothing else can be writing to it.
func (e *Engine) awaitExit(ctx context.Context, handle *term.Handle, step *store.Step, timeout time.Duration) bool {
	limit := time.Now().Add(timeout)
	ticker := time.NewTicker(700 * time.Millisecond)
	defer ticker.Stop()
	shown := ""
	for {
		if !handle.Alive() {
			return true
		}
		if time.Now().After(limit) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			if screen := handle.State().Screen; screen != shown {
				shown = screen
				step.Body = screen
				e.updateStep(step)
			}
		}
	}
}

// shellCommand wraps a command line so the shell interprets it, which is what
// makes pipes, redirections and globs work.
func shellCommand(command string) (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd.exe", []string{"/c", command}
	}
	shell := os.Getenv("SHELL")
	if strings.TrimSpace(shell) == "" {
		shell = "/bin/sh"
	}
	return shell, []string{"-lc", command}
}

// --------------------------------------------------------------- questions

// Ask puts a question to the user and blocks until it is answered.
func (e *Engine) Ask(ctx context.Context, run *store.Run, parent, kind, question string, options []store.Option, freeText bool) (string, error) {
	q := &store.Question{
		ID:       newID("q"),
		ChatID:   run.ChatID,
		RunID:    run.ID,
		Kind:     kind,
		Question: question,
		Options:  options,
		Status:   store.StatusPending,
	}
	step := e.addStep(run, parent, store.StepQuestion, "Waiting for you", question, store.StatusPending, map[string]any{
		"options":   options,
		"free_text": freeText,
		"kind":      kind,
	})
	q.StepID = step.ID
	if err := e.Store.CreateQuestion(q); err != nil {
		return "", err
	}
	// The browser needs the question id to post the answer back.
	step.Detail = mustJSON(map[string]any{
		"options":     options,
		"free_text":   freeText,
		"kind":        kind,
		"question_id": q.ID,
	})
	e.updateStep(step)
	_ = e.Store.SetRunStatus(run.ID, store.RunWaiting, "")
	if r, err := e.Store.GetRun(run.ID); err == nil {
		e.publish(run.ChatID, Event{Type: "run", Run: r})
	}
	e.publish(run.ChatID, Event{Type: "question", Question: q})

	ch := make(chan string, 1)
	e.mu.Lock()
	e.waiters[q.ID] = ch
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.waiters, q.ID)
		e.mu.Unlock()
	}()

	var answer string
	select {
	case answer = <-ch:
	case <-ctx.Done():
		_ = e.Store.AnswerQuestion(q.ID, "", store.StatusCancelled)
		step.Status = store.StatusCancelled
		step.Detail = mustJSON(map[string]any{
			"options":     options,
			"free_text":   freeText,
			"kind":        kind,
			"question_id": q.ID,
		})
		e.updateStep(step)
		return "", ctx.Err()
	}

	step.Status = store.StatusAnswered
	step.Detail = mustJSON(map[string]any{
		"options":     options,
		"free_text":   freeText,
		"kind":        kind,
		"question_id": q.ID,
		"answer":      answer,
	})
	e.updateStep(step)
	_ = e.Store.SetRunStatus(run.ID, store.RunRunning, "")
	if r, err := e.Store.GetRun(run.ID); err == nil {
		e.publish(run.ChatID, Event{Type: "run", Run: r})
	}
	return answer, nil
}

// Answer resolves a pending question. Called by the HTTP API.
func (e *Engine) Answer(id, value string) error {
	q, err := e.Store.GetQuestion(id)
	if err != nil {
		return err
	}
	if q.Status != store.StatusPending {
		return fmt.Errorf("this question was already answered")
	}
	if err := e.Store.AnswerQuestion(id, value, store.StatusAnswered); err != nil {
		return err
	}
	q.Status = store.StatusAnswered
	q.Answer = value
	e.publish(q.ChatID, Event{Type: "question", Question: q})

	e.mu.Lock()
	ch := e.waiters[id]
	e.mu.Unlock()
	if ch == nil {
		return fmt.Errorf("nobody is waiting for this question any more")
	}
	select {
	case ch <- value:
	default:
	}
	return nil
}

// ------------------------------------------------------------------ utils

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(`{}`)
	}
	return b
}

func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max]) + "…"
}

// truncateMiddle keeps the head and the tail of a long tool result.
func truncateMiddle(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	head := max * 2 / 3
	tail := max - head
	return string(r[:head]) + "\n\n… [" + fmt.Sprintf("%d characters omitted", len(r)-max) + "] …\n\n" + string(r[len(r)-tail:])
}

func firstLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return truncate(s, max)
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}
