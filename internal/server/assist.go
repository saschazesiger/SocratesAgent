package server

// Two features that talk *about* a terminal rather than to it: the Status
// button, which has a model read the screen and say what it means, and the
// Agent button, which has a model read the same screen and decide what to
// type. Both are server side on purpose - the operator loop outlives the
// browser, so a phone that locks in a pocket comes back to a finished run -
// and both read the pane through termux.CapturePane, which returns text with
// no escape sequences in it at all.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
	"github.com/saschazesiger/SocratesAgent/internal/openrouter"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// What the two features read and how far they may go.
const (
	// statusScreenLines is what the spoken summary is given. It is a screen
	// and a little history: enough to see how the last answer started, not so
	// much that a scrollback is being summarised.
	statusScreenLines = 120
	// agentScreenLines is what the operator sees before every decision. It is
	// larger because a menu, a diff and a permission prompt can all be on the
	// same screen and the interesting half is often above the fold.
	agentScreenLines = 200

	// agentMaxWall is the whole run, whatever it is doing. MaxSteps bounds how
	// often it may think; this bounds how long a build it is watching may take.
	agentMaxWall = 10 * time.Minute
	// agentMaxActions is how many keystrokes one step may ask for. Past this
	// the model is not taking a small step and looking again, it is typing a
	// program blind.
	agentMaxActions = 8
	// agentMaxTextRunes bounds one typed string.
	agentMaxTextRunes = 2000
	// agentActionGap is the pause between two actions. A TUI that is repainting
	// a menu drops keys sent inside one frame, and 120 ms is a rhythm a human
	// hand would have anyway.
	agentActionGap = 120 * time.Millisecond
	// agentFirstWait is how long a run waits at the start for a harness that is
	// still working. After that it looks anyway: the screen says it is busy and
	// the model may legitimately answer with Escape.
	agentFirstWait = 120 * time.Second
	// agentUnknownWait applies only when the detector has no answer at all.
	// There is no turn to wait for, so the wait is bounded rather than open.
	agentUnknownWait = 90 * time.Second
	// agentSettle is two detector ticks. Straight after send-keys the committed
	// state is up to a tick old, so "not busy" can still be the answer to the
	// screen before the keys landed.
	agentSettle = 2 * termux.ActivityInterval
	// agentHistorySteps is how many rounds of screen-and-decision the model is
	// shown. The screens are large, they repeat, and the newest one is the one
	// that matters.
	agentHistorySteps = 4
	// agentKeepDone is how long a finished run stays answerable. It is not a
	// log: it is the phone that locked during step two and unlocks after the
	// run ended, which is the ordinary way this feature is used.
	agentKeepDone = 2 * time.Minute

	// agentStatusTimeout bounds one spoken status. It is a short answer from a
	// fast model with a person waiting for it.
	agentStatusTimeout = 90 * time.Second
)

// The phases a run reports. They are the vocabulary the progress line renders.
const (
	phaseThinking = "thinking"
	phaseActing   = "acting"
	phaseWaiting  = "waiting"
	phaseDone     = "done"
	phaseError    = "error"
)

/* ------------------------------------------------------------- the status */

