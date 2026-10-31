package chattests

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The answers the socrates-tests tools give, as Markdown text. They are
// read by a model, so they are compact: what was recorded, what is next, and
// the files by absolute path, to open with the agent's own file tools.

// maxAnswer is the longest answer; a longer one is cut with a note.
const maxAnswer = 40 << 10

// statusOrder is the order in which a list's counts name the statuses: the
// problems first.
var statusOrder = []string{StatusFailed, StatusIssue, StatusBlocked, StatusRunning, StatusPassed, StatusSkipped, StatusDraft}

// ListText is every test in a line: its status, steps, latest run, and the
// marks (skipped, overridden, changed since the last run).
func ListText(tests []Test) string {
	if len(tests) == 0 {
		return "The chat has no tests yet. Write one with test_create: a title, the goal, and the steps, each an action and what must then be seen (expect)."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s.\n\n", plural(len(tests), "test"), Counts(tests))
	for _, t := range tests {
		b.WriteString(listLine(t))
		b.WriteByte('\n')
	}
	return cut(b.String(), "Cut: the list is longer than "+size(maxAnswer)+"; read single tests with test_read.")
}

// Counts names how many tests have each status: "2 failed, 5 passed".
func Counts(tests []Test) string {
	n := map[string]int{}
	for _, t := range tests {
		n[t.Status()]++
	}
	var parts []string
	for _, s := range statusOrder {
		if n[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n[s], s))
			delete(n, s)
		}
	}
	for s, c := range n {
		parts = append(parts, fmt.Sprintf("%d %s", c, s))
	}
	return strings.Join(parts, ", ")
}

func listLine(t Test) string {
	parts := []string{t.ID, StatusLabel(t.Status()), t.Title + " — " + plural(len(t.Steps), "step")}
	if r := t.Latest(); r == nil {
		parts = append(parts, "never run")
	} else {
		parts = append(parts, runLine(*r))
	}
	if t.Stale() {
		parts = append(parts, "changed since the last run")
	}
	if t.Skip != nil {
		parts = append(parts, fmt.Sprintf("set aside by %s: %q", who(t.Skip.By), t.Skip.Reason))
	}
	if o := t.Override; o != nil {
		parts = append(parts, "overridden by the person: "+StatusLabel(o.Status)+quoted(o.Note))
	}
	return "- " + strings.Join(parts, " · ")
}

// runLine is a run in a few words: "run 3 open, 2 of 4 judged", or "run 2
// failed (finished 10:42; step 3 fail: "...")".
func runLine(r Run) string {
	if r.IsOpen() {
		return fmt.Sprintf("run %d open, %d of %d judged", r.N, r.JudgedCount(), len(r.Steps))
	}
	s := fmt.Sprintf("run %d %s (finished %s", r.N, VerdictStatus(r.Verdict), when(*r.EndedAt))
	if r.Verdict != Pass {
		var worst []int
		for i, st := range r.Steps {
			if st.Verdict == r.Verdict {
				worst = append(worst, i+1)
			}
		}
		if len(worst) > 0 {
			st := r.Steps[worst[0]-1]
			s += fmt.Sprintf("; step %d %s: %q", worst[0], st.Verdict, clip(st.Observed, 140))
			if len(worst) > 1 {
				s += fmt.Sprintf(" and %d more", len(worst)-1)
			}
		}
	}
	return s + ")"
}

