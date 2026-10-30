package tests

import (
	"fmt"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/agent/chattools"
	"github.com/saschazesiger/SocratesAgent/internal/testmode"
	"github.com/saschazesiger/SocratesAgent/internal/testmode/catalog"
)

// What the test tools of a chat (chattools/catalogtools.go, answered in
// internal/server/testchat.go) and this server's share: the catalog check,
// a selection from run_tests' arguments, the answer of a started run, a
// report cut to size.

// SelectionOf is run_tests' arguments as a run's selection.
func SelectionOf(a chattools.RunTestsArgs) testmode.Selection {
	clean := func(list []string) []string {
		var out []string
		for _, s := range list {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	sel := testmode.Selection{Areas: clean(a.Areas), Tests: clean(a.Tests), Cells: clean(a.Cells), Tags: clean(a.Tags),
		Priorities: clean(a.Priorities), ProblemsOf: strings.TrimSpace(a.ProblemsOf)}
	for name, values := range a.Dims {
		if values = clean(values); len(values) > 0 {
			if sel.Dims == nil {
				sel.Dims = map[string][]string{}
			}
			sel.Dims[strings.TrimSpace(name)] = values
		}
	}
	for i, p := range sel.Priorities {
		sel.Priorities[i] = strings.ToUpper(p)
	}
	for i, id := range sel.Tests {
		sel.Tests[i] = strings.TrimSuffix(id, ".md")
	}
	return sel
}

// RunStartedText is what run_tests answers: the run, its cells, and that
// the results come to the chat by themselves.
func RunStartedText(c *testmode.ChatRun) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Started test run %d (%s): %d cell(s) (%s)", c.Run.Number, c.Run.ID, c.Cells, c.Run.Selection.Describe())
	if c.Behind != "" {
		fmt.Fprintf(&b, "; it starts once the repository's run %s is over", c.Behind)
	}
	b.WriteString(".\nSocrates sends the results to this chat as a message when the run ends, which starts your next turn. " +
		"End your turn now: do not wait or poll. A run takes minutes to hours; the person can watch it on the Tests page.")
	return b.String()
}

// CapReport cuts a report to max bytes at a line's end, and says so.
func CapReport(text string, max int, problemsOnly bool) string {
	if len(text) <= max {
		return text
	}
	cut := strings.LastIndexByte(text[:max], '\n')
	if cut <= 0 {
		cut = max
		for cut > 0 && text[cut]&0xC0 == 0x80 {
			cut--
		}
	}
	note := fmt.Sprintf("\n\n[The report is cut here at %d KB of %d KB.", max>>10, (len(text)+1023)>>10)
	if !problemsOnly {
		note += " Call test_results with problems_only true for the cells that did not pass alone."
	} else {
		note += " The run's page on the Tests page has every cell."
	}
	return text[:cut] + note + "]"
}

// CheckText is tests_check's answer for the repository of a folder: every
// error and warning with its file and line, then the areas with their tests
// and cells.
func CheckText(workdir string) string {
	root := testmode.FindRoot(workdir)
	if strings.TrimSpace(workdir) == "" || !testmode.HasCatalog(root) {
		return fmt.Sprintf("This repository (%s) has no test catalog: there is no %s/%s. Read %s for the format, then write "+
			"%s/%s, an area folder with its %s and the tests.", root, catalog.DirName, catalog.TestsFile, chattools.TestsFormat,
			catalog.DirName, catalog.TestsFile, catalog.AreaFile)
	}
	c, err := catalog.Load(root)
	if err != nil {
		return "The catalog could not be read: " + err.Error()
	}
	var b strings.Builder
	cells := c.Cells()
	fmt.Fprintf(&b, "The test catalog of %s (%s): %d area(s), %d test(s), %d cell(s); %d error(s), %d warning(s).\n",
		c.Title, root, len(c.Areas), c.TestCount(), len(cells), len(c.Errors), len(c.Warnings))
	if len(c.Errors) > 0 {
		b.WriteString("\nErrors (a run does not start):\n")
		for _, p := range c.Errors {
			p.File = catalog.DirName + "/" + p.File
			fmt.Fprintf(&b, "- %s\n", p.String())
		}
	}
	if len(c.Warnings) > 0 {
		b.WriteString("\nWarnings:\n")
		for _, p := range c.Warnings {
			p.File = catalog.DirName + "/" + p.File
			fmt.Fprintf(&b, "- %s\n", p.String())
		}
	}
	perTest := map[string][]string{}
	for _, cell := range cells {
		perTest[cell.Test.ID] = append(perTest[cell.Test.ID], cell.ID())
	}
	for _, a := range c.Areas {
		fmt.Fprintf(&b, "\nArea %s (%s)", a.ID, a.Title)
		for _, d := range a.Dimensions {
			var ids []string
			for _, v := range d.Values {
				ids = append(ids, v.ID)
			}
			for _, v := range d.Extra {
				ids = append(ids, v.ID+" (extra)")
			}
			fmt.Fprintf(&b, "; %s: %s", d.Name, strings.Join(ids, ", "))
		}
		b.WriteString("\n")
		for _, t := range a.Tests {
			fmt.Fprintf(&b, "- %s [%s", t.ID, t.Priority)
			if len(t.Tags) > 0 {
				fmt.Fprintf(&b, "; tags %s", strings.Join(t.Tags, ", "))
			}
			fmt.Fprintf(&b, "] %s: %d step(s), cells %s\n", t.Title, len(t.Steps), strings.Join(perTest[t.ID], " "))
		}
	}
	return b.String()
}
