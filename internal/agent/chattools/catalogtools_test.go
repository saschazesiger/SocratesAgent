package chattools

import (
	"strings"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/agent/testtools"
)

// A chat with the test tools has them after the chat tools, answers them,
// tells its agent when to use them (with their ToolSearch line), and carries
// them to `socrates mcp-ask`; a test session never has them, even when the
// flag is set; a chat without the flag has the chat tools alone.
func TestAChatOfARepositoryWithTestsHasTheTestTools(t *testing.T) {
	names := func(e Endpoint) string {
		var out []string
		for _, d := range e.Tools() {
			out = append(out, d.Name)
		}
		return strings.Join(out, ",")
	}
	chat := Endpoint{Socket: "/s", Token: "c.x", Tests: true}
	if got := names(chat); got != "list_chats,read_chat,send_to_chat,tests_format,tests_check,run_tests,test_results" {
		t.Fatalf("tools: %s", got)
	}
	for _, name := range []string{TestsFormat, TestsCheck, RunTests, TestResults, List} {
		if !chat.Has(name) {
			t.Errorf("Has(%s) is false", name)
		}
	}
	g := chat.Guidance("p_", true)
	if !strings.HasPrefix(g, Guidance("p_")) || !strings.Contains(g, TestGuidance("p_")) ||
		!strings.Contains(g, "select:p_tests_format,p_tests_check,p_run_tests,p_test_results") {
		t.Fatalf("guidance:\n%s", g)
	}
	for _, want := range []string{"p_run_tests", "p_test_results", "p_tests_format", "p_tests_check", "end your turn"} {
		if !strings.Contains(TestGuidance("p_"), want) {
			t.Errorf("the guidance does not say %q", want)
		}
	}
	if env := chat.Env(); env[EnvTests] != "1" {
		t.Fatalf("env %v", env)
	}
	t.Setenv(EnvSocket, "/s")
	t.Setenv(EnvToken, "c.x")
	t.Setenv(EnvKind, "")
	t.Setenv(EnvTests, "1")
	if got := FromEnv(); got != chat {
		t.Fatalf("from the environment: %+v", got)
	}

	plain := Endpoint{Socket: "/s", Token: "c.x"}
	if names(plain) != "list_chats,read_chat,send_to_chat" || plain.Has(RunTests) || strings.Contains(plain.Guidance("p_", true), RunTests) {
		t.Fatalf("a chat without the test tools: %s", names(plain))
	}
	if _, ok := plain.Env()[EnvTests]; ok {
		t.Fatal("a chat without the test tools names them in its environment")
	}

	cell := Endpoint{Socket: "/s", Token: "c.x", Kind: testtools.KindCell, Tests: true}
	if strings.Contains(names(cell), RunTests) || cell.Has(RunTests) || strings.Contains(cell.Guidance("p_", true), RunTests) {
		t.Fatalf("a test session has the chat's test tools: %s", names(cell))
	}
	if _, ok := cell.Env()[EnvTests]; ok {
		t.Fatal("a test session's environment names the chat's test tools")
	}
}

// Every test tool has a title, a description and an object schema; run_tests
// takes the selection's lists, dims, problems_of and a name, test_results a
// run and problems_only.
func TestTheTestToolsDefinitions(t *testing.T) {
	props := map[string][]string{
		TestsFormat: nil,
		TestsCheck:  nil,
		RunTests:    {"areas", "tests", "cells", "tags", "priorities", "dims", "problems_of", "name"},
		TestResults: {"run", "problems_only"},
	}
	defs := TestTools()
	if len(defs) != len(props) {
		t.Fatalf("%d tools", len(defs))
	}
	for _, d := range defs {
		if d.Title == "" || d.Description == "" || d.Schema["type"] != "object" {
			t.Errorf("%s: %+v", d.Name, d)
		}
		got, _ := d.Schema["properties"].(map[string]any)
		want, ok := props[d.Name]
		if !ok || len(got) != len(want) {
			t.Errorf("%s: properties %v, want %v", d.Name, got, want)
		}
		for _, p := range want {
			if got[p] == nil {
				t.Errorf("%s lacks %s", d.Name, p)
			}
		}
		if d.Schema["required"] != nil {
			t.Errorf("%s requires %v: every argument is optional", d.Name, d.Schema["required"])
		}
	}
	if !strings.Contains(defs[2].Description, "end your turn") {
		t.Errorf("run_tests does not say to end the turn: %s", defs[2].Description)
	}
}
