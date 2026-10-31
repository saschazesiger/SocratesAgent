package chattests

import "time"

// TestView is a test as the API sends it: its fields with what is derived
// from them.
type TestView struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Goal        string    `json:"goal"`
	Environment string    `json:"environment"`
	Tags        []string  `json:"tags"`
	Steps       []Step    `json:"steps"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	StepsAt     time.Time `json:"steps_at"`
	Skip        *Skip     `json:"skip"`
	Override    *Override `json:"override"`
	// Status is the test's status after precedence (Test.Status).
	Status string `json:"status"`
	// Stale says that the steps changed since the latest run began.
	Stale bool `json:"stale"`
	// Latest is the latest run in full, or null.
	Latest *Run `json:"latest"`
	// Runs are every run, oldest first: in full, or (for a list) only
	// their number, verdict, environment and times.
	Runs []Run `json:"runs"`
	// Events are the test's history, in full only.
	Events []Event `json:"events,omitempty"`
}

// View is what the API sends of a test. full sends every run in full and
// the events, as the test viewer needs them; else the runs are summaries
// beside the latest one in full, as the panel's list needs it.
func View(t Test, full bool) TestView {
	v := TestView{
		ID: t.ID, Title: t.Title, Goal: t.Goal, Environment: t.Environment, Tags: t.Tags, Steps: t.Steps,
		CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt, StepsAt: t.StepsAt, Skip: t.Skip, Override: t.Override,
		Status: t.Status(), Stale: t.Stale(), Runs: make([]Run, len(t.Runs)),
	}
	if v.Tags == nil {
		v.Tags = []string{}
	}
	if v.Steps == nil {
		v.Steps = []Step{}
	}
	if r := t.Latest(); r != nil {
		latest := *r
		v.Latest = &latest
	}
	for i, r := range t.Runs {
		if full {
			v.Runs[i] = r
		} else {
			v.Runs[i] = Run{N: r.N, StartedAt: r.StartedAt, EndedAt: r.EndedAt, Environment: r.Environment, Verdict: r.Verdict}
		}
	}
	if full {
		v.Events = t.Events
	}
	return v
}

// Views is View of every test.
func Views(tests []Test, full bool) []TestView {
	out := make([]TestView, len(tests))
	for i, t := range tests {
		out[i] = View(t, full)
	}
	return out
}
