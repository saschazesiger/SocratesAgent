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
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/harness"
)

// cacheKey is where the last discovery is kept between restarts.
const cacheKey = "agents_catalog"

// cacheSchema is stamped onto every cached snapshot, and a snapshot carrying
// another number is thrown away on load. Bump it whenever what a discovery
// puts into a snapshot changes meaning - a new effort level policy, a new
// field the picker relies on - so an upgraded Socrates does not serve the
// previous version's answer for the next half hour.
//
//	1  the first shape
//	2  efforts are every level the agent names, not the low/medium/high cut
const cacheSchema = 2

// TTL is how long a discovered catalogue is treated as current. Models are
// added to a CLI a few times a year; half an hour is generous either way.
const TTL = 30 * time.Minute

// versionTimeout is how long an agent gets to print its version. A CLI that
// cannot manage that in five seconds is not one a chat should be waiting on.
const versionTimeout = 5 * time.Second

// DiscoveryBudget is how long the probes get altogether. It is generous
// because it is not a request's patience: nobody is held up by it, and the
// point of the budget is only that a wedged CLI cannot keep a goroutine and a
// subprocess for the life of the server.
const DiscoveryBudget = 2 * time.Minute

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
	// Picks is the person's own short list from the dashboard, in their
	// order, each entry filled in from Models where the id is known. Empty
	// means the sheet offers Models instead.
	Picks []harness.Model `json:"picks"`
	Notes string          `json:"notes"`
	Error string          `json:"error"`
}

// AllEfforts is every effort level any of this agent's models offers, in
// order: what a model the agent has not reported can be offered, since there
// is nothing narrower to go on.
func (a Agent) AllEfforts() []string {
	var all []string
	for _, m := range a.Models {
		all = append(all, m.Efforts...)
	}
	return harness.OrderEfforts(all)
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
	Schema      int     `json:"schema,omitempty"`
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

	mu       sync.Mutex
	snapshot Snapshot
	loaded   bool
	// inflight is the discovery that is running, if any. Everyone who asks
	// while it runs waits on the same one: three CLIs asked for their model
	// list twice over is a slow machine for no reason.
	inflight *discovery
}

// discovery is one run of the probes. It runs on a context of its own and
// closes done when it is finished, whoever is still waiting by then.
type discovery struct {
	done chan struct{}
	snap Snapshot
	// complete says the probes ran to the end. A discovery cut short has
	// nothing worth remembering: its agents are all "context canceled", and
	// caching that for half an hour is how a first run ends up with a picker
	// that offers no models at all.
	complete bool
}

// New builds a catalogue over the store the settings live in.
func New(st Store, settings func() config.Settings) *Catalog {
	return &Catalog{store: st, settings: settings}
}

// Get returns the catalogue, discovering it only if nothing is cached. A stale
// cache is returned at once and renewed behind the request: a picker that has
// to wait twenty seconds for a model list is a picker nobody opens twice.
//
// On a cold cache the request waits for the discovery, but it does not own it.
// The browser gives up after twelve seconds and a cold codex plus opencode
// probe can take longer than that, so a discovery tied to the request's
// context was cancelled halfway - and the half-made answer, every agent
// carrying "context canceled", was what got cached for the next half hour.
// The first run of a fresh installation therefore offered a picker with no
// models in it. Detaching the work is the fix: whoever gave up, the probes
// finish and the answer is there for the next request a moment later.
func (c *Catalog) Get(ctx context.Context) Snapshot {
	snap, ok := c.Cached()
	if ok {
		if time.Since(time.UnixMilli(snap.RefreshedAt)) > TTL {
			c.discover()
		}
		return snap
	}
	d := c.discover()
	select {
	case <-d.done:
		return c.withSettings(d.snap)
	case <-ctx.Done():
		// This browser is gone. The discovery is not: it finishes, it caches,
		// and the next request - a reload a few seconds later - gets all of it.
		return Snapshot{Agents: []Agent{}}
	}
}

// Cached is what is known without asking anything. It is what chat creation
// validates against, and "nothing cached" is an answer it accepts: a chat
// queued offline must never be failed permanently by a cache miss.
func (c *Catalog) Cached() (Snapshot, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.loaded {
		var stored Snapshot
		switch err := c.store.GetJSON(cacheKey, &stored); {
		case err != nil || len(stored.Agents) == 0:
		case stored.Schema != cacheSchema:
			log.Printf("catalog: the cached agent list is from another version of Socrates; discovering again")
		default:
			// A curated list is this build's, not the cache's: it costs no
			// subprocess to read, and the cached copy would be the previous
			// build's list for as long as the cache is fresh.
			for i := range stored.Agents {
				refreshStatic(&stored.Agents[i])
			}
			c.snapshot = stored
		}
		c.loaded = true
	}
	if len(c.snapshot.Agents) == 0 {
		return Snapshot{}, false
	}
	return c.withSettings(c.snapshot), true
}

