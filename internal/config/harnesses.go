package config

import (
	"strings"
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

// Harness ids. The set is closed: these four are what a session can be.
const (
	HarnessShell    = "shell"
	HarnessClaude   = "claude"
	HarnessCodex    = "codex"
	HarnessOpenCode = "opencode"
)

// KnownHarnesses is every harness, in the order the picker offers them.
var KnownHarnesses = []string{HarnessShell, HarnessClaude, HarnessCodex, HarnessOpenCode}

// IsHarness reports whether id names one of them.
func IsHarness(id string) bool {
	for _, h := range KnownHarnesses {
		if id == h {
			return true
		}
	}
	return false
}

// HarnessSettings is what the user decides about the four programs a session
// can be. Everything else about them - how they are attached to, what their
// screen looks like, what they are allowed to do - is the app's business, not
// a setting.
type HarnessSettings struct {
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
	// Models is the person's own short list: the models the new-session sheet
	// offers for this harness, in this order, each with the effort it starts
	// on. Empty means "offer everything the harness reports", which is what a
	// fresh installation wants and what a four-hundred-entry list does not.
	Models []ModelPick `json:"models,omitempty"`
}

// ModelPick is one entry of that short list. The id is in the harness's own
// naming, picked from the discovered list or typed in - a typed one is not
// checked against anything here, because the discovery may simply be older
// than the model.
type ModelPick struct {
	ID     string `json:"id"`
	Effort string `json:"effort,omitempty"` // an effort level, "" = the model's own
}

// ShellOptions configures a plain login shell. There is no model, no effort
// and no permission story: it is the user's own shell, with their own rights,
// started as a login shell so the profile that sets up PATH and the prompt is
// read.
type ShellOptions struct {
	Common
}

// ClaudeOptions is the Claude Code launch surface: which binary, and what a
// session starts on when it was started without a choice of its own. The
// permission policy, the settings file, the theme and the environment are
// fixed in internal/harnesses/claude.go.
type ClaudeOptions struct {
	Common
	DefaultModel  string `json:"default_model"`  // --model
	DefaultEffort string `json:"default_effort"` // --effort
}

// CodexOptions is the Codex launch surface, on the same terms.
type CodexOptions struct {
	Common
	DefaultModel  string `json:"default_model"`  // -m
	DefaultEffort string `json:"default_effort"` // -c model_reasoning_effort
}

// OpenCodeOptions is the OpenCode launch surface. There is no default effort
// here because OpenCode has no reasoning-effort flag: the level a model runs
// at is part of the model id it is chosen with, which is what the Models list
// above already carries.
type OpenCodeOptions struct {
	Common
	DefaultModel string `json:"default_model"` // -m provider/model
}

// CodexEfforts is a closed list because Codex does not validate the value
// itself: a typo would reach the model as configuration and be ignored.
var CodexEfforts = []string{"minimal", "low", "medium", "high", "xhigh"}

// DefaultHarnesses is the launch surface of a fresh installation: all four
// offered, none of them overridden.
func DefaultHarnesses() HarnessSettings {
	return HarnessSettings{
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

// normalize tidies the parts a person types into. It never changes a switch:
// the dashboard sends the whole document, and a program that was turned off
// has to stay off, so Enabled is only ever set to true in Default().
func (c *Common) normalize() {
	c.Binary = strings.TrimSpace(c.Binary)
	c.Models = normalizePicks(c.Models)
}

func (h *HarnessSettings) normalize() {
	h.Shell.Common.normalize()

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
// every effort onto a level the harnesses understand.
func normalizePicks(in []ModelPick) []ModelPick {
	var out []ModelPick
	seen := map[string]bool{}
	for _, p := range in {
		id := strings.TrimSpace(p.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, ModelPick{ID: id, Effort: NormalizeEffort(p.Effort)})
	}
	return out
}

// validate has nothing left to refuse. Every field above is either a switch, a
// path or a value normalize closes onto a known list, so a save can no longer
// carry a typo that only shows up as a session which will not start. It stays
// as a method because Settings.Validate calls it, and because the next basic
// setting may well need it again.
func (h HarnessSettings) validate() error { return nil }
