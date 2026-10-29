package testmode

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/agent/testtools"
)

// The bugs of a repository over its runs (TEST-MODE.md, "Bugs over runs"):
// one list per repository, <data>/tests/<repo>/bugs.json, that every run's
// analysis reports into. Socrates gives the ids; the analyst names a bug it
// knows by its id (bugs_known), so that one root cause keeps its id over the
// runs. When a run is done, the list learns what it reported, reopens a
// fixed bug it saw again and marks fixed an open bug whose cells all pass
// in it. A person triages: status, severity, a note, a GitHub issue.

// The statuses of a bug.
const (
	BugOpen    = "open"
	BugKnown   = "known"
	BugNotABug = "not_a_bug"
	BugFixed   = "fixed"
)

// BugStatuses is every status of a bug.
var BugStatuses = []string{BugOpen, BugKnown, BugNotABug, BugFixed}

// knownFixedRuns is how many runs after it was fixed a bug is still shown
// to the analyst.
const knownFixedRuns = 10

// TrackedBug is a bug of the repository's list.
type TrackedBug struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Severity    string `json:"severity"`
	Status      string `json:"status"`
	Description string `json:"description,omitempty"`
	// Cells is the cells that show it, as last reported; Runs the runs that
	// reported it, oldest first.
	Cells    []string `json:"cells"`
	Runs     []string `json:"runs"`
	FirstRun string   `json:"first_run"`
	LastRun  string   `json:"last_run"`
	// FixedIn is the run that showed it fixed; Regressed that it came back
	// after it was fixed.
	FixedIn   string `json:"fixed_in,omitempty"`
	Regressed bool   `json:"regressed,omitempty"`
	// Note is the person's; Issue the URL of its issue.
	Note  string `json:"note,omitempty"`
	Issue string `json:"issue,omitempty"`
	// SeverityByHand is a severity the person set, which the analysts'
	// reports no longer change.
	SeverityByHand bool       `json:"severity_by_hand,omitempty"`
	UpdatedAt      int64      `json:"updated_at"`
	Events         []BugEvent `json:"events"`
}

// BugEvent is one thing that happened to a bug.
type BugEvent struct {
	At   int64  `json:"at"`
	Run  string `json:"run,omitempty"`
	What string `json:"what"`
}

// bugList is a repository's bugs.json: the bugs, and the number the next
// new one gets (ids are never given twice).
type bugList struct {
	Next int           `json:"next"`
	Bugs []*TrackedBug `json:"bugs"`
}

func (l Layout) bugsPath(repo string) string { return filepath.Join(l.RepoDir(repo), "bugs.json") }

func (l Layout) runBugsPath(repo, run string) string {
	return filepath.Join(l.RunDir(repo, run), "bugs.json")
}

// bugNumber is the number of a bug id (B-12), 0 for another.
func bugNumber(id string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(id)), "B-"))
	if err != nil || !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(id)), "B-") || n < 1 {
		return 0
	}
	return n
}

func (l *bugList) find(id string) *TrackedBug {
	n := bugNumber(id)
	if n == 0 {
		return nil
	}
	for _, b := range l.Bugs {
		if bugNumber(b.ID) == n {
			return b
		}
	}
	return nil
}

// bugListLocked is a repository's bug list, read from its file the first
// time.
func (r *Runner) bugListLocked(repo string) *bugList {
	if l := r.bugs[repo]; l != nil {
		return l
	}
	l := &bugList{}
	if err := readJSON(r.layout.bugsPath(repo), l); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("tests: %s: the bug list: %v", repo, err)
	}
	for _, b := range l.Bugs {
		l.Next = max(l.Next, bugNumber(b.ID)+1)
	}
	l.Next = max(l.Next, 1)
	r.bugs[repo] = l
	return l
}

func (r *Runner) saveBugsLocked(repo string) {
	if err := writeJSON(r.layout.bugsPath(repo), r.bugListLocked(repo)); err != nil {
		log.Printf("tests: %s: the bug list: %v", repo, err)
	}
	r.host.Publish(map[string]any{"t": "tests", "repo": repo, "bugs": true})
}

