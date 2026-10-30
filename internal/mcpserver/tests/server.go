// Package tests is Socrates Tests, an MCP server built into Socrates
// (docs/design/MCP.md, "Socrates Tests"): everything about the test catalog
// of the repository a chat works in, for its agent. It reads the catalog
// and its format, writes and deletes its files, checks it, estimates and
// starts runs, follows and controls them, reads their cells and reports,
// triages the bugs and reads and changes the repository's test settings:
// what the Tests page does, as tools.
//
// It needs no account and no configuration, and like every built-in server
// it is off in a chat until Admin or the chat switches it on. A chat that
// has it on is not given the four test tools of the chat tools
// (chattools.TestTools), which it has too. A test session never uses it.
package tests

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent/chattools"
	"github.com/saschazesiger/SocratesAgent/internal/agent/testtools"
	"github.com/saschazesiger/SocratesAgent/internal/mcpserver"
	"github.com/saschazesiger/SocratesAgent/internal/testmode"
	"github.com/saschazesiger/SocratesAgent/internal/testmode/catalog"
)

// Name is the server's name in a chat.
const Name = "socrates-tests"

// The tools, bare, besides the four the chat tools have too
// (chattools.TestsFormat, TestsCheck, RunTests, TestResults).
const (
	TestRead      = "test_read"
	CatalogRead   = "catalog_read"
	CatalogWrite  = "catalog_write"
	CatalogDelete = "catalog_delete"
	Estimate      = "tests_estimate"
	Runs          = "test_runs"
	Run           = "test_run"
	Cell          = "test_cell"
	RunControl    = "test_run_control"
	Retry         = "test_retry"
	Override      = "test_override"
	Bugs          = "test_bugs"
	Bug           = "test_bug"
	BugUpdate     = "test_bug_update"
	BugIssue      = "test_bug_issue"
	Search        = "test_search"
	Settings      = "test_settings"
)

// The actions of test_run_control.
var controlActions = []string{"stop", "resume", "analyse", "pin", "unpin", "delete"}

// maxFile is the largest catalog file catalog_write takes.
const maxFile = 1 << 20

// Deps is what the server needs of the Socrates server.
type Deps struct {
	// Runner is the test runner; nil while there is none (a catalog check
	// run from the command line, a server that is starting).
	Runner func() *testmode.Runner
	// CheckSettings refuses settings a run could not start with (a harness
	// that is not offered, an account of another harness): "" when they are
	// fine. Nil checks nothing.
	CheckSettings func(*testmode.Settings) string
}

// Server is Socrates Tests.
type Server struct{ deps Deps }

// New is the server.
func New(d Deps) *Server { return &Server{deps: d} }

func (s *Server) Name() string  { return Name }
func (s *Server) Title() string { return "Socrates Tests" }
func (s *Server) Description() string {
	return "Lets the agent work with the test catalog of its chat's repository: read, write and check tests, start and control runs, read results, triage bugs."
}

func (s *Server) Instructions() string {
	return "Socrates Tests: the test catalog Socrates runs for this chat's repository (" + catalog.DirName + "/), " +
		"the runs of it and their results, as the Tests page shows them. Agents work through the tests on real " +
		"devices and judge every step on screenshots; a reviewer and an analyst turn that into verdicts and bugs.\n" +
		"- To write or change a test, read " + chattools.TestsFormat + " first. Write the files with " + CatalogWrite +
		" (or your own file tools) and fix what " + chattools.TestsCheck + " reports until there are no errors.\n" +
		"- Before a big run, " + Estimate + " says how many cells it takes, how long and what it costs. Run as narrow " +
		"as the question: the tests or cells concerned, or problems_of \"latest\" for what failed. Never run " +
		"everything without being asked.\n" +
		"- After " + chattools.RunTests + ", end your turn: Socrates sends this chat the results as a message when the " +
		"run ends. Do not poll " + Run + " in a loop; read it when the person asks how a run is doing.\n" +
		"- Read results with " + chattools.TestResults + ", one cell in full with " + Cell + " (its screenshots are files " +
		"you can open), a test with its history with " + TestRead + ", the known bugs with " + Bugs + ".\n" +
		"- Stopping or deleting runs, overriding verdicts, triaging bugs, opening GitHub issues and changing the test " +
		"settings are the person's decisions: do them when asked, not on your own."
}

