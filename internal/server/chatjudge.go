package server

// The verdict on a chat's turn (outcome.go): a terminal's comes with the read
// of its screen that a turn's end starts, and a chat has no screen to read.
// So a chat's ended turn is judged by a call of its own, from its journal -
// the person's instruction, the answer, the calls and how they went, the task
// list - into the same outcome, with the same hourglass while it is read and
// the same verdict on Home, in the sidebar and in the push once it is in.

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
	d.judgedLocked(id, in, answer, nil, model, why)
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
	// A verdict a notification waits for is a read somebody waits on.
	if !d.slot(ctx, true) {
		return nil, "the turn was not judged in time: too many sessions were being read at once"
	}
	defer d.release(true)
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

// rejudgeChat is a person asking for a chat's verdict to be read again: the
// verdict on the idle the chat is at, as rejudgeLocked does for a terminal.
func (d *digester) rejudgeChat(id string) {
	d.mu.Lock()
	a := d.seen[id]
	o := d.outcomes[id]
	again := o != nil && a.State == termux.StateIdle && o.Since == a.Since && o.Phase != digestPending &&
		outcomesOn(d.srv)
	if again {
		o.Phase, o.Error, o.Due = digestPending, "", 0
		d.publishOutcomeLocked(id, o)
	}
	d.mu.Unlock()
	if again {
		go d.judgeChat(id, a)
	}
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
