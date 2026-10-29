//go:build !windows

package claude

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agent/chattools"
)

// A chat whose repository has a test catalog (chattools.Endpoint.Tests) has
// the test tools on the socrates server after the chat tools, and their
// guidance with its ToolSearch line after the chat tools' in its system
// prompt; a call of one goes to the endpoint.
func TestAChatWithTestsHasTheTestToolsInClaudeCode(t *testing.T) {
	e := newEnv(t)
	spec := e.spec("")
	spec.ChatTools = chattools.Endpoint{Socket: "/nonexistent/chat-tools.sock", Token: "c.x", Tests: true}
	a := New(Options{})
	r := newRec()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.Start(ctx, spec, r); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Stop() })
	send(t, a, "m1", "/mcp")
	send(t, a, "m2", "/argv")
	send(t, a, "m3", "/runtests web/checkout")
	r.turnsEnded(t, 3)
	var texts []string
	r.view(func(p *agent.Projection) {
		for _, m := range p.Messages {
			for _, part := range partsOf(m, agent.PartText) {
				texts = append(texts, part.Text)
			}
		}
	})
	mcp, argv, called := texts[0], texts[1], texts[len(texts)-1]
	want := chatsPrefix + chattools.Send + "," + chatsPrefix + chattools.TestsFormat + "," + chatsPrefix + chattools.TestsCheck + "," +
		chatsPrefix + chattools.RunTests + "," + chatsPrefix + chattools.TestResults
	if !strings.Contains(mcp, want) {
		t.Errorf("the tools:\n%s", mcp)
	}
	if !strings.Contains(argv, chattools.Guidance(chatsPrefix)) || !strings.Contains(argv, chattools.TestGuidance(chatsPrefix)) ||
		!strings.Contains(argv, "select:"+chatsPrefix+chattools.TestsFormat) {
		t.Errorf("the system prompt:\n%s", argv)
	}
	if !strings.Contains(called, "run_tests failed: Socrates' tests could not be reached") {
		t.Errorf("run_tests: %q", called)
	}
}
