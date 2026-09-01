// Package script parses FAKE_SCRIPT, the one env var that drives all three
// fake agent binaries (fakeclaude, fakecodex, fakeopencode).
//
// FAKE_SCRIPT holds a JSON array of steps. The same script is replayed on
// *every* turn, from the first one (FK-1), so a two-turn test sets one script
// and expects the same sequence twice.
//
//	[{"do":"text","text":"Looking at it."},
//	 {"do":"tool","name":"Bash","input":"go test ./...","output":"ok\n","exit":0},
//	 {"do":"sleep","ms":200},
//	 {"do":"text","text":"All tests pass."},
//	 {"do":"end","outcome":"ok"}]
//
// See the package doc of internal/harness/fakes for the full table.
package script

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"
)

// Step is one entry of FAKE_SCRIPT. Fields that do not apply to a step's "do"
// are ignored.
type Step struct {
	Do string `json:"do"`

	Text   string `json:"text"`   // text, reason
	Name   string `json:"name"`   // tool, subagent
	Input  string `json:"input"`  // tool, subagent
	Output string `json:"output"` // tool, subagent
	Exit   int    `json:"exit"`   // tool

	MS int `json:"ms"` // sleep

	Outcome string `json:"outcome"` // end: "ok" | "error" | "retry"
	Error   string `json:"error"`   // end
	Twice   bool   `json:"twice"`   // end: emit the turn end twice (C-4 trap)

	Code int `json:"code"` // die

	// Subagents overrides the reported subagent count on an end step. When
	// nil the fakes report the number of subagent steps the turn ran.
	Subagents *int `json:"subagents"`
}

// The recognised values of Step.Do.
const (
	DoText     = "text"
	DoReason   = "reason"
	DoTool     = "tool"
	DoSubagent = "subagent"
	DoAsk      = "ask"
	DoSleep    = "sleep"
	DoEnd      = "end"
	DoHang     = "hang"
	DoDie      = "die"
)

// The recognised values of Step.Outcome on an end step.
const (
	OutcomeOK    = "ok"
	OutcomeError = "error"
	OutcomeRetry = "retry"
)

// EnvScript is the env var holding the script.
const EnvScript = "FAKE_SCRIPT"

// EnvArgvFile is the env var naming a file the fakes append one JSON array of
// strings to per recorded event (argv at start, per-turn parameters later).
const EnvArgvFile = "FAKE_ARGV_FILE"

// Default is used when FAKE_SCRIPT is unset or empty, so a fake dropped on
// PATH does something sensible without any configuration.
var Default = []Step{
	{Do: DoText, Text: "ok"},
	{Do: DoEnd, Outcome: OutcomeOK},
}

// Load parses FAKE_SCRIPT.
func Load() ([]Step, error) {
	raw := os.Getenv(EnvScript)
	if raw == "" {
		out := make([]Step, len(Default))
		copy(out, Default)
		return out, nil
	}
	var steps []Step
	if err := json.Unmarshal([]byte(raw), &steps); err != nil {
		return nil, err
	}
	for _, s := range steps {
		switch s.Do {
		case DoText, DoReason, DoTool, DoSubagent, DoAsk, DoSleep, DoEnd, DoHang, DoDie:
		default:
			return nil, errors.New("unknown FAKE_SCRIPT step: " + s.Do)
		}
	}
	return steps, nil
}

// MustLoad is Load, exiting with a message on stderr when the script is bad.
func MustLoad() []Step {
	steps, err := Load()
	if err != nil {
		os.Stderr.WriteString("bad " + EnvScript + ": " + err.Error() + "\n")
		os.Exit(2)
	}
	return steps
}

// LastText returns the index of the last text step, or -1. Codex needs it to
// decide which agentMessage carries phase "final_answer".
func LastText(steps []Step) int {
	last := -1
	for i, s := range steps {
		if s.Do == DoText {
			last = i
		}
	}
	return last
}

// Chunks splits s into n roughly equal pieces, never returning fewer than n
// entries (the tail ones are empty for a very short string). The fakes stream
// text in three chunks.
func Chunks(s string, n int) []string {
	out := make([]string, n)
	r := []rune(s)
	if n <= 0 {
		return nil
	}
	per := len(r) / n
	if per*n < len(r) {
		per++
	}
	for i := 0; i < n; i++ {
		lo := i * per
		if lo > len(r) {
			lo = len(r)
		}
		hi := lo + per
		if hi > len(r) {
			hi = len(r)
		}
		out[i] = string(r[lo:hi])
	}
	// Anything left over (rounding) goes onto the last chunk.
	if per*n < len(r) {
		out[n-1] += string(r[per*n:])
	}
	return out
}

// Sleep waits for ms milliseconds or until ctx is done, reporting whether it
// slept the whole way.
func Sleep(ctx context.Context, ms int) bool {
	if ms <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(time.Duration(ms) * time.Millisecond)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// VersionAsked reports whether argv asks for the version. The three CLIs
// differ in which short alias they accept; the fakes take all of them, since
// the catalogue only ever passes --version and a fake that answered fewer
// aliases could only ever produce a false failure.
func VersionAsked(args []string) bool {
	for _, a := range args {
		switch a {
		case "--version", "-v", "-V":
			return true
		}
	}
	return false
}

// Record appends one JSON array of strings to $FAKE_ARGV_FILE. It is a no-op
// when the variable is unset. Every fake uses the same one-JSON-array-per-line
// format: fakeclaude and fakeopencode record their argv, and both fakecodex
// and fakeopencode record per-turn / per-request parameters as further lines.
func Record(values []string) {
	path := os.Getenv(EnvArgvFile)
	if path == "" {
		return
	}
	line, err := json.Marshal(values)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(line, '\n'))
}
