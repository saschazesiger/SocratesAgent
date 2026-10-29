package testmode

import (
	"sort"
	"strconv"

	"github.com/saschazesiger/SocratesAgent/internal/agent/testtools"
	"github.com/saschazesiger/SocratesAgent/internal/testmode/catalog"
)

// How a repository's cells stand over its runs (TEST-MODE.md, "Revision: the
// reworked Tests page"): a cell against the run before (prev, change), the
// streak of problems it is in (since), and its health over the last runs
// (flips, flaky). Every verdict here is the one that counts (Attempt.Final).

// healthRuns is how many of a repository's latest runs its health is read
// from.
const healthRuns = 20

// The changes of a cell against the run before.
const (
	ChangeRegressed = "regressed"
	ChangeFixed     = "fixed"
	ChangeNew       = "new"
)

// problem is whether a verdict is a problem of the product: issue or fail.
func problem(v string) bool { return v == testtools.Issue || v == testtools.Fail }

// changeOf is what a cell's verdict now makes of it against its verdict in
// the run before (none: hadPrev false).
func changeOf(prev string, hadPrev bool, now string) string {
	switch {
	case !hadPrev:
		return ChangeNew
	case passed(prev) && problem(now):
		return ChangeRegressed
	case problem(prev) && passed(now):
		return ChangeFixed
	}
	return ""
}

// PrevVerdict is a cell's verdict in an earlier run.
type PrevVerdict struct {
	Verdict string `json:"verdict"`
	Run     string `json:"run"`
	Number  int    `json:"number"`
	Attempt string `json:"attempt"`
}

// RunMark names a run: its id, number and when it was created.
type RunMark struct {
	Run    string `json:"run"`
	Number int    `json:"number"`
	At     int64  `json:"at"`
}

func markOf(rs *runState) *RunMark {
	return &RunMark{Run: rs.run.ID, Number: rs.run.Number, At: rs.run.CreatedAt}
}

// repoHistory is a repository's runs in the order of their numbers, each
// with the latest attempt of each of its cells that ended with a verdict.
type repoHistory struct {
	runs  []*runState
	cells []map[string]*Attempt
	index map[string]int
}

// historyLocked reads a repository's history from the runs in memory.
func (r *Runner) historyLocked(repo string) *repoHistory {
	h := &repoHistory{index: map[string]int{}}
	for _, rs := range r.runs {
		if rs.run.Repo == repo {
			h.runs = append(h.runs, rs)
		}
	}
	sort.Slice(h.runs, func(i, j int) bool {
		a, b := h.runs[i].run, h.runs[j].run
		if a.Number != b.Number {
			return a.Number < b.Number
		}
		return a.CreatedAt < b.CreatedAt
	})
	for i, rs := range h.runs {
		h.index[rs.run.ID] = i
		m := map[string]*Attempt{}
		for _, ref := range rs.run.Order {
			a := rs.attempts[ref.ID]
			if a == nil || !Final(a.State) || a.Final() == "" {
				continue
			}
			if b := m[a.CellID]; b == nil || a.N > b.N {
				m[a.CellID] = a
			}
		}
		h.cells = append(h.cells, m)
	}
	return h
}

// prev is the latest run before the run at i with a verdict of the cell:
// its index and the attempt, -1 for none.
func (h *repoHistory) prev(i int, cell string) (int, *Attempt) {
	for j := i - 1; j >= 0; j-- {
		if a := h.cells[j][cell]; a != nil {
			return j, a
		}
	}
	return -1, nil
}

// since is the first run of the streak of problems the cell is in at the
// run at i; blocked runs are skipped, nil when it is in none there.
func (h *repoHistory) since(i int, cell string) *RunMark {
	if i < 0 || i >= len(h.runs) {
		return nil
	}
	first := -1
	for j := i; j >= 0; j-- {
		a := h.cells[j][cell]
		if a == nil || a.Final() == testtools.Blocked {
			if j == i {
				return nil
			}
			continue
		}
		if !problem(a.Final()) {
			break
		}
		first = j
	}
	if first < 0 {
		return nil
	}
	return markOf(h.runs[first])
}

// decorate puts a row's prev, change and since: the attempt a is of the run
// at i.
func (h *repoHistory) decorate(row *CellRow, i int, a *Attempt) {
	if i < 0 || i >= len(h.runs) {
		return
	}
	j, p := h.prev(i, a.CellID)
	if p != nil {
		row.Prev = &PrevVerdict{Verdict: p.Final(), Run: h.runs[j].run.ID, Number: h.runs[j].run.Number, Attempt: p.ID}
	}
	now := ""
	if Final(a.State) {
		now = a.Final()
	}
	if p == nil {
		row.Change = ChangeNew
	} else if now != "" {
		row.Change = changeOf(p.Final(), true, now)
	}
	if now != "" {
		row.Since = h.since(i, a.CellID)
	}
}