// handleSessionStatus says out loud what a terminal is doing.
//
// The audio is not here: the page takes the text and posts it to
// /api/voice/speak, which already owns the length scaled deadline, the "Piper
// is still installing" answer and the stop logic - and 150 KB of WAV has no
// business in a JSON body when the same sentence is also shown on screen.
func (s *Server) handleSessionStatus(w http.ResponseWriter, r *http.Request) {
	row, ok := s.session(w, r)
	if !ok {
		return
	}
	settings := s.Settings()
	if strings.TrimSpace(settings.OpenRouter.APIKey) == "" {
		writeError(w, http.StatusBadRequest,
			"no OpenRouter API key is configured - open /admin and add your key")
		return
	}
	model := strings.TrimSpace(settings.OpenRouter.StatusModel)
	if model == "" {
		model = config.DefaultStatusModel
	}

	ctx, cancel := context.WithTimeout(r.Context(), agentStatusTimeout)
	defer cancel()
	screen, err := s.manager.CapturePane(ctx, row.ID, statusScreenLines)
	if err != nil {
		writeError(w, http.StatusBadRequest, "this session has no terminal to read: "+err.Error())
		return
	}
	activity := s.manager.ActivityOf(row.ID)
	language := config.NormalizeLanguage(settings.Voice.Language)

	temperature := 0.2
	client := openrouter.New(settings.OpenRouter.BaseURL, settings.OpenRouter.APIKey)
	res, err := client.Chat(ctx, openrouter.ChatRequest{
		Model:       model,
		Messages:    []openrouter.Message{{Role: "user", Content: statusPrompt(row.Harness, activity.State, language, screen)}},
		Temperature: &temperature,
		MaxTokens:   300,
	}, nil)
	if err != nil {
		code, message := modelProblem(err, model, "status")
		writeError(w, code, message)
		return
	}
	text := plainStatus(res.Content)
	if text == "" {
		writeError(w, http.StatusBadGateway, "the model answered with nothing to say")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"text":     text,
		"language": language,
		"state":    string(activity.State),
		"model":    model,
	})
}

// statusPrompt is the whole instruction, screen included.
//
// The language is the one in the settings rather than the language of the
// screen: Piper reads with the voice of that setting, so a German sentence in
// the English voice would be worse than an English sentence. One setting,
// three sides of the conversation.
func statusPrompt(harness string, state termux.State, language, screen string) string {
	return "You are told what a terminal running " + harnessLabel(harness) +
		" currently shows. It is " + stateWords(state) + ". In one to three short sentences, say " +
		"what a person needs to know: if it is working, what it is working on; if it has finished, " +
		"what the answer is; if it is waiting, what it is asking and what the choices are. Speak " +
		"plainly - this is read out loud, so no markdown, no code, no file paths unless they are " +
		"the point. Answer in " + config.LanguageName(language) + ".\nScreen:\n" + screen
}

// stateWords is the committed state as a sentence fragment.
func stateWords(state termux.State) string {
	switch state {
	case termux.StateBusy:
		return "busy"
	case termux.StateIdle:
		return "idle"
	case termux.StateWaiting:
		return "waiting for the user"
	default:
		return "in an unknown state"
	}
}

// plainStatus takes the markdown back off an answer that was told not to use
// any. It is read out loud, and a voice pronounces an asterisk.
func plainStatus(text string) string {
	text = strings.TrimSpace(text)
	text = strings.ReplaceAll(text, "**", "")
	text = strings.ReplaceAll(text, "`", "")
	return strings.TrimSpace(text)
}

// harnessLabel is the name of the program in the pane, as a person would say it.
func harnessLabel(id string) string {
	if h, ok := harnesses.Get(id); ok {
		return h.Label()
	}
	return "a terminal"
}

// modelProblem turns an OpenRouter failure into a status and a sentence a
// person can act on.
//
// A refusal OpenRouter blames on the request - a model id that does not exist,
// most of all - is a 4xx and has to say which id and where to change it.
// Nothing in this app validates a chat model id: there is no catalogue check
// on save, so "unknown model" is a sentence the dashboard is the answer to,
// not a bug report.
func modelProblem(err error, model, kind string) (int, string) {
	code := upstreamStatus(err)
	if code == http.StatusBadRequest {
		return code, fmt.Sprintf("unknown model %s - open /admin and pick a %s model. OpenRouter said: %s",
			model, kind, err.Error())
	}
	return code, err.Error()
}

/* -------------------------------------------------------- the operator run */

// agentRun is one operator run, live or just finished. It is the frame the
// page renders, with the machinery that drives it attached.
type agentRun struct {
	id      string
	session string
	prompt  string
	started int64

	mu      sync.Mutex
	step    int
	phase   string
	action  string
	note    string
	summary string
	failure string
	done    bool
	ended   time.Time
	cancel  context.CancelFunc
	// reason is what a cancellation was, recorded before the context is torn
	// down so that the loop can tell "the user pressed Cancel" from "ten
	// minutes are up".
	reason string
}

