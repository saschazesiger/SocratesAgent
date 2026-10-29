//go:build !windows

package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// What the model schedules, and why the CLI starts a turn by itself
// (AGENT-HOST.md §5.1, "Turns of the CLI's own"). Verified against Claude
// Code 2.1.312: ScheduleWakeup's result says {scheduledFor}, CronCreate's
// {id, humanSchedule, recurring, nextFireAt}; when one fires, the stream has
// only a command_lifecycle "started" with wakeup_source ("loop_wakeup" for a
// wakeup, "schedule_wakeup" for a cron job) and a command_uuid that is the
// uuid of the transcript line holding the prompt. A background task's report
// and a Monitor's event start a turn with no such frame at all; what it was is
// in the transcript alone.

// wakeupID is the id of the one wakeup a conversation has: a new
// ScheduleWakeup replaces the one before.
const wakeupID = "wakeup"

// scheduleResult reads a call to one of the scheduling tools once its result
// is in, and says the list again when it changed.
func (s *state) scheduleResult(c *toolContent, isError bool, structured json.RawMessage) {
	if isError {
		return
	}
	var in struct {
		Prompt    string `json:"prompt"`
		Reason    string `json:"reason"`
		Stop      bool   `json:"stop"`
		Cron      string `json:"cron"`
		Recurring *bool  `json:"recurring"`
		ID        string `json:"id"`
	}
	_ = json.Unmarshal(c.Input, &in)
	var out struct {
		ScheduledFor  float64 `json:"scheduledFor"`
		ID            string  `json:"id"`
		HumanSchedule string  `json:"humanSchedule"`
		Recurring     *bool   `json:"recurring"`
		NextFireAt    float64 `json:"nextFireAt"`
	}
	_ = json.Unmarshal(structured, &out)
	switch c.Tool {
	case "ScheduleWakeup":
		if in.Stop || out.ScheduledFor <= 0 {
			s.unschedule(wakeupID)
			return
		}
		s.schedule(agent.Schedule{ID: wakeupID, Kind: agent.ScheduleWakeup, At: int64(out.ScheduledFor),
			Prompt: in.Prompt, Reason: in.Reason})
	case "CronCreate":
		if out.ID == "" {
			return
		}
		recurring := true
		if out.Recurring != nil {
			recurring = *out.Recurring
		} else if in.Recurring != nil {
			recurring = *in.Recurring
		}
		s.schedule(agent.Schedule{ID: out.ID, Kind: agent.ScheduleCron, At: int64(math.Round(out.NextFireAt)),
			Cron: pick(out.HumanSchedule, in.Cron), Recurring: recurring, Prompt: in.Prompt})
	case "CronDelete":
		s.unschedule(in.ID)
	}
}

func (s *state) schedule(sc agent.Schedule) {
	for i := range s.schedules {
		if s.schedules[i].ID == sc.ID {
			s.schedules[i] = sc
			s.emitSchedules()
			return
		}
	}
	s.schedules = append(s.schedules, sc)
	s.emitSchedules()
}

func (s *state) unschedule(id string) {
	for i := range s.schedules {
		if s.schedules[i].ID == id {
			s.schedules = append(s.schedules[:i], s.schedules[i+1:]...)
			s.emitSchedules()
			return
		}
	}
}

func (s *state) emitSchedules() {
	ev := agent.Event{Type: agent.TypeSchedules}
	if len(s.schedules) > 0 {
		ev.Content, _ = json.Marshal(s.schedules)
	}
	s.emit(ev)
}

// fired is a schedule the CLI took up (command_lifecycle "started" with a
// wakeup_source): it is no longer pending - a recurring job is, but not at
// the time it said - and the turn it starts opens with what it was, from
// what the call asked for. The transcript says it exactly, under the same
// uuid, once the turn has looked (openers).
func (s *state) fired(source, uuid string) {
	var sc *agent.Schedule
	if source == "loop_wakeup" {
		for i := range s.schedules {
			if s.schedules[i].ID == wakeupID {
				sc = &s.schedules[i]
			}
		}
	} else {
		sc = s.cronFor("", time.Now())
	}
	op := opener{uuid: uuid, code: agent.ScheduleCron}
	if source == "loop_wakeup" {
		op.code = agent.ScheduleWakeup
	}
	if sc != nil {
		op.detail = sc.Prompt
		s.ran(*sc)
	}
	op.text = op.words()
	s.wake = &op
}

