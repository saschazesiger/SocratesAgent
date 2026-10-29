package catalog

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Step is one numbered step of a test: what to do and what must then be
// seen. N is its position, from 1.
type Step struct {
	N      int    `json:"n"`
	Do     string `json:"do"`
	Expect string `json:"expect"`
}

// The heading of a test's steps, and the headings that end a section (a
// level 1 or 2 heading; a deeper one belongs to the section above it).
var (
	stepsHeading   = regexp.MustCompile(`^ {0,3}##[ \t]+Steps[ \t]*#*[ \t]*$`)
	sectionHeading = regexp.MustCompile(`^ {0,3}##?([ \t]|$)`)
	fenceLine      = regexp.MustCompile("^ {0,3}(```|~~~)")
	orderedItem    = regexp.MustCompile(`^ {0,3}(\d{1,9})([.)])([ \t]+(.*))?$`)
	bulletItem     = regexp.MustCompile(`^ {0,3}[-*+][ \t]`)
)

// bodyProblem is a problem in a test's body, on the file's line.
type bodyProblem struct {
	line    int
	msg     string
	warning bool
}

// parseBody splits a test's body (starting on the file's line first) into
// its steps and its context: everything but the `## Steps` section, in its
// order. The steps are the section's one ordered list; each item is the
// action, then one line starting with `Expect:` and what must be seen, both
// of which may go on over the following lines.
func parseBody(body string, first int) (steps []Step, context string, problems []bodyProblem) {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	start, end := -1, len(lines)
	inFence := false
	for i, line := range lines {
		if fenceLine.MatchString(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		switch {
		case stepsHeading.MatchString(line) && start < 0:
			start = i
		case stepsHeading.MatchString(line):
			problems = append(problems, bodyProblem{line: first + i, msg: fmt.Sprintf("a second ## Steps (the first is on line %d)", first+start)})
			if end == len(lines) {
				end = i
			}
		case start >= 0 && end == len(lines) && sectionHeading.MatchString(line):
			end = i
		}
	}
	if start < 0 {
		problems = append(problems, bodyProblem{line: 0, msg: "no ## Steps: a test lists its steps under a heading ## Steps"})
		return nil, strings.TrimSpace(body), problems
	}
	before := strings.TrimSpace(strings.Join(lines[:start], "\n"))
	after := strings.TrimSpace(strings.Join(lines[end:], "\n"))
	context = strings.TrimSpace(before + "\n\n" + after)
	steps, problems = parseSteps(lines[start+1:end], first+start+1, problems)
	if len(steps) == 0 && len(problems) == 0 {
		problems = append(problems, bodyProblem{line: first + start, msg: "## Steps holds no steps: write them as a numbered list (1. ...)"})
	}
	return steps, context, problems
}

// parseSteps reads the ordered list of the steps section, lines starting on
// the file's line first.
func parseSteps(lines []string, first int, problems []bodyProblem) ([]Step, []bodyProblem) {
	type item struct {
		line  int
		num   int
		lines []string // the item's text, a line each, without indentation
		ns    []int    // the file's line of each
	}
	var items []*item
	var marker string
	blank := false
	for i, raw := range lines {
		n := first + i
		text := strings.TrimSpace(raw)
		if text == "" {
			blank = true
			continue
		}
		if m := orderedItem.FindStringSubmatch(raw); m != nil {
			if marker == "" {
				marker = m[2]
			} else if m[2] != marker {
				problems = append(problems, bodyProblem{line: n, msg: fmt.Sprintf("the steps are numbered with %q, this one with %q: one list uses one", "1"+marker, "1"+m[2])})
				return nil, problems
			}
			num, _ := strconv.Atoi(m[1])
			it := &item{line: n, num: num}
			if t := strings.TrimSpace(m[4]); t != "" {
				it.lines, it.ns = []string{t}, []int{n}
			}
			items = append(items, it)
			blank = false
			continue
		}
		indented := raw[0] == ' ' || raw[0] == '\t'
		switch {
		case len(items) == 0 && bulletItem.MatchString(raw):
			problems = append(problems, bodyProblem{line: n, msg: "the steps are a numbered list (1. or 1)), not bullets"})
			return nil, problems
		case len(items) == 0:
			problems = append(problems, bodyProblem{line: n, msg: "## Steps holds only the numbered list of steps; context goes above it"})
			return nil, problems
		case !indented && (blank || strings.HasPrefix(text, "#")):
			// A paragraph after a step that is not indented under it ends
			// the list, and ## Steps holds nothing but the list.
			problems = append(problems, bodyProblem{line: n, msg: "## Steps holds only the numbered list of steps; context goes above it"})
			return nil, problems
		}
		it := items[len(items)-1]
		it.lines = append(it.lines, text)
		it.ns = append(it.ns, n)
		blank = false
	}
	steps := make([]Step, 0, len(items))
	for i, it := range items {
		if it.num != i+1 {
			problems = append(problems, bodyProblem{line: it.line, warning: true, msg: fmt.Sprintf("step %d is numbered %d: steps count by their position", i+1, it.num)})
		}
		var do, expect []string
		expectLine := 0
		for j, text := range it.lines {
			switch {
			case strings.HasPrefix(text, "Expect:"):
				if expectLine != 0 {
					problems = append(problems, bodyProblem{line: it.ns[j], msg: fmt.Sprintf("step %d has a second Expect: (the first is on line %d)", i+1, expectLine)})
					continue
				}
				expectLine = it.ns[j]
				if t := strings.TrimSpace(strings.TrimPrefix(text, "Expect:")); t != "" {
					expect = append(expect, t)
				}
			case expectLine != 0:
				expect = append(expect, text)
			default:
				do = append(do, text)
			}
		}
		step := Step{N: i + 1, Do: strings.Join(do, " "), Expect: strings.Join(expect, " ")}
		switch {
		case step.Do == "":
			problems = append(problems, bodyProblem{line: it.line, msg: fmt.Sprintf("step %d says nothing to do before its Expect:", i+1)})
		case expectLine == 0:
			problems = append(problems, bodyProblem{line: it.line, msg: fmt.Sprintf("step %d has no line starting with Expect: (what must be seen)", i+1)})
		case step.Expect == "":
			problems = append(problems, bodyProblem{line: expectLine, msg: fmt.Sprintf("step %d's Expect: is empty", i+1)})
		}
		steps = append(steps, step)
	}
	return steps, problems
}
