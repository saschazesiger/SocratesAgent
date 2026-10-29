//go:build !windows

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent/testtools"
)

// The scripted agent in a test session (docs/design/TEST-MODE.md): when the
// chat is a test session (StartSpec.ChatTools names a role), the runner's
// first message is answered the way an agent would, through the role's test
// tools on the same endpoint the real harnesses' calls reach
// (chattools.Endpoint.Call). The modes are e2e/fakebin/fakeclaude's
// (testcell.go), so that a catalog works with either.
//
//	"Run this cell of the test catalog." takes a screenshot for every step
//	        and judges it pass, then ends the cell. The screenshot is the
//	        catalog's `shot <n>` when a tool puts one on the session's PATH
//	        (as fakeclaude does); otherwise the agent draws one of its own (a
//	        page of a shop in the shape of the cell's device, an error
//	        banner on a failed step) into $SOCRATES_TEST_EVIDENCE with its
//	        line in actions.jsonl; without either it judges without
//	        evidence. The test's parameter fake changes that: fail fails
//	        step 1, issue judges it issue, blocked blocks it (and the rest),
//	        flaky fails step 1 in runs whose number is odd, slow waits about
//	        2 s before each step and crawl 30 s, abort aborts at once, quiet
//	        ends the turn without a word, misjudged judges step 1 pass
//	        although its action failed (a reviewer turns it into an issue).
//	        fail, issue, blocked, slow, crawl and misjudged followed by
//	        from<n> or until<n> (failfrom5, issueuntil3) do so only in the
//	        runs numbered n and later, or before n.
//	"Review these cells" reads every cell's evidence and the agent's
//	        verdicts, sets a step the agent passed although its last action
//	        failed to issue, and closes the cell.
//	"Analyse test run" reads the known bugs and the run, writes one bug per
//	        test with cells that are issue or fail (under the id of a known
//	        bug that names a cell of the test, none for one marked
//	        not_a_bug; critical for a fail of a P0, major for another fail,
//	        minor for an issue, cosmetic for one of a P2) and the
//	        conclusions: a line per area, per device and the bugs.
//
// Anything else in a test session is answered as in a chat.

var (
	fakeStepStart  = regexp.MustCompile(`(?m)^(\d+)\. `)
	fakeExpect     = regexp.MustCompile(`(?m)^\s*Expect: (.*)$`)
	fakeParam      = regexp.MustCompile(`(?m)^- Parameters: .*\bfake=(\w+)`)
	fakeRunLine    = regexp.MustCompile(`(?m)^- Run: (\d+)$`)
	fakeCellLine   = regexp.MustCompile(`(?m)^- (\S+@\S*|\S+/\S+)$`)
	fakeOverview   = regexp.MustCompile(`(?m)^- (\S+) \((.*), (P\d)\): (\w+)`)
	fakeKnownHead  = regexp.MustCompile(`(?m)^## (B-\d+) \[(\w+),`)
	fakeKnownCells = regexp.MustCompile(`(?m)^Cells: (.*)$`)
	fakeStepHead   = regexp.MustCompile(`(?m)^## Step (\d+):`)
	fakeAgentStep  = regexp.MustCompile(`(?m)^(\d+)\. (\w+) - `)
	fakePhased     = regexp.MustCompile(`^(fail|issue|blocked|slow|crawl|misjudged)(from|until)(\d+)$`)
)

// The first words of the runner's messages to each role.
const (
	fakeCellPrompt     = "Run this cell of the test catalog."
	fakeReviewPrompt   = "Review these cells"
	fakeAnalysisPrompt = "Analyse test run"
)

// fakeModeIn is a test's fake mode as it holds in run n.
func fakeModeIn(mode string, n int) string {
	m := fakePhased.FindStringSubmatch(mode)
	if m == nil {
		return mode
	}
	at, _ := strconv.Atoi(m[3])
	if m[2] == "from" && n >= at || m[2] == "until" && n < at {
		return m[1]
	}
	return ""
}

