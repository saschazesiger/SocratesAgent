//go:build linux

package opencode

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agent/askuser"
	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
)

// askModel is the free model of OpenCode's own provider the ask_user tests
// use unless SOCRATES_OPENCODE_REAL_ASK_MODEL names another. The free models'
// quotas run out often; on 2026-10-23 this one and
// opencode/nemotron-3.5-super-vl-free still answered and both asked.
const askModel = "opencode/beam-free"

// realAskEnv is the environment of a real opencode for the ask_user tests:
// its binary first on PATH, a home in the test's folder, and the proxy this
// machine reaches the network through, if any. It skips unless
// SOCRATES_OPENCODE_REAL=1 and opencode is installed.
func realAskEnv(t *testing.T, data string) []string {
	t.Helper()
	if os.Getenv("SOCRATES_OPENCODE_REAL") != "1" {
		t.Skip("set SOCRATES_OPENCODE_REAL=1 to run against the installed opencode")
	}
	bin, err := exec.LookPath("opencode")
	if err != nil {
		t.Skip("no opencode on PATH")
	}
	env := []string{"PATH=" + filepath.Dir(bin) + string(os.PathListSeparator) + os.Getenv("PATH"), "HOME=" + data}
	for _, key := range []string{"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "no_proxy",
		"NODE_EXTRA_CA_CERTS", "SSL_CERT_FILE"} {
		if v, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+v)
		}
	}
	return env
}

// A real opencode serve starts `socrates mcp-ask` from the config, connects
// it and lists ask_user among the model's tools; the question tool is not.
func TestRealOpenCodeStartsTheAskUserServer(t *testing.T) {
	r := newRig(t)
	r.env = realAskEnv(t, r.data)
	spec := r.spec("")
	spec.Model = firstNonEmpty(os.Getenv("SOCRATES_OPENCODE_REAL_ASK_MODEL"), askModel)
	// The server's traffic is recorded both ways on its way through.
	traffic := filepath.Join(r.data, "mcp")
	spec.AskCommand = []string{"/bin/sh", "-c", `tee -a "$0.in" | "$1" mcp-ask --socket "$2" | tee -a "$0.out"`,
		traffic, os.Args[0], filepath.Join(r.data, "no-host.sock")}
	if err := r.start(spec); err != nil {
		t.Fatalf("start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var status map[string]struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	eventually(t, 30*time.Second, "the MCP server to connect", func() bool {
		if err := r.a.cl.call(ctx, http.MethodGet, "/mcp", nil, &status); err != nil {
			t.Logf("GET /mcp: %v", err)
			return false
		}
		return status[askuser.ServerName].Status == "connected"
	})
	in, _ := os.ReadFile(traffic + ".in")
	out, _ := os.ReadFile(traffic + ".out")
	t.Logf("opencode sent:\n%s", in)
	for _, want := range []string{`"method":"initialize"`, `"protocolVersion":"2025-11-25"`, `"method":"tools/list"`} {
		if !strings.Contains(string(in), want) {
			t.Errorf("opencode did not send %s", want)
		}
	}
	for _, want := range []string{`"protocolVersion":"2025-11-25"`, `"instructions":"When you need a decision`,
		`"name":"ask_user"`, `"inputSchema"`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("the server did not answer %s:\n%s", want, out)
		}
	}
}

