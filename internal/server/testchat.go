package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agent/chattools"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/testmode"
	"github.com/saschazesiger/SocratesAgent/internal/testmode/catalog"
)

// The test tools of a chat (chattools/catalogtools.go; TEST-MODE.md, "Runs
// started by chat agents"): a chat whose repository has a test catalog
// reads its format, checks it, starts runs of it and reads their results;
// the runner sends the chat the results of a run it started when the run
// ends (TellChat).

// testToolsOption is the options key of a chat that has the test tools
// whatever its repository holds: one the Tests page started to write the
// first test (createChatBody.Tests).
const testToolsOption = "test_tools"

// offersTestTools is whether a chat's agent gets the test tools: never a
// test session's; a chat's when it was started for them, or when its
// repository has a catalog (.socrates/tests/TESTS.md) as its host starts.
func offersTestTools(row *store.Session) bool {
	if row == nil || row.IsTest() || !row.IsChat() {
		return false
	}
	var o map[string]json.RawMessage
	if len(row.Options) > 0 && json.Unmarshal(row.Options, &o) == nil && string(o[testToolsOption]) == "true" {
		return true
	}
	return strings.TrimSpace(row.Workdir) != "" && testmode.HasCatalog(testmode.FindRoot(row.Workdir))
}

// testChatCall answers a chat's call of a test tool.
func (s *Server) testChatCall(row *store.Session, tool string, args json.RawMessage) chattools.Response {
	fail := func(format string, a ...any) chattools.Response {
		return chattools.Response{Text: fmt.Sprintf(format, a...), Error: true}
	}
	if tool == chattools.TestsFormat {
		return chattools.Response{Text: catalog.Format}
	}
	if tool == chattools.TestsCheck {
		return chattools.Response{Text: checkCatalogText(row.Workdir)}
	}
	if s.tests == nil {
		return fail("The test runner is not running.")
	}
	switch tool {
	case chattools.RunTests:
		var a chattools.RunTestsArgs
		if len(args) > 0 && string(args) != "null" {
			if err := json.Unmarshal(args, &a); err != nil {
				return fail("The arguments could not be read: %v", err)
			}
		}
		started, err := s.tests.StartChatRun(row.Workdir, a.Name, selectionOf(a), testmode.StartedBy{Chat: row.ID, Title: row.Title})
		if errors.Is(err, testmode.ErrNoCatalog) {
			return fail("This repository has no test catalog (%s): there is nothing to run. Read %s to write one.", catalog.DirName, chattools.TestsFormat)
		}
		if err != nil {
			return fail("The run was not started: %v", err)
		}
		return chattools.Response{Text: runStartedText(started)}
	case chattools.TestResults:
		var a chattools.TestResultsArgs
		if len(args) > 0 && string(args) != "null" {
			if err := json.Unmarshal(args, &a); err != nil {
				return fail("The arguments could not be read: %v", err)
			}
		}
		id, err := s.tests.ChatRunID(row.Workdir, a.Run)
		if err != nil {
			return fail("%s", err.Error())
		}
		problemsOnly := a.ProblemsOnly == nil || *a.ProblemsOnly
		report, err := s.tests.Report(id, testmode.ReportOptions{Paths: true, ProblemsOnly: problemsOnly})
		if err != nil {
			return fail("The results of %s could not be read: %v", id, err)
		}
		return chattools.Response{Text: capReport(report, chattools.MaxResults, problemsOnly)}
	}
	return fail("There is no tool %s.", tool)
}

// selectionOf is run_tests' arguments as a run's selection.
func selectionOf(a chattools.RunTestsArgs) testmode.Selection {
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

// runStartedText is what run_tests answers: the run, its cells, and that
// the results come to the chat by themselves.
func runStartedText(c *testmode.ChatRun) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Started test run %d (%s): %d cell(s) (%s)", c.Run.Number, c.Run.ID, c.Cells, c.Run.Selection.Describe())
	if c.Behind != "" {
		fmt.Fprintf(&b, "; it starts once the repository's run %s is over", c.Behind)
	}
	b.WriteString(".\nSocrates sends the results to this chat as a message when the run ends, which starts your next turn. " +
		"End your turn now: do not wait or poll. A run takes minutes to hours; the person can watch it on the Tests page.")
	return b.String()
}

// capReport cuts a report to max bytes at a line's end, and says so.
func capReport(text string, max int, problemsOnly bool) string {
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

// checkCatalogText is tests_check's answer for the repository of a folder:
// every error and warning with its file and line, then the areas with their
// tests and cells.
func checkCatalogText(workdir string) string {
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

// TellChat sends a chat the results of a test run its agent started, as
// send_to_chat sends a message: it starts the agent, or waits behind its
// turn. A chat that is gone, or is no chat a person reads, is not there.
func (h testHost) TellChat(ctx context.Context, chat, key, text string) error {
	s := h.s
	row, err := s.store.GetSession(chat)
	if errors.Is(err, store.ErrNotFound) || err == nil && (!row.IsChat() || row.IsTest()) {
		return testmode.ErrNotFound
	}
	if err != nil {
		return err
	}
	p, err := s.chats.peer(chat)
	if err != nil {
		return err
	}
	code, res := s.sendMessage(ctx, p, &chatMessageBody{ClientID: key, Text: text,
		Notice: agent.NoticeTestResults, Reason: "The results of the test run this chat started."})
	if code != http.StatusOK && code != http.StatusAccepted {
		msg, _ := res["error"].(string)
		return fmt.Errorf("%d %s", code, msg)
	}
	s.events.poke()
	return nil
}
