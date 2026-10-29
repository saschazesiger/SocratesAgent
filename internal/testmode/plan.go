package testmode

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent/testtools"
	"github.com/saschazesiger/SocratesAgent/internal/testmode/catalog"
)

// MaxInstructions is the most a test session's instructions (the catalog's
// texts) may be: Claude Code takes its whole system prompt as one argument,
// which Linux limits to 128 KiB, and the model's instructions and Socrates'
// own guidance need room beside them (TEST-MODE.md).
const MaxInstructions = 64 << 10

// plan is what a run took from the catalog when it was prepared, kept as
// plan.json: everything its sessions are started with, so that a session
// started later, or again after a restart, gets what the run started with
// whatever the catalog says now.
type plan struct {
	Title        string               `json:"title"`
	Instructions string               `json:"instructions"`
	AreaOrder    []string             `json:"area_order"`
	Areas        map[string]*planArea `json:"areas"`
	Tools        map[string]*planTool `json:"tools"`
}

type planArea struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Instructions string    `json:"instructions"`
	Columns      string    `json:"columns,omitempty"`
	Dims         []planDim `json:"dims,omitempty"`
	Evidence     string    `json:"evidence"`
	MCP          []string  `json:"mcp,omitempty"`
	// Prompt is the cell agents' instructions of the area: TESTS.md,
	// AREA.md and the instructions of every tool its tests use.
	Prompt string `json:"prompt"`
}

type planDim struct {
	Name   string          `json:"name"`
	Values []catalog.Value `json:"values"`
	Extra  []catalog.Value `json:"extra,omitempty"`
}

type planTool struct {
	Name         string        `json:"name"`
	Title        string        `json:"title,omitempty"`
	Dir          string        `json:"dir"` // the installed copy
	Bin          string        `json:"bin"`
	End          string        `json:"end,omitempty"`
	Instructions string        `json:"-"`
	Pool         *catalog.Pool `json:"pool,omitempty"`
}

func (l Layout) planPath(repo, run string) string {
	return filepath.Join(l.RunDir(repo, run), "plan.json")
}

// gitState is the repository's HEAD and whether its working tree has
// changes; empty for a folder that is no git checkout.
func gitState(ctx context.Context, dir string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	head, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", false
	}
	status, _ := exec.CommandContext(ctx, "git", "-C", dir, "status", "--porcelain", "--untracked-files=no").Output()
	return strings.TrimSpace(string(head)), len(strings.TrimSpace(string(status))) > 0
}

// selected is whether the selection takes a cell.
func (s Selection) selected(c catalog.Cell, problems map[string]bool) bool {
	t := c.Test
	if len(s.Areas) > 0 && !slices.Contains(s.Areas, t.Area) {
		return false
	}
	if len(s.Tests) > 0 && !slices.Contains(s.Tests, t.ID) {
		return false
	}
	if len(s.Priorities) > 0 && !slices.Contains(s.Priorities, t.Priority) {
		return false
	}
	if len(s.Tags) > 0 {
		hit := false
		for _, tag := range t.Tags {
			if slices.Contains(s.Tags, tag) {
				hit = true
			}
		}
		if !hit {
			return false
		}
	}
	for _, d := range c.Dims {
		if want := s.Dims[d.Name]; len(want) > 0 && !slices.Contains(want, d.ID) {
			return false
		}
	}
	if problems != nil && !problems[c.ID()] {
		return false
	}
	return true
}

// cellPrompt is an area's instructions for its cell agents.
func cellPrompt(cat *catalog.Catalog, area *catalog.Area, tools []*planTool) string {
	var b strings.Builder
	b.WriteString("# The tests of this repository")
	if cat.Title != "" {
		b.WriteString(" (" + cat.Title + ")")
	}
	b.WriteString("\n\n" + strings.TrimSpace(cat.Instructions) + "\n\n")
	b.WriteString("# The area " + area.ID)
	if area.Title != "" {
		b.WriteString(": " + area.Title)
	}
	b.WriteString("\n\n" + strings.TrimSpace(area.Instructions) + "\n")
	// The MCP servers built into Socrates the area names: their tools are
	// the agent's, their instructions part of the prompt.
	for _, name := range area.MCP {
		if catalog.BuiltinMCP == nil {
			break
		}
		if b2 := catalog.BuiltinMCP(name); b2 != nil {
			b.WriteString("\n# The MCP server " + name + "\n\n" + strings.TrimSpace(b2.Instructions) + "\n")
		}
	}
	for _, t := range tools {
		b.WriteString("\n# The tool " + t.Name)
		if t.Title != "" {
			b.WriteString(": " + t.Title)
		}
		b.WriteString("\n\nIts commands are on your PATH (" + filepath.Join(t.Dir, t.Bin) + ").\n\n")
		b.WriteString(strings.TrimSpace(t.Instructions) + "\n")
	}
	return clip(b.String(), MaxInstructions)
}

