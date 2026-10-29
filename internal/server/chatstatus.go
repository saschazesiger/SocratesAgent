package server

// The Status of a chat: an analysis of the whole session rather than a
// sentence about its last turn. The model is given the chat's record
// (chatreport.go) - every message, call and question with its time - and the
// measured facts about its time and its requests, and answers what is
// happening now, what is still to do and how long everything the agent was
// asked will probably still take (statusETA calibrates that). How long it
// has run already is measured, not asked: the page shows the server's
// numbers. Nothing is read out loud until the person asks for it.

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/openrouter"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// statusMaxTokens is a ceiling, not a target: the answer is a short object,
// but a model that thinks first spends its thinking out of the same budget.
const statusMaxTokens = 2000

// statusMarker opens the analysis' instruction: what the call is for, and
// how a stand-in gateway tells it from the other calls.
const statusMarker = "You analyse a coding session for the person who owns it"

// chatAnalysis is the model's object.
type chatAnalysis struct {
	Goal      string   `json:"goal"`
	Now       string   `json:"now"`
	Remaining []string `json:"remaining"`
	// Progress is the share of the whole work done, in percent.
	Progress   *float64 `json:"progress"`
	ETAMinutes *float64 `json:"eta_minutes"`
	ETAReason  string   `json:"eta_reason"`
	Spoken     string   `json:"spoken"`
}

// statusSystemPrompt is what the analysis is asked, and the shape of its
// answer.
//
// The estimate the page shows is not the model's number alone (statusETA),
// so neither the spoken text nor the reason may state a total of their own:
// the server says the estimate in the spoken text itself.
func statusSystemPrompt(language string) string {
	return statusMarker + ", who wants to know at a glance - or by ear, in a car - what the agent " +
		"is doing and when it will be done with everything. You are given the measured facts about its time " +
		"and its whole record: every message, every call the agent made and every question, each with the " +
		"time it happened.\n" +
		"Answer with one JSON object and nothing else:\n" +
		`{"goal":"…","now":"…","remaining":["…"],"progress":40,"eta_minutes":25,"eta_reason":"…","spoken":"…"}` + "\n" +
		"- goal: a few words: what the person's current request is for (not a summary of the chat).\n" +
		"- now: one sentence: what is happening at this moment - the call that runs and what it is for, " +
		"or that it waits for the person and for what, or that it has finished and with what result.\n" +
		"- remaining: the steps still to come until the whole request is done, in order, each a few words " +
		"(at most 6; empty when it is done or only waits for the person).\n" +
		"- progress: how much of the whole work for the request is done, in percent (0-100).\n" +
		"- eta_minutes: how many more minutes of work until the agent has finished EVERYTHING it has been " +
		"asked so far - not the current step or turn: every open item of its task list, every part of the " +
		"person's request it has not reached yet, the background tasks and subagents it waits for and the " +
		"turns they will wake it for, the messages it has not taken up yet, the checks that usually follow " +
		"(builds, tests, reviews, screenshots, commits, deploys) and its final report. Leave out time it " +
		"will spend waiting for the person's answers. null only when nothing runs, nothing is queued and " +
		"no background task is left, or it waits for the person.\n" +
		"- eta_reason: one or two sentences on what is left and how long such steps took in this chat (for " +
		"example: 3 of 7 tasks left, each took about 8 minutes; then the tests and a commit). Do not state a " +
		"total.\n" +
		"- spoken: two to four short sentences to be read out loud: what it is working on, what it does " +
		"right now and how far along it is, speaking of the agent in the third person. Do not say how long it " +
		"will take: that sentence is added to it. " +
		"No markdown, no code, no file paths unless they are the point.\n" +
		"How to estimate: coding agents take far longer than their next step suggests - a request that has " +
		"run for an hour rarely ends in two minutes, and between the steps you can see there is reading, " +
		"retrying, fixing what a test found, and checking. Judge from the measured facts: how long this " +
		"request has run, how far it has got, how long the earlier requests and steps in this chat took.\n" +
		"Write every text in " + config.LanguageName(language) + ". Never contradict the measured facts."
}

