//go:build statuseval

package server

// The backtest of a chat's Status estimate, against real journals. It is not
// part of the suite (the build tag keeps it out): it spends money at
// OpenRouter and reads journals that are not in the repository.
//
//	STATUS_EVAL_JOURNALS=/dir/of/*.jsonl STATUS_EVAL_KEY=sk-or-... \
//	STATUS_EVAL_VARIANTS=old,new STATUS_EVAL_MODELS=anthropic/claude-haiku-5.5 \
//	STATUS_EVAL_OUT=results.jsonl STATUS_EVAL_LIMIT=40 \
//	go test -tags statuseval -run TestStatusEval -v -timeout 2h ./internal/server
//
// Every journal is cut into episodes: a message from the person, and all the
// work the agent did for it - turns, background tasks, subagents, the turns a
// background task woke it for - until it rested with nothing left to do.
// Each episode is cut at a few moments while a turn ran, the chat is folded
// to that moment and the Status analysis is asked, from exactly what it would
// have seen then. The truth is how long the work really went on after that
// moment, without the time it waited for the person's answers.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/openrouter"
)

// evalEpisode is one request and the work it took.
type evalEpisode struct {
	start, end int64
	// waits are the spans the chat waited for the person, and interjections
	// the moments the person said more while the work went on.
	waits         [][2]int64
	interjections []int64
	// turned is whether a turn has started for it yet: until then the
	// chat is idle because the message has not been taken up.
	turned bool
}

// evalSample is one moment a Status could have been asked.
type evalSample struct {
	Chat    string  `json:"chat"`
	Harness string  `json:"harness"`
	At      int64   `json:"at"`
	Start   int64   `json:"start"`
	End     int64   `json:"end"`
	Frac    float64 `json:"frac"`
	// Elapsed and Truth are working minutes: before and after the moment.
	Elapsed     float64 `json:"elapsed_min"`
	Truth       float64 `json:"truth_min"`
	Interjected bool    `json:"interjected"`

	events  []agent.Event
	created int64
}

// evalResult is one variant's answer to one sample.
type evalResult struct {
	evalSample
	Variant string `json:"variant"`
	Model   string `json:"model"`
	// Pred is the model's own estimate, Shown the one the page shows.
	Pred   *float64        `json:"pred_min"`
	Shown  *float64        `json:"shown_min"`
	Answer json.RawMessage `json:"answer,omitempty"`
	Err    string          `json:"err,omitempty"`
	Runes  int             `json:"prompt_runes"`
	// WorkMin is how long the request has run as the server measures it
	// (workSpan), which is what an estimate made from it has to go by.
	WorkMin float64 `json:"work_min"`
	Secs    float64 `json:"secs"`
}

