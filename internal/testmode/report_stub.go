// report_stub.go is replaced by report.go when tests-b1 is merged.

package testmode

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent/testtools"
)

// ReportOptions is how a run's report is written: Paths gives screenshots
// as absolute paths (for an agent, which opens them with its file tools);
// ProblemsOnly leaves out the cells that passed.
type ReportOptions struct {
	Paths        bool
	ProblemsOnly bool
}

// Report is a run in Markdown: its head, the counts, every cell that did not
// pass with its failing steps, observations and screenshots, the bugs and
// the conclusions.
func (r *Runner) Report(runID string, opt ReportOptions) (string, error) {
	r.mu.Lock()
	rs := r.runs[runID]
	if rs == nil {
		r.mu.Unlock()
		return "", ErrNotFound
	}
	run := *rs.run
	title := filepath.Base(run.Path)
	if rs.plan != nil && rs.plan.Title != "" {
		title = rs.plan.Title
	}
	counts := countsOf(rs)
	var cells []Attempt
	evidence := map[string]string{}
	for _, a := range latestAttempts(rs) {
		c := *a
		cells = append(cells, c)
		evidence[a.ID] = r.layout.EvidenceDir(a)
	}
	r.mu.Unlock()
	bugs, conclusions, _ := r.Bugs(runID)

	var b strings.Builder
	head := "# Test run " + run.ID
	if run.Name != "" {
		head += " · " + run.Name
	}
	b.WriteString(head + "\n\n")
	fmt.Fprintf(&b, "- Repository: %s (%s)\n", title, run.Path)
	fmt.Fprintf(&b, "- State: %s\n", run.State)
	if run.Error != "" {
		fmt.Fprintf(&b, "- Error: %s\n", oneLine(run.Error))
	}
	if run.Commit != "" {
		commit := run.Commit
		if len(commit) > 12 {
			commit = commit[:12]
		}
		if run.Dirty {
			commit += " (with uncommitted changes)"
		}
		fmt.Fprintf(&b, "- Commit: %s\n", commit)
	}
	if run.StartedAt > 0 {
		fmt.Fprintf(&b, "- Started: %s", time.UnixMilli(run.StartedAt).UTC().Format("2006-01-02 15:04 UTC"))
		if run.EndedAt > 0 {
			fmt.Fprintf(&b, ", took %s", time.Duration(run.EndedAt-run.StartedAt).Round(time.Second))
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "- Selection: %s\n", run.Selection.Describe())
	if run.StartedBy != nil {
		fmt.Fprintf(&b, "- Started by the chat “%s”\n", run.StartedBy.Title)
	}
	if counts.Cells > 0 {
		fmt.Fprintf(&b, "- %s\n", countsLine(counts))
	}

	var problems, passed []Attempt
	for _, a := range cells {
		if v := a.Final(); Final(a.State) && (v == testtools.Pass || v == testtools.NA) {
			passed = append(passed, a)
		} else {
			problems = append(problems, a)
		}
	}
	shot := func(a *Attempt, name string) string {
		if opt.Paths {
			return filepath.Join(evidence[a.ID], filepath.FromSlash(name))
		}
		return name
	}
	if len(problems) > 0 {
		b.WriteString("\n## Cells that did not pass\n")
		for i := range problems {
			writeProblemCell(&b, &problems[i], shot)
		}
	} else if counts.Cells > 0 {
		b.WriteString("\nEvery cell passed.\n")
	}
	if !opt.ProblemsOnly && len(passed) > 0 {
		b.WriteString("\n## Cells that passed\n\n")
		for _, a := range passed {
			fmt.Fprintf(&b, "- %s · %s (%s)", a.CellID, a.Title, a.Final())
			if a.Summary != "" {
				fmt.Fprintf(&b, ": %s", oneLine(a.Summary))
			}
			b.WriteString("\n")
		}
	}
	if len(bugs) > 0 {
		b.WriteString("\n## Bugs\n")
		for _, bug := range bugs {
			fmt.Fprintf(&b, "\n### %s (%s): %s\n\n", bug.ID, bug.Severity, bug.Title)
			if len(bug.Cells) > 0 {
				fmt.Fprintf(&b, "Cells: %s\n\n", strings.Join(bug.Cells, ", "))
			}
			if d := strings.TrimSpace(bug.Description); d != "" {
				b.WriteString(d + "\n")
			}
		}
	}
	if c := strings.TrimSpace(conclusions); c != "" {
		b.WriteString("\n## Conclusions\n\n" + c + "\n")
	}
	return b.String(), nil
}

// writeProblemCell is a cell that did not pass: its verdict and who gave
// it, its summary and every step that did not pass with its screenshots.
func writeProblemCell(b *strings.Builder, a *Attempt, shot func(*Attempt, string) string) {
	v := a.Final()
	if v == "" {
		v = a.State
	}
	fmt.Fprintf(b, "\n### %s · %s: %s\n\n", a.CellID, a.Title, v)
	var facts []string
	for _, d := range a.Dims {
		label := d.Label
		if label == "" {
			label = d.ID
		}
		facts = append(facts, d.Name+" "+label)
	}
	facts = append(facts, fmt.Sprintf("priority %s", a.Priority), fmt.Sprintf("attempt %d", a.N))
	if a.Reviewed != "" && a.Reviewed != a.Verdict {
		facts = append(facts, fmt.Sprintf("the agent said %s, the reviewer made it %s", a.Verdict, a.Reviewed))
	}
	b.WriteString(strings.Join(facts, " · ") + "\n")
	if a.EndReason != "" {
		fmt.Fprintf(b, "\nEnded: %s\n", oneLine(a.EndReason))
	}
	if a.Summary != "" {
		fmt.Fprintf(b, "\nSummary: %s\n", oneLine(a.Summary))
	}
	if a.Review != nil && a.Review.Note != "" {
		fmt.Fprintf(b, "\nReviewer: %s\n", oneLine(a.Review.Note))
	}
	byN := map[int]*Action{}
	for i := range a.Actions {
		byN[a.Actions[i].N] = &a.Actions[i]
	}
	for i := range a.Steps {
		s := &a.Steps[i]
		sv := s.StepVerdict()
		if sv == testtools.Pass || sv == testtools.NA {
			continue
		}
		if sv == "" {
			sv = "not judged"
		}
		fmt.Fprintf(b, "\n- Step %d (%s): %s\n  - Expected: %s\n", s.N, sv, oneLine(s.Do), oneLine(s.Expect))
		if s.Observed != "" {
			fmt.Fprintf(b, "  - Observed: %s\n", oneLine(s.Observed))
		}
		if s.Review != nil && s.Review.Note != "" {
			fmt.Fprintf(b, "  - Reviewer (%s): %s\n", s.Review.Verdict, oneLine(s.Review.Note))
		}
		var shots []string
		for _, n := range s.Actions {
			if act := byN[n]; act != nil {
				if act.Error != "" {
					fmt.Fprintf(b, "  - Error of %s: %s\n", act.Action, oneLine(act.Error))
				}
				if act.Screenshot != "" {
					shots = append(shots, shot(a, act.Screenshot))
				}
			}
		}
		for _, name := range s.Attach {
			shots = append(shots, shot(a, name))
		}
		if len(shots) > 0 {
			fmt.Fprintf(b, "  - Screenshots: %s\n", strings.Join(shots, ", "))
		}
	}
}
