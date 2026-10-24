package server

// The two things that talk *about* a session rather than to it: the Status
// button, which has a model read what the session is doing and say what it
// means, and the namer, which has a model call the session something. Both
// are server side on purpose - a phone that locks in a pocket comes back to a
// finished answer. A chat is told from its journal; a Shell's screen is read
// through termux.CapturePane, which returns text with no escape sequences in
// it at all.

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
	"github.com/saschazesiger/SocratesAgent/internal/store"
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
	activity := s.manager.ActivityOf(row.ID)
	language := config.NormalizeLanguage(settings.Voice.Language)
	// The verdict on the turn the session is resting after, if there is one.
	verdict := ""
	if o, ok := s.digests.outcomeOf(row.ID); ok && activity.State == termux.StateIdle && o.Since == activity.Since {
		verdict = outcomeWords(o)
	}
	var prompt string
	if row.IsChat() {
		// A chat is told from its journal: its last turn, and what runs now.
		text, state := s.chats.statusOf(row.ID)
		if text == "" {
			writeError(w, http.StatusBadRequest, "this chat has nothing to say yet: send it a message first")
			return
		}
		prompt = chatStatusPrompt(row.Harness, state, language, verdict, text)
	} else {
		s.emitStatus(row.ID, statusPhaseCapturing, "Reading the screen")
		screen, err := s.manager.CapturePane(ctx, row.ID, statusScreenLines)
		if err != nil {
			s.emitStatus(row.ID, statusPhaseError, "That screen could not be read.")
			writeError(w, http.StatusBadRequest, "this session has no terminal to read: "+err.Error())
			return
		}
		prompt = statusPrompt(row.Harness, activity.State, language, verdict, screen)
	}

	temperature := 0.2
	client := openrouter.New(settings.OpenRouter.BaseURL, settings.OpenRouter.APIKey)
	s.emitStatus(row.ID, statusPhaseAsking, "Asking "+model)
	res, err := client.Chat(ctx, openrouter.ChatRequest{
		Model:       model,
		Messages:    []openrouter.Message{{Role: "user", Content: prompt}},
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
//
// verdict is the outcome of the turn the session is resting after, in words
// (outcomeWords), or "" for none: the person asking how a session is doing
// is told how its last turn left the work, as Home shows it.
func statusPrompt(harness string, state termux.State, language, verdict, screen string) string {
	prompt := "You are told what a terminal running " + harnessLabel(harness) +
		" currently shows. It is " + stateWords(state) + ". In one to three short sentences, say " +
		"what a person needs to know: if it is working, what it is working on; if it has finished, " +
		"what the answer is; if it is waiting, what it is asking and what the choices are. Speak " +
		"plainly - this is read out loud, so no markdown, no code, no file paths unless they are " +
		"the point. Answer in " + config.LanguageName(language) + "."
	if verdict != "" {
		prompt += "\nA check of the turn that just ended says: " + verdict + ". Mention it."
	}
	return prompt + "\nScreen:\n" + screen
}

// chatStatusPrompt is statusPrompt for a chat, which is told from its
// journal (chatNow) rather than shown as a screen.
func chatStatusPrompt(harness string, state termux.State, language, verdict, chat string) string {
	prompt := "You are told what a chat with " + chatLabel(harness) + ", a coding agent, is doing. It is " +
		stateWords(state) + ". In one to three short sentences, say what a person needs to know: if it is " +
		"working, what it is working on; if it has finished, what the answer is; if it is waiting, what it " +
		"is asking and what the choices are. Speak plainly - this is read out loud, so no markdown, no code, " +
		"no file paths unless they are the point. Answer in " + config.LanguageName(language) + "."
	if verdict != "" {
		prompt += "\nA check of the turn that just ended says: " + verdict + ". Mention it."
	}
	return prompt + "\nThe chat:\n" + chat
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
	case termux.StateTasks:
		return "done with its turn, but something it started - a background shell, a monitor or a " +
			"background sub-agent - is still running and it will pick up again when that reports back"
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
	// titleScreenLines is what the namer is shown of a Shell named by hand:
	// its latest screen, with what was typed last at the bottom.
	titleScreenLines = 200
	// titleTimeout bounds the whole run. It is a sidebar row renaming itself,
	// so a gateway that does not answer in time is reported, not waited out.
	titleTimeout = 30 * time.Second
	// titleMaxRunes is the longest name that still reads as a name in a
	// sidebar row on a phone.
	titleMaxRunes = 60
	// titleMaxTokens is a ceiling, not a target. The answer is a handful of
	// words, but a model that thinks before it answers spends its thinking
	// out of the same budget - and a ceiling of a few dozen tokens is then an
	// empty answer, every time.
	titleMaxTokens = 1024
)

// The phases of the "title" frame. Start makes the row pulse, done carries
// the new name, and error stops the pulse and says why - the browser shows it
// in a dialog, because a naming that failed without a word is a sidebar that
// just never changes and nobody knows why.
const (
	titlePhaseStart = "start"
	titlePhaseDone  = "done"
	titlePhaseError = "error"
)

// titleDriver names a chat once: the moment its first message is taken up.
//
// The moment is the first committed edge into `busy` after somebody has sent
// the chat something (termux.Activity.Asked). An agent can be busy while it
// starts, and that edge comes before anybody has said a word, so it is
// ignored; the first edge after a message is the agent taking it up.
//
// It is once per session and the once is persisted (store.TitleAuto) whatever
// the outcome, so a later prompt never renames a session and a server restart
// does not either; a name the user typed or a rename they made
// (store.TitleUser) is never touched.
type titleDriver struct {
	srv *Server

	mu sync.Mutex
	// seen is the last committed state per session, which is where the edge
	// into busy is found: the callback carries the new state and not the old.
	seen map[string]termux.State
	// live is the run in flight per session, so that two edges cannot start
	// two runs and deleting a session can stop the one it has.
	live map[string]context.CancelFunc
}

func newTitleDriver(s *Server) *titleDriver {
	return &titleDriver{
		srv:  s,
		seen: map[string]termux.State{},
		live: map[string]context.CancelFunc{},
	}
}

// observe takes every committed activity change and starts the title run on
// the first edge into busy that somebody asked for. It returns at once: the
// detector's tick is a tick, and it is never made to wait for a gateway.
func (d *titleDriver) observe(sessionID string, a termux.Activity) {
	if d == nil {
		return
	}
	d.mu.Lock()
	prev := d.seen[sessionID]
	d.seen[sessionID] = a.State
	d.mu.Unlock()
	if !a.Asked || a.State != termux.StateBusy || prev == termux.StateBusy {
		return
	}
	d.launch(sessionID, d.run)
}

// launch runs fn in the background as the one title run a session may have
// at a time, bounded by titleTimeout. It answers false, and runs nothing,
// while another one is still going.
func (d *titleDriver) launch(sessionID string, fn func(context.Context, string)) bool {
	d.mu.Lock()
	if _, running := d.live[sessionID]; running {
		d.mu.Unlock()
		return false
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
		fn(ctx, sessionID)
	}()
	return true
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

// run is the whole of it: read the first message, ask for a name, store it,
// and tell every open browser. Once it has started, every way out of it is
// reported: the row pulses from the first frame to the last.
func (d *titleDriver) run(ctx context.Context, sessionID string) {
	row, err := d.srv.store.GetSession(sessionID)
	if err != nil || row.TitleSource != "" {
		return
	}
	// Only a chat is named on its own. A Shell is a directory and a prompt:
	// there is nothing on its screen that a name could be about, and `cd` is
	// not a subject.
	if !row.IsChat() {
		return
	}
	namer, ok := d.srv.titleNamer()
	if !ok {
		// Not configured is not a fault: automatic names are simply off, and
		// the placeholder name stays.
		return
	}

	d.srv.emitTitle(sessionID, titlePhaseStart, "", "")
	// A chat is named after its first message, and what its first answer
	// has said so far.
	title, problem := "", ""
	first, answer := d.srv.chats.firstExchange(sessionID)
	if first == "" {
		problem = "the chat's first message could not be read, so there was nothing to name it after"
	} else {
		title, problem = namer.ask(ctx, chatTitlePrompt(row.Harness, first, answer, false))
	}
	if deleted := ctx.Err() == context.Canceled; deleted {
		// forget: the session is gone, and so is anybody to tell.
		return
	}
	// The attempt is what is spent, not the answer: whatever came back, this
	// session has had its one go.
	if problem != "" {
		_ = d.srv.store.MarkSessionTitled(sessionID)
		d.srv.emitTitle(sessionID, titlePhaseError, "", problem)
		return
	}
	if err := d.srv.store.SetAutoSessionTitle(sessionID, title); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Renamed by hand while the model was thinking: that name wins,
			// and the pulse just stops.
			d.srv.emitTitle(sessionID, titlePhaseDone, "", "")
			return
		}
		d.srv.emitTitle(sessionID, titlePhaseError, "", "the title could not be saved: "+err.Error())
		return
	}
	d.srv.emitTitle(sessionID, titlePhaseDone, title, "")
}

// titleNamer is the gateway and the model sessions are named with, and false
// while no OpenRouter key is set - automatic names are then off.
type titleNamer struct {
	key, baseURL, model string
}

func (s *Server) titleNamer() (titleNamer, bool) {
	settings := s.Settings()
	key := strings.TrimSpace(settings.OpenRouter.APIKey)
	if key == "" {
		return titleNamer{}, false
	}
	model := strings.TrimSpace(settings.OpenRouter.TitleModel)
	if model == "" {
		model = config.DefaultTitleModel
	}
	return titleNamer{key: key, baseURL: settings.OpenRouter.BaseURL, model: model}, true
}

// ask has the model answer a naming instruction. It answers with a title or
// with a sentence saying why there is none.
func (n titleNamer) ask(ctx context.Context, instruction string) (string, string) {
	temperature := 0.3
	client := openrouter.New(n.baseURL, n.key)
	res, err := client.Chat(ctx, openrouter.ChatRequest{
		Model:       n.model,
		Messages:    []openrouter.Message{{Role: "user", Content: instruction}},
		Temperature: &temperature,
		MaxTokens:   titleMaxTokens,
	}, nil)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Sprintf("the title model %s did not answer within %s", n.model, titleTimeout)
		}
		_, why := modelProblem(err, n.model, "title")
		return "", why
	}
	title := cleanTitle(res.Content)
	if title == "" {
		why := "the title model " + n.model + " answered with nothing usable"
		if res.FinishReason == "length" {
			why += " (it ran out of tokens, probably while reasoning)"
		}
		return "", why
	}
	return title, ""
}