// evalEpisodes finds the episodes of one journal.
func evalEpisodes(events []agent.Event) []evalEpisode {
	// settle is how long a rest has to last to be the end of the work: a
	// background task that reports back starts the next turn a moment
	// after the last one ended, and the tasks event can come first.
	const settle = 2 * 60 * 1000
	p := agent.NewProjection()
	var out []evalEpisode
	var cur *evalEpisode
	waitFrom, lastTurnEnd, restAt := int64(0), int64(0), int64(0)
	finish := func(end int64) {
		cur.end = end
		out = append(out, *cur)
		cur, waitFrom, restAt = nil, 0, 0
	}
	for _, ev := range events {
		at := ev.At
		if cur != nil && restAt != 0 && at-restAt > settle {
			finish(restAt)
		}
		before := p.Summary()
		p.Apply(ev)
		after := p.Summary()
		if ev.Type == agent.TypeTurnEnded {
			lastTurnEnd = at
		}
		// A turn the agent starts by itself an hour after it rested is a
		// monitor it left, not the work of the request any more.
		if ev.Type == agent.TypeTurnStarted && ev.MessageID == "" && cur != nil &&
			before.Activity == agent.ActivityTasks && at-lastTurnEnd > time.Hour.Milliseconds() {
			finish(lastTurnEnd)
		}
		// A message from the person (not an answer to a question).
		if ev.Type == agent.TypeMessage && ev.Role == agent.RoleUser {
			m := evalMessage(p, ev.MessageID)
			if m != nil && m.AnswerTo == "" {
				switch {
				case cur != nil && messageOrigin(m) != originPerson:
					// Another chat or Socrates itself: more for the work
					// that runs, which nobody could have foreseen.
					cur.interjections = append(cur.interjections, at)
				case cur == nil:
				case restAt != 0:
					finish(restAt)
				case before.Activity == agent.ActivityTasks && !before.TurnRunning:
					// Shells left running, and the person is back: the work
					// for the last request ended with its last turn.
					finish(lastTurnEnd)
				default:
					cur.interjections = append(cur.interjections, at)
				}
				if cur == nil {
					cur = &evalEpisode{start: at}
				}
			}
		}
		if cur == nil {
			continue
		}
		if ev.Type == agent.TypeTurnStarted {
			cur.turned = true
		}
		if after.Activity == agent.ActivityWaiting && waitFrom == 0 {
			waitFrom = at
		}
		if after.Activity != agent.ActivityWaiting && waitFrom != 0 {
			cur.waits = append(cur.waits, [2]int64{waitFrom, at})
			waitFrom = 0
		}
		resting := after.Activity == agent.ActivityIdle && after.Queued == 0 && !after.TurnRunning && cur.turned
		switch {
		case resting && restAt == 0:
			restAt = at
		case !resting:
			restAt = 0
		}
	}
	if cur != nil && restAt != 0 {
		finish(restAt)
	}
	// Resting with shells left running, long before the journal was read:
	// the work ended with its last turn.
	if cur != nil && len(events) > 0 && !p.Summary().TurnRunning && p.Summary().Activity == agent.ActivityTasks &&
		events[len(events)-1].At-lastTurnEnd > 20*time.Minute.Milliseconds() && lastTurnEnd > cur.start {
		finish(lastTurnEnd)
	}
	return out
}

func evalMessage(p *agent.Projection, id string) *agent.Message {
	for i := len(p.Messages) - 1; i >= 0; i-- {
		if p.Messages[i].ID == id {
			return p.Messages[i]
		}
	}
	return nil
}

// working is the span from a to b without the waits in it.
func (e evalEpisode) working(a, b int64) int64 {
	d := b - a
	for _, w := range e.waits {
		lo, hi := max(w[0], a), min(w[1], b)
		if hi > lo {
			d -= hi - lo
		}
	}
	return max(d, 0)
}

// evalSamples cuts every episode of every journal at the given fractions.
func evalSamples(t *testing.T, dir string, fracs []float64, minEpisode time.Duration) []evalSample {
	paths, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	var out []evalSample
	for _, path := range paths {
		events, err := agent.ReadJournal(path)
		if err != nil || len(events) == 0 {
			t.Logf("%s: %v", path, err)
			continue
		}
		harness := "claude"
		for _, ev := range events {
			if ev.Type == agent.TypeAgentInfo {
				var info struct {
					Agent string `json:"agent"`
				}
				if json.Unmarshal(ev.Content, &info) == nil && info.Agent != "" {
					harness = info.Agent
				}
				break
			}
		}
		chat := strings.TrimSuffix(filepath.Base(path), ".jsonl")
		episodes := evalEpisodes(events)
		if os.Getenv("STATUS_EVAL_DEBUG") != "" {
			for _, ep := range episodes {
				t.Logf("  episode %s - %s working %.1f min, %d waits, %d interjections",
					time.UnixMilli(ep.start).Format("01-02 15:04"), time.UnixMilli(ep.end).Format("01-02 15:04"),
					float64(ep.working(ep.start, ep.end))/60000, len(ep.waits), len(ep.interjections))
			}
		}
		used := 0
		for _, ep := range episodes {
			if time.Duration(ep.working(ep.start, ep.end))*time.Millisecond < minEpisode {
				continue
			}
			used++
			for _, f := range fracs {
				at := ep.start + int64(f*float64(ep.end-ep.start))
				n := sort.Search(len(events), func(i int) bool { return events[i].At > at })
				p := agent.Fold(events[:n])
				if s := p.Summary(); s.Activity != agent.ActivityBusy {
					continue
				}
				interjected := false
				for _, i := range ep.interjections {
					interjected = interjected || (i > at && i < ep.end)
				}
				out = append(out, evalSample{
					Chat: chat, Harness: harness, At: at, Start: ep.start, End: ep.end, Frac: f,
					Elapsed:     float64(ep.working(ep.start, at)) / 60000,
					Truth:       float64(ep.working(at, ep.end)) / 60000,
					Interjected: interjected,
					events:      events[:n], created: events[0].At,
				})
			}
		}
		t.Logf("%s: %s, %d events, %d episodes, %d long enough", chat, harness, len(events), len(episodes), used)
	}
	return out
}

