package chattests

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Start begins a run of a test, with a copy of its steps. A run of it still
// open is finished first, its steps not judged yet blocked ("Superseded by
// run n"). The person's override goes (their word was on the old run), kept
// in the events. Only the last KeepRuns runs stay; older ones go with their
// files. environment is where it runs, the test's own when empty; cursors
// are where each recording server's actions stood when it began.
func (s *Store) Start(chat, ref, environment string, cursors map[string]int) (Test, Run, error) {
	env := oneLine(environment)
	if n := utf8.RuneCountInString(env); n > MaxEnvironment {
		return Test{}, Run{}, fmt.Errorf("the environment is %d characters long; keep it to %d", n, MaxEnvironment)
	}
	var (
		out  Test
		run  Run
		drop []int
	)
	err := s.change(chat, func(d *doc) (bool, error) {
		i, err := find(d, ref)
		if err != nil {
			return false, err
		}
		t := &d.Tests[i]
		if t.Skip != nil {
			return false, fmt.Errorf("%s is set aside by %s (%q): bring it back with test_update {\"test\": %q, \"skip\": \"\"} before running it",
				t.ID, who(t.Skip.By), t.Skip.Reason, t.ID)
		}
		now := s.now()
		n := 1
		if last := t.Latest(); last != nil {
			n = last.N + 1
		}
		if open := t.Open(); open != nil {
			observed := fmt.Sprintf("Superseded by run %d", n)
			for j := range open.Steps {
				if open.Steps[j].Verdict == "" {
					open.Steps[j].Verdict, open.Steps[j].Observed, open.Steps[j].At = Blocked, observed, &now
				}
			}
			open.EndedAt, open.Verdict = &now, RunVerdict(open.Steps)
			if open.Summary == "" {
				open.Summary = supersededSummary(n)
			}
			addEvent(t, Event{At: now, By: ByAgent, What: EventFinished,
				Detail: fmt.Sprintf("Run %d: %s, superseded by run %d", open.N, open.Verdict, n)})
		}
		if o := t.Override; o != nil {
			addEvent(t, Event{At: now, By: ByAgent, What: EventOverrideRemoved,
				Detail: StatusLabel(o.Status) + quoted(o.Note) + overrideGone(n)})
			t.Override = nil
		}
		if env == "" {
			env = t.Environment
		}
		r := Run{N: n, StartedAt: now, Environment: env, Steps: make([]Judged, len(t.Steps))}
		for j, st := range t.Steps {
			r.Steps[j] = Judged{Action: st.Action, Expect: st.Expect}
		}
		if len(cursors) > 0 {
			r.Cursors = maps.Clone(cursors)
		}
		t.Runs = append(t.Runs, r)
		for len(t.Runs) > KeepRuns {
			drop = append(drop, t.Runs[0].N)
			t.Runs = t.Runs[1:]
		}
		detail := fmt.Sprintf("Run %d", n)
		if env != "" {
			detail += " · " + env
		}
		addEvent(t, Event{At: now, By: ByAgent, What: EventRun, Detail: detail})
		t.UpdatedAt = now
		out, run = *t, r
		return true, nil
	})
	if err != nil {
		return Test{}, Run{}, err
	}
	for _, n := range drop {
		os.RemoveAll(filepath.Join(s.FilesDir(chat, out.ID), "r"+strconv.Itoa(n)))
	}
	return out, run, nil
}

// Judgement is a step's judgement in the open run.
type Judgement struct {
	// Step counts from 1.
	Step     int
	Verdict  string
	Observed string
	Note     string
	// Files are copied into the run's folder as the step's evidence.
	Files []Attach
	// Output, when not empty, is kept as a text file: a command's output.
	Output string
	// Recorded, when set, is asked under the chat's lock, with the open
	// run's cursors, for what recording servers took since them: files to
	// keep beside Files and where the cursors stand after them. Unlike
	// Files they never fail the call: one that is gone, too large or past
	// the step's or the chat's room is left out, the newest kept first.
	Recorded func(cursors map[string]int) ([]Attach, map[string]int)
}

// Attach is a file to keep as evidence.
type Attach struct {
	// Path is the file's absolute path. It is copied, never moved.
	Path    string
	Caption string
	// Source is "agent" (the default) or the name of the server that
	// recorded the file.
	Source string
}

