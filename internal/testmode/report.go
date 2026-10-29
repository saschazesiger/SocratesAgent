package testmode

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent/testtools"
)

// The report of a run in Markdown (TEST-MODE.md, "Report"): what a person
// copies or downloads from the run's page, and what an agent reads of a
// run. Everything in it comes from what the run stored, which is redacted
// already; it adds no secret.

// ReportOptions shapes a report.
type ReportOptions struct {
	// Paths names the screenshots by their absolute paths, for an agent
	// that reads them; else by their file names.
	Paths bool
	// ProblemsOnly leaves out the list of every cell.
	ProblemsOnly bool
}

// Report is a run in Markdown: its head, the counts and the changes against
// the run before, the bugs by severity, every cell with a problem with its
// failing steps, and the conclusions.
func (r *Runner) Report(runID string, opt ReportOptions) (string, error) {
	r.mu.Lock()
	rs := r.runs[runID]
	if rs == nil || rs.deleting {
		r.mu.Unlock()
		return "", ErrNotFound
	}
	h := r.historyLocked(rs.run.Repo)
	i := h.index[runID]
	view := r.fullViewLocked(h, i, false)
	run := *rs.run
	title := filepath.Base(run.Path)
	if rs.plan != nil && rs.plan.Title != "" {
		title = rs.plan.Title
	}
	var rows []CellRow
	attempts := map[string]*Attempt{}
	for _, a := range latestAttempts(rs) {
		rows = append(rows, r.cellRowLocked(h, i, rs, a))
		// A copy: a reviewer may still change the steps once the lock is let go.
		c := *a
		c.Steps, c.Actions, c.Dims, c.Bugs = slices.Clone(a.Steps), slices.Clone(a.Actions), slices.Clone(a.Dims), slices.Clone(a.Bugs)
		attempts[a.ID] = &c
	}
	areas := map[string]string{}
	if rs.plan != nil {
		for id, pa := range rs.plan.Areas {
			areas[id] = pa.Title
		}
	}
	bugs := map[string]BugView{}
	for _, b := range r.bugViewsLocked(run.Repo, "") {
		bugs[b.ID] = b
	}
	var reported []testtools.Bug
	_ = readJSON(r.layout.runBugsPath(run.Repo, run.ID), &reported)
	conclusions, _ := os.ReadFile(filepath.Join(r.layout.RunDir(run.Repo, run.ID), "conclusions.md"))
	evidence := map[string]string{}
	for id, a := range attempts {
		evidence[id] = r.layout.EvidenceDir(a)
	}
	r.mu.Unlock()
	views := []RunView{view}
	r.addCosts(views)
	view = views[0]

	var b strings.Builder
	head := fmt.Sprintf("# %s · Run %d", title, run.Number)
	if run.Name != "" {
		head += ": " + run.Name
	}
	b.WriteString(head + "\n\n")
	commit := run.Commit
	if len(commit) > 12 {
		commit = commit[:12]
	}
	if commit != "" && run.Dirty {
		commit += " (with uncommitted changes)"
	}
	line := func(label, value string) {
		if value != "" {
			fmt.Fprintf(&b, "- %s: %s\n", label, value)
		}
	}
	state := run.State
	if run.Error != "" {
		state += " (" + oneLine(run.Error) + ")"
	}
	line("State", state)
	line("Run", run.ID)
	line("Selection", run.Selection.Describe())
	line("Commit", commit)
	if run.StartedAt > 0 {
		line("Started", time.UnixMilli(run.StartedAt).UTC().Format("2006-01-02 15:04 UTC"))
	} else {
		line("Created", time.UnixMilli(run.CreatedAt).UTC().Format("2006-01-02 15:04 UTC"))
	}
	if view.DurationMS > 0 {
		line("Duration", humanDuration(view.DurationMS))
	}
	if view.Cost != nil {
		line("Cost", fmt.Sprintf("$%.2f", *view.Cost))
	}
	line("Cell agent", roleText(run.Settings.Cell))
	if run.Settings.Review {
		line("Reviewer", roleText(run.Settings.Reviewer))
	}
	if run.Settings.Analysis {
		line("Analyst", roleText(run.Settings.Analyst))
	}

	b.WriteString("\n## Results\n\n")
	c := view.Counts
	var parts []string
	for _, v := range []string{testtools.Pass, testtools.Issue, testtools.Fail, testtools.Blocked, testtools.NA} {
		if n := c.Verdicts[v]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, v))
		}
	}
	if left := c.Cells - c.Done; left > 0 {
		parts = append(parts, fmt.Sprintf("%d not done", left))
	}
	fmt.Fprintf(&b, "%d cells: %s.", c.Cells, strings.Join(parts, ", "))
	if view.Changes != nil && i > 0 {
		fmt.Fprintf(&b, " Against the run before: %d regressed, %d fixed.", view.Changes.Regressed, view.Changes.Fixed)
	}
	b.WriteString("\n")
	for _, kind := range []string{ChangeRegressed, ChangeFixed} {
		var list []string
		for _, row := range rows {
			if row.Change == kind && row.Prev != nil {
				list = append(list, fmt.Sprintf("%s (%s in run %d, now %s)", row.CellID, row.Prev.Verdict, row.Prev.Number, row.Final))
			}
		}
		if len(list) > 0 {
			fmt.Fprintf(&b, "\n%s: %s\n", upperFirst(kind), strings.Join(list, "; "))
		}
	}

	if len(reported) > 0 {
		b.WriteString("\n## Bugs\n")
		for _, sev := range testtools.Severities {
			var list []testtools.Bug
			for _, x := range reported {
				if x.Severity == sev {
					list = append(list, x)
				}
			}
			if len(list) == 0 {
				continue
			}
			fmt.Fprintf(&b, "\n### %s\n\n", upperFirst(sev))
			for _, x := range list {
				status := ""
				if tb, ok := bugs[x.ID]; ok {
					status = " (" + strings.ReplaceAll(tb.Status, "_", " ")
					if tb.Regressed {
						status += ", came back after it was fixed"
					}
					if tb.Issue != "" {
						status += ", " + tb.Issue
					}
					status += ")"
				}
				fmt.Fprintf(&b, "- **%s** %s%s", x.ID, oneLine(x.Title), status)
				if len(x.Cells) > 0 {
					fmt.Fprintf(&b, ". Cells: %s", strings.Join(x.Cells, ", "))
				}
				b.WriteString("\n")
			}
		}
	}

	var problems []CellRow
	for _, row := range rows {
		if Final(row.State) && row.Final != "" && !passed(row.Final) {
			problems = append(problems, row)
		}
	}
	if len(problems) > 0 {
		b.WriteString("\n## Problems\n")
		for _, row := range problems {
			a := attempts[row.ID]
			fmt.Fprintf(&b, "\n### %s: %s\n\n", row.CellID, oneLine(a.Title))
			var dims []string
			for _, d := range a.Dims {
				label := d.ID
				if d.Label != "" && d.Label != d.ID {
					label = d.Label + " (" + d.ID + ")"
				}
				dims = append(dims, upperFirst(d.Name)+" "+label)
			}
			if area := areas[a.Area]; area != "" {
				dims = append([]string{"Area " + area}, dims...)
			}
			dims = append(dims, "Priority "+a.Priority, fmt.Sprintf("Attempt %d", a.N))
			b.WriteString(strings.Join(dims, " · ") + "\n\n")
			fmt.Fprintf(&b, "Verdict: **%s** (%s)", row.Final, whoText(a))
			if row.Prev != nil {
				fmt.Fprintf(&b, "; in run %d: %s", row.Prev.Number, row.Prev.Verdict)
				if row.Change != "" {
					b.WriteString(", " + row.Change)
				}
			} else if row.Change == ChangeNew {
				b.WriteString("; new in this run")
			}
			if row.Since != nil && row.Since.Number != run.Number {
				fmt.Fprintf(&b, "; a problem since run %d", row.Since.Number)
			}
			b.WriteString(".\n")
			if a.Override != nil && a.Override.Note != "" {
				fmt.Fprintf(&b, "Override note: %s\n", oneLine(a.Override.Note))
			}
			if len(a.Bugs) > 0 {
				fmt.Fprintf(&b, "Bugs: %s\n", strings.Join(a.Bugs, ", "))
			}
			b.WriteString("\n")
			for si := range a.Steps {
				s := &a.Steps[si]
				v := s.StepVerdict()
				if v == "" || passed(v) {
					continue
				}
				fmt.Fprintf(&b, "- Step %d, %s: %s\n", s.N, v, oneLine(s.Do))
				fmt.Fprintf(&b, "  - Expected: %s\n", oneLine(s.Expect))
				if s.Observed != "" {
					fmt.Fprintf(&b, "  - Observed (the agent said %s): %s\n", s.Verdict, oneLine(s.Observed))
				}
				if s.Review != nil {
					fmt.Fprintf(&b, "  - Reviewer (%s): %s\n", s.Review.Verdict, oneLine(s.Review.Note))
				}
				if shots := stepShots(a, s); len(shots) > 0 {
					if opt.Paths {
						for k := range shots {
							shots[k] = filepath.Join(evidence[a.ID], filepath.FromSlash(shots[k]))
						}
					}
					fmt.Fprintf(&b, "  - Screenshots: %s\n", strings.Join(shots, ", "))
				}
			}
			if a.Summary != "" {
				fmt.Fprintf(&b, "\nSummary: %s\n", oneLine(a.Summary))
			}
			if a.EndReason != "" {
				fmt.Fprintf(&b, "Ended: %s\n", oneLine(a.EndReason))
			}
			if a.Review != nil && a.Review.Note != "" {
				fmt.Fprintf(&b, "Review: %s\n", oneLine(a.Review.Note))
			}
		}
	}

	if !opt.ProblemsOnly && len(rows) > 0 {
		b.WriteString("\n## Every cell\n\n| Cell | Verdict | Before | Change |\n|---|---|---|---|\n")
		for _, row := range rows {
			v := row.Final
			if !Final(row.State) || v == "" {
				v = row.State
			}
			before := ""
			if row.Prev != nil {
				before = fmt.Sprintf("%s (run %d)", row.Prev.Verdict, row.Prev.Number)
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", cellText(row.CellID), v, before, row.Change)
		}
	}

	if md := strings.TrimSpace(string(conclusions)); md != "" {
		b.WriteString("\n## Conclusions\n\n")
		// The analyst's headings sit below the report's own.
		for _, l := range strings.Split(md, "\n") {
			if strings.HasPrefix(l, "#") {
				l = "##" + l
			}
			b.WriteString(l + "\n")
		}
	}
	return b.String(), nil
}

