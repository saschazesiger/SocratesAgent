package server

// A chat's record: the whole conversation as the Status analysis reads it -
// every message, call and question with the time it happened, every turn with
// how long it took - and the facts about time that are measured rather than
// guessed. The journal knows exactly when everything happened, so how long a
// session has run is computed here and never asked of a model; what is asked
// is what the timeline means, and how long the rest will probably take.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// What a chat's record may hold. The analysis model reads a million tokens;
// these keep one Status press to cents, and a long session to its start and
// its recent hours.
const (
	reportMaxRunes     = 160000
	reportMessageRunes = 4000
	reportTextRunes    = 2000
	reportToolRunes    = 240
	reportChildLines   = 12
)

// chatClock is what is measured about a chat's time, in milliseconds since
// the epoch (0 for never) and in milliseconds of duration.
type chatClock struct {
	Now          int64
	FirstMessage int64
	// TurnStarted is when the running turn started, 0 between turns.
	TurnStarted int64
	// CallStarted is when the newest running call of that turn started.
	CallStarted int64
	CallLine    string
	// LastEnded is when the last ended turn ended, and LastOutcome how.
	LastEnded   int64
	LastOutcome string
	// AskedAt is when the open question the chat waits on was asked.
	AskedAt int64
	// Turns is how many turns the chat has had, and Worked how long they
	// ran together.
	Turns  int
	Worked int64
	Tasks  []chatTask
	State  termux.State
}

// chatReport is a chat's record, and its clock.
type chatReport struct {
	Clock  chatClock
	Record string
}

// reportEntry is one line of the timeline at the moment it happened.
type reportEntry struct {
	at   int64
	text string
	// keep is the person's first message, which a record cut for length
	// keeps.
	keep bool
	// rank orders the lines of one moment: a turn's start before what it
	// did, its end after.
	rank int
}

// turnSpan is one turn as the journal shows it: the exact bounds of the
// running and the last turn, and for an older turn, its first and last
// event.
type turnSpan struct {
	id         string
	start, end int64
	outcome    string
	running    bool
}

// readChatReport is the record of a chat at now.
func readChatReport(p *agent.Projection, now time.Time) chatReport {
	ms := now.UnixMilli()
	clock := chatClock{Now: ms, State: termux.State(p.Summary().Activity), Tasks: todoState(p)}
	var entries []reportEntry
	spans := map[string]*turnSpan{}
	var order []string
	span := func(id string) *turnSpan {
		if s := spans[id]; s != nil {
			return s
		}
		s := &turnSpan{id: id}
		spans[id] = s
		order = append(order, id)
		return s
	}

	firstUser := true
	for _, m := range p.Messages {
		switch m.Role {
		case agent.RoleUser:
			if m.State != "" && m.State != agent.MessageDelivered {
				continue
			}
			if clock.FirstMessage == 0 || m.At < clock.FirstMessage {
				clock.FirstMessage = m.At
			}
			who := "Person"
			if m.AnswerTo != "" {
				who = "Person (answering a question)"
			}
			entries = append(entries, reportEntry{at: m.At, keep: firstUser,
				text: who + ": " + clipRunes(messageWords(m), reportMessageRunes)})
			firstUser = false
		case agent.RoleAssistant:
			if m.TurnID == "" {
				continue
			}
			s := span(m.TurnID)
			if s.start == 0 || m.At < s.start {
				s.start = m.At
			}
			for _, part := range m.Parts {
				if part.UpdatedAt > s.end {
					s.end = part.UpdatedAt
				}
				if line := reportPart(part, ms); line != "" {
					entries = append(entries, reportEntry{at: part.At, text: line})
				}
			}
		}
	}
	for _, a := range p.Asks {
		q := askWords(a)
		if q == "" {
			continue
		}
		entries = append(entries, reportEntry{at: a.At, text: "Agent asked the person: " + q})
		if a.Answered && a.AnsweredAt > 0 {
			entries = append(entries, reportEntry{at: a.AnsweredAt, text: "The person answered: " +
				clipRunes(string(a.Answer), reportTextRunes)})
		}
	}
	if a := waitingAsk(p); a != nil {
		clock.AskedAt = a.At
	}

	// The exact bounds of the two turns the projection keeps.
	if t := p.LastTurn; t != nil {
		s := span(t.ID)
		s.start, s.end, s.outcome = t.StartedAt, t.EndedAt, t.Outcome
		clock.LastEnded, clock.LastOutcome = t.EndedAt, t.Outcome
	}
	if t := p.Turn; t != nil {
		s := span(t.ID)
		s.start, s.end, s.running = t.StartedAt, ms, true
		clock.TurnStarted = t.StartedAt
		if tool := runningTool(p, t.ID); tool != nil {
			clock.CallStarted, clock.CallLine = tool.At, oneLine(toolLine(tool), reportToolRunes)
		}
	}
	for _, id := range order {
		s := spans[id]
		if s.start == 0 {
			continue
		}
		clock.Turns++
		if s.end > s.start {
			clock.Worked += s.end - s.start
		}
		entries = append(entries, reportEntry{at: s.start, rank: -1,
			text: "--- Turn " + strconv.Itoa(clock.Turns) + " started"})
		switch {
		case s.running:
			// Its end is now, and the facts say how long it has run.
		case s.end > 0:
			how := ""
			if s.outcome != "" {
				how = ", " + s.outcome
			}
			entries = append(entries, reportEntry{at: s.end, rank: 1, text: "--- Turn " + strconv.Itoa(clock.Turns) +
				" ended after " + span2words(s.end-s.start) + how})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].at != entries[j].at {
			return entries[i].at < entries[j].at
		}
		return entries[i].rank < entries[j].rank
	})
	return chatReport{Clock: clock, Record: renderReport(entries, now)}
}