// overviewPrompt is the instructions of a reviewer or the analyst: the
// repository's and every area's texts, without the tools'.
func (p *plan) overviewPrompt() string {
	var b strings.Builder
	b.WriteString("# The tests of this repository")
	if p.Title != "" {
		b.WriteString(" (" + p.Title + ")")
	}
	b.WriteString("\n\n" + strings.TrimSpace(p.Instructions) + "\n")
	for _, id := range p.AreaOrder {
		a := p.Areas[id]
		b.WriteString("\n# The area " + a.ID)
		if a.Title != "" {
			b.WriteString(": " + a.Title)
		}
		b.WriteString("\n\n" + strings.TrimSpace(a.Instructions) + "\n")
	}
	return clip(b.String(), MaxInstructions)
}

// prepared is what preparing a run makes.
type prepared struct {
	plan     *plan
	secrets  map[string]map[string]string
	attempts []*Attempt
}

// prepare reads the catalog, snapshots it, reads the secrets, installs the
// tools and expands the selected cells into the run's first attempts. It
// takes minutes when a tool's setup runs, and holds no lock.
func (r *Runner) prepare(ctx context.Context, run *Run, problems map[string]bool) (*prepared, error) {
	cat, err := catalog.Load(run.Path)
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
			lines = append(lines, p.String())
		}
		return nil, fmt.Errorf("the catalog has errors:\n%s", strings.Join(lines, "\n"))
	}
	var cells []catalog.Cell
	for _, c := range cat.Cells() {
		if run.Selection.selected(c, problems) {
			cells = append(cells, c)
		}
	}
	if len(cells) == 0 {
		return nil, fmt.Errorf("the selection (%s) takes no cell of the catalog", run.Selection.Describe())
	}
	dir := r.layout.RunDir(run.Repo, run.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := catalog.Snapshot(run.Path, filepath.Join(dir, "catalog")); err != nil {
		return nil, fmt.Errorf("copy the catalog: %w", err)
	}
	run.Commit, run.Dirty = gitState(ctx, run.Path)

	// The tools the selected cells use, in the catalog's order.
	var tools []*catalog.Tool
	seen := map[string]bool{}
	areaTools := map[string][]string{}
	for _, c := range cells {
		for _, t := range cat.ToolsOf(c.Test) {
			if !seen[t.Name] {
				seen[t.Name] = true
				tools = append(tools, t)
			}
			if !slices.Contains(areaTools[c.Test.Area], t.Name) {
				areaTools[c.Test.Area] = append(areaTools[c.Test.Area], t.Name)
			}
		}
	}
	secrets, err := readSecrets(ctx, run.Path, tools)
	if err != nil {
		return nil, err
	}
	p := &plan{Title: cat.Title, Instructions: cat.Instructions, Areas: map[string]*planArea{}, Tools: map[string]*planTool{}}
	for _, t := range tools {
		dest, err := installTool(ctx, r.layout, t, run.Path)
		if err != nil {
			return nil, err
		}
		bin := t.Bin
		if bin == "" {
			bin = "bin"
		}
		p.Tools[t.Name] = &planTool{Name: t.Name, Title: t.Title, Dir: dest, Bin: bin, End: t.End,
			Instructions: t.Instructions, Pool: t.Pool}
		if t.Pool != nil && len(members(t.Pool, secrets[t.Name])) == 0 {
			return nil, fmt.Errorf("the pool of %s has no members: %s is empty", t.Name, t.Pool.Secret)
		}
	}
	for _, area := range cat.Areas {
		pa := &planArea{ID: area.ID, Title: area.Title, Instructions: area.Instructions, Columns: area.Columns,
			Evidence: area.Evidence, MCP: area.MCP}
		for _, d := range area.Dimensions {
			pa.Dims = append(pa.Dims, planDim{Name: d.Name, Values: d.Values, Extra: d.Extra})
		}
		var used []*planTool
		for _, name := range areaTools[area.ID] {
			used = append(used, p.Tools[name])
		}
		pa.Prompt = cellPrompt(cat, area, used)
		p.Areas[area.ID] = pa
		p.AreaOrder = append(p.AreaOrder, area.ID)
	}
	if err := writeJSON(r.layout.planPath(run.Repo, run.ID), p); err != nil {
		return nil, err
	}

	now := time.Now().UnixMilli()
	var attempts []*Attempt
	for _, c := range cells {
		t := c.Test
		area := cat.Area(t.Area)
		a := &Attempt{
			ID: newAttemptID(), Run: run.ID, Repo: run.Repo, Test: t.ID, Area: t.Area, Title: t.Title,
			Priority: t.Priority, Tags: t.Tags, Cell: c.Key, CellID: c.ID(), N: 1, Params: mergeParams(area, t),
			Exclusive: t.Exclusive, Evidence: area.Evidence, Context: t.Context,
			State: AttemptQueued, QueuedAt: now,
		}
		if a.Evidence == "" {
			a.Evidence = catalog.EvidenceScreenshot
		}
		timeout := t.Timeout
		if timeout <= 0 {
			timeout = area.Timeout
		}
		if timeout <= 0 {
			timeout = 45 * time.Minute
		}
		a.TimeoutMS = timeout.Milliseconds()
		for _, d := range c.Dims {
			a.Dims = append(a.Dims, Dim{Name: d.Name, ID: d.ID, Label: d.Label})
		}
		for _, tool := range cat.ToolsOf(t) {
			a.Tools = append(a.Tools, tool.Name)
		}
		for _, s := range t.Steps {
			a.Steps = append(a.Steps, Step{N: s.N, Do: s.Do, Expect: s.Expect})
		}
		attempts = append(attempts, a)
	}
	return &prepared{plan: p, secrets: secrets, attempts: attempts}, nil
}

