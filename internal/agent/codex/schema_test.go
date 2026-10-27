package codex

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The app-server protocol is experimental and changes between releases. The
// adapter is pinned to the schema of the codex it was built against, kept in
// testdata/schema (generated with `codex app-server generate-json-schema
// --experimental`, gzipped). TestPinnedSchema checks every method, field,
// item type and error code the adapter relies on against it, and
// TestInstalledCodexSchema runs the same checks against the codex on this
// machine, so an update that breaks the adapter fails loudly before it ships.
// SOCRATES_UPDATE_CODEX_SCHEMA=1 rewrites the pin from the installed codex.

// schemaFiles are the bundles the checks read: the three method unions with
// the definitions they use, and the one response the adapter builds itself.
var schemaFiles = []string{"ClientRequest.json", "ServerRequest.json", "ServerNotification.json", "DynamicToolCallResponse.json"}

// adapterNeeds is what the adapter sends and reads.
var adapterNeeds = struct {
	clientMethods, notifications, serverRequests []string
	fields                                       map[string][]string
	itemTypes                                    []string
	errorCodes                                   []string
}{
	clientMethods: []string{"initialize", "thread/start", "thread/resume", "thread/turns/list", "thread/items/list",
		"turn/start", "turn/steer", "turn/interrupt", "thread/compact/start", "review/start", "thread/shellCommand",
		"thread/backgroundTerminals/list", "config/read"},
	notifications: []string{"turn/started", "turn/completed", "item/started", "item/completed", "item/agentMessage/delta",
		"item/plan/delta", "item/reasoning/summaryTextDelta", "item/reasoning/textDelta", "item/reasoning/summaryPartAdded",
		"item/commandExecution/outputDelta", "item/fileChange/outputDelta", "turn/diff/updated", "turn/plan/updated",
		"thread/tokenUsage/updated", "account/rateLimits/updated", "error", "warning", "configWarning", "model/rerouted",
		"serverRequest/resolved"},
	serverRequests: []string{"item/commandExecution/requestApproval", "item/fileChange/requestApproval",
		"item/tool/requestUserInput", "mcpServer/elicitation/request", "item/permissions/requestApproval", "item/tool/call",
		"execCommandApproval", "applyPatchApproval", "currentTime/read"},
	fields: map[string][]string{
		"InitializeParams":                  {"clientInfo", "capabilities"},
		"AskForApproval":                    {"granular"},
		"ThreadStartParams":                 {"cwd", "approvalPolicy", "sandbox", "model", "config", "dynamicTools", "developerInstructions"},
		"ThreadResumeParams":                {"threadId", "excludeTurns", "cwd", "approvalPolicy", "sandbox", "config"},
		"ConfigReadParams":                  {"cwd", "includeLayers"},
		"TurnStartParams":                   {"threadId", "input", "clientUserMessageId", "model", "effort", "collaborationMode"},
		"TurnSteerParams":                   {"threadId", "input", "clientUserMessageId", "expectedTurnId"},
		"TurnInterruptParams":               {"threadId", "turnId"},
		"ThreadShellCommandParams":          {"threadId", "command"},
		"ReviewStartParams":                 {"threadId", "target"},
		"ThreadTurnsListParams":             {"threadId", "cursor", "limit", "sortDirection", "itemsView"},
		"ThreadItemsListParams":             {"threadId", "turnId", "cursor", "limit", "sortDirection"},
		"ErrorNotification":                 {"error", "willRetry", "threadId", "turnId"},
		"TurnError":                         {"message", "codexErrorInfo"},
		"ThreadTokenUsage":                  {"last", "modelContextWindow"},
		"TokenUsageBreakdown":               {"inputTokens", "cachedInputTokens", "cacheWriteInputTokens", "outputTokens", "reasoningOutputTokens"},
		"RateLimitSnapshot":                 {"primary", "secondary"},
		"RateLimitWindow":                   {"usedPercent", "windowDurationMins", "resetsAt"},
		"TurnDiffUpdatedNotification":       {"threadId", "turnId", "diff"},
		"TurnPlanUpdatedNotification":       {"explanation", "plan"},
		"Turn":                              {"id", "status", "error", "rootTurnId"},
		"DynamicToolCallParams":             {"threadId", "turnId", "callId", "tool", "arguments"},
		"DynamicToolCallResponse":           {"contentItems", "success"},
		"DynamicToolSpec":                   {"type", "name", "description", "inputSchema"},
		"ToolRequestUserInputParams":        {"threadId", "turnId", "itemId", "questions"},
		"ToolRequestUserInputQuestion":      {"id", "header", "question", "isOther", "isSecret", "options"},
		"ToolRequestUserInputOption":        {"label", "description"},
		"McpServerElicitationRequestParams": {"serverName", "threadId", "turnId", "mode", "message", "requestedSchema", "_meta"},
		"AsyncUserInputQuestion":            {"title", "options"},
		"FileUpdateChange":                  {"path", "kind", "diff"},
	},
	itemTypes: []string{"userMessage", "agentMessage", "reasoning", "plan", "commandExecution", "fileChange", "mcpToolCall",
		"dynamicToolCall", "collabAgentToolCall", "subAgentActivity", "webSearch", "imageView", "imageGeneration",
		"enteredReviewMode", "exitedReviewMode", "contextCompaction"},
	errorCodes: []string{"usageLimitExceeded", "activeTurnNotSteerable"},
}

