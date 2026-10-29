package testmode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/agent/testtools"
	"github.com/saschazesiger/SocratesAgent/internal/testmode/catalog"
)

// ErrNotFound is a repository, run or attempt the runner does not know.
var ErrNotFound = errors.New("not found")

// ------------------------------------------------------------ repositories

// FindRoot is the repository a folder belongs to: the nearest folder at or
// above it with a .git, else the folder itself.
func FindRoot(dir string) string {
	dir = filepath.Clean(dir)
	for d := dir; ; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return dir
		}
		d = parent
	}
}

// HasCatalog is whether a repository has a test catalog.
func HasCatalog(root string) bool {
	fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(catalog.DirName), catalog.TestsFile))
	return err == nil && fi.Mode().IsRegular()
}

// Discover registers the repositories with a catalog among the folders
// (Admin's presets, the chats' folders), unless they were removed by hand.
func (r *Runner) Discover(folders []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	changed := false
	seen := map[string]bool{}
	for _, f := range folders {
		if strings.TrimSpace(f) == "" {
			continue
		}
		root := FindRoot(f)
		if seen[root] {
			continue
		}
		seen[root] = true
		key := RepoKey(root)
		if _, ok := r.repos[key]; ok || !HasCatalog(root) {
			continue
		}
		r.repos[key] = &Repo{Key: key, Path: root}
		changed = true
	}
	if changed {
		r.saveReposLocked()
	}
}

// AddRepo registers a repository by its path.
func (r *Runner) AddRepo(path string) (*Repo, error) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return nil, errors.New("the path must be absolute")
	}
	root := FindRoot(path)
	if !HasCatalog(root) {
		return nil, fmt.Errorf("%s has no %s/%s", root, catalog.DirName, catalog.TestsFile)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := RepoKey(root)
	repo := &Repo{Key: key, Path: root, Added: true}
	if old := r.repos[key]; old != nil {
		old.Hidden = false
		repo = old
	}
	r.repos[key] = repo
	r.saveReposLocked()
	return repo, nil
}

// RemoveRepo takes a repository off the list; its runs stay on disk until
// it is added again or they are deleted.
func (r *Runner) RemoveRepo(key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	repo := r.repos[key]
	if repo == nil {
		return ErrNotFound
	}
	if r.activeRunLocked(key, "") != nil {
		return ErrBusy
	}
	repo.Hidden = true
	r.saveReposLocked()
	return nil
}

func (r *Runner) saveReposLocked() {
	list := make([]*Repo, 0, len(r.repos))
	for _, repo := range r.repos {
		list = append(list, repo)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Key < list[j].Key })
	raw, _ := json.Marshal(list)
	if err := r.host.SetKV(kvRepos, string(raw)); err != nil {
		log.Printf("tests: %v", err)
	}
}

// Repo is a repository by key, nil for none.
func (r *Runner) Repo(key string) *Repo {
	r.mu.Lock()
	defer r.mu.Unlock()
	if repo := r.repos[key]; repo != nil && !repo.Hidden {
		c := *repo
		return &c
	}
	return nil
}

// ---------------------------------------------------------------- settings

func (r *Runner) settingsLocked(key string) Settings {
	s, ok := r.settings[key]
	if !ok {
		s = DefaultSettings()
		if raw, err := r.host.GetKV(kvSettings + key); err == nil && raw != "" {
			_ = json.Unmarshal([]byte(raw), &s)
		}
		s.Normalize()
		r.settings[key] = s
	}
	return s
}

// Settings is a repository's settings.
func (r *Runner) Settings(key string) Settings {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.settingsLocked(key)
}