// testSession works a test session's message: handled is false for a
// message that is none of the runner's, ok false when the turn was
// interrupted.
func (f *fakeAdapter) testSession(ctx context.Context, t *fakeTurn, text string) (handled, ok bool) {
	if !f.spec.ChatTools.Test() {
		return false, false
	}
	switch kind := f.spec.ChatTools.Kind; {
	case kind == testtools.KindCell && strings.HasPrefix(text, fakeCellPrompt):
		return true, f.testCell(ctx, t, text)
	case kind == testtools.KindReviewer && strings.HasPrefix(text, fakeReviewPrompt):
		return true, f.testReview(ctx, t, text)
	case kind == testtools.KindAnalyst && strings.HasPrefix(text, fakeAnalysisPrompt):
		return true, f.testAnalysis(ctx, t)
	}
	return false, false
}

// testCall is one call of a test tool, shown as a tool part the way Claude
// Code's MCP calls are; ok is false when the turn was interrupted.
func (f *fakeAdapter) testCall(ctx context.Context, t *fakeTurn, name string, args any) (text string, failed, ok bool) {
	raw := json.RawMessage(`{}`)
	if args != nil {
		raw, _ = json.Marshal(args)
	}
	id := t.msgID + "_" + fakeID()
	content := map[string]any{"tool": "mcp__socrates__" + name, "tool_use_id": id, "input": raw, "summary": name}
	running, _ := json.Marshal(content)
	f.out.Emit(Event{Type: TypePart, MessageID: t.msgID, PartID: id, Kind: PartTool, State: PartRunning, Content: running})
	started := time.Now()
	resp := f.spec.ChatTools.Call(ctx, name, raw)
	if ctx.Err() != nil {
		return "", true, false
	}
	state := PartDone
	if resp.Error {
		state, content["is_error"] = PartError, true
	}
	content["duration_ms"] = time.Since(started).Milliseconds()
	done, _ := json.Marshal(content)
	f.out.Emit(Event{Type: TypePart, MessageID: t.msgID, PartID: id, Kind: PartTool, State: state, Content: done, Text: resp.Text})
	return resp.Text, resp.Error, true
}

// fakeStep is a step of a cell's first message.
type fakeStep struct {
	n      int
	expect string
}

// fakeSteps is the steps of a cell's first message.
func fakeSteps(text string) []fakeStep {
	_, body, _ := strings.Cut(text, "## Steps")
	starts := fakeStepStart.FindAllStringSubmatchIndex(body, -1)
	var out []fakeStep
	for i, s := range starts {
		end := len(body)
		if i+1 < len(starts) {
			end = starts[i+1][0]
		}
		n, _ := strconv.Atoi(body[s[2]:s[3]])
		step := fakeStep{n: n}
		if m := fakeExpect.FindStringSubmatch(body[s[0]:end]); m != nil {
			step.expect = strings.TrimSpace(m[1])
		}
		out = append(out, step)
	}
	return out
}

