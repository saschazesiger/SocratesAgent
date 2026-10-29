// Package catalog reads a repository's test catalog, .socrates/tests/: the
// general instructions (TESTS.md), the areas (a folder with an AREA.md each),
// their tests (a Markdown file each, the steps under ## Steps) and the tools
// (tools/<name>/TOOL.md). It validates them, expands every test into its
// cells (a test on one value per dimension) and copies a catalog into a run.
// The format is TEST-MODE.md's, sections "The catalog" and "The tool
// contract".
//
// Load never stops at the first problem: everything it can read is in the
// Catalog, and every problem is in Errors (a run does not start) or Warnings
// (it does), with its file and line, so `socrates tests check` and the Tests
// page can show all of them at once.
package catalog

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The catalog's place in a repository and its fixed file names.
const (
	DirName   = ".socrates/tests"
	TestsFile = "TESTS.md"
	AreaFile  = "AREA.md"
	ToolFile  = "TOOL.md"
	ToolsDir  = "tools"
	readme    = "README.md"
)

// MaxPromptBytes caps the instructions every cell of an area gets in its
// system prompt: TESTS.md, AREA.md and the TOOL.md of every tool the area's
// tests use. It leaves 8 KiB of the 64 KiB instructions cap for Socrates'
// own test protocol.
const MaxPromptBytes = 56 << 10

// DefaultTimeout is how long one attempt of a cell may take when neither
// the area nor the test says.
const DefaultTimeout = 45 * time.Minute

// The evidence an area's verdicts need.
const (
	EvidenceScreenshot = "screenshot"
	EvidenceAny        = "any"
	EvidenceNone       = "none"
)

// The priorities of a test, most important first.
const (
	P0 = "P0"
	P1 = "P1"
	P2 = "P2"
)

// Problem is one thing wrong (or worth a warning) in the catalog. File is
// relative to the catalog folder, with forward slashes; Line is 0 when the
// problem is the file's or the folder's as a whole.
type Problem struct {
	File    string `json:"file"`
	Line    int    `json:"line,omitempty"`
	Message string `json:"message"`
}

// String is the problem as a compiler would print it: file:line: message.
func (p Problem) String() string {
	if p.Line > 0 {
		return fmt.Sprintf("%s:%d: %s", p.File, p.Line, p.Message)
	}
	return fmt.Sprintf("%s: %s", p.File, p.Message)
}

// Catalog is a repository's test catalog as read from its working tree.
type Catalog struct {
	// Root is the repository's root and Dir its catalog folder, both
	// absolute.
	Root string
	Dir  string
	// Title names the repository's tests in Socrates (TESTS.md's title, or
	// the repository folder's name).
	Title string
	// Instructions is TESTS.md's body, in the system prompt of every cell,
	// reviewer and analyst of the repository.
	Instructions string
	// Areas are sorted by their folder's name.
	Areas []*Area
	// Tools are keyed by name (the folder under tools/).
	Tools map[string]*Tool

	Errors   []Problem
	Warnings []Problem

	areas map[string]*Area
	tests map[string]*Test
	mcp   map[string]bool // the servers of .mcp.json, read when an area names one
}

// Area is a top-level folder of the catalog with its AREA.md.
type Area struct {
	ID           string
	Title        string
	Instructions string
	// Tools every test of the area gets.
	Tools []string
	// Columns is the dimension the results table shows as columns; "" when
	// the area has no dimension.
	Columns    string
	Dimensions []*Dimension
	// Params are the area's defaults handed to the tools, keyed by name, in
	// ParamOrder.
	Params     map[string]string
	ParamOrder []string
	// Evidence is what a verdict needs: screenshot, any or none.
	Evidence string
	// MCP names the servers of the repository's .mcp.json the cells may use.
	MCP     []string
	Timeout time.Duration
	// Tests are sorted by id.
	Tests []*Test

	lines map[string]int // the front matter's line of each key
}

