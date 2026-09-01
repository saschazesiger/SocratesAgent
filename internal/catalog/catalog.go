// Package catalog answers one question: which coding agents are on this
// machine, and what can each of them be run on.
//
// It is the only thing that spawns an agent binary outside a chat, so it is
// also the only place that has to worry about a CLI taking twenty seconds to
// answer. Everything it learns is cached - in memory and in the key/value
// store - so a restart does not cost three subprocess spawns and a request
// never waits on discovery when there is a cached answer.
package catalog

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/harness"
)

// cacheKey is where the last discovery is kept between restarts.
const cacheKey = "agents_catalog"

// TTL is how long a discovered catalogue is treated as current. Models are
// added to a CLI a few times a year; half an hour is generous either way.
const TTL = 30 * time.Minute

// versionTimeout is how long an agent gets to print its version. A CLI that
// cannot manage that in five seconds is not one a chat should be waiting on.
const versionTimeout = 5 * time.Second

// Agent is one program, as the picker and the admin card see it. Every field
// is always present: an agent that is missing says so in `error` rather than
// arriving half filled in.
type Agent struct {
	ID            string          `json:"id"`
	Label         string          `json:"label"`
	Enabled       bool            `json:"enabled"`
	Installed     bool            `json:"installed"`
	Path          string          `json:"path"`
	Version       string          `json:"version"`
	HasEffort     bool            `json:"has_effort"`
	DefaultModel  string          `json:"default_model"`
	DefaultEffort string          `json:"default_effort"`
	Static        bool            `json:"static"`
	Models        []harness.Model `json:"models"`
	Notes         string          `json:"notes"`
	Error         string          `json:"error"`
}

// Model returns one model of this agent by its id.
func (a Agent) Model(id string) (harness.Model, bool) {
	for _, m := range a.Models {
		if m.ID == id {
			return m, true
		}
	}
	return harness.Model{}, false
}

// Snapshot is the whole catalogue at one moment.
type Snapshot struct {
	Agents      []Agent `json:"agents"`
	RefreshedAt int64   `json:"refreshed_at"`
}

// Agent returns one entry of a snapshot.
func (s Snapshot) Agent(id string) (Agent, bool) {
	for _, a := range s.Agents {
		if a.ID == id {
			return a, true
		}
	}
	return Agent{}, false
}

// Store is the little of the database this package needs.
type Store interface {
	GetJSON(key string, out any) error
	SetJSON(key string, in any) error
}

// Catalog is the cached answer plus the machinery to renew it.
type Catalog struct {
	store    Store
	settings func() config.Settings

	mu        sync.Mutex
	snapshot  Snapshot
	loaded    bool
	refreshMu sync.Mutex
	// refreshing keeps a background renewal from being started a second time
	// while the first one is still spawning processes.
	refreshing bool
}

// New builds a catalogue over the store the settings live in.
func New(st Store, settings func() config.Settings) *Catalog {
	return &Catalog{store: st, settings: settings}
}

// Get returns the catalogue, discovering it only if nothing is cached. A stale
// cache is returned at once and renewed in the background: a picker that has
// to wait twenty seconds for a model list is a picker nobody opens twice.
func (c *Catalog) Get(ctx context.Context) Snapshot {
	c.mu.Lock()
	if !c.loaded {
		var stored Snapshot
		if err := c.store.GetJSON(cacheKey, &stored); err == nil && len(stored.Agents) > 0 {
			c.snapshot = stored
		}
		c.loaded = true
	}
	snap := c.snapshot
	c.mu.Unlock()

	if len(snap.Agents) == 0 {
		return c.Refresh(ctx)
	}
	if time.Since(time.UnixMilli(snap.RefreshedAt)) > TTL {
		c.refreshInBackground()
	}
	return c.withSettings(snap)
}

// Cached is what is known without asking anything. It is what chat creation
// validates against, and "nothing cached" is an answer it accepts: a chat
// queued offline must never be failed permanently by a cache miss.
func (c *Catalog) Cached() (Snapshot, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.loaded {
		var stored Snapshot
		if err := c.store.GetJSON(cacheKey, &stored); err == nil && len(stored.Agents) > 0 {
			c.snapshot = stored
		}
		c.loaded = true
	}
	if len(c.snapshot.Agents) == 0 {
		return Snapshot{}, false
	}
	return c.withSettings(c.snapshot), true
}

