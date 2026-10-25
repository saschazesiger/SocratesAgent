package server

// An answer said for the ear: one of the agent's answers rewritten for
// reading it out loud to somebody who cannot look at the screen. It reads the
// journal and nothing else, and uses StatusModel.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/openrouter"
)

const (
	// spokenTimeout bounds one answer said for the ear, and spokenKeep how
	// many of them are kept, so that pressing Read aloud twice is one call.
	spokenTimeout = 60 * time.Second
	spokenKeep    = 64
	// spokenPlainRunes is an answer short enough to read as it is, when it
	// has no Markdown that a voice would trip over.
	spokenPlainRunes = 280
)

// spokenMarker does the same for an answer said for the ear.
const spokenMarker = "You rewrite a coding agent's answer to be read out loud"

// spokenCache keeps the answers said for the ear, so that pressing Read
// aloud twice is one call.
type spokenCache struct {
	mu     sync.Mutex
	spoken map[string]string
	order  []string
}

func newSpokenCache() *spokenCache { return &spokenCache{spoken: map[string]string{}} }

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
	s.spoken.mu.Lock()
	kept, have := s.spoken.spoken[key]
	s.spoken.mu.Unlock()
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
	s.spoken.mu.Lock()
	if _, had := s.spoken.spoken[key]; !had {
		s.spoken.order = append(s.spoken.order, key)
		if len(s.spoken.order) > spokenKeep {
			delete(s.spoken.spoken, s.spoken.order[0])
			s.spoken.order = s.spoken.order[1:]
		}
	}
	s.spoken.spoken[key] = text
	s.spoken.mu.Unlock()
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