// view is the run as the API and the WebSocket both carry it: the agent frame
// without its "t", so that the page renders a run from a frame, from hello or
// from GET with one function.
func (r *agentRun) view() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.viewLocked()
}

func (r *agentRun) viewLocked() map[string]any {
	return map[string]any{
		"run_id":  r.id,
		"step":    r.step,
		"phase":   r.phase,
		"action":  r.action,
		"note":    r.note,
		"done":    r.done,
		"error":   r.failure,
		"prompt":  r.prompt,
		"summary": r.summary,
		"started": r.started,
	}
}

// agentDriver owns the runs. One per session at a time, in a goroutine that
// belongs to the server rather than to the request that started it.
type agentDriver struct {
	srv *Server

	mu   sync.Mutex
	runs map[string]*agentRun
	// keep is how long a finished run is still answered for. A field rather
	// than a constant so that a test can ask what "afterwards" looks like
	// without waiting two minutes for it.
	keep time.Duration
}

func newAgentDriver(s *Server) *agentDriver {
	return &agentDriver{srv: s, runs: map[string]*agentRun{}, keep: agentKeepDone}
}

// setKeep changes how long a finished run is answered for. It exists so that a
// test can ask what "afterwards" looks like without waiting two minutes for it.
func (d *agentDriver) setKeep(keep time.Duration) {
	d.mu.Lock()
	d.keep = keep
	d.mu.Unlock()
}

// runView answers with the run of one session, or nil. It is what the server
// installs as agentRunOf, so that hello and GET .../agent say the same thing.
func (d *agentDriver) runView(sessionID string) any {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	run, ok := d.runs[sessionID]
	keep := d.keep
	d.mu.Unlock()
	if !ok {
		return nil
	}
	run.mu.Lock()
	stale := run.done && !run.ended.IsZero() && time.Since(run.ended) > keep
	run.mu.Unlock()
	if stale {
		return nil
	}
	return run.view()
}

// begin registers a run, or refuses because one is already going.
func (d *agentDriver) begin(sessionID, prompt string) (*agentRun, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if current, ok := d.runs[sessionID]; ok {
		current.mu.Lock()
		live := !current.done
		current.mu.Unlock()
		if live {
			return current, false
		}
	}
	run := &agentRun{
		id:      termux.NewID(),
		session: sessionID,
		prompt:  prompt,
		started: time.Now().UnixMilli(),
		phase:   phaseWaiting,
		action:  "starting",
	}
	d.runs[sessionID] = run
	return run, true
}

// cancel ends the run of one session, if there is a live one. It is what the
// Cancel button calls, and what deleting a session calls: a run typing into a
// pane that is being torn down is the one way this feature could do damage
// nobody asked for.
func (d *agentDriver) cancel(sessionID, reason string) bool {
	if d == nil {
		return false
	}
	d.mu.Lock()
	run, ok := d.runs[sessionID]
	d.mu.Unlock()
	if !ok {
		return false
	}
	run.mu.Lock()
	live, cancel := !run.done, run.cancel
	if live {
		run.reason = reason
	}
	run.mu.Unlock()
	if !live || cancel == nil {
		return false
	}
	cancel()
	return true
}

// update changes a run and tells every viewer of its session at once. Every
// phase change goes through here, which is why the progress line never has to
// be polled.
func (d *agentDriver) update(run *agentRun, f func(*agentRun)) {
	run.mu.Lock()
	f(run)
	view := run.viewLocked()
	run.mu.Unlock()
	d.srv.emitAgent(run.session, view)
}

