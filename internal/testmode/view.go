package testmode

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/agent/testtools"
	"github.com/saschazesiger/SocratesAgent/internal/testmode/catalog"
)

// What the Tests page reads: the views of repositories, runs and cells, a
// cell's record, the history of a test and the search over every run.

// Counts is how a run's cells stand: by verdict (as it counts, reviewed
// where reviewed) for the ended ones, by state for the others.
type Counts struct {
	Cells    int            `json:"cells"`
	Done     int            `json:"done"`
	Verdicts map[string]int `json:"verdicts"`
	States   map[string]int `json:"states"`
	Reviews  int            `json:"reviews"` // waiting or running
}

func countsOf(rs *runState) Counts {
	c := Counts{Verdicts: map[string]int{}, States: map[string]int{}}
	for _, a := range latestAttempts(rs) {
		c.Cells++
		if Final(a.State) {
			c.Done++
			if v := a.Final(); v != "" {
				c.Verdicts[v]++
			} else {
				c.States[a.State]++
			}
		} else {
			c.States[a.State]++
		}
		if a.Review != nil && (a.Review.State == ReviewPending || a.Review.State == ReviewRunning) {
			c.Reviews++
		}
	}
	return c
}

// RunView is a run as the page lists it.
type RunView struct {
	*Run
	Counts Counts `json:"counts"`
	Areas  []Area `json:"areas,omitempty"`
	Pools  []Pool `json:"pools,omitempty"`
	Bugs   int    `json:"bugs"`
}

// Area is an area of a run's plan: what the table is laid out by.
type Area struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Columns string    `json:"columns,omitempty"`
	Dims    []planDim `json:"dims,omitempty"`
}

// Pool is a run's pool with who holds which member.
type Pool struct {
	Tool    string       `json:"tool"`
	Title   string       `json:"title,omitempty"`
	Members []PoolMember `json:"members"`
}

// PoolMember is one member of a pool.
type PoolMember struct {
	N       int      `json:"n"`
	Label   string   `json:"label"`
	Holders []string `json:"holders,omitempty"` // cell ids
	Busy    bool     `json:"busy,omitempty"`    // busy outside Socrates
}

func (r *Runner) runViewLocked(rs *runState, full bool) RunView {
	run := *rs.run
	v := RunView{Run: &run, Counts: countsOf(rs)}
	if rs.plan != nil && full {
		for _, id := range rs.plan.AreaOrder {
			pa := rs.plan.Areas[id]
			v.Areas = append(v.Areas, Area{ID: pa.ID, Title: pa.Title, Columns: pa.Columns, Dims: pa.Dims})
		}
		held := r.heldLocked(rs.run.Repo)
		holders := map[string]map[int][]string{}
		for _, other := range r.runs {
			if other.run.Repo != rs.run.Repo {
				continue
			}
			for _, a := range other.attempts {
				if a.State != AttemptStarting && a.State != AttemptRunning && !other.ending[a.ID] {
					continue
				}
				for _, l := range a.Leases {
					if holders[l.Tool] == nil {
						holders[l.Tool] = map[int][]string{}
					}
					holders[l.Tool][l.Member] = append(holders[l.Tool][l.Member], a.CellID)
				}
			}
		}
		names := make([]string, 0, len(rs.plan.Tools))
		for name := range rs.plan.Tools {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			t := rs.plan.Tools[name]
			if t.Pool == nil || rs.secrets == nil {
				continue
			}
			p := Pool{Tool: name, Title: t.Title}
			busy := r.busy[rs.run.Repo+"/"+name].busy
			for i, m := range members(t.Pool, rs.secrets[name]) {
				n := i + 1
				p.Members = append(p.Members, PoolMember{N: n, Label: memberLabel(m, n), Holders: holders[name][n], Busy: busy[n] && held[name][n] == 0})
			}
			v.Pools = append(v.Pools, p)
		}
	}
	if raw, err := os.ReadFile(filepath.Join(r.layout.RunDir(rs.run.Repo, rs.run.ID), "bugs.json")); err == nil {
		var bugs []testtools.Bug
		if jsonUnmarshal(raw, &bugs) == nil {
			v.Bugs = len(bugs)
		}
	}
	return v
}

