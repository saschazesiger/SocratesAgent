package server

// The chat beside the terminal: a conversation with a model that can see the
// screen, and that types on the screen when the answer needs it to.
//
// It is the third of the features that talk *about* a terminal rather than to
// it, and it subsumes the second: the old Agent button asked for a goal in one
// field and answered with a progress line, which is a conversation with the
// turn count fixed at one. Here the same operator run (assist.go, B.4) is one
// of the two things an answer can be, and which of the two it is, is the
// model's decision rather than the button's.
//
// Everything a browser needs survives it: the conversation is persisted in the
// key/value store, so a phone that locked, reloaded or lost signal comes back
// to what was said, and the answer itself is written by a goroutine that
// outlives the request the way an operator run does.

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/openrouter"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// What one answer costs and how much of the past it is given.
const (
	// chatScreenLines is what the assistant is shown. Fewer than the
	// operator's 200: this one is answering a question about the screen, not
	// deciding a keystroke from a menu that may be above the fold.
	chatScreenLines = 150
	// chatHistoryMessages is how much of the conversation goes back to the
	// model. Six turns is enough for "and the other one?" to mean something
	// and small enough that the screen stays the bulk of the tokens.
	chatHistoryMessages = 12
	// chatKeepMessages is how much of the conversation is kept at all. It is a
	// chat beside a terminal, not a transcript: the screen is the record.
	chatKeepMessages = 50
	// chatMaxRunes bounds one message from the browser. A question is a
	// question; a paste of a whole file belongs in the terminal.
	chatMaxRunes = 4000
	// chatTimeout bounds one answer. Longer than a status, because this one
	// may be reasoning about what to do rather than describing a screen.
	chatTimeout = 120 * time.Second
	// chatMaxTokens is a spoken-length answer with room to be wrong in.
	chatMaxTokens = 600
)

// chatMessage is one line of the conversation, as it is stored and as the
// browser reads it.
type chatMessage struct {
	Role string `json:"role"` // "user" | "assistant"
	Text string `json:"text"`
	TS   int64  `json:"ts"`
	// RunID names the operator run this message started, which is what makes
	// the bubble the place the Cancel button lives.
	RunID string `json:"run_id,omitempty"`
	// Failed marks an answer that is the reason there is no answer. It is
	// stored like any other, because the phone that reloads has to see why.
	Failed bool `json:"failed,omitempty"`
}

// chatDriver owns the conversations. One per session, in the key/value store,
// with a mutex over the read-modify-write so that two messages arriving in the
// same millisecond do not lose one of each other.
type chatDriver struct {
	srv *Server
	mu  sync.Mutex
}

func newChatDriver(s *Server) *chatDriver { return &chatDriver{srv: s} }

// chatKey is where one session's conversation lives. The key/value table is
// the same one the unread marks use, and for the same reason: this is a small
// document per session, and a table for it would be a migration for nothing.
func chatKey(sessionID string) string { return "chat." + sessionID }

// history is the conversation, oldest first, and never nil: hello carries it
// and a browser reading `null` where it expects a list is one guard nobody
// should have to write.
func (d *chatDriver) history(sessionID string) []chatMessage {
	if d == nil {
		return []chatMessage{}
	}
	var stored []chatMessage
	if err := d.srv.store.GetJSON(chatKey(sessionID), &stored); err != nil || stored == nil {
		return []chatMessage{}
	}
	return stored
}

// append adds one message, trims the conversation to its cap and tells every
// viewer of the session. It is the only writer.
func (d *chatDriver) append(sessionID string, msg chatMessage) chatMessage {
	if msg.TS == 0 {
		msg.TS = time.Now().UnixMilli()
	}
	d.mu.Lock()
	stored := d.history(sessionID)
	stored = append(stored, msg)
	if len(stored) > chatKeepMessages {
		stored = stored[len(stored)-chatKeepMessages:]
	}
	_ = d.srv.store.SetJSON(chatKey(sessionID), stored)
	d.mu.Unlock()
	d.srv.emitChat(sessionID, chatFrame(msg))
	return msg
}

// forget drops a conversation. Deleting a session takes its chat with it: the
// screen it was about is gone, and a key/value row nobody can reach is litter.
func (d *chatDriver) forget(sessionID string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_ = d.srv.store.DeleteKV(chatKey(sessionID))
}

func chatFrame(msg chatMessage) map[string]any {
	frame := map[string]any{"role": msg.Role, "text": msg.Text, "ts": msg.TS}
	if msg.RunID != "" {
		frame["run_id"] = msg.RunID
	}
	if msg.Failed {
		frame["failed"] = true
	}
	return frame
}

/* ------------------------------------------------------------- the routes */