// numberLocked is a run's number, 0 for a run that is gone.
func (r *Runner) numberLocked(id string) int {
	if rs := r.runs[id]; rs != nil {
		return rs.run.Number
	}
	return 0
}

func (b *TrackedBug) event(at int64, run, what string) {
	b.Events = append(b.Events, BugEvent{At: at, Run: run, What: what})
	b.UpdatedAt = at
}

// knownBugsText is what bugs_known tells the analyst of a run: every bug of
// the repository but those fixed more than knownFixedRuns runs before it.
func (r *Runner) knownBugsText(rs *runState) string {
	var b strings.Builder
	for _, bug := range r.bugViewsLocked(rs.run.Repo, "") {
		if bug.Status == BugFixed && bug.FixedNumber < rs.run.Number-knownFixedRuns {
			continue
		}
		fmt.Fprintf(&b, "## %s [%s, %s] %s\n", bug.ID, bug.Status, bug.Severity, bug.Title)
		if len(bug.Cells) > 0 {
			fmt.Fprintf(&b, "Cells: %s\n", strings.Join(bug.Cells, ", "))
		}
		fmt.Fprintf(&b, "Reported in %d run(s), last in run %d", len(bug.Runs), bug.LastNumber)
		if bug.Status == BugFixed && bug.FixedNumber > 0 {
			fmt.Fprintf(&b, "; fixed in run %d", bug.FixedNumber)
		}
		b.WriteString(".\n")
		if bug.Note != "" {
			fmt.Fprintf(&b, "Note of the person: %s\n", bug.Note)
		}
		if d := clip(bug.Description, 1500); d != "" {
			b.WriteString(d + "\n")
		}
		b.WriteString("\n")
	}
	if b.Len() == 0 {
		return "The repository has no known bug yet: every bug you report is new (leave its id empty)."
	}
	return "The bugs of this repository known from earlier runs. Report a bug with the same root cause under its id; " +
		"leave the id empty for a new one.\n\n" + strings.TrimSpace(b.String())
}

// settleBugIDsLocked gives the bugs an analyst wrote the ids Socrates keeps:
// an id of the repository's list stays, and so does one Socrates gave a new
// bug when a list was written before (bugs_put's answer names it) and that
// is not on the repository's list yet; any other (empty, or the analyst's
// own such as B1) is a new bug, which gets the id the run's list before gave
// a bug of that title, or a new one.
func (r *Runner) settleBugIDsLocked(rs *runState, bugs []testtools.Bug) error {
	list := r.bugListLocked(rs.run.Repo)
	before := map[string]string{}
	var old []testtools.Bug
	if readJSON(r.layout.runBugsPath(rs.run.Repo, rs.run.ID), &old) == nil {
		for _, b := range old {
			if list.find(b.ID) == nil && bugNumber(b.ID) > 0 {
				before[strings.ToLower(strings.TrimSpace(b.Title))] = b.ID
			}
		}
	}
	used := map[string]bool{}
	settled := make([]bool, len(bugs))
	for i := range bugs {
		id := ""
		if known := list.find(bugs[i].ID); known != nil {
			id = known.ID
		} else if n := bugNumber(bugs[i].ID); n > 0 && n < list.Next {
			id = fmt.Sprintf("B-%d", n)
		}
		if id == "" {
			continue
		}
		if used[id] {
			return fmt.Errorf("%s is in the list twice: one entry per bug", id)
		}
		bugs[i].ID, settled[i] = id, true
		used[id] = true
	}
	grew := false
	for i := range bugs {
		if settled[i] {
			continue
		}
		if id := before[strings.ToLower(strings.TrimSpace(bugs[i].Title))]; id != "" && !used[id] {
			bugs[i].ID = id
		} else {
			bugs[i].ID = fmt.Sprintf("B-%d", list.Next)
			list.Next++
			grew = true
		}
		used[bugs[i].ID] = true
	}
	if grew {
		// The numbers given are kept, so that none is given twice.
		r.saveBugsLocked(rs.run.Repo)
	}
	return nil
}