// publishLocked tells every page how a run stands.
func (r *Runner) publishLocked(rs *runState) {
	v := r.runViewLocked(rs, false)
	r.host.Publish(map[string]any{"t": "tests", "repo": rs.run.Repo, "run": rs.run.ID, "state": rs.run.State,
		"counts": v.Counts, "name": rs.run.Name, "created_at": rs.run.CreatedAt})
}

// RepoView is a repository as the page lists it.
type RepoView struct {
	Repo
	Title  string   `json:"title"`
	Active *RunView `json:"active,omitempty"`
	Last   *RunView `json:"last,omitempty"`
	Runs   int      `json:"runs"`
}

// Repos is every repository on the list, by title.
func (r *Runner) Repos() []RepoView {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []RepoView
	for _, repo := range r.repos {
		if repo.Hidden {
			continue
		}
		v := RepoView{Repo: *repo, Title: filepath.Base(repo.Path)}
		var newest *runState
		for _, rs := range r.runs {
			if rs.run.Repo != repo.Key {
				continue
			}
			v.Runs++
			if rs.plan != nil && rs.plan.Title != "" {
				v.Title = rs.plan.Title
			}
			if Active(rs.run.State) && rs.run.State != RunWaiting {
				rv := r.runViewLocked(rs, false)
				v.Active = &rv
			}
			if newest == nil || rs.run.CreatedAt > newest.run.CreatedAt {
				newest = rs
			}
		}
		if newest != nil {
			rv := r.runViewLocked(newest, false)
			v.Last = &rv
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title) })
	return out
}

// Active is every run that is not over, for Home.
func (r *Runner) Active() []RunView {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []RunView
	for _, rs := range r.sortedRunsLocked() {
		if Active(rs.run.State) {
			out = append(out, r.runViewLocked(rs, false))
		}
	}
	return out
}