// A real opencode serve connects the folder's MCP servers it is given
// (StartSpec.MCPServers) and leaves off the one the folder's own opencode.json
// defines: that one is turned off by name and never started.
func TestRealOpenCodeFolderMCPServers(t *testing.T) {
	r := newRig(t)
	r.env = realAskEnv(t, r.data)
	marker := filepath.Join(r.data, "project-server-started")
	project := map[string]any{"mcp": map[string]any{"proj": map[string]any{"type": "local",
		"command": []string{"/bin/sh", "-c", "touch " + marker + "; exec " + os.Args[0] + " mcp-ask --socket /nowhere"}}}}
	raw, _ := json.Marshal(project)
	if err := os.WriteFile(filepath.Join(r.work, "opencode.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	spec := r.spec("")
	spec.Model = firstNonEmpty(os.Getenv("SOCRATES_OPENCODE_REAL_ASK_MODEL"), askModel)
	spec.MCPServers = []agent.MCPServer{{Name: "lab", Transport: agent.MCPStdio, Command: os.Args[0],
		Args: []string{"mcp-ask", "--socket", filepath.Join(r.data, "no-host.sock")}}}
	if err := r.start(spec); err != nil {
		t.Fatalf("start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var status map[string]struct {
		Status string `json:"status"`
	}
	eventually(t, 30*time.Second, "the folder's MCP server to connect", func() bool {
		if err := r.a.cl.call(ctx, http.MethodGet, "/mcp", nil, &status); err != nil {
			t.Logf("GET /mcp: %v", err)
			return false
		}
		return status["lab"].Status == "connected"
	})
	if s, ok := status["proj"]; ok && s.Status != "disabled" {
		t.Errorf("the project's own server is %q, want it off: %v", s.Status, status)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("opencode started the MCP server of the folder's opencode.json")
	}
}

// A free model on a real opencode, inside a real host, asks with ask_user and
// goes on from the answer. Free models do not always follow the prompt, so
// this one says what happened when it fails.
func TestRealOpenCodeModelAsksWithAskUser(t *testing.T) {
	h := newHostRig(t)
	env := realAskEnv(t, h.r.data)
	model := firstNonEmpty(os.Getenv("SOCRATES_OPENCODE_REAL_ASK_MODEL"), askModel)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := h.l.Launch(ctx, agenthost.Spec{ChatID: h.chat, Harness: Harness, Workdir: h.r.work, Model: model, Env: env,
		ParkAfterMS: time.Hour.Milliseconds()}); err != nil {
		t.Fatalf("launch: %v", err)
	}
	cn := h.dial()
	cn.until("ready", func(ev agent.Event) bool { return ev.Type == agent.TypeProcess && ev.State == agent.ProcessReady })
	cn.send("m1", "Ask me with the "+agenthost.MCPAskTool+" tool which colour I like, as one single-choice question "+
		"with the options red and blue. After I answer, reply with only the colour I chose, in capitals.")
	ask := cn.until("the ask or the turn's end", func(ev agent.Event) bool {
		return ev.Type == agent.TypeAsk || ev.Type == agent.TypeTurnEnded
	})
	if ask.Type == agent.TypeTurnEnded && ask.Outcome == agent.OutcomeLimited {
		t.Skipf("the free model %s is out of quota (%s); SOCRATES_OPENCODE_REAL_ASK_MODEL names another", model, ask.Text)
	}
	if ask.Type != agent.TypeAsk {
		t.Fatalf("the model ended its turn without asking: %+v\n%s", ask, journalText(t, h))
	}
	p := askuser.Decode(ask.Payload)
	if p == nil || len(p.Questions) == 0 {
		t.Fatalf("payload %s", ask.Payload)
	}
	q := p.Questions[0]
	choice := ""
	for _, o := range q.Options {
		if strings.Contains(strings.ToLower(o.Label+o.ID), "blue") {
			choice = o.ID
		}
	}
	answer := map[string]any{"answers": map[string]any{q.ID: map[string]any{"selected": []string{choice}}}}
	if choice == "" {
		answer = map[string]any{"answers": map[string]any{q.ID: map[string]any{"other": "blue"}}}
	}
	raw, _ := json.Marshal(answer)
	t.Logf("asked %s; answering %s", ask.Payload, raw)
	if err := cn.c.Send(agenthost.Command{Op: agenthost.OpAnswer, ClientID: "a1", AskID: ask.AskID, Answer: raw}); err != nil {
		t.Fatal(err)
	}
	if ev := cn.until("the delivery", func(ev agent.Event) bool { return ev.Type == agent.TypeAskDelivered }); ev.Via != agent.ViaTool {
		t.Fatalf("delivered %+v\n%s", ev, journalDump(t, h))
	}
	end := cn.until("the turn's end", turnEnded)
	text := journalText(t, h)
	if !strings.Contains(text, "tool done: "+`{"answers":{"`+q.ID+`":{"selected":["`) {
		t.Fatalf("the tool did not return the answer:\n%s", text)
	}
	if end.Outcome == agent.OutcomeLimited {
		// The answer reached the model's tool call; the free quota ran out on
		// the request that would have read it.
		t.Skipf("the free model %s ran out of quota after the answer (%s)", model, end.Text)
	}
	if end.Outcome != agent.OutcomeCompleted || !strings.Contains(text, "BLUE") {
		t.Fatalf("turn %+v\n%s", end, text)
	}
}

// journalText is the chat's text parts, for a failure message.
func journalText(t *testing.T, h *hostRig) string {
	t.Helper()
	events, err := agent.ReadJournal(agenthost.JournalPath(h.r.data, h.chat))
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, m := range agent.Fold(events).Messages {
		for _, part := range m.Parts {
			b.WriteString(part.Kind + " " + part.State + ": " + part.Text + "\n")
		}
	}
	return b.String()
}

// journalDump is the chat's whole journal and the host's log, for a failure
// message.
func journalDump(t *testing.T, h *hostRig) string {
	t.Helper()
	events, _ := agent.ReadJournal(agenthost.JournalPath(h.r.data, h.chat))
	var b strings.Builder
	for _, ev := range events {
		raw, _ := json.Marshal(ev)
		if len(raw) > 300 {
			raw = raw[:300]
		}
		b.Write(raw)
		b.WriteString("\n")
	}
	log, _ := os.ReadFile(filepath.Join(agenthost.ChatDir(h.r.data, h.chat), "host.log"))
	return b.String() + string(log)
}
