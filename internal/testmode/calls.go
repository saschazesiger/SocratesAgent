package testmode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/agent/testtools"
	"github.com/saschazesiger/SocratesAgent/internal/testmode/catalog"
)

// The calls of the test tools (internal/agent/testtools), as the server
// hands them over: the chat that made it, the tool and its arguments. The
// answer is a text for the model and whether it is an error.

// maxText is the longest observation, summary or note kept.
const maxText = 4000

// Call answers one call of a test tool.
func (r *Runner) Call(_ context.Context, chat, tool string, args json.RawMessage) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ref, ok := r.byChat[chat]
	rs := r.runs[ref.Run]
	if !ok || rs == nil || rs.deleting {
		return "This session belongs to no test run Socrates knows.", true
	}
	if rs.resuming {
		return "Socrates is taking the test run up again after a restart; call again in a minute.", true
	}
	if ref.Kind == testtools.KindCell && rs.unredactable() {
		return "The test run is paused: Socrates could not read its secrets again. Wait; call again later.", true
	}
	if !testtools.Has(ref.Kind, tool) {
		return "There is no tool " + tool + " in this session.", true
	}
	var text string
	var err error
	switch ref.Kind {
	case testtools.KindCell:
		a := rs.attempts[ref.ID]
		if a == nil {
			return "This cell is gone.", true
		}
		text, err = r.cellCall(rs, a, tool, args)
	case testtools.KindReviewer:
		h := rs.helper(ref.ID)
		if h == nil {
			return "This review is gone.", true
		}
		text, err = r.reviewCall(rs, h, tool, args)
	case testtools.KindAnalyst:
		h := rs.helper(ref.ID)
		if h == nil {
			return "This analysis is gone.", true
		}
		text, err = r.analystCall(rs, h, tool, args)
	}
	r.publishLocked(rs)
	if err != nil {
		return err.Error(), true
	}
	return text, false
}

func decode(args json.RawMessage, v any) error {
	if len(args) == 0 || string(args) == "null" {
		return nil
	}
	var s string
	if json.Unmarshal(args, &s) == nil {
		args = json.RawMessage(s)
	}
	if err := json.Unmarshal(args, v); err != nil {
		return fmt.Errorf("the arguments could not be read: %v", err)
	}
	return nil
}

// ------------------------------------------------------------------ cell

func (r *Runner) cellCall(rs *runState, a *Attempt, tool string, args json.RawMessage) (string, error) {
	if Final(a.State) {
		return "", fmt.Errorf("this cell is over (%s%s); end your turn", a.State, suffix(a.EndReason))
	}
	if a.State != AttemptRunning {
		return "", fmt.Errorf("this cell is not running")
	}
	dir := r.layout.EvidenceDir(a)
	if _, err := ingest(a, dir, rs.red); err != nil {
		return "", fmt.Errorf("the evidence could not be read: %v", err)
	}
	switch tool {
	case testtools.Steps:
		r.saveAttemptLocked(a)
		return stepsText(a), nil
	case testtools.Judge:
		var p testtools.JudgeArgs
		if err := decode(args, &p); err != nil {
			return "", err
		}
		return r.judgeLocked(rs, a, p)
	case testtools.End:
		var p testtools.EndArgs
		if err := decode(args, &p); err != nil {
			return "", err
		}
		if strings.TrimSpace(p.Summary) == "" {
			return "", fmt.Errorf("say in a summary what works and what does not")
		}
		var open []string
		for _, s := range a.Steps {
			if s.Verdict == "" {
				open = append(open, fmt.Sprint(s.N))
			}
		}
		if len(open) > 0 {
			return "", fmt.Errorf("steps %s are not judged yet; judge them first (rest with blocked for what was not reached)", strings.Join(open, ", "))
		}
		a.Summary = clip(rs.red.String(p.Summary), maxText)
		r.finishLocked(rs, a, AttemptEnded, "")
		return fmt.Sprintf("The cell is ended: %s. End your turn now.", a.Verdict), nil
	case testtools.Abort:
		var p testtools.AbortArgs
		if err := decode(args, &p); err != nil {
			return "", err
		}
		reason := clip(rs.red.String(p.Reason), 1000)
		if reason == "" {
			reason = "aborted"
		}
		r.finishLocked(rs, a, AttemptAborted, "not reached: "+reason)
		return "The cell is aborted. End your turn now.", nil
	}
	return "", fmt.Errorf("unknown tool %s", tool)
}

