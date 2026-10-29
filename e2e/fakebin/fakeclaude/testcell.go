package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// Socrates' test mode (docs/design/TEST-MODE.md): a test session's first
// message is the runner's, and the fake does what an agent would.
//
//	"Run this cell of the test catalog." runs the catalog's `shot <n>` for
//	        every step (the tool on PATH, which writes the evidence), judges
//	        each step pass and ends the cell; the test's parameter fake=fail
//	        fails step 1, fake=abort aborts at once, fake=quiet ends the turn
//	        without a word, and anything after that first message is
//	        answered with words only.
//	"Review these cells" closes every cell of the review unchanged.
//	"Analyse test run" writes one bug per cell that did not pass and the
//	        conclusions.

var (
	stepLine  = regexp.MustCompile(`(?m)^(\d+)\. `)
	fakeParam = regexp.MustCompile(`(?m)^- Parameters: .*\bfake=(\w+)`)
	cellLine  = regexp.MustCompile(`(?m)^- (\S+@\S*|\S+/\S+)$`)
)

// testSession runs the test mode's scripts; false for a message that is
// none of them.
func (t *turnRun) testSession(text string) bool {
	switch {
	case strings.HasPrefix(text, "Run this cell of the test catalog."):
		t.testCell(text)
	case strings.HasPrefix(text, "Review these cells"):
		t.testReview(text)
	case strings.HasPrefix(text, "Analyse test run"):
		t.testAnalysis()
	default:
		return false
	}
	return true
}

func toJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

func (t *turnRun) testCell(text string) {
	mode := ""
	if m := fakeParam.FindStringSubmatch(text); m != nil {
		mode = m[1]
	}
	_, steps, _ := strings.Cut(text, "## Steps")
	n := len(stepLine.FindAllString(steps, -1))
	switch mode {
	case "quiet":
		t.say("Nothing to do.")
		return
	case "abort":
		t.mcpTool(`test_abort {"reason":"the fake device went away"}`)
		return
	}
	for i := 1; i <= n && !t.cut; i++ {
		cmd := exec.CommandContext(t.ctx, "shot", fmt.Sprint(i))
		out, err := cmd.CombinedOutput()
		t.tool("Bash", map[string]any{"command": fmt.Sprintf("shot %d", i), "description": "Take a screenshot"},
			string(out), map[string]any{"stdout": string(out), "stderr": "", "interrupted": false}, err != nil, "")
		verdict, observed := "pass", fmt.Sprintf("Step %d shows what it should.", i)
		if mode == "fail" && i == 1 {
			verdict, observed = "fail", "The page shows an error instead."
		}
		t.mcpTool("test_judge " + toJSON(map[string]any{"step": i, "verdict": verdict, "observed": observed}))
	}
	if !t.cut {
		t.mcpTool(`test_end {"summary":"The fake worked every step."}`)
	}
}

func (t *turnRun) testReview(text string) {
	for _, m := range cellLine.FindAllStringSubmatch(text, -1) {
		if t.cut {
			return
		}
		cell := m[1]
		t.mcpTool("review_evidence " + toJSON(map[string]any{"cell": cell}))
		t.mcpTool("review_agent " + toJSON(map[string]any{"cell": cell}))
		t.mcpTool("review_done " + toJSON(map[string]any{"cell": cell, "note": "The screenshots show what the agent said."}))
	}
}

func (t *turnRun) testAnalysis() {
	t.mcpTool(`run_overview {}`)
	t.mcpTool(`bugs_put {"bugs":[{"id":"B1","title":"The fake failure","severity":"major","cells":[],"description":"Seen by the fake."}]}`)
	t.mcpTool(`conclusions_put {"markdown":"# Conclusions\n\nThe fake run is analysed."}`)
}
