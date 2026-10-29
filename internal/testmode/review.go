package testmode

import (
	"fmt"
	"hash/fnv"
	"slices"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/agent/testtools"
)

// The review (TEST-MODE.md, "Review and analysis", Device QA's review.ts):
// every cell the agent did not pass; every passed cell with a passed step
// whose last interaction had an error; one passed cell in five, a stable
// sample; and every passed cell of a test once a reviewed pass of it turned
// out wrong. Always the cell's latest attempt.

// sampleEvery is the share of passed cells sampled: one in so many.
const sampleEvery = 5

// reviewReason is why an ended attempt is reviewed, "" when it is not.
func reviewReason(a *Attempt, widened bool) string {
	if !passed(a.Verdict) {
		return "the agent judged it " + a.Verdict
	}
	for i := range a.Steps {
		s := &a.Steps[i]
		if s.Verdict != testtools.Pass {
			continue
		}
		var last *Action
		for _, n := range s.Actions {
			if n >= 1 && n <= len(a.Actions) && a.Actions[n-1].Interaction {
				last = &a.Actions[n-1]
			}
		}
		if last != nil && last.Error != "" {
			return fmt.Sprintf("step %d passed although its last interaction failed", s.N)
		}
	}
	if widened {
		return "a reviewed pass of this test turned out wrong"
	}
	h := fnv.New32a()
	h.Write([]byte(a.CellID))
	if h.Sum32()%sampleEvery == 0 {
		return "one passed cell in five is checked"
	}
	return ""
}

// queueReviewLocked marks an ended attempt for review when the rules say so.
func (r *Runner) queueReviewLocked(rs *runState, a *Attempt) {
	if !rs.run.Settings.Review || a.State == AttemptStopped || a.State == AttemptFailed {
		return
	}
	widened := false
	for _, x := range latestAttempts(rs) {
		if x.Test == a.Test && x.Review != nil && x.Review.State == ReviewDone && passed(x.Verdict) && !passed(x.Final()) {
			widened = true
		}
	}
	if reason := reviewReason(a, widened); reason != "" {
		a.Review = &Review{State: ReviewPending, Reason: reason}
		r.saveAttemptLocked(a)
	}
}

// startHelpersLocked starts the reviewers and the analyst a run needs now.
func (r *Runner) startHelpersLocked(rs *runState, running *int) []start {
	run := rs.run
	if (run.State != RunRunning && run.State != RunFinishing) || rs.plan == nil {
		return nil
	}
	var out []start
	cellsDone := true
	for _, a := range rs.attempts {
		if !Final(a.State) {
			cellsDone = false
		}
	}
	if run.Settings.Review {
		var pending []*Attempt
		for _, a := range latestAttempts(rs) {
			if a.Review != nil && a.Review.State == ReviewPending && Final(a.State) {
				pending = append(pending, a)
			}
		}
		active := 0
		for _, h := range run.Reviews {
			if h.State == "running" {
				active++
			}
		}
		for len(pending) > 0 && active < run.Settings.ReviewParallel && *running < r.global.MaxSessions &&
			(len(pending) >= ReviewBatch || cellsDone) {
			n := min(ReviewBatch, len(pending))
			batch := pending[:n]
			pending = pending[n:]
			h := &Helper{ID: newHelperID(), State: "running", Role: run.Settings.Reviewer, StartedAt: r.now().UnixMilli()}
			account, wait := r.host.PickAccount(h.Role.Harness, h.Role.Accounts, r.loadLocked())
			if wait != "" {
				break
			}
			h.Role.Account = account
			for _, a := range batch {
				h.Cells = append(h.Cells, a.CellID)
				h.Attempts = append(h.Attempts, a.ID)
				a.Review.State, a.Review.Helper = ReviewRunning, h.ID
				r.saveAttemptLocked(a)
			}
			run.Reviews = append(run.Reviews, h)
			active++
			*running++
			out = append(out, start{rs: rs, helper: h, spec: SessionSpec{
				Ref:   Ref{Kind: testtools.KindReviewer, Repo: run.Repo, Run: run.ID, ID: h.ID},
				Title: fmt.Sprintf("Review of %d test cells", len(h.Cells)),
				Role:  h.Role, Workdir: r.layout.RunDir(run.Repo, run.ID),
				Message: reviewMessage(h),
			}})
		}
	}
	if run.Settings.Analysis && run.Analysis == nil && cellsDone && *running < r.global.MaxSessions && reviewsOver(rs) {
		h := &Helper{ID: newHelperID(), State: "running", Role: run.Settings.Analyst, StartedAt: r.now().UnixMilli()}
		if account, wait := r.host.PickAccount(h.Role.Harness, h.Role.Accounts, r.loadLocked()); wait == "" {
			h.Role.Account = account
			run.Analysis = h
			*running++
			out = append(out, start{rs: rs, helper: h, spec: SessionSpec{
				Ref:   Ref{Kind: testtools.KindAnalyst, Repo: run.Repo, Run: run.ID, ID: h.ID},
				Title: fmt.Sprintf("Analysis of test run %d (%s)", run.Number, run.ID),
				Role:  h.Role, Workdir: run.Path,
				Message: analysisMessage(rs),
			}})
		}
	}
	if len(out) > 0 {
		r.saveRunLocked(rs)
	}
	return out
}