// cronFor is the cron job that ran now with prompt (any prompt: ""): a job
// once that was due by now, the nearest first, else a recurring one; nil
// when that leaves more than one, or none.
func (s *state) cronFor(prompt string, now time.Time) *agent.Schedule {
	var due, recurring []*agent.Schedule
	for i := range s.schedules {
		sc := &s.schedules[i]
		if sc.Kind != agent.ScheduleCron || (prompt != "" && sc.Prompt != prompt) {
			continue
		}
		switch {
		case sc.Recurring:
			recurring = append(recurring, sc)
		case sc.At <= now.Add(cronSlack).UnixMilli():
			due = append(due, sc)
		}
	}
	if len(due) > 0 {
		sort.Slice(due, func(i, j int) bool { return due[i].At > due[j].At })
		return due[0]
	}
	if len(recurring) == 1 || (prompt != "" && len(recurring) > 0) {
		return recurring[0]
	}
	return nil
}

// cronSlack is how early a job once may run: the CLI moves a job at :00 or
// :30 up to 90 s earlier.
const cronSlack = 2 * time.Minute

// ran takes a schedule that fired off the list, or (a recurring job) the
// time it said off it.
func (s *state) ran(sc agent.Schedule) {
	if sc.Kind == agent.ScheduleCron && sc.Recurring {
		for i := range s.schedules {
			if s.schedules[i].ID == sc.ID && s.schedules[i].At != 0 {
				s.schedules[i].At = 0
				s.emitSchedules()
			}
		}
		return
	}
	s.unschedule(sc.ID)
}

// ------------------------------------------------------------ the openers

// opener is what a turn of the CLI's own was started for: a wakeup or a
// cron job the model scheduled, a background task reporting back, a
// Monitor's event, a message from another session. The turn opens with it
// as a notice part, so that the chat says why the agent speaks again.
type opener struct {
	uuid string
	// code is wakeup, cron, task, monitor or peer.
	code string
	// text is the line, and detail all of it (a prompt, an event, a
	// message).
	text, detail string
}

func (o opener) words() string {
	switch o.code {
	case agent.ScheduleWakeup:
		if o.detail != "" {
			return "Woke up as scheduled: " + firstLine(o.detail)
		}
		return "Woke up as scheduled."
	case agent.ScheduleCron:
		if o.detail != "" {
			return "A scheduled task ran: " + firstLine(o.detail)
		}
		return "A scheduled task ran."
	}
	return o.text
}

// openerOf is what a line of the main transcript started a turn for, if it
// is one of those.
func openerOf(l transcriptLine) (opener, bool) {
	if l.Type != "user" {
		return opener{}, false
	}
	var msg message
	if json.Unmarshal(l.Message, &msg) != nil {
		return opener{}, false
	}
	text, ok := msg.text()
	if !ok {
		return opener{}, false
	}
	kind := ""
	if l.Origin != nil {
		kind = l.Origin.Kind
	}
	switch {
	case kind == "task-notification" || (kind == "" && strings.HasPrefix(text, "<task-notification>")):
		return notificationOpener(l.UUID, text), true
	case kind == "peer":
		o := opener{uuid: l.UUID, code: "peer", detail: pick(l.Origin.Body, text)}
		o.text = "A message from another agent"
		if name := pick(l.Origin.Name, l.Origin.From); name != "" {
			o.text = "A message from the agent “" + name + "”"
		}
		if first := firstLine(o.detail); first != "" {
			o.text += ": " + first
		}
		return o, true
	case l.IsMeta && (l.TurnOrigin == "scheduled" || l.WakeupSource != "" || l.ClientPlatform == "scheduled_trigger"):
		o := opener{uuid: l.UUID, code: agent.ScheduleCron, detail: strings.TrimSpace(text)}
		if l.WakeupSource == "loop_wakeup" {
			o.code = agent.ScheduleWakeup
		}
		o.text = o.words()
		return o, true
	}
	return opener{}, false
}

