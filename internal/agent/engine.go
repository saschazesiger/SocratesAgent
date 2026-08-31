// Package agent implements the top level orchestration loop: it talks to the
// model over OpenRouter, works at a real terminal on the user's machine - which
// is also how it drives the coding agents - and streams every step to the
// browser.
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
	Type    string         `json:"type"`
	Step    *store.Step    `json:"step,omitempty"`
	StepID  string         `json:"step_id,omitempty"`
	Run     *store.Run     `json:"run,omitempty"`
	Message *store.Message `json:"message,omitempty"`
	Chat    *store.Chat    `json:"chat,omitempty"`
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

	// commitMu makes writing a row and announcing it one indivisible step. The
	// browser treats the highest revision it has seen as "everything up to
	// here is mine", and that is only true if events reach it in the order the
	// revisions were handed out.
	commitMu sync.Mutex

	mu     sync.Mutex
	active map[string]*runHandle
	// screens remembers, per session, the last meaningful screen a wait saw
	// and when it last changed. It is what lets the answer guard tell a
	// program that is thinking from a dialog that has been sitting there.
	screens map[string]screenMark
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
		watching:  map[string]bool{},
		screens:   map[string]screenMark{},
	}
}

// screenMark is one session's screen as a wait last saw it.
type screenMark struct {
	norm    string
	changed time.Time
}

// markScreen records what a wait saw, so a later look can tell how long the
// screen has been standing still.
func (e *Engine) markScreen(id, norm string, changed time.Time) {
	if id == "" || changed.IsZero() {
		return
	}
	e.mu.Lock()
	e.screens[id] = screenMark{norm: norm, changed: changed}
	e.mu.Unlock()
}

// changedAt returns when this session's screen last changed, as far as this
// process knows, or the zero time when the screen has moved since - or was
// never watched at all.
func (e *Engine) changedAt(id, norm string) time.Time {
	e.mu.Lock()
	mark, ok := e.screens[id]
	e.mu.Unlock()
	if !ok || mark.norm != norm {
		return time.Time{}
	}
	return mark.changed
}

// forgetScreen drops what was remembered about a session that is going away.
func (e *Engine) forgetScreen(id string) {
	e.mu.Lock()
	delete(e.screens, id)
	e.mu.Unlock()
}

func newID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + hex.EncodeToString(b[:])
}

func (e *Engine) publish(chatID string, ev Event) { e.Bus.Publish(chatID, ev) }

// commitStep writes a step and publishes it without letting another writer in
// between, so revision order and delivery order are the same thing.
func (e *Engine) commitStep(st *store.Step) error {
	e.commitMu.Lock()
	defer e.commitMu.Unlock()
	if err := e.Store.PutStep(st); err != nil {
		return err
	}
	e.publish(st.ChatID, Event{Type: "step", Step: copyStep(st)})
	return nil
}

// commitMessage does the same for a visible chat bubble.
func (e *Engine) commitMessage(m *store.Message) error {
	e.commitMu.Lock()
	defer e.commitMu.Unlock()
	if err := e.Store.AddMessage(m); err != nil {
		return err
	}
	e.publish(m.ChatID, Event{Type: "message", Message: m})
	return nil
}

// commitStepRemoval retires a step. A deletion carries no revision of its own,
// which is why a reconnecting client is also told which steps still exist.
func (e *Engine) commitStepRemoval(chatID, stepID string) error {
	e.commitMu.Lock()
	defer e.commitMu.Unlock()
	if err := e.Store.DeleteStep(stepID); err != nil {
		return err
	}
	e.publish(chatID, Event{Type: "step_removed", StepID: stepID})
	return nil
}

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

// Turn is one user message on its way into a chat: what was typed, whether
// the browser is in hands free mode, and the key that makes sending it safe to
// repeat over a connection that may drop mid request.
type Turn struct {
	ChatID   string
	Text     string
	Auto     bool
	ClientID string
}

