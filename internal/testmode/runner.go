package testmode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent/testtools"
	"github.com/saschazesiger/SocratesAgent/internal/mcpserver"
	"github.com/saschazesiger/SocratesAgent/internal/testmode/catalog"
)

// Host is what the runner needs of the server: sessions, accounts, the
// events and push, and somewhere to keep its settings.
type Host interface {
	// StartSession creates a test session (a chat its run owns) and sends it
	// its first message; it returns the chat's id.
	StartSession(ctx context.Context, spec SessionSpec) (string, error)
	// SendMessage sends a test session a message of the runner's own.
	SendMessage(ctx context.Context, chat, text string) error
	// StopSession interrupts a test session's turn; the chat stays.
	StopSession(ctx context.Context, chat string)
	// RemoveSession deletes a test session.
	RemoveSession(ctx context.Context, chat string)
	// SessionStatus is whether a test session is still there and whether a
	// turn of it runs: what a restart reconciles with.
	SessionStatus(chat string) (exists, running bool)
	// PickAccount is the account a session of harness runs on: the best of
	// allowed (every account of the harness when it is empty), taking how
	// many of the run's sessions each account holds (load); "" with why when
	// none can be taken now.
	PickAccount(harness string, allowed []string, load map[string]int) (string, string)
	// Publish sends a frame to every page; Notify pushes a notification.
	Publish(frame map[string]any)
	Notify(title, body, url string)
	GetKV(key string) (string, error)
	SetKV(key, value string) error
	// MCPPool is the pool of accounts of an MCP server built into Socrates,
	// nil for a server without one (or not built in).
	MCPPool(server string) mcpserver.Pool
	// EndMCP ends what the built-in MCP servers keep for a test session (a
	// device it left open), before its leases are released.
	EndMCP(chat string)
	// SessionCosts is what each of the test sessions cost (USD) as the
	// server's cost ledger has it; a session it knows nothing of is absent.
	SessionCosts(chats []string) map[string]float64
}

// SessionSpec is a test session to start.
type SessionSpec struct {
	Ref     Ref
	Title   string
	Role    Role
	Workdir string
	Message string
	// Created is told the session's id once its row is there and before its
	// first message is sent, so that its first tool call already finds it.
	Created func(chat string)
}

// Ref is what a test session's row holds in its options (store.TestOption):
// which run and which of its attempts or helpers it is.
type Ref struct {
	Kind string `json:"kind"` // testtools.KindCell, KindReviewer, KindAnalyst
	Repo string `json:"repo"`
	Run  string `json:"run"`
	ID   string `json:"id"`
}

// Observation is what the server tells the runner of a test session when
// its activity changes.
type Observation struct {
	TurnRunning bool
	Idle        bool
	// Failed is the agent that did not start, with why.
	Failed bool
	Detail string
}

// The keys of the runner's settings in the store's kv.
const (
	kvRepos    = "tests.repos"
	kvSettings = "tests.settings."
	kvGlobal   = "tests.global"
)

// Runner runs the test runs of every repository.
type Runner struct {
	host   Host
	layout Layout

	ctx    context.Context
	cancel context.CancelFunc
	wake   chan struct{}
	wg     sync.WaitGroup

	mu       sync.Mutex
	repos    map[string]*Repo
	settings map[string]Settings
	global   Global
	runs     map[string]*runState
	// numbers is the largest run number each repository has had, so that a
	// number is not given twice while the server runs, even when the run
	// that had it was deleted.
	numbers map[string]int
	// bugs is each repository's bug list (bugs.json), read when first
	// needed; github whether its issues can be made, read at most once a
	// minute.
	bugs   map[string]*bugList
	github map[string]githubReading
	byChat map[string]Ref
	turns  map[string]bool // a turn of the chat was seen running
	busy   map[string]busyReading
	// loaded is set once the runs are read, closed once Close began: no
	// goroutine starts after that.
	loaded bool
	closed bool
	// gen counts the changes of the runs and attempts (changedLocked);
	// hist is each repository's history as of a gen, read again once it
	// moved on.
	gen  uint64
	hist map[string]*repoHistory
	// now is the clock, a field for tests.
	now func() time.Time
}

// changedLocked notes that a run or an attempt changed (or came or went):
// the histories read before are read again when next asked for.
func (r *Runner) changedLocked() { r.gen++ }

type busyReading struct {
	busy map[int]bool
	at   time.Time
}

// runState is a run in memory.
type runState struct {
	run      *Run
	plan     *plan
	attempts map[string]*Attempt
	secrets  map[string]map[string]string
	red      *Redactor
	// ending holds the attempts whose end hooks run: their leases are held
	// until the hooks are done.
	ending map[string]bool
	// peeked is when the evidence of a running attempt was last read for
	// its live view.
	peeked map[string]time.Time
	// preparing is set while prepare runs; resuming while an active run is
	// taken up again after a start (its secrets are read again), when no
	// cell of it starts and no call of its sessions is answered; deleting
	// while a deleted run waits for its end hooks.
	preparing bool
	resuming  bool
	deleting  bool
	// counts is countsOf the run as of the runner's gen countsGen.
	counts    Counts
	countsGen uint64
	countsOK  bool
	// reported is the run's bugs.json as last read (reportedLocked).
	reported     []testtools.Bug
	reportedRead bool
}

// unredactable is whether what a run's sessions write cannot be redacted
// now, so that nothing of it may be read into what Socrates stores or
// shows: while the run is taken up again after a start (its secrets are
// being read again), or when they could not be read (a run paused for it).
// A run whose plan has no tool has no secrets to miss. Every read of a
// running attempt's evidence (a call of its agent, the live view, the
// attempt's view, its end) asks this first.
func (rs *runState) unredactable() bool {
	if rs.resuming {
		return true
	}
	return rs.red == nil && (rs.plan == nil || len(rs.plan.Tools) > 0)
}

// New is a runner over the data directory; Start loads and starts it.
func New(dataDir string, host Host) *Runner {
	ctx, cancel := context.WithCancel(context.Background())
	return &Runner{host: host, layout: Layout{Data: dataDir}, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1),
		repos: map[string]*Repo{}, settings: map[string]Settings{}, runs: map[string]*runState{}, numbers: map[string]int{},
		bugs: map[string]*bugList{}, hist: map[string]*repoHistory{}, github: map[string]githubReading{}, byChat: map[string]Ref{}, turns: map[string]bool{}, busy: map[string]busyReading{}, now: time.Now}
}

// Load reads the settings and every run from the data directory. The
// server loads the runner before it reattaches the chats, so that what the
// test sessions report as they come back finds their runs.
func (r *Runner) Load() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.loaded {
		return
	}
	r.loaded = true
	r.loadSettingsLocked()
	r.loadRunsLocked()
	for _, rs := range r.runs {
		if Active(rs.run.State) && rs.run.State != RunWaiting {
			rs.resuming = true
		}
	}
}

// Start takes up the runs that were active and starts the loop.
func (r *Runner) Start() {
	r.Load()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepRuntimeLocked()
	// The bug lists learn of the runs that were done when the server
	// stopped before they did; a list that could not be read is built
	// again from the runs.
	r.catchUpBugsLocked()
	// tests-b2: the chats of runs that ended before a restart told them.
	r.tellStartersLocked()
	for _, rs := range r.runs {
		if rs.resuming {
			rs := rs
			r.spawnLocked(func() { r.resume(rs) })
		}
	}
	r.spawnLocked(r.loop)
}

// Close stops the loop and the hooks that run. Sessions keep running; the
// next start takes them up, and runs the end hooks that were cut short.
func (r *Runner) Close() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	r.cancel()
	r.wg.Wait()
}

// spawnLocked runs f on a goroutine the runner waits for at Close; none once
// Close began.
func (r *Runner) spawnLocked(f func()) {
	if r.closed {
		return
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		f()
	}()
}