// Dimension is an axis an area expands its tests over. Values run by
// default, in their order (the order of the table's columns); Extra values
// run only for a test that names them.
type Dimension struct {
	Name    string
	Values  []Value
	Extra   []Value
	Default Selection
}

// Value is one value of a dimension: its id (in the cell's key) and label.
type Value struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// The kinds of a Selection.
const (
	SelectAll    = "all"
	SelectList   = "list"
	SelectExcept = "except"
	SelectRotate = "rotate"
)

// Selection is which values of a dimension a test runs on: all of Values,
// the listed IDs (Extra ones included), all of Values but IDs, or one value
// per combination of the other dimensions, rotated.
type Selection struct {
	Kind string   `json:"kind"`
	IDs  []string `json:"ids,omitempty"`
}

// Test is one Markdown file of an area.
type Test struct {
	// ID is the file's path in the catalog without .md
	// (web-browser/sign-in/validation): what its history is keyed by.
	ID string
	// Area is the area's id, File the path relative to the catalog folder.
	Area     string
	File     string
	Title    string
	Priority string
	Tags     []string
	// Tools are the test's own, on top of the area's (ToolsOf has both).
	Tools []string
	// Exclusive names the shared test data the test changes: no two cells
	// naming one run at once.
	Exclusive []string
	// Select is the selection of every dimension of the area: the test's
	// own, or the area's default.
	Select map[string]Selection
	// Params are the area's with the test's own on top.
	Params map[string]string
	// Timeout is the test's, or the area's.
	Timeout time.Duration
	// Previous is the id the test had before a rename, whose history it
	// carries on.
	Previous string
	// Context is the body without its ## Steps, for the cell's first
	// message.
	Context string
	Steps   []Step
	// Index is the test's position in its area (by id), which rotation
	// offsets by.
	Index int
	// explicitPriority is whether the front matter named the priority.
	explicitPriority bool
	lines            map[string]int // the front matter's line of each key
}

// Tool is a program of tools/<name>/ that the tests put on PATH.
type Tool struct {
	Name string
	// Dir is the tool's folder in the checkout, absolute.
	Dir   string
	Title string
	// Bin is the folder put first on PATH, relative to Dir.
	Bin string
	// Setup runs once per run in the run's copy of the tool; End after every
	// attempt of a cell that used it. Both are shell commands, "" for none.
	Setup string
	End   string
	// Instructions is TOOL.md's body, for the agent.
	Instructions string
	Secrets      Secrets
	Pool         *Pool
}

// Secrets says how a tool's secrets are read: Command prints NAME=value
// lines (run in the repository root), Names the ones the tool takes.
type Secrets struct {
	Command string
	Names   []SecretName
}

// SecretName is one secret a tool takes; an optional one (written NAME?) may
// be missing from the command's output.
type SecretName struct {
	Name     string
	Optional bool
}

// Pool is a tool's set of accounts: one member per entry of Secret, split
// at Separator, each carrying PerMember sessions at once. Status, when set,
// prints which members are busy outside Socrates.
type Pool struct {
	Secret    string
	Separator string
	PerMember int
	Status    string
}

// Load reads the catalog of the repository at repoRoot. It fails only when
// there is no catalog to read (no .socrates/tests or no TESTS.md in it);
// every problem in its content is in the Catalog's Errors and Warnings.
func Load(repoRoot string) (*Catalog, error) {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, err
	}
	c := &Catalog{
		Root:  root,
		Dir:   filepath.Join(root, filepath.FromSlash(DirName)),
		Tools: map[string]*Tool{},
		areas: map[string]*Area{},
		tests: map[string]*Test{},
	}
	if fi, err := os.Stat(c.Dir); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("%s has no test catalog (%s)", root, DirName)
	}
	src, err := os.ReadFile(filepath.Join(c.Dir, TestsFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("the test catalog %s has no %s", c.Dir, TestsFile)
	}
	if err != nil {
		return nil, err
	}
	c.loadTests(string(src))

	entries, err := os.ReadDir(c.Dir)
	if err != nil {
		return nil, err
	}
	if fi, err := os.Lstat(filepath.Join(c.Dir, ToolsDir)); err == nil {
		switch {
		case fi.IsDir():
			c.loadTools()
		case fi.Mode()&fs.ModeSymlink != 0:
			c.warnf(ToolsDir, 0, "a symbolic link is not part of the catalog: its tools are not read")
		}
	}
	for _, e := range entries {
		name := e.Name()
		switch {
		case strings.HasPrefix(name, "."), name == ToolsDir, name == TestsFile, name == readme:
		case e.Type()&fs.ModeSymlink != 0:
			c.warnf(name, 0, "a symbolic link is not part of the catalog (it is not copied into a run)")
		case e.IsDir():
			c.loadArea(name)
		case strings.HasSuffix(name, ".md"):
			c.warnf(name, 0, "not part of an area: a test goes into an area's folder")
		}
	}
	c.validate()
	return c, nil
}