func (f *fakeAdapter) testCell(ctx context.Context, t *fakeTurn, text string) bool {
	mode := ""
	if m := fakeParam.FindStringSubmatch(text); m != nil {
		mode = m[1]
	}
	run := 0
	if m := fakeRunLine.FindStringSubmatch(text); m != nil {
		run, _ = strconv.Atoi(m[1])
	}
	mode = fakeModeIn(mode, run)
	device, _ := lookupEnv(f.spec.Env, "SOCRATES_TEST_DIM_DEVICE")
	switch mode {
	case "quiet":
		t.say("Nothing to do.")
		return true
	case "abort":
		if _, _, ok := f.testCall(ctx, t, testtools.Abort, map[string]any{"reason": "the device session went away before the first step"}); !ok {
			return false
		}
		t.say("The cell is aborted: the device went away.")
		return true
	}
	pause := map[string]time.Duration{"slow": 2 * time.Second, "crawl": 30 * time.Second}[mode]
	if pause == 0 {
		pause = 150 * time.Millisecond
	}
	worst := testtools.Pass
	for _, step := range fakeSteps(text) {
		if !fakeWait(ctx, pause) {
			return false
		}
		verdict, observed := testtools.Pass, "The page shows what the step expects: "+strings.TrimSuffix(step.expect, ".")+"."
		if step.expect == "" {
			observed = fmt.Sprintf("Step %d shows what it should.", step.n)
		}
		actionErr := ""
		if step.n == 1 {
			switch {
			case mode == "fail", mode == "flaky" && run%2 == 1:
				verdict, observed = testtools.Fail, `The page shows an error instead: "Something went wrong. Please try again."`
			case mode == "issue":
				verdict, observed = testtools.Issue, "It works, but the button overlaps the text above it."
			case mode == "blocked":
				verdict = testtools.Blocked
			case mode == "misjudged":
				observed, actionErr = "The page shows what the step expects after a second tap.", "the first tap on the button did nothing"
			}
		}
		if !f.testEvidence(ctx, t, step.n, device, verdict, actionErr) {
			return false
		}
		args := map[string]any{"step": step.n, "verdict": verdict, "observed": observed}
		if verdict == testtools.Blocked {
			args = map[string]any{"rest": true, "verdict": verdict, "observed": "not reached: the device was not available"}
		}
		if _, _, ok := f.testCall(ctx, t, testtools.Judge, args); !ok {
			return false
		}
		worst = testtools.Worse(worst, verdict)
		if verdict == testtools.Blocked {
			break
		}
	}
	summary := map[string]string{
		testtools.Pass:    "Every step works on this device.",
		testtools.Issue:   "It works, but the first step shows a layout problem.",
		testtools.Fail:    "The first step fails with an error; the rest works.",
		testtools.Blocked: "The device was not available, so nothing could be tested.",
	}[worst]
	if _, _, ok := f.testCall(ctx, t, testtools.End, map[string]any{"summary": summary}); !ok {
		return false
	}
	t.say(summary)
	return true
}