func obj(props map[string]any, required ...string) map[string]any {
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

var (
	runArg  = str("The run's id (as test_runs names it); the repository's latest run when left out.")
	cellArg = str("The cell's id: the test id, @, and its dimension values joined by '.' (web/checkout@pixel.es); or an attempt's id.")
	pathArg = str("The file's path inside " + catalog.DirName + "/, such as web/checkout.md, web/AREA.md, TESTS.md or tools/devices/TOOL.md.")
)

func (s *Server) Tools() []mcpserver.Tool {
	var out []mcpserver.Tool
	for _, d := range chattools.TestTools() {
		out = append(out, mcpserver.Tool{Name: d.Name, Title: d.Title, Description: d.Description, Schema: d.Schema})
	}
	verdicts := append(slices.Clone(testtools.Verdicts), "")
	return append(out,
		mcpserver.Tool{Name: TestRead, Title: "Read a test",
			Description: "A test of the catalog: its file as written, its cells and how each stood in the latest runs, and the bugs that name one of its cells.",
			Schema:      obj(map[string]any{"test": str("The test's id: its path in the catalog without .md, such as web/checkout.")}, "test")},
		mcpserver.Tool{Name: CatalogRead, Title: "Read a catalog file",
			Description: "A file of the catalog as it is on disk (TESTS.md, an AREA.md, a test, a TOOL.md), or the list of every file when path is left out.",
			Schema:      obj(map[string]any{"path": pathArg})},
		mcpserver.Tool{Name: CatalogWrite, Title: "Write a catalog file",
			Description: "Creates or replaces a file of the catalog (folders are made as needed) and checks the catalog: the answer names " +
				"the problems of this file and how many the catalog has. Read tests_format first. A test is a .md below an area folder; " +
				"an area needs its AREA.md; the catalog needs TESTS.md.",
			Schema: obj(map[string]any{
				"path":       pathArg,
				"content":    str("The file's whole text."),
				"executable": map[string]any{"type": "boolean", "description": "Make the file executable: a tool's script under tools/<name>/bin/. A file written again keeps its executable bit."},
			}, "path", "content")},
		mcpserver.Tool{Name: CatalogDelete, Title: "Delete a catalog file",
			Description: "Deletes a file of the catalog (and the folders it leaves empty), then checks the catalog. A test's history stays under its id.",
			Schema:      obj(map[string]any{"path": pathArg}, "path")},
		mcpserver.Tool{Name: Estimate, Title: "Estimate a run",
			Description: "What a run of a selection would take, without starting it: its cells and tests, about how long and what it costs " +
				"(from the repository's last runs), and why it could not start. The selection is run_tests'.",
			Schema: selectionSchema(false)},
		mcpserver.Tool{Name: Runs, Title: "List the runs",
			Description: "The repository's test runs, newest first: number, id, name, state, selection, counts by verdict, cost, who started it.",
			Schema:      obj(map[string]any{"limit": map[string]any{"type": "integer", "minimum": 1, "description": "How many, 10 when left out."}})},
		mcpserver.Tool{Name: Run, Title: "A run's state",
			Description: "One run as it is now: its state, progress and counts, and every cell with its state and verdict. For a run that is over, test_results has more.",
			Schema:      obj(map[string]any{"run": runArg})},
		mcpserver.Tool{Name: Cell, Title: "One cell in full",
			Description: "A cell of a run: every step with what it expects, the agent's, the reviewer's and the person's verdicts, the observations, " +
				"the actions and the absolute paths of the screenshots (open them with your file tools), the summary and its other attempts.",
			Schema: obj(map[string]any{"run": runArg, "cell": cellArg}, "cell")},
		mcpserver.Tool{Name: RunControl, Title: "Control a run",
			Description: "stop: stop an active run; resume: take up a paused run (its secrets are read again); analyse: run the analysis of a " +
				"run that is over again; pin / unpin: keep a run from being pruned; delete: delete a run that is over with its evidence, for good.",
			Schema: obj(map[string]any{"run": str("The run's id."), "action": map[string]any{"type": "string", "enum": controlActions}}, "run", "action")},
		mcpserver.Tool{Name: Retry, Title: "Retry a cell",
			Description: "Queues another attempt of a cell that is over; a run that is over is taken up again for it. The results come to this chat when the run ends again only if this chat started it.",
			Schema:      obj(map[string]any{"run": runArg, "cell": str("The cell's id.")}, "cell")},
		mcpserver.Tool{Name: Override, Title: "Override a cell's verdict",
			Description: "Sets the person's verdict of a cell's attempt that is over, with a note; it counts over the agent's and the reviewer's. An empty verdict removes the override.",
			Schema: obj(map[string]any{"run": runArg, "cell": cellArg,
				"verdict": map[string]any{"type": "string", "enum": verdicts, "description": "pass, issue, fail, blocked or na; empty to remove."},
				"note":    str("Why.")}, "cell", "verdict")},
		mcpserver.Tool{Name: Bugs, Title: "The known bugs",
			Description: "The repository's bugs as the runs' analyses found them, the worst first: id, status, severity, title, cells, runs, note, issue.",
			Schema: obj(map[string]any{"status": map[string]any{"type": "string", "enum": append([]string{"all"}, testmode.BugStatuses...),
				"description": "Only bugs of this status; every bug when left out."}})},
		mcpserver.Tool{Name: Bug, Title: "One bug",
			Description: "A bug in full: its description, the cells that show it with their verdicts, the runs that reported it and its history.",
			Schema:      obj(map[string]any{"id": str("The bug's id, such as B-3.")}, "id")},
		mcpserver.Tool{Name: BugUpdate, Title: "Triage a bug",
			Description: "Changes a bug's status, severity, note or issue address; what is left out stays. A status or severity set here is kept over what later analyses say.",
			Schema: obj(map[string]any{
				"id":       str("The bug's id."),
				"status":   map[string]any{"type": "string", "enum": testmode.BugStatuses},
				"severity": map[string]any{"type": "string", "enum": testtools.Severities},
				"note":     str("The person's note on it (empty removes it)."),
				"issue":    str("The https:// address of its issue (empty removes it)."),
			}, "id")},
		mcpserver.Tool{Name: BugIssue, Title: "Open a GitHub issue for a bug",
			Description: "Opens a GitHub issue of the bug in the repository (with gh, as the person's GitHub sign-in) and keeps its address on the bug.",
			Schema:      obj(map[string]any{"id": str("The bug's id."), "title": str("The issue's title."), "body": str("The issue's text in Markdown.")}, "id", "title", "body")},
		mcpserver.Tool{Name: Search, Title: "Search the runs",
			Description: "Finds the cells of the repository's runs whose test, title, observations, summary, review notes or bugs hold every word, newest run first.",
			Schema: obj(map[string]any{"query": str("The words."),
				"limit": map[string]any{"type": "integer", "minimum": 1, "description": "How many, 20 when left out."}}, "query")},
		mcpserver.Tool{Name: Settings, Title: "The test settings",
			Description: "The repository's settings for its runs: the harness, model, effort and accounts of the cell agents, reviewers and analyst, " +
				"how many cells run at once, review and analysis, attempts, how many runs are kept, the MCP servers of the cells. With set, " +
				"changes the settings named in it (the rest stay) and answers them as saved.",
			Schema: obj(map[string]any{"set": map[string]any{"type": "object",
				"description": "The settings to change, as the answer shows them, such as {\"parallel\": 2, \"cell\": {\"model\": \"claude-opus-5-5\"}}."}})},
	)
}

// selectionSchema is run_tests' schema (chattools.TestTools), with or
// without its name.
func selectionSchema(withName bool) map[string]any {
	for _, d := range chattools.TestTools() {
		if d.Name != chattools.RunTests {
			continue
		}
		props := map[string]any{}
		for k, v := range d.Schema["properties"].(map[string]any) {
			if k != "name" || withName {
				props[k] = v
			}
		}
		return obj(props)
	}
	return obj(map[string]any{})
}

// CheckArea refuses an area that names this server: it is for chats, and a
// test session never gets it.
func (s *Server) CheckArea(map[string][]string, map[string]string) []string {
	return []string{Name + " is for chats: a test cell cannot use it"}
}

// ChatsOnly keeps the server out of every test session.
func (s *Server) ChatsOnly() {}

// call is one call: the session's repository and the runner.
type call struct {
	s       *Server
	sess    mcpserver.Session
	runner  *testmode.Runner
	root    string
	repoKey string
}

func (s *Server) Call(ctx context.Context, sess mcpserver.Session, tool string, args json.RawMessage) mcpserver.Result {
	if sess.Test != nil || strings.TrimSpace(sess.Workdir) == "" {
		return mcpserver.Error("Socrates Tests works for a chat in a folder; a test session cannot use it.")
	}
	c := &call{s: s, sess: sess, root: testmode.FindRoot(sess.Workdir)}
	c.repoKey = testmode.RepoKey(c.root)
	if s.deps.Runner != nil {
		c.runner = s.deps.Runner()
	}
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}
	text, err := c.do(tool, args)
	if err != nil {
		return mcpserver.Error(err.Error())
	}
	return mcpserver.Text(text)
}