// changes counts the cells of the run at i that regressed and that were
// fixed against the run before.
func (h *repoHistory) changes(i int) *Changes {
	c := &Changes{}
	for cell, a := range h.cells[i] {
		_, p := h.prev(i, cell)
		if p == nil {
			continue
		}
		switch changeOf(p.Final(), true, a.Final()) {
		case ChangeRegressed:
			c.Regressed++
		case ChangeFixed:
			c.Fixed++
		}
	}
	return c
}

// CellHealth is how a cell stands over the repository's last runs.
type CellHealth struct {
	// Cell is the cell's key (pixel.es), CellID its id.
	Cell   string `json:"cell"`
	CellID string `json:"cell_id"`
	Dims   []Dim  `json:"dims,omitempty"`
	// Latest is its latest verdict, with the run and the attempt that gave
	// it.
	Latest  string `json:"latest,omitempty"`
	Run     string `json:"run,omitempty"`
	Number  int    `json:"number,omitempty"`
	Attempt string `json:"attempt,omitempty"`
	// Since is the first run of its current streak of problems.
	Since *RunMark `json:"since,omitempty"`
	// Runs is how many of the runs have a verdict of it, Passes how many of
	// them passed it, Flips how often it went between passed and issue or
	// fail (blocked runs skipped). Flaky is two flips or more.
	Runs   int  `json:"runs"`
	Passes int  `json:"passes"`
	Flips  int  `json:"flips"`
	Flaky  bool `json:"flaky,omitempty"`
}

// health is how a cell stands over the last healthRuns runs.
func (h *repoHistory) health(cell string) CellHealth {
	c := CellHealth{CellID: cell}
	from := max(0, len(h.runs)-healthRuns)
	last, side := -1, 0
	for j := from; j < len(h.runs); j++ {
		a := h.cells[j][cell]
		if a == nil {
			continue
		}
		c.Runs++
		v := a.Final()
		if passed(v) {
			c.Passes++
		}
		last = j
		c.Cell, c.Dims = a.Cell, a.Dims
		now := 0
		switch {
		case passed(v):
			now = 1
		case problem(v):
			now = 2
		}
		if now != 0 {
			if side != 0 && now != side {
				c.Flips++
			}
			side = now
		}
	}
	if last >= 0 {
		a := h.cells[last][cell]
		c.Latest, c.Run, c.Number, c.Attempt = a.Final(), h.runs[last].run.ID, h.runs[last].run.Number, a.ID
		c.Since = h.since(last, cell)
	}
	c.Flaky = c.Flips >= 2
	return c
}

// TestHealth is how a test stands: its cells' worst latest verdict, whether
// one of them is flaky, the oldest streak of problems among them and the
// latest run that ran it.
type TestHealth struct {
	Latest  string   `json:"latest,omitempty"`
	Flaky   bool     `json:"flaky,omitempty"`
	Since   *RunMark `json:"since,omitempty"`
	LastRun *RunMark `json:"last_run,omitempty"`
}

// testHealth is a test's health from its cells'; the cells of its latest
// run that ran it count for its latest verdict.
func (h *repoHistory) testHealth(cells []CellHealth) TestHealth {
	var t TestHealth
	lastNumber := 0
	for _, c := range cells {
		if c.Number > lastNumber {
			lastNumber = c.Number
		}
	}
	for _, c := range cells {
		if c.Flaky {
			t.Flaky = true
		}
		if c.Number == lastNumber && c.Latest != "" {
			t.Latest = testtools.Worse(t.Latest, c.Latest)
			t.LastRun = &RunMark{Run: c.Run, Number: c.Number}
		}
		if c.Since != nil && (t.Since == nil || c.Since.Number < t.Since.Number) {
			s := *c.Since
			t.Since = &s
		}
	}
	if t.LastRun != nil {
		if i, ok := h.index[t.LastRun.Run]; ok {
			t.LastRun.At = h.runs[i].run.CreatedAt
		}
	}
	return t
}

