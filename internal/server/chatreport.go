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

// What a chat's record may hold. The analysis model reads a million tokens,
// and the estimate is better the more of the session it sees; these keep one
// Status press to about a cent with the default model, and the longest
// session to its start and its recent hours.
type reportLimits struct {
	// Max is the whole record, Message one message of the person's, Text
	// one text of the agent's and Tool one call's line.
	Max, Message, Text, Tool int
	// Children is how many calls inside a subagent or workflow are shown.
	Children int
}

var defaultReportLimits = reportLimits{Max: 400000, Message: 12000, Text: 6000, Tool: 400, Children: 24}

// What the facts say about earlier requests.
const (
	// reportRequestRunes is how much of an earlier request's text the facts
	// quote: enough to compare it with the current one.
	reportRequestRunes = 300
	// reportEarlierWork is how many earlier requests the facts list, the
	// newest ones.
	reportEarlierWork = 12
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
	// Work is the request the agent works on now (or worked on last), and
	// Earlier the requests before it, oldest first.
	Work    workSpan
	Earlier []workSpan
	// Background is how many background tasks run, Agents how many of them
	// are subagents or workflows; Queued is how many messages wait for the
	// agent to take them up, and Schedules what it will wake itself for.
	Background, Agents, Queued, Schedules int
}

// workSpan is one request of the person's and all the work it took: the
// turns it started, the ones the agent started by itself after it (a
// background task reporting back) and the messages the person added while it
// ran, until the next request found the agent resting.
type workSpan struct {
	Request string
	// Start is when the request was sent, End when the last turn of its work
	// ended (0 while one runs), Waited how long of it the agent waited for
	// the person's answers.
	Start, End, Waited int64
	Turns, Calls       int
	// Running is whether its work goes on now: a turn of it runs, or a
	// subagent or background task the agent waits for.
	Running bool
}

// Working is how long the agent has worked on it, without its waits, until
// it ended or until now.
func (w workSpan) Working(now int64) int64 {
	end := w.End
	if w.Running || end == 0 {
		end = now
	}
	return max(end-w.Start-w.Waited, 0)
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
	return readChatReportWith(p, now, defaultReportLimits)
}

