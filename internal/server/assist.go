package server

// The two things that talk *about* a terminal rather than to it: the Status
// button, which has a model read the screen and say what it means, and the
// namer, which has a model read the first screen and call the session
// something. Both are server side on purpose - a phone that locks in a pocket
// comes back to a finished answer - and both read the pane through
// termux.CapturePane, which returns text with no escape sequences in it at all.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
	"github.com/saschazesiger/SocratesAgent/internal/openrouter"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// What the spoken status reads and how long it may take.
const (
	// statusScreenLines is what the spoken summary is given. It is a screen
	// and a little history: enough to see how the last answer started, not so
	// much that a scrollback is being summarised.
	statusScreenLines = 120

	// statusTimeout bounds one spoken status. It is a short answer from a
	// fast model with a person waiting for it.
	statusTimeout = 90 * time.Second
)

// The phases a spoken status reports while it is being made. Pressing Status
// is a question to a model over a network, and until this existed the only
// evidence that anything was happening was a button that had gone quiet. Each
// one is broadcast to the session's viewers as it starts, so the ticker on
// every device attached to this session shows the same line.
const (
	statusPhaseCapturing = "capturing"
	statusPhaseAsking    = "asking"
	statusPhaseSpeaking  = "speaking"
	statusPhaseDone      = "done"
	statusPhaseError     = "error"
)

/* ------------------------------------------------------------- the status */

// handleSessionStatus says out loud what a terminal is doing.
//
// The audio is not here: the page takes the text and posts it to
// /api/voice/speak, which already owns the length scaled deadline, the "no
// key yet" answer and the stop logic - and a hundred kilobytes of MP3 has no
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

	ctx, cancel := context.WithTimeout(r.Context(), statusTimeout)
	defer cancel()
	s.emitStatus(row.ID, statusPhaseCapturing, "Reading the screen")
	screen, err := s.manager.CapturePane(ctx, row.ID, statusScreenLines)
	if err != nil {
		s.emitStatus(row.ID, statusPhaseError, "That screen could not be read.")
		writeError(w, http.StatusBadRequest, "this session has no terminal to read: "+err.Error())
		return
	}
	activity := s.manager.ActivityOf(row.ID)
	language := config.NormalizeLanguage(settings.Voice.Language)

	temperature := 0.2
	client := openrouter.New(settings.OpenRouter.BaseURL, settings.OpenRouter.APIKey)
	s.emitStatus(row.ID, statusPhaseAsking, "Asking "+model)
	res, err := client.Chat(ctx, openrouter.ChatRequest{
		Model:       model,
		Messages:    []openrouter.Message{{Role: "user", Content: statusPrompt(row.Harness, activity.State, language, screen)}},
		Temperature: &temperature,
		MaxTokens:   300,
	}, nil)
	if err != nil {
		code, message := modelProblem(err, model, "status")
		s.emitStatus(row.ID, statusPhaseError, "That session could not be summarised.")
		writeError(w, code, message)
		return
	}
	text := plainStatus(res.Content)
	if text == "" {
		s.emitStatus(row.ID, statusPhaseError, "There was nothing to say about that screen.")
		writeError(w, http.StatusBadGateway, "the model answered with nothing to say")
		return
	}
	// The browser is about to hand this to the voice, so the phase is announced
	// from here: the answer and the news that it is being read are one event,
	// and a second round trip to say so would arrive after the voice did.
	s.emitStatus(row.ID, statusPhaseSpeaking, "Speaking")
	s.emitStatus(row.ID, statusPhaseDone, text)
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
// screen: the voice of that setting is what reads, so a German sentence in
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
// not a bug report. A key OpenRouter will not accept is the other half of
// that: it arrives as the same 4xx and is the same walk to /admin, but the
// model id is not what is wrong with it and saying so would send the owner
// looking for a model that exists.
func modelProblem(err error, model, kind string) (int, string) {
	code := upstreamStatus(err)
	var routed *openrouter.StatusError
	if errors.As(err, &routed) &&
		(routed.Status == http.StatusUnauthorized || routed.Status == http.StatusForbidden) {
		return code, "OpenRouter rejected the API key - open /admin and check it. OpenRouter said: " +
			err.Error()
	}
	if code == http.StatusBadRequest {
		return code, fmt.Sprintf("unknown model %s - open /admin and pick a %s model. OpenRouter said: %s",
			model, kind, err.Error())
	}
	return code, err.Error()
}

/* ---------------------------------------------------------- the title run */