// SetSettings changes a repository's settings; runs keep theirs.
func (r *Runner) SetSettings(key string, s Settings) (Settings, error) {
	s.Normalize()
	raw, err := json.Marshal(s)
	if err != nil {
		return s, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.repos[key] == nil {
		return s, ErrNotFound
	}
	if err := r.host.SetKV(kvSettings+key, string(raw)); err != nil {
		return s, err
	}
	r.settings[key] = s
	return s, nil
}

// Global is the settings over every repository.
func (r *Runner) Global() Global {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.global
}

// SetGlobal changes them.
func (r *Runner) SetGlobal(g Global) (Global, error) {
	g.Normalize()
	raw, _ := json.Marshal(g)
	if err := r.host.SetKV(kvGlobal, string(raw)); err != nil {
		return g, err
	}
	r.mu.Lock()
	r.global = g
	r.mu.Unlock()
	r.poke()
	return g, nil
}

// --------------------------------------------------------------- control

// StopRun stops a run: queued cells are dropped, running ones interrupted,
// their hooks run and their leases released.
func (r *Runner) StopRun(id string) error {
	r.mu.Lock()
	rs := r.runs[id]
	if rs == nil {
		r.mu.Unlock()
		return ErrNotFound
	}
	if !Active(rs.run.State) {
		r.mu.Unlock()
		return nil
	}
	chats := r.stopLocked(rs)
	r.endRunLocked(rs, RunStopped)
	for _, c := range chats {
		r.spawnLocked(func() { r.host.StopSession(r.ctx, c) })
	}
	r.mu.Unlock()
	r.poke()
	return nil
}

// stopLocked ends everything of a run that runs, and returns the chats to
// interrupt.
func (r *Runner) stopLocked(rs *runState) []string {
	var chats []string
	for _, ref := range rs.run.Order {
		a := rs.attempts[ref.ID]
		if a == nil || Final(a.State) {
			continue
		}
		if a.Chat != "" {
			chats = append(chats, a.Chat)
		}
		r.finishLocked(rs, a, AttemptStopped, "the run was stopped")
	}
	now := r.now().UnixMilli()
	for _, h := range append(append([]*Helper{}, rs.run.Reviews...), rs.run.Analysis) {
		if h != nil && h.State == "running" {
			if h.Chat != "" {
				chats = append(chats, h.Chat)
			}
			h.State, h.EndedAt, h.Error = "failed", now, "the run was stopped"
		}
	}
	for _, a := range latestAttempts(rs) {
		if a.Review != nil && (a.Review.State == ReviewPending || a.Review.State == ReviewRunning) {
			a.Review.State, a.Review.Note = ReviewSkipped, "the run was stopped"
			r.saveAttemptLocked(a)
		}
	}
	return chats
}

// DeleteRun deletes a run with its sessions and files; an active one is
// stopped first. A run whose end hooks still run is deleted once they are
// done (runEndHooks): until then its leases still count.
func (r *Runner) DeleteRun(id string) error {
	if err := r.StopRun(id); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	rs := r.runs[id]
	if rs == nil {
		return ErrNotFound
	}
	if len(rs.ending) > 0 {
		rs.deleting = true
		r.host.Publish(map[string]any{"t": "tests", "repo": rs.run.Repo, "run": rs.run.ID, "deleted": true})
		return nil
	}
	for _, c := range r.deleteLocked(rs) {
		r.spawnLocked(func() { r.host.RemoveSession(context.Background(), c) })
	}
	return nil
}

// deleteLocked forgets a run and removes its folder; it returns its chats.
func (r *Runner) deleteLocked(rs *runState) []string {
	var chats []string
	for chat, ref := range r.byChat {
		if ref.Run == rs.run.ID {
			chats = append(chats, chat)
			delete(r.byChat, chat)
			delete(r.turns, chat)
		}
	}
	delete(r.runs, rs.run.ID)
	if err := os.RemoveAll(r.layout.RunDir(rs.run.Repo, rs.run.ID)); err != nil {
		log.Printf("tests: run %s: %v", rs.run.ID, err)
	}
	_ = os.RemoveAll(r.layout.RunRuntimeDir(rs.run.ID))
	r.host.Publish(map[string]any{"t": "tests", "repo": rs.run.Repo, "run": rs.run.ID, "deleted": true})
	return chats
}

// pruneLocked deletes a repository's runs beyond its retention: the newest
// KeepRuns stay, and the pinned and the active ones on top.
func (r *Runner) pruneLocked(repo string) {
	keep := r.settingsLocked(repo).KeepRuns
	var list []*runState
	for _, rs := range r.runs {
		if rs.run.Repo == repo && !rs.run.Pinned && !Active(rs.run.State) {
			list = append(list, rs)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].run.CreatedAt > list[j].run.CreatedAt })
	for i := keep; i < len(list); i++ {
		if len(list[i].ending) > 0 {
			continue
		}
		for _, c := range r.deleteLocked(list[i]) {
			r.spawnLocked(func() { r.host.RemoveSession(context.Background(), c) })
		}
	}
}