func suffix(s string) string {
	if s == "" {
		return ""
	}
	return ": " + s
}

func stepsText(a *Attempt) string {
	var b strings.Builder
	next := 0
	for _, s := range a.Steps {
		v := s.Verdict
		if v == "" {
			v = "not judged"
			if next == 0 {
				next = s.N
			}
		}
		fmt.Fprintf(&b, "%d. %s\n   Expect: %s\n   Verdict: %s", s.N, s.Do, s.Expect, v)
		if s.Observed != "" {
			b.WriteString(" - " + s.Observed)
		}
		b.WriteString("\n")
	}
	if fresh := len(a.Actions) - a.Judged; fresh > 0 {
		fmt.Fprintf(&b, "\n%d action(s) recorded since the last judgement.", fresh)
	}
	if next > 0 {
		fmt.Fprintf(&b, "\nNext: step %d.", next)
	} else {
		b.WriteString("\nEvery step is judged: end the cell with " + testtools.End + ".")
	}
	return b.String()
}

func (r *Runner) judgeLocked(rs *runState, a *Attempt, p testtools.JudgeArgs) (string, error) {
	p.Verdict = strings.TrimSpace(strings.ToLower(p.Verdict))
	if !testtools.ValidVerdict(p.Verdict) {
		return "", fmt.Errorf("the verdict must be one of %s", strings.Join(testtools.Verdicts, ", "))
	}
	observed := clip(rs.red.String(p.Observed), maxText)
	if observed == "" {
		return "", fmt.Errorf("say what you observed")
	}
	dir := r.layout.EvidenceDir(a)
	if p.Rest {
		if p.Verdict != testtools.Blocked && p.Verdict != testtools.NA {
			return "", fmt.Errorf("the rest can only be judged blocked or na; judge the other steps one by one")
		}
		n := 0
		for i := range a.Steps {
			if a.Steps[i].Verdict == "" {
				a.Steps[i].Verdict, a.Steps[i].Observed, a.Steps[i].At = p.Verdict, observed, r.now().UnixMilli()
				n++
			}
		}
		r.saveAttemptLocked(a)
		return fmt.Sprintf("%d step(s) judged %s. %s", n, p.Verdict, stepsHint(a)), nil
	}
	if p.Step < 1 || p.Step > len(a.Steps) {
		return "", fmt.Errorf("there is no step %d; the test has %d", p.Step, len(a.Steps))
	}
	s := &a.Steps[p.Step-1]
	// The actions since the previous judgement are this step's evidence.
	var fresh []int
	for i := a.Judged; i < len(a.Actions); i++ {
		fresh = append(fresh, a.Actions[i].N)
	}
	var attached []string
	for i, src := range p.Attach {
		name, err := attach(dir, src, len(s.Attach)+i+1, rs.run.Path, os.TempDir())
		if err != nil {
			return "", fmt.Errorf("could not keep %s: %v", src, err)
		}
		attached = append(attached, name)
	}
	if needsScreenshot(a.Evidence, p.Verdict) {
		has := false
		for _, n := range append(append([]int{}, s.Actions...), fresh...) {
			if a.Actions[n-1].Screenshot != "" {
				has = true
			}
		}
		for _, name := range append(append([]string{}, s.Attach...), attached...) {
			if isImage(name) {
				has = true
			}
		}
		if !has && a.Evidence == catalog.EvidenceScreenshot {
			return "", fmt.Errorf("a %s needs a screenshot among the step's evidence: take one with your tools (or attach one) and judge again", p.Verdict)
		}
		if !has && len(fresh) == 0 && len(s.Actions) == 0 && len(attached) == 0 && len(s.Attach) == 0 {
			return "", fmt.Errorf("a %s needs evidence: carry the step out with your tools (or attach a file) and judge again", p.Verdict)
		}
	}
	s.Actions = append(s.Actions, fresh...)
	s.Attach = append(s.Attach, attached...)
	s.Verdict, s.Observed, s.At = p.Verdict, observed, r.now().UnixMilli()
	a.Judged = len(a.Actions)
	r.saveAttemptLocked(a)
	return fmt.Sprintf("Step %d judged %s with %d action(s) of evidence. %s", s.N, s.Verdict, len(s.Actions), stepsHint(a)), nil
}

