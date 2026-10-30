package testmode

import (
	"fmt"
	"strings"
)

// CellText is one cell of a run as a chat's agent reads it (the Socrates
// Tests MCP server, internal/mcpserver/tests): its latest attempt, or the
// attempt of that id, with its steps, the agent's, the reviewer's and the
// person's verdicts, its actions and the absolute paths of its screenshots,
// and the other attempts of the cell.
func (r *Runner) CellText(runID, cell string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rs := r.runs[runID]
	if rs == nil || rs.deleting {
		return "", ErrNotFound
	}
	cell = strings.TrimSpace(cell)
	a := rs.attempts[cell]
	if a == nil {
		a = cellOf(rs, cell)
	}
	if a == nil {
		return "", fmt.Errorf("run %s has no cell %s", runID, cell)
	}
	var b strings.Builder
	b.WriteString(r.evidenceText(a, true))
	fmt.Fprintf(&b, "\nState: %s", a.State)
	if a.EndReason != "" {
		fmt.Fprintf(&b, " (%s)", oneLine(a.EndReason))
	}
	if a.Wait != "" {
		fmt.Fprintf(&b, "; %s", a.Wait)
	}
	fmt.Fprintf(&b, "\nVerdict: %s (agent: %s", orDash(a.Final()), orDash(a.Verdict))
	if a.Reviewed != "" {
		fmt.Fprintf(&b, ", reviewer: %s", a.Reviewed)
	}
	if a.Override != nil {
		fmt.Fprintf(&b, ", the person: %s", a.Override.Verdict)
	}
	b.WriteString(")\n")
	if a.Override != nil && a.Override.Note != "" {
		fmt.Fprintf(&b, "The person's note: %s\n", a.Override.Note)
	}
	if a.Summary != "" {
		fmt.Fprintf(&b, "Summary: %s\n", a.Summary)
	}
	if a.Review != nil && a.Review.Note != "" {
		fmt.Fprintf(&b, "Review: %s\n", a.Review.Note)
	}
	if len(a.Bugs) > 0 {
		fmt.Fprintf(&b, "Bugs: %s\n", strings.Join(a.Bugs, ", "))
	}
	var others []string
	for _, ref := range rs.run.Order {
		if x := rs.attempts[ref.ID]; x != nil && x.CellID == a.CellID && x.ID != a.ID {
			others = append(others, fmt.Sprintf("attempt %d (%s): %s", x.N, x.ID, orDash(x.Final())))
		}
	}
	if len(others) > 0 {
		fmt.Fprintf(&b, "Other attempts of the cell: %s\n", strings.Join(others, "; "))
	}
	return b.String(), nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