func (c *call) do(tool string, args json.RawMessage) (string, error) {
	switch tool {
	case chattools.TestsFormat:
		return catalog.Format, nil
	case chattools.TestsCheck:
		return CheckText(c.sess.Workdir), nil
	case CatalogRead:
		var a struct {
			Path string `json:"path"`
		}
		if err := decode(args, &a); err != nil {
			return "", err
		}
		return c.readFile(a.Path)
	case CatalogWrite:
		var a struct {
			Path       string `json:"path"`
			Content    string `json:"content"`
			Executable bool   `json:"executable"`
		}
		if err := decode(args, &a); err != nil {
			return "", err
		}
		return c.writeFile(a.Path, a.Content, a.Executable)
	case CatalogDelete:
		var a struct {
			Path string `json:"path"`
		}
		if err := decode(args, &a); err != nil {
			return "", err
		}
		return c.deleteFile(a.Path)
	case TestRead:
		var a struct {
			Test string `json:"test"`
		}
		if err := decode(args, &a); err != nil {
			return "", err
		}
		return c.readTest(a.Test)
	}
	if c.runner == nil {
		return "", errors.New("The test runner is not running.")
	}
	switch tool {
	case chattools.RunTests:
		var a chattools.RunTestsArgs
		if err := decode(args, &a); err != nil {
			return "", err
		}
		started, err := c.runner.StartChatRun(c.sess.Workdir, a.Name, SelectionOf(a), testmode.StartedBy{Chat: c.sess.ID, Title: c.sess.Title})
		if errors.Is(err, testmode.ErrNoCatalog) {
			return "", fmt.Errorf("This repository has no test catalog (%s): there is nothing to run. Read %s to write one.", catalog.DirName, chattools.TestsFormat)
		}
		if err != nil {
			return "", fmt.Errorf("The run was not started: %v", err)
		}
		return RunStartedText(started), nil
	case chattools.TestResults:
		var a chattools.TestResultsArgs
		if err := decode(args, &a); err != nil {
			return "", err
		}
		id, err := c.runner.ChatRunID(c.sess.Workdir, a.Run)
		if err != nil {
			return "", err
		}
		problemsOnly := a.ProblemsOnly == nil || *a.ProblemsOnly
		report, err := c.runner.Report(id, testmode.ReportOptions{Paths: true, ProblemsOnly: problemsOnly})
		if err != nil {
			return "", fmt.Errorf("The results of %s could not be read: %v", id, err)
		}
		return CapReport(report, chattools.MaxResults, problemsOnly), nil
	case Estimate:
		var a chattools.RunTestsArgs
		if err := decode(args, &a); err != nil {
			return "", err
		}
		return c.estimate(SelectionOf(a))
	case Runs:
		var a struct {
			Limit int `json:"limit"`
		}
		if err := decode(args, &a); err != nil {
			return "", err
		}
		return c.runs(a.Limit), nil
	case Run:
		var a struct {
			Run string `json:"run"`
		}
		if err := decode(args, &a); err != nil {
			return "", err
		}
		return c.run(a.Run)
	case Cell:
		var a struct {
			Run  string `json:"run"`
			Cell string `json:"cell"`
		}
		if err := decode(args, &a); err != nil {
			return "", err
		}
		id, err := c.runner.ChatRunID(c.sess.Workdir, a.Run)
		if err != nil {
			return "", err
		}
		text, err := c.runner.CellText(id, a.Cell)
		return text, notFound(err, "run "+id)
	case RunControl:
		var a struct {
			Run    string `json:"run"`
			Action string `json:"action"`
		}
		if err := decode(args, &a); err != nil {
			return "", err
		}
		return c.control(a.Run, a.Action)
	case Retry:
		var a struct {
			Run  string `json:"run"`
			Cell string `json:"cell"`
		}
		if err := decode(args, &a); err != nil {
			return "", err
		}
		id, err := c.runner.ChatRunID(c.sess.Workdir, a.Run)
		if err != nil {
			return "", err
		}
		at, err := c.runner.RetryCell(id, strings.TrimSpace(a.Cell))
		if err != nil {
			return "", notFound(err, "cell "+a.Cell+" in run "+id)
		}
		return fmt.Sprintf("Attempt %d of %s (%s) is queued in run %s.", at.N, at.CellID, at.ID, id), nil
	case Override:
		var a struct {
			Run     string `json:"run"`
			Cell    string `json:"cell"`
			Verdict string `json:"verdict"`
			Note    string `json:"note"`
		}
		if err := decode(args, &a); err != nil {
			return "", err
		}
		return c.override(a.Run, a.Cell, a.Verdict, a.Note)
	case Bugs:
		var a struct {
			Status string `json:"status"`
		}
		if err := decode(args, &a); err != nil {
			return "", err
		}
		return c.bugs(a.Status)
	case Bug:
		var a struct {
			ID string `json:"id"`
		}
		if err := decode(args, &a); err != nil {
			return "", err
		}
		return c.bug(a.ID)
	case BugUpdate:
		var a struct {
			ID string `json:"id"`
			testmode.BugPatch
		}
		if err := decode(args, &a); err != nil {
			return "", err
		}
		if err := c.ensureRepo(); err != nil {
			return "", err
		}
		v, err := c.runner.PatchBug(c.repoKey, strings.TrimSpace(a.ID), a.BugPatch)
		if err != nil {
			return "", notFound(err, "bug "+a.ID)
		}
		return "Saved:\n" + bugLine(*v), nil
	case BugIssue:
		var a struct {
			ID    string `json:"id"`
			Title string `json:"title"`
			Body  string `json:"body"`
		}
		if err := decode(args, &a); err != nil {
			return "", err
		}
		if err := c.ensureRepo(); err != nil {
			return "", err
		}
		url, err := c.runner.CreateIssue(c.repoKey, strings.TrimSpace(a.ID), a.Title, a.Body)
		if err != nil {
			return "", notFound(err, "bug "+a.ID)
		}
		return "Opened " + url + " and kept it on the bug.", nil
	case Search:
		var a struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		if err := decode(args, &a); err != nil {
			return "", err
		}
		return c.search(a.Query, a.Limit), nil
	case Settings:
		var a struct {
			Set json.RawMessage `json:"set"`
		}
		if err := decode(args, &a); err != nil {
			return "", err
		}
		return c.settings(a.Set)
	}
	return "", fmt.Errorf("Socrates Tests has no tool %s.", tool)
}