func needsScreenshot(evidence, verdict string) bool {
	if evidence == catalog.EvidenceNone {
		return false
	}
	return verdict == testtools.Pass || verdict == testtools.Issue || verdict == testtools.Fail
}

func stepsHint(a *Attempt) string {
	for _, s := range a.Steps {
		if s.Verdict == "" {
			return fmt.Sprintf("Next: step %d.", s.N)
		}
	}
	return "Every step is judged: end the cell with " + testtools.End + "."
}

// ---------------------------------------------------------------- review

// cellOf is the latest attempt of a cell of the run.
func cellOf(rs *runState, cellID string) *Attempt {
	cellID = strings.TrimSpace(cellID)
	for _, a := range latestAttempts(rs) {
		if a.CellID == cellID {
			return a
		}
	}
	return nil
}

func (r *Runner) reviewCall(rs *runState, h *Helper, tool string, args json.RawMessage) (string, error) {
	if h.State != "running" {
		return "", fmt.Errorf("this review is over (%s); end your turn", h.State)
	}
	if tool == testtools.ReviewList {
		var b strings.Builder
		for _, id := range h.Cells {
			a := rs.helperAttempt(h, id)
			if a == nil || a.Review == nil || a.Review.Helper != h.ID {
				continue
			}
			state := "to review"
			if a.Review.State == ReviewDone {
				state = "closed"
			}
			fmt.Fprintf(&b, "- %s (%s): %s - %s\n", id, a.Title, a.Review.Reason, state)
		}
		return b.String(), nil
	}
	var cell testtools.CellArgs
	if err := decode(args, &cell); err != nil {
		return "", err
	}
	a := rs.helperAttempt(h, cell.Cell)
	if a == nil || a.Review == nil || a.Review.Helper != h.ID {
		return "", fmt.Errorf("%s is not one of your cells (%s)", cell.Cell, testtools.ReviewList)
	}
	switch tool {
	case testtools.ReviewEvidence:
		return r.evidenceText(a, false), nil
	case testtools.ReviewAgent:
		var b strings.Builder
		fmt.Fprintf(&b, "Verdict of the cell: %s\nSummary: %s\n", a.Verdict, a.Summary)
		if a.EndReason != "" {
			fmt.Fprintf(&b, "Ended: %s\n", a.EndReason)
		}
		for _, s := range a.Steps {
			fmt.Fprintf(&b, "\n%d. %s - %s", s.N, s.Verdict, s.Observed)
		}
		return b.String(), nil
	case testtools.ReviewStep:
		var p testtools.ReviewStepArgs
		if err := decode(args, &p); err != nil {
			return "", err
		}
		p.Verdict = strings.TrimSpace(strings.ToLower(p.Verdict))
		if !testtools.ValidVerdict(p.Verdict) {
			return "", fmt.Errorf("the verdict must be one of %s", strings.Join(testtools.Verdicts, ", "))
		}
		if p.Step < 1 || p.Step > len(a.Steps) {
			return "", fmt.Errorf("there is no step %d", p.Step)
		}
		note := clip(rs.red.String(p.Note), maxText)
		if note == "" {
			return "", fmt.Errorf("say in a note what the screenshots show")
		}
		a.Steps[p.Step-1].Review = &StepReview{Verdict: p.Verdict, Note: note, At: r.now().UnixMilli()}
		a.Reviewed, _ = a.worst(true)
		if a.Reviewed == a.Verdict {
			a.Reviewed = ""
		}
		r.saveAttemptLocked(a)
		return fmt.Sprintf("Step %d of %s is now %s; the cell is %s.", p.Step, a.CellID, p.Verdict, a.Final()), nil
	case testtools.ReviewDone:
		var p testtools.ReviewDoneArgs
		if err := decode(args, &p); err != nil {
			return "", err
		}
		a.Review.State, a.Review.Note, a.Review.At = ReviewDone, clip(rs.red.String(p.Note), maxText), r.now().UnixMilli()
		r.saveAttemptLocked(a)
		// A passed cell that a review turned shows the other passes of the
		// test are not to be trusted either.
		if passed(a.Verdict) && !passed(a.Final()) {
			for _, x := range latestAttempts(rs) {
				if x.Test == a.Test && x != a && x.Review == nil && Final(x.State) && passed(x.Verdict) {
					x.Review = &Review{State: ReviewPending, Reason: "a reviewed pass of this test turned out wrong"}
					r.saveAttemptLocked(x)
				}
			}
		}
		r.retryLocked(rs, a, false)
		left := 0
		for _, id := range h.Cells {
			if x := cellOf(rs, id); x != nil && x.Review != nil && x.Review.State == ReviewRunning {
				left++
			}
		}
		if left == 0 {
			return "Closed. Every cell of this review is closed: end your turn.", nil
		}
		return fmt.Sprintf("Closed. %d cell(s) left.", left), nil
	}
	return "", fmt.Errorf("unknown tool %s", tool)
}

