package engine

import (
	"context"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/openrouter"
)

// generateTitle names a chat after its first message. It is the one thing left
// that OpenRouter answers, besides transcribing what was spoken: the agents
// have their own credentials and their own models, and neither of them is
// asked to name anything.
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

func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max]) + "…"
}