// testEvidence takes step n's screenshot: the catalog's shot when it is on
// the session's PATH, else one of the agent's own when the session has an
// evidence folder.
func (f *fakeAdapter) testEvidence(ctx context.Context, t *fakeTurn, n int, device, verdict, actionErr string) bool {
	if shot, err := ResolveBinary(f.spec.Env, "shot"); err == nil {
		cmd := exec.CommandContext(ctx, shot, strconv.Itoa(n))
		cmd.Env, cmd.Dir = f.spec.Env, f.spec.Workdir
		out, err := cmd.CombinedOutput()
		if ctx.Err() != nil {
			return false
		}
		state := PartDone
		if err != nil {
			state = PartError
		}
		input := map[string]any{"command": fmt.Sprintf("shot %d", n), "description": "Take a screenshot"}
		raw, _ := json.Marshal(map[string]any{"tool": "Bash", "tool_use_id": t.msgID + "_shot" + strconv.Itoa(n), "input": input,
			"command": input["command"], "summary": input["command"]})
		f.out.Emit(Event{Type: TypePart, MessageID: t.msgID, PartID: t.msgID + "_shot" + strconv.Itoa(n), Kind: PartTool,
			State: state, Content: raw, Text: string(out)})
		return true
	}
	dir, _ := lookupEnv(f.spec.Env, "SOCRATES_TEST_EVIDENCE")
	if dir == "" {
		return true
	}
	locale, _ := lookupEnv(f.spec.Env, "SOCRATES_TEST_DIM_LOCALE")
	name := fmt.Sprintf("step-%d.png", n)
	pic := fakeScreenshot(device, n, verdict, actionErr != "")
	if err := os.WriteFile(filepath.Join(dir, name), pic, 0o644); err != nil {
		t.say("Could not write the screenshot: " + err.Error() + "\n\n")
		return true
	}
	line := map[string]any{"at": time.Now().UTC().Format(time.RFC3339Nano), "tool": "scripted", "action": "screenshot",
		"summary": fmt.Sprintf("step %d on %s", n, orDash(device)), "screenshot": name, "interaction": true, "ms": 150 + 40*n}
	if locale != "" {
		line["url"] = "https://shop.example/" + locale + "/"
		line["summary"] = fmt.Sprintf("step %d on %s in %s", n, orDash(device), locale)
	}
	if actionErr != "" {
		line["error"] = actionErr
	}
	raw, _ := json.Marshal(line)
	file, err := os.OpenFile(filepath.Join(dir, "actions.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err == nil {
		_, err = file.Write(append(raw, '\n'))
		if cerr := file.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		t.say("Could not record the screenshot: " + err.Error() + "\n\n")
	}
	return true
}

func orDash(s string) string {
	if s == "" {
		return "the device"
	}
	return s
}

func (f *fakeAdapter) testReview(ctx context.Context, t *fakeTurn, text string) bool {
	changed := 0
	cells := fakeCellLine.FindAllStringSubmatch(text, -1)
	for _, m := range cells {
		cell := m[1]
		evidence, _, ok := f.testCall(ctx, t, testtools.ReviewEvidence, map[string]any{"cell": cell})
		if !ok {
			return false
		}
		// The steps whose last action failed, by the evidence.
		failedAction := map[int]bool{}
		heads := fakeStepHead.FindAllStringSubmatchIndex(evidence, -1)
		for i, h := range heads {
			end := len(evidence)
			if i+1 < len(heads) {
				end = heads[i+1][0]
			}
			n, _ := strconv.Atoi(evidence[h[2]:h[3]])
			var last string
			for _, line := range strings.Split(evidence[h[0]:end], "\n") {
				if strings.HasPrefix(line, "- #") {
					last = line
				}
			}
			failedAction[n] = strings.Contains(last, " ERROR: ")
		}
		agent, _, ok := f.testCall(ctx, t, testtools.ReviewAgent, map[string]any{"cell": cell})
		if !ok {
			return false
		}
		note := "The screenshots show what the agent said."
		for _, s := range fakeAgentStep.FindAllStringSubmatch(agent, -1) {
			n, _ := strconv.Atoi(s[1])
			if s[2] != testtools.Pass || !failedAction[n] {
				continue
			}
			if _, _, ok := f.testCall(ctx, t, testtools.ReviewStep, map[string]any{"cell": cell, "step": n, "verdict": testtools.Issue,
				"note": "The action of this step failed (the first tap did nothing); it only worked when repeated."}); !ok {
				return false
			}
			note = fmt.Sprintf("Step %d passed although its action failed: it is an issue.", n)
			changed++
		}
		if _, _, ok := f.testCall(ctx, t, testtools.ReviewDone, map[string]any{"cell": cell, "note": note}); !ok {
			return false
		}
	}
	t.say(fmt.Sprintf("Reviewed %d cell(s); %d step(s) corrected.", len(cells), changed))
	return true
}

func (f *fakeAdapter) testAnalysis(ctx context.Context, t *fakeTurn) bool {
	known, _, ok := f.testCall(ctx, t, testtools.BugsKnown, nil)
	if !ok {
		return false
	}
	// The known bugs by the tests of their cells; a test whose bug the
	// person marked not a bug is not reported again.
	byTest := map[string]string{}
	notABug := map[string]bool{}
	heads := fakeKnownHead.FindAllStringSubmatchIndex(known, -1)
	for i, h := range heads {
		end := len(known)
		if i+1 < len(heads) {
			end = heads[i+1][0]
		}
		id, status := known[h[2]:h[3]], known[h[4]:h[5]]
		if m := fakeKnownCells.FindStringSubmatch(known[h[0]:end]); m != nil {
			for _, cell := range strings.Split(m[1], ", ") {
				test, _, _ := strings.Cut(strings.TrimSpace(cell), "@")
				if _, ok := byTest[test]; !ok {
					byTest[test] = id
					notABug[test] = status == "not_a_bug"
				}
			}
		}
	}
	over, _, ok := f.testCall(ctx, t, testtools.RunOverview, nil)
	if !ok {
		return false
	}
	type testBugs struct {
		title, severity string
		cells           []string
		fails           int
	}
	rank := map[string]int{"critical": 4, "major": 3, "minor": 2, "cosmetic": 1}
	found := map[string]*testBugs{}
	// What the conclusions count: per area and per device, cells and the
	// ones that did not pass.
	type tally struct{ cells, bad int }
	areas, devices := map[string]*tally{}, map[string]*tally{}
	count := func(m map[string]*tally, key string, bad bool) {
		if m[key] == nil {
			m[key] = &tally{}
		}
		m[key].cells++
		if bad {
			m[key].bad++
		}
	}
	for _, m := range fakeOverview.FindAllStringSubmatch(over, -1) {
		cell, title, prio, verdict := m[1], m[2], m[3], m[4]
		test, dims, _ := strings.Cut(cell, "@")
		area, _, _ := strings.Cut(test, "/")
		device, _, _ := strings.Cut(dims, ".")
		bad := verdict != testtools.Pass && verdict != testtools.NA
		count(areas, area, bad)
		if device != "" {
			count(devices, device, bad)
		}
		if verdict != testtools.Issue && verdict != testtools.Fail || notABug[test] {
			continue
		}
		b := found[test]
		if b == nil {
			b = &testBugs{title: title}
			found[test] = b
		}
		b.cells = append(b.cells, cell)
		sev := "minor"
		switch {
		case verdict == testtools.Fail && prio == "P0":
			sev = "critical"
		case verdict == testtools.Fail:
			sev = "major"
		case prio == "P2":
			sev = "cosmetic"
		}
		if verdict == testtools.Fail {
			b.fails++
		}
		if rank[sev] > rank[b.severity] {
			b.severity = sev
		}
	}
	tests := make([]string, 0, len(found))
	for test := range found {
		tests = append(tests, test)
	}
	sort.Strings(tests)
	bugs := []map[string]any{}
	for _, test := range tests {
		b := found[test]
		title := b.title + ": an error instead of the result"
		what := "The first step shows \"Something went wrong. Please try again.\" instead of what it expects."
		if b.fails == 0 {
			title = b.title + ": the layout overlaps"
			what = "The step works, but its button overlaps the text above it."
		}
		bugs = append(bugs, map[string]any{"id": byTest[test], "title": title, "severity": b.severity, "cells": b.cells,
			"description": fmt.Sprintf("%s\n\nSeen in %s. To reproduce: run the test `%s` on the device named in the cell. "+
				"The cause is not known (the Scripted agent does not read the code).", what, strings.Join(b.cells, ", "), test)})
	}
	if _, _, ok := f.testCall(ctx, t, testtools.BugsPut, map[string]any{"bugs": bugs}); !ok {
		return false
	}
	var md strings.Builder
	md.WriteString("## Areas\n\n")
	for _, key := range fakeSorted(areas) {
		a := areas[key]
		fmt.Fprintf(&md, "- **%s**: %d of %d cells passed.\n", key, a.cells-a.bad, a.cells)
	}
	if len(devices) > 0 {
		md.WriteString("\n## Devices\n\n")
		for _, key := range fakeSorted(devices) {
			d := devices[key]
			fmt.Fprintf(&md, "- **%s**: %d of %d cells passed.\n", key, d.cells-d.bad, d.cells)
		}
	}
	md.WriteString("\n## Bugs\n\n")
	if len(tests) == 0 {
		md.WriteString("None in this run.\n")
	}
	for _, sev := range testtools.Severities {
		for _, test := range tests {
			if b := found[test]; b.severity == sev {
				fmt.Fprintf(&md, "- %s: %s (%d cell(s))\n", sev, b.title, len(b.cells))
			}
		}
	}
	if _, _, ok := f.testCall(ctx, t, testtools.ConclusionsPut, map[string]any{"markdown": md.String()}); !ok {
		return false
	}
	t.say(fmt.Sprintf("The run is analysed: %d bug(s).", len(bugs)))
	return true
}

func fakeSorted[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// fakeScreenshot draws a page of a shop as a device of that kind shows it:
// a header, a product photo, lines of text, a price and a button. A failed
// step has an error banner, an issue a button over the text, a blocked one
// a grey screen; a failed action leaves the button pressed. The step picks
// the photo's colour.
func fakeScreenshot(device string, step int, verdict string, pressed bool) []byte {
	w, h, phone := 800, 500, false
	switch d := strings.ToLower(device); {
	case strings.Contains(d, "phone") || strings.Contains(d, "pixel") || strings.Contains(d, "android"):
		w, h, phone = 280, 600, true
	case strings.Contains(d, "tablet") || strings.Contains(d, "ipad") || strings.Contains(d, "tab"):
		w, h = 480, 640
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	fill := func(x0, y0, x1, y1 int, c color.RGBA) {
		draw.Draw(img, image.Rect(x0, y0, x1, y1), &image.Uniform{c}, image.Point{}, draw.Src)
	}
	page := color.RGBA{0xfa, 0xf8, 0xf5, 0xff}
	ink := color.RGBA{0x33, 0x33, 0x33, 0xff}
	soft := color.RGBA{0xc8, 0xc4, 0xbe, 0xff}
	brand := color.RGBA{0x1f, 0x5f, 0x5b, 0xff}
	accent := color.RGBA{0xe8, 0x74, 0x3b, 0xff}
	fill(0, 0, w, h, page)
	if verdict == testtools.Blocked {
		fill(0, 0, w, h, color.RGBA{0x55, 0x55, 0x55, 0xff})
		fill(w/2-w/6, h/2-8, w/2+w/6, h/2+8, color.RGBA{0x99, 0x99, 0x99, 0xff})
		return fakePNG(img)
	}
	m := w / 16
	header := h / 11
	fill(0, 0, w, header, brand)
	fill(m, header/3, m+header/3*4, header-header/3, color.RGBA{0xf2, 0xe8, 0xd5, 0xff})
	if phone {
		for i := 0; i < 3; i++ {
			y := header/3 + i*header/6
			fill(w-m-header/2, y, w-m, y+header/14+1, page)
		}
	} else {
		for i := 0; i < 4; i++ {
			x := w/2 + i*w/10
			fill(x, header/2-3, x+w/14, header/2+3, color.RGBA{0xd9, 0xe6, 0xe4, 0xff})
		}
	}
	y := header + m
	if verdict == testtools.Fail {
		fill(0, header, w, header+h/12, color.RGBA{0xc6, 0x28, 0x28, 0xff})
		fill(m, header+h/24-4, w-3*m, header+h/24+4, color.RGBA{0xff, 0xeb, 0xee, 0xff})
		y = header + h/12 + m
	}
	photos := []color.RGBA{{0xb7, 0xd3, 0xdf, 0xff}, {0xe5, 0xc8, 0x9c, 0xff}, {0xc5, 0xdc, 0xb5, 0xff}, {0xd8, 0xbf, 0xd8, 0xff}}
	photo := photos[(step-1+len(photos))%len(photos)]
	textX := m
	if phone {
		fill(m, y, w-m, y+h/3, photo)
		y += h/3 + m
	} else {
		fill(m, y, w/2-m/2, y+h/2, photo)
		textX = w/2 + m/2
	}
	fill(textX, y, w-m-(w-textX)/4, y+h/28, ink)
	y += h/28 + m/2
	for i := 0; i < 3; i++ {
		fill(textX, y, w-m-i*(w-textX)/9, y+h/60+1, soft)
		y += h/60 + m/2
	}
	fill(textX, y, textX+(w-textX)/4, y+h/30, accent)
	y += h/30 + m
	button := accent
	if pressed {
		button = color.RGBA{0xb0, 0x52, 0x24, 0xff}
	}
	by := y
	if verdict == testtools.Issue {
		// Over the text above it, and off the edge.
		by = y - h/10
		fill(textX+(w-textX)/3, by, w+m, by+h/16, button)
	} else {
		fill(textX, by, w-m, by+h/16, button)
	}
	return fakePNG(img)
}

func fakePNG(img image.Image) []byte {
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}
