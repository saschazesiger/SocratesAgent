// Package testtools is the tools of a test session (docs/design/TEST-MODE.md):
// what the agent of a cell, a reviewer or an analyst of a test run is given
// instead of ask_user and the chat tools, on the same Socrates server and
// through the same endpoint (chattools.Endpoint, whose Kind names the role).
// The calls are answered by the server's test runner (internal/testmode).
//
// The protocol each role is told in its instructions is here too, so that the
// text and the tools it names cannot drift apart.
package testtools

import (
	"strings"
)

// The roles of a test session, as chattools.Endpoint.Kind names them. The
// empty kind is an ordinary chat.
const (
	KindCell     = "test-cell"
	KindReviewer = "test-review"
	KindAnalyst  = "test-analyst"
)

// IsTest is whether kind is one of a test session's.
func IsTest(kind string) bool {
	return kind == KindCell || kind == KindReviewer || kind == KindAnalyst
}

// The tools' names, bare. An agent prefixes them as it does ask_user's
// (Claude Code: mcp__socrates__test_judge; OpenCode: socrates_test_judge;
// Codex: the bare name).
const (
	Steps = "test_steps"
	Judge = "test_judge"
	End   = "test_end"
	Abort = "test_abort"

	ReviewList     = "review_list"
	ReviewEvidence = "review_evidence"
	ReviewAgent    = "review_agent"
	ReviewStep     = "review_step"
	ReviewDone     = "review_done"

	RunOverview    = "run_overview"
	RunCell        = "run_cell"
	BugsPut        = "bugs_put"
	ConclusionsPut = "conclusions_put"
)

// The verdicts, worst first (Worse orders them).
const (
	Fail    = "fail"
	Blocked = "blocked"
	Issue   = "issue"
	Pass    = "pass"
	NA      = "na"
)

// Verdicts is every verdict, worst first.
var Verdicts = []string{Fail, Blocked, Issue, Pass, NA}

// ValidVerdict reports whether v is a verdict.
func ValidVerdict(v string) bool {
	for _, x := range Verdicts {
		if x == v {
			return true
		}
	}
	return false
}

// Rank is a verdict's place, 0 the worst; an unknown verdict ranks last.
func Rank(v string) int {
	for i, x := range Verdicts {
		if x == v {
			return i
		}
	}
	return len(Verdicts)
}

// Worse is the worse of two verdicts; "" counts as none.
func Worse(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	case Rank(b) < Rank(a):
		return b
	}
	return a
}

// Severities of a bug, worst first.
var Severities = []string{"critical", "major", "minor", "cosmetic"}

// Def is one tool as an agent is told about it.
type Def struct {
	Name        string
	Title       string
	Description string
	Schema      map[string]any
}

