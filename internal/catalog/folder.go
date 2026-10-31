package catalog

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
)

// maxFolders bounds the folder catalogues that are remembered. A person
// works in a handful of configured folders.
const maxFolders = 64

// folderDiscoveries is how many folder discoveries run at once: each is a
// few runs of the agent's CLI, and a path typed letter by letter, or many
// chats opened, must not start a crowd of them.
const folderDiscoveries = 2

// folderEntry is one agent's catalogue under one set of configuration
// files: what it was discovered from, and the answer once the discovery is
// over.
type folderEntry struct {
	key  string
	done chan struct{}
	// at is when the answer came, agent the answer, and ok says it is one:
	// a discovery that failed leaves the folder on the general catalogue,
	// and is not remembered.
	at    time.Time
	agent Agent
	ok    bool
}

// Folder is the agents whose catalogue is another in dir than everywhere -
// OpenCode in a folder with an opencode.json of its own, which can add
// providers, name the model it starts on and put its subagents on models of
// their own. An agent dir does not change is not in the answer: the general
// catalogue is its answer there.
//
// A folder's catalogue is discovered behind the request, as the general one
// is, and kept for every folder with the same configuration files (the
// subfolders of a project, a dynamic chat's new folder) until those files,
// the agent or the general catalogue change, or for TTL. A request that gives
// up leaves the discovery running for the next one; one that failed is
// tried again by the next request.
func (c *Catalog) Folder(ctx context.Context, dir string) []Agent {
	dir = filepath.Clean(strings.TrimSpace(dir))
	if !filepath.IsAbs(dir) {
		return nil
	}
	snap := c.Get(ctx)
	var out []Agent
	for _, a := range snap.Agents {
		if found, ok := c.folderAgent(ctx, a, snap, dir); ok {
			out = append(out, found)
		}
	}
	return out
}

// FolderAgent is one agent's catalogue in dir, waiting for its discovery as
// long as ctx lets it. ok is false for a folder that is like any other, and
// when no answer came.
func (c *Catalog) FolderAgent(ctx context.Context, agentID, dir string) (Agent, bool) {
	dir = filepath.Clean(strings.TrimSpace(dir))
	if !filepath.IsAbs(dir) {
		return Agent{}, false
	}
	snap := c.Get(ctx)
	a, ok := snap.Agent(agentID)
	if !ok {
		return Agent{}, false
	}
	return c.folderAgent(ctx, a, snap, dir)
}

// FolderCached is one agent's catalogue in dir as far as it is known
// without running anything: the answer of a discovery that ran for the
// folder's configuration as it is now.
func (c *Catalog) FolderCached(agentID, dir string) (Agent, bool) {
	dir = filepath.Clean(strings.TrimSpace(dir))
	snap, ok := c.Cached()
	if !ok || !filepath.IsAbs(dir) {
		return Agent{}, false
	}
	a, ok := snap.Agent(agentID)
	if !ok {
		return Agent{}, false
	}
	entry, ok := c.folderEntry(a, snap, dir, false)
	if !ok {
		return Agent{}, false
	}
	return c.answer(entry, nil)
}

// FolderFiles is the folder's own configuration of an agent, found without
// running anything; none means the agent is in dir what it is everywhere.
func FolderFiles(agentID, dir string) []string {
	h, ok := harnesses.Get(agentID)
	if !ok {
		return nil
	}
	fd, ok := h.(harnesses.FolderDiscoverer)
	if !ok {
		return nil
	}
	return fd.FolderFiles(dir)
}

func (c *Catalog) folderAgent(ctx context.Context, a Agent, snap Snapshot, dir string) (Agent, bool) {
	entry, ok := c.folderEntry(a, snap, dir, true)
	if !ok {
		return Agent{}, false
	}
	return c.answer(entry, ctx.Done())
}

// answer is an entry's agent with the dashboard's settings on it, once its
// discovery is over; with a nil wait, only if it already is.
func (c *Catalog) answer(entry *folderEntry, wait <-chan struct{}) (Agent, bool) {
	select {
	case <-entry.done:
	case <-wait:
		return Agent{}, false
	}
	if !entry.ok {
		return Agent{}, false
	}
	return c.withSettings(Snapshot{Agents: []Agent{entry.agent}}).Agents[0], true
}

