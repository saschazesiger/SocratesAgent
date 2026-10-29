package testmode

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/agent/testtools"
	"github.com/saschazesiger/SocratesAgent/internal/testmode/catalog"
)

// Runs started by chat agents (TEST-MODE.md, "Runs started by chat agents"):
// a chat's agent starts a run of its repository's catalog with run_tests
// (StartChatRun), the run remembers the chat (Run.StartedBy), and when the
// run ends the chat is sent its results once (tellStarterLocked), which
// starts its agent as a message from another chat would.

// StartedBy is the chat whose agent started a run.
type StartedBy struct {
	Chat  string `json:"chat"`
	Title string `json:"title,omitempty"`
	// Told is when the chat was sent the run's results (ms); 0 until then.
	// It is kept in run.json, so a restart never sends them twice.
	Told int64 `json:"told,omitempty"`
}

// ChatHost is what a Host offers when it can tell a chat a run's results;
// a host without it starts runs for chats all the same, and tells nobody.
type ChatHost interface {
	// TellChat sends a chat (never a test session) a message from Socrates
	// Tests: it starts the chat's agent, or waits behind its turn. key names
	// the message, so that it is taken once. ErrNotFound when the chat is
	// gone.
	TellChat(ctx context.Context, chat, key, text string) error
}

// ErrNoCatalog is a repository without a test catalog.
var ErrNoCatalog = errors.New("no test catalog")

// ChatRun is what StartChatRun started.
type ChatRun struct {
	Run *Run
	// Cells is how many cells the selection takes.
	Cells int
	// Behind is the repository's active run, which this one waits for; ""
	// when it starts at once.
	Behind string
}

// ProblemsLatest is the ProblemsOf that names the repository's latest run
// that is not active.
const ProblemsLatest = "latest"

// StartChatRun starts a run of the catalog of the repository a chat works
// in (workdir), with the repository's settings, for the chat by. It refuses
// what a run would fail on at once - no catalog, a catalog with errors, a
// selection that names what the catalog does not have or takes no cell -
// with words an agent can act on. The repository joins the Tests page's
// list if it is not on it.
func (r *Runner) StartChatRun(workdir, name string, sel Selection, by StartedBy) (*ChatRun, error) {
	root := FindRoot(workdir)
	if !HasCatalog(root) {
		return nil, fmt.Errorf("%w: %s has no %s/%s", ErrNoCatalog, root, catalog.DirName, catalog.TestsFile)
	}
	cat, err := catalog.Load(root)
	if err != nil {
		return nil, err
	}
	if len(cat.Errors) > 0 {
		var lines []string
		for i, p := range cat.Errors {
			if i == 10 {
				lines = append(lines, fmt.Sprintf("and %d more", len(cat.Errors)-10))
				break
			}
			lines = append(lines, catalog.DirName+"/"+p.String())
		}
		return nil, fmt.Errorf("the catalog has errors, and a run would not start:\n%s", strings.Join(lines, "\n"))
	}
	if err := checkSelection(cat, sel); err != nil {
		return nil, err
	}
	key := RepoKey(root)
	var problems map[string]bool
	if sel.ProblemsOf != "" {
		r.mu.Lock()
		var prev *runState
		if sel.ProblemsOf == ProblemsLatest {
			prev = r.latestRunLocked(key, true)
			if prev == nil {
				r.mu.Unlock()
				return nil, errors.New("problems_of latest: the repository has no run that ended yet")
			}
			sel.ProblemsOf = prev.run.ID
		} else if prev = r.runs[sel.ProblemsOf]; prev == nil || prev.run.Repo != key {
			r.mu.Unlock()
			return nil, fmt.Errorf("problems_of: there is no run %s of this repository", sel.ProblemsOf)
		}
		problems = problemsOf(prev)
		r.mu.Unlock()
	}
	cells := 0
	for _, c := range cat.Cells() {
		if sel.selected(c, problems) {
			cells++
		}
	}
	if cells == 0 {
		if problems != nil && len(problems) == 0 {
			return nil, fmt.Errorf("run %s has no cell that did not pass: there is nothing to run again", sel.ProblemsOf)
		}
		return nil, fmt.Errorf("the selection (%s) takes no cell of the catalog; tests_check lists its tests and cells", sel.Describe())
	}
	repo, err := r.AddRepo(root)
	if err != nil {
		return nil, err
	}
	run, err := r.StartRun(repo.Key, name, sel, nil)
	if err != nil {
		return nil, err
	}
	out := &ChatRun{Cells: cells}
	r.mu.Lock()
	defer r.mu.Unlock()
	rs := r.runs[run.ID]
	if rs == nil {
		return nil, ErrNotFound
	}
	by.Told = 0
	rs.run.StartedBy = &by
	r.saveRunLocked(rs)
	// A run that could not even be prepared may be over already.
	r.tellStarterLocked(rs)
	if other := r.activeRunLocked(repo.Key, run.ID); other != nil && rs.run.State == RunWaiting {
		out.Behind = other.run.ID
	}
	c := *rs.run
	out.Run = &c
	return out, nil
}