// Test returns the test with id, or nil.
func (c *Catalog) Test(id string) *Test { return c.tests[id] }

// Area returns the area with id, or nil.
func (c *Catalog) Area(id string) *Area { return c.areas[id] }

// ToolsOf returns the tools of a test: its area's, then its own, each once.
// A name no tool has is left out (it is an error of the catalog).
func (c *Catalog) ToolsOf(t *Test) []*Tool {
	var names []string
	if a := c.areas[t.Area]; a != nil {
		names = append(names, a.Tools...)
	}
	names = append(names, t.Tools...)
	var out []*Tool
	seen := map[string]bool{}
	for _, name := range names {
		if tool := c.Tools[name]; tool != nil && !seen[name] {
			seen[name] = true
			out = append(out, tool)
		}
	}
	return out
}

// ToolNames returns the names of the catalog's tools, sorted.
func (c *Catalog) ToolNames() []string {
	names := make([]string, 0, len(c.Tools))
	for name := range c.Tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// TestCount is the number of tests in every area.
func (c *Catalog) TestCount() int { return len(c.tests) }

func (c *Catalog) errorf(file string, line int, format string, a ...any) {
	c.Errors = append(c.Errors, Problem{File: file, Line: line, Message: fmt.Sprintf(format, a...)})
}

func (c *Catalog) warnf(file string, line int, format string, a ...any) {
	c.Warnings = append(c.Warnings, Problem{File: file, Line: line, Message: fmt.Sprintf(format, a...)})
}

// readFile reads a catalog file (rel with forward slashes) and splits it
// into its parsed front matter and its body. A problem is recorded and the
// front matter comes back empty, so the caller still reads what it can.
func (c *Catalog) readFile(rel string) (front *yamlMap, body string, bodyLine int, ok bool) {
	src, err := os.ReadFile(filepath.Join(c.Dir, filepath.FromSlash(rel)))
	if err != nil {
		c.errorf(rel, 0, "%v", err)
		return newYAMLMap(0), "", 1, false
	}
	return c.parseFile(rel, string(src))
}

func (c *Catalog) parseFile(rel, src string) (front *yamlMap, body string, bodyLine int, ok bool) {
	text, frontLine, body, bodyLine, err := splitFrontMatter(src)
	if err == nil {
		front, err = parseYAML(text, frontLine)
	}
	if err != nil {
		var ye *yamlError
		if errors.As(err, &ye) {
			c.errorf(rel, ye.Line, "%s", ye.Msg)
		} else {
			c.errorf(rel, 0, "%v", err)
		}
		return newYAMLMap(1), body, bodyLine, false
	}
	return front, body, bodyLine, true
}

// loadTests reads TESTS.md.
func (c *Catalog) loadTests(src string) {
	front, body, _, _ := c.parseFile(TestsFile, src)
	d := decoder{c: c, file: TestsFile, m: front}
	d.keys("title")
	c.Title = d.str("title")
	if c.Title == "" {
		c.Title = filepath.Base(c.Root)
	}
	c.Instructions = strings.TrimSpace(body)
}