// Start records the user message and launches the orchestration loop. A turn
// that carries a ClientID is idempotent: sending it again - because the phone
// lost signal before the response came back - returns the run that already
// exists instead of saying the same thing twice.
func (e *Engine) Start(turn Turn) (*store.Run, error) {
	chatID, text := turn.ChatID, turn.Text
	chat, err := e.Store.GetChat(chatID)
	if err != nil {
		return nil, err
	}
	if existing, err := e.Store.MessageByClientID(chatID, turn.ClientID); err == nil {
		if run, err := e.Store.GetRun(existing.RunID); err == nil {
			return run, nil
		}
		return &store.Run{ID: existing.RunID, ChatID: chatID, Status: store.RunDone}, nil
	}
	e.mu.Lock()
	if _, busy := e.active[chatID]; busy {
		e.mu.Unlock()
		return nil, ErrBusy
	}
	ctx, cancel := context.WithCancel(context.Background())
	run := &store.Run{ID: newID("run"), ChatID: chatID, Status: store.RunRunning, Auto: turn.Auto}
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

	msg := &store.Message{
		ID: newID("msg"), ChatID: chatID, RunID: run.ID,
		Role: "user", Content: text, ClientID: turn.ClientID,
	}
	if err := e.Store.AppendLLMMessage(chatID, mustJSON(openrouter.Message{Role: "user", Content: text})); err != nil {
		return fail(err)
	}
	if err := e.Store.CreateRun(run); err != nil {
		return fail(err)
	}
	if err := e.commitMessage(msg); err != nil {
		return fail(err)
	}
	_ = e.Store.TouchChat(chatID)
	// Talking to an archived chat is what brings it back. Nothing else has to
	// be restored: archiving only ended what was running, and this turn starts
	// whatever it needs itself.
	if chat.Archived {
		if err := e.Store.SetChatArchived(chatID, false); err == nil {
			chat.Archived, chat.ArchivedAt = false, 0
			e.publish(chatID, Event{Type: "chat", Chat: chat})
		}
	}

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
	// holds counts how many times in a row the model was sent back to the
	// terminal instead of being allowed to answer.
	holds := 0
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
			// An answer given while the program is still generating is the
			// one failure a user cannot check: it reads plausible and it is
			// out of date. So the run does not end here until the terminals
			// it opened are quiet.
			note := e.busyHold(run, holds)
			if ctx.Err() != nil {
				e.finish(run, chat, store.RunCancelled, "", "")
				return
			}
			if note != "" {
				holds++
				answer.Finish(store.StatusDone)
				// The note goes in as a user turn: a provider is free to
				// hoist a system message to the top of the conversation,
				// where this one would read as a standing instruction rather
				// than as what it is, an interruption at this exact point.
				if err := e.Store.AppendLLMMessage(chat.ID,
					mustJSON(openrouter.Message{Role: "user", Content: note})); err != nil {
					log.Printf("agent: persist busy note: %v", err)
				}
				continue
			}
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
		// The count is about consecutive attempts to answer too early, so
		// doing real work in between clears it - but a terminal_wait that
		// came back "still working" is not progress, it is the same standoff
		// continuing, and it must not buy three more holds.
		progress := false

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
			if call.Function.Name != toolTerminalWait || !strings.Contains(out, "Status: "+waitBusy) {
				progress = true
			}
			toolMsg := openrouter.Message{Role: "tool", ToolCallID: call.ID, Name: call.Function.Name, Content: out}
			if err := e.Store.AppendLLMMessage(chat.ID, mustJSON(toolMsg)); err != nil {
				log.Printf("agent: persist tool message: %v", err)
			}
		}
		if progress {
			holds = 0
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

// maxBusyHolds is how often one answer may be held back. Each hold costs a
// model call, and a model that has been sent back to the terminal three times
// running is not going to be talked round by a fourth note - at that point the
// user is better served by the answer plus a warning than by a run that never
// ends. The count only resets when the model does something other than ask a
// still working session whether it is done yet.
const maxBusyHolds = 3

// busyHold looks at the terminals this run opened and returns the note to send
// the model instead of letting it answer, or an empty string when it may
// speak. Only a skill with a busy pattern can hold anything back: a program
// with no way of saying "still working" is never guessed at, which is why an
// ad hoc shell session never delays an answer.
func (e *Engine) busyHold(run *store.Run, holds int) string {
	if e.Terminals == nil {
		return ""
	}
	var busy []string
	for _, handle := range e.Terminals.List(run.ChatID) {
		if handle.Meta(metaRun) != run.ID || !handle.Alive() {
			continue
		}
		skill := e.skillOfSession(handle)
		if !skill.HoldsReply() {
			continue
		}
		pattern := skill.Busy()
		if pattern == nil {
			continue
		}
		norm := normaliseScreen(handle.State().Screen)
		working, matched := stillWorking(handle, pattern, e.changedAt(handle.ID(), norm), skill.Idle())
		if !working {
			continue
		}
		busy = append(busy, fmt.Sprintf("`%s` (%s): the screen matches %q",
			handle.ID(), handle.Name(), matched))
	}
	if len(busy) == 0 {
		return ""
	}
	if holds >= maxBusyHolds {
		// Held three times already. Rather than leaving the user with nothing,
		// the answer goes out - and the process view says what it is worth.
		e.addStep(run, "", store.StepText, "Answered while a program was still working",
			fmt.Sprintf("Socrates was sent back to the terminal %d times and still wanted to answer, "+
				"so the answer was allowed through. Still working:\n%s", holds, strings.Join(busy, "\n")),
			store.StatusInterrupted, nil)
		return ""
	}
	e.addStep(run, "", store.StepText, "Waiting for the program",
		"The answer was held back because a terminal session is still working:\n"+strings.Join(busy, "\n"),
		store.StatusDone, nil)
	return "[orchestrator] The program in " + strings.Join(busy, "; ") + " is still working, so the " +
		"screen you just read is not its result. Do not answer the user yet: call terminal_wait on that " +
		"session until it reports `idle`, or answer the question on its screen. If the screen shows a " +
		"question only the user can answer, say so and stop."
}

// finish closes a run, optionally writing the visible answer.
func (e *Engine) finish(run *store.Run, chat *store.Chat, status, errMsg, answer string) {
	if answer != "" {
		msg := &store.Message{ID: newID("msg"), ChatID: chat.ID, RunID: run.ID, Role: "assistant", Content: answer}
		if err := e.commitMessage(msg); err != nil {
			log.Printf("agent: store answer: %v", err)
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
	b.WriteString("Each of these is a skill: a program someone has written down how to operate, " +
		"the way you would brief a new colleague on it. Read the skill before you use it.\n")
	enabled := settings.EnabledSkills()
	if len(enabled) == 0 {
		b.WriteString("No skills are configured. You still have a shell, so you can do the work " +
			"yourself with ordinary commands - and tell the user that no skills are enabled in the " +
			"admin dashboard.\n")
	}
	for _, sk := range enabled {
		fmt.Fprintf(&b, "\n### %s (skill `%s`)\n%s\n", sk.Name, sk.ID, strings.TrimSpace(sk.Description))
		command, args := sk.CommandLine()
		fmt.Fprintf(&b, "Started as: `%s`\n", strings.TrimSpace(command+" "+strings.Join(args, " ")))
		// Only worth saying for a program that has an unattended mode at all:
		// anything else simply behaves the way it behaves.
		if !sk.SkipPermissions && (len(sk.AskArgs) > 0 || len(sk.SkipArgs) > 0) {
			b.WriteString("It will ask before it changes anything, so expect permission prompts on the " +
				"screen and answer them.\n")
		}
		for _, section := range []struct{ title, body string }{
			{"Starting it", sk.Startup},
			{"Giving it a task", sk.GivingTasks},
			{"Reading its state", sk.ReadingState},
			{"Answering its questions", sk.Answering},
			{"Interrupting and quitting", sk.Exiting},
			{"Notes", sk.Notes},
		} {
			if body := strings.TrimSpace(section.body); body != "" {
				fmt.Fprintf(&b, "\n**%s.** %s\n", section.title, body)
			}
		}
		if sk.Interactive() {
			fmt.Fprintf(&b, "\n**Interactive only.** Use it solely through terminal_open with "+
				"`skill: \"%s\"`. Never run it through shell_run", sk.ID)
			// The list is written as a sentence and ends in a full stop, which
			// would look wrong inside the brackets.
			if forms := strings.TrimRight(strings.TrimSpace(sk.HeadlessForms), "."); forms != "" {
				fmt.Fprintf(&b, " and never use its non-interactive forms (%s)", forms)
			}
			b.WriteString(". The user is watching the terminal and wants to read along and take " +
				"over, which only the real interactive session gives them.\n")
		} else if usage := strings.TrimSpace(sk.HeadlessUsage); usage != "" {
			fmt.Fprintf(&b, "\n**Also usable non-interactively** via shell_run: %s\n", usage)
		} else {
			b.WriteString("\n**Also usable non-interactively** via shell_run when a terminal session " +
				"would be overkill.\n")
		}
	}

	// The internet paragraph is rendered rather than stored, so it appears
	// exactly when the two tools do and never describes a capability that is
	// switched off.
	if settings.Internet.Enabled {
		b.WriteString("\n## Reading the web\n")
		b.WriteString(config.InternetPrompt)
		b.WriteString("\n")
	}

	b.WriteString("\n## Driving a program\n" +
		"Open it with terminal_open, wait for it to finish starting, type the brief and submit it, " +
		"then wait for it to go idle and read the screen. If it asks something, answer it: a menu is " +
		"answered with the arrow keys and enter, or by typing the number next to the choice. " +
		"Never assume a keypress worked - the screen comes back with every call, so look at it. " +
		"When the skill tells you which keys a dialog takes, use those rather than guessing.\n" +
		"Never report a result while the program is still working. terminal_wait answers with a status: " +
		"`idle` means it has finished its turn and the screen is worth reading; `still working` means it " +
		"has not, however finished the screen looks - call terminal_wait again, as often as it takes, and " +
		"say nothing to the user in the meantime. A spinner, a ticking timer or a line like \"esc to " +
		"interrupt\" is the program telling you it is mid-thought. The only two reasons to stop waiting " +
		"are an `idle` status and a question or menu on the screen that is waiting for your answer.\n")

	if sessions := e.sessionSummary(chat); sessions != "" {
		b.WriteString("\n## Open terminal sessions\n")
		b.WriteString(sessions)
	}

	fmt.Fprintf(&b, "\n## Context\nCurrent date: %s.\n", time.Now().Format("2006-01-02"))

	// The answer is read out loud by a voice that speaks one language. An
	// English answer coming out of a German voice is the worst of both, so the
	// language the user chose for speech is also the language to write in.
	fmt.Fprintf(&b, "\n## Language\nWrite every message to the user in %s, whatever language the "+
		"terminal output or the code you are reading happens to be in. Only quote a command, a "+
		"path or an error message in its original wording.\n",
		config.LanguageName(settings.Voice.Language))
	if run != nil && run.Auto {
		b.WriteString("\n## Voice mode\nThe user is in hands free voice mode. Your final answer is read out " +
			"loud: keep it under roughly 120 words, use plain sentences, no markdown, no code blocks, no lists " +
			"of file paths. If you need a decision from the user, ask for it in one short spoken " +
			"sentence and end your turn - they will answer out loud.\n")
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
	if err := e.commitStep(st); err != nil {
		log.Printf("agent: put step: %v", err)
	}
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
	if err := e.commitStep(st); err != nil {
		log.Printf("agent: update step: %v", err)
	}
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
	if err := l.engine.commitStepRemoval(l.step.ChatID, l.step.ID); err != nil {
		log.Printf("agent: delete step: %v", err)
	}
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
	metaSkill = "skill"
	// metaLegacySkill is where the same id lived before skills had their name.
	metaLegacySkill = "tool"
	metaStep        = "step"
	metaRun         = "run"
	metaChat        = "chat"
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

// skillOfSession returns the skill a session was started from, or a zero
// skill with usable defaults for an ad hoc command.
func (e *Engine) skillOfSession(handle *term.Handle) config.Skill {
	settings := e.Settings()
	if skill, ok := settings.Skill(skillIDOf(handle)); ok {
		return skill
	}
	return config.Skill{}
}

// skillIDOf reads which skill a session came from. A session started before
// skills were called skills stored the id under the old key, and it may well
// still be running.
func skillIDOf(handle *term.Handle) string {
	if id := handle.Meta(metaSkill); id != "" {
		return id
	}
	return handle.Meta(metaLegacySkill)
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
func (e *Engine) openTerminal(ctx context.Context, chat *store.Chat, run *store.Run, skillID, command, name, dir string) string {
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
	var skill config.Skill

	switch {
	case strings.TrimSpace(skillID) != "":
		found, ok := settings.Skill(strings.TrimSpace(skillID))
		if !ok || !found.Enabled {
			names := []string{}
			for _, sk := range settings.EnabledSkills() {
				names = append(names, sk.ID)
			}
			if len(names) == 0 {
				return fmt.Sprintf("There is no enabled skill called %q, and none are configured. "+
					"Use `command` to run something directly.", skillID)
			}
			return fmt.Sprintf("There is no enabled skill called %q. Available: %s.",
				skillID, strings.Join(names, ", "))
		}
		skill = found
		spec.Command, spec.Args = skill.CommandLine()
		spec.Env = skill.Env
		spec.Cols, spec.Rows = skill.Cols, skill.Rows
		spec.Meta[metaSkill] = skill.ID
		if label == "" {
			label = skill.Name
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
		"skill":     spec.Meta[metaSkill],
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
	if pattern := strings.TrimSpace(skill.ReadyPattern); pattern != "" {
		_, _, _ = handle.WaitFor(ctx, pattern, 45*time.Second)
	} else {
		_, _, _ = handle.WaitIdle(ctx, 2500*time.Millisecond, 45*time.Second)
	}

	note := fmt.Sprintf("Started %s as session `%s` in %s.", label, handle.ID(), workdir)
	if startup := strings.TrimSpace(skill.Startup); startup != "" {
		note += "\nStarting it: " + startup
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
				"skill":     handle.Meta(metaSkill),
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
					// The program is gone; what its screen looked like, and
					// when it last moved, is of no further use to anyone.
					e.forgetScreen(handle.ID())
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