// reportPart is one part of an answer in the record, or "" for one that says
// nothing a reader needs: thinking, a task list (the facts carry its state)
// and an empty text.
func reportPart(part *agent.Part, now int64) string {
	switch part.Kind {
	case agent.PartText:
		if text := strings.TrimSpace(part.Text); text != "" {
			return "Agent: " + clipRunes(text, reportTextRunes)
		}
	case agent.PartTool, agent.PartDiff:
		line := "Call: " + oneLine(toolOutcome(part), reportToolRunes)
		switch {
		case part.State == agent.PartRunning:
			line += " - running for " + span2words(now-part.At)
		case part.UpdatedAt > part.At:
			line += " - took " + span2words(part.UpdatedAt-part.At)
		}
		if inside := childLines(part.Children, now); inside != "" {
			line += "\n" + inside
		}
		return line
	case agent.PartPlan:
		return "Agent proposed a plan: " + clipRunes(strings.TrimSpace(part.Text), reportTextRunes)
	case agent.PartNotice:
		if text := strings.TrimSpace(part.Text); text != "" {
			return "Notice: " + oneLine(text, reportToolRunes)
		}
	}
	return ""
}

// childLines is what a subagent or a workflow did inside its call: how many
// calls, and the newest of them.
func childLines(children []*agent.Part, now int64) string {
	var lines []string
	calls := 0
	for _, c := range children {
		if c.Kind != agent.PartTool && c.Kind != agent.PartDiff {
			continue
		}
		calls++
		line := "    - " + oneLine(toolOutcome(c), reportToolRunes)
		if c.State == agent.PartRunning {
			line += " - running for " + span2words(now-c.At)
		}
		lines = append(lines, line)
	}
	if calls == 0 {
		return ""
	}
	if len(lines) > reportChildLines {
		lines = append([]string{"    (" + strconv.Itoa(calls-reportChildLines) + " earlier calls inside)"},
			lines[len(lines)-reportChildLines:]...)
	}
	return "    inside it, " + strconv.Itoa(calls) + " calls; the newest:\n" + strings.Join(lines, "\n")
}