/* ------------------------------------------------- a naming asked for */

// retitlePromptRunes is how much of a first prompt the namer is shown. A
// prompt can be a hundred kilobytes; what it is about is in its beginning.
const retitlePromptRunes = 4000

// handleRetitleSession names a session again, now, from what there is to go
// by: the first prompt - the prepared session's field as it is on screen, or
// the draft it was started with - and the screen of a session that has one.
//
// It is the button beside the name, which replaces whatever the session was
// called, and the first time somebody leaves a prepared session's prompt
// field ("unnamed"), which names only a session nobody has named yet - and
// says nothing when there is nothing to do, because nobody pressed anything.
//
// It answers when the name is there, with the session: a prepared session
// has no socket for the "title" frames to arrive on. The frames still go to
// every other browser, so their rows pulse and take the new name too; a
// failure is said to the one who asked, and only stops the pulse elsewhere.
// A run outlives a request that gave up waiting for it, and still saves.
func (s *Server) handleRetitleSession(w http.ResponseWriter, r *http.Request) {
	row, ok := s.session(w, r)
	if !ok {
		return
	}
	var body struct {
		Prompt  string `json:"prompt"`
		Unnamed bool   `json:"unnamed"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	quiet := func(code int, why string) {
		if body.Unnamed {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeError(w, code, why)
	}
	if row.Harness == config.HarnessVSCode {
		quiet(http.StatusBadRequest, "an editor is named after its folder")
		return
	}
	if body.Unnamed && row.TitleSource != "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	namer, ok := s.titleNamer()
	if !ok {
		quiet(http.StatusConflict, "automatic titles are off: open /admin and set an OpenRouter API key")
		return
	}
	prompt := ""
	if row.State == store.StatePrestart {
		// The field can be ahead of the saved draft by a pause in the typing.
		prompt = strings.TrimSpace(body.Prompt)
	}
	// A started chat's draft is what its composer holds, not a first prompt:
	// its first message is in its journal.
	if prompt == "" && (!row.IsChat() || row.State == store.StatePrestart) {
		if draft, err := s.store.GetDraft(row.ID); err == nil {
			prompt = strings.TrimSpace(draft.Text)
		}
	}
	if runes := []rune(prompt); len(runes) > retitlePromptRunes {
		prompt = string(runes[:retitlePromptRunes])
	}
	if prompt == "" && row.State == store.StatePrestart {
		quiet(http.StatusConflict, "there is nothing to name it after yet: write the first prompt")
		return
	}
	was := row.Title
	// The problem the run ended with, or "" once it saved or lost to a
	// rename by hand.
	result := make(chan string, 1)
	launched := s.titles.launch(row.ID, func(ctx context.Context, id string) {
		s.emitTitle(id, titlePhaseStart, "", "")
		title, problem := "", ""
		if chat := row.IsChat() && row.State != store.StatePrestart; chat {
			// A chat is named again from its journal: its first message, and
			// where the conversation has got to.
			first, _ := s.chats.firstExchange(id)
			if prompt == "" {
				prompt = first
			}
			latest := s.chats.latestExchange(id)
			if prompt == "" && latest == "" {
				problem = "there was nothing to name the chat after: it has no message yet"
			} else {
				title, problem = namer.ask(ctx, chatTitlePrompt(row.Harness, prompt, latest, true))
			}
		} else {
			screen := ""
			if row.State != store.StatePrestart {
				// A session whose program has ended has no pane, and then the
				// first prompt is all there is.
				screen, _ = s.manager.CapturePane(ctx, id, titleScreenLines)
			}
			if prompt == "" && strings.TrimSpace(screen) == "" {
				problem = "there was nothing to name the session after: no first prompt, and no screen"
			} else {
				title, problem = namer.ask(ctx, retitlePrompt(row.Harness, prompt, screen))
			}
		}
		if ctx.Err() == context.Canceled {
			result <- "the session was deleted"
			return
		}
		if problem == "" {
			var err error
			if body.Unnamed {
				err = s.store.SetAutoSessionTitle(id, title)
			} else {
				err = s.store.ReplaceSessionTitle(id, was, title)
			}
			switch {
			case errors.Is(err, store.ErrNotFound):
				// Renamed by hand in the meantime: that name wins.
				title = ""
			case err != nil:
				title, problem = "", "the title could not be saved: "+err.Error()
			}
		}
		s.emitTitle(id, titlePhaseDone, title, "")
		result <- problem
	})
	if !launched {
		quiet(http.StatusConflict, "the session is being named already")
		return
	}
	select {
	case problem := <-result:
		if problem != "" {
			writeError(w, http.StatusBadGateway, problem)
			return
		}
		s.answerWithSession(w, row.ID)
	case <-r.Context().Done():
	}
}

// retitlePrompt is the instruction for a naming somebody asked for: the first
// prompt, the screen, or both - whichever the session has.
func retitlePrompt(harness, prompt, screen string) string {
	label := harnessLabel(harness)
	var b strings.Builder
	switch {
	case prompt != "" && screen != "":
		b.WriteString("Below are the first prompt a user wrote for a session of " + label +
			" and the latest screen of its terminal. ")
	case prompt != "":
		b.WriteString("Below is the first prompt a user wrote for a session of " + label + ". ")
	default:
		b.WriteString("Below is the latest screen of a terminal running " + label + ". ")
	}
	b.WriteString("Give this session a title that says what it is about, as short and plain as " +
		"possible: 2 to 5 words, in English even if the session is in another language. " +
		"Name the task, not the program - never mention " + label + ". ")
	if screen != "" {
		b.WriteString("Where the screen shows the work has moved on, name what it is about now. ")
	}
	b.WriteString("Answer with the title and nothing else: no quotation marks, no full stop at the end, " +
		"no markdown, no explanation.")
	if prompt != "" {
		b.WriteString("\nFirst prompt:\n" + prompt)
	}
	if screen != "" {
		b.WriteString("\nScreen:\n" + screen)
	}
	return b.String()
}

// chatTitlePrompt is the naming instruction of a chat: its first message,
// and the first answer (later) or where the conversation has got to (again).
func chatTitlePrompt(harness, first, more string, again bool) string {
	label := chatLabel(harness)
	var b strings.Builder
	b.WriteString("Below is the first message a user wrote in a chat with " + label)
	switch {
	case more != "" && again:
		b.WriteString(", and the latest exchange of the chat")
	case more != "":
		b.WriteString(", and the start of its first answer")
	}
	b.WriteString(". Give this chat a title that says what it is about, as short and plain as possible: " +
		"2 to 5 words, in English even if the chat is in another language. " +
		"Name the task, not the program - never mention " + label + ". ")
	if again && more != "" {
		b.WriteString("Where the latest exchange shows the work has moved on, name what it is about now. ")
	}
	b.WriteString("Answer with the title and nothing else: no quotation marks, no full stop at the end, " +
		"no markdown, no explanation.\nFirst message:\n" + clipRunes(first, retitlePromptRunes))
	if more != "" {
		if again {
			b.WriteString("\nThe latest exchange:\n" + more)
		} else {
			b.WriteString("\nThe first answer, so far:\n" + more)
		}
	}
	return b.String()
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