// notificationOpener is a task's report in a line: its summary ("Background
// command "…" completed (exit code 0)", "Agent "…" finished") and, for a
// Monitor, the event it saw.
func notificationOpener(uuid, text string) opener {
	summary, _ := between(text, "<summary>", "</summary>")
	event, _ := between(text, "<event>", "</event>")
	result, _ := between(text, "<result>", "</result>")
	summary, event = strings.TrimSpace(summary), strings.TrimSpace(event)
	o := opener{uuid: uuid, code: "task", detail: strings.TrimSpace(pick(event, result))}
	if strings.HasPrefix(summary, "Monitor") {
		o.code = "monitor"
	}
	switch {
	case strings.HasPrefix(summary, "Monitor event: ") && event != "":
		o.text = "Monitor " + strings.TrimPrefix(summary, "Monitor event: ") + ": " + firstLine(event)
	case summary != "" && event != "":
		o.text = summary + ": " + firstLine(event)
	case summary != "":
		o.text = summary
	default:
		o.text = "A background task reported back."
	}
	return o
}

// emitOpener is an opener as the notice part its turn opens with, under
// the transcript line's uuid: the same opener said twice (from the call that
// scheduled it, then from the transcript) is one part.
func (s *state) emitOpener(o opener) {
	id := "n_" + o.uuid
	if o.uuid == "" {
		id = "n_" + randomID()
	}
	p := s.newPart(id, agent.PartNotice, "", agent.PartDone)
	extra := map[string]any{"code": o.code}
	if o.detail != "" {
		extra["detail"] = o.detail
	}
	p.text, p.extra = o.text, extra
	s.emitPart(p)
	delete(s.parts, id)
}

// tailWindow is how much of the end of a transcript the first look reads,
// before any turn of this process has ended and said where it ended.
const tailWindow = 256 << 10

// lookForOpeners reads what the transcript gained since the last look, for
// the lines the running turn of the CLI's own was started for. It looks
// until it has found some: the CLI writes them about when it starts the
// turn.
func (s *state) lookForOpeners() {
	t := s.turn
	if t == nil || !t.looking {
		return
	}
	found := false
	for _, l := range s.newLines() {
		o, ok := openerOf(l)
		if !ok {
			continue
		}
		found = true
		if o.code == agent.ScheduleCron {
			if sc := s.cronFor(o.detail, time.Now()); sc != nil {
				s.ran(*sc)
			}
		}
		s.emitOpener(o)
	}
	if found {
		t.looking = false
	}
}

// newLines is the lines of the main transcript written since the last look,
// from the last assistant line among them on: the earlier ones belong to
// turns that have answered.
func (s *state) newLines() []transcriptLine {
	if s.transcript == "" || s.transcriptOf != s.conv {
		s.transcript, s.transcriptOf, s.tail = findTranscript(s.configDir, s.conv), s.conv, -1
	}
	if s.transcript == "" {
		return nil
	}
	f, err := os.Open(s.transcript)
	if err != nil {
		return nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil
	}
	start, partial := s.tail, false
	if start < 0 || start > info.Size() {
		start, partial = max(0, info.Size()-tailWindow), info.Size() > tailWindow
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(f, info.Size()-start))
	if err != nil {
		return nil
	}
	// Only whole lines: the one being written is read next time.
	end := bytes.LastIndexByte(data, '\n') + 1
	data = data[:end]
	s.tail = start + int64(end)
	if partial {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		}
	}
	var lines []transcriptLine
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64<<10), len(data)+1)
	for sc.Scan() {
		var l transcriptLine
		if json.Unmarshal(sc.Bytes(), &l) != nil || l.IsSidechain {
			continue
		}
		if l.Type == "assistant" {
			lines = lines[:0]
			continue
		}
		lines = append(lines, l)
	}
	return lines
}

// markTail is where the transcript ends now, for the next look to start
// at: a turn has ended, and nothing written so far opens the next one.
func (s *state) markTail() {
	if s.transcript == "" || s.transcriptOf != s.conv {
		s.transcript, s.transcriptOf = findTranscript(s.configDir, s.conv), s.conv
	}
	if s.transcript == "" {
		s.tail = -1
		return
	}
	if info, err := os.Stat(s.transcript); err == nil {
		s.tail = info.Size()
	}
}