// statusUserPrompt is the chat the analysis is asked about: the measured
// facts and the record.
func statusUserPrompt(harness string, report chatReport, created int64, now time.Time, verdict string) string {
	facts := clockFacts(report.Clock, created, now)
	if verdict != "" {
		facts += "A check of the turn that just ended says: " + verdict + "\n"
	}
	return "A chat with " + chatLabel(harness) + ", a coding agent.\n\nFacts:\n" + facts +
		"\nRecord:\n" + report.Record
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
	settings := s.Settings()
	client := openrouter.New(settings.OpenRouter.BaseURL, settings.OpenRouter.APIKey)
	s.emitStatus(row.ID, statusPhaseAsking, "Asking "+model)
	var got chatAnalysis
	err := askJSON(ctx, client, model, []openrouter.Message{
		{Role: "system", Content: statusSystemPrompt(language)},
		{Role: "user", Content: statusUserPrompt(row.Harness, report, row.CreatedAt, now, verdict)},
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
		spoken = plainStatus(strings.TrimSpace(got.Now + " " + got.ETAReason))
	}
	if spoken == "" {
		s.emitStatus(row.ID, statusPhaseError, "There was nothing to say about that chat.")
		writeError(w, http.StatusBadGateway, "the model answered with nothing to say")
		return
	}
	c := report.Clock
	analysis := map[string]any{
		"goal":       plainStatus(got.Goal),
		"now":        plainStatus(got.Now),
		"remaining":  plainList(got.Remaining),
		"eta_reason": plainStatus(got.ETAReason),
	}
	if p := got.Progress; p != nil && !math.IsNaN(*p) {
		analysis["progress"] = math.Round(min(max(*p, 0), 100))
	}
	if eta, ok := statusETA(got, c); ok {
		analysis["eta_minutes"] = eta.minutes
		analysis["eta_low"] = eta.low
		analysis["eta_high"] = eta.high
		spoken = strings.TrimSpace(spoken + " " + etaWords(eta, language))
	}
	s.emitStatus(row.ID, statusPhaseDone, spoken)
	work := c.Work
	writeJSON(w, http.StatusOK, map[string]any{
		"text":     spoken,
		"language": language,
		"state":    string(c.State),
		"model":    model,
		"analysis": analysis,
		"clock": map[string]any{
			"now":           c.Now,
			"first_message": c.FirstMessage,
			"turn_started":  c.TurnStarted,
			"asked_at":      c.AskedAt,
			"last_ended":    c.LastEnded,
			"turns":         c.Turns,
			"worked_ms":     c.Worked,
			"call":          c.CallLine,
			"call_started":  c.CallStarted,
			"work_started":  work.Start,
			"work_ms":       work.Working(c.Now),
			"work_turns":    work.Turns,
			"work_calls":    work.Calls,
			"background":    c.Background,
			"agents":        c.Agents,
			"queued":        c.Queued,
		},
		"tasks": statusTasks(c.Tasks),
	})
}

// statusEstimate is the estimate as the page shows it, in whole minutes:
// the likeliest value and a range around it.
type statusEstimate struct {
	minutes, low, high float64
}

// statusETA is how long the work will probably still take, or false when
// the model says nothing is left to wait for.
//
// A model's own number is a poor estimate: backtested against 139 moments
// of 37 requests in the real journals of this server (statuseval_test.go),
// the old Status - which asked for the current turn - was off by a factor
// of 2.7 at the median and within a factor of two of the truth 41% of the
// time, and asking for the whole of the work did not change that: too low
// early in a long piece of work, too high near the end of a short one. What
// the page shows is the geometric mean of three estimates, which was off by
// a factor of 2.0 at the median and within a factor of two half the time:
// the model's, how long the request has run so far (work that has run for a
// while most often needs about as long again), and that time carried
// forward by the share the model says is done. The range is a factor of two
// either way, which is about how often the truth fell inside it.
func statusETA(a chatAnalysis, c chatClock) (statusEstimate, bool) {
	m := a.ETAMinutes
	if m == nil || math.IsNaN(*m) || math.IsInf(*m, 0) || *m < 0 {
		return statusEstimate{}, false
	}
	// Resting with nothing left behind it is done, whatever the model's
	// number: the time it has run would make an estimate of nothing.
	if c.State == termux.StateIdle && c.Background == 0 && c.Queued == 0 {
		return statusEstimate{}, false
	}
	floor := func(v float64) float64 { return max(v, statusETAFloor) }
	elapsed := float64(c.Work.Working(c.Now)) / 60000
	logs := []float64{math.Log(floor(min(*m, 100000))), math.Log(floor(elapsed))}
	if p := a.Progress; p != nil && !math.IsNaN(*p) {
		done := min(max(*p, 5), 95)
		logs = append(logs, math.Log(floor(elapsed*(100-done)/done)))
	}
	sum := 0.0
	for _, l := range logs {
		sum += l
	}
	eta := math.Max(1, math.Round(math.Exp(sum/float64(len(logs)))))
	return statusEstimate{minutes: eta, low: math.Max(1, math.Round(eta/2)), high: eta * 2}, true
}

// statusETAFloor is the least each estimate counts for, in minutes: a
// request that started a moment ago has not run for no time at all, and a
// logarithm of nothing is no estimate.
const statusETAFloor = 0.5

// etaWords is the estimate as one sentence for the voice, in the language
// the rest is read in.
func etaWords(e statusEstimate, language string) string {
	span := func(total float64, de bool) string {
		m := int(math.Round(total))
		h, rest := m/60, m%60
		if de {
			minutes := func(n int) string {
				if n == 1 {
					return "eine Minute"
				}
				return strconv.Itoa(n) + " Minuten"
			}
			switch {
			case m < 2:
				return "eine Minute"
			case h == 0:
				return minutes(m)
			case rest == 0 && h == 1:
				return "eine Stunde"
			case rest == 0:
				return strconv.Itoa(h) + " Stunden"
			case h == 1:
				return "eine Stunde und " + minutes(rest)
			}
			return strconv.Itoa(h) + " Stunden und " + minutes(rest)
		}
		minutes := func(n int) string {
			if n == 1 {
				return "1 minute"
			}
			return strconv.Itoa(n) + " minutes"
		}
		switch {
		case m < 2:
			return "a minute"
		case h == 0:
			return minutes(m)
		case rest == 0 && h == 1:
			return "an hour"
		case rest == 0:
			return strconv.Itoa(h) + " hours"
		case h == 1:
			return "an hour and " + minutes(rest)
		}
		return strconv.Itoa(h) + " hours and " + minutes(rest)
	}
	if config.NormalizeLanguage(language) == "de" {
		return "Bis alles fertig ist, dauert es voraussichtlich noch etwa " + span(e.minutes, true) + "."
	}
	return "It will probably need about " + span(e.minutes, false) + " more until everything is done."
}

// plainList is a list of the model's words, without the markdown and the
// empty ones.
func plainList(items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if item = plainStatus(strings.TrimLeft(item, "-*• ")); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// statusTasks is the task list as the page draws it.
func statusTasks(tasks []chatTask) []map[string]string {
	out := make([]map[string]string, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, map[string]string{"subject": t.Subject, "status": t.Status})
	}
	return out
}