func (r *Runner) spawn(f func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.spawnLocked(f)
}

// sweepRuntimeLocked removes the runtime folders no active run needs: what
// a run or an attempt left when the server went down before it was over.
func (r *Runner) sweepRuntimeLocked() {
	dir := filepath.Join(r.layout.Data, "run", "tests")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	keep := map[string]bool{}
	for _, rs := range r.runs {
		if !Active(rs.run.State) {
			continue
		}
		keep[rs.run.ID] = true
		for _, a := range rs.attempts {
			keep[a.ID] = true
		}
	}
	for _, e := range entries {
		if !keep[e.Name()] {
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
}

// poke wakes the loop.
func (r *Runner) poke() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// tick is how often the loop looks at the time limits without a reason to.
var tick = 5 * time.Second

func (r *Runner) loop() {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-r.wake:
		case <-t.C:
		}
		r.schedule()
	}
}

func (r *Runner) loadSettingsLocked() {
	if raw, err := r.host.GetKV(kvRepos); err == nil && raw != "" {
		var list []*Repo
		if json.Unmarshal([]byte(raw), &list) == nil {
			for _, repo := range list {
				if ValidRepoKey(repo.Key) {
					r.repos[repo.Key] = repo
				}
			}
		}
	}
	if raw, err := r.host.GetKV(kvGlobal); err == nil && raw != "" {
		_ = json.Unmarshal([]byte(raw), &r.global)
	}
	r.global.Normalize()
}

// loadRunsLocked reads every run of every repository folder.
func (r *Runner) loadRunsLocked() {
	base := filepath.Join(r.layout.Data, "tests")
	repos, _ := os.ReadDir(base)
	for _, rd := range repos {
		if !rd.IsDir() || !ValidRepoKey(rd.Name()) {
			continue
		}
		runs, _ := os.ReadDir(filepath.Join(base, rd.Name(), "runs"))
		for _, e := range runs {
			if !e.IsDir() || !ValidRunID(e.Name()) {
				continue
			}
			rs, err := r.loadRun(rd.Name(), e.Name())
			if err != nil {
				log.Printf("tests: run %s: %v", e.Name(), err)
				continue
			}
			r.runs[rs.run.ID] = rs
			r.changedLocked()
		}
	}
	r.numberRunsLocked()
}

// numberRunsLocked gives the runs from before run numbers theirs, in the
// order they were created after the numbered ones, and saves them.
func (r *Runner) numberRunsLocked() {
	byRepo := map[string][]*runState{}
	for _, rs := range r.runs {
		byRepo[rs.run.Repo] = append(byRepo[rs.run.Repo], rs)
		r.numbers[rs.run.Repo] = max(r.numbers[rs.run.Repo], rs.run.Number)
	}
	for repo, list := range byRepo {
		sort.Slice(list, func(i, j int) bool { return list[i].run.CreatedAt < list[j].run.CreatedAt })
		for _, rs := range list {
			if rs.run.Number > 0 {
				continue
			}
			r.numbers[repo]++
			rs.run.Number = r.numbers[repo]
			r.saveRunLocked(rs)
		}
	}
}

func (r *Runner) loadRun(repo, id string) (*runState, error) {
	run := &Run{}
	if err := readJSON(filepath.Join(r.layout.RunDir(repo, id), "run.json"), run); err != nil {
		return nil, err
	}
	rs := newRunState(run)
	p := &plan{}
	if err := readJSON(r.layout.planPath(repo, id), p); err == nil {
		rs.plan = p
	}
	for _, ref := range run.Order {
		a := &Attempt{Repo: repo, Run: id, Test: ref.Test, Cell: ref.Cell, N: ref.N}
		path := filepath.Join(r.layout.AttemptDir(a), "record.json")
		if err := readJSON(path, a); err != nil {
			log.Printf("tests: run %s: attempt %s: %v", id, ref.ID, err)
			continue
		}
		rs.attempts[a.ID] = a
		if a.Chat != "" {
			r.byChat[a.Chat] = Ref{Kind: testtools.KindCell, Repo: repo, Run: id, ID: a.ID}
		}
	}
	for _, h := range run.Reviews {
		if h.Chat != "" {
			r.byChat[h.Chat] = Ref{Kind: testtools.KindReviewer, Repo: repo, Run: id, ID: h.ID}
		}
	}
	if h := run.Analysis; h != nil && h.Chat != "" {
		r.byChat[h.Chat] = Ref{Kind: testtools.KindAnalyst, Repo: repo, Run: id, ID: h.ID}
	}
	return rs, nil
}