// finish ends a run one way or the other. failure empty is a run that got
// where it was going or stopped at a bound; failure set is a run that could
// not carry on, and the page turns it into a toast.
func (d *agentDriver) finish(run *agentRun, summary, note, failure string) {
	d.update(run, func(r *agentRun) {
		r.done, r.ended = true, time.Now()
		r.action = ""
		if summary != "" {
			r.summary = summary
		}
		if note != "" {
			r.note = note
		}
		if failure != "" {
			r.phase, r.failure = phaseError, failure
			return
		}
		r.phase = phaseDone
	})
}

/* ------------------------------------------------------------- the routes */

// handleAgentStart takes a goal and starts a run for it.
func (s *Server) handleAgentStart(w http.ResponseWriter, r *http.Request) {
	row, ok := s.session(w, r)
	if !ok {
		return
	}
	var body struct {
		Prompt string `json:"prompt"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	prompt := strings.TrimSpace(body.Prompt)
	if prompt == "" {
		writeError(w, http.StatusBadRequest, "say what the agent should do")
		return
	}
	settings := s.Settings()
	// The one policy lever. A shared deployment can keep the operator on the
	// three coding harnesses, which have a permission prompt of their own,
	// and off a shell, which has none.
	if row.Harness == config.HarnessShell && !settings.Agent.AllowShell {
		writeError(w, http.StatusBadRequest,
			"the agent is not allowed to drive a shell session here - turn it on in /admin")
		return
	}
	if row.State != store.StateRunning {
		writeError(w, http.StatusBadRequest, "this session has no running terminal to drive")
		return
	}
	if strings.TrimSpace(settings.OpenRouter.APIKey) == "" {
		writeError(w, http.StatusBadRequest,
			"no OpenRouter API key is configured - open /admin, add your key and pick an agent model")
		return
	}

	run, started := s.agents.begin(row.ID, prompt)
	if !started {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "this session is already being driven; cancel that run first",
			"run":   run.view(),
		})
		return
	}
	// The run belongs to the server, not to this request: the whole point is
	// that it keeps going with every browser closed.
	ctx, cancel := context.WithTimeout(context.Background(), agentMaxWall)
	run.mu.Lock()
	run.cancel = cancel
	run.mu.Unlock()
	go func() {
		defer cancel()
		s.agents.drive(ctx, run, row.ID, row.Harness, settings)
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{"run_id": run.id})
}

// handleAgentRun mirrors the live run, and answers null when there is none.
// A run lives only in memory, so this is also what a server restart answers.
func (s *Server) handleAgentRun(w http.ResponseWriter, r *http.Request) {
	row, ok := s.session(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": s.agents.runView(row.ID)})
}

// handleAgentCancel stops a run. It answers the same whether there was one to
// stop or not: the button is pressed by somebody who wants it stopped, and
// "there was nothing to stop" is not a failure they can do anything about.
func (s *Server) handleAgentCancel(w http.ResponseWriter, r *http.Request) {
	row, ok := s.session(w, r)
	if !ok {
		return
	}
	stopped := s.agents.cancel(row.ID, "the run was cancelled")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cancelled": stopped})
}

/* --------------------------------------------------------------- the loop */

// drive is the operator run: wait for the harness to be free, read the screen,
// ask the model what to do, do it, wait again.
//
// Everything that bounds it is here rather than in the model: the number of
// steps, the wall clock, how many keys one step may send, and the guards on
// which keys those may be. A model that decides to type for ever runs out of
// steps; a model that decides to type nothing at all ends the run.
func (d *agentDriver) drive(ctx context.Context, run *agentRun, sessionID, harness string, settings config.Settings) {
	client := openrouter.New(settings.OpenRouter.BaseURL, settings.OpenRouter.APIKey)
	model := strings.TrimSpace(settings.OpenRouter.AgentModel)
	if model == "" {
		model = config.DefaultAgentModel
	}
	maxSteps := settings.Agent.MaxSteps
	if maxSteps <= 0 {
		maxSteps = config.DefaultAgentMaxSteps
	}

	messages := []openrouter.Message{{
		Role:    "system",
		Content: agentSystemPrompt(harnessLabel(harness), run.prompt),
	}}
	carried := []string{}
	interrupts := 0
	lastInterrupt := false

	// The harness may still be finishing the last thing it was asked. Typing
	// into that would land halfway through somebody else's turn.
	d.update(run, func(r *agentRun) { r.phase, r.action = phaseWaiting, "waiting for the terminal" })
	first, cancelFirst := context.WithTimeout(ctx, agentFirstWait)
	_, waitErr := d.srv.manager.WaitIdle(first, sessionID)
	cancelFirst()
	if err := ctx.Err(); err != nil {
		d.finish(run, "", "", d.endReason(run, err))
		return
	}
	if waitErr != nil {
		carried = append(carried, "The terminal was still busy when this run started.")
	}

	for step := 1; step <= maxSteps; step++ {
		d.update(run, func(r *agentRun) {
			r.step, r.phase, r.action = step, phaseThinking, "reading the screen"
		})
		screen, err := d.srv.manager.CapturePane(ctx, sessionID, agentScreenLines)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				d.finish(run, "", "", d.endReason(run, ctxErr))
				return
			}
			d.finish(run, "", "", "the terminal is gone: "+err.Error())
			return
		}

		prompt := "Screen:\n" + screen
		if len(carried) > 0 {
			prompt = "About your last step: " + strings.Join(carried, " ") + "\n\n" + prompt
			carried = carried[:0]
		}
		messages = trimAgentHistory(append(messages, openrouter.Message{Role: "user", Content: prompt}))

		decision, raw, err := d.decide(ctx, client, model, messages)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				d.finish(run, "", "", d.endReason(run, ctxErr))
				return
			}
			var refusal *openrouter.StatusError
			if errors.As(err, &refusal) {
				_, message := modelProblem(err, model, "agent")
				d.finish(run, "", "", message)
				return
			}
			d.finish(run, "", "", err.Error())
			return
		}
		messages = append(messages, openrouter.Message{Role: "assistant", Content: raw})

		plan, notes := planActions(decision.Actions, harness, &interrupts, &lastInterrupt)
		d.update(run, func(r *agentRun) {
			r.phase, r.note = phaseActing, joinNote(decision.Note, notes)
			if len(plan) == 0 {
				r.action = "nothing to type"
			}
		})
		for _, act := range plan {
			if err := d.srv.manager.SendKeys(ctx, sessionID, []termux.Key{act.key}); err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					d.finish(run, "", "", d.endReason(run, ctxErr))
					return
				}
				d.finish(run, "", "", "the keys could not be typed: "+err.Error())
				return
			}
			// The same call the input path makes after a keystroke from a
			// browser. It lowers the unread mark - the operator has seen the
			// screen, so nobody has to - and it is what lets a `waiting` state
			// whose prompt is off the screen be released by quiescence: the
			// prompt was answered, and this is the record that it was.
			d.srv.manager.NoteInput(sessionID)
			d.update(run, func(r *agentRun) { r.phase, r.action = phaseActing, act.said })
		}
		carried = append(carried, notes...)

		if decision.Done {
			d.finish(run, strings.TrimSpace(decision.Summary), "", "")
			return
		}
		// Three interrupts in one run is a model fighting the terminal rather
		// than driving it, and the third one has just been sent.
		if interrupts >= 3 {
			d.finish(run, strings.TrimSpace(decision.Summary),
				"three interrupts in one run", "the agent kept interrupting the terminal; it was stopped")
			return
		}

		if err := d.waitForTurn(ctx, run, sessionID); err != nil {
			d.finish(run, "", "", d.endReason(run, err))
			return
		}
	}

	d.finish(run, fmt.Sprintf("stopped after %d steps without finishing", maxSteps),
		fmt.Sprintf("the step limit of %d was reached", maxSteps), "")
}

// endReason names the ending a context gave the run. Cancel and the wall clock
// are both a dead context and they are not the same news.
func (d *agentDriver) endReason(run *agentRun, err error) string {
	run.mu.Lock()
	reason := run.reason
	run.mu.Unlock()
	if reason != "" {
		return reason
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Sprintf("the run ran out of time after %s", agentMaxWall)
	}
	return "the run ended: " + err.Error()
}

// waitForTurn waits for what the keys started to be over.
//
// It is bounded by the run's own wall clock and by nothing else, because one
// long build is one step: a per step timeout would spend twelve steps on
// twelve screens that all say "still working". The only exception is a session
// the detector cannot read at all, where there is no turn to wait for and the
// wait is a bounded pause instead.
func (d *agentDriver) waitForTurn(ctx context.Context, run *agentRun, sessionID string) error {
	d.update(run, func(r *agentRun) { r.phase, r.action = phaseWaiting, "waiting for the terminal" })
	if err := sleepFor(ctx, agentSettle); err != nil {
		return err
	}
	if d.srv.manager.ActivityOf(sessionID).State == termux.StateUnknown {
		bounded, cancel := context.WithTimeout(ctx, agentUnknownWait)
		err := d.awaitKnown(bounded, sessionID)
		cancel()
		if err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
	}
	if _, err := d.srv.manager.WaitIdle(ctx, sessionID); err != nil {
		return err
	}
	return nil
}

// awaitKnown waits for the detector to have any answer at all.
func (d *agentDriver) awaitKnown(ctx context.Context, sessionID string) error {
	if d.srv.manager.ActivityOf(sessionID).State != termux.StateUnknown {
		return nil
	}
	changes, stop := d.srv.manager.SubscribeActivity()
	defer stop()
	if d.srv.manager.ActivityOf(sessionID).State != termux.StateUnknown {
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case change, ok := <-changes:
			if !ok {
				return nil
			}
			if change.SessionID == sessionID && change.Activity.State != termux.StateUnknown {
				return nil
			}
		}
	}
}

func sleepFor(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

/* ------------------------------------------------------- asking the model */

// agentAction is one thing to do: type characters, press a named key, or wait.
type agentAction struct {
	Text   string `json:"text"`
	Key    string `json:"key"`
	WaitMS int    `json:"wait_ms"`
}

// agentDecision is the whole answer, which is one JSON object and nothing else.
type agentDecision struct {
	Actions []agentAction `json:"actions"`
	Done    bool          `json:"done"`
	Summary string        `json:"summary"`
	Note    string        `json:"note"`
}

// errAgentJSON is the end of a run whose model will not answer with an object.
var errAgentJSON = errors.New("the operator model did not answer with usable JSON")

// decide asks the model once and, if the answer is not JSON, exactly once more
// with the parser's complaint attached.
//
// Once more and no further: a model that cannot produce an object twice in a
// row will not produce one on the fifth attempt either, and every attempt is a
// screen's worth of tokens.
func (d *agentDriver) decide(ctx context.Context, client *openrouter.Client, model string,
	messages []openrouter.Message) (agentDecision, string, error) {

	temperature := 0.0
	attempt := messages
	for try := 1; try <= 2; try++ {
		res, err := client.Chat(ctx, openrouter.ChatRequest{
			Model:       model,
			Messages:    attempt,
			Temperature: &temperature,
			MaxTokens:   800,
		}, nil)
		if err != nil {
			return agentDecision{}, "", err
		}
		decision, perr := parseDecision(res.Content)
		if perr == nil {
			return decision, res.Content, nil
		}
		if try == 2 {
			return agentDecision{}, "", errAgentJSON
		}
		attempt = append(append([]openrouter.Message{}, attempt...),
			openrouter.Message{Role: "assistant", Content: res.Content},
			openrouter.Message{Role: "user", Content: "That was not usable JSON: " + perr.Error() +
				". Answer with one JSON object and nothing else."})
	}
	return agentDecision{}, "", errAgentJSON
}

// parseDecision reads the object, with the fence a chat model puts round it
// taken off first.
func parseDecision(raw string) (agentDecision, error) {
	text := stripFence(strings.TrimSpace(raw))
	var decision agentDecision
	if text == "" {
		return decision, errors.New("the answer was empty")
	}
	if err := json.Unmarshal([]byte(text), &decision); err != nil {
		return decision, err
	}
	return decision, nil
}

func stripFence(text string) string {
	if !strings.HasPrefix(text, "```") {
		return text
	}
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[i+1:]
	} else {
		return text
	}
	if j := strings.LastIndex(text, "```"); j >= 0 {
		text = text[:j]
	}
	return strings.TrimSpace(text)
}