// TestText is everything about a test: its fields, goal and steps, its runs
// (newest first, or only run n) with every judgement and the evidence by
// absolute path, the override, the skip and the last events. filesDir is
// the test's folder (Store.FilesDir).
func TestText(t Test, run int, filesDir string) (string, error) {
	runs := t.Runs
	if run != 0 {
		r := t.Run(run)
		if r == nil {
			return "", fmt.Errorf("run %d of %s does not exist: %s", run, t.ID, runsHave(t))
		}
		runs = []Run{*r}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s · %s\n\n", t.ID, t.Title)
	status := "Status: " + StatusLabel(t.Status())
	if t.Override != nil {
		status += " (the person's override)"
	} else if t.Skip != nil {
		status += fmt.Sprintf(" (set aside by %s)", who(t.Skip.By))
	}
	if t.Stale() {
		status += " · changed since the last run"
	}
	b.WriteString(status + "\n")
	if t.Environment != "" {
		b.WriteString("Environment: " + t.Environment + "\n")
	}
	if len(t.Tags) > 0 {
		b.WriteString("Tags: " + strings.Join(t.Tags, ", ") + "\n")
	}
	fmt.Fprintf(&b, "Created %s · updated %s · %s\n", when(t.CreatedAt), when(t.UpdatedAt), runsHave(t))
	if o := t.Override; o != nil {
		fmt.Fprintf(&b, "Override by the person, %s: %s%s. Take it as the final word.\n", when(o.At), StatusLabel(o.Status), quoted(o.Note))
	}
	if sk := t.Skip; sk != nil {
		fmt.Fprintf(&b, "Set aside by %s, %s: %s\n", who(sk.By), when(sk.At), sk.Reason)
	}
	if t.Goal != "" {
		b.WriteString("\n## Goal\n\n" + t.Goal + "\n")
	}
	b.WriteString("\n## Steps\n\n")
	writeSteps(&b, t.Steps)
	for i := len(runs) - 1; i >= 0; i-- {
		writeRun(&b, t, runs[i], filesDir)
	}
	if len(t.Events) > 0 {
		events := t.Events
		if len(events) > 20 {
			events = events[len(events)-20:]
		}
		b.WriteString("\n## Events\n\n")
		for _, e := range events {
			fmt.Fprintf(&b, "- %s, %s: %s", when(e.At), who(e.By), e.What)
			if e.Detail != "" {
				b.WriteString(" — " + e.Detail)
			}
			b.WriteByte('\n')
		}
	}
	note := "Cut: the answer is longer than " + size(maxAnswer) + "; read one run with test_read {\"test\": \"" + t.ID + "\", \"run\": n}."
	return cut(b.String(), note), nil
}

func writeSteps(b *strings.Builder, steps []Step) {
	for i, st := range steps {
		fmt.Fprintf(b, "%d. %s\n   Expect: %s\n", i+1, indent(st.Action), indent(st.Expect))
	}
}

func writeRun(b *strings.Builder, t Test, r Run, filesDir string) {
	head := fmt.Sprintf("\n## Run %d · ", r.N)
	if r.IsOpen() {
		head += fmt.Sprintf("open, %d of %d judged · started %s", r.JudgedCount(), len(r.Steps), when(r.StartedAt))
	} else {
		head += fmt.Sprintf("%s · %s to %s", r.Verdict, when(r.StartedAt), when(*r.EndedAt))
	}
	if r.Environment != "" {
		head += " · " + r.Environment
	}
	b.WriteString(head + "\n\n")
	if r.Summary != "" {
		b.WriteString("Summary: " + indent(r.Summary) + "\n\n")
	}
	for i, st := range r.Steps {
		verdict := st.Verdict
		if verdict == "" {
			verdict = "not judged"
		}
		fmt.Fprintf(b, "%d. %s · %s\n", i+1, verdict, indent(st.Action))
		// The expectation is in the steps above, unless the test was
		// edited after this run began.
		if i >= len(t.Steps) || t.Steps[i].Expect != st.Expect || t.Steps[i].Action != st.Action {
			b.WriteString("   Expect: " + indent(st.Expect) + "\n")
		}
		if st.Observed != "" {
			b.WriteString("   Observed: " + indent(st.Observed) + "\n")
		}
		if st.Note != "" {
			b.WriteString("   Note: " + indent(st.Note) + "\n")
		}
		writeEvidence(b, st.Evidence, filesDir, "   ")
	}
	if len(r.Evidence) > 0 {
		b.WriteString("\nThe run's evidence:\n")
		writeEvidence(b, r.Evidence, filesDir, "")
	}
	if r.Findings != "" {
		b.WriteString("\n### Findings\n\n" + r.Findings + "\n")
	}
}

func writeEvidence(b *strings.Builder, list []Evidence, filesDir, pad string) {
	for _, e := range list {
		b.WriteString(pad + "- " + evidenceLine(e, filesDir) + "\n")
	}
}

// evidenceLine is a file with its caption: "/abs/r2/03-home.png — The
// home page (image, 120 KB)".
func evidenceLine(e Evidence, filesDir string) string {
	s := filepath.Join(filesDir, filepath.FromSlash(e.File))
	if e.Caption != "" {
		s += " — " + e.Caption
	}
	about := e.Kind + ", " + size(e.Size)
	if e.Source != "" && e.Source != string(ByAgent) {
		about += ", from " + e.Source
	}
	return s + " (" + about + ")"
}

// StartText answers a run that began: the steps to work through and how to
// record them.
func StartText(t Test, r Run) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Run %d of %s %q started", r.N, t.ID, t.Title)
	if r.Environment != "" {
		b.WriteString(" · " + r.Environment)
	}
	b.WriteString(".\n")
	if len(t.Runs) > 1 {
		if prev := t.Runs[len(t.Runs)-2]; prev.Summary == supersededSummary(r.N) {
			fmt.Fprintf(&b, "Run %d was still open: it is finished now, its steps not judged blocked; its verdict: %s.\n", prev.N, prev.Verdict)
		}
	}
	for _, e := range t.Events {
		if e.What == EventOverrideRemoved && strings.HasSuffix(e.Detail, overrideGone(r.N)) {
			fmt.Fprintf(&b, "The person's override (%s) was on an earlier run and no longer applies.\n", strings.TrimSuffix(e.Detail, overrideGone(r.N)))
		}
	}
	if t.Goal != "" {
		b.WriteString("\nGoal:\n" + t.Goal + "\n")
	}
	b.WriteString("\nSteps:\n")
	steps := make([]Step, len(r.Steps))
	for i, st := range r.Steps {
		steps[i] = Step{Action: st.Action, Expect: st.Expect}
	}
	writeSteps(&b, steps)
	fmt.Fprintf(&b, "\nJudge each step with test_step as you go (the verdict, what you observed, a screenshot of anything visible); then end the run with test_finish {\"test\": %q, \"summary\": ...}.", t.ID)
	return cut(b.String(), "Cut: read the test in full with test_read.")
}