// mergeBugsLocked brings the repository's bug list up to date with a run
// that is done: the bugs it reported (its bugs.json) get it, a fixed one it
// reported is open again, and an open one it did not report whose cells it
// ran and all of which pass now is fixed. A run analysed again takes back
// what it reported before and no longer does. Known bugs and bugs that are
// not one keep their status: only the person changes it.
func (r *Runner) mergeBugsLocked(rs *runState) {
	run := rs.run
	var reported []testtools.Bug
	_ = readJSON(r.layout.runBugsPath(run.Repo, run.ID), &reported)
	list := r.bugListLocked(run.Repo)
	now := r.now().UnixMilli()
	num := run.Number
	got := map[string]bool{}
	changed := false
	for _, rb := range reported {
		b := list.find(rb.ID)
		if b == nil {
			if bugNumber(rb.ID) == 0 {
				continue
			}
			b = &TrackedBug{ID: rb.ID, Status: BugOpen, Severity: rb.Severity, FirstRun: run.ID, Cells: []string{}, Runs: []string{}}
			b.event(now, run.ID, fmt.Sprintf("reported in run %d", num))
			list.Bugs = append(list.Bugs, b)
			list.Next = max(list.Next, bugNumber(b.ID)+1)
		} else if !slices.Contains(b.Runs, run.ID) {
			b.event(now, run.ID, fmt.Sprintf("reported again in run %d", num))
		}
		got[b.ID] = true
		changed = true
		if !slices.Contains(b.Runs, run.ID) {
			b.Runs = append(b.Runs, run.ID)
		}
		if b.LastRun == "" || r.numberLocked(b.LastRun) <= num {
			b.Title, b.Description, b.LastRun = rb.Title, rb.Description, run.ID
			b.Cells = append([]string{}, rb.Cells...)
			if !b.SeverityByHand {
				b.Severity = rb.Severity
			}
		}
		if b.FirstRun == "" || r.numberLocked(b.FirstRun) > num {
			b.FirstRun = run.ID
		}
		if b.Status == BugFixed && num > r.numberLocked(b.FixedIn) {
			b.Status, b.Regressed, b.FixedIn = BugOpen, true, ""
			b.event(now, run.ID, fmt.Sprintf("reopened: reported again in run %d after it was fixed", num))
		}
		b.UpdatedAt = now
	}
	// What an earlier analysis of this run reported and this one does not.
	keep := list.Bugs[:0]
	for _, b := range list.Bugs {
		if got[b.ID] || !slices.Contains(b.Runs, run.ID) {
			keep = append(keep, b)
			continue
		}
		changed = true
		b.Runs = slices.DeleteFunc(b.Runs, func(id string) bool { return id == run.ID })
		if len(b.Runs) == 0 && b.Note == "" && b.Issue == "" {
			continue
		}
		b.LastRun, b.FirstRun = "", ""
		for _, id := range b.Runs {
			if b.LastRun == "" || r.numberLocked(id) > r.numberLocked(b.LastRun) {
				b.LastRun = id
			}
			if b.FirstRun == "" || r.numberLocked(id) < r.numberLocked(b.FirstRun) {
				b.FirstRun = id
			}
		}
		b.event(now, run.ID, fmt.Sprintf("no longer reported by run %d, which was analysed again", num))
		keep = append(keep, b)
	}
	list.Bugs = keep
	// An open bug whose cells all pass in this run is fixed.
	verdicts := map[string]string{}
	for _, a := range latestAttempts(rs) {
		if Final(a.State) && a.Final() != "" {
			verdicts[a.CellID] = a.Final()
		}
	}
	for _, b := range list.Bugs {
		if b.Status != BugOpen || got[b.ID] || len(b.Cells) == 0 || num <= r.numberLocked(b.LastRun) {
			continue
		}
		fixed := true
		for _, c := range b.Cells {
			if v, ok := verdicts[c]; !ok || !passed(v) {
				fixed = false
				break
			}
		}
		if fixed {
			b.Status, b.FixedIn = BugFixed, run.ID
			b.event(now, run.ID, fmt.Sprintf("fixed: its cells pass in run %d", num))
			changed = true
		}
	}
	if changed {
		r.saveBugsLocked(run.Repo)
	}
}

