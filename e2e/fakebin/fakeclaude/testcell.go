package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Socrates' test mode (docs/design/TEST-MODE.md): a test session's first
// message is the runner's, and the fake does what an agent would.
//
//	"Run this cell of the test catalog." runs the catalog's `shot <n>` for
//	        every step (the tool on PATH, which writes the evidence), judges
//	        each step pass and ends the cell. The test's parameter fake
//	        changes that: fail fails step 1, issue judges it issue, blocked
//	        blocks it (and the rest), flaky fails step 1 in runs whose number
//	        is odd (the header's "- Run: n"), slow waits about 2 s before
//	        each step and crawl 30 s, abort aborts at once, quiet ends the
//	        turn without a word. fail, issue, blocked, slow and crawl followed
//	        by from<n> or until<n> (failfrom5, issueuntil3) do so only in the
//	        runs numbered n and later, or before n. Anything after that first
//	        message is answered with words only.
//	"Review these cells" closes every cell of the review unchanged.
//	"Analyse test run" reads the known bugs and the run, writes one bug per
//	        test with cells that are issue or fail (under the id of a known
//	        bug that names a cell of the test; critical for a fail of a P0,
//	        major for another fail, minor for an issue, cosmetic for one of
//	        a P2) and the conclusions.

var (
	stepLine  = regexp.MustCompile(`(?m)^(\d+)\. `)
	fakeParam = regexp.MustCompile(`(?m)^- Parameters: .*\bfake=(\w+)`)
	runLine   = regexp.MustCompile(`(?m)^- Run: (\d+)$`)
	cellLine  = regexp.MustCompile(`(?m)^- (\S+@\S*|\S+/\S+)$`)
	// overviewLine is a cell of run_overview: its id, title, priority and
	// verdict.
	overviewLine = regexp.MustCompile(`(?m)^- (\S+) \((.*), (P\d)\): (\w+)`)
	// knownHead and knownCells are a bug of bugs_known.
	knownHead  = regexp.MustCompile(`(?m)^## (B-\d+) \[`)
	knownCells = regexp.MustCompile(`(?m)^Cells: (.*)$`)
	// phased is a mode that holds only in some runs.
	phased = regexp.MustCompile(`^(fail|issue|blocked|slow|crawl)(from|until)(\d+)$`)
)

// modeIn is a test's fake mode as it holds in run n.
func modeIn(mode string, n int) string {
	m := phased.FindStringSubmatch(mode)
	if m == nil {
		return mode
	}
	at, _ := strconv.Atoi(m[3])
	if m[2] == "from" && n >= at || m[2] == "until" && n < at {
		return m[1]
	}
	return ""
}

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
	run := 0
	if m := runLine.FindStringSubmatch(text); m != nil {
		run, _ = strconv.Atoi(m[1])
	}
	mode = modeIn(mode, run)
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
		if pause := map[string]time.Duration{"slow": 2 * time.Second, "crawl": 30 * time.Second}[mode]; pause > 0 {
			select {
			case <-t.ctx.Done():
				return
			case <-time.After(pause):
			}
		}
		cmd := exec.CommandContext(t.ctx, "shot", fmt.Sprint(i))
		out, err := cmd.CombinedOutput()
		t.tool("Bash", map[string]any{"command": fmt.Sprintf("shot %d", i), "description": "Take a screenshot"},
			string(out), map[string]any{"stdout": string(out), "stderr": "", "interrupted": false}, err != nil, "")
		verdict, observed := "pass", fmt.Sprintf("Step %d shows what it should.", i)
		if i == 1 {
			switch {
			case mode == "fail", mode == "flaky" && run%2 == 1:
				verdict, observed = "fail", "The page shows an error instead."
			case mode == "issue":
				verdict, observed = "issue", "It works, but the button overlaps the text."
			case mode == "blocked":
				t.mcpTool("test_judge " + toJSON(map[string]any{"rest": true, "verdict": "blocked",
					"observed": "not reached: the fake device was not available"}))
				t.mcpTool(`test_end {"summary":"The fake device was not available."}`)
				return
			}
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
	known, _ := t.mcpTool(`bugs_known {}`)
	// The known bugs by the tests of their cells.
	byTest := map[string]string{}
	heads := knownHead.FindAllStringSubmatchIndex(known, -1)
	for i, h := range heads {
		end := len(known)
		if i+1 < len(heads) {
			end = heads[i+1][0]
		}
		id := known[h[2]:h[3]]
		if m := knownCells.FindStringSubmatch(known[h[0]:end]); m != nil {
			for _, cell := range strings.Split(m[1], ", ") {
				test, _, _ := strings.Cut(strings.TrimSpace(cell), "@")
				if _, ok := byTest[test]; !ok {
					byTest[test] = id
				}
			}
		}
	}
	over, _ := t.mcpTool(`run_overview {}`)
	cells := map[string][]string{}
	titles := map[string]string{}
	severity := map[string]string{}
	rank := map[string]int{"critical": 4, "major": 3, "minor": 2, "cosmetic": 1}
	var tests []string
	for _, m := range overviewLine.FindAllStringSubmatch(over, -1) {
		if m[4] != "issue" && m[4] != "fail" {
			continue
		}
		test, _, _ := strings.Cut(m[1], "@")
		if _, ok := cells[test]; !ok {
			tests = append(tests, test)
		}
		cells[test] = append(cells[test], m[1])
		titles[test] = m[2]
		sev := "minor"
		switch {
		case m[4] == "fail" && m[3] == "P0":
			sev = "critical"
		case m[4] == "fail":
			sev = "major"
		case m[3] == "P2":
			sev = "cosmetic"
		}
		if rank[sev] > rank[severity[test]] {
			severity[test] = sev
		}
	}
	sort.Strings(tests)
	bugs := []map[string]any{}
	for _, test := range tests {
		bugs = append(bugs, map[string]any{"id": byTest[test], "title": "The fake failure of " + test, "severity": severity[test],
			"cells": cells[test], "description": "Seen by the fake in " + titles[test] + "."})
	}
	t.mcpTool("bugs_put " + toJSON(map[string]any{"bugs": bugs}))
	t.mcpTool(`conclusions_put {"markdown":"# Conclusions\n\nThe fake run is analysed."}`)
}
