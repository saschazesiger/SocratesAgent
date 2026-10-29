package testmode

import (
	"slices"
	"sort"

	"github.com/saschazesiger/SocratesAgent/internal/testmode/catalog"
)

// Estimate is what a selection would run: its cells and tests, about how
// long they take (absent without an earlier run to measure) and what they
// cost (absent unless earlier runs' costs are known), and why it could not
// run.
type Estimate struct {
	Cells  int               `json:"cells"`
	Tests  int               `json:"tests"`
	EtaMS  int64             `json:"eta_ms,omitempty"`
	Cost   *float64          `json:"cost,omitempty"`
	Errors []catalog.Problem `json:"errors"`
}

// Estimate expands a selection of a repository's catalog as a run of it
// would (selectCells, as prepare does) and says how long and what it would
// cost: the median time and cost of a cell in the repository's last runs,
// the time divided by how many cells would run at once.
func (r *Runner) Estimate(repoKey string, sel Selection, settings *Settings) (*Estimate, error) {
	repo := r.Repo(repoKey)
	if repo == nil {
		return nil, ErrNotFound
	}
	e := &Estimate{Errors: []catalog.Problem{}}
	r.mu.Lock()
	s := r.settingsLocked(repoKey)
	if settings != nil {
		s = *settings
	}
	s.Normalize()
	sel, err := r.resolveSelectionLocked(repoKey, sel)
	var problems map[string]bool
	if err == nil {
		problems = r.problemsLocked(sel)
	}
	r.mu.Unlock()
	if err != nil {
		e.Errors = append(e.Errors, catalog.Problem{Message: err.Error()})
		return e, nil
	}
	cat, err := catalog.Load(repo.Path)
	if err != nil {
		return nil, err
	}
	e.Errors = append(e.Errors, cat.Errors...)
	cells, err := selectCells(cat, sel, problems)
	if err != nil {
		if len(cat.Errors) == 0 {
			e.Errors = append(e.Errors, catalog.Problem{Message: err.Error()})
		}
		return e, nil
	}
	e.Cells = len(cells)
	tests := map[string]bool{}
	var pools []poolRef
	seen := map[string]bool{}
	for _, c := range cells {
		tests[c.Test.ID] = true
		for _, t := range cat.ToolsOf(c.Test) {
			if t.Pool != nil && !seen[t.Name] {
				seen[t.Name] = true
				pools = append(pools, poolRef{name: t.Name, pool: t.Pool})
			}
		}
		if area := cat.Area(c.Test.Area); area != nil {
			for _, name := range append(slices.Clone(area.MCP), s.MCP...) {
				if !seen["mcp:"+name] && r.host.MCPPool(name) != nil {
					seen["mcp:"+name] = true
					pools = append(pools, poolRef{name: name, mcp: true})
				}
			}
		}
	}
	e.Tests = len(tests)

	type sample struct {
		chats []string
		cells int
	}
	var samples []sample
	r.mu.Lock()
	h := r.historyLocked(repoKey)
	if m := medianCellMS(h); m > 0 {
		e.EtaMS = int64(e.Cells) * m / int64(r.parallelLocked(repoKey, s, pools))
	}
	for j := len(h.runs) - 1; j >= 0 && len(samples) < estimateRuns; j-- {
		rs := h.runs[j]
		if Active(rs.run.State) {
			continue
		}
		if n := len(latestAttempts(rs)); n > 0 {
			samples = append(samples, sample{chats: runChats(rs), cells: n})
		}
	}
	r.mu.Unlock()

	// The cost of a cell: each earlier run's cost by its cells, the median.
	var all []string
	for _, x := range samples {
		all = append(all, x.chats...)
	}
	if len(all) == 0 {
		return e, nil
	}
	costs := r.host.SessionCosts(all)
	var per []float64
	for _, x := range samples {
		sum, known := 0.0, false
		for _, c := range x.chats {
			if v, ok := costs[c]; ok {
				sum, known = sum+v, true
			}
		}
		if known {
			per = append(per, sum/float64(x.cells))
		}
	}
	if len(per) > 0 {
		sort.Float64s(per)
		m := per[len(per)/2]
		if len(per)%2 == 0 {
			m = (per[len(per)/2-1] + per[len(per)/2]) / 2
		}
		c := m * float64(e.Cells)
		e.Cost = &c
	}
	return e, nil
}
