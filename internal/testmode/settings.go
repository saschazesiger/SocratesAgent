package testmode

import (
	"slices"
	"strings"
)

// Settings are a repository's settings for its runs, which a run may
// override and keeps as it started with.
type Settings struct {
	Cell     Role `json:"cell"`
	Reviewer Role `json:"reviewer"`
	Analyst  Role `json:"analyst"`
	// Parallel is how many cells run at once; 0 is the pools' capacity, or
	// DefaultParallel for a catalog without pools.
	Parallel       int  `json:"parallel"`
	ReviewParallel int  `json:"review_parallel"`
	Review         bool `json:"review"`
	Analysis       bool `json:"analysis"`
	// Attempts is how many attempts a cell gets at most (the first and the
	// automatic retries of a blocked one).
	Attempts int `json:"attempts"`
	// KeepRuns is how many runs are kept; the pinned ones count on top.
	KeepRuns int `json:"keep_runs"`
}

// Defaults of the settings (TEST-MODE.md, Settings).
const (
	DefaultParallel       = 4
	DefaultReviewParallel = 2
	DefaultAttempts       = 2
	DefaultKeepRuns       = 20
	// ReviewBatch is about how many cells one reviewer takes.
	ReviewBatch = 6
	// DefaultMaxSessions is how many test sessions run at once on the whole
	// server, over every repository.
	DefaultMaxSessions = 8
)

// DefaultSettings is a repository's settings before anybody changed them:
// Claude Code, Sonnet running the cells, Opus reviewing and analysing (Device
// QA's model evaluation, 2026-10-28).
func DefaultSettings() Settings {
	return Settings{
		Cell:           Role{Harness: "claude", Model: "claude-sonnet-5-5", Effort: "medium"},
		Reviewer:       Role{Harness: "claude", Model: "claude-opus-5-5", Effort: "high"},
		Analyst:        Role{Harness: "claude", Model: "claude-opus-5-5", Effort: "high"},
		ReviewParallel: DefaultReviewParallel,
		Review:         true,
		Analysis:       true,
		Attempts:       DefaultAttempts,
		KeepRuns:       DefaultKeepRuns,
	}
}

// Normalize puts every setting into its range.
func (s *Settings) Normalize() {
	d := DefaultSettings()
	for _, r := range []struct{ got, def *Role }{{&s.Cell, &d.Cell}, {&s.Reviewer, &d.Reviewer}, {&s.Analyst, &d.Analyst}} {
		r.got.Harness = strings.TrimSpace(r.got.Harness)
		r.got.Model = strings.TrimSpace(r.got.Model)
		r.got.Effort = strings.TrimSpace(r.got.Effort)
		r.got.Account = ""
		var accounts []string
		for _, a := range r.got.Accounts {
			if a = strings.TrimSpace(a); a != "" && !slices.Contains(accounts, a) {
				accounts = append(accounts, a)
			}
		}
		r.got.Accounts = accounts
		if r.got.Harness == "" {
			*r.got = *r.def
		}
	}
	s.Parallel = clamp(s.Parallel, 0, 64)
	if s.ReviewParallel <= 0 {
		s.ReviewParallel = DefaultReviewParallel
	}
	s.ReviewParallel = clamp(s.ReviewParallel, 1, 16)
	if s.Attempts <= 0 {
		s.Attempts = DefaultAttempts
	}
	s.Attempts = clamp(s.Attempts, 1, 5)
	if s.KeepRuns <= 0 {
		s.KeepRuns = DefaultKeepRuns
	}
	s.KeepRuns = clamp(s.KeepRuns, 1, 1000)
}

func clamp(v, lo, hi int) int { return max(lo, min(hi, v)) }

// Global is the settings over every repository.
type Global struct {
	MaxSessions int `json:"max_sessions"`
}

// Normalize puts the settings into their range.
func (g *Global) Normalize() {
	if g.MaxSessions <= 0 {
		g.MaxSessions = DefaultMaxSessions
	}
	g.MaxSessions = clamp(g.MaxSessions, 1, 128)
}

// Selection is which cells of the catalog a run runs. Every list that is
// set narrows it; Dims narrows each named dimension to its values.
type Selection struct {
	Areas      []string            `json:"areas,omitempty"`
	Tests      []string            `json:"tests,omitempty"`
	Tags       []string            `json:"tags,omitempty"`
	Priorities []string            `json:"priorities,omitempty"`
	Dims       map[string][]string `json:"dims,omitempty"`
	// ProblemsOf is a run whose cells that did not pass are run again: only
	// those, intersected with the rest.
	ProblemsOf string `json:"problems_of,omitempty"`
}

// Describe is the selection in a few words.
func (s Selection) Describe() string {
	var parts []string
	add := func(label string, list []string) {
		if len(list) > 0 {
			parts = append(parts, label+" "+strings.Join(list, ", "))
		}
	}
	add("areas", s.Areas)
	add("tests", s.Tests)
	add("tags", s.Tags)
	add("priorities", s.Priorities)
	keys := make([]string, 0, len(s.Dims))
	for k := range s.Dims {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		add(k, s.Dims[k])
	}
	if s.ProblemsOf != "" {
		parts = append(parts, "problems of "+s.ProblemsOf)
	}
	if len(parts) == 0 {
		return "everything"
	}
	return strings.Join(parts, "; ")
}

// Repo is a repository the Tests page knows.
type Repo struct {
	Key   string `json:"key"`
	Path  string `json:"path"`
	Added bool   `json:"added,omitempty"` // added by hand rather than found
	// Hidden is a repository removed from the list by hand, which finding it
	// again does not bring back; adding it by hand does.
	Hidden bool `json:"hidden,omitempty"`
}