// itemFields are the ThreadItem fields the adapter reads, by type.
var itemFields = map[string][]string{
	"userMessage":      {"clientId", "content"},
	"agentMessage":     {"text", "phase", "delivery", "questions"},
	"reasoning":        {"summary", "content"},
	"commandExecution": {"command", "cwd", "status", "aggregatedOutput", "exitCode", "durationMs"},
	"fileChange":       {"changes", "status"},
	"subAgentActivity": {"kind", "agentThreadId", "agentPath", "model", "reasoningEffort"},
	"dynamicToolCall":  {"tool", "arguments", "status", "contentItems", "success"},
}

type schemaDoc struct {
	OneOf       []json.RawMessage          `json:"oneOf"`
	Definitions map[string]json.RawMessage `json:"definitions"`
	raw         json.RawMessage
}

type schema map[string]schemaDoc

// RealEnv runs the tests against the installed codex binary, with the
// scripted model of package fakemodel standing in for OpenAI. They are
// skipped otherwise: they need codex on the PATH and take a while.
const RealEnv = "SOCRATES_REAL_CODEX"

func loadPinned(t *testing.T) schema {
	t.Helper()
	s := schema{}
	for _, name := range schemaFiles {
		f, err := os.Open(filepath.Join("testdata", "schema", name+".gz"))
		if err != nil {
			t.Fatal(err)
		}
		zr, err := gzip.NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(zr)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		var doc schemaDoc
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		doc.raw = raw
		s[name] = doc
	}
	return s
}

func (s schema) methods(file string) map[string]bool {
	out := map[string]bool{}
	for _, raw := range s[file].OneOf {
		var v struct {
			Properties struct {
				Method struct {
					Enum []string `json:"enum"`
				} `json:"method"`
			} `json:"properties"`
		}
		_ = json.Unmarshal(raw, &v)
		for _, m := range v.Properties.Method.Enum {
			out[m] = true
		}
	}
	return out
}

func (s schema) def(name string) json.RawMessage {
	if doc, ok := s[name+".json"]; ok {
		return doc.raw
	}
	for _, file := range schemaFiles {
		if d, ok := s[file].Definitions[name]; ok {
			return d
		}
	}
	return nil
}

// properties is every property name of an object schema, through allOf,
// anyOf and oneOf.
func properties(raw json.RawMessage) map[string]bool {
	var v struct {
		Properties map[string]json.RawMessage `json:"properties"`
		AllOf      []json.RawMessage          `json:"allOf"`
		AnyOf      []json.RawMessage          `json:"anyOf"`
		OneOf      []json.RawMessage          `json:"oneOf"`
	}
	out := map[string]bool{}
	if json.Unmarshal(raw, &v) != nil {
		return out
	}
	for k := range v.Properties {
		out[k] = true
	}
	for _, list := range [][]json.RawMessage{v.AllOf, v.AnyOf, v.OneOf} {
		for _, sub := range list {
			for k := range properties(sub) {
				out[k] = true
			}
		}
	}
	return out
}

