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
	testsmcp "github.com/saschazesiger/SocratesAgent/internal/mcpserver/tests"
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

// testsMCPOn is whether a chat has Socrates Tests on (the built-in MCP
// server, internal/mcpserver/tests): it has the test tools there, so the
// chat tools leave theirs out rather than offer them twice.
func (s *Server) testsMCPOn(row *store.Session) bool {
	if on, chosen := readMCPChoices(row.Options)[testsmcp.Name]; chosen {
		return on
	}
	return builtinDefault(s.Settings(), testsmcp.Name)
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
		return chattools.Response{Text: testsmcp.CheckText(row.Workdir)}
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
		started, err := s.tests.StartChatRun(row.Workdir, a.Name, testsmcp.SelectionOf(a), testmode.StartedBy{Chat: row.ID, Title: row.Title})
		if errors.Is(err, testmode.ErrNoCatalog) {
			return fail("This repository has no test catalog (%s): there is nothing to run. Read %s to write one.", catalog.DirName, chattools.TestsFormat)
		}
		if err != nil {
			return fail("The run was not started: %v", err)
		}
		return chattools.Response{Text: testsmcp.RunStartedText(started)}
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
		return chattools.Response{Text: testsmcp.CapReport(report, chattools.MaxResults, problemsOnly)}
	}
	return fail("There is no tool %s.", tool)
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