// JudgedText confirms a step's judgement: what was recorded, the files
// added, and which steps remain.
func JudgedText(t Test, step int, added []Evidence, filesDir string) string {
	r := t.Latest()
	if r == nil || step < 1 || step > len(r.Steps) {
		return fmt.Sprintf("Step %d of %s recorded.", step, t.ID)
	}
	st := r.Steps[step-1]
	var b strings.Builder
	fmt.Fprintf(&b, "Step %d of run %d of %s: %s, recorded.\n", step, r.N, t.ID, st.Verdict)
	writeAdded(&b, added, filesDir)
	if len(st.Evidence) == 0 && st.Verdict != NA {
		b.WriteString("No file shows this step: if it is something visible, attach a screenshot with test_attach.\n")
	}
	b.WriteString(remaining(t, *r))
	return cut(b.String(), "")
}

// AttachText confirms files attached to a step or (step 0) to the run.
func AttachText(t Test, step int, added []Evidence, filesDir string) string {
	r := t.Latest()
	var b strings.Builder
	n := 0
	if r != nil {
		n = r.N
	}
	if step == 0 {
		fmt.Fprintf(&b, "Attached to run %d of %s:\n", n, t.ID)
	} else {
		fmt.Fprintf(&b, "Attached to step %d of run %d of %s:\n", step, n, t.ID)
	}
	writeAdded(&b, added, filesDir)
	return cut(b.String(), "")
}