// readChatReportWith is the record of a chat at now, within lim.
func readChatReportWith(p *agent.Projection, now time.Time, lim reportLimits) chatReport {
	ms := now.UnixMilli()
	sum := p.Summary()
	clock := chatClock{Now: ms, State: termux.State(sum.Activity), Tasks: todoState(p),
		Background: sum.Tasks, Agents: sum.Agents, Queued: sum.Queued, Schedules: sum.Schedules}
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
	// The person's requests, in the order they were sent, and how many calls
	// each turn made.
	var requests []*agent.Message
	calls := map[string]int{}
	for _, m := range p.Messages {
		switch m.Role {
		case agent.RoleUser:
			if m.State == agent.MessagePending || m.State == agent.MessageQueued {
				// Sent, and not taken up yet: work that is still to come.
				entries = append(entries, reportEntry{at: m.At,
					text: "Person (sent while the agent works; it has not read this yet): " +
						clipRunes(messageWords(m), lim.Message)})
				continue
			}
			if m.State != "" && m.State != agent.MessageDelivered {
				continue
			}
			if clock.FirstMessage == 0 || m.At < clock.FirstMessage {
				clock.FirstMessage = m.At
			}
			who := "Person"
			switch messageOrigin(m) {
			case originChat:
				who = "Another chat"
			case originSocrates:
				who = "Socrates, on the person's behalf"
			default:
				if m.AnswerTo != "" {
					who = "Person (answering a question)"
				} else {
					requests = append(requests, m)
				}
			}
			entries = append(entries, reportEntry{at: m.At, keep: firstUser,
				text: who + ": " + clipRunes(messageWords(m), lim.Message)})
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
				if part.Kind == agent.PartTool || part.Kind == agent.PartDiff {
					calls[m.TurnID] += 1 + countCalls(part.Children)
				}
				if line := reportPart(part, ms, lim); line != "" {
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
				clipRunes(string(a.Answer), lim.Text)})
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
			clock.CallStarted, clock.CallLine = tool.At, oneLine(toolLine(tool), lim.Tool)
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
	// Between turns, a subagent or a background task the agent waits for is
	// still the work of the newest request.
	active := clock.State == termux.StateBusy || clock.State == termux.StateTasks
	clock.Work, clock.Earlier = workSpans(requests, spans, order, calls, p.Asks, ms, active)
	return chatReport{Clock: clock, Record: renderReport(entries, now, lim.Max)}
}

// Where a user message came from, by its client id: the person, another
// chat (send_to_chat, chattools.go) or Socrates itself telling the agent to
// go on after an account switch (agenthost switch.go). Only the person's
// messages are requests; the others are part of the work that runs.
const (
	originPerson = iota
	originChat
	originSocrates
)

func messageOrigin(m *agent.Message) int {
	switch {
	case strings.HasPrefix(m.ClientID, "chat-tools-"):
		return originChat
	case strings.HasSuffix(m.ClientID, ":continue"):
		return originSocrates
	}
	return originPerson
}

// workSpans cuts a chat into the person's requests and the work each took
// (workSpan), and answers the newest one and the ones before it.
//
// A request starts a span when the agent was resting when it was sent: no
// turn ran then. One sent while a turn ran is more of the same work. The
// projection keeps no record of background tasks between turns, so a turn
// the agent started by itself belongs to the request before it.
func workSpans(requests []*agent.Message, spans map[string]*turnSpan, order []string,
	calls map[string]int, asks []*agent.Ask, now int64, active bool) (workSpan, []workSpan) {
	busy := func(at int64) bool {
		for _, s := range spans {
			end := s.end
			if s.running {
				end = now
			}
			if s.start > 0 && s.start < at && at < end {
				return true
			}
		}
		return false
	}
	var work []workSpan
	for _, m := range requests {
		if len(work) > 0 && busy(m.At) {
			continue
		}
		work = append(work, workSpan{Request: oneLine(messageWords(m), reportRequestRunes), Start: m.At})
	}
	if len(work) == 0 {
		return workSpan{}, nil
	}
	// Every turn belongs to the last request sent before it started.
	for _, id := range order {
		s := spans[id]
		if s.start == 0 {
			continue
		}
		i := sort.Search(len(work), func(i int) bool { return work[i].Start > s.start }) - 1
		if i < 0 {
			continue
		}
		w := &work[i]
		w.Turns++
		w.Calls += calls[id]
		if s.running {
			w.Running = true
		} else if s.end > w.End {
			w.End = s.end
		}
	}
	if active {
		work[len(work)-1].Running = true
	}
	// A request whose turns left no trace ends where the next one starts,
	// not now: it would grow for as long as the chat is open.
	for i := range work[:len(work)-1] {
		if !work[i].Running && work[i].End == 0 {
			work[i].End = work[i+1].Start
		}
	}
	// The waits of each request, clipped to it - a question can stay open
	// after the turn that asked it ended - and merged where two questions
	// were open at once.
	waits := make([][][2]int64, len(work))
	for _, a := range asks {
		i := sort.Search(len(work), func(i int) bool { return work[i].Start > a.At }) - 1
		if i < 0 {
			continue
		}
		end := now
		switch {
		case a.Answered && a.AnsweredAt > 0:
			end = a.AnsweredAt
		case a.Closed && a.ClosedAt > 0:
			end = a.ClosedAt
		}
		limit := now
		if !work[i].Running && work[i].End > 0 {
			limit = work[i].End
		}
		if lo, hi := max(a.At, work[i].Start), min(end, limit); hi > lo {
			waits[i] = append(waits[i], [2]int64{lo, hi})
		}
	}
	for i, spans := range waits {
		sort.Slice(spans, func(a, b int) bool { return spans[a][0] < spans[b][0] })
		reach := int64(0)
		for _, w := range spans {
			lo := max(w[0], reach)
			if w[1] > lo {
				work[i].Waited += w[1] - lo
			}
			reach = max(reach, w[1])
		}
	}
	return work[len(work)-1], work[:len(work)-1]
}

// countCalls is how many calls a subagent or workflow made inside its call.
func countCalls(children []*agent.Part) int {
	n := 0
	for _, c := range children {
		if c.Kind == agent.PartTool || c.Kind == agent.PartDiff {
			n += 1 + countCalls(c.Children)
		}
	}
	return n
}

// reportPart is one part of an answer in the record, or "" for one that says
// nothing a reader needs: thinking, a task list (the facts carry its state)
// and an empty text.
func reportPart(part *agent.Part, now int64, lim reportLimits) string {
	switch part.Kind {
	case agent.PartText:
		if text := strings.TrimSpace(part.Text); text != "" {
			return "Agent: " + clipRunes(text, lim.Text)
		}
	case agent.PartTool, agent.PartDiff:
		line := "Call: " + oneLine(toolOutcome(part), lim.Tool)
		switch {
		case part.State == agent.PartRunning:
			line += " - running for " + span2words(now-part.At)
		case part.UpdatedAt > part.At:
			line += " - took " + span2words(part.UpdatedAt-part.At)
		}
		if inside := childLines(part.Children, now, lim); inside != "" {
			line += "\n" + inside
		}
		return line
	case agent.PartPlan:
		return "Agent proposed a plan: " + clipRunes(strings.TrimSpace(part.Text), lim.Text)
	case agent.PartNotice:
		if text := strings.TrimSpace(part.Text); text != "" {
			return "Notice: " + oneLine(text, lim.Tool)
		}
	}
	return ""
}

// childLines is what a subagent or a workflow did inside its call: how many
// calls, and the newest of them.
func childLines(children []*agent.Part, now int64, lim reportLimits) string {
	var lines []string
	calls := 0
	for _, c := range children {
		if c.Kind != agent.PartTool && c.Kind != agent.PartDiff {
			continue
		}
		calls++
		line := "    - " + oneLine(toolOutcome(c), lim.Tool)
		if c.State == agent.PartRunning {
			line += " - running for " + span2words(now-c.At)
		}
		lines = append(lines, line)
	}
	if calls == 0 {
		return ""
	}
	if len(lines) > lim.Children {
		lines = append([]string{"    (" + strconv.Itoa(calls-lim.Children) + " earlier calls inside)"},
			lines[len(lines)-lim.Children:]...)
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
func renderReport(entries []reportEntry, now time.Time, maxRunes int) string {
	lines := make([]string, len(entries))
	total := 0
	for i, e := range entries {
		lines[i] = "[" + time.UnixMilli(e.at).In(now.Location()).Format("2006-01-02 15:04:05") + "] " + e.text
		total += len([]rune(lines[i])) + 1
	}
	if total <= maxRunes {
		return strings.Join(lines, "\n")
	}
	var head []string
	start := 0
	if len(entries) > 0 && entries[0].keep {
		head = append(head, lines[0])
		total -= len([]rune(lines[0])) + 1
		start = 1
	}
	// The line that says what was left out is room the cut has to leave.
	budget := maxRunes - runeLen(head) - 64
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
	if c.Background > 0 {
		b.WriteString("Background tasks running: " + strconv.Itoa(c.Background))
		if c.Agents > 0 {
			b.WriteString(", " + strconv.Itoa(c.Agents) + " of them subagents or workflows")
		}
		b.WriteString(" - the agent picks up again when they report back\n")
	}
	if c.Queued > 0 {
		b.WriteString("Messages from the person the agent has not taken up yet: " + strconv.Itoa(c.Queued) + "\n")
	}
	if c.Schedules > 0 {
		b.WriteString("Wake-ups the agent has scheduled for itself: " + strconv.Itoa(c.Schedules) + "\n")
	}
	if w := c.Work; w.Start > 0 {
		b.WriteString("\nThe current request (the person's last message that found the agent resting):\n")
		b.WriteString("  sent " + at(w.Start) + ": " + w.Request + "\n")
		b.WriteString("  worked on it so far: " + span2words(w.Working(c.Now)))
		if w.Waited > 0 {
			b.WriteString(" (not counting " + span2words(w.Waited) + " waiting for the person's answers)")
		}
		b.WriteString(", in " + countOf(w.Turns, "turn") + " with " + countOf(w.Calls, "call") + "\n")
		if !w.Running && c.Background == 0 && c.Queued == 0 && w.End > 0 {
			b.WriteString("  its work ended: " + at(w.End) + "\n")
		}
	}
	if len(c.Earlier) > 0 {
		earlier := c.Earlier
		if len(earlier) > reportEarlierWork {
			earlier = earlier[len(earlier)-reportEarlierWork:]
		}
		b.WriteString("\nHow long the agent worked on the person's earlier requests in this chat (newest last):\n")
		for _, w := range earlier {
			b.WriteString("  - " + span2words(w.Working(c.Now)) + ", " + countOf(w.Turns, "turn") + ", " +
				countOf(w.Calls, "call") + ": " + w.Request + "\n")
		}
	}
	if len(c.Tasks) > 0 {
		done := 0
		for _, t := range c.Tasks {
			if t.Status == "completed" {
				done++
			}
		}
		b.WriteString("\nIts task list (" + strconv.Itoa(done) + " of " + strconv.Itoa(len(c.Tasks)) + " done):\n")
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

// countOf is a count with its noun: "1 turn", "3 turns".
func countOf(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}
