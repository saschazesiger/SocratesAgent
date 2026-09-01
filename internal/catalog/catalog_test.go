package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"

	_ "github.com/saschazesiger/SocratesAgent/internal/harness/claude"
	_ "github.com/saschazesiger/SocratesAgent/internal/harness/codex"
	_ "github.com/saschazesiger/SocratesAgent/internal/harness/opencode"
)

// memStore is the key/value half of the store, which is all this package uses.
type memStore struct{ values map[string]string }

func newStore() *memStore { return &memStore{values: map[string]string{}} }

func (m *memStore) GetJSON(key string, out any) error {
	raw, ok := m.values[key]
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
	m.values[key] = string(raw)
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
