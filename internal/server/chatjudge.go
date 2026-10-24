package server

// The verdict on a chat's turn (outcome.go): a chat's ended turn is judged by
// the home model from its journal - the person's instruction, the answer, the
// calls and how they went, the task list - with an hourglass while it is
// read and the verdict on Home, in the sidebar and in the push once it is in.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/openrouter"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// chatJudgeMarker opens every chat judge's instruction: what the call is
// for, and how a stand-in gateway tells it from the other calls.
const chatJudgeMarker = "You judge the turn a coding agent has just ended"

// chatJudgeSystemPrompt is what a chat's judge is asked, and the shape of
// its answer.
func chatJudgeSystemPrompt() string {
	return chatJudgeMarker + ", for Home, the page of Socrates that shows a person how each of their " +
		"coding sessions left the work. You are given the turn as the chat recorded it: the last " +
		"instruction the person gave the program, its answer, the calls it made and how they went, and " +
		"its task list.\nAnswer with one JSON object and nothing else:\n" +
		`{"outcome":{"kind":"complete|partial|needs_input|blocked","reason":"…","todos":["…"]}}` + "\n" +
		outcomeRules()
}

// judgeChat reads the verdict on the turn a chat ended in activity in, and
// ends the outcome waiting for it (judgedLocked) with the verdict or with why
// there is none.
func (d *digester) judgeChat(id string, in termux.Activity) {
	settings := d.srv.Settings()
	model := strings.TrimSpace(settings.OpenRouter.HomeModel)
	if model == "" {
		model = config.DefaultHomeModel
	}
	answer, why := d.askChatJudge(id, settings, model)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.judgedLocked(id, in, answer, model, why)
}

// askChatJudge is the call: the turn read from the journal, the model asked
// within the digests' bounds on calls and time.
func (d *digester) askChatJudge(id string, settings config.Settings, model string) (*outcomeAnswer, string) {
	key := strings.TrimSpace(settings.OpenRouter.APIKey)
	if key == "" {
		return nil, digestNoKey
	}
	turn, ok := d.srv.chats.endedTurn(id)
	if !ok {
		return nil, "the chat's last turn could not be read"
	}
	ctx, cancel := context.WithTimeout(context.Background(), digestQueueWait)
	defer cancel()
	if !d.slot(ctx) {
		return nil, "the turn was not judged in time: too many turns were being judged at once"
	}
	defer d.release()
	ctx, stop := context.WithTimeout(ctx, digestTimeout)
	defer stop()
	client := openrouter.New(settings.OpenRouter.BaseURL, key)
	got, err := askDigest(ctx, client, model, []openrouter.Message{
		{Role: "system", Content: chatJudgeSystemPrompt()},
		{Role: "user", Content: chatTurnPrompt(turn)},
	}, outcomeMaxTokens)
	if err != nil {
		var refusal *openrouter.StatusError
		switch {
		case errors.As(err, &refusal):
			_, why := modelProblem(err, model, "home")
			return nil, why
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return nil, fmt.Sprintf("the home model %s did not answer within %s", model, digestTimeout)
		}
		return nil, err.Error()
	}
	return got.Outcome, ""
}

// endedTurn is the turn a chat ended last, as its judge is told it.
func (h *chatHub) endedTurn(id string) (chatTurn, bool) {
	p, err := h.peer(id)
	if err != nil {
		return chatTurn{}, false
	}
	var turn chatTurn
	ok := false
	_ = p.view(func(proj *agent.Projection) {
		if proj.Turn == nil && proj.LastTurn != nil {
			turn, ok = readChatTurn(proj, proj.LastTurn), true
		}
	})
	return turn, ok
}