// whoText says who gave an attempt's verdict as it counts.
func whoText(a *Attempt) string {
	var parts []string
	parts = append(parts, "the agent said "+a.Verdict)
	if a.Reviewed != "" {
		parts = append(parts, "the reviewer made it "+a.Reviewed)
	}
	if a.Override != nil {
		parts = append(parts, "overridden by hand to "+a.Override.Verdict)
	}
	return strings.Join(parts, ", ")
}

func roleText(r Role) string {
	parts := slices.DeleteFunc([]string{r.Harness, r.Model, r.Effort}, func(s string) bool { return s == "" })
	return strings.Join(parts, " · ")
}

// oneLine is a text on one line, for a list item.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// cellText is a cell id as a table's cell: its pipes escaped.
func cellText(s string) string { return strings.ReplaceAll(s, "|", "\\|") }

func humanDuration(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Round(time.Minute).Minutes()))
	}
	return fmt.Sprintf("%d h %d min", int(d.Hours()), int(d.Minutes())%60)
}

// ReportFileName is the file a run's report downloads as:
// <repository>-run-<number>.md, of safe characters only.
func ReportFileName(repo string, number int) string {
	name := strings.Trim(keyUnsafe.ReplaceAllString(strings.ToLower(repo), "-"), "-")
	if name == "" {
		name = "tests"
	}
	return fmt.Sprintf("%s-run-%d.md", name, number)
}