// trimAgentHistory keeps the system prompt and the last few rounds. The
// screens are the bulk of the conversation and they mostly repeat.
func trimAgentHistory(messages []openrouter.Message) []openrouter.Message {
	const keep = 2 * agentHistorySteps
	if len(messages) <= keep+1 {
		return messages
	}
	out := make([]openrouter.Message, 0, keep+1)
	out = append(out, messages[0])
	return append(out, messages[len(messages)-keep:]...)
}

// agentSystemPrompt is what the operator is told it is doing. The goal is part
// of it rather than a message of its own, so that no later screen can look
// like a new instruction.
func agentSystemPrompt(label, goal string) string {
	return "You drive a terminal that runs " + label + ". You are given the last " +
		fmt.Sprint(agentScreenLines) + " lines of the screen and a goal from the user. " +
		"Answer with one JSON object and nothing else:\n" +
		`{"actions":[…],"done":bool,"summary":"…","note":"…"}.` + "\n" +
		`An action is {"text":"…"} to type characters, {"key":"NAME"} for one of ` +
		strings.Join(agentKeyVocabulary, ",") + `, or {"wait_ms":N} to pause up to ` +
		fmt.Sprint(termux.MaxKeyWait.Milliseconds()) + " ms. " +
		"Text never contains a newline; to submit, send Enter. Digits and letters are text, never keys.\n" +
		"Take the smallest step that makes progress, then look again - at most " +
		fmt.Sprint(agentMaxActions) + " actions.\n" +
		"The screen may show a menu, a model picker or a permission prompt; answer it the way the " +
		"goal implies. Set done when the goal is reached or cannot be reached, and say why in " +
		"summary.\nGoal: " + goal
}

