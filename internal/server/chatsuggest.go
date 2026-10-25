package server

// Two things a model does with a chat's answer for the person rather than
// for the agent: the messages the person most likely sends next, offered as
// chips over the composer when a turn ends, and an answer said for the ear,
// for reading it out loud to somebody who cannot look at the screen. Both
// read the journal and nothing else, and both use StatusModel.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/openrouter"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

const (
	// suggestTimeout bounds one suggestion call, and suggestWait how long a
	// page that asks waits for one on its way.
	suggestTimeout = 60 * time.Second
	suggestWait    = 30 * time.Second
	// suggestMax is how many chips there are at most, and suggestRunes how
	// long one may be.
	suggestMax   = 3
	suggestRunes = 160
	// suggestConcurrency bounds the calls for every chat together.
	suggestConcurrency = 3

	// spokenTimeout bounds one answer said for the ear, and spokenKeep how
	// many of them are kept, so that pressing Read aloud twice is one call.
	spokenTimeout = 60 * time.Second
	spokenKeep    = 64
	// spokenPlainRunes is an answer short enough to read as it is, when it
	// has no Markdown that a voice would trip over.
	spokenPlainRunes = 280
)

// suggestMarker opens the suggestion instruction: what the call is for, and
// how a stand-in gateway tells it from the other calls.
const suggestMarker = "You suggest the next message a person sends to a coding agent"

// spokenMarker does the same for an answer said for the ear.
const spokenMarker = "You rewrite a coding agent's answer to be read out loud"

// suggestions are the chips of one chat's last turn.
type suggestions struct {
	TurnID string
	done   chan struct{}
	Items  []string
	Error  string
}

// suggester makes and keeps the suggestions of every chat's last turn.
type suggester struct {
	srv *Server
	sem chan struct{}

	mu     sync.Mutex
	seen   map[string]termux.Activity
	byChat map[string]*suggestions
	spoken map[string]string
	order  []string
}

func newSuggester(s *Server) *suggester {
	return &suggester{
		srv:    s,
		sem:    make(chan struct{}, suggestConcurrency),
		seen:   map[string]termux.Activity{},
		byChat: map[string]*suggestions{},
		spoken: map[string]string{},
	}
}

func suggestionsOn(s *Server) bool {
	settings := s.Settings()
	return settings.OpenRouter.Suggestions && strings.TrimSpace(settings.OpenRouter.APIKey) != ""
}

// observe takes every committed activity change and starts the suggestions
// of a turn that has just ended. It returns at once.
func (g *suggester) observe(id string, a termux.Activity) {
	if g == nil {
		return
	}
	g.mu.Lock()
	prev, had := g.seen[id]
	g.seen[id] = a
	g.mu.Unlock()
	if had && endsTurn(prev, a) && suggestionsOn(g.srv) {
		go g.start(id)
	}
}

// forget drops what is kept of a deleted chat.
func (g *suggester) forget(id string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.seen, id)
	delete(g.byChat, id)
}

// suggestTurn is what the suggestions of a chat are made from: its last turn
// when it completed, nothing runs, and it does not wait on a question - the
// dialog answers those.
func suggestTurn(p *agent.Projection) (string, bool) {
	t := p.LastTurn
	if p.Turn != nil || t == nil || t.Outcome != agent.OutcomeCompleted || waitingAsk(p) != nil {
		return "", false
	}
	return t.ID, true
}

