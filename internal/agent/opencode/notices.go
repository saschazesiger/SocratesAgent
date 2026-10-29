//go:build !windows

package opencode

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// NoticeToast is a warning or an error OpenCode showed its own TUI as a
// toast (tui.toast.show): the only word some of its problems get.
const NoticeToast = "toast"

// mcpWait bounds the read of the MCP servers' status: GET /mcp answers once
// OpenCode has tried every server, and a local one that never answers holds
// it up to OpenCode's own timeout (30 s, measured on 1.19.5).
const mcpWait = 2 * time.Minute

// noticeState is what the adapter has said about OpenCode itself, so that
// it is said once.
type noticeState struct {
	// mcp serialises the reads of GET /mcp, and mcpSaid is what the last
	// one said ("" for nothing wrong).
	mcp     sync.Mutex
	mcpSaid string
	// toasts are the toasts already said, by their words.
	toasts map[string]bool
}

// ocMCPStatus is one server of GET /mcp: connected, disabled, failed{error},
// needs_auth, needs_client_registration{error} (verified against 1.19.5).
type ocMCPStatus struct {
	Status string `json:"status"`
	Error  string `json:"error"`
}

// checkMCP reads which MCP servers OpenCode could not start and says so in
// the chat (agent.NoticeMCP), when that changed since it last said it.
// OpenCode reports a server it cannot start or that needs a sign-in nowhere
// else but in a toast of its TUI. Called after a start, a catch-up and an
// mcp.tools.changed (what a server whose connection closed publishes).
func (a *Adapter) checkMCP(ctx context.Context, gen int, cl *client) {
	a.notes.mcp.Lock()
	defer a.notes.mcp.Unlock()
	ctx, cancel := context.WithTimeout(ctx, mcpWait)
	defer cancel()
	var status map[string]ocMCPStatus
	if err := cl.call(ctx, http.MethodGet, "/mcp", nil, &status); err != nil {
		if ctx.Err() == nil {
			log.Printf("opencode: read the MCP servers' status: %v", err)
		}
		return
	}
	text := mcpProblems(status)
	a.mu.Lock()
	say := a.gen == gen && text != "" && text != a.notes.mcpSaid
	if a.gen == gen {
		a.notes.mcpSaid = text
	}
	a.mu.Unlock()
	if say {
		a.out.Emit(agent.Event{Type: agent.TypeNotice, Code: agent.NoticeMCP, Text: text})
	}
}

// mcpProblems is a line for every server that is not running for a reason
// other than the config turning it off, by name.
func mcpProblems(status map[string]ocMCPStatus) string {
	names := make([]string, 0, len(status))
	for name := range status {
		names = append(names, name)
	}
	sort.Strings(names)
	var lines []string
	for _, name := range names {
		s := status[name]
		why := strings.TrimSpace(s.Error)
		switch s.Status {
		case "failed":
			if why == "" {
				why = "it failed"
			}
			lines = append(lines, fmt.Sprintf("OpenCode could not start the MCP server %q: %s", name, why))
		case "needs_auth":
			lines = append(lines, fmt.Sprintf("The MCP server %q needs a sign-in: run opencode mcp auth %s", name, name))
		case "needs_client_registration":
			if why == "" {
				why = "it needs a client ID in the config"
			}
			lines = append(lines, fmt.Sprintf("The MCP server %q needs a sign-in OpenCode cannot start: %s", name, why))
		}
	}
	return strings.Join(lines, "\n")
}

// ocToast is tui.toast.show's payload (verified against 1.19.5).
type ocToast struct {
	Title   string `json:"title"`
	Message string `json:"message"`
	Variant string `json:"variant"`
}

// handleToast says a warning or an error OpenCode showed as a toast, once
// for as long as the adapter lives. A toast about an MCP server is said by
// checkMCP instead, which knows every server's state: the read it starts
// says it once OpenCode has tried them all.
func (a *Adapter) handleToast(gen int, t ocToast) {
	if t.Variant != "warning" && t.Variant != "error" {
		return
	}
	text := strings.TrimSpace(t.Message)
	if title := strings.TrimSpace(t.Title); title != "" && text != "" {
		text = title + ": " + text
	} else if text == "" {
		text = title
	}
	if text == "" {
		return
	}
	a.mu.Lock()
	ours := a.gen == gen
	cl, life := a.cl, a.life
	a.mu.Unlock()
	if !ours {
		return
	}
	if strings.Contains(t.Title, "MCP") {
		if cl != nil && life != nil {
			go a.checkMCP(life, gen, cl)
		}
		return
	}
	a.mu.Lock()
	if a.notes.toasts == nil {
		a.notes.toasts = map[string]bool{}
	}
	said := a.notes.toasts[text]
	a.notes.toasts[text] = true
	a.mu.Unlock()
	if !said {
		a.out.Emit(agent.Event{Type: agent.TypeNotice, Code: NoticeToast, Text: text})
	}
}

// mcpChanged is mcp.tools.changed: a server's tools changed, or its
// connection closed, which leaves it failed.
func (a *Adapter) mcpChanged(gen int) {
	a.mu.Lock()
	cl, life, ours := a.cl, a.life, a.gen == gen
	a.mu.Unlock()
	if ours && cl != nil && life != nil {
		go a.checkMCP(life, gen, cl)
	}
}

// childStatusLocked says in its task part that a subagent's model call is
// retried, and why: once per attempt (childNoticeLocked writes a part only
// when its words change).
func (a *Adapter) childStatusLocked(session string, st ocStatus) {
	if st.Type != statusRetry {
		return
	}
	text := fmt.Sprintf("The subagent's call is retried (attempt %d): %s", st.Attempt, st.Message)
	if st.Action != nil && st.Action.Reason != "" {
		text = "The subagent reached a limit: " + firstNonEmpty(st.Action.Message, st.Action.Title, st.Message)
	}
	a.childNoticeLocked(session, "retry", text, false)
}

// childNoticeLocked shows what happened to a subagent inside the task part
// that runs it: its error, or that its model call is being retried. One
// part of each per task part, the newest word replacing the one before.
func (a *Adapter) childNoticeLocked(session, what, text string, failed bool) {
	c, ok := a.children[session]
	if !ok || text == "" {
		return
	}
	state := agent.PartDone
	if failed {
		state = agent.PartError
	}
	content := []byte(`{"code":"` + what + `"}`)
	a.emitPartLocked(c.parent+"-"+what, c.msg, c.parent, converted{kind: agent.PartNotice, state: state, text: text, content: content})
}