func mergeParams(area *catalog.Area, t *catalog.Test) map[string]string {
	out := map[string]string{}
	if area != nil {
		for k, v := range area.Params {
			out[k] = v
		}
	}
	for k, v := range t.Params {
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// firstMessage is what a cell's agent is sent to begin.
func firstMessage(a *Attempt) string {
	var b strings.Builder
	b.WriteString("Run this cell of the test catalog.\n\n")
	b.WriteString("- Test: " + a.Test + " - " + a.Title + "\n")
	b.WriteString("- Priority: " + a.Priority + "\n")
	for _, d := range a.Dims {
		label := d.ID
		if d.Label != "" && d.Label != d.ID {
			label += " (" + d.Label + ")"
		}
		b.WriteString("- " + upperFirst(d.Name) + ": " + label + "\n")
	}
	b.WriteString(fmt.Sprintf("- Attempt: %d\n", a.N))
	for _, l := range a.Leases {
		b.WriteString(fmt.Sprintf("- %s: account %d (%s), leased for this cell\n", l.Tool, l.Member, l.Label))
	}
	if len(a.Params) > 0 {
		keys := make([]string, 0, len(a.Params))
		for k := range a.Params {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		var ps []string
		for _, k := range keys {
			ps = append(ps, k+"="+a.Params[k])
		}
		b.WriteString("- Parameters: " + strings.Join(ps, ", ") + "\n")
	}
	if ctx := strings.TrimSpace(a.Context); ctx != "" {
		b.WriteString("\n" + ctx + "\n")
	}
	b.WriteString("\n## Steps\n\n")
	for _, s := range a.Steps {
		b.WriteString(fmt.Sprintf("%d. %s\n   Expect: %s\n", s.N, s.Do, s.Expect))
	}
	b.WriteString("\nBegin with step 1. Judge every step with " + testtools.Judge + " right after carrying it out, and end with " +
		testtools.End + ".")
	return b.String()
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