// evalVariant builds the messages of one way of asking, and reads the
// model's own estimate and the one the page would show out of the answer.
type evalVariant struct {
	ask  func(s evalSample, p *agent.Projection, now time.Time) []openrouter.Message
	read func(raw []byte, c chatClock) (model, shown *float64)
}

func evalVariants() map[string]evalVariant {
	readNew := func(raw []byte, c chatClock) (*float64, *float64) {
		var a chatAnalysis
		_ = json.Unmarshal(raw, &a)
		if eta, ok := statusETA(a, c); ok {
			return a.ETAMinutes, &eta.minutes
		}
		return a.ETAMinutes, nil
	}
	newVariant := func(lim reportLimits) evalVariant {
		return evalVariant{
			ask: func(s evalSample, p *agent.Projection, now time.Time) []openrouter.Message {
				report := readChatReportWith(p, now, lim)
				return []openrouter.Message{
					{Role: "system", Content: statusSystemPrompt("en")},
					{Role: "user", Content: statusUserPrompt(s.Harness, report, s.created, now, "")},
				}
			},
			read: readNew,
		}
	}
	small := reportLimits{Max: 160000, Message: 4000, Text: 2000, Tool: 240, Children: 12}
	return map[string]evalVariant{
		// new is the Status as it is, and small the same with the record
		// cut to the limits it had before.
		"new":   newVariant(defaultReportLimits),
		"small": newVariant(small),
		// old is the Status as it was before this rework: the current
		// turn's estimate, from the record with the old limits.
		"old": {
			ask: func(s evalSample, p *agent.Projection, now time.Time) []openrouter.Message {
				report := readChatReport(p, now)
				return []openrouter.Message{
					{Role: "system", Content: oldStatusSystemPrompt("en")},
					{Role: "user", Content: "A chat with " + chatLabel(s.Harness) + ", a coding agent.\n\nFacts:\n" +
						clockFacts(report.Clock, s.created, now) + "\nRecord:\n" + report.Record},
				}
			},
			read: func(raw []byte, _ chatClock) (*float64, *float64) {
				var a struct {
					ETAMinutes *float64 `json:"eta_minutes"`
				}
				_ = json.Unmarshal(raw, &a)
				return a.ETAMinutes, a.ETAMinutes
			},
		},
	}
}

// TestStatusLive asks the Status of one journal as it stands now, the way
// the server would, and shows what the shown estimate is made of:
//
//	STATUS_EVAL_LIVE=/path/events.jsonl STATUS_EVAL_KEY=sk-or-... \
//	go test -tags statuseval -run TestStatusLive -v ./internal/server
func TestStatusLive(t *testing.T) {
	path := os.Getenv("STATUS_EVAL_LIVE")
	if path == "" {
		t.Skip("STATUS_EVAL_LIVE is not set")
	}
	events, err := agent.ReadJournal(path)
	if err != nil || len(events) == 0 {
		t.Fatalf("%s: %v", path, err)
	}
	now := time.Now()
	report := readChatReport(agent.Fold(events), now)
	user := statusUserPrompt("claude", report, events[0].At, now, "")
	t.Logf("facts:\n%s\n(record %d runes)", clockFacts(report.Clock, events[0].At, now), len([]rune(report.Record)))
	key := os.Getenv("STATUS_EVAL_KEY")
	if key == "" {
		return
	}
	model := os.Getenv("STATUS_EVAL_MODELS")
	if model == "" {
		model = "anthropic/claude-haiku-5.5"
	}
	var a chatAnalysis
	if err := askJSON(context.Background(), openrouter.New("", key), model, []openrouter.Message{
		{Role: "system", Content: statusSystemPrompt("en")}, {Role: "user", Content: user},
	}, statusMaxTokens, 0.2, &a); err != nil {
		t.Fatal(err)
	}
	b, _ := json.MarshalIndent(a, "", "  ")
	eta, ok := statusETA(a, report.Clock)
	t.Logf("answer:\n%s\nworking %.1f min; shown %+v %v", b, float64(report.Clock.Work.Working(report.Clock.Now))/60000, eta, ok)
}

