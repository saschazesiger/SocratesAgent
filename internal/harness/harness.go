// Package harness is the contract between Socrates and one coding agent. An
// Adapter turns that agent's native headless protocol - Claude Code's
// stream-json, Codex's JSON-RPC app server, OpenCode's HTTP and SSE server -
// into one normalised stream of Events.
//
// The package runs inside an agent-host process and knows nothing about the
// database, the web server or the engine. It may depend on internal/proc, and
// on nothing else of this application.
package harness

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Adapter is one coding agent seen as a turn-taking session. Exactly one
// adapter lives inside one agent-host process, and it owns exactly one CLI
// process (or server) for the lifetime of that host.
//
// Rules every adapter obeys:
//   - Start returns only when the session can take a turn. Anything that
//     happens later is reported on Events.
//   - Between a Send and the matching turn_finished, no second Send arrives:
//     the engine enforces one turn per chat.
//   - Every Send eventually produces exactly one turn_finished with the same
//     TurnID, or a fatal. There is no third outcome.
//   - Events is closed exactly once, after the last event, when the adapter
//     is finished.
//   - Every method is safe to call from a goroutine other than the one that
//     ranges over Events.
type Adapter interface {
	// Start launches the agent and brings the session up. When spec.SessionID
	// is non-empty it resumes that native session instead of creating one.
	// A session_id event is emitted as soon as the native id is known,
	// including when it is the one that was passed in.
	Start(ctx context.Context, spec Spec) error

	// Send delivers one user message and begins a turn. It returns as soon as
	// the agent has accepted the message; the turn itself ends with a
	// turn_finished event carrying this turnID.
	Send(ctx context.Context, turnID, text string) error

	// Interrupt cancels the turn in flight. It is a no-op when nothing is
	// running. The turn ends with turn_finished{outcome:"interrupted"}.
	Interrupt(ctx context.Context) error

	// Events is the single normalised event stream. Every event this adapter
	// ever produces goes through here, in order.
	Events() <-chan Event

	// Close shuts the agent down, giving it up to grace to exit cleanly
	// before it is killed. It is idempotent.
	Close(ctx context.Context, grace time.Duration) error
}

// Spec is everything an adapter needs to run one chat's session. It is also
// the on-disk form: the host writes it into spec.json, so a host restarted by
// hand with only --dir can rebuild the session.
type Spec struct {
	Agent     string   `json:"agent"`            // "claude" | "codex" | "opencode"
	Model     string   `json:"model"`            // agent-native model id, never an OpenRouter id
	Effort    string   `json:"effort,omitempty"` // "" | "low" | "medium" | "high"
	Cwd       string   `json:"cwd"`              // absolute working directory for this chat
	ChatID    string   `json:"chat_id"`
	ChatTitle string   `json:"chat_title,omitempty"` // cosmetic, for `claude --name`
	SessionID string   `json:"session_id,omitempty"` // native session/thread id, for resume
	Binary    string   `json:"binary,omitempty"`     // absolute path override; empty = look up Agent's default name on PATH
	ExtraArgs []string `json:"extra_args,omitempty"` // appended verbatim to argv, from settings
	Env       []string `json:"env,omitempty"`        // extra KEY=VALUE on top of os.Environ()
	Dir       string   `json:"dir"`                  // the host directory; adapters may use Dir/agent/ as scratch
}

// Descriptor is what an adapter package registers about itself: enough for the
// picker, the admin card and the diagnostics to describe an agent without
// starting one.
type Descriptor struct {
	ID            string   // "claude" | "codex" | "opencode"
	Label         string   // "Claude Code"
	Binary        string   // default executable name on PATH
	VersionArgs   []string // e.g. []string{"--version"}
	DefaultModel  string
	DefaultEffort string
	HasEffort     bool
	Notes         string // shown in the admin card and /api/agents
	New           func() Adapter
	Discover      func(ctx context.Context, bin string) (Catalog, error)
}

var (
	registryMu sync.RWMutex
	registry   = map[string]Descriptor{}
)

// Register adds an adapter to the registry. It is called from an adapter
// package's init, which is why main blank-imports all three: both roles - the
// web server and an agent host - look agents up by id.
func Register(d Descriptor) {
	if d.ID == "" {
		panic("harness: an adapter registered without an id")
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[d.ID] = d
}

// Get returns one registered adapter.
func Get(id string) (Descriptor, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	d, ok := registry[id]
	return d, ok
}

// IDs lists the registered adapters in a stable order, so a picker built from
// it does not shuffle between two page loads.
func IDs() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(registry))
	for id := range registry {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