func object(props map[string]any, required ...string) map[string]any {
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

func verdict(desc string) map[string]any {
	return map[string]any{"type": "string", "enum": Verdicts, "description": desc}
}

var cellArg = str("The cell's id, as review_list or run_overview names it: the test id, `@`, and its dimension values (web-browser/header-navigation@pixel.es).")

// Tools is the tools of a role, in the order they are listed; none for an
// ordinary chat.
func Tools(kind string) []Def {
	switch kind {
	case KindCell:
		return []Def{
			{Name: Steps, Title: "Show the test's steps",
				Description: "Shows the steps of this cell's test with what each expects, the verdicts given so far and the next step to judge.",
				Schema:      object(map[string]any{})},
			{Name: Judge, Title: "Judge a step",
				Description: "Judges one step of the test (or every step not judged yet, with rest). The evidence actions the tools " +
					"recorded since the previous judgement become this step's evidence, so judge each step right after carrying " +
					"it out. A pass, issue or fail needs a screenshot among them (or among attach) unless the area says otherwise. " +
					"Judging a step again replaces its verdict. Describe what is on screen, not what the step should have done.",
				Schema: object(map[string]any{
					"step":     map[string]any{"type": "integer", "minimum": 1, "description": "The step's number. Leave out with rest."},
					"rest":     map[string]any{"type": "boolean", "description": "Judge every step not judged yet the same way (blocked: not reached)."},
					"verdict":  verdict("pass, issue, fail, blocked or na (see the verdict table in your instructions)."),
					"observed": str("What you saw: on screen, in the output. One to three sentences."),
					"attach": map[string]any{"type": "array", "items": map[string]any{"type": "string"},
						"description": "Files to keep as evidence besides the tools' own (absolute paths or paths in the evidence folder): screenshots you took another way, logs."},
				}, "verdict", "observed")},
			{Name: End, Title: "End the cell",
				Description: "Ends the cell once every step is judged. The summary is the conclusion in one or two sentences: what works and what does not, in this cell.",
				Schema:      object(map[string]any{"summary": str("The conclusion in one or two sentences.")}, "summary")},
			{Name: Abort, Title: "Abort the cell",
				Description: "Aborts the cell when it cannot go on (the device session died, the tool keeps failing). Every step not judged becomes blocked; the cell may be retried.",
				Schema:      object(map[string]any{"reason": str("Why the cell cannot go on.")}, "reason")},
		}
	case KindReviewer:
		return []Def{
			{Name: ReviewList, Title: "List the cells to review",
				Description: "Lists the cells this review is for, with why each is reviewed and whether it is done.",
				Schema:      object(map[string]any{})},
			{Name: ReviewEvidence, Title: "Read a cell's evidence",
				Description: "A cell's steps with what each expects, its actions and the absolute paths of its screenshots, " +
					"without the agent's verdicts. Look at every screenshot and decide each step before you read the agent's (review_agent).",
				Schema: object(map[string]any{"cell": cellArg}, "cell")},
			{Name: ReviewAgent, Title: "Read the agent's verdicts",
				Description: "The verdicts, observations and summary the cell's agent gave.",
				Schema:      object(map[string]any{"cell": cellArg}, "cell")},
			{Name: ReviewStep, Title: "Correct a step",
				Description: "Sets a step's reviewed verdict where the evidence shows something else than the agent said, with a note on what the screenshots show.",
				Schema: object(map[string]any{
					"cell":    cellArg,
					"step":    map[string]any{"type": "integer", "minimum": 1},
					"verdict": verdict("The verdict the evidence supports."),
					"note":    str("What the screenshots show."),
				}, "cell", "step", "verdict", "note")},
			{Name: ReviewDone, Title: "Close a cell's review",
				Description: "Closes a cell's review with one sentence: what was checked and what changed.",
				Schema:      object(map[string]any{"cell": cellArg, "note": str("What was checked and what changed.")}, "cell", "note")},
		}
	case KindAnalyst:
		bug := object(map[string]any{
			"id":          str("B1, B2, ... in order of severity."),
			"title":       str("One line."),
			"severity":    map[string]any{"type": "string", "enum": Severities},
			"cells":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "The ids of the cells that show it."},
			"description": str("Markdown: what happens, where, how to reproduce, the suspected cause with file references."),
		}, "id", "title", "severity", "cells", "description")
		return []Def{
			{Name: RunOverview, Title: "Overview of the run",
				Description: "Every cell of the run's latest attempts: its verdict (the reviewed one where a reviewer changed it), the observations of its non-pass steps and its summary.",
				Schema: object(map[string]any{
					"verdict": verdict("Only cells with this verdict."),
					"area":    str("Only cells of this area."),
				})},
			{Name: RunCell, Title: "One cell in full",
				Description: "A cell's steps, verdicts, observations, review notes, actions and screenshot paths.",
				Schema:      object(map[string]any{"cell": cellArg}, "cell")},
			{Name: BugsPut, Title: "Write the bug list",
				Description: "Writes the run's bug list, replacing the one before: one bug per root cause, not per cell.",
				Schema: object(map[string]any{
					"bugs": map[string]any{"type": "array", "items": bug},
				}, "bugs")},
			{Name: ConclusionsPut, Title: "Write the conclusions",
				Description: "Writes the run's conclusions in Markdown, replacing those before: per area, per column value (device), then the bugs by severity.",
				Schema:      object(map[string]any{"markdown": str("The conclusions.")}, "markdown")},
		}
	}
	return nil
}

// Has is whether kind offers a tool of that bare name.
func Has(kind, name string) bool {
	for _, d := range Tools(kind) {
		if d.Name == name {
			return true
		}
	}
	return false
}

// Names is the prefixed names of a role's tools, comma separated, for a
// ToolSearch select.
func Names(kind, prefix string) string {
	var out []string
	for _, d := range Tools(kind) {
		out = append(out, prefix+d.Name)
	}
	return strings.Join(out, ",")
}

const verdictTable = "| Verdict | When |\n|---|---|\n" +
	"| pass | Everything the step expects happens, and nothing looks wrong |\n" +
	"| issue | It works, but something is wrong a person would notice: layout, overlap, clipped or untranslated text, slowness, a confusing message |\n" +
	"| fail | It cannot be completed, gives a wrong result, loses data, hangs or shows an error |\n" +
	"| blocked | Not a product fault: infrastructure, the device, a third party refusing automation, test data missing, a step never reached |\n" +
	"| na | The step does not exist in this cell by design |\n\n" +
	"A cell takes its worst step: fail > blocked > issue > pass > na."