// fullViewLocked is the run at i as the page lists it, with its changes and
// times (its cost is added outside the lock: addCosts).
func (r *Runner) fullViewLocked(h *repoHistory, i int, full bool) RunView {
	rs := h.runs[i]
	v := r.runViewLocked(rs, full)
	v.Changes = h.changes(i)
	now := r.now().UnixMilli()
	if rs.run.StartedAt > 0 {
		end := rs.run.EndedAt
		if end == 0 && Active(rs.run.State) {
			end = now
		}
		if end > rs.run.StartedAt {
			v.DurationMS = end - rs.run.StartedAt
		}
	}
	if Active(rs.run.State) && rs.plan != nil {
		left := 0
		for _, a := range latestAttempts(rs) {
			if !Final(a.State) {
				left++
			}
		}
		if median := medianCellMS(h); median > 0 && left > 0 {
			v.EtaMS = int64(left) * median / int64(r.parallelLocked(rs.run.Repo, rs.run.Settings, r.runPoolsLocked(rs)))
		}
	}
	v.chats = runChats(rs)
	return v
}

// runChats is every session of a run: its cells', reviewers' and analyst's.
func runChats(rs *runState) []string {
	var out []string
	for _, a := range rs.attempts {
		if a.Chat != "" {
			out = append(out, a.Chat)
		}
	}
	for _, x := range append(append([]*Helper{}, rs.run.Reviews...), rs.run.Analysis) {
		if x != nil && x.Chat != "" {
			out = append(out, x.Chat)
		}
	}
	return out
}

// addCosts puts what each run's sessions cost, from the server's cost
// ledger; a run none of whose sessions the ledger knows has none.
func (r *Runner) addCosts(views []RunView) {
	var all []string
	for _, v := range views {
		all = append(all, v.chats...)
	}
	if len(all) == 0 {
		return
	}
	costs := r.host.SessionCosts(all)
	for i := range views {
		sum, known := 0.0, false
		for _, c := range views[i].chats {
			if x, ok := costs[c]; ok {
				sum, known = sum+x, true
			}
		}
		if known {
			views[i].Cost = &sum
		}
	}
}

// estimateRuns is how many of a repository's latest runs the estimates read.
const estimateRuns = 10

// medianCellMS is the median time a cell's attempt ran in the repository's
// last runs, 0 when none ran.
func medianCellMS(h *repoHistory) int64 {
	var list []int64
	for j := max(0, len(h.runs)-estimateRuns); j < len(h.runs); j++ {
		for _, a := range h.runs[j].attempts {
			if Final(a.State) && a.StartedAt > 0 && a.EndedAt > a.StartedAt {
				list = append(list, a.EndedAt-a.StartedAt)
			}
		}
	}
	return median(list)
}

func median(list []int64) int64 {
	if len(list) == 0 {
		return 0
	}
	sort.Slice(list, func(i, j int) bool { return list[i] < list[j] })
	n := len(list)
	if n%2 == 1 {
		return list[n/2]
	}
	return (list[n/2-1] + list[n/2]) / 2
}

// poolRef is a pool a run's cells lease from: a tool's, or a built-in MCP
// server's (mcp).
type poolRef struct {
	name string
	mcp  bool
	pool *catalog.Pool
}

// runPoolsLocked is the pools a run's cells lease from.
func (r *Runner) runPoolsLocked(rs *runState) []poolRef {
	var out []poolRef
	if rs.plan == nil {
		return nil
	}
	for name, t := range rs.plan.Tools {
		if t.Pool != nil {
			out = append(out, poolRef{name: name, pool: t.Pool})
		}
	}
	seen := map[string]bool{}
	for _, pa := range rs.plan.Areas {
		for _, name := range append(append([]string{}, pa.MCP...), rs.run.Settings.MCP...) {
			if !seen[name] && r.host.MCPPool(name) != nil {
				seen[name] = true
				out = append(out, poolRef{name: name, mcp: true})
			}
		}
	}
	return out
}

// poolSizeLocked is how many cells a pool carries at once, as the
// repository's runs know it: its members times their sessions from a run
// that has the secrets in memory, else how many members its runs leased; 0
// when unknown.
func (r *Runner) poolSizeLocked(repo string, p poolRef) int {
	seen := map[string]bool{}
	for _, rs := range r.runs {
		if rs.run.Repo != repo {
			continue
		}
		if !p.mcp && rs.plan != nil && rs.secrets != nil {
			if t := rs.plan.Tools[p.name]; t != nil && t.Pool != nil {
				if n := len(members(t.Pool, rs.secrets[p.name])); n > 0 {
					return n * max(1, t.Pool.PerMember)
				}
			}
		}
		for _, a := range rs.attempts {
			for _, l := range a.Leases {
				if l.Tool == p.name && l.MCP == p.mcp {
					seen[l.Account+"#"+strconv.Itoa(l.Member)] = true
				}
			}
		}
	}
	n := len(seen)
	if p.pool != nil {
		n *= max(1, p.pool.PerMember)
	}
	return n
}