// Judge records a step's judgement in the open run. Judging a step again
// replaces its verdict, observation and note and keeps its evidence, adding
// the new files to it. It answers the test, the step as it now stands and
// the evidence this call added.
func (s *Store) Judge(chat, ref string, j Judgement) (Test, Judged, []Evidence, error) {
	verdict, ok := ParseVerdict(j.Verdict)
	if !ok {
		return Test{}, Judged{}, nil, fmt.Errorf("%q is not a verdict: use pass, issue, fail, blocked or na", j.Verdict)
	}
	observed, note := strings.TrimSpace(j.Observed), strings.TrimSpace(j.Note)
	switch {
	case observed == "":
		return Test{}, Judged{}, nil, errors.New("observed is empty: describe what was on screen (or what the command answered), not what should have been")
	case len(observed) > MaxObserved:
		return Test{}, Judged{}, nil, fmt.Errorf("observed is %s; keep it to %s and attach the rest as a file or as output", size(int64(len(observed))), size(MaxObserved))
	case len(note) > MaxNote:
		return Test{}, Judged{}, nil, fmt.Errorf("the note is %s; keep it to %s and put the rest in the run's findings", size(int64(len(note))), size(MaxNote))
	}
	var (
		out   Test
		step  Judged
		added []Evidence
		made  []string
	)
	err := s.change(chat, func(d *doc) (bool, error) {
		t, r, err := openRun(d, ref)
		if err != nil {
			return false, err
		}
		if err := stepExists(t, r, j.Step, false); err != nil {
			return false, err
		}
		now := s.now()
		st := &r.Steps[j.Step-1]
		var ev []Evidence
		ev, made, err = s.keep(chat, d, t, r, len(st.Evidence), j.Files, j.Output, now)
		if err != nil {
			return false, err
		}
		st.Verdict, st.Observed, st.Note, st.At = verdict, observed, note, &now
		st.Evidence = append(st.Evidence, ev...)
		if j.Recorded != nil {
			files, cursors := j.Recorded(maps.Clone(r.Cursors))
			rec, recMade := s.keepRecorded(chat, d, t, r, len(st.Evidence), files, now)
			made = append(made, recMade...)
			st.Evidence = append(st.Evidence, rec...)
			ev = append(ev, rec...)
			if len(cursors) > 0 {
				if r.Cursors == nil {
					r.Cursors = map[string]int{}
				}
				maps.Copy(r.Cursors, cursors)
			}
		}
		t.UpdatedAt = now
		out, step, added = *t, *st, ev
		return true, nil
	})
	if err != nil {
		// The files are copied before the save; if it failed, they go again.
		removeAll(made)
		return Test{}, Judged{}, nil, err
	}
	return out, step, added, nil
}

// Attach keeps files as evidence of the open run: of one of its steps, or of
// the run itself when step is 0. It answers the test and the evidence added.
func (s *Store) Attach(chat, ref string, step int, files []Attach) (Test, []Evidence, error) {
	if len(files) == 0 {
		return Test{}, nil, errors.New("no files to attach: name at least one by its path")
	}
	var (
		out   Test
		added []Evidence
		made  []string
	)
	err := s.change(chat, func(d *doc) (bool, error) {
		t, r, err := openRun(d, ref)
		if err != nil {
			return false, err
		}
		if err := stepExists(t, r, step, true); err != nil {
			return false, err
		}
		now := s.now()
		list := &r.Evidence
		if step > 0 {
			list = &r.Steps[step-1].Evidence
		}
		var ev []Evidence
		ev, made, err = s.keep(chat, d, t, r, len(*list), files, "", now)
		if err != nil {
			return false, err
		}
		*list = append(*list, ev...)
		t.UpdatedAt = now
		out, added = *t, ev
		return true, nil
	})
	if err != nil {
		removeAll(made)
		return Test{}, nil, err
	}
	return out, added, nil
}

// Finish ends the open run: the steps not judged yet are blocked ("Not
// reached"), and the run's verdict is its worst step's. summary is a
// sentence or two; findings are Markdown, free-form.
func (s *Store) Finish(chat, ref, summary, findings string) (Test, Run, error) {
	summary, findings = strings.TrimSpace(summary), strings.TrimSpace(findings)
	switch {
	case summary == "":
		return Test{}, Run{}, errors.New("the summary is empty: say in a sentence or two what the run showed")
	case len(summary) > MaxSummary:
		return Test{}, Run{}, fmt.Errorf("the summary is %s; keep it to %s and put the details in findings", size(int64(len(summary))), size(MaxSummary))
	case len(findings) > MaxFindings:
		return Test{}, Run{}, fmt.Errorf("the findings are %s; keep them to %s", size(int64(len(findings))), size(MaxFindings))
	}
	var (
		out Test
		run Run
	)
	err := s.change(chat, func(d *doc) (bool, error) {
		t, r, err := openRun(d, ref)
		if err != nil {
			return false, err
		}
		now := s.now()
		for j := range r.Steps {
			if r.Steps[j].Verdict == "" {
				r.Steps[j].Verdict, r.Steps[j].Observed, r.Steps[j].At = Blocked, "Not reached", &now
			}
		}
		r.EndedAt, r.Verdict, r.Summary, r.Findings = &now, RunVerdict(r.Steps), summary, findings
		addEvent(t, Event{At: now, By: ByAgent, What: EventFinished,
			Detail: fmt.Sprintf("Run %d: %s (%s)", r.N, r.Verdict, countText(r.Steps))})
		t.UpdatedAt = now
		out, run = *t, *r
		return true, nil
	})
	if err != nil {
		return Test{}, Run{}, err
	}
	return out, run, nil
}