// resume takes up a run that was active when the server stopped: a run
// still preparing is prepared again; the secrets of one that ran are read
// again (they were in memory only) and written into its vaults, and its
// sessions are reconciled with what they did meanwhile. Until then no cell
// of it starts and no call of its sessions is answered (resuming).
func (r *Runner) resume(rs *runState) {
	r.mu.Lock()
	run := rs.run
	state := run.State
	if state == RunPreparing {
		rs.resuming = false
		r.mu.Unlock()
		r.activate(run.ID)
		return
	}
	r.mu.Unlock()
	if rs.plan == nil {
		r.mu.Lock()
		rs.resuming = false
		r.mu.Unlock()
		r.failRun(run.ID, "the run's plan is missing")
		return
	}
	cat, err := catalog.Load(run.Path)
	var secrets map[string]map[string]string
	if err == nil {
		var tools []*catalog.Tool
		for name := range rs.plan.Tools {
			if t := cat.Tools[name]; t != nil {
				tools = append(tools, t)
			}
		}
		secrets, err = readSecrets(r.ctx, run.Path, tools)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	rs.resuming = false
	if err != nil {
		run.State, run.Error = RunPaused, "The secrets could not be read again after a restart: "+err.Error()
		r.saveRunLocked(rs)
		r.publishLocked(rs)
		return
	}
	rs.secrets = secrets
	rs.red = NewRedactor(flatten(secrets))
	if run.State == RunPaused {
		run.State, run.Error = RunRunning, ""
	}
	var nudges [][2]string
	for _, a := range rs.attempts {
		ref := Ref{Kind: testtools.KindCell, Repo: run.Repo, Run: run.ID, ID: a.ID}
		switch {
		case a.State == AttemptStarting:
			// It was being started when the server stopped: start it again.
			if a.Chat != "" {
				chat := a.Chat
				delete(r.byChat, chat)
				r.spawnLocked(func() { r.host.RemoveSession(r.ctx, chat) })
			}
			a.State, a.Leases, a.Chat = AttemptQueued, nil, ""
			r.saveAttemptLocked(a)
		case a.State == AttemptRunning:
			if err := r.writeAttemptVaultLocked(rs, a); err != nil {
				log.Printf("tests: %s: %v", a.ID, err)
			}
			for _, l := range a.Leases {
				if pool := r.host.MCPPool(l.Tool); l.MCP && pool != nil && !pool.Restore(a.ID, l.Account) {
					log.Printf("tests: %s: the %s account %s is gone", a.ID, l.Tool, l.Label)
				}
			}
			if n := r.reconcileLocked(rs, ref, a.Chat); n != "" {
				nudges = append(nudges, [2]string{a.Chat, n})
			}
		case Final(a.State):
			// An attempt whose end hooks were cut short by the stop has its
			// runtime folder still: they run again.
			if _, err := os.Stat(r.layout.RuntimeDir(a.ID)); err == nil && len(a.Leases) > 0 && r.hasEndHooks(rs, a) {
				if err := r.writeAttemptVaultLocked(rs, a); err == nil {
					rs.ending[a.ID] = true
					env, a := r.attemptEnvLocked(rs, a), a
					r.spawnLocked(func() { r.runEndHooks(rs, a, env) })
				}
			}
		}
	}
	for _, h := range append(append([]*Helper{}, run.Reviews...), run.Analysis) {
		if h == nil || h.State != "running" {
			continue
		}
		kind := testtools.KindReviewer
		if h == run.Analysis {
			kind = testtools.KindAnalyst
		}
		if n := r.reconcileLocked(rs, Ref{Kind: kind, Repo: run.Repo, Run: run.ID, ID: h.ID}, h.Chat); n != "" {
			nudges = append(nudges, [2]string{h.Chat, n})
		}
	}
	r.saveRunLocked(rs)
	r.publishLocked(rs)
	for _, n := range nudges {
		n := n
		r.spawnLocked(func() { r.nudge(n[0], n[1]) })
	}
	r.poke()
}

// reconcileLocked is a running session after a restart: one that is gone
// ends its attempt or helper, one whose turn ended meanwhile is taken as a
// turn that ended now, one whose turn runs is watched for its end. It
// returns the nudge to send.
func (r *Runner) reconcileLocked(rs *runState, ref Ref, chat string) string {
	if chat == "" {
		if ref.Kind == testtools.KindCell {
			if a := rs.attempts[ref.ID]; a != nil {
				r.finishLocked(rs, a, AttemptAborted, "not reached: its session was never made")
			}
		} else if h := rs.helper(ref.ID); h != nil {
			h.State, h.Error, h.EndedAt = "failed", "its session was never made", r.now().UnixMilli()
			r.helperFailedLocked(rs, h)
		}
		return ""
	}
	exists, running := r.host.SessionStatus(chat)
	switch {
	case !exists:
		if ref.Kind == testtools.KindCell {
			if a := rs.attempts[ref.ID]; a != nil {
				r.finishLocked(rs, a, AttemptAborted, "not reached: its session is gone")
			}
		} else if h := rs.helper(ref.ID); h != nil {
			h.State, h.Error, h.EndedAt = "failed", "its session is gone", r.now().UnixMilli()
			r.helperFailedLocked(rs, h)
		}
		return ""
	case running:
		r.turns[chat] = true
		return ""
	}
	return r.turnEndedLocked(rs, ref)
}

// nudge sends a session a message of the runner's, off the caller's
// goroutine: the server's sending waits for the session's host to take it,
// which the goroutine that reported the session's activity would hold up.
func (r *Runner) nudge(chat, text string) {
	if err := r.host.SendMessage(r.ctx, chat, text); err != nil {
		log.Printf("tests: %s: could not nudge: %v", chat, err)
	}
}

func flatten(secrets map[string]map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range secrets {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// saveRunLocked writes run.json; nothing for a run that was deleted.
func (r *Runner) saveRunLocked(rs *runState) {
	r.changedLocked()
	if r.runs[rs.run.ID] != rs {
		return
	}
	if err := writeJSON(filepath.Join(r.layout.RunDir(rs.run.Repo, rs.run.ID), "run.json"), rs.run); err != nil {
		log.Printf("tests: run %s: %v", rs.run.ID, err)
	}
}

// saveAttemptLocked writes an attempt's record.json; nothing for an attempt
// of a run that was deleted.
func (r *Runner) saveAttemptLocked(a *Attempt) {
	r.changedLocked()
	if rs := r.runs[a.Run]; rs == nil || rs.attempts[a.ID] != a {
		return
	}
	if err := writeJSON(filepath.Join(r.layout.AttemptDir(a), "record.json"), a); err != nil {
		log.Printf("tests: attempt %s: %v", a.ID, err)
	}
}

// ---------------------------------------------------------------- starting

// ErrBusy is a repository that has an active run already, for a caller that
// wants to say so.
var ErrBusy = errors.New("the repository has an active run")

// StartRun creates a run of a repository's catalog. It is prepared at once,
// or once the repository's active run is over.
func (r *Runner) StartRun(repoKey, name string, sel Selection, settings *Settings) (*Run, error) {
	r.mu.Lock()
	repo := r.repos[repoKey]
	if repo == nil {
		r.mu.Unlock()
		return nil, fmt.Errorf("no repository %s", repoKey)
	}
	s := r.settingsLocked(repoKey)
	if settings != nil {
		s = *settings
	}
	s.Normalize()
	sel, err := r.resolveSelectionLocked(repoKey, sel)
	if err != nil {
		r.mu.Unlock()
		return nil, err
	}
	now := r.now()
	r.numbers[repoKey]++
	run := &Run{ID: newRunID(now), Repo: repoKey, Number: r.numbers[repoKey], Path: repo.Path, Name: strings.TrimSpace(name),
		State: RunWaiting, Selection: sel, Settings: s, CreatedAt: now.UnixMilli()}
	rs := newRunState(run)
	r.runs[run.ID] = rs
	if r.activeRunLocked(repoKey, run.ID) == nil {
		run.State = RunPreparing
	}
	r.saveRunLocked(rs)
	r.publishLocked(rs)
	if run.State == RunPreparing {
		r.spawnLocked(func() { r.activate(run.ID) })
	}
	r.mu.Unlock()
	return run, nil
}

func newRunState(run *Run) *runState {
	return &runState{run: run, attempts: map[string]*Attempt{}, ending: map[string]bool{}, peeked: map[string]time.Time{}}
}

// resolveSelectionLocked checks a selection's ProblemsOf against the
// repository's runs and puts the id of the run it means for "latest".
func (r *Runner) resolveSelectionLocked(repo string, sel Selection) (Selection, error) {
	sel.Cells = slices.Clone(sel.Cells)
	for i := range sel.Cells {
		sel.Cells[i] = strings.TrimSpace(sel.Cells[i])
	}
	switch sel.ProblemsOf = strings.TrimSpace(sel.ProblemsOf); sel.ProblemsOf {
	case "":
	case ProblemsLatest:
		latest := r.latestRunLocked(repo)
		if latest == nil {
			return sel, errors.New("the repository has no run that is over yet")
		}
		sel.ProblemsOf = latest.run.ID
	default:
		if prev := r.runs[sel.ProblemsOf]; prev == nil || prev.run.Repo != repo {
			return sel, fmt.Errorf("no run %s of this repository", sel.ProblemsOf)
		}
	}
	return sel, nil
}

// problemsLocked is the cells of the run a selection's ProblemsOf names
// that did not pass, as they count; nil for a selection without one.
func (r *Runner) problemsLocked(sel Selection) map[string]bool {
	if sel.ProblemsOf == "" {
		return nil
	}
	problems := map[string]bool{}
	if prev := r.runs[sel.ProblemsOf]; prev != nil {
		for _, a := range latestAttempts(prev) {
			if v := a.Final(); v != testtools.Pass && v != testtools.NA {
				problems[a.CellID] = true
			}
		}
	}
	return problems
}

// activeRunLocked is the repository's active run other than except, nil
// for none.
func (r *Runner) activeRunLocked(repo, except string) *runState {
	for _, rs := range r.runs {
		if rs.run.Repo == repo && rs.run.ID != except && Active(rs.run.State) && rs.run.State != RunWaiting {
			return rs
		}
	}
	return nil
}

// activate prepares a run and queues its cells.
func (r *Runner) activate(id string) {
	r.mu.Lock()
	rs := r.runs[id]
	if rs == nil || rs.preparing {
		r.mu.Unlock()
		return
	}
	rs.preparing = true
	rs.run.State, rs.run.Error = RunPreparing, ""
	run := *rs.run
	problems := r.problemsLocked(run.Selection)
	r.saveRunLocked(rs)
	r.publishLocked(rs)
	r.mu.Unlock()

	p, err := r.prepare(r.ctx, &run, problems)

	r.mu.Lock()
	defer r.mu.Unlock()
	rs.preparing = false
	if rs.run.State != RunPreparing {
		// Stopped meanwhile.
		return
	}
	rs.run.Commit, rs.run.Dirty = run.Commit, run.Dirty
	if err != nil {
		rs.run.State, rs.run.Error, rs.run.EndedAt = RunFailed, err.Error(), r.now().UnixMilli()
		r.saveRunLocked(rs)
		r.publishLocked(rs)
		r.tellStarterLocked(rs) // tests-b2
		r.nextRunLocked(rs.run.Repo)
		return
	}
	rs.plan, rs.secrets = p.plan, p.secrets
	rs.red = NewRedactor(flatten(p.secrets))
	for _, a := range p.attempts {
		rs.attempts[a.ID] = a
		rs.run.Order = append(rs.run.Order, AttemptRef{ID: a.ID, Test: a.Test, Cell: a.Cell, N: a.N})
		r.saveAttemptLocked(a)
	}
	rs.run.State, rs.run.StartedAt = RunRunning, r.now().UnixMilli()
	r.saveRunLocked(rs)
	r.publishLocked(rs)
	r.poke()
}

// nextRunLocked activates the oldest waiting run of a repository once none
// is active.
func (r *Runner) nextRunLocked(repo string) {
	if r.activeRunLocked(repo, "") != nil {
		return
	}
	var next *runState
	for _, rs := range r.runs {
		if rs.run.Repo == repo && rs.run.State == RunWaiting && (next == nil || rs.run.CreatedAt < next.run.CreatedAt) {
			next = rs
		}
	}
	if next == nil {
		return
	}
	next.run.State = RunPreparing
	r.saveRunLocked(next)
	id := next.run.ID
	r.spawnLocked(func() { r.activate(id) })
}

func (r *Runner) failRun(id, why string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rs := r.runs[id]
	if rs == nil {
		return
	}
	rs.run.State, rs.run.Error, rs.run.EndedAt = RunFailed, why, r.now().UnixMilli()
	r.saveRunLocked(rs)
	r.publishLocked(rs)
	r.tellStarterLocked(rs) // tests-b2
	r.nextRunLocked(rs.run.Repo)
}

// ------------------------------------------------------------ scheduling

// start is one session the schedule decided to start, started outside the
// lock.
type start struct {
	rs     *runState
	a      *Attempt
	helper *Helper
	spec   SessionSpec
}

// schedule looks at every active run: time limits, then what can start.
func (r *Runner) schedule() {
	r.mu.Lock()
	now := r.now()
	var starts []start
	var stops []string
	running := r.sessionsLocked()
	for _, rs := range r.sortedRunsLocked() {
		if rs.run.State != RunRunning && rs.run.State != RunFinishing || rs.resuming || rs.deleting {
			continue
		}
		for _, h := range append(append([]*Helper{}, rs.run.Reviews...), rs.run.Analysis) {
			if h != nil && h.State == "running" && now.UnixMilli()-h.StartedAt > helperTimeout.Milliseconds() {
				if h.Chat != "" {
					stops = append(stops, h.Chat)
				}
				h.State, h.EndedAt, h.Error = "failed", now.UnixMilli(), fmt.Sprintf("it took longer than %s", helperTimeout)
				r.helperFailedLocked(rs, h)
				r.saveRunLocked(rs)
			}
		}
		for _, a := range rs.attempts {
			if a.State == AttemptRunning && a.TimeoutMS > 0 && now.UnixMilli()-a.StartedAt > a.TimeoutMS {
				if a.Chat != "" {
					stops = append(stops, a.Chat)
				}
				r.finishLocked(rs, a, AttemptAborted, fmt.Sprintf("not reached: the cell took longer than its %s", time.Duration(a.TimeoutMS)*time.Millisecond))
			}
		}
		starts = append(starts, r.startCellsLocked(rs, &running)...)
		starts = append(starts, r.startHelpersLocked(rs, &running)...)
		r.advanceLocked(rs)
	}
	for _, chat := range stops {
		r.spawnLocked(func() { r.host.StopSession(r.ctx, chat) })
	}
	for _, s := range starts {
		r.spawnLocked(func() { r.launch(s) })
	}
	r.mu.Unlock()
}

// helperTimeout is how long a reviewer or an analyst may take.
var helperTimeout = 90 * time.Minute

// loadLocked is how many test sessions run on each harness account.
func (r *Runner) loadLocked() map[string]int {
	load := map[string]int{}
	for _, rs := range r.runs {
		for _, a := range rs.attempts {
			if (a.State == AttemptStarting || a.State == AttemptRunning) && a.Role.Account != "" {
				load[a.Role.Account]++
			}
		}
		for _, h := range append(append([]*Helper{}, rs.run.Reviews...), rs.run.Analysis) {
			if h != nil && h.State == "running" && h.Role.Account != "" {
				load[h.Role.Account]++
			}
		}
	}
	return load
}

// sortedRunsLocked is the runs oldest first: the first run takes the free
// sessions first.
func (r *Runner) sortedRunsLocked() []*runState {
	list := make([]*runState, 0, len(r.runs))
	for _, rs := range r.runs {
		list = append(list, rs)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].run.CreatedAt < list[j].run.CreatedAt })
	return list
}

// sessionsLocked is how many test sessions run on the server.
func (r *Runner) sessionsLocked() int {
	n := 0
	for _, rs := range r.runs {
		for _, a := range rs.attempts {
			if a.State == AttemptStarting || a.State == AttemptRunning {
				n++
			}
		}
		for _, h := range rs.run.Reviews {
			if h.State == "running" {
				n++
			}
		}
		if h := rs.run.Analysis; h != nil && h.State == "running" {
			n++
		}
	}
	return n
}

// heldLocked is the pool members held in a repository, by tool and member.
func (r *Runner) heldLocked(repo string) map[string]map[int]int {
	held := map[string]map[int]int{}
	for _, rs := range r.runs {
		if rs.run.Repo != repo {
			continue
		}
		for _, a := range rs.attempts {
			if a.State != AttemptStarting && a.State != AttemptRunning && !rs.ending[a.ID] {
				continue
			}
			for _, l := range a.Leases {
				if l.MCP {
					continue
				}
				if held[l.Tool] == nil {
					held[l.Tool] = map[int]int{}
				}
				held[l.Tool][l.Member]++
			}
		}
	}
	return held
}

// hasPools is whether the run's tools have a pool.
func (r *Runner) hasPools(rs *runState) bool {
	if rs.plan == nil {
		return false
	}
	for _, t := range rs.plan.Tools {
		if t.Pool != nil {
			return true
		}
	}
	for _, pa := range rs.plan.Areas {
		for _, name := range append(slices.Clone(pa.MCP), rs.run.Settings.MCP...) {
			if r.host.MCPPool(name) != nil {
				return true
			}
		}
	}
	return false
}

// columnOf is the value of an attempt's column dimension.
func (rs *runState) columnOf(a *Attempt) string {
	col := ""
	if rs.plan != nil {
		if pa := rs.plan.Areas[a.Area]; pa != nil {
			col = pa.Columns
		}
	}
	for _, d := range a.Dims {
		if col == "" || d.Name == col {
			return d.ID
		}
	}
	return ""
}

// startCellsLocked decides which of a run's queued cells start now, takes
// their leases and accounts, and says why the others wait.
func (r *Runner) startCellsLocked(rs *runState, running *int) []start {
	if rs.run.State != RunRunning || rs.plan == nil {
		return nil
	}
	var queue []*Attempt
	runningCols := map[string]int{}
	inRun := 0
	held := map[string]bool{} // exclusive names
	// Every session of the server counts on its account: other runs' cells
	// and helpers spend the same allowance.
	load := r.loadLocked()
	for _, ref := range rs.run.Order {
		a := rs.attempts[ref.ID]
		if a == nil {
			continue
		}
		switch a.State {
		case AttemptQueued, AttemptWaiting:
			queue = append(queue, a)
		case AttemptStarting, AttemptRunning:
			inRun++
			runningCols[rs.columnOf(a)]++
			for _, x := range a.Exclusive {
				held[x] = true
			}
		}
	}
	if len(queue) == 0 {
		return nil
	}
	// Exclusive names other runs of the repository hold count too.
	for _, other := range r.runs {
		if other == rs || other.run.Repo != rs.run.Repo {
			continue
		}
		for _, a := range other.attempts {
			if a.State == AttemptStarting || a.State == AttemptRunning {
				for _, x := range a.Exclusive {
					held[x] = true
				}
			}
		}
	}
	// Cells on a column value no running cell is on go first.
	sort.SliceStable(queue, func(i, j int) bool {
		return runningCols[rs.columnOf(queue[i])] == 0 && runningCols[rs.columnOf(queue[j])] > 0
	})
	leases := r.heldLocked(rs.run.Repo)
	// Without a limit of its own a run with pools is limited by them, and
	// says so: a cell waits for an account, not for "a session".
	capacity := rs.run.Settings.Parallel
	if capacity == 0 && !r.hasPools(rs) {
		capacity = DefaultParallel
	}
	var out []start
	waitingFor := map[string]int{}
	for _, a := range queue {
		wait := ""
		switch {
		case capacity > 0 && inRun >= capacity:
			wait = fmt.Sprintf("waiting for a free session (%d of %d running)", inRun, capacity)
		case *running >= r.global.MaxSessions:
			wait = fmt.Sprintf("waiting: the server runs %d test sessions, its limit", *running)
		}
		if wait == "" {
			for _, x := range a.Exclusive {
				if held[x] {
					wait = "waiting for the fixture " + x
					break
				}
			}
		}
		var got []Lease
		if wait == "" {
			got, wait = r.leaseLocked(rs, a, leases, waitingFor)
		}
		account := ""
		if wait == "" {
			account, wait = r.host.PickAccount(rs.run.Settings.Cell.Harness, rs.run.Settings.Cell.Accounts, load)
		}
		// The accounts of the built-in MCP servers last: their pools keep
		// the leases themselves.
		if wait == "" {
			var more []Lease
			more, wait = r.leaseMCPLocked(rs, a, waitingFor)
			got = append(got, more...)
		}
		if wait != "" {
			if a.State != AttemptWaiting || a.Wait != wait {
				a.State, a.Wait = AttemptWaiting, wait
				r.saveAttemptLocked(a)
			}
			continue
		}
		// Taken: the leases count for the cells after it.
		for _, l := range got {
			if l.MCP {
				continue
			}
			if leases[l.Tool] == nil {
				leases[l.Tool] = map[int]int{}
			}
			leases[l.Tool][l.Member]++
		}
		for _, x := range a.Exclusive {
			held[x] = true
		}
		load[account]++
		inRun++
		*running++
		runningCols[rs.columnOf(a)]++
		a.State, a.Wait, a.Leases = AttemptStarting, "", got
		a.Role = rs.run.Settings.Cell
		a.Role.Account = account
		a.WaitedMS = r.now().UnixMilli() - a.QueuedAt
		r.saveAttemptLocked(a)
		out = append(out, start{rs: rs, a: a, spec: SessionSpec{
			Ref:   Ref{Kind: testtools.KindCell, Repo: rs.run.Repo, Run: rs.run.ID, ID: a.ID},
			Title: "Test " + a.CellID, Role: a.Role, Workdir: rs.run.Path,
		}})
	}
	if len(out) > 0 || len(queue) > 0 {
		r.publishLocked(rs)
	}
	return out
}

// leaseLocked takes a free member of every pool the attempt's tools have;
// none when one pool has no member free, with the reason.
func (r *Runner) leaseLocked(rs *runState, a *Attempt, held map[string]map[int]int, waitingFor map[string]int) ([]Lease, string) {
	var got []Lease
	for _, name := range a.Tools {
		t := rs.plan.Tools[name]
		if t == nil || t.Pool == nil {
			continue
		}
		list := members(t.Pool, rs.secrets[name])
		per := max(1, t.Pool.PerMember)
		busy := r.busyLocked(rs, t)
		best := 0
		for pass := 0; pass < 2 && best == 0; pass++ {
			for i := range list {
				n := i + 1
				if held[name][n] >= per {
					continue
				}
				// Members busy outside Socrates go last.
				if pass == 0 && busy[n] {
					continue
				}
				best = n
				break
			}
		}
		if best == 0 {
			waitingFor[name]++
			title := t.Title
			if title == "" {
				title = name
			}
			return nil, fmt.Sprintf("waiting for a free %s account (%d ahead)", title, waitingFor[name]-1)
		}
		got = append(got, Lease{Tool: name, Member: best, Label: memberLabel(list[best-1], best)})
	}
	return got, ""
}

// mcpPools is the built-in MCP servers with a pool an attempt's session
// gets: those its area names and those the run's settings add.
func (r *Runner) mcpPools(rs *runState, a *Attempt) []string {
	var names []string
	if pa := rs.plan.Areas[a.Area]; pa != nil {
		names = append(names, pa.MCP...)
	}
	names = append(names, rs.run.Settings.MCP...)
	var out []string
	for _, name := range names {
		if !slices.Contains(out, name) && r.host.MCPPool(name) != nil {
			out = append(out, name)
		}
	}
	return out
}

// leaseMCPLocked takes an account of every built-in MCP server with a pool
// the attempt's area names, for the attempt; none when one has no account
// free, with the reason (and what was taken is given back).
func (r *Runner) leaseMCPLocked(rs *runState, a *Attempt, waitingFor map[string]int) ([]Lease, string) {
	var got []Lease
	for _, name := range r.mcpPools(rs, a) {
		pool := r.host.MCPPool(name)
		lease, why := pool.Lease(a.ID)
		if lease.Account == "" {
			for _, l := range got {
				r.host.MCPPool(l.Tool).Release(a.ID)
			}
			waitingFor["mcp:"+name]++
			if strings.HasPrefix(why, "every ") || why == "" {
				return nil, fmt.Sprintf("waiting for a free %s account (%d ahead)", pool.PoolTitle(), waitingFor["mcp:"+name]-1)
			}
			return nil, "waiting: " + why
		}
		got = append(got, Lease{Tool: name, Label: lease.Label, MCP: true, Account: lease.Account})
	}
	return got, ""
}

// releaseMCP gives back the attempt's leases of built-in MCP servers.
func (r *Runner) releaseMCP(a *Attempt) {
	for _, l := range a.Leases {
		if l.MCP {
			if pool := r.host.MCPPool(l.Tool); pool != nil {
				pool.Release(a.ID)
			}
		}
	}
}

// busyEvery is how long a pool's status reading is used.
const busyEvery = time.Minute

// busyLocked is the members of a pool busy outside Socrates, read by its
// status hook at most once a minute, in the background.
func (r *Runner) busyLocked(rs *runState, t *planTool) map[int]bool {
	if t.Pool == nil || strings.TrimSpace(t.Pool.Status) == "" {
		return nil
	}
	key := rs.run.Repo + "/" + t.Name
	reading, ok := r.busy[key]
	if !ok || r.now().Sub(reading.at) > busyEvery {
		r.busy[key] = busyReading{busy: reading.busy, at: r.now()}
		env := r.runEnvLocked(rs)
		dir, line := t.Dir, t.Pool.Status
		r.spawnLocked(func() {
			busy := poolBusy(r.ctx, dir, line, env)
			r.mu.Lock()
			r.busy[key] = busyReading{busy: busy, at: r.now()}
			r.mu.Unlock()
		})
	}
	return reading.busy
}

// runEnvLocked is the environment of a run's hooks: the run's vault, which
// holds every tool's secrets and no lease.
func (r *Runner) runEnvLocked(rs *runState) []string {
	vault := filepath.Join(r.layout.RunRuntimeDir(rs.run.ID), "vault")
	if err := writeVault(vault, flatten(rs.secrets)); err != nil {
		log.Printf("tests: run %s: %v", rs.run.ID, err)
	}
	return append(os.Environ(), "SOCRATES_TEST=1", "SOCRATES_TEST_RUN="+rs.run.ID,
		"SOCRATES_TEST_REPO="+rs.run.Path, "SOCRATES_TEST_VAULT="+vault)
}

// launch starts a session the schedule decided on.
func (r *Runner) launch(s start) {
	r.mu.Lock()
	if s.a != nil {
		if s.a.State != AttemptStarting {
			r.mu.Unlock()
			return
		}
		if err := r.writeAttemptVaultLocked(s.rs, s.a); err != nil {
			r.finishLocked(s.rs, s.a, AttemptFailed, "not reached: its vault could not be written: "+err.Error())
			r.mu.Unlock()
			return
		}
		_ = os.MkdirAll(r.layout.EvidenceDir(s.a), 0o700)
		s.spec.Message = firstMessage(s.a, s.rs.run.Number)
	}
	// The session is known as soon as its row is: its first tool call may
	// come before StartSession returns.
	s.spec.Created = func(chat string) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.runs[s.rs.run.ID] == s.rs && !s.rs.deleting {
			r.byChat[chat] = s.spec.Ref
			if s.a != nil {
				s.a.Chat = chat
			} else if s.helper != nil {
				s.helper.Chat = chat
			}
		}
	}
	r.mu.Unlock()

	chat, err := r.host.StartSession(r.ctx, s.spec)

	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now().UnixMilli()
	// A run deleted while its session started takes the session with it.
	if r.runs[s.rs.run.ID] != s.rs || s.rs.deleting {
		if chat != "" {
			delete(r.byChat, chat)
			r.spawnLocked(func() { r.host.RemoveSession(r.ctx, chat) })
		}
		return
	}
	if err != nil && chat != "" {
		delete(r.byChat, chat)
		r.spawnLocked(func() { r.host.RemoveSession(r.ctx, chat) })
	}
	if s.a != nil {
		a := s.a
		if err != nil {
			a.Chat = ""
			r.finishLocked(s.rs, a, AttemptFailed, "not reached: the session could not be started: "+err.Error())
			return
		}
		r.byChat[chat] = s.spec.Ref
		a.Chat = chat
		if a.State != AttemptStarting {
			// Stopped while it started: its end hooks ran without the
			// session, so what a built-in MCP server keeps for it is closed
			// here.
			r.saveAttemptLocked(a)
			r.spawnLocked(func() {
				r.host.StopSession(r.ctx, chat)
				r.host.EndMCP(chat)
			})
			return
		}
		a.State, a.StartedAt = AttemptRunning, now
		r.saveAttemptLocked(a)
	} else if h := s.helper; h != nil {
		if err != nil {
			h.Chat = ""
			h.State, h.Error, h.EndedAt = "failed", err.Error(), now
			r.helperFailedLocked(s.rs, h)
		} else {
			h.Chat = chat
			r.byChat[chat] = s.spec.Ref
			if h.State != "running" {
				r.spawnLocked(func() { r.host.StopSession(r.ctx, chat) })
			}
		}
		r.saveRunLocked(s.rs)
	}
	r.publishLocked(s.rs)
	r.poke()
}