func writeAdded(b *strings.Builder, added []Evidence, filesDir string) {
	if len(added) == 0 {
		return
	}
	b.WriteString("Attached:\n")
	writeEvidence(b, added, filesDir, "")
}

// remaining says which steps of an open run are not judged yet.
func remaining(t Test, r Run) string {
	var left []string
	for i, st := range r.Steps {
		if st.Verdict == "" {
			left = append(left, strconv.Itoa(i+1))
		}
	}
	if len(left) == 0 {
		return fmt.Sprintf("Every step is judged: end the run with test_finish {\"test\": %q, \"summary\": ...}.", t.ID)
	}
	word := "Step "
	if len(left) > 1 {
		word = "Steps "
	}
	return word + strings.Join(left, ", ") + " not judged yet; then test_finish."
}

// FinishText confirms a finished run: its verdict, the counts, the test's
// status and the steps not reached.
func FinishText(t Test, r Run) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Run %d of %s finished: %s (%s). %s is now %s.\n", r.N, t.ID, r.Verdict, countText(r.Steps), t.ID, StatusLabel(t.Status()))
	var missed []string
	for i, st := range r.Steps {
		if st.Verdict == Blocked && st.Observed == "Not reached" {
			missed = append(missed, strconv.Itoa(i+1))
		}
	}
	if len(missed) > 0 {
		fmt.Fprintf(&b, "Not reached, so blocked: %s %s.\n", pick(len(missed) == 1, "step", "steps"), strings.Join(missed, ", "))
	}
	if t.Skip != nil {
		fmt.Fprintf(&b, "The test is set aside by %s (%q), so it shows as skipped.\n", who(t.Skip.By), t.Skip.Reason)
	}
	return b.String()
}

// CreatedText confirms a new test.
func CreatedText(t Test) string {
	return fmt.Sprintf("Created %s %q with %s (draft). Run it with test_start {\"test\": %q}.", t.ID, t.Title, plural(len(t.Steps), "step"), t.ID)
}

// UpdatedText confirms an edit.
func UpdatedText(t Test) string {
	s := fmt.Sprintf("%s %q: %s, %s.", t.ID, t.Title, StatusLabel(t.Status()), plural(len(t.Steps), "step"))
	if t.Stale() {
		s += " The steps changed since the last run."
	}
	if t.Skip != nil {
		s += fmt.Sprintf(" Set aside: %q.", t.Skip.Reason)
	}
	return s
}

// runsHave names a test's runs: "runs 1–3", "run 2", "no runs yet".
func runsHave(t Test) string {
	switch len(t.Runs) {
	case 0:
		return "no runs yet"
	case 1:
		return "run " + strconv.Itoa(t.Runs[0].N)
	}
	return fmt.Sprintf("runs %d–%d", t.Runs[0].N, t.Runs[len(t.Runs)-1].N)
}

// cut keeps an answer under maxAnswer, at a line's end, with a note.
func cut(s, note string) string {
	if len(s) <= maxAnswer {
		return s
	}
	keep := s[:maxAnswer]
	if i := strings.LastIndexByte(keep, '\n'); i > 0 {
		keep = keep[:i+1]
	}
	for !utf8.ValidString(keep) {
		keep = keep[:len(keep)-1]
	}
	if note == "" {
		note = "Cut: the answer is longer than " + size(maxAnswer) + "."
	}
	return keep + "\n… " + note + "\n"
}

// clip shortens a text to n characters on one line, for a list.
func clip(s string, n int) string {
	s = oneLine(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

// indent keeps a text with several lines under its list item.
func indent(s string) string { return strings.ReplaceAll(s, "\n", "\n   ") }

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

func pick(c bool, a, b string) string {
	if c {
		return a
	}
	return b
}