// evidenceText is a cell's steps and evidence with the screenshots' paths;
// withVerdicts adds the agent's and the review's verdicts.
func (r *Runner) evidenceText(a *Attempt, withVerdicts bool) string {
	dir := r.layout.EvidenceDir(a)
	var b strings.Builder
	fmt.Fprintf(&b, "Cell %s: %s\n", a.CellID, a.Title)
	for _, d := range a.Dims {
		fmt.Fprintf(&b, "%s: %s (%s)\n", d.Name, d.ID, d.Label)
	}
	fmt.Fprintf(&b, "Attempt %d. Screenshots are files under %s.\n", a.N, dir)
	used := map[int]bool{}
	for _, s := range a.Steps {
		fmt.Fprintf(&b, "\n## Step %d: %s\nExpect: %s\n", s.N, s.Do, s.Expect)
		if withVerdicts {
			fmt.Fprintf(&b, "Agent: %s - %s\n", s.Verdict, s.Observed)
			if s.Review != nil {
				fmt.Fprintf(&b, "Review: %s - %s\n", s.Review.Verdict, s.Review.Note)
			}
		}
		for _, n := range s.Actions {
			if n >= 1 && n <= len(a.Actions) {
				used[n] = true
				writeAction(&b, dir, &a.Actions[n-1])
			}
		}
		for _, name := range s.Attach {
			fmt.Fprintf(&b, "- attached: %s\n", filepath.Join(dir, name))
		}
		if len(s.Actions) == 0 && len(s.Attach) == 0 {
			b.WriteString("- no evidence\n")
		}
	}
	var rest []int
	for i := range a.Actions {
		if !used[a.Actions[i].N] {
			rest = append(rest, i)
		}
	}
	if len(rest) > 0 {
		b.WriteString("\n## Actions after the last judgement\n")
		for _, i := range rest {
			writeAction(&b, dir, &a.Actions[i])
		}
	}
	return b.String()
}

func writeAction(b *strings.Builder, dir string, act *Action) {
	fmt.Fprintf(b, "- #%d %s: %s", act.N, act.Action, act.Summary)
	if act.URL != "" {
		fmt.Fprintf(b, " (%s)", act.URL)
	}
	if act.Error != "" {
		fmt.Fprintf(b, " ERROR: %s", act.Error)
	}
	if act.Screenshot != "" {
		fmt.Fprintf(b, "\n  screenshot: %s", filepath.Join(dir, act.Screenshot))
	}
	b.WriteString("\n")
}

// --------------------------------------------------------------- analyst