// reviewsOver is whether no review of a run waits or runs.
func reviewsOver(rs *runState) bool {
	for _, a := range latestAttempts(rs) {
		if a.Review != nil && (a.Review.State == ReviewPending || a.Review.State == ReviewRunning) {
			return false
		}
	}
	for _, h := range rs.run.Reviews {
		if h.State == "running" {
			return false
		}
	}
	return true
}

func reviewMessage(h *Helper) string {
	return "Review these cells of the test run, each with " + testtools.ReviewEvidence + ", then " + testtools.ReviewAgent +
		", correcting with " + testtools.ReviewStep + " and closing with " + testtools.ReviewDone + ":\n\n- " +
		strings.Join(h.Cells, "\n- ") + "\n\n" + testtools.ReviewList + " says why each is reviewed."
}

func analysisMessage(rs *runState) string {
	c := countsOf(rs)
	return fmt.Sprintf("Analyse test run %s (run %d, %s): %d cells, %d pass, %d issue, %d fail, %d blocked. "+
		"Read it with %s and %s and the bugs known from earlier runs with %s, then write the bugs with %s and the "+
		"conclusions with %s.",
		rs.run.ID, rs.run.Number, rs.run.Selection.Describe(), c.Cells, c.Verdicts[testtools.Pass], c.Verdicts[testtools.Issue],
		c.Verdicts[testtools.Fail], c.Verdicts[testtools.Blocked], testtools.RunOverview, testtools.RunCell,
		testtools.BugsKnown, testtools.BugsPut, testtools.ConclusionsPut)
}

// helperFailedLocked lets go of what a helper that failed held: its cells'
// reviews are skipped, an analysis that failed counts as done (the run ends
// without one).
func (r *Runner) helperFailedLocked(rs *runState, h *Helper) {
	for _, a := range rs.helperAttempts(h) {
		if a.Review != nil && a.Review.Helper == h.ID && a.Review.State != ReviewDone {
			a.Review.State, a.Review.Note = ReviewSkipped, "the reviewer could not finish: "+h.Error
			r.saveAttemptLocked(a)
		}
	}
}

// helperTurnEndedLocked is a helper's turn that ended: done when it did its
// work, else nudged once, then let go. It returns the nudge to send.
func (r *Runner) helperTurnEndedLocked(rs *runState, h *Helper, kind string) string {
	var open []string
	if kind == testtools.KindReviewer {
		for _, a := range rs.helperAttempts(h) {
			if a.Review != nil && a.Review.Helper == h.ID && a.Review.State == ReviewRunning {
				open = append(open, a.CellID)
			}
		}
	} else {
		for _, w := range []string{"bugs", "conclusions"} {
			if !slices.Contains(h.Wrote, w) {
				open = append(open, w)
			}
		}
	}
	now := r.now().UnixMilli()
	if len(open) == 0 {
		h.State, h.EndedAt = "done", now
		if kind == testtools.KindAnalyst {
			rs.run.Analysed = now
		}
		r.saveRunLocked(rs)
		return ""
	}
	if h.Nudged < 1 {
		h.Nudged++
		r.saveRunLocked(rs)
		if kind == testtools.KindReviewer {
			return "You ended your turn, but these cells are not closed yet: " + strings.Join(open, ", ") +
				". Review each and close it with " + testtools.ReviewDone + "."
		}
		return "You ended your turn, but the " + strings.Join(open, " and ") + " are not written yet. Write them with " +
			testtools.BugsPut + " and " + testtools.ConclusionsPut + "."
	}
	h.State, h.EndedAt, h.Error = "done", now, "ended without finishing: "+strings.Join(open, ", ")
	if kind == testtools.KindReviewer {
		r.helperFailedLocked(rs, h)
	} else {
		rs.run.Analysed = now
	}
	r.saveRunLocked(rs)
	return ""
}

// helperAttempts is the attempts a reviewer was given.
func (rs *runState) helperAttempts(h *Helper) []*Attempt {
	var out []*Attempt
	for _, id := range h.Attempts {
		if a := rs.attempts[id]; a != nil {
			out = append(out, a)
		}
	}
	return out
}

// helperAttempt is the attempt of a cell a reviewer was given: the one it
// was given, even when the cell has been queued again since.
func (rs *runState) helperAttempt(h *Helper, cellID string) *Attempt {
	cellID = strings.TrimSpace(cellID)
	for i, c := range h.Cells {
		if c == cellID && i < len(h.Attempts) {
			return rs.attempts[h.Attempts[i]]
		}
	}
	return nil
}
