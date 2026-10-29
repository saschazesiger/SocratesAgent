package server

// The cost ledger and the page that reads it (/costs).
//
// Every conversation any CLI had on this machine - under every profile, from
// a session that still exists or one deleted long ago - is read from the
// CLI's own files by internal/costs and kept in the store's ledger, hour by
// hour and model by model. The ledger is mirrored to the bucket like the
// sessions are, so a restore brings every cost back even though the
// transcripts stay behind on the old disk.
//
//	GET /api/costs   the whole ledger, for the page to sum up in its own time zone

import (
	"context"
	"log"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/costs"
	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
	"github.com/saschazesiger/SocratesAgent/internal/paths"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

const (
	// costsEvery is how often the transcripts are looked at. A pass over
	// files that did not change is a walk of their sizes and times.
	costsEvery = 2 * time.Minute
	// costsFirstAfter keeps the first pass, which on a new installation reads
	// every transcript there is, out of the way of the start.
	costsFirstAfter = 20 * time.Second
	// costsFresh is how old a pass may be before the page asks for another.
	costsFresh = 30 * time.Second
)

// costLedger keeps the store's cost ledger current.
type costLedger struct {
	s       *Server
	scanner *costs.Scanner
	poke    chan struct{}

	mu       sync.Mutex // one pass at a time
	running  atomic.Bool
	lastPass atomic.Int64 // Unix milliseconds of the last finished pass
}

func newCostLedger(s *Server) *costLedger {
	return &costLedger{s: s, scanner: costs.NewScanner(), poke: make(chan struct{}, 1)}
}

func (c *costLedger) start(ctx context.Context) {
	go func() {
		wait := time.NewTimer(costsFirstAfter)
		defer wait.Stop()
		select {
		case <-ctx.Done():
			return
		case <-wait.C:
		case <-c.poke:
		}
		ticker := time.NewTicker(costsEvery)
		defer ticker.Stop()
		for {
			c.pass()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case <-c.poke:
			}
		}
	}()
}

// refresh asks for a pass soon, without waiting for it.
func (c *costLedger) refresh() {
	select {
	case c.poke <- struct{}{}:
	default:
	}
}

// roots are the directories of every account on this machine: the agents'
// own folders in HOME, every profile directory on the disk - the profiles
// before accounts left theirs under <data>/profiles, and a profile that was
// deleted still has the conversations it had - and the agents' shared
// folders, where every account's transcripts are kept once accounts share
// them (REBUILD.md §6.1). A conversation found under two of them is one.
func (c *costLedger) roots() []costs.Root {
	dataDir := c.s.dataDir
	layout := paths.New(dataDir)
	roots := []costs.Root{{
		ClaudeDir:   harnesses.ClaudeConfigDir(),
		CodexHome:   harnesses.CodexHome(),
		OpenCodeDir: harnesses.OpenCodeDataDir(),
	}, {
		ClaudeDir:   layout.Agent("claude"),
		CodexHome:   layout.Agent("codex"),
		OpenCodeDir: layout.Agent("opencode"),
	}}
	dirs, _ := filepath.Glob(filepath.Join(dataDir, "profiles", "*"))
	sort.Strings(dirs)
	for _, dir := range dirs {
		// A profile's folders, as the build before accounts laid them out:
		// OpenCode's under the XDG data home it was started with.
		roots = append(roots, costs.Root{
			Profile:     filepath.Base(dir),
			ClaudeDir:   filepath.Join(dir, "claude"),
			CodexHome:   filepath.Join(dir, "codex"),
			OpenCodeDir: filepath.Join(dir, "xdg-data", "opencode"),
		})
	}
	return roots
}

// pass reads what changed in the transcripts into the ledger, and brings the
// name and folder of every conversation a session ran in up to date.
func (c *costLedger) pass() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.running.Store(true)
	defer c.running.Store(false)
	started := time.Now()

	st := c.s.store
	seen, err := st.CostFingerprints()
	if err != nil {
		log.Printf("costs: %v", err)
		return
	}
	bySession := map[string]*store.Session{}
	// Every session, the test runs' too: a test session's conversations are
	// its own, not strays.
	rows, err := st.ListAllSessions()
	if err != nil {
		log.Printf("costs: %v", err)
		return
	}
	for i := range rows {
		if rows[i].CLISessionID != "" && !rows[i].IsChat() {
			bySession[store.CostKey(rows[i].Harness, rows[i].CLISessionID)] = &rows[i]
		}
	}

	// The chats first: their journals are what they cost, and what they own
	// is no transcript's to count (chatcosts.go).
	owned, fresh := c.chatLedgers(rows, seen)
	for key := range fresh {
		if _, err := st.DeleteCostLedger(key); err != nil {
			log.Printf("costs: %s: %v", key, err)
		}
	}
	changed := 0
	for _, conv := range c.scanner.Scan(c.roots(), seen) {
		if owned[conv.Key()] {
			if err := st.SetCostFingerprint(conv.Key(), conv.Fingerprint); err != nil {
				log.Printf("costs: %s: %v", conv.Key(), err)
			}
			continue
		}
		ledger := store.CostLedger{
			CostConversation: store.CostConversation{
				Harness: conv.Harness, ConvID: conv.ID, Profile: conv.Profile,
				Workdir: conv.Cwd, Title: conv.Title, FirstAt: conv.FirstAt, LastAt: conv.LastAt,
			},
			Rows: conv.Rows,
		}
		// What the ledger already knows about the conversation outlives the
		// session it ran in: a name and a folder are kept once the session
		// that gave them is gone.
		if before, err := st.GetCostLedger(conv.Key()); err == nil {
			ledger.SessionID = before.SessionID
			if before.Title != "" {
				ledger.Title = before.Title
			}
			if before.Workdir != "" {
				ledger.Workdir = before.Workdir
			}
		}
		if row := bySession[conv.Key()]; row != nil {
			ledger.SessionID, ledger.Title, ledger.Workdir = row.ID, row.Title, row.Workdir
		}
		did, err := st.PutCostLedger(ledger)
		if err != nil {
			log.Printf("costs: %s: %v", conv.Key(), err)
			continue
		}
		if did {
			changed++
		}
		if err := st.SetCostFingerprint(conv.Key(), conv.Fingerprint); err != nil {
			log.Printf("costs: %s: %v", conv.Key(), err)
		}
	}
	for key, row := range bySession {
		if _, err := st.SetCostMeta(key, row.Workdir, row.Title, row.ID); err != nil {
			log.Printf("costs: %s: %v", key, err)
		}
	}
	c.lastPass.Store(time.Now().UnixMilli())
	if took := time.Since(started); changed > 0 || took > 10*time.Second {
		log.Printf("costs: %d conversations updated in %s", changed, took.Round(time.Millisecond))
	}
}

