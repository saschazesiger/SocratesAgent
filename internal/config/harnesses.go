package config

import (
	"strings"
	"unicode/utf8"
)

// This file is the option catalogue: one struct per harness, every field a
// setting the admin dashboard exposes, and nothing else.
//
// It is deliberately short. Until 2026-09-02 this file carried some seventy
// fields - permission modes, sandbox policies, remote control, raw flag and
// environment escape hatches, generated-config textareas - and every one of
// them was a way for a person to make a session that would not start, or one
// that was less safe than the app promises. The product owner reversed that:
// the dashboard now exposes the basics only, and everything else is fixed
// policy compiled into the launchers. See docs/design/HARNESS-POLICY.md for
// what that policy is and where each flag in it was verified.
//
// What is left here is what an installation genuinely differs on: whether a
// program is offered at all, where its binary lives, which models its picker
// should list, and the model and effort a session starts on when the person
// starting it did not choose. A stored document from an older version simply
// keeps its dead keys; they decode into nothing and are dropped on the next
// save.

// Harness ids. The set is closed: these five are what a session can be.
const (
	HarnessVSCode   = "vscode"
	HarnessShell    = "shell"
	HarnessClaude   = "claude"
	HarnessCodex    = "codex"
	HarnessOpenCode = "opencode"
)

// KnownHarnesses is every harness, in the order the picker offers them.
var KnownHarnesses = []string{HarnessShell, HarnessClaude, HarnessCodex, HarnessOpenCode, HarnessVSCode}

// IsHarness reports whether id names one of them.
func IsHarness(id string) bool {
	for _, h := range KnownHarnesses {
		if id == h {
			return true
		}
	}
	return false
}

// HarnessSettings is what the user decides about the five programs a session
// can be. Everything else about them - how they are attached to, what their
// screen looks like, what they are allowed to do - is the app's business, not
// a setting.
type HarnessSettings struct {
	VSCode   VSCodeOptions   `json:"vscode"`
	Shell    ShellOptions    `json:"shell"`
	Claude   ClaudeOptions   `json:"claude"`
	Codex    CodexOptions    `json:"codex"`
	OpenCode OpenCodeOptions `json:"opencode"`
}

// Common is the handful of settings every harness has: whether it is offered
// at all, where its program lives, and which models its picker lists.
type Common struct {
	Enabled bool `json:"enabled"`
	// Binary overrides where the program is found. Empty means "look it up on
	// PATH", which is what a normal installation wants.
	Binary string `json:"binary,omitempty"`
	// Models is the person's favourites: the models the pickers offer first
	// for this harness, in this order, each with the effort it starts on, and
	// the first of them the one a new chat starts on. Every other model the
	// harness reports stays on offer below them - a four-hundred-entry list is
	// searched, not shortened.
	Models []ModelPick `json:"models,omitempty"`
}

// ModelPick is one of those favourites. The id is in the harness's own
// naming, picked from the discovered list or typed in - a typed one is not
// checked against anything here, because the discovery may simply be older
// than the model.
type ModelPick struct {
	ID     string `json:"id"`
	Effort string `json:"effort,omitempty"` // an effort level, "" = the model's own
	// Instructions is what the person wants a chat on this model to be told,
	// in this harness: it goes into the agent's instructions while the chat
	// runs on this model, and only then (agent.ModelInstructions).
	Instructions string `json:"instructions,omitempty"`
}

// MaxModelInstructions is the most a model's instructions may hold, in
// bytes. They travel as one command-line argument (Claude Code's
// --append-system-prompt), which Linux caps at 128 KiB, and a prompt this
// long is already more than a model's instructions should be.
const MaxModelInstructions = 32 << 10

// ShellOptions configures a plain login shell. There is no model, no effort
// and no permission story: it is the user's own shell, with their own rights,
// started as a login shell so the profile that sets up PATH and the prompt is
// read.
type ShellOptions struct {
	Common
}

// VSCodeOptions configures the browser IDE executable; it has no model picker.
type VSCodeOptions struct {
	Common
}

// ClaudeOptions is what Claude Code's chats are started with: which binary,
// and the model and effort a chat starts on when it was started without a
// choice of its own. The permission policy and the environment are fixed in
// its adapter (internal/agent/claude).
type ClaudeOptions struct {
	Common
	DefaultModel  string `json:"default_model"`
	DefaultEffort string `json:"default_effort"`
}

// CodexOptions is the same for Codex's chats.
type CodexOptions struct {
	Common
	DefaultModel  string `json:"default_model"`
	DefaultEffort string `json:"default_effort"`
}

// OpenCodeOptions is the same for OpenCode's chats. There is no default
// effort here because OpenCode's levels - its variants - are a model's own:
// each favourite in the Models list above carries the one it starts on, and
// any other model starts on the variant OpenCode's TUI last ran it at.
type OpenCodeOptions struct {
	Common
	DefaultModel string `json:"default_model"`
}

