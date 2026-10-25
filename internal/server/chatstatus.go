package server

// The Status of a chat: an analysis of the whole session rather than a
// sentence about its last turn. The model is given the chat's record
// (chatreport.go) - every message, call and question with its time - and the
// measured facts about its time, and answers what the session is about, what
// is happening now and how long the rest will probably take. How long it has
// run already is measured, not asked: the page shows the server's numbers.

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/openrouter"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// statusMaxTokens is a ceiling, not a target: the answer is a short object,
// but a model that thinks first spends its thinking out of the same budget.
const statusMaxTokens = 2000

// statusMarker opens the analysis' instruction: what the call is for, and
// how a stand-in gateway tells it from the other calls.
const statusMarker = "You analyse a coding session for the person who owns it"

// chatAnalysis is the model's object.
type chatAnalysis struct {
	Summary    string   `json:"summary"`
	Now        string   `json:"now"`
	ETA        string   `json:"eta"`
	ETAMinutes *float64 `json:"eta_minutes"`
	Spoken     string   `json:"spoken"`
}

// statusSystemPrompt is what the analysis is asked, and the shape of its
// answer.
func statusSystemPrompt(language string) string {
	return statusMarker + ", who wants to know at a glance - or by ear, in a car - where it " +
		"stands. You are given the facts about its time, which are measured, and its whole record: every " +
		"message, every call the agent made and every question, each with the time it happened.\n" +
		"Answer with one JSON object and nothing else:\n" +
		`{"summary":"…","now":"…","eta":"…","eta_minutes":12,"spoken":"…"}` + "\n" +
		"- summary: two or three sentences: what the session is about, and what has been done so far.\n" +
		"- now: one sentence: what is happening at this moment - the call that runs and what it is for, " +
		"or that it waits for the person and for what, or that it has finished and with what result.\n" +
		"- eta: a short phrase with your estimate of how long the current work will still take, and in a " +
		"few words why (for example: about 10 minutes - 3 of 7 tasks left, each took about 3 minutes). " +
		"Base it on the task list, the pace of this session's earlier calls and turns, and what usually " +
		"still comes (tests, a review, the final answer). When no turn runs, say that nothing is running " +
		"or that it waits for the person. A range is fine; precision you do not have is not.\n" +
		"- eta_minutes: that estimate in minutes, as a number, or null when nothing runs or it waits " +
		"for the person.\n" +
		"- spoken: three to five short sentences to be read out loud, covering what it is about, what " +
		"runs now, how long the current turn has been at it - or, between turns, since when it rests - " +
		"(from the facts, rounded the way a person says it) and your estimate. No markdown, no code, no file paths unless they are the point.\n" +
		"Write every text in " + config.LanguageName(language) + ". Never contradict the measured facts."
}

// statusOfChat answers a chat's Status press with the analysis.
func (s *Server) statusOfChat(ctx context.Context, w http.ResponseWriter, row *store.Session,
	model, language, verdict string) {
	now := time.Now()
	report, ok := s.chats.reportOf(row.ID, now)
	if !ok || report.Clock.FirstMessage == 0 {
		writeError(w, http.StatusBadRequest, "this chat has nothing to say yet: send it a message first")
		return
	}
	facts := clockFacts(report.Clock, row.CreatedAt, now)
	if verdict != "" {
		facts += "A check of the turn that just ended says: " + verdict + "\n"
	}
	settings := s.Settings()
	client := openrouter.New(settings.OpenRouter.BaseURL, settings.OpenRouter.APIKey)
	s.emitStatus(row.ID, statusPhaseAsking, "Asking "+model)
	var got chatAnalysis
	err := askJSON(ctx, client, model, []openrouter.Message{
		{Role: "system", Content: statusSystemPrompt(language)},
		{Role: "user", Content: "A chat with " + chatLabel(row.Harness) + ", a coding agent.\n\nFacts:\n" + facts +
			"\nRecord:\n" + report.Record},
	}, statusMaxTokens, 0.2, &got)
	if err != nil {
		code, message := modelProblem(err, model, "status")
		if errors.Is(err, errDigestJSON) {
			code, message = http.StatusBadGateway, "the status model "+model+" did not answer with usable JSON"
		}
		s.emitStatus(row.ID, statusPhaseError, "That session could not be summarised.")
		writeError(w, code, message)
		return
	}
	spoken := plainStatus(got.Spoken)
	if spoken == "" {
		spoken = plainStatus(strings.TrimSpace(got.Summary + " " + got.Now + " " + got.ETA))
	}
	if spoken == "" {
		s.emitStatus(row.ID, statusPhaseError, "There was nothing to say about that chat.")
		writeError(w, http.StatusBadGateway, "the model answered with nothing to say")
		return
	}
	s.emitStatus(row.ID, statusPhaseSpeaking, "Speaking")
	s.emitStatus(row.ID, statusPhaseDone, spoken)

	c := report.Clock
	out := map[string]any{
		"text":     spoken,
		"language": language,
		"state":    string(c.State),
		"model":    model,
		"analysis": map[string]any{
			"summary": plainStatus(got.Summary),
			"now":     plainStatus(got.Now),
			"eta":     plainStatus(got.ETA),
		},
		"clock": map[string]any{
			"now":           c.Now,
			"first_message": c.FirstMessage,
			"turn_started":  c.TurnStarted,
			"asked_at":      c.AskedAt,
			"last_ended":    c.LastEnded,
			"turns":         c.Turns,
			"worked_ms":     c.Worked,
		},
	}
	if m := got.ETAMinutes; m != nil && !math.IsNaN(*m) && *m >= 0 && *m < 100000 {
		out["analysis"].(map[string]any)["eta_minutes"] = math.Round(*m)
	}
	writeJSON(w, http.StatusOK, out)
}