// PinRun keeps a run out of the retention, or puts it back.
func (r *Runner) PinRun(id string, pinned bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rs := r.runs[id]
	if rs == nil {
		return ErrNotFound
	}
	rs.run.Pinned = pinned
	r.saveRunLocked(rs)
	r.publishLocked(rs)
	if !pinned {
		r.pruneLocked(rs.run.Repo)
	}
	return nil
}

// ResumeRun tries a paused run again: its secrets are read anew.
func (r *Runner) ResumeRun(id string) error {
	r.mu.Lock()
	rs := r.runs[id]
	if rs == nil {
		r.mu.Unlock()
		return ErrNotFound
	}
	if rs.run.State != RunPaused {
		r.mu.Unlock()
		return fmt.Errorf("the run is %s, not paused", rs.run.State)
	}
	rs.resuming = true
	r.spawnLocked(func() { r.resume(rs) })
	r.mu.Unlock()
	return nil
}

// RetryCell queues another attempt of a cell by hand. A run that is over
// is taken up again for it, unless the repository has another active run.
func (r *Runner) RetryCell(runID, cellID string) (*Attempt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rs := r.runs[runID]
	if rs == nil {
		return nil, ErrNotFound
	}
	a := cellOf(rs, cellID)
	if a == nil {
		return nil, ErrNotFound
	}
	if !Final(a.State) {
		return nil, errors.New("the cell is still running")
	}
	switch rs.run.State {
	case RunRunning, RunFinishing:
	case RunDone, RunStopped:
		if r.activeRunLocked(rs.run.Repo, rs.run.ID) != nil {
			return nil, ErrBusy
		}
		if rs.secrets == nil {
			return nil, errors.New("the run's secrets are not read since the last start; start a new run of the cell")
		}
		rs.run.State, rs.run.EndedAt, rs.run.Notified = RunRunning, 0, false
		rs.run.Analysis = nil
	default:
		return nil, fmt.Errorf("the run is %s", rs.run.State)
	}
	attempts := rs.run.Settings.Attempts
	rs.run.Settings.Attempts = a.N + 1
	a.Retried = ""
	ok := r.retryAnyLocked(rs, a)
	rs.run.Settings.Attempts = max(attempts, a.N+1)
	if !ok {
		return nil, errors.New("the cell could not be queued again")
	}
	r.saveRunLocked(rs)
	r.publishLocked(rs)
	r.poke()
	return rs.attempts[a.Retried], nil
}

// retryAnyLocked queues another attempt whatever the verdict.
func (r *Runner) retryAnyLocked(rs *runState, a *Attempt) bool {
	verdict := a.Reviewed
	a.Reviewed = testtools.Blocked
	ok := r.retryLocked(rs, a)
	a.Reviewed = verdict
	r.saveAttemptLocked(a)
	return ok
}

// Analyse runs the analysis of a run that is over again.
func (r *Runner) Analyse(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rs := r.runs[id]
	if rs == nil {
		return ErrNotFound
	}
	if rs.run.State != RunDone && rs.run.State != RunStopped {
		return fmt.Errorf("the run is %s; it is analysed when its cells are done", rs.run.State)
	}
	if r.activeRunLocked(rs.run.Repo, rs.run.ID) != nil {
		return ErrBusy
	}
	if rs.run.Analysis != nil && rs.run.Analysis.Chat != "" {
		chat := rs.run.Analysis.Chat
		delete(r.byChat, chat)
		r.spawnLocked(func() { r.host.RemoveSession(context.Background(), chat) })
	}
	rs.run.Analysis = nil
	rs.run.Settings.Analysis = true
	rs.run.State, rs.run.EndedAt = RunFinishing, 0
	r.saveRunLocked(rs)
	r.publishLocked(rs)
	r.poke()
	return nil
}