func TestStatusEval(t *testing.T) {
	dir := os.Getenv("STATUS_EVAL_JOURNALS")
	if dir == "" {
		t.Skip("STATUS_EVAL_JOURNALS is not set")
	}
	samples := evalSamples(t, dir, []float64{0.05, 0.2, 0.35, 0.5, 0.65, 0.8, 0.95}, time.Minute)
	clean := samples[:0:0]
	for _, s := range samples {
		if !s.Interjected {
			clean = append(clean, s)
		}
	}
	t.Logf("%d samples, %d without the person saying more in between", len(samples), len(clean))
	if os.Getenv("STATUS_EVAL_INTERJECTED") == "" {
		samples = clean
	}
	// A fixed shuffle, so that a limit takes the same samples every run.
	rand.New(rand.NewSource(1)).Shuffle(len(samples), func(i, j int) { samples[i], samples[j] = samples[j], samples[i] })
	if n, _ := strconv.Atoi(os.Getenv("STATUS_EVAL_LIMIT")); n > 0 && n < len(samples) {
		samples = samples[:n]
	}
	// A results file from an earlier run is scored again with the code as
	// it is now, from the answers it holds: no model is asked.
	if from := os.Getenv("STATUS_EVAL_RESCORE"); from != "" {
		evalRescore(t, samples, from, os.Getenv("STATUS_EVAL_OUT"))
		return
	}
	if n, _ := strconv.Atoi(os.Getenv("STATUS_EVAL_DUMP")); n > 0 {
		for _, s := range samples[:min(n, len(samples))] {
			now := time.UnixMilli(s.At)
			report := readChatReport(agent.Fold(s.events), now)
			t.Logf("---- %s truth %.1f elapsed %.1f\n%s\n(record %d runes)", s.Chat[:8], s.Truth, s.Elapsed,
				clockFacts(report.Clock, s.created, now), len([]rune(report.Record)))
		}
		return
	}
	out := os.Getenv("STATUS_EVAL_OUT")
	if out == "" {
		for _, s := range samples {
			t.Logf("%s %s frac %.1f elapsed %.1f truth %.1f", s.Chat[:8], time.UnixMilli(s.At).Format("01-02 15:04"),
				s.Frac, s.Elapsed, s.Truth)
		}
		return
	}
	key := os.Getenv("STATUS_EVAL_KEY")
	models := strings.Split(os.Getenv("STATUS_EVAL_MODELS"), ",")
	variants := evalVariants()
	f, err := os.OpenFile(out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var mu sync.Mutex
	jobs := make(chan func())
	var wg sync.WaitGroup
	workers, _ := strconv.Atoi(os.Getenv("STATUS_EVAL_WORKERS"))
	for range max(workers, 6) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				job()
			}
		}()
	}
	client := openrouter.New("", key)
	for _, name := range strings.Split(os.Getenv("STATUS_EVAL_VARIANTS"), ",") {
		v, ok := variants[name]
		if !ok {
			t.Fatalf("no variant %q", name)
		}
		for _, model := range models {
			for _, s := range samples {
				jobs <- func() {
					now := time.UnixMilli(s.At)
					p := agent.Fold(s.events)
					msgs := v.ask(s, p, now)
					clock := readChatReport(p, now).Clock
					r := evalResult{evalSample: s, Variant: name, Model: model,
						WorkMin: float64(clock.Work.Working(s.At)) / 60000}
					for _, m := range msgs {
						if s, ok := m.Content.(string); ok {
							r.Runes += len([]rune(s))
						}
					}
					begin := time.Now()
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
					var raw json.RawMessage
					err := askJSON(ctx, client, model, msgs, 4000, 0.2, &raw)
					cancel()
					r.Secs = time.Since(begin).Seconds()
					if err != nil {
						r.Err = err.Error()
					} else {
						r.Answer = raw
						r.Pred, r.Shown = v.read(raw, clock)
					}
					line, _ := json.Marshal(r)
					mu.Lock()
					f.Write(append(line, '\n'))
					mu.Unlock()
					pred := "nil"
					if r.Pred != nil {
						pred = fmt.Sprintf("%.1f", *r.Pred)
					}
					t.Logf("%s %s %s truth %.1f pred %s %s", name, model, s.Chat[:8], s.Truth, pred, r.Err)
				}
			}
		}
	}
	close(jobs)
	wg.Wait()
	evalSummary(t, out)
}

