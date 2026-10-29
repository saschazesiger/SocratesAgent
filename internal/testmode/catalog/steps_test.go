package catalog

import (
	"reflect"
	"strings"
	"testing"
)

// The steps are the one numbered list under ## Steps: each item's first
// lines are the action, its Expect: line and what follows it what must be
// seen; the rest of the body, before and after the section, is the context.
func TestStepsAreReadFromTheBody(t *testing.T) {
	body := `Signed out, no running session.

## Steps

1. Open / (the home page in the cell's locale).
   Expect: hero heading and the header with the logo; on a phone
   the menu button instead of the links.
2. Type ` + "`example.com`" + ` into the start URL field
   (on phones through the device keyboard).
   Expect: the field shows example.com.

10. Go back.
    Expect: the home page.

## Notes

The consent dialog may appear late.
`
	steps, context, problems := parseBody(body, 5)
	want := []Step{
		{1, "Open / (the home page in the cell's locale).", "hero heading and the header with the logo; on a phone the menu button instead of the links."},
		{2, "Type `example.com` into the start URL field (on phones through the device keyboard).", "the field shows example.com."},
		{3, "Go back.", "the home page."},
	}
	if !reflect.DeepEqual(steps, want) {
		t.Fatalf("steps:\n%+v\nwant\n%+v", steps, want)
	}
	if context != "Signed out, no running session.\n\n## Notes\n\nThe consent dialog may appear late." {
		t.Fatalf("context: %q", context)
	}
	// The third step is numbered 10: a warning, on the file's line.
	if len(problems) != 1 || !problems[0].warning || problems[0].line != 5+11 || !strings.Contains(problems[0].msg, "numbered 10") {
		t.Fatalf("problems: %+v", problems)
	}

	steps, context, problems = parseBody("## Steps\n1) Do it.\n   Expect: done.\n2) Again\nExpect: done again\n", 1)
	if len(problems) != 0 || len(steps) != 2 || steps[1].Do != "Again" || steps[1].Expect != "done again" || context != "" {
		t.Fatalf("1) markers and lazy lines: %+v %q %+v", steps, context, problems)
	}

	// A ## Steps inside a code block is not the heading.
	_, _, problems = parseBody("```\n## Steps\n```\n", 1)
	if len(problems) != 1 || !strings.Contains(problems[0].msg, "no ## Steps") {
		t.Fatalf("fenced: %+v", problems)
	}
}

// What the steps section must not be is an error on its line.
func TestStepsProblems(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		line       int
		want       string
	}{
		{"no section", "Just context.\n", 0, "no ## Steps"},
		{"empty section", "## Steps\n\n## Notes\n", 1, "holds no steps"},
		{"bullets", "## Steps\n- Do it.\n  Expect: x\n", 2, "not bullets"},
		{"text before the list", "## Steps\nSome words.\n1. Do.\n   Expect: x\n", 2, "context goes above it"},
		{"text after the list", "## Steps\n1. Do.\n   Expect: x\n\nMore words.\n", 5, "context goes above it"},
		{"no expect", "## Steps\n1. Do.\n   Then more.\n", 2, "no line starting with Expect:"},
		{"lower-case expect", "## Steps\n1. Do.\n   expect: x\n", 2, "no line starting with Expect:"},
		{"empty expect", "## Steps\n1. Do.\n   Expect:\n", 3, "Expect: is empty"},
		{"expect only", "## Steps\n1. Expect: x\n", 2, "nothing to do"},
		{"second expect", "## Steps\n1. Do.\n   Expect: x\n   Expect: y\n", 4, "second Expect:"},
		{"mixed markers", "## Steps\n1. Do.\n   Expect: x\n2) Do.\n   Expect: y\n", 4, "one list uses one"},
		{"heading in the list", "## Steps\n1. Do.\n   Expect: x\n### More\n", 4, "context goes above it"},
		{"second section", "## Steps\n1. Do.\n   Expect: x\n## Steps\n", 4, "second ## Steps"},
	} {
		_, _, problems := parseBody(tc.body, 1)
		var found bool
		for _, p := range problems {
			if !p.warning && p.line == tc.line && strings.Contains(p.msg, tc.want) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: %+v, want line %d with %q", tc.name, problems, tc.line, tc.want)
		}
	}
}
