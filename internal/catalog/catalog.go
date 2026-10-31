// Package catalog answers one question: which coding agents are on this
// machine, and what can each of them be run on.
//
// It is the only thing that spawns a program outside a session, so it is
// also the only place that has to worry about a CLI taking twenty seconds to
// answer. Everything it learns is cached - in memory and in the key/value
// store - so a restart does not cost three subprocess spawns and a request
// never waits on discovery when there is a cached answer.
package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
)

// CacheKey is where the last discovery is kept between restarts.
const CacheKey = "agents_catalog"

// cacheSchema is stamped onto every cached snapshot, and a snapshot carrying
// another number is thrown away on load. Bump it whenever what a discovery
// puts into a snapshot changes meaning - a new effort level policy, a new
// field the picker relies on - so an upgraded Socrates does not serve the
// previous version's answer for the next half hour.
//
//	1  the first shape
//	2  efforts are every level the agent names, not the low/medium/high cut
//	4  OpenCode's models are named, carry their variants, and are every
//	   account's; recent and favorites
const cacheSchema = 4

// TTL is how long a discovered catalogue is treated as current. Models are
// added to a CLI a few times a year; half an hour is generous either way.
const TTL = 30 * time.Minute

// versionTimeout is how long an agent gets to print its version. A CLI that
// cannot manage that in five seconds is not one a new session should wait on.
const versionTimeout = 5 * time.Second

// DiscoveryBudget is how long the probes get altogether. It is generous
// because it is not a request's patience: nobody is held up by it, and the
// point of the budget is only that a wedged CLI cannot keep a goroutine and a
// subprocess for the life of the server.
const DiscoveryBudget = 2 * time.Minute

// updatesEvery is how often, at most, the updater's status file is looked at.
// A stat is cheap but Cached is on every request's path, and an update that
// lands a few seconds before anybody notices is no worse for it.
const updatesEvery = 5 * time.Second

// Agent is one program, as the picker and the admin card see it. Every field
// is always present: an agent that is missing says so in `error` rather than
// arriving half filled in.
type Agent struct {
	ID            string            `json:"id"`
	Label         string            `json:"label"`
	Enabled       bool              `json:"enabled"`
	Installed     bool              `json:"installed"`
	Path          string            `json:"path"`
	Version       string            `json:"version"`
	HasEffort     bool              `json:"has_effort"`
	DefaultModel  string            `json:"default_model"`
	DefaultEffort string            `json:"default_effort"`
	Static        bool              `json:"static"`
	Models        []harnesses.Model `json:"models"`
	// Picks is the person's favourites from the dashboard, in their order,
	// each entry filled in from Models where the id is known. The pickers
	// offer them first and every other model after them.
	Picks []harnesses.Model `json:"picks"`
	// Recent and Favorites are what the agent's own CLI keeps first: the
	// models OpenCode's TUI used last, newest first, and the ones starred
	// there.
	Recent    []string `json:"recent"`
	Favorites []string `json:"favorites"`
	Notes     string   `json:"notes"`
	Error     string   `json:"error"`
	// Folder is the folder's own configuration of the agent, on an entry
	// discovered for one folder (Catalog.Folder) - never on the general one.
	Folder *harnesses.FolderConfig `json:"folder,omitempty"`
}

// AllEfforts is every effort level any of this agent's models offers, in
// order: what a model the agent has not reported can be offered, since there
// is nothing narrower to go on.
func (a Agent) AllEfforts() []string {
	var all []string
	for _, m := range a.Models {
		all = append(all, m.Efforts...)
	}
	return harnesses.OrderEfforts(all)
}

// Model returns one model of this agent by its id.
func (a Agent) Model(id string) (harnesses.Model, bool) {
	for _, m := range a.Models {
		if m.ID == id {
			return m, true
		}
	}
	return harnesses.Model{}, false
}