// threadItems is each ThreadItem variant's properties, by type.
func (s schema) threadItems() map[string]map[string]bool {
	var v struct {
		OneOf []json.RawMessage `json:"oneOf"`
	}
	_ = json.Unmarshal(s.def("ThreadItem"), &v)
	out := map[string]map[string]bool{}
	for _, raw := range v.OneOf {
		var variant struct {
			Properties struct {
				Type struct {
					Enum []string `json:"enum"`
				} `json:"type"`
			} `json:"properties"`
		}
		_ = json.Unmarshal(raw, &variant)
		for _, typ := range variant.Properties.Type.Enum {
			out[typ] = properties(raw)
		}
	}
	return out
}

func checkSchema(t *testing.T, s schema) {
	t.Helper()
	if missing := schemaGaps(s); len(missing) > 0 {
		t.Fatalf("the codex app-server schema lacks what the adapter depends on:\n  %s", strings.Join(missing, "\n  "))
	}
}

// schemaGaps is everything the adapter needs that s does not have.
func schemaGaps(s schema) []string {
	var missing []string
	need := func(have map[string]bool, what string, names []string) {
		for _, n := range names {
			if !have[n] {
				missing = append(missing, what+" "+n)
			}
		}
	}
	need(s.methods("ClientRequest.json"), "client method", adapterNeeds.clientMethods)
	need(s.methods("ServerNotification.json"), "notification", adapterNeeds.notifications)
	need(s.methods("ServerRequest.json"), "server request", adapterNeeds.serverRequests)
	for def, fields := range adapterNeeds.fields {
		raw := s.def(def)
		if raw == nil {
			missing = append(missing, "definition "+def)
			continue
		}
		need(properties(raw), def+" field", fields)
	}
	items := s.threadItems()
	for _, typ := range adapterNeeds.itemTypes {
		if items[typ] == nil {
			missing = append(missing, "item type "+typ)
		}
	}
	for typ, fields := range itemFields {
		if items[typ] != nil {
			need(items[typ], typ+" field", fields)
		}
	}
	codes := string(s.def("CodexErrorInfo"))
	for _, c := range adapterNeeds.errorCodes {
		if !strings.Contains(codes, `"`+c+`"`) {
			missing = append(missing, "error code "+c)
		}
	}
	sort.Strings(missing)
	return missing
}

func TestPinnedSchema(t *testing.T) {
	s := loadPinned(t)
	checkSchema(t, s)
	// The check is not vacuous: a schema without turn/steer fails it.
	doc := s["ClientRequest.json"]
	var kept []json.RawMessage
	for _, raw := range doc.OneOf {
		if !strings.Contains(string(raw), `"turn/steer"`) {
			kept = append(kept, raw)
		}
	}
	doc.OneOf = kept
	s["ClientRequest.json"] = doc
	if gaps := schemaGaps(s); len(gaps) != 1 || gaps[0] != "client method turn/steer" {
		t.Fatalf("gaps %v", gaps)
	}
}

func TestInstalledCodexSchema(t *testing.T) {
	if os.Getenv(RealEnv) != "1" && os.Getenv("SOCRATES_UPDATE_CODEX_SCHEMA") != "1" {
		t.Skip("set " + RealEnv + "=1 to check the installed codex's schema")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex is not installed")
	}
	dir := t.TempDir()
	out, err := exec.Command("codex", "app-server", "generate-json-schema", "--experimental", "--out", dir).CombinedOutput()
	if err != nil {
		t.Fatalf("generate the schema: %v\n%s", err, out)
	}
	installed := schema{}
	raws := map[string][]byte{}
	for _, name := range schemaFiles {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		var doc schemaDoc
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		doc.raw = raw
		installed[name], raws[name] = doc, raw
	}
	checkSchema(t, installed)
	version, _ := exec.Command("codex", "--version").Output()
	if os.Getenv("SOCRATES_UPDATE_CODEX_SCHEMA") == "1" {
		for name, raw := range raws {
			var buf bytes.Buffer
			zw := gzip.NewWriter(&buf)
			zw.Write(raw)
			zw.Close()
			if err := os.WriteFile(filepath.Join("testdata", "schema", name+".gz"), buf.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join("testdata", "schema", "VERSION"), version, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	pinned, _ := os.ReadFile(filepath.Join("testdata", "schema", "VERSION"))
	if !bytes.Equal(bytes.TrimSpace(pinned), bytes.TrimSpace(version)) {
		t.Logf("the installed %s is not the pinned %s; the adapter's needs are met", bytes.TrimSpace(version), bytes.TrimSpace(pinned))
	}
}
