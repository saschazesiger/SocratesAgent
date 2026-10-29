package chattools

import "fmt"

// The test tools of a chat (docs/design/TEST-MODE.md, "Runs started by chat
// agents"): a chat whose repository has a test catalog, or that was started
// to write one, gets four more tools on the same server as the chat tools,
// through the same endpoint (Endpoint.Tests). They are not a test session's
// tools (internal/agent/testtools): those work one cell of a run, these
// start runs and read their results.
//
//	tests_format   the catalog format reference
//	tests_check    validates the repository's catalog
//	run_tests      starts a run of a selection of the catalog
//	test_results   a run's results in Markdown
const (
	TestsFormat = "tests_format"
	TestsCheck  = "tests_check"
	RunTests    = "run_tests"
	TestResults = "test_results"
)

// MaxResults is how much of a run's report test_results gives at most.
const MaxResults = 40 << 10

// RunTestsArgs is run_tests' arguments. Every list that is set narrows the
// selection; none set is the whole catalog.
type RunTestsArgs struct {
	Areas      []string            `json:"areas,omitempty"`
	Tests      []string            `json:"tests,omitempty"`
	Cells      []string            `json:"cells,omitempty"`
	Tags       []string            `json:"tags,omitempty"`
	Priorities []string            `json:"priorities,omitempty"`
	Dims       map[string][]string `json:"dims,omitempty"`
	// ProblemsOf is "latest" or a run's id: only the cells of that run that
	// did not pass.
	ProblemsOf string `json:"problems_of,omitempty"`
	Name       string `json:"name,omitempty"`
}

// TestResultsArgs is test_results' arguments.
type TestResultsArgs struct {
	// Run is a run's id, or "latest" (also when empty).
	Run string `json:"run,omitempty"`
	// ProblemsOnly leaves out the cells that passed; on when left out.
	ProblemsOnly *bool `json:"problems_only,omitempty"`
}

// TestTools is the four tools, in the order they are listed.
func TestTools() []Def {
	list := func(desc string) map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
	}
	return []Def{
		{
			Name:  TestsFormat,
			Title: "The test catalog's format",
			Description: "The reference of the test catalog Socrates runs (.socrates/tests/ in the repository): the " +
				"folders, TESTS.md, AREA.md, a test file with its steps, TOOL.md and the tool contract, the verdicts. " +
				"Read it before you write or change a test.",
			Schema: map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name:  TestsCheck,
			Title: "Check the test catalog",
			Description: "Validates this repository's test catalog as Socrates does before a run: every error (a run " +
				"does not start) and warning, with its file and line, and the areas, tests and cells it has. Run it " +
				"after every change to a test.",
			Schema: map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name:  RunTests,
			Title: "Run tests",
			Description: "Starts a run of this repository's test catalog with the repository's settings: every cell " +
				"(a test on one device, locale ...) is worked by an agent of its own, reviewed and analysed, which takes " +
				"minutes to hours. Narrow it to what you need: every list that is set narrows the selection, none set " +
				"runs everything. It answers at once with the run and its cell count; when the run ends, Socrates sends " +
				"the results to this chat as a message, which starts your next turn. So end your turn after it: do not " +
				"wait or poll.",
			Schema: map[string]any{"type": "object", "properties": map[string]any{
				"areas":      list("Areas (the catalog's top-level folders, such as web)."),
				"tests":      list("Test ids: the file's path in the catalog without .md, such as web/checkout."),
				"cells":      list("Cell ids: a test id, @, and its dimension values joined by '.', such as web/checkout@pixel.es."),
				"tags":       list("Tags: the tests that have one of them."),
				"priorities": list("Priorities: P0, P1, P2."),
				"dims": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"description": "Per dimension its values to run on, such as {\"device\": [\"pixel\"]}."},
				"problems_of": map[string]any{"type": "string", "description": "\"latest\" or a run's id: only the cells of that run that did not pass."},
				"name":        map[string]any{"type": "string", "description": "A short name for the run, shown on the Tests page."},
			}},
		},
		{
			Name:  TestResults,
			Title: "Read a test run's results",
			Description: "A run of this repository's tests in Markdown: its counts, every cell that did not pass with " +
				"its failing steps, what the agent observed and the paths of its screenshots (open them with your file " +
				"tools), the bugs and the conclusions. " + fmt.Sprintf("At most %d KB.", MaxResults>>10),
			Schema: map[string]any{"type": "object", "properties": map[string]any{
				"run":           map[string]any{"type": "string", "description": "The run's id; the latest run of the repository when left out."},
				"problems_only": map[string]any{"type": "boolean", "description": "Only the cells that did not pass (the default); false lists every cell."},
			}},
		},
	}
}

// IsTestTool is whether name (bare) is one of TestTools.
func IsTestTool(name string) bool {
	return name == TestsFormat || name == TestsCheck || name == RunTests || name == TestResults
}

// TestGuidance is what an agent's instructions say about the test tools:
// when to use them. prefix is Guidance's.
func TestGuidance(prefix string) string {
	format, check, run, results := prefix+TestsFormat, prefix+TestsCheck, prefix+RunTests, prefix+TestResults
	return "This repository has a test catalog Socrates runs (.socrates/tests/): agents work through its tests on " +
		"real devices and judge every step on screenshots.\n" +
		"- After you fixed something the tests cover, run the tests concerned with " + run + ": as narrow as " +
		"the change (the tests or cells it affects, or problems_of \"latest\" to run again what failed), never " +
		"everything without being asked. Then end your turn: the results arrive here as a message when the run ends.\n" +
		"- When they arrive, read the details with " + results + " (its screenshots are files you can open) " +
		"before you fix anything else.\n" +
		"- To write or change a test, read " + format + " first and check your work with " + check + " until it " +
		"has no errors."
}