// parallelLocked is how many cells of a run with these settings and pools
// run at once: its own limit, else its pools' capacity, else the default;
// never more than the server's limit.
func (r *Runner) parallelLocked(repo string, s Settings, pools []poolRef) int {
	p := s.Parallel
	if p <= 0 {
		for _, ref := range pools {
			if n := r.poolSizeLocked(repo, ref); n > 0 && (p <= 0 || n < p) {
				p = n
			}
		}
	}
	if p <= 0 {
		p = DefaultParallel
	}
	return max(1, min(p, r.global.MaxSessions))
}

// ---------------------------------------------------------------- overview

// TrendPoint is a run in the repository's trend.
type TrendPoint struct {
	ID        string `json:"id"`
	Number    int    `json:"number"`
	Name      string `json:"name,omitempty"`
	CreatedAt int64  `json:"created_at"`
	State     string `json:"state"`
	Counts    Counts `json:"counts"`
}

// FlakyTest is a test with a flaky cell.
type FlakyTest struct {
	Test  string   `json:"test"`
	Title string   `json:"title"`
	Area  string   `json:"area"`
	Cells []string `json:"cells"`
	Flips int      `json:"flips"`
}

// Overview is how a repository stands: its two latest runs that are over,
// its trend, what changed and what keeps failing, its flaky tests and its
// open bugs.
type Overview struct {
	Latest    *RunView     `json:"latest"`
	Previous  *RunView     `json:"previous"`
	Trend     []TrendPoint `json:"trend"`
	Regressed []CellRow    `json:"regressed"`
	Failing   []CellRow    `json:"failing"`
	Fixed     []CellRow    `json:"fixed"`
	Flaky     []FlakyTest  `json:"flaky"`
	Bugs      []BugView    `json:"bugs"`
}

// Overview is a repository's overview.
func (r *Runner) Overview(repo string) (*Overview, error) {
	r.mu.Lock()
	if r.repos[repo] == nil {
		r.mu.Unlock()
		return nil, ErrNotFound
	}
	h := r.historyLocked(repo)
	o := &Overview{Trend: []TrendPoint{}, Regressed: []CellRow{}, Failing: []CellRow{}, Fixed: []CellRow{},
		Flaky: []FlakyTest{}, Bugs: []BugView{}}
	var views []RunView
	latest := -1
	for i := len(h.runs) - 1; i >= 0 && len(views) < 2; i-- {
		if Active(h.runs[i].run.State) {
			continue
		}
		if latest < 0 {
			latest = i
		}
		views = append(views, r.fullViewLocked(h, i, false))
	}
	for i := max(0, len(h.runs)-healthRuns); i < len(h.runs); i++ {
		rs := h.runs[i]
		o.Trend = append(o.Trend, TrendPoint{ID: rs.run.ID, Number: rs.run.Number, Name: rs.run.Name,
			CreatedAt: rs.run.CreatedAt, State: rs.run.State, Counts: countsOf(rs)})
	}
	if latest >= 0 {
		rs := h.runs[latest]
		for _, a := range latestAttempts(rs) {
			if !Final(a.State) || a.Final() == "" {
				continue
			}
			row := r.cellRowLocked(h, latest, rs, a)
			switch {
			case row.Change == ChangeRegressed:
				o.Regressed = append(o.Regressed, row)
			case row.Change == ChangeFixed:
				o.Fixed = append(o.Fixed, row)
			}
			if problem(row.Final) {
				o.Failing = append(o.Failing, row)
			}
		}
	}
	o.Flaky = h.flakyTests()
	for _, b := range r.bugViewsLocked(repo, BugOpen) {
		if len(o.Bugs) == 10 {
			break
		}
		o.Bugs = append(o.Bugs, b)
	}
	r.mu.Unlock()
	r.addCosts(views)
	if len(views) > 0 {
		o.Latest = &views[0]
	}
	if len(views) > 1 {
		o.Previous = &views[1]
	}
	return o, nil
}