// BugView is a bug as the page shows it: with the numbers of its runs.
type BugView struct {
	TrackedBug
	FirstNumber int `json:"first_number,omitempty"`
	LastNumber  int `json:"last_number,omitempty"`
	FixedNumber int `json:"fixed_number,omitempty"`
	// Seen is how many runs reported it.
	Seen int `json:"seen"`
}

func (r *Runner) bugViewLocked(b *TrackedBug) BugView {
	v := BugView{TrackedBug: *b, FirstNumber: r.numberLocked(b.FirstRun), LastNumber: r.numberLocked(b.LastRun),
		FixedNumber: r.numberLocked(b.FixedIn), Seen: len(b.Runs)}
	v.Cells, v.Runs, v.Events = slices.Clone(b.Cells), slices.Clone(b.Runs), slices.Clone(b.Events)
	if v.Cells == nil {
		v.Cells = []string{}
	}
	if v.Runs == nil {
		v.Runs = []string{}
	}
	if v.Events == nil {
		v.Events = []BugEvent{}
	}
	return v
}

// bugViewsLocked is a repository's bugs of a status ("" or "all": every
// bug), the worst first, then the last seen first.
func (r *Runner) bugViewsLocked(repo, status string) []BugView {
	out := []BugView{}
	for _, b := range r.bugListLocked(repo).Bugs {
		if status == "" || status == "all" || b.Status == status {
			out = append(out, r.bugViewLocked(b))
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		si, sj := slices.Index(testtools.Severities, out[i].Severity), slices.Index(testtools.Severities, out[j].Severity)
		if si != sj {
			return si < sj
		}
		if out[i].LastNumber != out[j].LastNumber {
			return out[i].LastNumber > out[j].LastNumber
		}
		return bugNumber(out[i].ID) < bugNumber(out[j].ID)
	})
	return out
}