// checkSelection says what a selection names that the catalog does not
// have, with what it has, so that an agent can correct it.
func checkSelection(cat *catalog.Catalog, sel Selection) error {
	var problems []string
	var areas []string
	for _, a := range cat.Areas {
		areas = append(areas, a.ID)
	}
	for _, id := range sel.Areas {
		if cat.Area(id) == nil {
			problems = append(problems, fmt.Sprintf("there is no area %q (the areas: %s)", id, strings.Join(areas, ", ")))
		}
	}
	for _, id := range sel.Tests {
		if cat.Test(id) == nil {
			problems = append(problems, fmt.Sprintf("there is no test %q (a test's id is its path in the catalog without .md, such as %s)", id, exampleTest(cat)))
		}
	}
	if len(sel.Cells) > 0 {
		have := map[string]bool{}
		for _, c := range cat.Cells() {
			have[c.ID()] = true
		}
		for _, id := range sel.Cells {
			if !have[id] {
				problems = append(problems, fmt.Sprintf("there is no cell %q (tests_check lists every test's cells)", id))
			}
		}
	}
	for _, p := range sel.Priorities {
		if p != catalog.P0 && p != catalog.P1 && p != catalog.P2 {
			problems = append(problems, fmt.Sprintf("%q is no priority (P0, P1 or P2)", p))
		}
	}
	dims := map[string]map[string]bool{}
	for _, a := range cat.Areas {
		for _, d := range a.Dimensions {
			if dims[d.Name] == nil {
				dims[d.Name] = map[string]bool{}
			}
			for _, v := range append(append([]catalog.Value{}, d.Values...), d.Extra...) {
				dims[d.Name][v.ID] = true
			}
		}
	}
	names := make([]string, 0, len(sel.Dims))
	for name := range sel.Dims {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		values, ok := dims[name]
		if !ok {
			problems = append(problems, fmt.Sprintf("no area has a dimension %q", name))
			continue
		}
		for _, v := range sel.Dims[name] {
			if !values[v] {
				var have []string
				for id := range values {
					have = append(have, id)
				}
				sort.Strings(have)
				problems = append(problems, fmt.Sprintf("%q is no value of %s (%s)", v, name, strings.Join(have, ", ")))
			}
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return errors.New("the selection names what the catalog does not have:\n- " + strings.Join(problems, "\n- "))
}

func exampleTest(cat *catalog.Catalog) string {
	for _, a := range cat.Areas {
		if len(a.Tests) > 0 {
			return a.Tests[0].ID
		}
	}
	return "web/checkout"
}

// problemsOf is the cells of a run whose latest attempt did not pass, as
// "the problems of run X" runs them again.
func problemsOf(rs *runState) map[string]bool {
	out := map[string]bool{}
	for _, a := range latestAttempts(rs) {
		if v := a.Final(); v != testtools.Pass && v != testtools.NA {
			out[a.CellID] = true
		}
	}
	return out
}

// latestRunLocked is a repository's newest run; with ended set, its newest
// that is not active. nil for none.
func (r *Runner) latestRunLocked(repo string, ended bool) *runState {
	var out *runState
	for _, rs := range r.runs {
		if rs.run.Repo != repo || ended && Active(rs.run.State) {
			continue
		}
		if out == nil || rs.run.CreatedAt > out.run.CreatedAt {
			out = rs
		}
	}
	return out
}

// ChatRunID is the run a chat working in workdir means by id: "latest" (or
// "") is its repository's newest run, active or not; any other id has to be
// a run of that repository.
func (r *Runner) ChatRunID(workdir, id string) (string, error) {
	key := RepoKey(FindRoot(workdir))
	r.mu.Lock()
	defer r.mu.Unlock()
	id = strings.TrimSpace(id)
	if id == "" || id == ProblemsLatest {
		rs := r.latestRunLocked(key, false)
		if rs == nil {
			return "", errors.New("This repository has no test run yet; start one with run_tests.")
		}
		return rs.run.ID, nil
	}
	if rs := r.runs[id]; rs == nil || rs.run.Repo != key {
		return "", fmt.Errorf("There is no run %s of this repository.", id)
	}
	return id, nil
}

// tellStartersLocked tells the chats of the runs that ended without their
// chat being told: a restart that came between the end and the message.
func (r *Runner) tellStartersLocked() {
	for _, rs := range r.runs {
		r.tellStarterLocked(rs)
	}
}

// tellStarterLocked sends the chat that started a run its results, once
// the run is over (done, stopped or failed), and once only: Told is set and
// kept before the message goes, so that neither a second end nor a restart
// sends it again.
func (r *Runner) tellStarterLocked(rs *runState) {
	by := rs.run.StartedBy
	if by == nil || by.Chat == "" || by.Told != 0 || Active(rs.run.State) {
		return
	}
	host, ok := r.host.(ChatHost)
	if !ok {
		return
	}
	by.Told = r.now().UnixMilli()
	r.saveRunLocked(rs)
	chat, key, text := by.Chat, "tests-"+rs.run.ID, r.resultsMessageLocked(rs)
	r.spawnLocked(func() {
		if err := host.TellChat(r.ctx, chat, key, text); err != nil && !errors.Is(err, ErrNotFound) {
			log.Printf("tests: could not tell the chat %s the results of its run: %v", chat, err)
		}
	})
}

// The most problem cells the results message names one by one.
const messageCells = 10

// resultsMessageLocked is what the chat that started a run is told when it
// ends: a line for the agent on where it comes from, the run's state and
// counts, a line per cell that did not pass, and where to read more.
func (r *Runner) resultsMessageLocked(rs *runState) string {
	run := rs.run
	var b strings.Builder
	// TODO(tests-b1): name the run by its number (Run.Number) and say the
	// regressions against the run before, once the runner has them.
	fmt.Fprintf(&b, "[From Socrates Tests: the test run %s you started with run_tests has ended. Not a message of a person or of another chat.]\n\n", run.ID)
	title := filepath.Base(run.Path)
	if rs.plan != nil && rs.plan.Title != "" {
		title = rs.plan.Title
	}
	name := ""
	if run.Name != "" {
		name = " (“" + run.Name + "”)"
	}
	fmt.Fprintf(&b, "Run %s%s of %s: %s.", run.ID, name, title, run.State)
	if run.State == RunFailed && run.Error != "" {
		first, _, _ := strings.Cut(run.Error, "\n")
		fmt.Fprintf(&b, " It could not run: %s", clip(first, 300))
	}
	b.WriteString("\n")
	counts := countsOf(rs)
	if counts.Cells > 0 {
		fmt.Fprintf(&b, "%s\n", countsLine(counts))
	}
	var problems []*Attempt
	for _, a := range latestAttempts(rs) {
		if !Final(a.State) {
			continue
		}
		if v := a.Final(); v != testtools.Pass && v != testtools.NA {
			problems = append(problems, a)
		}
	}
	if len(problems) > 0 {
		b.WriteString("Problems:\n")
		for i, a := range problems {
			if i == messageCells {
				fmt.Fprintf(&b, "- and %d more\n", len(problems)-messageCells)
				break
			}
			v := a.Final()
			if v == "" {
				v = a.State
			}
			fmt.Fprintf(&b, "- %s (%s): %s\n", a.CellID, v, clip(reasonOf(a), 160))
		}
	}
	b.WriteString("Call test_results for the details: every failing step, what was observed, and the screenshots.")
	return b.String()
}

// countsLine is a run's counts in a line: "4 cells: 3 pass, 1 fail".
func countsLine(c Counts) string {
	var parts []string
	for _, v := range []string{testtools.Pass, testtools.Issue, testtools.Fail, testtools.Blocked, testtools.NA} {
		if n := c.Verdicts[v]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, v))
		}
	}
	states := make([]string, 0, len(c.States))
	for s := range c.States {
		states = append(states, s)
	}
	slices.Sort(states)
	for _, s := range states {
		parts = append(parts, fmt.Sprintf("%d %s", c.States[s], s))
	}
	cells := "cells"
	if c.Cells == 1 {
		cells = "cell"
	}
	return fmt.Sprintf("%d %s: %s", c.Cells, cells, strings.Join(parts, ", "))
}

// reasonOf is why a cell did not pass, in a line: its first step with its
// verdict and what was observed there, else why the attempt ended, else its
// summary.
func reasonOf(a *Attempt) string {
	v := a.Final()
	for i := range a.Steps {
		s := &a.Steps[i]
		if sv := s.StepVerdict(); sv != "" && sv == v {
			text := fmt.Sprintf("step %d", s.N)
			if obs := strings.TrimSpace(s.Observed); obs != "" {
				text += ": " + oneLine(obs)
			}
			if s.Review != nil && strings.TrimSpace(s.Review.Note) != "" {
				text += " (reviewer: " + oneLine(s.Review.Note) + ")"
			}
			return text
		}
	}
	if a.EndReason != "" {
		return oneLine(a.EndReason)
	}
	if a.Summary != "" {
		return oneLine(a.Summary)
	}
	return "no step judged"
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