// folderEntry is the entry of a's catalogue for dir's configuration,
// starting its discovery when start says so and there is no current one. ok
// is false for an agent that dir does not change.
func (c *Catalog) folderEntry(a Agent, snap Snapshot, dir string, start bool) (*folderEntry, bool) {
	h, ok := harnesses.Get(a.ID)
	if !ok || !a.Installed || a.Path == "" {
		return nil, false
	}
	fd, ok := h.(harnesses.FolderDiscoverer)
	if !ok {
		return nil, false
	}
	files := fd.FolderFiles(dir)
	if len(files) == 0 {
		return nil, false
	}
	// Folders with the same configuration files have the same catalogue.
	id := a.ID + "\x00" + strings.Join(files, "\x00")
	key := folderKey(files, a, snap)

	c.mu.Lock()
	defer c.mu.Unlock()
	old := c.folders[id]
	if old != nil && old.key == key {
		select {
		case <-old.done:
			// An answer past its time is still the answer while the next
			// one is found.
			if start && time.Since(old.at) > TTL {
				old.at = time.Now()
				c.startFolderLocked(id, key, fd, a, dir, true)
			}
		default:
		}
		return old, true
	}
	if !start {
		return nil, false
	}
	return c.startFolderLocked(id, key, fd, a, dir, false), true
}

// startFolderLocked discovers a's catalogue in dir on a context of its own.
// A renewal leaves the answer there is in place until the new one is there,
// and in place if the new one fails. Called with c.mu held.
func (c *Catalog) startFolderLocked(id, key string, fd harnesses.FolderDiscoverer, a Agent, dir string, renewal bool) *folderEntry {
	entry := &folderEntry{key: key, done: make(chan struct{})}
	if c.folders == nil {
		c.folders = map[string]*folderEntry{}
	}
	if c.folderSlots == nil {
		c.folderSlots = make(chan struct{}, folderDiscoveries)
	}
	if !renewal {
		c.folders[id] = entry
		c.trimFoldersLocked()
	}
	accounts, slots := c.accounts, c.folderSlots
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), DiscoveryBudget)
		defer cancel()
		var (
			cat harnesses.Catalog
			err error
		)
		select {
		case slots <- struct{}{}:
			// The accounts' environments are made here, not under the lock:
			// they read the database and open keys.
			var envs [][]string
			if accounts != nil {
				envs = accounts.Envs(a.ID)
			}
			cat, err = fd.DiscoverModelsInFolder(ctx, a.Path, dir, envs)
			<-slots
		case <-ctx.Done():
			err = ctx.Err()
		}
		folder := a
		folder.Picks = nil
		if err == nil {
			folder.Models, folder.DefaultModel = []harnesses.Model{}, ""
			applyCatalog(&folder, cat)
		} else {
			log.Printf("catalog: the models of %s in %s: %v", a.ID, dir, err)
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		switch {
		case renewal && (err != nil || c.folders[id] == nil || c.folders[id].key != key):
			// A failed renewal keeps the answer there is; a configuration that
			// changed meanwhile has a newer entry of its own.
			return
		case renewal:
			c.folders[id] = entry
		case err != nil && c.folders[id] == entry:
			// A failure is not an answer: the next request tries again.
			delete(c.folders, id)
		}
		entry.agent, entry.ok, entry.at = folder, err == nil, time.Now()
		close(entry.done)
	}()
	return entry
}

// trimFoldersLocked forgets the oldest answers beyond maxFolders. A
// discovery still running is kept. Called with c.mu held.
func (c *Catalog) trimFoldersLocked() {
	for len(c.folders) > maxFolders {
		oldest, at := "", time.Time{}
		for id, e := range c.folders {
			select {
			case <-e.done:
			default:
				continue
			}
			if oldest == "" || e.at.Before(at) {
				oldest, at = id, e.at
			}
		}
		if oldest == "" {
			return
		}
		delete(c.folders, oldest)
	}
}

// folderKey is what a folder's catalogue was discovered from: the
// configuration files as they are on disk, the agent's binary, and the
// general catalogue it was discovered beside, which an update, a sign-in or
// a Refresh renew.
func folderKey(files []string, a Agent, snap Snapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%s|%d|%s|", a.Path, a.Version, snap.RefreshedAt, snap.Accounts)
	for _, f := range files {
		if fi, err := os.Stat(f); err == nil {
			fmt.Fprintf(&b, "%s:%d:%d;", f, fi.ModTime().UnixNano(), fi.Size())
		}
	}
	return b.String()
}
