// Package agent implements the top level orchestration loop: it talks to the
// model over OpenRouter, delegates work to the coding agents, asks the user
// when it needs a decision, and streams every step to the browser.
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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/backends"
	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/openrouter"
	"github.com/saschazesiger/SocratesAgent/internal/store"
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

	// SelfPath and Bridge* are used to launch the MCP permission bridge for
	// delegate agents that run in interactive approval mode.
	SelfPath    string
	BridgeURL   string
	BridgeToken string

	mu      sync.Mutex
	active  map[string]*runHandle
	waiters map[string]chan string
}

// New creates an engine.
func New(st *store.Store, bus *Bus, settings func() config.Settings) *Engine {
	self, _ := os.Executable()
	return &Engine{
		Store:    st,
		Bus:      bus,
		Settings: settings,
		SelfPath: self,
		active:   map[string]*runHandle{},
		waiters:  map[string]chan string{},
	}
}

func newID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + hex.EncodeToString(b[:])
}

// NewToken returns a random secret, used for the bridge handshake.
func NewToken() string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
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
	b.WriteString("\n\n## Available delegate agents\n")
	enabled := settings.EnabledBackends()
	if len(enabled) == 0 {
		b.WriteString("None are configured right now. Answer from your own knowledge and tell the user " +
			"that no delegate agents are enabled in the admin dashboard.\n")
	}
	for _, be := range enabled {
		fmt.Fprintf(&b, "- `%s` (%s): %s\n", be.ID, be.Name, strings.TrimSpace(be.Description))
	}
	fmt.Fprintf(&b, "\n## Working directory\nDelegate agents run in `%s`. Mention paths relative to it.\n",
		e.workspaceFor(chat, settings))
	fmt.Fprintf(&b, "\n## Context\nCurrent date: %s.\n", time.Now().Format("2006-01-02"))
	if run != nil && run.Auto {
		b.WriteString("\n## Voice mode\nThe user is in hands free voice mode. Your final answer is read out " +
			"loud: keep it under roughly 120 words, use plain sentences, no markdown, no code blocks, no lists " +
			"of file paths. When you use ask_user, keep every option to a few spoken words.\n")
	}
	return b.String()
}

// workspaceFor returns the directory delegate agents work in for a chat.
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
	e.publish(run.ChatID, Event{Type: "step", Step: st})
	return st
}

func (e *Engine) updateStep(st *store.Step) {
	if err := e.Store.PutStep(st); err != nil {
		log.Printf("agent: update step: %v", err)
		return
	}
	e.publish(st.ChatID, Event{Type: "step", Step: st})
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

// ------------------------------------------------------------- delegation

func (e *Engine) runDelegate(ctx context.Context, chat *store.Chat, run *store.Run, backend config.Backend, task, title string) (string, error) {
	settings := e.Settings()
	workdir := e.workspaceFor(chat, settings)
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		return "", fmt.Errorf("create workspace %s: %w", workdir, err)
	}

	label := strings.TrimSpace(title)
	if label == "" {
		label = firstLine(task, 90)
	}
	parent := e.addStep(run, "", store.StepDelegate, backend.Name, label, store.StatusRunning, map[string]any{
		"agent":      backend.ID,
		"agent_name": backend.Name,
		"task":       task,
		"workspace":  workdir,
		"approval":   backend.Approval,
	})

	// Child steps are keyed by the agent's own event ids so updates land on
	// the same row (a tool call that finishes later, for example).
	var childMu sync.Mutex
	children := map[string]*store.Step{}
	started := time.Now()

	emit := func(ev backends.Event) {
		childMu.Lock()
		defer childMu.Unlock()
		key := ev.ID
		if key == "" {
			key = newID("ev")
		}
		st, ok := children[key]
		if !ok {
			st = &store.Step{
				ID: newID("step"), RunID: run.ID, ChatID: run.ChatID, ParentID: parent.ID,
				Seq: e.nextSeq(run), Kind: subKind(ev.Kind), CreatedAt: time.Now().UnixMilli(),
			}
			children[key] = st
		}
		st.Title = ev.Title
		st.Body = ev.Body
		st.Status = ev.Status
		if st.Status == "" {
			st.Status = store.StatusDone
		}
		if ev.Detail != nil {
			st.Detail = mustJSON(ev.Detail)
		}
		e.updateStep(st)
	}

	req := backends.Request{
		Backend:     backend,
		Prompt:      task,
		Workdir:     workdir,
		SelfPath:    e.SelfPath,
		BridgeURL:   e.BridgeURL,
		BridgeToken: e.BridgeToken,
		RunID:       run.ID,
		StepID:      parent.ID,
	}
	res, runErr := backends.Run(ctx, req, emit)

	detail := map[string]any{
		"agent":       backend.ID,
		"agent_name":  backend.Name,
		"task":        task,
		"workspace":   workdir,
		"approval":    backend.Approval,
		"duration_ms": time.Since(started).Milliseconds(),
	}
	if res != nil {
		detail["result"] = truncate(res.Text, 4000)
		detail["exit_code"] = res.ExitCode
		for k, v := range res.Meta {
			detail[k] = v
		}
	}
	parent.Detail = mustJSON(detail)

	// Any child still marked running was cut short with the process.
	childMu.Lock()
	for _, st := range children {
		if st.Status == store.StatusRunning {
			st.Status = store.StatusInterrupted
			e.updateStep(st)
		}
	}
	childMu.Unlock()

	if runErr != nil {
		parent.Status = store.StatusFailed
		detail["error"] = runErr.Error()
		parent.Detail = mustJSON(detail)
		e.updateStep(parent)
		if errors.Is(runErr, context.Canceled) {
			return "", runErr
		}
		return "", runErr
	}
	parent.Status = store.StatusDone
	e.updateStep(parent)
	if res == nil {
		return "", fmt.Errorf("no result")
	}
	return res.Text, nil
}

func subKind(kind string) string {
	switch kind {
	case backends.EventThinking:
		return "sub_thinking"
	case backends.EventText:
		return "sub_text"
	case backends.EventTool:
		return "sub_tool"
	case backends.EventError:
		return "sub_error"
	case backends.EventLog:
		return "sub_log"
	default:
		return "sub_status"
	}
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

// RequestPermission is called by the MCP bridge when a delegate agent wants to
// use a tool while running in interactive approval mode.
func (e *Engine) RequestPermission(ctx context.Context, runID, stepID, agentName, toolName, inputSummary string) (bool, string, error) {
	run, err := e.Store.GetRun(runID)
	if err != nil {
		return false, "", err
	}
	question := fmt.Sprintf("%s wants to use %s", orDefault(agentName, "The agent"), toolName)
	if strings.TrimSpace(inputSummary) != "" {
		question += ":\n" + truncate(inputSummary, 800)
	}
	options := []store.Option{
		{Value: "allow", Label: "Allow"},
		{Value: "deny", Label: "Deny"},
	}
	answer, err := e.Ask(ctx, run, stepID, "permission", question, options, false)
	if err != nil {
		return false, "", err
	}
	if strings.EqualFold(answer, "allow") || strings.EqualFold(answer, "yes") {
		return true, "", nil
	}
	return false, "The user denied this action. Continue without it or explain what you need.", nil
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