// flakyTests is the tests with a flaky cell over the last runs, by test.
func (h *repoHistory) flakyTests() []FlakyTest {
	cells := map[string]*Attempt{}
	for j := max(0, len(h.runs)-healthRuns); j < len(h.runs); j++ {
		for id, a := range h.cells[j] {
			cells[id] = a
		}
	}
	byTest := map[string]*FlakyTest{}
	for id, a := range cells {
		c := h.health(id)
		if !c.Flaky {
			continue
		}
		f := byTest[a.Test]
		if f == nil {
			f = &FlakyTest{Test: a.Test, Title: a.Title, Area: a.Area}
			byTest[a.Test] = f
		}
		f.Cells = append(f.Cells, id)
		f.Flips += c.Flips
	}
	out := []FlakyTest{}
	for _, f := range byTest {
		sort.Strings(f.Cells)
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Flips != out[j].Flips {
			return out[i].Flips > out[j].Flips
		}
		return out[i].Test < out[j].Test
	})
	return out
}

// ------------------------------------------------------------------ a test

// TestRunRef is a run that ran a test.
type TestRunRef struct {
	ID        string `json:"id"`
	Number    int    `json:"number"`
	Name      string `json:"name,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

// GridCell is a cell's result in one run.
type GridCell struct {
	Verdict string `json:"verdict,omitempty"`
	State   string `json:"state"`
	Attempt string `json:"attempt"`
}

// TestView is a test with its history: its catalog entry (nil once it is
// gone from the catalog), its cells' health, the last runs that ran it and
// a grid of its cells over them, and the bugs that name one of its cells.
type TestView struct {
	Test  *CatalogTest                   `json:"test"`
	Cells []CellHealth                   `json:"cells"`
	Runs  []TestRunRef                   `json:"runs"`
	Grid  map[string]map[string]GridCell `json:"grid"`
	Bugs  []BugView                      `json:"bugs"`
}

// TestDetail is a test of a repository with its history.
func (r *Runner) TestDetail(repo, test string) (*TestView, error) {
	rp := r.Repo(repo)
	if rp == nil {
		return nil, ErrNotFound
	}
	v := &TestView{Cells: []CellHealth{}, Runs: []TestRunRef{}, Grid: map[string]map[string]GridCell{}, Bugs: []BugView{}}
	var keys []catalog.Cell
	if cat, err := catalog.Load(rp.Path); err == nil {
		if t := cat.Test(test); t != nil {
			ct := catalogTest(t)
			v.Test = &ct
			keys = cat.CellsOf(t)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	h := r.historyLocked(repo)
	// The cells: the catalog's as it is now, then any its runs had that it
	// no longer has.
	seen := map[string]bool{}
	var cells []string
	for _, c := range keys {
		seen[c.ID()] = true
		cells = append(cells, c.ID())
	}
	var ran []int
	for i := len(h.runs) - 1; i >= 0 && len(ran) < healthRuns; i-- {
		hit := false
		for _, a := range h.runs[i].attempts {
			if a.Test != test {
				continue
			}
			hit = true
			if !seen[a.CellID] {
				seen[a.CellID] = true
				cells = append(cells, a.CellID)
			}
		}
		if hit {
			ran = append(ran, i)
		}
	}
	if v.Test == nil && len(ran) == 0 {
		return nil, ErrNotFound
	}
	for _, i := range ran {
		rs := h.runs[i]
		v.Runs = append(v.Runs, TestRunRef{ID: rs.run.ID, Number: rs.run.Number, Name: rs.run.Name, CreatedAt: rs.run.CreatedAt})
		for _, a := range latestAttempts(rs) {
			if a.Test != test {
				continue
			}
			if v.Grid[a.CellID] == nil {
				v.Grid[a.CellID] = map[string]GridCell{}
			}
			g := GridCell{State: a.State, Attempt: a.ID}
			if Final(a.State) {
				g.Verdict = a.Final()
			}
			v.Grid[a.CellID][rs.run.ID] = g
		}
	}
	for i, id := range cells {
		c := h.health(id)
		if i < len(keys) {
			c.Cell, c.Dims = keys[i].Key, dimsOf(keys[i])
		}
		v.Cells = append(v.Cells, c)
	}
	for _, b := range r.bugViewsLocked(repo, "") {
		for _, c := range b.Cells {
			if seen[c] {
				v.Bugs = append(v.Bugs, b)
				break
			}
		}
	}
	return v, nil
}

func dimsOf(c catalog.Cell) []Dim {
	var out []Dim
	for _, d := range c.Dims {
		out = append(out, Dim{Name: d.Name, ID: d.ID, Label: d.Label})
	}
	return out
}