// decode reads a call's arguments; unknown keys are refused, so that a typo
// is never ignored.
func decode(args json.RawMessage, v any) error {
	dec := json.NewDecoder(strings.NewReader(string(args)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("The arguments could not be read: %v", err)
	}
	return nil
}

// notFound words the runner's ErrNotFound for what was asked.
func notFound(err error, what string) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, testmode.ErrNotFound):
		return fmt.Errorf("There is no %s.", what)
	case errors.Is(err, testmode.ErrBusy):
		return errors.New("The repository has another active run; wait until it is over or stop it.")
	}
	return err
}

// ensureRepo puts the repository on the Tests page's list, as a run of it
// does: the bugs and settings are kept per listed repository.
func (c *call) ensureRepo() error {
	if c.runner.Repo(c.repoKey) != nil {
		return nil
	}
	if !testmode.HasCatalog(c.root) {
		return fmt.Errorf("This repository (%s) has no test catalog yet; read %s to write one.", c.root, chattools.TestsFormat)
	}
	_, err := c.runner.AddRepo(c.root)
	return err
}

// ------------------------------------------------------------------ views

func when(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format("2006-01-02 15:04 UTC")
}

func countsText(n testmode.Counts) string {
	parts := []string{fmt.Sprintf("%d/%d cells done", n.Done, n.Cells)}
	for _, v := range testtools.Verdicts {
		if n.Verdicts[v] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n.Verdicts[v], v))
		}
	}
	return strings.Join(parts, ", ")
}