// Runs is a repository's runs, newest first.
func (r *Runner) Runs(repo string) []RunView {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []RunView
	for _, rs := range r.runs {
		if rs.run.Repo == repo {
			out = append(out, r.runViewLocked(rs, false))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

// RunDetail is a run with its areas and pools.
func (r *Runner) RunDetail(id string) (RunView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rs := r.runs[id]
	if rs == nil {
		return RunView{}, ErrNotFound
	}
	return r.runViewLocked(rs, true), nil
}

// CellRow is the latest attempt of a cell, as a row of the results table.
type CellRow struct {
	ID        string   `json:"id"` // attempt
	Test      string   `json:"test"`
	Area      string   `json:"area"`
	Title     string   `json:"title"`
	Priority  string   `json:"priority"`
	Tags      []string `json:"tags,omitempty"`
	Cell      string   `json:"cell"`
	CellID    string   `json:"cell_id"`
	Dims      []Dim    `json:"dims,omitempty"`
	N         int      `json:"attempt"`
	State     string   `json:"state"`
	Wait      string   `json:"wait,omitempty"`
	Verdict   string   `json:"verdict,omitempty"`
	Reviewed  string   `json:"reviewed,omitempty"`
	Final     string   `json:"final,omitempty"`
	Summary   string   `json:"summary,omitempty"`
	EndReason string   `json:"end_reason,omitempty"`
	Review    string   `json:"review,omitempty"`
	Bugs      []string `json:"bugs,omitempty"`
	StartedAt int64    `json:"started_at,omitempty"`
	EndedAt   int64    `json:"ended_at,omitempty"`
	// Shots is how many screenshots its evidence has.
	Shots int `json:"shots"`
}

func rowOf(a *Attempt) CellRow {
	row := CellRow{ID: a.ID, Test: a.Test, Area: a.Area, Title: a.Title, Priority: a.Priority, Tags: a.Tags, Cell: a.Cell,
		CellID: a.CellID, Dims: a.Dims, N: a.N, State: a.State, Wait: a.Wait, Verdict: a.Verdict, Reviewed: a.Reviewed,
		Final: a.Final(), Summary: a.Summary, EndReason: a.EndReason, Bugs: a.Bugs, StartedAt: a.StartedAt, EndedAt: a.EndedAt}
	if a.Review != nil {
		row.Review = a.Review.State
	}
	for _, act := range a.Actions {
		if act.Screenshot != "" {
			row.Shots++
		}
	}
	return row
}

// Cells is the latest attempt of every cell of a run, in the run's order.
func (r *Runner) Cells(id string) ([]CellRow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rs := r.runs[id]
	if rs == nil {
		return nil, ErrNotFound
	}
	out := []CellRow{}
	for _, a := range latestAttempts(rs) {
		out = append(out, rowOf(a))
	}
	return out, nil
}

// AttemptView is an attempt in full, with the other attempts of its cell.
type AttemptView struct {
	*Attempt
	Final    string    `json:"final"`
	Attempts []CellRow `json:"attempts"`
	// Files is the evidence's other files (logs).
	Files []string `json:"files,omitempty"`
}

// AttemptDetail is one attempt of a run.
func (r *Runner) AttemptDetail(runID, attemptID string) (*AttemptView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rs := r.runs[runID]
	if rs == nil {
		return nil, ErrNotFound
	}
	a := rs.attempts[attemptID]
	if a == nil {
		return nil, ErrNotFound
	}
	// What the tools wrote while it runs is shown as it comes.
	if a.State == AttemptRunning {
		if n, _ := ingest(a, r.layout.EvidenceDir(a), rs.red); n > 0 {
			r.saveAttemptLocked(a)
		}
	}
	c := *a
	v := &AttemptView{Attempt: &c, Final: a.Final()}
	for _, ref := range rs.run.Order {
		if x := rs.attempts[ref.ID]; x != nil && x.CellID == a.CellID {
			v.Attempts = append(v.Attempts, rowOf(x))
		}
	}
	if entries, err := os.ReadDir(filepath.Join(r.layout.EvidenceDir(a), "logs")); err == nil {
		for _, e := range entries {
			if e.Type().IsRegular() {
				v.Files = append(v.Files, "logs/"+e.Name())
			}
		}
	}
	return v, nil
}

// EvidencePath is the path of a file of an attempt's evidence, and how a
// text file of it is to be redacted when it is served: one written while
// the cell runs is not redacted on disk yet (redactEvidence).
func (r *Runner) EvidencePath(runID, attemptID, name string) (string, func([]byte) []byte, error) {
	r.mu.Lock()
	rs := r.runs[runID]
	var a *Attempt
	var red *Redactor
	if rs != nil {
		a, red = rs.attempts[attemptID], rs.red
	}
	r.mu.Unlock()
	if a == nil {
		return "", nil, ErrNotFound
	}
	path, err := EvidenceFile(r.layout.EvidenceDir(a), name)
	if err != nil {
		return "", nil, err
	}
	redact := func(raw []byte) []byte { return raw }
	if red != nil && redactable(name) {
		redact = func(raw []byte) []byte { return []byte(red.String(string(raw))) }
	}
	return path, redact, nil
}

// Bugs is a run's bug list and conclusions.
func (r *Runner) Bugs(runID string) ([]testtools.Bug, string, error) {
	r.mu.Lock()
	rs := r.runs[runID]
	r.mu.Unlock()
	if rs == nil {
		return nil, "", ErrNotFound
	}
	dir := r.layout.RunDir(rs.run.Repo, rs.run.ID)
	bugs := []testtools.Bug{}
	if raw, err := os.ReadFile(filepath.Join(dir, "bugs.json")); err == nil {
		_ = jsonUnmarshal(raw, &bugs)
	}
	md, _ := os.ReadFile(filepath.Join(dir, "conclusions.md"))
	return bugs, string(md), nil
}

// HistoryPoint is how a test or cell stood in one run.
type HistoryPoint struct {
	Run       string `json:"run"`
	CreatedAt int64  `json:"created_at"`
	Verdict   string `json:"verdict"`
	Attempt   string `json:"attempt,omitempty"`
}

// History is, per test of a repository, its worst verdict (over its cells)
// in each of the last limit runs that ran it, newest first; with cell set,
// only that cell's.
func (r *Runner) History(repo string, limit int, cell string) map[string][]HistoryPoint {
	r.mu.Lock()
	defer r.mu.Unlock()
	var runs []*runState
	for _, rs := range r.runs {
		if rs.run.Repo == repo && rs.plan != nil {
			runs = append(runs, rs)
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].run.CreatedAt > runs[j].run.CreatedAt })
	out := map[string][]HistoryPoint{}
	for _, rs := range runs {
		worst := map[string]string{}
		attempt := map[string]string{}
		for _, a := range latestAttempts(rs) {
			if cell != "" && a.CellID != cell {
				continue
			}
			v := a.Final()
			if !Final(a.State) {
				v = a.State
			} else if v == "" {
				continue
			}
			key := a.Test
			if cell != "" {
				key = a.CellID
			}
			if prev, ok := worst[key]; !ok || testtools.Rank(v) < testtools.Rank(prev) {
				worst[key], attempt[key] = v, a.ID
			}
		}
		for key, v := range worst {
			if len(out[key]) < limit {
				out[key] = append(out[key], HistoryPoint{Run: rs.run.ID, CreatedAt: rs.run.CreatedAt, Verdict: v, Attempt: attempt[key]})
			}
		}
	}
	return out
}

// Hit is a cell that matched a search.
type Hit struct {
	CellRow
	Run       string `json:"run"`
	CreatedAt int64  `json:"created_at"`
	Snippet   string `json:"snippet"`
}

// Search finds the attempts of a repository's runs whose test, title,
// observations, summary, review notes or bugs hold every word of q, newest
// run first, at most limit.
func (r *Runner) Search(repo, q string, limit int) []Hit {
	words := strings.Fields(strings.ToLower(q))
	if len(words) == 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var runs []*runState
	for _, rs := range r.runs {
		if rs.run.Repo == repo {
			runs = append(runs, rs)
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].run.CreatedAt > runs[j].run.CreatedAt })
	var out []Hit
	for _, rs := range runs {
		bugTitles := map[string]string{}
		if raw, err := os.ReadFile(filepath.Join(r.layout.RunDir(rs.run.Repo, rs.run.ID), "bugs.json")); err == nil {
			var bugs []testtools.Bug
			if jsonUnmarshal(raw, &bugs) == nil {
				for _, b := range bugs {
					bugTitles[b.ID] = b.Title
				}
			}
		}
		for _, ref := range rs.run.Order {
			a := rs.attempts[ref.ID]
			if a == nil {
				continue
			}
			var texts []string
			texts = append(texts, a.CellID, a.Title, a.Summary, a.EndReason, strings.Join(a.Tags, " "))
			for _, s := range a.Steps {
				texts = append(texts, s.Observed)
				if s.Review != nil {
					texts = append(texts, s.Review.Note)
				}
			}
			if a.Review != nil {
				texts = append(texts, a.Review.Note)
			}
			for _, b := range a.Bugs {
				texts = append(texts, b+" "+bugTitles[b])
			}
			all := strings.ToLower(strings.Join(texts, "\n"))
			match := true
			for _, w := range words {
				if !strings.Contains(all, w) {
					match = false
					break
				}
			}
			if !match {
				continue
			}
			out = append(out, Hit{CellRow: rowOf(a), Run: rs.run.ID, CreatedAt: rs.run.CreatedAt, Snippet: snippet(texts, words[0])})
			if len(out) >= limit {
				return out
			}
		}
	}
	return out
}

func snippet(texts []string, word string) string {
	for _, t := range texts {
		i := strings.Index(strings.ToLower(t), word)
		if i < 0 {
			continue
		}
		start := max(0, i-60)
		end := min(len(t), i+len(word)+100)
		for start > 0 && !utf8Start(t[start]) {
			start--
		}
		for end < len(t) && !utf8Start(t[end]) {
			end++
		}
		s := strings.TrimSpace(t[start:end])
		if start > 0 {
			s = "…" + s
		}
		if end < len(t) {
			s += "…"
		}
		return s
	}
	return ""
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// CatalogView is a repository's catalog as it is now.
type CatalogView struct {
	Title    string            `json:"title"`
	Errors   []catalog.Problem `json:"errors"`
	Warnings []catalog.Problem `json:"warnings"`
	Areas    []CatalogArea     `json:"areas"`
	Tools    []CatalogTool     `json:"tools"`
	Cells    int               `json:"cells"`
}

// CatalogArea is an area of the catalog.
type CatalogArea struct {
	ID         string        `json:"id"`
	Title      string        `json:"title"`
	Tools      []string      `json:"tools,omitempty"`
	Columns    string        `json:"columns,omitempty"`
	Dims       []planDim     `json:"dims,omitempty"`
	Tests      []CatalogTest `json:"tests"`
	Body       string        `json:"body"`
	Evidence   string        `json:"evidence"`
	TimeoutMin int           `json:"timeout_min"`
}

// CatalogTest is a test of the catalog.
type CatalogTest struct {
	ID       string         `json:"id"`
	Title    string         `json:"title"`
	Priority string         `json:"priority"`
	Tags     []string       `json:"tags,omitempty"`
	Tools    []string       `json:"tools,omitempty"`
	Steps    []catalog.Step `json:"steps"`
	Context  string         `json:"context,omitempty"`
	Cells    []string       `json:"cells"`
}

// CatalogTool is a tool of the catalog.
type CatalogTool struct {
	Name     string   `json:"name"`
	Title    string   `json:"title"`
	Command  string   `json:"secrets_command,omitempty"`
	Names    []string `json:"secrets,omitempty"`
	Pool     string   `json:"pool,omitempty"`
	Setup    string   `json:"setup,omitempty"`
	End      string   `json:"end,omitempty"`
	Body     string   `json:"body"`
	Optional []string `json:"optional,omitempty"`
}

// Catalog reads a repository's catalog now.
func (r *Runner) Catalog(key string) (*CatalogView, error) {
	repo := r.Repo(key)
	if repo == nil {
		return nil, ErrNotFound
	}
	cat, err := catalog.Load(repo.Path)
	if err != nil {
		return nil, err
	}
	v := &CatalogView{Title: cat.Title, Errors: cat.Errors, Warnings: cat.Warnings}
	if v.Errors == nil {
		v.Errors = []catalog.Problem{}
	}
	if v.Warnings == nil {
		v.Warnings = []catalog.Problem{}
	}
	for _, area := range cat.Areas {
		ca := CatalogArea{ID: area.ID, Title: area.Title, Tools: area.Tools, Columns: area.Columns, Body: area.Instructions,
			Evidence: area.Evidence, TimeoutMin: int(area.Timeout.Minutes())}
		for _, d := range area.Dimensions {
			ca.Dims = append(ca.Dims, planDim{Name: d.Name, Values: d.Values, Extra: d.Extra})
		}
		for _, t := range area.Tests {
			ct := CatalogTest{ID: t.ID, Title: t.Title, Priority: t.Priority, Tags: t.Tags, Tools: t.Tools, Steps: t.Steps, Context: t.Context}
			for _, c := range cat.CellsOf(t) {
				ct.Cells = append(ct.Cells, c.Key)
				v.Cells++
			}
			ca.Tests = append(ca.Tests, ct)
		}
		v.Areas = append(v.Areas, ca)
	}
	for _, name := range cat.ToolNames() {
		t := cat.Tools[name]
		ct := CatalogTool{Name: name, Title: t.Title, Command: t.Secrets.Command, Setup: t.Setup, End: t.End, Body: t.Instructions}
		for _, n := range t.Secrets.Names {
			ct.Names = append(ct.Names, n.Name)
			if n.Optional {
				ct.Optional = append(ct.Optional, n.Name)
			}
		}
		if t.Pool != nil {
			ct.Pool = t.Pool.Secret
		}
		v.Tools = append(v.Tools, ct)
	}
	return v, nil
}

// SecretsCheck is what a tool's secrets command gave: the names, never the
// values, and the pool's members by their labels.
type SecretsCheck struct {
	Found   []string `json:"found"`
	Missing []string `json:"missing"`
	Members []string `json:"members,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// CheckSecrets runs a tool's secrets command and says what it gave.
func (r *Runner) CheckSecrets(key, tool string) (*SecretsCheck, error) {
	repo := r.Repo(key)
	if repo == nil {
		return nil, ErrNotFound
	}
	cat, err := catalog.Load(repo.Path)
	if err != nil {
		return nil, err
	}
	t := cat.Tools[tool]
	if t == nil {
		return nil, ErrNotFound
	}
	out := &SecretsCheck{Found: []string{}, Missing: []string{}}
	loose := *t
	loose.Secrets.Names = nil
	for _, n := range t.Secrets.Names {
		loose.Secrets.Names = append(loose.Secrets.Names, catalog.SecretName{Name: n.Name, Optional: true})
	}
	got, err := readSecrets(r.ctx, repo.Path, []*catalog.Tool{&loose})
	if err != nil {
		out.Error = NewRedactor(nil).String(err.Error())
		return out, nil
	}
	for _, n := range t.Secrets.Names {
		if got[tool][n.Name] != "" {
			out.Found = append(out.Found, n.Name)
		} else if !n.Optional {
			out.Missing = append(out.Missing, n.Name)
		}
	}
	for i, m := range members(t.Pool, got[tool]) {
		out.Members = append(out.Members, memberLabel(m, i+1))
	}
	return out, nil
}

var errNoRepo = errors.New("no such repository")