/* --------------------------------------------------------------- the keys */

// agentKeyVocabulary is the fixed set of keys a run may press, in the order
// the system prompt lists them. It is a superset of the key bar's, because the
// operator has to walk a TUI menu and the key bar does not.
var agentKeyVocabulary = []string{
	"Enter", "Escape", "Tab", "BSpace", "Space", "Up", "Down", "Left", "Right",
	"Home", "End", "PageUp", "PageDown", "C-c", "C-d", "C-z", "C-l",
}

// agentKeys maps the vocabulary onto itself by its lower case spelling. Every
// name in it is a tmux key name, verified against tmux 3.6.
var agentKeys = func() map[string]string {
	m := make(map[string]string, len(agentKeyVocabulary))
	for _, name := range agentKeyVocabulary {
		m[strings.ToLower(name)] = name
	}
	return m
}()

// agentKeyName maps what a model wrote onto a tmux key name, or says no.
//
// The vocabulary is fixed, but its spelling is not worth a failed step: the
// key bar in this very app calls the interrupt "Ctrl-C", so a model that has
// seen a screenshot of it will too.
func agentKeyName(raw string) (string, bool) {
	key := strings.ToLower(strings.TrimSpace(raw))
	key = strings.NewReplacer(" ", "", "_", "", "+", "-").Replace(key)
	switch {
	case strings.HasPrefix(key, "ctrl-"):
		key = "c-" + strings.TrimPrefix(key, "ctrl-")
	case strings.HasPrefix(key, "control-"):
		key = "c-" + strings.TrimPrefix(key, "control-")
	case strings.HasPrefix(key, "^"):
		key = "c-" + key[1:]
	}
	switch key {
	case "return":
		key = "enter"
	case "esc":
		key = "escape"
	case "backspace", "bs":
		key = "bspace"
	case "pgup", "prior":
		key = "pageup"
	case "pgdn", "pgdown", "next":
		key = "pagedown"
	}
	name, ok := agentKeys[key]
	return name, ok
}