// start answers the suggestions of a chat's last turn, made or on their way,
// and starts them when there are none: nil when that turn is none to make
// them for.
func (g *suggester) start(id string) *suggestions {
	row, err := g.srv.store.GetSession(id)
	if err != nil || !row.IsChat() {
		return nil
	}
	p, err := g.srv.chats.peer(id)
	if err != nil {
		return nil
	}
	turnID, record := "", ""
	_ = p.view(func(proj *agent.Projection) {
		id, ok := suggestTurn(proj)
		if !ok {
			return
		}
		turnID = id
		report := readChatReport(proj, time.Now())
		record = "Facts:\n" + clockFacts(report.Clock, row.CreatedAt, time.Now()) + "\nRecord:\n" + report.Record
	})
	if turnID == "" {
		return nil
	}
	g.mu.Lock()
	if have := g.byChat[id]; have != nil && have.TurnID == turnID {
		g.mu.Unlock()
		return have
	}
	next := &suggestions{TurnID: turnID, done: make(chan struct{})}
	g.byChat[id] = next
	g.mu.Unlock()

	go func() {
		defer close(next.done)
		items, why := g.ask(row.Harness, record)
		g.mu.Lock()
		next.Items, next.Error = items, why
		g.mu.Unlock()
	}()
	return next
}

// ask is the call: the record in, two or three messages out.
func (g *suggester) ask(harness, record string) ([]string, string) {
	settings := g.srv.Settings()
	model := strings.TrimSpace(settings.OpenRouter.StatusModel)
	if model == "" {
		model = config.DefaultStatusModel
	}
	ctx, cancel := context.WithTimeout(context.Background(), suggestTimeout)
	defer cancel()
	select {
	case g.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, "too many suggestions were being made at once"
	}
	defer func() { <-g.sem }()
	client := openrouter.New(settings.OpenRouter.BaseURL, settings.OpenRouter.APIKey)
	var got struct {
		Suggestions []string `json:"suggestions"`
	}
	err := askJSON(ctx, client, model, []openrouter.Message{
		{Role: "system", Content: suggestSystemPrompt()},
		{Role: "user", Content: "A chat with " + chatLabel(harness) + ", a coding agent. Its turn has just ended.\n\n" + record},
	}, 1000, 0.4, &got)
	if err != nil {
		if errors.Is(err, errDigestJSON) {
			return nil, "the model " + model + " did not answer with usable JSON"
		}
		_, why := modelProblem(err, model, "status")
		return nil, why
	}
	return cleanSuggestions(got.Suggestions), ""
}

func suggestSystemPrompt() string {
	return suggestMarker + ", right after the agent has ended its turn. You are given the chat's " +
		"record: every message, call and question with its time.\n" +
		"Suggest the two or three messages the person is most likely to send next, each the way the " +
		"person would write it to the agent: short (at most a dozen words), concrete, in the language " +
		"the person writes in. Good suggestions carry the work on: the obvious next step, checking what " +
		"was just done (run the tests, show the diff), answering a question the agent asked at the end " +
		"of its answer, or wrapping up (commit and push). Never suggest what has already been done.\n" +
		"Answer with one JSON object and nothing else: " + `{"suggestions":["…","…"]}`
}

// cleanSuggestions keeps what a chip can hold: one line each, no doubles, at
// most suggestMax.
func cleanSuggestions(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.Trim(oneLine(strings.NewReplacer("**", "", "`", "").Replace(s), suggestRunes), " \"'“”")
		key := strings.ToLower(s)
		if s == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, s)
		if len(out) == suggestMax {
			break
		}
	}
	return out
}

// handleChatSuggestions answers the chips of a chat's last turn, waiting for
// them while they are on their way.
func (s *Server) handleChatSuggestions(w http.ResponseWriter, r *http.Request) {
	row, _, ok := s.chatOf(w, r)
	if !ok {
		return
	}
	empty := map[string]any{"turn_id": "", "items": []string{}}
	if !suggestionsOn(s) {
		writeJSON(w, http.StatusOK, empty)
		return
	}
	got := s.suggests.start(row.ID)
	if got == nil {
		writeJSON(w, http.StatusOK, empty)
		return
	}
	select {
	case <-got.done:
	case <-time.After(suggestWait):
	case <-r.Context().Done():
		return
	}
	s.suggests.mu.Lock()
	items, why := append([]string{}, got.Items...), got.Error
	s.suggests.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"turn_id": got.TurnID, "items": items, "error": why})
}

/* ------------------------------------------------- an answer for the ear */