// costsView is the whole ledger in a compact form: a conversation and a
// model are named once and referred to by their index in every row.
type costsView struct {
	Scanning  bool              `json:"scanning"`
	ScannedAt int64             `json:"scanned_at"`
	Profiles  map[string]string `json:"profiles"`
	Convs     []costConvView    `json:"conversations"`
	Models    []string          `json:"models"`
	// Rows are [conversation, hour, model, cost, input, output, cache read,
	// cache write, requests, unpriced requests].
	Rows [][10]float64 `json:"rows"`
}

type costConvView struct {
	Key       string `json:"key"`
	Harness   string `json:"harness"`
	Profile   string `json:"profile"`
	Workdir   string `json:"workdir"`
	Project   string `json:"project"`
	Title     string `json:"title"`
	SessionID string `json:"session_id,omitempty"`
	// Live is whether the session the conversation ran in still exists.
	Live    bool  `json:"live,omitempty"`
	FirstAt int64 `json:"first_at"`
	LastAt  int64 `json:"last_at"`
}

// legacyProfilesKey is where the build before accounts kept its sign-in
// profiles, and defaultProfileName what it called the one with no id: the
// agents' own folders in HOME.
const (
	legacyProfilesKey  = "profiles"
	defaultProfileName = "Default"
)

// moreProject is the project of every conversation in a directory Socrates
// made for one session, the sidebar's "More Chats" (folders.js).
const moreProject = "#more"

func (s *Server) handleCosts(w http.ResponseWriter, r *http.Request) {
	if time.Now().UnixMilli()-s.costs.lastPass.Load() > costsFresh.Milliseconds() {
		s.costs.refresh()
	}
	ledgers, err := s.store.ListCostLedgers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sessions, err := s.store.ListAllSessions()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	live := map[string]bool{}
	for _, row := range sessions {
		live[row.ID] = true
	}

	view := costsView{
		Scanning:  s.costs.running.Load() || s.costs.lastPass.Load() == 0,
		ScannedAt: s.costs.lastPass.Load(),
		Profiles:  map[string]string{"": defaultProfileName},
		Convs:     []costConvView{},
		Models:    []string{},
		Rows:      [][10]float64{},
	}
	// A conversation from before accounts names the profile it ran in, and
	// the profiles' names are still in the store, where the accounts
	// migration read them from.
	var profiles []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := s.store.GetJSON(legacyProfilesKey, &profiles); err == nil {
		for _, p := range profiles {
			view.Profiles[p.ID] = p.Name
		}
	}
	// A chat's ledgers are kept per account, the account's id as the
	// profile (chatcosts.go).
	if list, err := s.store.ListAccounts(); err == nil {
		for _, a := range list {
			view.Profiles[a.ID] = a.Label
		}
	}

	root := filepath.Clean(s.Settings().Workspace.Root)
	models := map[string]int{}
	for i, l := range ledgers {
		project := l.Workdir
		if l.Workdir != "" && root != "." && root != "/" && strings.HasPrefix(filepath.Clean(l.Workdir)+"/", root+"/") &&
			filepath.Clean(l.Workdir) != root {
			project = moreProject
		}
		view.Convs = append(view.Convs, costConvView{
			Key: l.Key(), Harness: l.Harness, Profile: l.Profile, Workdir: l.Workdir, Project: project,
			Title: l.Title, SessionID: l.SessionID, Live: l.SessionID != "" && live[l.SessionID],
			FirstAt: l.FirstAt, LastAt: l.LastAt,
		})
		for _, row := range l.Rows {
			m, ok := models[row.Model]
			if !ok {
				m = len(view.Models)
				models[row.Model] = m
				view.Models = append(view.Models, row.Model)
			}
			view.Rows = append(view.Rows, [10]float64{
				float64(i), float64(row.Hour), float64(m), row.CostUSD,
				float64(row.Input), float64(row.Output), float64(row.CacheRead), float64(row.CacheWrite),
				float64(row.Requests), float64(row.Unpriced),
			})
		}
	}
	writeJSONCompressed(w, r, http.StatusOK, view)
}