// handleChatHistory is what a browser with no socket asks for on attach.
func (s *Server) handleChatHistory(w http.ResponseWriter, r *http.Request) {
	row, ok := s.session(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": s.chats.history(row.ID)})
}

// handleChatPost takes a message and answers it out of band.
//
// The answer is not in this response. It is a model call that may take a
// minute and may end in a run that types for ten, and the browser that asked
// may be a phone that locks in the meantime - so what comes back is the
// message being accepted, and everything after it arrives on the socket and is
// in the store for whoever asks next.
func (s *Server) handleChatPost(w http.ResponseWriter, r *http.Request) {
	row, ok := s.session(w, r)
	if !ok {
		return
	}
	var body struct {
		Text string `json:"text"`
		Auto bool   `json:"auto"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeError(w, http.StatusBadRequest, "say something to ask about")
		return
	}
	if runes := []rune(text); len(runes) > chatMaxRunes {
		text = strings.TrimSpace(string(runes[:chatMaxRunes]))
	}
	settings := s.Settings()
	if strings.TrimSpace(settings.OpenRouter.APIKey) == "" {
		writeError(w, http.StatusBadRequest,
			"no OpenRouter API key is configured - open /admin, add your key and pick an agent model")
		return
	}

	asked := s.chats.append(row.ID, chatMessage{Role: "user", Text: text})
	go s.chats.answer(row.ID, row.Harness, settings, body.Auto)
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "msg": chatFrame(asked)})
}

/* ------------------------------------------------------------ the answer */

// chatDecision is the whole of what the assistant may say: a sentence for the
// person, and optionally a goal for the operator.
//
// It is a bare JSON object rather than OpenRouter's tool calling because this
// client has no tool calling in it: `openrouter.ChatRequest` carries a model,
// messages, a temperature and a token cap, and adding a tools field, a
// tool_calls field on the response and a second round trip for the tool result
// would be a protocol for one call site. The object is parsed with the same
// fence stripper the operator's decisions use, and an answer that is not an
// object at all is not a failure: it is taken as the reply, because a model
// that answered a question in plain prose answered the question.
type chatDecision struct {
	Reply string `json:"reply"`
	Act   string `json:"act"`
}

// answer writes one assistant turn. It runs in its own goroutine, so nothing
// here may assume there is still a browser.
func (d *chatDriver) answer(sessionID, harness string, settings config.Settings, auto bool) {
	ctx, cancel := context.WithTimeout(context.Background(), chatTimeout)
	defer cancel()

	model := strings.TrimSpace(settings.OpenRouter.AgentModel)
	if model == "" {
		model = config.DefaultAgentModel
	}
	screen, err := d.srv.manager.CapturePane(ctx, sessionID, chatScreenLines)
	if err != nil {
		screen = "(this session has no terminal to read)"
	}
	state := d.srv.manager.ActivityOf(sessionID).State

	messages := []openrouter.Message{{
		Role:    "system",
		Content: chatSystemPrompt(harnessLabel(harness), state, auto, config.LanguageName(settings.Voice.Language), screen),
	}}
	for _, msg := range tailMessages(d.history(sessionID), chatHistoryMessages) {
		role := "user"
		if msg.Role == "assistant" {
			role = "assistant"
		}
		messages = append(messages, openrouter.Message{Role: role, Content: msg.Text})
	}

	temperature := 0.3
	client := openrouter.New(settings.OpenRouter.BaseURL, settings.OpenRouter.APIKey)
	res, err := client.Chat(ctx, openrouter.ChatRequest{
		Model:       model,
		Messages:    messages,
		Temperature: &temperature,
		MaxTokens:   chatMaxTokens,
	}, nil)
	if err != nil {
		_, message := modelProblem(err, model, "agent")
		d.append(sessionID, chatMessage{Role: "assistant", Text: message, Failed: true})
		return
	}
	decision := parseChatAnswer(res.Content)
	reply := strings.TrimSpace(decision.Reply)
	goal := strings.TrimSpace(decision.Act)
	if reply == "" && goal == "" {
		d.append(sessionID, chatMessage{
			Role: "assistant", Text: "The model answered with nothing to say.", Failed: true,
		})
		return
	}
	if goal == "" {
		// The markdown is left on. It is rendered as markdown-lite in the
		// panel, and the one place it would be read out loud - the voice -
		// strips it there, where the stripping is actually needed.
		d.append(sessionID, chatMessage{Role: "assistant", Text: reply})
		return
	}
	if reply == "" {
		reply = "Working on it."
	}
	d.startRun(sessionID, harness, settings, reply, goal)
}

// startRun is the operator loop, asked for by a conversation rather than by a
// button, with its ending posted back into the conversation that asked.
func (d *chatDriver) startRun(sessionID, harness string, settings config.Settings, reply, goal string) {
	row, err := d.srv.store.GetSession(sessionID)
	if err != nil {
		return
	}
	if refusal := runRefusal(row, harness, settings); refusal != "" {
		d.append(sessionID, chatMessage{Role: "assistant", Text: reply})
		d.append(sessionID, chatMessage{Role: "assistant", Text: refusal, Failed: true})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), d.srv.agents.wallClock())
	run, started := d.srv.agents.begin(sessionID, goal, cancel)
	if !started {
		cancel()
		d.append(sessionID, chatMessage{Role: "assistant", Text: reply})
		d.append(sessionID, chatMessage{
			Role:   "assistant",
			Text:   "This session is already being driven; stop that run first.",
			Failed: true,
		})
		return
	}
	// The ending is attached before the loop starts, so a run that fails on
	// its first call still says so in the conversation.
	run.mu.Lock()
	run.onEnd = func(summary, failure string) {
		if failure != "" {
			d.append(sessionID, chatMessage{Role: "assistant", Text: failure, Failed: true})
			return
		}
		if summary = strings.TrimSpace(summary); summary != "" {
			d.append(sessionID, chatMessage{Role: "assistant", Text: summary})
		}
	}
	run.mu.Unlock()
	// The reply carries the run id, which is what makes the bubble the place
	// the Cancel button lives and what lets a reload find the run again.
	d.append(sessionID, chatMessage{Role: "assistant", Text: reply, RunID: run.id})
	go func() {
		defer cancel()
		d.srv.agents.drive(ctx, run, sessionID, harness, settings)
	}()
}

// runRefusal is the one policy lever and the one precondition, in the words
// the chat panel shows. It is the same pair handleAgentStart checks, said
// where a conversation can read them.
func runRefusal(row *store.Session, harness string, settings config.Settings) string {
	if harness == config.HarnessShell && !settings.Agent.AllowShell {
		return "The agent is not allowed to drive a shell session here - turn it on in /admin."
	}
	if row.State != store.StateRunning {
		return "This session has no running terminal to drive."
	}
	return ""
}

// tailMessages is the last few of a conversation.
func tailMessages(all []chatMessage, keep int) []chatMessage {
	if len(all) <= keep {
		return all
	}
	return all[len(all)-keep:]
}

// parseChatAnswer reads the object, and takes prose as an answer.
//
// A model that ignored the schema and wrote a plain sentence has still done
// the main thing asked of it, and throwing that away to show a parser error
// would make the chat worse exactly when the model is being unhelpful. Only
// `act` is lost, which is the half that must never be guessed at.
func parseChatAnswer(raw string) chatDecision {
	text := stripFence(strings.TrimSpace(raw))
	var decision chatDecision
	if strings.HasPrefix(text, "{") {
		if err := json.Unmarshal([]byte(text), &decision); err == nil {
			return decision
		}
	}
	return chatDecision{Reply: strings.TrimSpace(raw)}
}

// chatSystemPrompt is what the assistant is told it is, screen included.
//
// The rule about acting is the whole of the design: the default is to answer,
// and typing into somebody's terminal is the exception that the request has to
// ask for. A model that volunteered keystrokes for "what is it doing?" would
// be the feature nobody wants.
func chatSystemPrompt(label string, state termux.State, auto bool, language, screen string) string {
	var b strings.Builder
	b.WriteString("You are the assistant beside a terminal that runs " + label + ". ")
	b.WriteString("You are given the last " + strconv.Itoa(chatScreenLines) + " lines of its screen and what it is doing, ")
	b.WriteString("and you answer the person watching it.\n")
	b.WriteString("Answer with one JSON object and nothing else:\n")
	b.WriteString(`{"reply":"what you say to the person","act":null}` + "\n")
	b.WriteString("Set \"act\" to a short goal in one sentence - and only then - when the request cannot be " +
		"answered in words because it needs something typed into the terminal. An operator will be " +
		"given that goal and will read the screen and press the keys. A question about what is on " +
		"the screen, what something means or what to do next is answered with words and " +
		`"act":null.` + "\n")
	b.WriteString("Answer plainly: no markdown, no headings, no lists, no code fences. ")
	if auto {
		b.WriteString("Your reply is read out loud, so keep it to one or two spoken sentences and " +
			"leave out file paths and identifiers unless they are the point. ")
	} else {
		b.WriteString("Keep it short; a few sentences at most. ")
	}
	b.WriteString("Write the reply in " + language + ".\n")
	b.WriteString("The terminal is " + stateWords(state) + ".\nScreen:\n" + screen)
	return b.String()
}