// Refresh throws the cache away and asks every installed CLI again. It is what
// the admin dashboard's Refresh button calls, and the caller waits for it - but
// only with its own patience: the probes run detached, exactly as they do for
// a cold Get, so a browser that gives up cannot leave a half-made catalogue
// behind.
func (c *Catalog) Refresh(ctx context.Context) Snapshot {
	c.mu.Lock()
	c.snapshot = Snapshot{}
	c.loaded = true
	c.mu.Unlock()
	if err := c.store.SetJSON(cacheKey, Snapshot{}); err != nil {
		log.Printf("catalog: could not clear the cached agent list: %v", err)
	}

	d := c.discover()
	select {
	case <-d.done:
		return c.withSettings(d.snap)
	case <-ctx.Done():
		return Snapshot{Agents: []Agent{}}
	}
}

// discover returns the discovery that is running, starting one if there is
// none. It always runs on a context of its own, never a request's.
func (c *Catalog) discover() *discovery {
	c.mu.Lock()
	if c.inflight != nil {
		d := c.inflight
		c.mu.Unlock()
		return d
	}
	d := &discovery{done: make(chan struct{})}
	c.inflight = d
	c.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), DiscoveryBudget)
		defer cancel()
		snap := c.probeAll(ctx)
		// Only a discovery that ran to the end is remembered. One that was cut
		// short reports every agent as "context canceled", and half an hour of
		// that is a picker with nothing in it.
		complete := ctx.Err() == nil
		if complete {
			c.mu.Lock()
			c.snapshot = snap
			c.loaded = true
			c.mu.Unlock()
			if err := c.store.SetJSON(cacheKey, snap); err != nil {
				// A catalogue that cannot be cached is still a catalogue; it
				// just costs three subprocess spawns again after a restart.
				log.Printf("catalog: could not cache the agent list: %v", err)
			}
		} else {
			log.Printf("catalog: the agent probes did not finish within %s; nothing was cached", DiscoveryBudget)
		}

		// Everything is in place before anybody is told the discovery is over,
		// so a caller that has just been handed the answer can also read it
		// back out of the cache.
		c.mu.Lock()
		d.snap, d.complete = snap, complete
		c.inflight = nil
		c.mu.Unlock()
		close(d.done)
	}()
	return d
}

// probeAll asks every agent the settings know about where it is and what it
// can run.
func (c *Catalog) probeAll(ctx context.Context) Snapshot {
	settings := c.settings()
	snap := Snapshot{RefreshedAt: time.Now().UnixMilli(), Schema: cacheSchema}
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
		snap.Agents = append(snap.Agents, discoverOne(ctx, desc, entry))
	}
	return snap
}

// withSettings overlays the parts of an entry that are a setting rather than a
// discovery, so switching an agent off or changing the short list in the
// dashboard takes effect without spawning anything.
func (c *Catalog) withSettings(snap Snapshot) Snapshot {
	settings := c.settings()
	out := Snapshot{RefreshedAt: snap.RefreshedAt, Schema: snap.Schema, Agents: make([]Agent, 0, len(snap.Agents))}
	for _, a := range snap.Agents {
		if a.Models == nil {
			a.Models = []harness.Model{}
		}
		a.Picks = []harness.Model{}
		if entry, ok := settings.Agents.Entry(a.ID); ok {
			a.Enabled = entry.Enabled
			a.Picks = Picks(a, entry.Models)
		}
		out.Agents = append(out.Agents, a)
	}
	return out
}

// Picks fills the dashboard's short list in from what the agent reported: a
// known id keeps its label, hint and effort levels, and an id the agent has
// not reported - typed in, or newer than the discovery - is offered as it was
// typed, with every effort level when the agent has an effort mechanism at
// all, because there is nothing to narrow it down with. The first entry is
// the default, so the sheet starts on it.
func Picks(a Agent, picks []config.ModelPick) []harness.Model {
	out := make([]harness.Model, 0, len(picks))
	for i, p := range picks {
		m, known := a.Model(p.ID)
		if !known {
			m = harness.Model{ID: p.ID, Label: p.ID, Hint: "typed in the dashboard", Efforts: a.AllEfforts()}
		}
		m.DefaultEffort = p.Effort
		m.Default = i == 0
		out = append(out, m)
	}
	return out
}

// discoverOne asks one agent where it is, what version it is and what it can run.
func discoverOne(ctx context.Context, desc harness.Descriptor, entry config.AgentEntry) Agent {
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
	applyCatalog(&a, cat)
	return a
}

// applyCatalog puts what an adapter reported into the agent's entry.
func applyCatalog(a *Agent, cat harness.Catalog) {
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
		a.Models[i].Efforts = harness.OrderEfforts(a.Models[i].Efforts)
		if len(a.Models[i].Efforts) > 0 {
			a.HasEffort = true
		}
		if a.Models[i].Default && a.DefaultModel == "" {
			a.DefaultModel = a.Models[i].ID
		}
	}
}

// refreshStatic replaces a cached curated list with this build's. A static
// Discover spawns nothing, so it is cheap enough to run on every load.
func refreshStatic(a *Agent) {
	if !a.Static || !a.Installed {
		return
	}
	desc, ok := harness.Get(a.ID)
	if !ok || desc.Discover == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cat, err := desc.Discover(ctx, a.Path)
	if err != nil || !cat.Static {
		return
	}
	a.DefaultModel = desc.DefaultModel
	applyCatalog(a, cat)
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