// Guidance is the protocol a role is told in its instructions, with the
// tools named as the agent knows them (prefix). toolSearch adds the line an
// agent that defers MCP tools behind ToolSearch (Claude Code) needs.
func Guidance(kind, prefix string, toolSearch bool) string {
	t := func(name string) string { return prefix + name }
	var b strings.Builder
	switch kind {
	case KindCell:
		b.WriteString("# You are running one cell of a test\n\n" +
			"Socrates runs a test catalog of this repository. You run exactly one cell: one test, on the values of its " +
			"dimensions (device, locale, ...) the first message names. Nobody watches this session: there is nobody to " +
			"ask, so decide yourself, and what you cannot settle is blocked.\n\n" +
			"Work the steps in order. For each step: carry it out with as many actions as it takes, using the tools " +
			"your instructions describe (they are on your PATH); look at every screenshot they give you; then call " +
			t(Judge) + " for the step at once, so that the actions since the last judgement become its evidence. " +
			"Judge only what you have seen: a claim about a price, a label or a count must be on a screenshot or in " +
			"the tool's output. Describe what is on screen, not what the step should have done. A step that cannot be " +
			"carried out is blocked; when the rest of the test cannot be reached, judge it with rest and verdict blocked " +
			"(\"not reached: ...\"). " + t(Steps) + " shows the steps, your verdicts so far and the next step.\n\n" +
			"When every step is judged, call " + t(End) + " with a summary of one or two sentences: what works and what " +
			"does not, in this cell. When the cell cannot go on at all (the device session died), call " + t(Abort) + ". " +
			"Then end your turn: the cell is over.\n\n" +
			"Rules: never edit, commit or delete anything in the repository; never read the file " +
			"$SOCRATES_TEST_VAULT or print a secret, a token, a password or a magic link (the tools type secrets " +
			"themselves); never touch another cell's device or files.\n\n" + verdictTable)
	case KindReviewer:
		b.WriteString("# You are reviewing cells of a test run\n\n" +
			"Agents ran cells of a test catalog and judged each step on the evidence the tools recorded. You check their " +
			"verdicts. You never run a test yourself and change nothing but the reviews.\n\n" +
			"Call " + t(ReviewList) + " for your cells. For each cell: read its evidence with " + t(ReviewEvidence) + " and " +
			"look at every screenshot of every step with your file reading tool; decide each step's verdict from the " +
			"evidence first, then read the agent's with " + t(ReviewAgent) + ". Where they differ, call " + t(ReviewStep) +
			" with the verdict the evidence supports and a note on what the screenshots show; a defensible borderline call " +
			"stays. Also correct an observation the screenshots do not support (a price, a label, \"nothing covers it\") " +
			"and a step that was not carried out as asked (a desktop path on a phone, a tap that never happened): its " +
			"verdict then is what the evidence shows, blocked if it shows nothing. Close every cell with " + t(ReviewDone) +
			" and one sentence: what was checked, what changed. End your turn when every cell is closed.\n\n" + verdictTable)
	case KindAnalyst:
		b.WriteString("# You are analysing a test run\n\n" +
			"The cells of a test run are done and reviewed. Read the run with " + t(RunOverview) + " and " + t(RunCell) +
			" (and the screenshots it names, with your file reading tool), and the repository's code where it helps to " +
			"name the cause. Then write:\n\n" +
			"1. The bug list with " + t(BugsPut) + ": one bug per root cause, not per cell, with its severity (critical " +
			"blocks a core flow for many; major breaks a flow or a device class; minor has a workaround; cosmetic), the " +
			"cells that show it, and a description: what happens, where, how to reproduce, the suspected cause with " +
			"file references and how sure you are. Blocked cells are not bugs of the product.\n" +
			"2. The conclusions with " + t(ConclusionsPut) + ": a paragraph per area, then a line per column value " +
			"(device), then the bugs by severity.\n\n" +
			"Change nothing in the repository. End your turn when both are written.\n\n" + verdictTable)
	default:
		return ""
	}
	if toolSearch {
		b.WriteString("\n\nLoad the tools with ToolSearch (select:" + Names(kind, prefix) + ") first.")
	}
	return b.String()
}

// Arguments of the calls, as the server reads them.

// JudgeArgs is test_judge's.
type JudgeArgs struct {
	Step     int      `json:"step"`
	Rest     bool     `json:"rest"`
	Verdict  string   `json:"verdict"`
	Observed string   `json:"observed"`
	Attach   []string `json:"attach"`
}

// EndArgs is test_end's.
type EndArgs struct {
	Summary string `json:"summary"`
}

// AbortArgs is test_abort's.
type AbortArgs struct {
	Reason string `json:"reason"`
}

// CellArgs is the argument of the calls about one cell.
type CellArgs struct {
	Cell string `json:"cell"`
}

// ReviewStepArgs is review_step's.
type ReviewStepArgs struct {
	Cell    string `json:"cell"`
	Step    int    `json:"step"`
	Verdict string `json:"verdict"`
	Note    string `json:"note"`
}

// ReviewDoneArgs is review_done's.
type ReviewDoneArgs struct {
	Cell string `json:"cell"`
	Note string `json:"note"`
}

// OverviewArgs is run_overview's.
type OverviewArgs struct {
	Verdict string `json:"verdict"`
	Area    string `json:"area"`
}

// Bug is one entry of bugs_put.
type Bug struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Severity    string   `json:"severity"`
	Cells       []string `json:"cells"`
	Description string   `json:"description"`
}

// BugsArgs is bugs_put's.
type BugsArgs struct {
	Bugs []Bug `json:"bugs"`
}

// ConclusionsArgs is conclusions_put's.
type ConclusionsArgs struct {
	Markdown string `json:"markdown"`
}