// BugList is a repository's bugs of a status ("" or "all": every bug).
func (r *Runner) BugList(repo, status string) ([]BugView, error) {
	if status != "" && status != "all" && !slices.Contains(BugStatuses, status) {
		return nil, fmt.Errorf("the status must be one of %s or all", strings.Join(BugStatuses, ", "))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.repos[repo] == nil {
		return nil, ErrNotFound
	}
	return r.bugViewsLocked(repo, status), nil
}

// BugDetail is a bug with its cells (the latest attempt of each in the
// last run that reported it, against the run before) and the runs that
// reported it, newest first.
type BugDetail struct {
	Bug   BugView      `json:"bug"`
	Cells []CellRow    `json:"cells"`
	Runs  []TestRunRef `json:"runs"`
}

// Bug is one bug of a repository.
func (r *Runner) Bug(repo, id string) (*BugDetail, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.repos[repo] == nil {
		return nil, ErrNotFound
	}
	b := r.bugListLocked(repo).find(id)
	if b == nil {
		return nil, ErrNotFound
	}
	d := &BugDetail{Bug: r.bugViewLocked(b), Cells: []CellRow{}, Runs: []TestRunRef{}}
	h := r.historyLocked(repo)
	for _, cell := range b.Cells {
		// The cell in the bug's last run, else in the newest run that has it.
		var rs *runState
		var a *Attempt
		if x := r.runs[b.LastRun]; x != nil {
			rs, a = x, cellOf(x, cell)
		}
		for i := len(h.runs) - 1; a == nil && i >= 0; i-- {
			rs, a = h.runs[i], cellOf(h.runs[i], cell)
		}
		if a != nil {
			d.Cells = append(d.Cells, r.cellRowLocked(h, h.index[rs.run.ID], rs, a))
		}
	}
	for i := len(b.Runs) - 1; i >= 0; i-- {
		if rs := r.runs[b.Runs[i]]; rs != nil {
			d.Runs = append(d.Runs, TestRunRef{ID: rs.run.ID, Number: rs.run.Number, Name: rs.run.Name, CreatedAt: rs.run.CreatedAt})
		}
	}
	sort.SliceStable(d.Runs, func(i, j int) bool { return d.Runs[i].Number > d.Runs[j].Number })
	return d, nil
}

// BugPatch is what a person changes of a bug; a field left out stays.
type BugPatch struct {
	Status   *string `json:"status"`
	Severity *string `json:"severity"`
	Note     *string `json:"note"`
	Issue    *string `json:"issue"`
}

// maxNote is the longest note a person may keep on a bug.
const maxNote = 4000

// PatchBug triages a bug: its status, severity, note or issue.
func (r *Runner) PatchBug(repo, id string, p BugPatch) (*BugView, error) {
	if p.Status != nil && !slices.Contains(BugStatuses, *p.Status) {
		return nil, fmt.Errorf("the status must be one of %s", strings.Join(BugStatuses, ", "))
	}
	if p.Severity != nil && !slices.Contains(testtools.Severities, *p.Severity) {
		return nil, fmt.Errorf("the severity must be one of %s", strings.Join(testtools.Severities, ", "))
	}
	if p.Note != nil && len(strings.TrimSpace(*p.Note)) > maxNote {
		return nil, fmt.Errorf("the note is longer than %d characters", maxNote)
	}
	if p.Issue != nil {
		u := strings.TrimSpace(*p.Issue)
		if u != "" && (!strings.HasPrefix(u, "https://") || strings.ContainsAny(u, " \n\t")) {
			return nil, errors.New("the issue must be an https:// address")
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.repos[repo] == nil {
		return nil, ErrNotFound
	}
	b := r.bugListLocked(repo).find(id)
	if b == nil {
		return nil, ErrNotFound
	}
	now := r.now().UnixMilli()
	if p.Status != nil && *p.Status != b.Status {
		b.Status = *p.Status
		switch b.Status {
		case BugFixed:
			// Fixed by hand: as of the repository's latest run, so that a
			// later report opens it again.
			b.FixedIn = ""
			if latest := r.latestRunLocked(repo); latest != nil {
				b.FixedIn = latest.run.ID
			}
		case BugOpen:
			b.FixedIn = ""
		}
		b.event(now, "", "status set to "+b.Status+" by hand")
	}
	if p.Severity != nil && (*p.Severity != b.Severity || !b.SeverityByHand) {
		b.Severity, b.SeverityByHand = *p.Severity, true
		b.event(now, "", "severity set to "+b.Severity+" by hand")
	}
	if p.Note != nil && strings.TrimSpace(*p.Note) != b.Note {
		b.Note = strings.TrimSpace(*p.Note)
		b.event(now, "", "note changed")
	}
	if p.Issue != nil && strings.TrimSpace(*p.Issue) != b.Issue {
		b.Issue = strings.TrimSpace(*p.Issue)
		what := "issue removed"
		if b.Issue != "" {
			what = "issue set: " + b.Issue
		}
		b.event(now, "", what)
	}
	r.saveBugsLocked(repo)
	v := r.bugViewLocked(b)
	return &v, nil
}

// latestRunLocked is the repository's latest run that is not active, nil
// for none.
func (r *Runner) latestRunLocked(repo string) *runState {
	var latest *runState
	for _, rs := range r.runs {
		if rs.run.Repo != repo || Active(rs.run.State) || rs.deleting {
			continue
		}
		if latest == nil || rs.run.Number > latest.run.Number ||
			rs.run.Number == latest.run.Number && rs.run.CreatedAt > latest.run.CreatedAt {
			latest = rs
		}
	}
	return latest
}