// writeAttemptVaultLocked writes the vault of an attempt: its tools' secrets
// and its leases.
func (r *Runner) writeAttemptVaultLocked(rs *runState, a *Attempt) error {
	if rs.secrets == nil {
		return errors.New("the run's secrets are not read")
	}
	values := map[string]string{}
	for _, name := range a.Tools {
		var pool string
		if t := rs.plan.Tools[name]; t != nil && t.Pool != nil {
			pool = t.Pool.Secret
		}
		for k, v := range rs.secrets[name] {
			// A cell gets its own member of a pool (the lease), never the
			// list of every member.
			if k != pool {
				values[k] = v
			}
		}
	}
	for _, l := range a.Leases {
		t := rs.plan.Tools[l.Tool]
		if t == nil {
			continue
		}
		list := members(t.Pool, rs.secrets[l.Tool])
		if l.Member < 1 || l.Member > len(list) {
			return fmt.Errorf("the pool of %s has no member %d", l.Tool, l.Member)
		}
		env := envName(l.Tool)
		values["SOCRATES_LEASE_"+env] = fmt.Sprint(l.Member)
		values["SOCRATES_LEASE_"+env+"_VALUE"] = list[l.Member-1]
	}
	return writeVault(filepath.Join(r.layout.RuntimeDir(a.ID), "vault"), values)
}

