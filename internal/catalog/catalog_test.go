package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"

	_ "github.com/saschazesiger/SocratesAgent/internal/harness/claude"
	_ "github.com/saschazesiger/SocratesAgent/internal/harness/codex"
	_ "github.com/saschazesiger/SocratesAgent/internal/harness/opencode"
)

// memStore is the key/value half of the store, which is all this package uses.
// It is locked because the real one is: discovery runs on a goroutine of its
// own and writes the cache from there.
type memStore struct {
	mu     sync.Mutex
	values map[string]string
}

func newStore() *memStore { return &memStore{values: map[string]string{}} }

func (m *memStore) GetJSON(key string, out any) error {
	m.mu.Lock()
	raw, ok := m.values[key]
	m.mu.Unlock()
	if !ok {
		return errors.New("not found")
	}
	return json.Unmarshal([]byte(raw), out)
}

func (m *memStore) SetJSON(key string, in any) error {
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.values[key] = string(raw)
	m.mu.Unlock()
	return nil
}

// onlyClaude puts a fake `claude` on PATH and nothing else, so no test in this
// package ever spawns a real coding agent: the two probes that would - codex
// and opencode - have nothing to find.
func onlyClaude(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\necho 'claude 9.9.9-test'\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func settingsFn(s config.Settings) func() config.Settings {
	return func() config.Settings { return s }
}

func TestDiscoveryReportsWhatIsInstalledAndWhatIsNot(t *testing.T) {
	onlyClaude(t)
	c := New(newStore(), settingsFn(config.Default()))
	snap := c.Refresh(context.Background())

	if len(snap.Agents) != 3 {
		t.Fatalf("expected one entry per agent, got %d", len(snap.Agents))
	}
	claude, ok := snap.Agent("claude")
	if !ok {
		t.Fatal("no claude entry")
	}
	if !claude.Installed || claude.Path == "" {
		t.Fatalf("claude was not found: %#v", claude)
	}
	if claude.Version != "claude 9.9.9-test" {
		t.Errorf("version = %q", claude.Version)
	}
	// The curated list ships with WP1 so the picker works from day one, and it
	// says out loud that it is curated rather than discovered.
	if !claude.Static {
		t.Error("the curated list is not marked static")
	}
	if len(claude.Models) != 4 || claude.DefaultModel != "sonnet" {
		t.Fatalf("models = %#v (default %q)", claude.Models, claude.DefaultModel)
	}
	if !claude.HasEffort {
		t.Error("claude reports no effort at all")
	}
	if claude.Error != "" {
		t.Errorf("an installed agent reported an error: %q", claude.Error)
	}

	// An agent that is not on this machine says so in one sentence and offers
	// nothing, rather than arriving half filled in.
	codex, _ := snap.Agent("codex")
	if codex.Installed {
		t.Fatal("codex was found on a PATH that has only claude on it")
	}
	if codex.Error == "" {
		t.Error("a missing agent did not say why")
	}
	if codex.Models == nil {
		t.Error("models must always be present, even empty")
	}
}

// Every effort a picker can offer has to be one the CLI understands, so the
// list is filtered on the way out rather than trusted.
func TestEffortsAreFilteredToTheIntersection(t *testing.T) {
	onlyClaude(t)
	c := New(newStore(), settingsFn(config.Default()))
	snap := c.Refresh(context.Background())
	claude, _ := snap.Agent("claude")
	for _, m := range claude.Models {
		for _, e := range m.Efforts {
			switch e {
			case config.EffortLow, config.EffortMedium, config.EffortHigh:
			default:
				t.Fatalf("model %s offers effort %q, which is not one of the three", m.ID, e)
			}
		}
	}
}

// A restart must not cost three subprocess spawns, so the last discovery is
// kept in the key/value store and read back.
func TestTheCatalogueSurvivesARestart(t *testing.T) {
	onlyClaude(t)
	st := newStore()
	first := New(st, settingsFn(config.Default()))
	before := first.Refresh(context.Background())

	// A second Catalog over the same store, as a restart would build it. Its
	// PATH now has nothing on it at all: if it discovered again, claude would
	// come back as missing.
	t.Setenv("PATH", t.TempDir())
	second := New(st, settingsFn(config.Default()))
	cached, ok := second.Cached()
	if !ok {
		t.Fatal("nothing was cached")
	}
	claude, _ := cached.Agent("claude")
	if !claude.Installed {
		t.Fatal("the cached catalogue was not used")
	}
	if cached.RefreshedAt != before.RefreshedAt {
		t.Errorf("refreshed_at changed on a read: %d vs %d", cached.RefreshedAt, before.RefreshedAt)
	}
}

// Nothing cached is an answer chat creation accepts: a chat queued offline
// must never be failed permanently by a cache miss.
func TestNothingIsCachedOnAFreshInstall(t *testing.T) {
	c := New(newStore(), settingsFn(config.Default()))
	if _, ok := c.Cached(); ok {
		t.Fatal("a fresh install reported a cached catalogue")
	}
}

// Switching an agent off is a setting, not a discovery, so it takes effect
// without spawning anything.
func TestTheEnabledSwitchIsReadFromSettingsNotFromTheCache(t *testing.T) {
	onlyClaude(t)
	st := newStore()
	settings := config.Default()
	c := New(st, func() config.Settings { return settings })
	if snap := c.Refresh(context.Background()); !mustAgent(t, snap, "claude").Enabled {
		t.Fatal("claude is off by default")
	}
	settings.Agents.Claude.Enabled = false
	cached, ok := c.Cached()
	if !ok {
		t.Fatal("nothing cached")
	}
	if mustAgent(t, cached, "claude").Enabled {
		t.Fatal("the switch did not reach the cached snapshot")
	}
}

// A stale cache is answered from at once and renewed behind the request: a
// picker that waits twenty seconds for a model list is one nobody opens twice.
func TestAStaleCacheIsStillAnsweredImmediately(t *testing.T) {
	onlyClaude(t)
	st := newStore()
	c := New(st, settingsFn(config.Default()))
	c.Refresh(context.Background())

	c.mu.Lock()
	c.snapshot.RefreshedAt = time.Now().Add(-2 * TTL).UnixMilli()
	c.mu.Unlock()

	done := make(chan Snapshot, 1)
	go func() { done <- c.Get(context.Background()) }()
	select {
	case snap := <-done:
		if len(snap.Agents) != 3 {
			t.Fatalf("the stale answer was incomplete: %#v", snap)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Get blocked on a background refresh")
	}
}

// Model lookup is what POST /api/chats validates against.
func TestModelLookup(t *testing.T) {
	onlyClaude(t)
	c := New(newStore(), settingsFn(config.Default()))
	claude := mustAgent(t, c.Refresh(context.Background()), "claude")
	if m, ok := claude.Model("sonnet"); !ok || m.Label != "Sonnet" {
		t.Fatalf("sonnet = %#v %v", m, ok)
	}
	if _, ok := claude.Model("not-a-model"); ok {
		t.Fatal("a model nobody ships was found")
	}
}

func mustAgent(t *testing.T, snap Snapshot, id string) Agent {
	t.Helper()
	a, ok := snap.Agent(id)
	if !ok {
		t.Fatalf("no %s in the catalogue", id)
	}
	return a
}

// slowClaude puts a claude on PATH that takes its time answering, which is
// what a cold codex or opencode probe does on a real machine.
func slowClaude(t *testing.T, delay time.Duration) {
	t.Helper()
	dir := t.TempDir()
	// /bin/sleep by its full path: PATH is about to hold nothing but this
	// directory, so a bare `sleep` would not be found and the script would
	// return instantly - which is the opposite of the point.
	script := fmt.Sprintf("#!/bin/sh\n/bin/sleep %.2f\necho 'claude 9.9.9-slow'\n", delay.Seconds())
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

// The browser gives up on the agent list after twelve seconds, and a cold
// codex plus opencode probe can take longer than that. A discovery that rode
// the request's context was cancelled halfway when it did - and the half-made
// answer, every agent carrying "context canceled", was cached for the next
// half hour, so the first run of a fresh installation offered a picker with no
// models in it at all.
//
// The request may give up. The discovery may not.
func TestARequestThatGivesUpDoesNotPoisonTheCatalogue(t *testing.T) {
	slowClaude(t, 2*time.Second)
	c := New(newStore(), settingsFn(config.Default()))

	impatient, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	started := time.Now()
	first := c.Get(impatient)
	if waited := time.Since(started); waited > time.Second {
		t.Fatalf("the request waited %s for a discovery it does not own", waited)
	}
	// It has nothing to say, which is honest: nothing is known yet.
	if len(first.Agents) != 0 {
		t.Fatalf("a request that gave up returned %d agents", len(first.Agents))
	}
	// And nothing half-made was written down.
	if snap, ok := c.Cached(); ok {
		for _, a := range snap.Agents {
			if strings.Contains(a.Error, "context canceled") {
				t.Fatalf("a cancelled discovery was cached: %#v", a)
			}
		}
	}

	// The next request - a reload a moment later - gets the whole thing.
	second := c.Get(context.Background())
	claude := mustAgent(t, second, "claude")
	if !claude.Installed || claude.Version != "claude 9.9.9-slow" {
		t.Fatalf("the second request did not get a finished discovery: %#v", claude)
	}
	if len(claude.Models) != 4 {
		t.Fatalf("models = %#v", claude.Models)
	}
	if claude.Error != "" {
		t.Fatalf("the finished discovery carries an error: %q", claude.Error)
	}

	// And it is the cache from here on, not another round of probes.
	cached, ok := c.Cached()
	if !ok {
		t.Fatal("the finished discovery was not cached")
	}
	if got := mustAgent(t, cached, "claude"); !got.Installed {
		t.Fatalf("the cached entry is not the finished one: %#v", got)
	}
}

// The same for the dashboard's Refresh button: the person may close the tab,
// and the discovery still finishes and still replaces the cache.
func TestARefreshThatIsAbandonedStillFinishes(t *testing.T) {
	slowClaude(t, 2*time.Second)
	c := New(newStore(), settingsFn(config.Default()))
	if a := mustAgent(t, c.Get(context.Background()), "claude"); !a.Installed {
		t.Fatalf("the first discovery did not work: %#v", a)
	}

	impatient, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if got := c.Refresh(impatient); len(got.Agents) != 0 {
		t.Fatalf("an abandoned refresh returned %d agents", len(got.Agents))
	}
	// Whatever the cache says now, it is never a cancelled discovery.
	if snap, ok := c.Cached(); ok {
		for _, a := range snap.Agents {
			if strings.Contains(a.Error, "context canceled") {
				t.Fatalf("an abandoned refresh was cached: %#v", a)
			}
		}
	}
	if a := mustAgent(t, c.Get(context.Background()), "claude"); !a.Installed || len(a.Models) != 4 {
		t.Fatalf("the refresh did not finish behind the request: %#v", a)
	}
}