// Snapshot is the whole catalogue at one moment.
type Snapshot struct {
	Agents      []Agent `json:"agents"`
	RefreshedAt int64   `json:"refreshed_at"`
	Schema      int     `json:"schema,omitempty"`
	// Accounts is Accounts.Fingerprint as it was when the discovery began.
	Accounts string `json:"accounts,omitempty"`
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

// notes is the one sentence the dashboard shows under each harness. It lives
// here rather than in the launcher because it is presentation: what the
// program is for, in the words of the person choosing between four of them.
// Whether it is installed is the card's to say (admin.js programTrouble), so
// a note never says how to install it.
var notes = map[string]string{
	config.HarnessVSCode:   "VS Code in your browser, powered by code-server.",
	config.HarnessShell:    "A plain login shell in the session's working directory.",
	config.HarnessClaude:   "Anthropic's Claude Code, resumed by the session id Socrates chooses for it.",
	config.HarnessCodex:    "OpenAI's Codex, run as a chat over its app server.",
	config.HarnessOpenCode: "OpenCode, run as a chat over its own password-protected server.",
}

// Store is the little of the database this package needs.
type Store interface {
	GetJSON(key string, out any) error
	SetJSON(key string, in any) error
}

// Accounts is what the catalogue is told about the accounts the agents run
// as. An OpenCode account's providers are whatever it signed in to, so its
// models are only known by asking OpenCode as that account.
type Accounts interface {
	// Envs is the environment of every account of the agent, for a
	// discovery. None means the service's own.
	Envs(agent string) [][]string
	// Fingerprint is cheap and changes whenever a discovery through the
	// accounts would find something else: an account added or removed, a
	// sign-in, or a CLI's own model state written.
	Fingerprint() string
}

// Catalog is the cached answer plus the machinery to renew it.
type Catalog struct {
	store    Store
	settings func() config.Settings

	// accounts is what UseAccounts was given, and accountsSeen the last look
	// at its fingerprint (see superseded).
	accounts     Accounts
	accountsSeen time.Time
	accountsNow  string

	mu       sync.Mutex
	snapshot Snapshot
	loaded   bool
	// inflight is the discovery that is running, if any. Everyone who asks
	// while it runs waits on the same one: three CLIs asked for their model
	// list twice over is a slow machine for no reason.
	inflight *discovery

	// updates is what WatchUpdates was told to watch; see there.
	updates updatesWatch

	// folders is the catalogue of an agent in a folder that configures it
	// for itself, by agent and configuration files (folder.go), and
	// folderSlots bounds the discoveries of them that run at once.
	folders     map[string]*folderEntry
	folderSlots chan struct{}
}

// updatesWatch is the little that is remembered about the updater's status
// file between looks at it, so that most looks are one stat and nothing more.
type updatesWatch struct {
	path    string
	every   time.Duration
	checked time.Time
	// mtime and finished are the file as it was last read: when it was
	// written, and whether the run it describes was over by then. The file is
	// read again only when its mtime moves.
	mtime    time.Time
	finished bool
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

// UseAccounts makes the discoveries ask the agents as their accounts, and a
// change to the accounts supersede the cached catalogue the way an update
// does: answered from at once and discovered again behind the request.
func (c *Catalog) UseAccounts(a Accounts) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.accounts = a
	c.accountsSeen = time.Time{}
}

// WatchUpdates points the catalogue at the status file socrates-update.service
// writes - <dataDir>/updates.json - so an update that replaced an agent's
// binary is noticed without waiting out the TTL: the dashboard would otherwise
// go on showing the version that was just replaced for up to half an hour, and
// across a restart for as long as the stored copy is fresh.
//
// A cached catalogue counts as superseded once the file was written after the
// catalogue was discovered and says the run is over ("done" or "failed" - a
// failed run may still have replaced some of the binaries). A superseded
// catalogue is treated exactly like an expired one: the next Get still answers
// from it at once and discovers again behind the request, because a picker
// that waits on three probes after every nightly update is the thing the cache
// is there to prevent. A run still in progress changes nothing; the write that
// ends it does.
//
// The file is looked at no more than once every few seconds, and read only
// when its mtime has moved. A missing or unreadable file is no update. An
// empty path stops the watch. It is safe to call at any time.
func (c *Catalog) WatchUpdates(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.updates = updatesWatch{path: path, every: updatesEvery}
}

// superseded reports whether an update finished after the cached catalogue
// was discovered, or the accounts changed since. It is called with c.mu held.
func (c *Catalog) superseded(now time.Time) bool {
	if len(c.snapshot.Agents) == 0 {
		return false
	}
	if c.accounts != nil {
		if now.Sub(c.accountsSeen) >= updatesEvery {
			c.accountsSeen = now
			c.accountsNow = c.accounts.Fingerprint()
		}
		if c.accountsNow != c.snapshot.Accounts {
			return true
		}
	}
	w := &c.updates
	if w.path == "" {
		return false
	}
	if now.Sub(w.checked) >= w.every {
		w.checked = now
		w.look()
	}
	return w.finished && w.mtime.After(time.UnixMilli(c.snapshot.RefreshedAt))
}

// look stats the status file and, when it has been written since the last
// look, reads what state the run was in. The updater writes it through a
// temporary file and a rename, so a read never sees half of it.
func (w *updatesWatch) look() {
	info, err := os.Stat(w.path)
	if err != nil {
		w.mtime, w.finished = time.Time{}, false
		return
	}
	if info.ModTime().Equal(w.mtime) {
		return
	}
	w.mtime, w.finished = info.ModTime(), false
	raw, err := os.ReadFile(w.path)
	if err != nil {
		return
	}
	var status struct {
		State string `json:"state"`
	}
	if json.Unmarshal(raw, &status) != nil {
		return
	}
	w.finished = status.State == "done" || status.State == "failed"
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
	snap, ok, superseded := c.cached()
	if ok {
		if superseded || time.Since(time.UnixMilli(snap.RefreshedAt)) > TTL {
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

// Cached is what is known without asking anything. It is what session creation
// validates against, and "nothing cached" is an answer it accepts: a session
// queued offline must never be failed permanently by a cache miss.
func (c *Catalog) Cached() (Snapshot, bool) {
	snap, ok, _ := c.cached()
	return snap, ok
}

// cached is Cached, and also whether an update has landed since the answer was
// discovered - which is Get's business, not session creation's: a superseded
// catalogue is still the best answer there is until the next one arrives.
func (c *Catalog) cached() (Snapshot, bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.loaded {
		var stored Snapshot
		switch err := c.store.GetJSON(CacheKey, &stored); {
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
		return Snapshot{}, false, false
	}
	return c.withSettings(c.snapshot), true, c.superseded(time.Now())
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
	c.folders = nil
	c.mu.Unlock()
	if err := c.store.SetJSON(CacheKey, Snapshot{}); err != nil {
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
			if err := c.store.SetJSON(CacheKey, snap); err != nil {
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
	c.mu.Lock()
	accounts := c.accounts
	c.mu.Unlock()
	snap := Snapshot{RefreshedAt: time.Now().UnixMilli(), Schema: cacheSchema}
	// The fingerprint is taken before the probes, so a sign-in while they
	// run is a change the next look notices.
	if accounts != nil {
		snap.Accounts = accounts.Fingerprint()
	}
	for _, h := range harnesses.Registry() {
		entry, known := settings.Harnesses.Entry(string(h.Kind()))
		if !known {
			continue
		}
		var envs [][]string
		if _, ok := h.(harnesses.AccountDiscoverer); ok && accounts != nil {
			envs = accounts.Envs(string(h.Kind()))
		}
		snap.Agents = append(snap.Agents, discoverOne(ctx, h, entry, envs))
	}
	return snap
}

// withSettings overlays the parts of an entry that are a setting rather than a
// discovery, so switching an agent off or changing the favourites in the
// dashboard takes effect without spawning anything.
func (c *Catalog) withSettings(snap Snapshot) Snapshot {
	settings := c.settings()
	out := Snapshot{RefreshedAt: snap.RefreshedAt, Schema: snap.Schema, Accounts: snap.Accounts, Agents: make([]Agent, 0, len(snap.Agents))}
	for _, a := range snap.Agents {
		if a.Models == nil {
			a.Models = []harnesses.Model{}
		}
		if a.Recent == nil {
			a.Recent = []string{}
		}
		if a.Favorites == nil {
			a.Favorites = []string{}
		}
		a.Picks = []harnesses.Model{}
		if entry, ok := settings.Harnesses.Entry(a.ID); ok {
			a.Enabled = entry.Enabled
			a.Picks = Picks(a, entry.Models)
		}
		out.Agents = append(out.Agents, a)
	}
	return out
}

// Picks fills the dashboard's favourites in from what the agent reported: a
// known id keeps its label, hint and effort levels, and an id the agent has
// not reported - typed in, or newer than the discovery - is offered as it was
// typed, with every effort level when the agent has an effort mechanism at
// all, because there is nothing to narrow it down with. The first entry is
// the default, so the sheet starts on it (harnesses.js defaultModelFor: the
// first one the agent reported, when it reported any).
func Picks(a Agent, picks []config.ModelPick) []harnesses.Model {
	out := make([]harnesses.Model, 0, len(picks))
	for i, p := range picks {
		m, known := a.Model(p.ID)
		if !known {
			m = harnesses.Model{ID: p.ID, Label: p.ID, Hint: "typed in the dashboard", Efforts: a.AllEfforts()}
		}
		m.DefaultEffort = p.Effort
		m.Default = i == 0
		out = append(out, m)
	}
	return out
}

// discoverOne asks one harness where it is, what version it is and what it can
// run - as each of the accounts in envs, for a harness whose models depend on
// the account.
func discoverOne(ctx context.Context, h harnesses.Harness, entry config.Common, envs [][]string) Agent {
	id := string(h.Kind())
	a := Agent{
		ID: id, Label: h.Label(), Enabled: entry.Enabled,
		Notes:  notes[id],
		Models: []harnesses.Model{},
	}
	bin := strings.TrimSpace(entry.Binary)
	if bin == "" {
		bin = h.DefaultBinary()
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		// A shell is never missing: the fallback chain ends at /bin/sh, and a
		// machine without one has bigger problems than this catalogue.
		if id == config.HarnessShell {
			a.Installed, a.Path = true, bin
			return a
		}
		a.Error = bin + " is not on this machine (nothing named that on PATH)"
		return a
	}
	a.Installed = true
	a.Path = path
	a.Version = probeVersion(ctx, path, h.VersionArgs())

	var cat harnesses.Catalog
	if ad, ok := h.(harnesses.AccountDiscoverer); ok && len(envs) > 0 {
		cat, err = ad.DiscoverModelsIn(ctx, path, envs)
	} else {
		cat, err = h.DiscoverModels(ctx, path)
	}
	switch {
	case errors.Is(err, harnesses.ErrNoModels):
		return a
	case err != nil:
		// Installed, but its model list could not be read. The picker still
		// offers it, and a typed id is accepted rather than refused.
		a.Error = err.Error()
		return a
	}
	applyCatalog(&a, cat)
	return a
}

// applyCatalog puts what an adapter reported into the agent's entry.
func applyCatalog(a *Agent, cat harnesses.Catalog) {
	a.Static = cat.Static
	if cat.Notes != "" {
		a.Notes = cat.Notes
	}
	if cat.Models != nil {
		a.Models = cat.Models
	}
	a.Recent, a.Favorites = cat.Recent, cat.Favorites
	a.Folder = cat.Folder
	// has_effort means "at least one of this agent's models offers one". It
	// exists only so the picker can hide the effort control before a model has
	// been chosen; once one is, that model's own list decides.
	a.HasEffort = false
	for i := range a.Models {
		a.Models[i].Efforts = harnesses.OrderEfforts(a.Models[i].Efforts)
		if len(a.Models[i].Efforts) > 0 {
			a.HasEffort = true
		}
		if a.Models[i].Default && a.DefaultModel == "" {
			a.DefaultModel = a.Models[i].ID
		}
	}
}

// refreshStatic replaces a cached curated list with this build's. A static
// model list is read out of the binary rather than discovered, so it costs
// nothing to rebuild on every load - and the cached copy would otherwise be
// the previous build's list for as long as the cache is fresh.
func refreshStatic(a *Agent) {
	if !a.Static || !a.Installed {
		return
	}
	h, ok := harnesses.Get(a.ID)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cat, err := h.DiscoverModels(ctx, a.Path)
	if err != nil || !cat.Static {
		return
	}
	a.DefaultModel = ""
	applyCatalog(a, cat)
}

// probeVersion runs the binary's version command and keeps the first readable
// line of it. A failure is not an error worth reporting on its own - the model
// list is the stronger signal that a CLI is usable - but neither is it a
// version: dash answers `--version` with `sh: 0: Illegal option --` and a exit
// status, and the dashboard would show that sentence as the shell's version.
// So a command that failed has no version at all.
func probeVersion(ctx context.Context, path string, args []string) string {
	if len(args) == 0 {
		args = []string{"--version"}
	}
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, args...).CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
}