// plannedKey is one action that survived the guards, with the sentence the
// progress line shows for it.
type plannedKey struct {
	key  termux.Key
	said string
}

// planActions turns a model's list into keys, dropping what may not be sent
// and saying why. A dropped action never aborts the step: the note reaches the
// model on the next one, which is how it learns that C-d is not available.
//
// There is deliberately no blocklist of dangerous text here. The operator
// types into a terminal that already runs as this user, and a model that
// wanted to could spell the same command across two actions; what is real is
// this list of guards, a bounded run, and every key it sends being on screen.
func planActions(actions []agentAction, harness string, interrupts *int, lastInterrupt *bool) ([]plannedKey, []string) {
	var plan []plannedKey
	var notes []string
	for i, action := range actions {
		if i >= agentMaxActions {
			notes = append(notes, fmt.Sprintf("Only the first %d actions of that step were run.", agentMaxActions))
			break
		}
		switch {
		case action.Text != "":
			text := action.Text
			if strings.ContainsAny(text, "\r\n") {
				// Submitting is always an explicit Enter, so that a multi line
				// paste can never fire a turn half typed.
				text = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(text)
				notes = append(notes, "Newlines were stripped from your text; send Enter to submit.")
			}
			if runes := []rune(text); len(runes) > agentMaxTextRunes {
				text = string(runes[:agentMaxTextRunes])
				notes = append(notes, fmt.Sprintf("Your text was cut to %d characters.", agentMaxTextRunes))
			}
			if text == "" {
				continue
			}
			*lastInterrupt = false
			plan = append(plan, plannedKey{
				key:  termux.Key{Text: text, Wait: agentActionGap},
				said: "typed " + quoteShort(text),
			})
		case action.Key != "":
			name, ok := agentKeyName(action.Key)
			if !ok {
				notes = append(notes, fmt.Sprintf("%q is not a key I can press; type letters and digits as text.", action.Key))
				continue
			}
			if name == "C-c" {
				if *lastInterrupt {
					notes = append(notes, "Two interrupts in a row were asked for; the second was dropped.")
					continue
				}
				*interrupts++
				*lastInterrupt = true
			} else {
				*lastInterrupt = false
			}
			if name == "C-d" && harness == config.HarnessShell {
				// C-d at a shell prompt closes the pane, which ends the
				// session the run is driving.
				notes = append(notes, "C-d closes a shell, so it was not sent.")
				continue
			}
			plan = append(plan, plannedKey{
				key:  termux.Key{Name: name, Wait: agentActionGap},
				said: "pressed " + name,
			})
		case action.WaitMS > 0:
			wait := time.Duration(action.WaitMS) * time.Millisecond
			if wait > termux.MaxKeyWait {
				wait = termux.MaxKeyWait
				notes = append(notes, fmt.Sprintf("A wait was shortened to %s.", termux.MaxKeyWait))
			}
			plan = append(plan, plannedKey{
				key:  termux.Key{Wait: wait},
				said: "waited " + wait.String(),
			})
		default:
			notes = append(notes, `An action needs "text", "key" or "wait_ms".`)
		}
	}
	return plan, notes
}

// quoteShort is a typed string as the progress line shows it: short, because
// the line is one line on a phone.
func quoteShort(text string) string {
	runes := []rune(text)
	if len(runes) > 40 {
		return fmt.Sprintf("%q", string(runes[:40])+"…")
	}
	return fmt.Sprintf("%q", text)
}

// joinNote is what the run's hover detail says: the model's own note, and
// whatever the guards had to say about the step.
func joinNote(note string, guards []string) string {
	note = strings.TrimSpace(note)
	if len(guards) == 0 {
		return note
	}
	joined := strings.Join(guards, " ")
	if note == "" {
		return joined
	}
	return note + " " + joined
}