func (c *Catalog) refreshInBackground() {
	c.mu.Lock()
	if c.refreshing {
		c.mu.Unlock()
		return
	}
	c.refreshing = true
	c.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		c.Refresh(ctx)
		c.mu.Lock()
		c.refreshing = false
		c.mu.Unlock()
	}()
}

// Refresh discovers everything again, synchronously. It is what the admin
// dashboard's Refresh button calls.
func (c *Catalog) Refresh(ctx context.Context) Snapshot {
	// One discovery at a time: three CLIs being asked for their model list
	// twice over is a slow machine for no reason.
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()

	settings := c.settings()
	snap := Snapshot{RefreshedAt: time.Now().UnixMilli()}
	for _, id := range harness.IDs() {
		desc, ok := harness.Get(id)
		if !ok {
			continue
		}
		entry, known := settings.Agents.Entry(id)
		if !known {
			// An adapter with no settings entry is one this build registered
			// for its own tests. It is not offered.
			continue
		}
		snap.Agents = append(snap.Agents, discover(ctx, desc, entry))
	}
	c.mu.Lock()
	c.snapshot = snap
	c.loaded = true
	c.mu.Unlock()
	if err := c.store.SetJSON(cacheKey, snap); err != nil {
		// A catalogue that cannot be cached is still a catalogue.
		_ = err
	}
	return c.withSettings(snap)
}

// withSettings overlays the parts of an entry that are a setting rather than a
// discovery, so switching an agent off in the dashboard takes effect without
// spawning anything.
func (c *Catalog) withSettings(snap Snapshot) Snapshot {
	settings := c.settings()
	out := Snapshot{RefreshedAt: snap.RefreshedAt, Agents: make([]Agent, 0, len(snap.Agents))}
	for _, a := range snap.Agents {
		if entry, ok := settings.Agents.Entry(a.ID); ok {
			a.Enabled = entry.Enabled
		}
		if a.Models == nil {
			a.Models = []harness.Model{}
		}
		out.Agents = append(out.Agents, a)
	}
	return out
}

// discover asks one agent where it is, what version it is and what it can run.
func discover(ctx context.Context, desc harness.Descriptor, entry config.AgentEntry) Agent {
	a := Agent{
		ID: desc.ID, Label: desc.Label, Enabled: entry.Enabled,
		HasEffort: desc.HasEffort, DefaultModel: desc.DefaultModel,
		DefaultEffort: desc.DefaultEffort, Notes: desc.Notes,
		Models: []harness.Model{},
	}
	bin := strings.TrimSpace(entry.Binary)
	if bin == "" {
		bin = desc.Binary
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		a.Error = bin + " is not on this machine (nothing named that on PATH)"
		return a
	}
	a.Installed = true
	a.Path = path
	a.Version = probeVersion(ctx, path, desc.VersionArgs)

	if desc.Discover == nil {
		return a
	}
	cat, err := desc.Discover(ctx, path)
	if err != nil {
		// Installed, but its model list could not be read. The picker still
		// offers it, and a typed id is accepted rather than refused.
		a.Error = err.Error()
		return a
	}
	a.Static = cat.Static
	if cat.Notes != "" {
		a.Notes = cat.Notes
	}
	if cat.Models != nil {
		a.Models = cat.Models
	}
	// has_effort means "at least one of this agent's models offers one". It
	// exists only so the picker can hide the effort control before a model has
	// been chosen; once one is, that model's own list decides.
	a.HasEffort = false
	for i := range a.Models {
		a.Models[i].Efforts = harness.FilterEfforts(a.Models[i].Efforts)
		if len(a.Models[i].Efforts) > 0 {
			a.HasEffort = true
		}
		if a.Models[i].Default && a.DefaultModel == "" {
			a.DefaultModel = a.Models[i].ID
		}
	}
	return a
}

// probeVersion runs the binary's version command and keeps the first readable
// line of it. A failure is not an error worth reporting on its own: the model
// list is the stronger signal that a CLI is usable.
func probeVersion(ctx context.Context, path string, args []string) string {
	if len(args) == 0 {
		args = []string{"--version"}
	}
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, args...).CombinedOutput()
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if err != nil && line == "" {
		return ""
	}
	return line
}