// CodexEfforts is a closed list because Codex does not validate the value
// itself: a typo would reach the model as configuration and be ignored.
var CodexEfforts = []string{"minimal", "low", "medium", "high", "xhigh"}

// DefaultHarnesses is the launch surface of a fresh installation: all five
// offered, none of them overridden.
func DefaultHarnesses() HarnessSettings {
	return HarnessSettings{
		VSCode:   VSCodeOptions{Common: Common{Enabled: true}},
		Shell:    ShellOptions{Common: Common{Enabled: true}},
		Claude:   ClaudeOptions{Common: Common{Enabled: true}},
		Codex:    CodexOptions{Common: Common{Enabled: true}},
		OpenCode: OpenCodeOptions{Common: Common{Enabled: true}},
	}
}

// Entry returns the settings every harness shares, and whether the id names a
// harness at all. It is what discovery and the picker ask: is this program
// offered, where does it live, and which models did the user shortlist.
func (h HarnessSettings) Entry(id string) (Common, bool) {
	switch id {
	case HarnessVSCode:
		return h.VSCode.Common, true
	case HarnessShell:
		return h.Shell.Common, true
	case HarnessClaude:
		return h.Claude.Common, true
	case HarnessCodex:
		return h.Codex.Common, true
	case HarnessOpenCode:
		return h.OpenCode.Common, true
	}
	return Common{}, false
}

// ModelInstructions is the instructions of the harness id's favourites that
// have any, by model id, nil when none has.
func (h HarnessSettings) ModelInstructions(id string) map[string]string {
	entry, _ := h.Entry(id)
	var out map[string]string
	for _, p := range entry.Models {
		if p.Instructions == "" {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[p.ID] = p.Instructions
	}
	return out
}

// Defaults is the model and effort a chat of the agent id starts on when it
// is started without a choice of its own: the dashboard's Default model and
// Default effort, empty for a program that has neither.
func (h HarnessSettings) Defaults(id string) (model, effort string) {
	switch id {
	case HarnessClaude:
		return h.Claude.DefaultModel, h.Claude.DefaultEffort
	case HarnessCodex:
		return h.Codex.DefaultModel, h.Codex.DefaultEffort
	case HarnessOpenCode:
		return h.OpenCode.DefaultModel, ""
	}
	return "", ""
}

// normalize tidies the parts a person types into. It never changes a switch:
// the dashboard sends the whole document, and a program that was turned off
// has to stay off, so Enabled is only ever set to true in Default().
func (c *Common) normalize() {
	c.Binary = strings.TrimSpace(c.Binary)
	c.Models = normalizePicks(c.Models)
}

func (h *HarnessSettings) normalize() {
	h.Shell.Common.normalize()
	h.VSCode.Common.normalize()

	c := &h.Claude
	c.Common.normalize()
	c.DefaultModel = strings.TrimSpace(c.DefaultModel)
	c.DefaultEffort = NormalizeEffort(c.DefaultEffort)

	x := &h.Codex
	x.Common.normalize()
	x.DefaultModel = strings.TrimSpace(x.DefaultModel)
	x.DefaultEffort = oneOf(x.DefaultEffort, CodexEfforts, "")

	o := &h.OpenCode
	o.Common.normalize()
	o.DefaultModel = strings.TrimSpace(o.DefaultModel)
}

// oneOf keeps a value that is on the list and replaces anything else with the
// fallback. A closed list in the dashboard is only closed if the server says
// so too: the API takes whatever is sent to it.
func oneOf(value string, allowed []string, fallback string) string {
	value = strings.TrimSpace(value)
	for _, a := range allowed {
		if value == a {
			return value
		}
	}
	return fallback
}

// normalizePicks trims every id, drops the empty ones and the repeats (the
// first occurrence wins, so the order a person arranged survives), and maps
// every effort onto a level the harnesses understand. Instructions lose the
// blank lines around them and whatever is over MaxModelInstructions.
func normalizePicks(in []ModelPick) []ModelPick {
	var out []ModelPick
	seen := map[string]bool{}
	for _, p := range in {
		id := strings.TrimSpace(p.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, ModelPick{ID: id, Effort: NormalizeEffort(p.Effort),
			Instructions: cutUTF8(strings.TrimSpace(p.Instructions), MaxModelInstructions)})
	}
	return out
}

// cutUTF8 is s cut to at most max bytes, on a character's edge.
func cutUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return strings.TrimSpace(s[:max])
}

// validate has nothing left to refuse. Every field above is either a switch, a
// path or a value normalize closes onto a known list, so a save can no longer
// carry a typo that only shows up as a session which will not start. It stays
// as a method because Settings.Validate calls it, and because the next basic
// setting may well need it again.
func (h HarnessSettings) validate() error { return nil }