// askWords is a question the agent asked, in one line per question.
func askWords(a *agent.Ask) string {
	q := chatQuestion(a)
	if q == nil {
		return ""
	}
	var parts []string
	for _, item := range q.Items {
		text := item.Text
		var labels []string
		for _, o := range item.Options {
			labels = append(labels, o.Label)
		}
		if len(labels) > 0 {
			text += " (" + strings.Join(labels, ", ") + ")"
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, " / ")
}

// renderReport writes the timeline, each line with its clock time, and cuts
// it from the old end when it is too long: the first message stays, and so
// does everything recent.
func renderReport(entries []reportEntry, now time.Time) string {
	lines := make([]string, len(entries))
	total := 0
	for i, e := range entries {
		lines[i] = "[" + time.UnixMilli(e.at).In(now.Location()).Format("2006-01-02 15:04:05") + "] " + e.text
		total += len([]rune(lines[i])) + 1
	}
	if total <= reportMaxRunes {
		return strings.Join(lines, "\n")
	}
	var head []string
	start := 0
	if len(entries) > 0 && entries[0].keep {
		head = append(head, lines[0])
		total -= len([]rune(lines[0])) + 1
		start = 1
	}
	budget := reportMaxRunes - runeLen(head)
	cut := start
	for cut < len(lines) && total > budget {
		total -= len([]rune(lines[cut])) + 1
		cut++
	}
	head = append(head, "[…] "+strconv.Itoa(cut-start)+" earlier entries left out for length")
	return strings.Join(append(head, lines[cut:]...), "\n")
}

func runeLen(lines []string) int {
	n := 0
	for _, l := range lines {
		n += len([]rune(l)) + 1
	}
	return n
}

// span2words is a duration as a person says it: "40s", "12m 05s", "3h 20m",
// "2d 4h".
func span2words(ms int64) string {
	if ms < 0 {
		ms = 0
	}
	d := time.Duration(ms) * time.Millisecond
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
}

// clockFacts is the measured half of the record, as the analysis model is
// told it.
func clockFacts(c chatClock, created int64, now time.Time) string {
	at := func(ms int64) string {
		return time.UnixMilli(ms).In(now.Location()).Format("2006-01-02 15:04:05") + " (" +
			span2words(c.Now-ms) + " ago)"
	}
	var b strings.Builder
	b.WriteString("Now: " + now.Format("2006-01-02 15:04:05 MST") + "\n")
	if created > 0 {
		b.WriteString("The chat was opened: " + at(created) + "\n")
	}
	if c.FirstMessage > 0 {
		b.WriteString("Its first message: " + at(c.FirstMessage) + "\n")
	}
	b.WriteString("Turns so far: " + strconv.Itoa(c.Turns) + ", working " + span2words(c.Worked) + " in all\n")
	b.WriteString("State: " + stateWords(c.State) + "\n")
	if c.TurnStarted > 0 {
		b.WriteString("The current turn started: " + at(c.TurnStarted) + "\n")
	}
	if c.CallStarted > 0 {
		b.WriteString("Running now: " + c.CallLine + ", for " + span2words(c.Now-c.CallStarted) + "\n")
	}
	if c.AskedAt > 0 {
		b.WriteString("It has waited for the person's answer since: " + at(c.AskedAt) + "\n")
	}
	if c.TurnStarted == 0 && c.LastEnded > 0 {
		b.WriteString("The last turn ended: " + at(c.LastEnded) + suffix(", ", c.LastOutcome) + "\n")
	}
	if len(c.Tasks) > 0 {
		done := 0
		for _, t := range c.Tasks {
			if t.Status == "completed" {
				done++
			}
		}
		b.WriteString("Its task list (" + strconv.Itoa(done) + " of " + strconv.Itoa(len(c.Tasks)) + " done):\n")
		for _, t := range c.Tasks {
			mark := "[ ]"
			switch t.Status {
			case "completed":
				mark = "[x]"
			case "in_progress":
				mark = "[~]"
			}
			b.WriteString(mark + " " + t.Subject + "\n")
		}
	}
	return b.String()
}