// handleChatSpoken answers one of the agent's answers said for the ear: what
// it did, what came of it and what it asks, in a few spoken sentences in the
// voice's language. Code, paths and tables are told, not read.
func (s *Server) handleChatSpoken(w http.ResponseWriter, r *http.Request) {
	row, p, ok := s.chatOf(w, r)
	if !ok {
		return
	}
	var body struct {
		MessageID string `json:"message_id"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	answer := ""
	_ = p.view(func(proj *agent.Projection) {
		if m := proj.Message(body.MessageID); m != nil && m.Role == agent.RoleAssistant {
			var texts []string
			for _, part := range m.Parts {
				if part.Kind == agent.PartText && strings.TrimSpace(part.Text) != "" {
					texts = append(texts, strings.TrimSpace(part.Text))
				}
			}
			answer = strings.Join(texts, "\n\n")
		}
	})
	if answer == "" {
		writeError(w, http.StatusBadRequest, "that answer has nothing to read out loud")
		return
	}
	settings := s.Settings()
	language := config.NormalizeLanguage(settings.Voice.Language)
	if plainEnough(answer) {
		writeJSON(w, http.StatusOK, map[string]any{"text": answer, "language": language})
		return
	}
	if strings.TrimSpace(settings.OpenRouter.APIKey) == "" {
		writeError(w, http.StatusBadRequest, "no OpenRouter API key is configured - open /admin and add your key")
		return
	}
	key := row.ID + "/" + body.MessageID + "/" + language + "/" + textKey(answer)
	s.suggests.mu.Lock()
	kept, have := s.suggests.spoken[key]
	s.suggests.mu.Unlock()
	if have {
		writeJSON(w, http.StatusOK, map[string]any{"text": kept, "language": language})
		return
	}
	model := strings.TrimSpace(settings.OpenRouter.StatusModel)
	if model == "" {
		model = config.DefaultStatusModel
	}
	ctx, cancel := context.WithTimeout(r.Context(), spokenTimeout)
	defer cancel()
	temperature := 0.2
	client := openrouter.New(settings.OpenRouter.BaseURL, settings.OpenRouter.APIKey)
	res, err := client.Chat(ctx, openrouter.ChatRequest{
		Model: model,
		Messages: []openrouter.Message{
			{Role: "system", Content: spokenSystemPrompt(language)},
			{Role: "user", Content: clipRunes(answer, 60000)},
		},
		Temperature: &temperature,
		MaxTokens:   1200,
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
	s.suggests.mu.Lock()
	if _, had := s.suggests.spoken[key]; !had {
		s.suggests.order = append(s.suggests.order, key)
		if len(s.suggests.order) > spokenKeep {
			delete(s.suggests.spoken, s.suggests.order[0])
			s.suggests.order = s.suggests.order[1:]
		}
	}
	s.suggests.spoken[key] = text
	s.suggests.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"text": text, "language": language, "model": model})
}

func spokenSystemPrompt(language string) string {
	return spokenMarker + " to the person who asked for it, who cannot look at the screen - they may be " +
		"driving. In two to six short sentences, say what the agent did, what came of it, and any " +
		"question it asks the person, with the choices. Leave out code, command lines, file paths, URLs " +
		"and tables unless they are the point; say in words what they show. Plain sentences only: no " +
		"Markdown, no lists, no symbols a voice would read out. Write in " + config.LanguageName(language) +
		", whatever language the answer is in. Answer with the sentences and nothing else."
}

// plainEnough is an answer a voice can read as it stands: short, and with
// nothing in it that is drawn rather than said.
func plainEnough(text string) bool {
	if len([]rune(text)) > spokenPlainRunes {
		return false
	}
	for _, mark := range []string{"```", "`", "|", "\n-", "\n*", "\n#", "#", "http", "/"} {
		if strings.Contains(text, mark) {
			return false
		}
	}
	return true
}

// textKey keys an answer's spoken version by its words, so that an answer
// still being written is not read from what it said before.
func textKey(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:8])
}