func runLine(v testmode.RunView) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- Run %d (%s)", v.Number, v.ID)
	if v.Name != "" {
		fmt.Fprintf(&b, " %q", v.Name)
	}
	fmt.Fprintf(&b, ": %s", v.State)
	if v.Error != "" {
		fmt.Fprintf(&b, " (%s)", strings.Join(strings.Fields(v.Error), " "))
	}
	fmt.Fprintf(&b, "; %s; %s", v.Selection.Describe(), countsText(v.Counts))
	if v.Bugs > 0 {
		fmt.Fprintf(&b, "; %d bug(s)", v.Bugs)
	}
	if v.Cost != nil {
		fmt.Fprintf(&b, "; $%.2f", *v.Cost)
	}
	if t := when(v.CreatedAt); t != "" {
		fmt.Fprintf(&b, "; created %s", t)
	}
	if v.StartedBy != nil {
		fmt.Fprintf(&b, "; started by the chat %q", v.StartedBy.Title)
	}
	if v.Pinned {
		b.WriteString("; pinned")
	}
	return b.String()
}

func (c *call) runs(limit int) string {
	if limit <= 0 {
		limit = 10
	}
	list := c.runner.Runs(c.repoKey)
	if len(list) == 0 {
		return "This repository has no test run yet."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d run(s) of %s, newest first:\n", len(list), c.root)
	for i, v := range list {
		if i == limit {
			fmt.Fprintf(&b, "and %d older run(s).\n", len(list)-limit)
			break
		}
		b.WriteString(runLine(v) + "\n")
	}
	return b.String()
}

func (c *call) run(id string) (string, error) {
	id, err := c.runner.ChatRunID(c.sess.Workdir, id)
	if err != nil {
		return "", err
	}
	v, err := c.runner.RunDetail(id)
	if err != nil {
		return "", notFound(err, "run "+id)
	}
	cells, err := c.runner.Cells(id)
	if err != nil {
		return "", notFound(err, "run "+id)
	}
	var b strings.Builder
	b.WriteString(strings.TrimPrefix(runLine(v), "- ") + "\n")
	if v.Counts.Reviews > 0 {
		fmt.Fprintf(&b, "%d review(s) waiting or running.\n", v.Counts.Reviews)
	}
	if v.EtaMS > 0 {
		fmt.Fprintf(&b, "About %s to go.\n", (time.Duration(v.EtaMS) * time.Millisecond).Round(time.Minute))
	}
	b.WriteString("\nCells:\n")
	for _, r := range cells {
		fmt.Fprintf(&b, "- %s (attempt %d, %s): %s", r.CellID, r.N, r.ID, r.State)
		if r.Final != "" {
			fmt.Fprintf(&b, ", %s", r.Final)
		}
		if r.Wait != "" {
			fmt.Fprintf(&b, ", %s", r.Wait)
		}
		if r.Summary != "" {
			fmt.Fprintf(&b, ": %s", strings.Join(strings.Fields(r.Summary), " "))
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

func (c *call) control(id, action string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", errors.New("Name the run.")
	}
	id, err := c.runner.ChatRunID(c.sess.Workdir, id)
	if err != nil {
		return "", err
	}
	var done string
	switch action {
	case "stop":
		err, done = c.runner.StopRun(id), "stopped"
	case "resume":
		err, done = c.runner.ResumeRun(id), "taken up again"
	case "analyse":
		err, done = c.runner.Analyse(id), "being analysed again"
	case "pin":
		err, done = c.runner.PinRun(id, true), "pinned"
	case "unpin":
		err, done = c.runner.PinRun(id, false), "no longer pinned"
	case "delete":
		err, done = c.runner.DeleteRun(id), "deleted"
	default:
		return "", fmt.Errorf("The action must be one of %s.", strings.Join(controlActions, ", "))
	}
	if err != nil {
		return "", notFound(err, "run "+id)
	}
	return "Run " + id + " is " + done + ".", nil
}

func (c *call) override(run, cell, verdict, note string) (string, error) {
	id, err := c.runner.ChatRunID(c.sess.Workdir, run)
	if err != nil {
		return "", err
	}
	cells, err := c.runner.Cells(id)
	if err != nil {
		return "", notFound(err, "run "+id)
	}
	cell = strings.TrimSpace(cell)
	attempt := ""
	for _, r := range cells {
		if r.ID == cell || r.CellID == cell {
			attempt = r.ID
		}
	}
	if attempt == "" {
		attempt = cell
	}
	row, err := c.runner.SetOverride(id, attempt, verdict, note)
	if err != nil {
		return "", notFound(err, "cell "+cell+" in run "+id)
	}
	if row.Override == nil {
		return fmt.Sprintf("The override of %s is removed: its verdict is %s again.", row.CellID, row.Final), nil
	}
	return fmt.Sprintf("%s is %s now, by the person's verdict.", row.CellID, row.Final), nil
}

func bugLine(v testmode.BugView) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- %s [%s, %s] %s", v.ID, v.Status, v.Severity, v.Title)
	if len(v.Cells) > 0 {
		fmt.Fprintf(&b, "; cells %s", strings.Join(v.Cells, " "))
	}
	fmt.Fprintf(&b, "; seen in %d run(s)", v.Seen)
	if v.LastNumber > 0 {
		fmt.Fprintf(&b, ", last run %d", v.LastNumber)
	}
	if v.Regressed {
		b.WriteString("; came back after it was fixed")
	}
	if v.Issue != "" {
		fmt.Fprintf(&b, "; issue %s", v.Issue)
	}
	if v.Note != "" {
		fmt.Fprintf(&b, "; note: %s", strings.Join(strings.Fields(v.Note), " "))
	}
	return b.String()
}

func (c *call) bugs(status string) (string, error) {
	if c.runner.Repo(c.repoKey) == nil {
		return "This repository has no bugs: no run of it was analysed yet.", nil
	}
	list, err := c.runner.BugList(c.repoKey, strings.TrimSpace(status))
	if err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "No bugs.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d bug(s), the worst first:\n", len(list))
	for _, v := range list {
		b.WriteString(bugLine(v) + "\n")
	}
	return b.String(), nil
}

func (c *call) bug(id string) (string, error) {
	if c.runner.Repo(c.repoKey) == nil {
		return "", fmt.Errorf("There is no bug %s.", id)
	}
	d, err := c.runner.Bug(c.repoKey, strings.TrimSpace(id))
	if err != nil {
		return "", notFound(err, "bug "+id)
	}
	var b strings.Builder
	b.WriteString(strings.TrimPrefix(bugLine(d.Bug), "- ") + "\n")
	if d.Bug.Description != "" {
		b.WriteString("\n" + strings.TrimSpace(d.Bug.Description) + "\n")
	}
	if len(d.Cells) > 0 {
		b.WriteString("\nCells:\n")
		for _, r := range d.Cells {
			fmt.Fprintf(&b, "- %s in run %s (attempt %s): %s", r.CellID, r.Run, r.ID, r.Final)
			if r.Summary != "" {
				fmt.Fprintf(&b, ": %s", strings.Join(strings.Fields(r.Summary), " "))
			}
			b.WriteString("\n")
		}
	}
	if len(d.Runs) > 0 {
		var runs []string
		for _, r := range d.Runs {
			runs = append(runs, fmt.Sprintf("%d (%s)", r.Number, r.ID))
		}
		fmt.Fprintf(&b, "\nReported by the runs %s.\n", strings.Join(runs, ", "))
	}
	return b.String(), nil
}

func (c *call) search(q string, limit int) string {
	if limit <= 0 {
		limit = 20
	}
	if strings.TrimSpace(q) == "" {
		return "Name the words to look for."
	}
	hits := c.runner.Search(c.repoKey, q, limit)
	if len(hits) == 0 {
		return "No cell of this repository's runs holds every word of " + fmt.Sprintf("%q", q) + "."
	}
	var b strings.Builder
	for _, h := range hits {
		fmt.Fprintf(&b, "- %s in run %s (attempt %s): %s", h.CellID, h.Run, h.ID, h.Final)
		if h.Snippet != "" {
			fmt.Fprintf(&b, ": %s", strings.Join(strings.Fields(h.Snippet), " "))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (c *call) estimate(sel testmode.Selection) (string, error) {
	if err := c.ensureRepo(); err != nil {
		return "", err
	}
	e, err := c.runner.Estimate(c.repoKey, sel, nil)
	if err != nil {
		return "", notFound(err, "repository "+c.root)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d cell(s) of %d test(s) (%s)", e.Cells, e.Tests, sel.Describe())
	if e.EtaMS > 0 {
		fmt.Fprintf(&b, "; about %s", (time.Duration(e.EtaMS) * time.Millisecond).Round(time.Minute))
	}
	if e.Cost != nil {
		fmt.Fprintf(&b, "; about $%.2f", *e.Cost)
	}
	if e.EtaMS == 0 && e.Cost == nil {
		b.WriteString("; no earlier run to tell the time and cost from")
	}
	b.WriteString(".\n")
	if len(e.Errors) > 0 {
		b.WriteString("\nIt would not start:\n")
		for _, p := range e.Errors {
			fmt.Fprintf(&b, "- %s\n", p.String())
		}
	}
	return b.String(), nil
}

func (c *call) settings(set json.RawMessage) (string, error) {
	if err := c.ensureRepo(); err != nil {
		return "", err
	}
	cur := c.runner.Settings(c.repoKey)
	if len(set) > 0 && string(set) != "null" {
		// A copy of its own: the runner's settings share their lists, which
		// decoding would write into.
		var next testmode.Settings
		if raw, err := json.Marshal(cur); err != nil {
			return "", err
		} else if err := json.Unmarshal(raw, &next); err != nil {
			return "", err
		}
		// What set names replaces it; the rest stays as it is.
		dec := json.NewDecoder(strings.NewReader(string(set)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&next); err != nil {
			return "", fmt.Errorf("set could not be read: %v", err)
		}
		if c.s.deps.CheckSettings != nil {
			if msg := c.s.deps.CheckSettings(&next); msg != "" {
				return "", errors.New(msg)
			}
		}
		saved, err := c.runner.SetSettings(c.repoKey, next)
		if err != nil {
			return "", err
		}
		cur = saved
	}
	out, err := json.MarshalIndent(cur, "", "  ")
	if err != nil {
		return "", err
	}
	return "The test settings of " + c.root + ":\n" + string(out), nil
}

func (c *call) readTest(id string) (string, error) {
	id = strings.TrimSuffix(strings.TrimSpace(id), ".md")
	if id == "" {
		return "", errors.New("Name the test.")
	}
	rel, err := catalogPath(id + ".md")
	if err != nil {
		return "", err
	}
	if err := c.noLinks(rel); err != nil {
		return "", err
	}
	var b strings.Builder
	src, readErr := os.ReadFile(filepath.Join(c.dir(), filepath.FromSlash(rel)))
	if readErr == nil {
		fmt.Fprintf(&b, "%s/%s:\n\n````markdown\n%s\n````\n", catalog.DirName, rel, strings.TrimRight(string(src), "\n"))
	} else {
		fmt.Fprintf(&b, "The catalog has no file %s/%s.\n", catalog.DirName, rel)
	}
	if c.runner == nil || c.runner.Repo(c.repoKey) == nil {
		if readErr != nil {
			return "", fmt.Errorf("There is no test %s.", id)
		}
		return b.String() + "\nNo run of this repository yet.", nil
	}
	v, err := c.runner.TestDetail(c.repoKey, id)
	if err != nil {
		return "", notFound(err, "test "+id)
	}
	if v.Test == nil && len(v.Runs) == 0 && readErr != nil {
		return "", fmt.Errorf("There is no test %s.", id)
	}
	if len(v.Cells) > 0 {
		b.WriteString("\nCells, as they stood in the latest runs:\n")
		for _, h := range v.Cells {
			fmt.Fprintf(&b, "- %s: ", h.CellID)
			if h.Latest == "" {
				b.WriteString("never run\n")
				continue
			}
			fmt.Fprintf(&b, "%s in run %d (%s, attempt %s); passed %d of %d run(s)", h.Latest, h.Number, h.Run, h.Attempt, h.Passes, h.Runs)
			if h.Since != nil {
				fmt.Fprintf(&b, "; a problem since run %d", h.Since.Number)
			}
			b.WriteString("\n")
		}
	}
	if len(v.Bugs) > 0 {
		b.WriteString("\nBugs:\n")
		for _, bug := range v.Bugs {
			b.WriteString(bugLine(bug) + "\n")
		}
	}
	return b.String(), nil
}
