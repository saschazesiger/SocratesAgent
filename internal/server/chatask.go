package server

// A question about a passage of a chat: the person selects a few words of the
// conversation - a term they do not know, a line whose reason they missed -
// and asks about it. The model is the one Status uses, and it is given what
// Status is given: the chat's whole record (chatreport.go). It is a side
// conversation of the person's own: nothing of it reaches the chat's agent,
// and nothing of it is kept. A follow-up sends the earlier questions and
// answers again, so the server holds no state for it.

import (
	"context"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/openrouter"
)

// What one question may hold and how long it may take.
const (
	// askQuoteRunes is the longest passage that is quoted as it is. A longer
	// selection keeps its start and its end: what it is about is clear from
	// those, and the record holds the rest.
	askQuoteRunes = 6000
	// askQuestionRunes is the longest question, and the longest earlier
	// answer that is sent again with a follow-up.
	askQuestionRunes = 4000
	// askThreadTurns is how many earlier questions a follow-up carries.
	askThreadTurns = 12
	// askMaxTokens is a ceiling, not a target: a model that thinks first
	// spends its thinking out of the same budget.
	askMaxTokens = 4000
	// askTimeout bounds one answer. Someone is reading the dialog meanwhile.
	askTimeout = 120 * time.Second
)

// askMarker opens the instruction: what the call is for, and how a stand-in
// gateway tells it from the other calls.
const askMarker = "You answer a question about a passage of a coding session"

// askTurn is one earlier question of the same dialog and its answer.
type askTurn struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// askRequest is what the page sends.
type askRequest struct {
	Quote    string    `json:"quote"`
	Question string    `json:"question"`
	Thread   []askTurn `json:"thread"`
	// Preset is set when the question is one of the page's ready-made
	// ones: those are English, and are answered in the language Socrates
	// speaks (settings.voice.language) rather than in theirs.
	Preset bool `json:"preset"`
}

// askSystemPrompt is what the model is told the call is for.
func askSystemPrompt(language string, preset bool) string {
	tongue := "Answer in the language the question is written in."
	if preset {
		tongue = "The question is a ready-made one: answer in " + config.LanguageName(language) + "."
	}
	return askMarker + ". Its owner reads the chat between them and a coding agent, selected a passage of " +
		"it and asks about it - a word or term they do not know, why the agent did or said something, what " +
		"a line means for their work. You are given the chat's record with the time of every message and " +
		"call, the passage and the question.\n\n" +
		"Answer the question about that passage, from the record and from what you know. Use the record for " +
		"context: what the passage refers to, what happened before and after it. Explain a technical term in " +
		"plain words first and then what it means in this chat. When the record does not say something, say " +
		"so rather than guess.\n\n" +
		tongue + " Be short: a few sentences or a short list, " +
		"longer only when the question asks for it. Use Markdown (bold, lists, inline code, short code " +
		"blocks); no headings, no preamble, and do not repeat the question or the passage."
}

// askFirstPrompt is the first user message: the chat, the passage and the
// first question.
func askFirstPrompt(harness string, record, quote, question string) string {
	return "A chat with " + chatLabel(harness) + ", a coding agent.\n\nRecord:\n" + record +
		"\n\nThe passage the person selected:\n<<<\n" + quote + "\n>>>\n\nTheir question: " + question
}

// clipAsk keeps text within n runes. A cut text keeps its start and its
// end, joined by an ellipsis, when keepEnd is set, and only its start
// otherwise.
func clipAsk(text string, n int, keepEnd bool) string {
	if utf8.RuneCountInString(text) <= n {
		return text
	}
	if !keepEnd {
		return clipRunes(text, n)
	}
	runes := []rune(text)
	half := n / 2
	return strings.TrimSpace(string(runes[:half])) + "\n[…]\n" + strings.TrimSpace(string(runes[len(runes)-half:]))
}

// handleSessionAsk answers a question about a passage of a chat.
func (s *Server) handleSessionAsk(w http.ResponseWriter, r *http.Request) {
	row, ok := s.session(w, r)
	if !ok {
		return
	}
	var req askRequest
	if !readJSON(w, r, &req) {
		return
	}
	if !row.IsChat() {
		writeError(w, http.StatusBadRequest, "only a chat can be asked about")
		return
	}
	quote := clipAsk(strings.TrimSpace(req.Quote), askQuoteRunes, true)
	question := clipAsk(strings.TrimSpace(req.Question), askQuestionRunes, false)
	if question == "" {
		writeError(w, http.StatusBadRequest, "there is no question to answer")
		return
	}
	if quote == "" {
		writeError(w, http.StatusBadRequest, "select the part of the chat the question is about")
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
	report, ok := s.chats.reportOf(row.ID, time.Now())
	if !ok || report.Clock.FirstMessage == 0 {
		writeError(w, http.StatusBadRequest, "this chat has nothing to ask about yet")
		return
	}

	// The earlier questions and answers of the dialog, as the conversation
	// they were: the first carries the chat, the rest only themselves.
	thread := req.Thread
	if len(thread) > askThreadTurns {
		thread = thread[len(thread)-askThreadTurns:]
	}
	messages := []openrouter.Message{{Role: "system", Content: askSystemPrompt(settings.Voice.Language, req.Preset)}}
	ask := func(q string) {
		if len(messages) == 1 {
			q = askFirstPrompt(row.Harness, report.Record, quote, q)
		}
		messages = append(messages, openrouter.Message{Role: "user", Content: q})
	}
	for _, turn := range thread {
		q := clipAsk(strings.TrimSpace(turn.Question), askQuestionRunes, false)
		a := clipAsk(strings.TrimSpace(turn.Answer), askQuestionRunes, false)
		if q == "" || a == "" {
			continue
		}
		ask(q)
		messages = append(messages, openrouter.Message{Role: "assistant", Content: a})
	}
	ask(question)

	ctx, cancel := context.WithTimeout(r.Context(), askTimeout)
	defer cancel()
	temperature := 0.3
	client := openrouter.New(settings.OpenRouter.BaseURL, settings.OpenRouter.APIKey)
	res, err := client.Chat(ctx, openrouter.ChatRequest{
		Model:       model,
		Messages:    messages,
		Temperature: &temperature,
		MaxTokens:   askMaxTokens,
	}, nil)
	if err != nil {
		code, message := modelProblem(err, model, "status")
		writeError(w, code, message)
		return
	}
	answer := strings.TrimSpace(res.Content)
	if answer == "" {
		writeError(w, http.StatusBadGateway, "the model answered with nothing")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"answer": answer, "model": model})
}