// What naming a session costs and how far it may go.
const (
	// titleScreenLines is what the namer is shown. It is the operator's
	// screen: the first answer of a coding harness is long, and the question
	// that produced it is usually above the fold.
	titleScreenLines = 200
	// titleTimeout bounds the whole run. Nobody is waiting for it - it is a
	// sidebar row renaming itself - so it fails silently rather than late.
	titleTimeout = 20 * time.Second
	// titleMaxRunes is the longest name that still reads as a name in a
	// sidebar row on a phone.
	titleMaxRunes = 60
	// titleMaxTokens is a handful of words. The model is asked for three to
	// seven, and a ceiling stops a paragraph being paid for.
	titleMaxTokens = 60
	// titleMinAge is how old a session has to be before a finished turn is
	// worth naming it after.
	//
	// A harness starting up is work: it paints its box, reads its config and
	// settles, and the detector sees that as an edge out of busy before
	// anybody has typed a word. Naming a session from that screen produces
	// the name of the program - and spends the one go on it. Under this mark
	// an edge is ignored rather than spent, so the next one still counts.
	titleMinAge = 30 * time.Second
)

// titleDriver names a session once, the first time it has answered anything.
//
// The moment is the first committed edge out of `busy` that lands after the
// session has been alive for titleMinAge: the harness has said something, and
// it is late enough for what it said to be the work rather than the start-up.
// It happens whether or not anybody is looking, because the point of it is the
// sidebar of the browser that is looking at a different session.
//
// It is once per session and the once is persisted (store.TitleAuto), so a
// server restart does not rename a session that has already been named; a name
// the user typed or a rename they made (store.TitleUser) is never touched.
type titleDriver struct {
	srv *Server

	mu sync.Mutex
	// now is the clock the age gate reads. It is a field so that the gate can
	// be proven without a test standing still for half a minute; everything
	// else reads time.Now directly.
	now func() time.Time
	// seen is the last committed state per session, which is where the edge
	// out of busy is found: the callback carries the new state and not the old.
	seen map[string]termux.State
	// live is the run in flight per session, so that two edges cannot start
	// two runs and deleting a session can stop the one it has.
	live map[string]context.CancelFunc
}

func newTitleDriver(s *Server) *titleDriver {
	return &titleDriver{
		srv:  s,
		now:  time.Now,
		seen: map[string]termux.State{},
		live: map[string]context.CancelFunc{},
	}
}

// clock is the driver's idea of the time, read under the lock because the run
// that reads it is a goroutine and the tests that move it are not.
func (d *titleDriver) clock() time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.now()
}

// setClock moves the age gate's clock. It exists for the tests of that gate
// and for nothing else.
func (d *titleDriver) setClock(now func() time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.now = now
}

// observe takes every committed activity change and starts the title run on
// the first edge out of busy. It returns at once: the detector's tick is a
// tick, and it is never made to wait for a gateway.
func (d *titleDriver) observe(sessionID string, state termux.State) {
	if d == nil {
		return
	}
	d.mu.Lock()
	prev, known := d.seen[sessionID]
	d.seen[sessionID] = state
	_, running := d.live[sessionID]
	// An edge needs a previous state: a session first seen while it is already
	// idle has never been watched working, and there is no answer on its
	// screen that this server saw arrive.
	if !known || running || prev != termux.StateBusy || state == termux.StateBusy {
		d.mu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), titleTimeout)
	d.live[sessionID] = cancel
	d.mu.Unlock()

	go func() {
		defer func() {
			cancel()
			d.mu.Lock()
			delete(d.live, sessionID)
			d.mu.Unlock()
		}()
		d.run(ctx, sessionID)
	}()
}