// SetOverride sets the person's word on a test's status, with a note; an
// empty status removes it. Removing one there is not changes nothing.
func (s *Store) SetOverride(chat, ref, status, note string) (Test, error) {
	note = strings.TrimSpace(note)
	st := ""
	if strings.TrimSpace(status) != "" {
		var ok bool
		if st, ok = ParseOverride(status); !ok {
			return Test{}, fmt.Errorf("%q cannot be an override: use passed, issue, failed, blocked or skipped, or nothing to remove it", status)
		}
	}
	if len(note) > MaxNote {
		return Test{}, fmt.Errorf("the note is %s; keep it to %s", size(int64(len(note))), size(MaxNote))
	}
	var out Test
	err := s.change(chat, func(d *doc) (bool, error) {
		i, err := find(d, ref)
		if err != nil {
			return false, err
		}
		t := &d.Tests[i]
		now := s.now()
		if st == "" {
			if t.Override == nil {
				out = *t
				return false, nil
			}
			addEvent(t, Event{At: now, By: ByPerson, What: EventOverrideRemoved,
				Detail: "It was " + StatusLabel(t.Override.Status) + quoted(t.Override.Note)})
			t.Override = nil
		} else {
			t.Override = &Override{Status: st, Note: note, At: now}
			addEvent(t, Event{At: now, By: ByPerson, What: EventOverride, Detail: StatusLabel(st) + quoted(note)})
		}
		t.UpdatedAt = now
		out = *t
		return true, nil
	})
	return out, err
}

// supersededSummary is the summary of a run that a new run n finished.
func supersededSummary(n int) string {
	return fmt.Sprintf("Superseded by run %d before it was finished.", n)
}

// overrideGone ends the event of an override that run n removed.
func overrideGone(n int) string { return fmt.Sprintf(", removed by run %d", n) }

// openRun is the test ref names and its open run.
func openRun(d *doc, ref string) (*Test, *Run, error) {
	i, err := find(d, ref)
	if err != nil {
		return nil, nil, err
	}
	t := &d.Tests[i]
	r := t.Open()
	if r == nil {
		if last := t.Latest(); last != nil {
			return nil, nil, fmt.Errorf("%s has no open run (run %d was finished %s): call test_start first", t.ID, last.N, when(*last.EndedAt))
		}
		return nil, nil, fmt.Errorf("%s has no open run: call test_start first", t.ID)
	}
	return t, r, nil
}

// stepExists checks a step number of a run; 0 names the run itself where
// that is allowed.
func stepExists(t *Test, r *Run, step int, runToo bool) error {
	if (step == 0 && runToo) || (step >= 1 && step <= len(r.Steps)) {
		return nil
	}
	if step == 0 {
		return errors.New("no step is named: steps count from 1")
	}
	return fmt.Errorf("step %d does not exist: run %d of %s has %d steps", step, r.N, t.ID, len(r.Steps))
}

// countText counts a run's steps by verdict: "3 pass, 1 fail, 1 blocked".
func countText(steps []Judged) string {
	var parts []string
	for _, v := range []string{Pass, Issue, Fail, Blocked, NA} {
		n := 0
		for _, s := range steps {
			if s.Verdict == v {
				n++
			}
		}
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, v))
		}
	}
	if open := len(steps) - judgedOf(steps); open > 0 {
		parts = append(parts, fmt.Sprintf("%d not judged", open))
	}
	return strings.Join(parts, ", ")
}

func judgedOf(steps []Judged) int {
	n := 0
	for _, s := range steps {
		if s.Verdict != "" {
			n++
		}
	}
	return n
}

// who names who did something, as a sentence says it.
func who(by By) string {
	if by == ByPerson {
		return "the person"
	}
	return "the agent"
}

// quoted is a note after a status: ` ("note")`, or nothing.
func quoted(note string) string {
	if note == "" {
		return ""
	}
	return fmt.Sprintf(" (%q)", note)
}

// when is a moment as the answers show it, in the server's time zone: the
// time today, else the day too.
func when(at time.Time) string {
	at = at.Local()
	now := clock().Local()
	switch {
	case at.Year() == now.Year() && at.YearDay() == now.YearDay():
		return at.Format("15:04")
	case at.Year() == now.Year():
		return at.Format("Jan 2 15:04")
	}
	return at.Format("2006-01-02 15:04")
}

// clock is the time the answers compare with, replaced by the tests.
var clock = time.Now