// envName is a name as part of an environment variable's.
func envName(s string) string {
	return strings.ToUpper(keyUnsafe.ReplaceAllString(strings.ToLower(s), "_"))
}

// --------------------------------------------------------- the sessions

// Launch is what a test session is started with besides a chat's own.
type Launch struct {
	Kind         string
	Instructions string
	Env          []string
	// Path is folders to put first on PATH.
	Path []string
	// MCP is the names of the repository's MCP servers the session may use;
	// every other is off.
	MCP []string
}

// LaunchFor is what the session of ref is launched with, every time its
// host starts: from the run's plan and the attempt's record, so that a host
// started after a park or a restart gets the same.
func (r *Runner) LaunchFor(raw json.RawMessage) (*Launch, error) {
	var ref Ref
	if err := json.Unmarshal(raw, &ref); err != nil {
		return nil, fmt.Errorf("not a test session: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	rs := r.runs[ref.Run]
	if rs == nil || rs.plan == nil {
		return nil, fmt.Errorf("the test run %s is gone", ref.Run)
	}
	l := &Launch{Kind: ref.Kind, Env: []string{"SOCRATES_TEST=1", "SOCRATES_TEST_RUN=" + rs.run.ID, "SOCRATES_TEST_REPO=" + rs.run.Path}}
	switch ref.Kind {
	case testtools.KindCell:
		a := rs.attempts[ref.ID]
		if a == nil {
			return nil, fmt.Errorf("the attempt %s is gone", ref.ID)
		}
		pa := rs.plan.Areas[a.Area]
		if pa != nil {
			l.Instructions, l.MCP = pa.Prompt, slices.Clone(pa.MCP)
		}
		for _, name := range rs.run.Settings.MCP {
			if !slices.Contains(l.MCP, name) {
				l.MCP = append(l.MCP, name)
			}
		}
		runtime := r.layout.RuntimeDir(a.ID)
		l.Env = append(l.Env,
			"SOCRATES_TEST_ID="+a.Test, "SOCRATES_TEST_CELL="+a.CellID, fmt.Sprintf("SOCRATES_TEST_ATTEMPT=%d", a.N),
			"SOCRATES_TEST_EVIDENCE="+r.layout.EvidenceDir(a), "SOCRATES_TEST_RUNTIME="+runtime,
			"SOCRATES_TEST_VAULT="+filepath.Join(runtime, "vault"))
		for _, d := range a.Dims {
			l.Env = append(l.Env, "SOCRATES_TEST_DIM_"+envName(d.Name)+"="+d.ID)
		}
		for k, v := range a.Params {
			l.Env = append(l.Env, "SOCRATES_TEST_PARAM_"+envName(k)+"="+v)
		}
		for _, name := range a.Tools {
			if t := rs.plan.Tools[name]; t != nil {
				l.Path = append(l.Path, filepath.Join(t.Dir, t.Bin))
			}
		}
		if a.State == AttemptRunning || a.State == AttemptStarting {
			if err := r.writeAttemptVaultLocked(rs, a); err != nil {
				log.Printf("tests: %s: %v", a.ID, err)
			}
		}
	case testtools.KindReviewer, testtools.KindAnalyst:
		l.Instructions = rs.plan.overviewPrompt()
	default:
		return nil, fmt.Errorf("unknown test session kind %q", ref.Kind)
	}
	return l, nil
}

// MCPCell is the cell a test session works, as an MCP server built into
// Socrates sees it; nil for a session that works none (a reviewer).
func (r *Runner) MCPCell(chat string) *mcpserver.TestCell {
	r.mu.Lock()
	defer r.mu.Unlock()
	ref, ok := r.byChat[chat]
	if !ok || ref.Kind != testtools.KindCell {
		return nil
	}
	rs := r.runs[ref.Run]
	if rs == nil {
		return nil
	}
	a := rs.attempts[ref.ID]
	// An attempt that ended has no device to start, and no lease.
	if a == nil || a.State != AttemptRunning && a.State != AttemptStarting {
		return nil
	}
	c := &mcpserver.TestCell{Run: rs.run.ID, Test: a.Test, Cell: a.CellID, Attempt: a.ID, N: a.N, Repo: rs.run.Path,
		Dims: map[string]string{}, Params: map[string]string{}, Evidence: r.layout.EvidenceDir(a)}
	for _, d := range a.Dims {
		c.Dims[d.Name] = d.ID
	}
	for k, v := range a.Params {
		c.Params[k] = v
	}
	if pa := rs.plan.Areas[a.Area]; pa != nil {
		c.Prompted = slices.Clone(pa.MCP)
	}
	return c
}

// IsTestChat is whether a chat is one of the runner's sessions.
func (r *Runner) IsTestChat(chat string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.byChat[chat]
	return ok
}

// Observe takes what a test session's activity now is.
func (r *Runner) Observe(chat string, o Observation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ref, ok := r.byChat[chat]
	rs := r.runs[ref.Run]
	if !ok || rs == nil {
		return
	}
	if o.TurnRunning {
		r.turns[chat] = true
	}
	// A run being taken up again reconciles its sessions once it is.
	if rs.resuming {
		return
	}
	ended := false
	if !o.TurnRunning && o.Idle && r.turns[chat] {
		delete(r.turns, chat)
		ended = true
	}
	if o.Failed {
		if a := rs.attempts[ref.ID]; ref.Kind == testtools.KindCell && a != nil && a.State == AttemptRunning {
			r.finishLocked(rs, a, AttemptAborted, "not reached: the agent did not start: "+clip(o.Detail, 300))
		} else if h := rs.helper(ref.ID); ref.Kind != testtools.KindCell && h != nil && h.State == "running" {
			h.State, h.Error, h.EndedAt = "failed", "the agent did not start: "+clip(o.Detail, 300), r.now().UnixMilli()
			r.helperFailedLocked(rs, h)
			r.saveRunLocked(rs)
		}
	} else if ended {
		if n := r.turnEndedLocked(rs, ref); n != "" {
			r.spawnLocked(func() { r.nudge(chat, n) })
		}
	}
	r.publishLocked(rs)
	r.poke()
}

// turnEndedLocked is a session's turn that ended: a cell not ended yet is
// nudged once, then aborted; a helper is done, or nudged once, then let go.
// It returns the nudge to send.
func (r *Runner) turnEndedLocked(rs *runState, ref Ref) string {
	switch ref.Kind {
	case testtools.KindCell:
		a := rs.attempts[ref.ID]
		if a == nil || a.State != AttemptRunning {
			return ""
		}
		if a.Nudged < 1 {
			a.Nudged++
			r.saveAttemptLocked(a)
			return fmt.Sprintf("You ended your turn, but the cell is not finished: %s Judge the remaining steps with %s "+
				"and end with %s, or call %s if the cell cannot go on.", r.unjudgedLocked(a), testtools.Judge, testtools.End, testtools.Abort)
		}
		r.finishLocked(rs, a, AttemptAborted, "not reached: the agent ended its turn without ending the cell")
	case testtools.KindReviewer, testtools.KindAnalyst:
		if h := rs.helper(ref.ID); h != nil && h.State == "running" {
			return r.helperTurnEndedLocked(rs, h, ref.Kind)
		}
	}
	return ""
}

func (r *Runner) unjudgedLocked(a *Attempt) string {
	var open []string
	for _, s := range a.Steps {
		if s.Verdict == "" {
			open = append(open, fmt.Sprint(s.N))
		}
	}
	switch len(open) {
	case 0:
		return "every step is judged, but the cell was not ended."
	case 1:
		return "step " + open[0] + " is not judged."
	}
	return "steps " + strings.Join(open, ", ") + " are not judged."
}

func (rs *runState) helper(id string) *Helper {
	for _, h := range rs.run.Reviews {
		if h.ID == id {
			return h
		}
	}
	if h := rs.run.Analysis; h != nil && h.ID == id {
		return h
	}
	return nil
}

// ---------------------------------------------------------------- ending

// finishLocked ends an attempt: what its tools wrote is read, the steps not
// judged are blocked, its verdict is set, its end hooks run and its leases
// are released after them, a retry is queued where the rules say so, and a
// review where they say that.
func (r *Runner) finishLocked(rs *runState, a *Attempt, state, reason string) {
	if Final(a.State) {
		return
	}
	wasRunning := a.State == AttemptRunning || a.State == AttemptStarting
	dir := r.layout.EvidenceDir(a)
	// Without the secrets (a paused run stopped) what the tools wrote last
	// stays in the evidence folder only: read now, it would be stored with
	// the secrets in it.
	if !rs.unredactable() {
		if _, err := ingest(a, dir, rs.red); err != nil {
			log.Printf("tests: %s: evidence: %v", a.ID, err)
		}
		if links := readLinks(dir, rs.red); len(links) > 0 {
			a.Links = links
		}
	}
	if state != AttemptEnded {
		for i := range a.Steps {
			if a.Steps[i].Verdict == "" && state != AttemptStopped {
				a.Steps[i].Verdict, a.Steps[i].Observed = testtools.Blocked, reason
			}
		}
		a.EndReason = reason
	}
	a.State, a.EndedAt, a.Wait = state, r.now().UnixMilli(), ""
	var all bool
	a.Verdict, all = a.worst(false)
	// A cell stopped before every step was judged has no verdict: it was
	// not run, whatever its first steps showed.
	if state == AttemptStopped && !all {
		a.Verdict = ""
	}
	r.saveAttemptLocked(a)
	if wasRunning {
		rs.ending[a.ID] = true
		env := r.attemptEnvLocked(rs, a)
		r.spawnLocked(func() { r.runEndHooks(rs, a, env) })
	} else {
		_ = os.RemoveAll(r.layout.RuntimeDir(a.ID))
		r.releaseMCP(a)
	}
	if state == AttemptStopped {
		return
	}
	if r.retryLocked(rs, a, false) {
		return
	}
	r.queueReviewLocked(rs, a)
}

func (r *Runner) hasEndHooks(rs *runState, a *Attempt) bool {
	for _, name := range a.Tools {
		if t := rs.plan.Tools[name]; t != nil && strings.TrimSpace(t.End) != "" {
			return true
		}
	}
	return false
}

// attemptEnvLocked is the environment of an attempt's hooks: what its
// session had.
func (r *Runner) attemptEnvLocked(rs *runState, a *Attempt) []string {
	runtime := r.layout.RuntimeDir(a.ID)
	env := append(os.Environ(), "SOCRATES_TEST=1", "SOCRATES_TEST_RUN="+rs.run.ID, "SOCRATES_TEST_REPO="+rs.run.Path,
		"SOCRATES_TEST_ID="+a.Test, "SOCRATES_TEST_CELL="+a.CellID, fmt.Sprintf("SOCRATES_TEST_ATTEMPT=%d", a.N),
		"SOCRATES_TEST_EVIDENCE="+r.layout.EvidenceDir(a), "SOCRATES_TEST_RUNTIME="+runtime,
		"SOCRATES_TEST_VAULT="+filepath.Join(runtime, "vault"))
	for _, d := range a.Dims {
		env = append(env, "SOCRATES_TEST_DIM_"+envName(d.Name)+"="+d.ID)
	}
	for k, v := range a.Params {
		env = append(env, "SOCRATES_TEST_PARAM_"+envName(k)+"="+v)
	}
	return env
}

// runEndHooks runs an attempt's end hooks with its vault still there, then
// redacts what the tools left in its evidence, removes the vault and its
// runtime folder and releases its leases. Hooks cut short by the server's
// stop leave all of it as it is: the next start runs them again (resume).
func (r *Runner) runEndHooks(rs *runState, a *Attempt, env []string) {
	r.mu.Lock()
	var hooks []*planTool
	for _, name := range a.Tools {
		if t := rs.plan.Tools[name]; t != nil && strings.TrimSpace(t.End) != "" {
			hooks = append(hooks, t)
		}
	}
	red := rs.red
	dir := r.layout.EvidenceDir(a)
	chat := a.Chat
	r.mu.Unlock()
	var problems []string
	for _, t := range hooks {
		stdout, stderr, err := runCommand(r.ctx, t.Dir, t.End, env, hookTimeout, maxHookOutput)
		if r.ctx.Err() != nil {
			return
		}
		if err != nil {
			problems = append(problems, fmt.Sprintf("the end hook of %s failed: %v %s", t.Name, err, tail(stdout+stderr, 400)))
		}
	}
	// A device a built-in MCP server holds for the cell is closed (its links
	// land in the evidence), then its account is given back.
	if chat != "" {
		r.host.EndMCP(chat)
	}
	r.releaseMCP(a)
	redactEvidence(dir, red)
	_ = os.RemoveAll(r.layout.RuntimeDir(a.ID))
	r.mu.Lock()
	delete(rs.ending, a.ID)
	if links := readLinks(dir, red); len(links) > 0 {
		a.Links = links
		r.saveAttemptLocked(a)
	}
	if len(problems) > 0 {
		note := red.String(strings.Join(problems, "; "))
		if a.EndReason == "" {
			a.EndReason = note
		} else {
			a.EndReason += " (" + note + ")"
		}
		r.saveAttemptLocked(a)
	}
	if rs.deleting && len(rs.ending) == 0 && r.runs[rs.run.ID] == rs {
		chats := r.deleteLocked(rs)
		for _, c := range chats {
			r.spawnLocked(func() { r.host.RemoveSession(context.Background(), c) })
		}
	} else {
		r.publishLocked(rs)
	}
	r.mu.Unlock()
	r.poke()
}

// retryLocked queues another attempt of a cell whose attempt was blocked or
// failed to start (any attempt with force), while the run's attempts allow;
// true when it did.
func (r *Runner) retryLocked(rs *runState, a *Attempt, force bool) bool {
	if rs.run.State != RunRunning && rs.run.State != RunFinishing {
		return false
	}
	if !force && a.State != AttemptFailed && a.Final() != testtools.Blocked {
		return false
	}
	if a.N >= rs.run.Settings.Attempts || a.Retried != "" {
		return false
	}
	next := &Attempt{
		ID: newAttemptID(), Run: a.Run, Repo: a.Repo, Test: a.Test, Area: a.Area, Title: a.Title, Priority: a.Priority,
		Tags: a.Tags, Cell: a.Cell, CellID: a.CellID, N: a.N + 1, Dims: a.Dims, Params: a.Params, Tools: a.Tools,
		Exclusive: a.Exclusive, Evidence: a.Evidence, TimeoutMS: a.TimeoutMS, Context: a.Context,
		State: AttemptQueued, QueuedAt: r.now().UnixMilli(),
	}
	for _, s := range a.Steps {
		next.Steps = append(next.Steps, Step{N: s.N, Do: s.Do, Expect: s.Expect})
	}
	a.Retried = next.ID
	r.saveAttemptLocked(a)
	rs.attempts[next.ID] = next
	rs.run.Order = append(rs.run.Order, AttemptRef{ID: next.ID, Test: next.Test, Cell: next.Cell, N: next.N})
	r.saveAttemptLocked(next)
	if rs.run.State == RunFinishing {
		rs.run.State = RunRunning
	}
	r.saveRunLocked(rs)
	return true
}

// latestAttempts is the newest attempt of every cell of a run.
func latestAttempts(rs *runState) []*Attempt {
	latest := map[string]*Attempt{}
	var order []string
	for _, ref := range rs.run.Order {
		a := rs.attempts[ref.ID]
		if a == nil {
			continue
		}
		prev, ok := latest[a.CellID]
		if !ok {
			order = append(order, a.CellID)
		}
		if !ok || a.N > prev.N {
			latest[a.CellID] = a
		}
	}
	out := make([]*Attempt, 0, len(order))
	for _, id := range order {
		out = append(out, latest[id])
	}
	return out
}

// advanceLocked moves a run on once its cells are done: to its review and
// analysis, then to done.
func (r *Runner) advanceLocked(rs *runState) {
	run := rs.run
	if run.State != RunRunning && run.State != RunFinishing {
		return
	}
	for _, a := range rs.attempts {
		if !Final(a.State) || rs.ending[a.ID] {
			return
		}
	}
	if run.State == RunRunning {
		run.State = RunFinishing
		r.saveRunLocked(rs)
		r.publishLocked(rs)
	}
	// Reviews pending or running hold it.
	for _, a := range latestAttempts(rs) {
		if a.Review != nil && (a.Review.State == ReviewPending || a.Review.State == ReviewRunning) {
			return
		}
	}
	for _, h := range run.Reviews {
		if h.State == "running" {
			return
		}
	}
	if run.Settings.Analysis && run.Analysis == nil {
		return // startHelpersLocked starts it
	}
	if h := run.Analysis; h != nil && h.State == "running" {
		return
	}
	r.endRunLocked(rs, RunDone)
}

// endRunLocked ends a run: it is said, pushed, and the repository's next
// run starts; old runs beyond the retention go.
func (r *Runner) endRunLocked(rs *runState, state string) {
	run := rs.run
	if state == RunStopped {
		run.Stopped = true
	}
	if state == RunDone && run.Stopped {
		// A run that was stopped and taken up again (an analysis, a retry)
		// ends stopped again: it changes nothing in the bug list.
		state = RunStopped
	}
	run.State, run.EndedAt = state, r.now().UnixMilli()
	run.BugsPending = state == RunDone
	_ = os.RemoveAll(r.layout.RunRuntimeDir(run.ID))
	r.saveRunLocked(rs)
	if state == RunDone {
		// The repository's bug list learns what the run reported, and which
		// of its open bugs the run shows fixed; and of an earlier run that
		// it did not learn of (a failed write).
		r.mergePendingLocked(run.Repo)
	}
	r.publishLocked(rs)
	if state == RunDone && !run.Notified {
		run.Notified = true
		r.saveRunLocked(rs)
		counts := countsOf(rs)
		title := "Tests done"
		if r.repos[run.Repo] != nil {
			title += " · " + filepath.Base(r.repos[run.Repo].Path)
		}
		body := fmt.Sprintf("%d cells: %d pass, %d issue, %d fail, %d blocked", counts.Cells,
			counts.Verdicts[testtools.Pass], counts.Verdicts[testtools.Issue], counts.Verdicts[testtools.Fail], counts.Verdicts[testtools.Blocked])
		url := "/tests#repo=" + run.Repo + "&run=" + run.ID
		r.spawnLocked(func() { r.host.Notify(title, body, url) })
	}
	r.tellStarterLocked(rs) // tests-b2
	r.nextRunLocked(run.Repo)
	r.pruneLocked(run.Repo)
}