func (r *Runner) analystCall(rs *runState, h *Helper, tool string, args json.RawMessage) (string, error) {
	switch tool {
	case testtools.RunOverview:
		var p testtools.OverviewArgs
		if err := decode(args, &p); err != nil {
			return "", err
		}
		var b strings.Builder
		for _, a := range latestAttempts(rs) {
			if p.Verdict != "" && a.Final() != p.Verdict || p.Area != "" && a.Area != p.Area {
				continue
			}
			fmt.Fprintf(&b, "- %s (%s, %s): %s", a.CellID, a.Title, a.Priority, a.Final())
			if a.Reviewed != "" {
				fmt.Fprintf(&b, " (agent: %s)", a.Verdict)
			}
			if a.Summary != "" {
				b.WriteString(" - " + a.Summary)
			}
			if a.EndReason != "" {
				b.WriteString(" [" + a.EndReason + "]")
			}
			b.WriteString("\n")
			for _, s := range a.Steps {
				if v := s.StepVerdict(); v != testtools.Pass && v != testtools.NA {
					fmt.Fprintf(&b, "    step %d %s: %s\n", s.N, v, s.Observed)
					if s.Review != nil {
						fmt.Fprintf(&b, "      review: %s\n", s.Review.Note)
					}
				}
			}
		}
		if b.Len() == 0 {
			return "No cell matches.", nil
		}
		return b.String(), nil
	case testtools.RunCell:
		var p testtools.CellArgs
		if err := decode(args, &p); err != nil {
			return "", err
		}
		a := cellOf(rs, p.Cell)
		if a == nil {
			return "", fmt.Errorf("no cell %s", p.Cell)
		}
		text := r.evidenceText(a, true)
		text += fmt.Sprintf("\nVerdict: %s (agent: %s)\nSummary: %s\n", a.Final(), a.Verdict, a.Summary)
		return text, nil
	case testtools.BugsKnown:
		return r.knownBugsText(rs), nil
	case testtools.BugsPut:
		var p testtools.BugsArgs
		if err := decode(args, &p); err != nil {
			return "", err
		}
		for i := range p.Bugs {
			bug := &p.Bugs[i]
			bug.ID = strings.TrimSpace(bug.ID)
			if strings.TrimSpace(bug.Title) == "" {
				return "", fmt.Errorf("bug %d needs a title", i+1)
			}
			if !slices.Contains(testtools.Severities, bug.Severity) {
				return "", fmt.Errorf("bug %d (%s): the severity must be one of %s", i+1, bug.Title, strings.Join(testtools.Severities, ", "))
			}
			bug.Title = clip(rs.red.String(bug.Title), 300)
			bug.Description = clip(rs.red.String(bug.Description), 20000)
		}
		// The ids Socrates keeps: a known bug's stays, a new bug gets one.
		if err := r.settleBugIDsLocked(rs, p.Bugs); err != nil {
			return "", err
		}
		cells := map[string]*Attempt{}
		for _, a := range latestAttempts(rs) {
			cells[a.CellID] = a
			a.Bugs = nil
		}
		var ids []string
		for i := range p.Bugs {
			bug := &p.Bugs[i]
			known := []string{}
			for _, c := range bug.Cells {
				if a := cells[strings.TrimSpace(c)]; a != nil && !slices.Contains(known, a.CellID) {
					known = append(known, a.CellID)
					a.Bugs = append(a.Bugs, bug.ID)
				}
			}
			bug.Cells = known
			ids = append(ids, bug.ID+" ("+bug.Title+")")
		}
		for _, a := range cells {
			r.saveAttemptLocked(a)
		}
		if p.Bugs == nil {
			p.Bugs = []testtools.Bug{}
		}
		rs.forgetReported()
		if err := writeJSON(r.layout.runBugsPath(rs.run.Repo, rs.run.ID), p.Bugs); err != nil {
			return "", err
		}
		if !slices.Contains(h.Wrote, "bugs") {
			h.Wrote = append(h.Wrote, "bugs")
			r.saveRunLocked(rs)
		}
		if len(ids) == 0 {
			return "An empty bug list is written.", nil
		}
		return fmt.Sprintf("%d bug(s) written, with the ids Socrates keeps: %s.", len(p.Bugs), strings.Join(ids, "; ")), nil
	case testtools.ConclusionsPut:
		var p testtools.ConclusionsArgs
		if err := decode(args, &p); err != nil {
			return "", err
		}
		md := strings.TrimSpace(rs.red.String(p.Markdown))
		if md == "" {
			return "", fmt.Errorf("the conclusions are empty")
		}
		path := filepath.Join(r.layout.RunDir(rs.run.Repo, rs.run.ID), "conclusions.md")
		if err := os.WriteFile(path, []byte(clip(md, 100000)+"\n"), 0o600); err != nil {
			return "", err
		}
		if !slices.Contains(h.Wrote, "conclusions") {
			h.Wrote = append(h.Wrote, "conclusions")
			r.saveRunLocked(rs)
		}
		return "The conclusions are written.", nil
	}
	return "", fmt.Errorf("unknown tool %s", tool)
}