// forget drops what is remembered about a session and stops a run that is
// still going. A model naming a row that no longer exists is harmless; writing
// its answer into a deleted session's row is not.
func (d *titleDriver) forget(sessionID string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	cancel := d.live[sessionID]
	delete(d.live, sessionID)
	delete(d.seen, sessionID)
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// run is the whole of it: read the screen, ask for a name, store it, and tell
// every open browser. Every way out of it is silent - there is no request to
// answer and no button that was pressed.
func (d *titleDriver) run(ctx context.Context, sessionID string) {
	row, err := d.srv.store.GetSession(sessionID)
	if err != nil || row.TitleSource != "" {
		return
	}
	// Too young to be about anything yet. This is a silent return and not a
	// spent turn: the session is named on the first turn that finishes after
	// it has been alive for titleMinAge.
	if d.clock().Sub(time.UnixMilli(row.CreatedAt)) < titleMinAge {
		return
	}
	// A shell session is a directory and a prompt. There is nothing on its
	// screen that a name could be about, and `cd` is not a subject.
	if row.Harness == config.HarnessShell {
		return
	}
	settings := d.srv.Settings()
	key := strings.TrimSpace(settings.OpenRouter.APIKey)
	if key == "" {
		// Not configured is not a fault: the placeholder name stays, and the
		// session is named the first time it answers after a key is added.
		return
	}
	model := strings.TrimSpace(settings.OpenRouter.TitleModel)
	if model == "" {
		model = config.DefaultTitleModel
	}
	screen, err := d.srv.manager.CapturePane(ctx, sessionID, titleScreenLines)
	if err != nil || strings.TrimSpace(screen) == "" {
		return
	}

	temperature := 0.3
	client := openrouter.New(settings.OpenRouter.BaseURL, key)
	res, err := client.Chat(ctx, openrouter.ChatRequest{
		Model:       model,
		Messages:    []openrouter.Message{{Role: "user", Content: titlePrompt(row.Harness, screen)}},
		Temperature: &temperature,
		MaxTokens:   titleMaxTokens,
	}, nil)
	// The attempt is what is spent, not the answer: whatever came back, this
	// session has had its one go. A model id that does not exist would
	// otherwise be paid for again at the end of every turn, for ever.
	if err != nil {
		if ctx.Err() == nil {
			_ = d.srv.store.MarkSessionTitled(sessionID)
		}
		return
	}
	title := cleanTitle(res.Content)
	if title == "" {
		_ = d.srv.store.MarkSessionTitled(sessionID)
		return
	}
	if err := d.srv.store.SetAutoSessionTitle(sessionID, title); err != nil {
		return
	}
	d.srv.onSessionTitle(sessionID, title)
}

// titlePrompt is the naming instruction, screen included.
//
// The language is the screen's and not the voice setting's, which is the one
// place these prompts differ: a title is read, not spoken, and a German
// conversation with an English name on it would be a name for nobody.
func titlePrompt(harness, screen string) string {
	return "Below is the screen of a terminal running " + harnessLabel(harness) +
		". Give this session a title: 3 to 7 words saying what it is about, so that somebody " +
		"scanning a list of sessions knows which one this is. Write it in the language the " +
		"person on the screen is evidently writing in; if that is unclear, write it in English. " +
		"Answer with the title and nothing else: no quotation marks, no full stop at the end, " +
		"no markdown, no explanation.\nScreen:\n" + screen
}

// cleanTitle takes a name out of whatever the model answered with. Empty is a
// refusal, and the caller keeps the name the session already had.
func cleanTitle(text string) string {
	text = strings.TrimSpace(text)
	// A fenced answer is the commonest way this instruction is disobeyed.
	if strings.HasPrefix(text, "```") {
		if end := strings.Index(text[3:], "```"); end >= 0 {
			text = text[3 : 3+end]
		} else {
			text = text[3:]
		}
		if nl := strings.IndexByte(text, '\n'); nl >= 0 && !strings.Contains(text[:nl], " ") {
			text = text[nl+1:] // the language tag of the fence
		}
	}
	// One line, whatever else came with it.
	if nl := strings.IndexAny(text, "\r\n"); nl >= 0 {
		text = text[:nl]
	}
	text = strings.NewReplacer("**", "", "*", "", "`", "", "#", "").Replace(text)
	text = strings.Join(strings.Fields(text), " ")
	text = trimTitleEdges(text)
	if runes := []rune(text); len(runes) > titleMaxRunes {
		cut := string(runes[:titleMaxRunes])
		if space := strings.LastIndexByte(cut, ' '); space > titleMaxRunes/2 {
			cut = cut[:space]
		}
		text = trimTitleEdges(cut)
	}
	return text
}

// trimTitleEdges takes the quotes and the punctuation off, in as many passes
// as it takes: a quoted answer that also ends in a full stop leaves the
// closing quote behind the stop, and one pass would keep the opening one.
func trimTitleEdges(text string) string {
	for {
		trimmed := strings.Trim(text, " \t\"'“”‘’«»")
		trimmed = strings.TrimRight(trimmed, " .!,;:")
		trimmed = strings.TrimSpace(trimmed)
		if trimmed == text {
			return trimmed
		}
		text = trimmed
	}
}