// evalRescore reads the answers of an earlier run and writes them out again
// with the estimate the page would show now.
func evalRescore(t *testing.T, samples []evalSample, from, out string) {
	raw, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	type at struct {
		chat string
		at   int64
	}
	byAt := map[at]evalSample{}
	for _, s := range samples {
		byAt[at{s.Chat, s.At}] = s
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var r evalResult
		if json.Unmarshal([]byte(line), &r) != nil || len(r.Answer) == 0 {
			continue
		}
		s, ok := byAt[at{r.Chat, r.At}]
		if !ok {
			continue
		}
		clock := readChatReport(agent.Fold(s.events), time.UnixMilli(s.At)).Clock
		r.WorkMin = float64(clock.Work.Working(s.At)) / 60000
		if v, ok := evalVariants()[r.Variant]; ok {
			r.Pred, r.Shown = v.read(r.Answer, clock)
		} else {
			var a chatAnalysis
			_ = json.Unmarshal(r.Answer, &a)
			r.Shown = nil
			if eta, ok := statusETA(a, clock); ok {
				r.Shown = &eta.minutes
			}
		}
		b, _ := json.Marshal(r)
		lines = append(lines, string(b))
	}
	if err := os.WriteFile(out, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	evalSummary(t, out)
}

// evalSummary scores every variant and model in a results file: the error
// of an estimate is |ln(estimate/truth)|, both counted as at least a minute,
// and a missing estimate counts as none (a minute) while the work went on.
func evalSummary(t *testing.T, path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	type key struct{ variant, model, estimate string }
	errs := map[key][]float64{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var r evalResult
		if json.Unmarshal([]byte(line), &r) != nil {
			continue
		}
		for name, v := range map[string]*float64{"model": r.Pred, "shown": r.Shown, "elapsed": &r.WorkMin} {
			pred := 0.0
			if v != nil {
				pred = *v
			}
			errs[key{r.Variant, r.Model, name}] = append(errs[key{r.Variant, r.Model, name}],
				math.Abs(math.Log(max(pred, 1)/max(r.Truth, 1))))
		}
	}
	keys := make([]key, 0, len(errs))
	for k := range errs {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return fmt.Sprint(keys[i]) < fmt.Sprint(keys[j])
	})
	for _, k := range keys {
		e := errs[k]
		sort.Float64s(e)
		sum, within := 0.0, 0
		for _, x := range e {
			sum += x
			if x <= math.Ln2 {
				within++
			}
		}
		t.Logf("%-6s %-32s %-8s n=%3d median %.2f mean %.2f within 2x %3.0f%%", k.variant, k.model, k.estimate,
			len(e), e[len(e)/2], sum/float64(len(e)), 100*float64(within)/float64(len(e)))
	}
}

// oldStatusSystemPrompt is the instruction the Status had before it was
// asked for the whole of the work.
func oldStatusSystemPrompt(language string) string {
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
		"Write every text in English. Never contradict the measured facts."
}
