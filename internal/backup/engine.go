package backup

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/paths"
	"github.com/saschazesiger/SocratesAgent/internal/s3"
)

// DefaultWarnAfter is how long without a successful backup before Status
// carries a warning.
const DefaultWarnAfter = 24 * time.Hour

// firstRunDelay is how soon after a start the first run is due when the last
// success is older than the interval: soon, but not in the middle of the
// start itself.
const firstRunDelay = time.Minute

var (
	// ErrBusy is a run asked for while one is running.
	ErrBusy = errors.New("a backup is already running")
	// ErrNotConfigured is a run asked for with no backup configured.
	ErrNotConfigured = errors.New("no backup is configured")
	// ErrNotInstalled is a run asked for on a data directory that holds no
	// installation yet: a new machine neither set up nor restored, or one
	// whose restore waits to be moved into place. Its snapshot would be an
	// empty one, newer than every real snapshot in the bucket and so the
	// one "latest" restores; a run waits until there is something to back
	// up, and is not a failure.
	ErrNotInstalled = errors.New("this data directory holds no installation yet, so there is nothing to back up")
	// ErrNoDatabase is a walk that did not take the database. A snapshot
	// without it restores no installation, and is not written.
	ErrNoDatabase = errors.New("the snapshot would not hold the database, so it was not written")
)

// Options configure an Engine.
type Options struct {
	DataDir string
	// Version is the Socrates build, written into every manifest.
	Version string
	// Exclude names folders inside the data directory that are not backed
	// up although nothing in paths.NotBackedUp says so: a workspace root
	// that lies inside it. It is asked at every run.
	Exclude func() []string
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// Iterations is the PBKDF2 count of a new backup's meta.json; zero is
	// vault.Iterations. Tests lower it.
	Iterations int
	// GCGrace is DefaultGCGrace unless set.
	GCGrace time.Duration
	// WarnAfter is DefaultWarnAfter unless set.
	WarnAfter time.Duration
	// Getenv reads the environment a configuration is migrated from; nil is
	// os.Getenv.
	Getenv func(string) string
}

func (o *Options) defaults() {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.GCGrace <= 0 {
		o.GCGrace = DefaultGCGrace
	}
	if o.WarnAfter <= 0 {
		o.WarnAfter = DefaultWarnAfter
	}
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
}

// Engine runs the backups of one data directory.
type Engine struct {
	opts Options

	// run is held for the whole of a run: one at a time.
	run sync.Mutex

	mu      sync.Mutex
	cfg     *Config
	repo    *repo
	repoFor string
	st      state
	started time.Time
	running *progress
	cancel  context.CancelFunc
	nextRun time.Time
	// others are the hosts besides this one that wrote a snapshot in the
	// last WarnAfter, as the last listing found them.
	others []string
	// listedFor is the backup whose chunks have been listed in this
	// process (its place and salt).
	listedFor string

	wake   chan struct{}
	stop   context.CancelFunc
	closed chan struct{}
}

// New loads the configuration - carrying the bucket mirror's over first, for
// a data directory that has one - and the state of the last runs. It does
// not touch the network.
func New(opts Options) (*Engine, error) {
	opts.defaults()
	if migrated, err := MigrateConfig(opts.DataDir, opts.Getenv); err != nil {
		log.Printf("backup: could not carry the bucket mirror's configuration over: %v", err)
	} else if migrated {
		log.Printf("backup: the bucket the mirror used is now the backup's, under the prefix %s", DefaultPrefix)
	}
	e := &Engine{
		opts:    opts,
		st:      loadState(opts.DataDir),
		started: opts.Now(),
		wake:    make(chan struct{}, 1),
	}
	cfg, err := LoadConfig(opts.DataDir)
	if err != nil {
		// A broken backup.json is backups off, said on the admin page,
		// where saving the form writes a good one.
		log.Printf("backup: %v", err)
		e.st.LastError, e.st.LastErrorAt = err.Error(), opts.Now().UnixMilli()
		return e, nil
	}
	// A backup.json on a data directory that holds no installation was
	// brought from another machine, to restore from: its host id is that
	// machine's. Whatever this one becomes - restored, or set up afresh - it
	// backs up as a machine of its own, and never into the other's
	// snapshots and their retention.
	if cfg != nil && cfg.HostID != "" {
		if ok, err := installed(opts.DataDir); err == nil && !ok {
			cfg.HostID = ""
		}
	}
	if cfg != nil && cfg.HostID == "" {
		cfg.HostID = NewHostID()
		if err := SaveConfig(opts.DataDir, *cfg); err != nil {
			return nil, err
		}
	}
	e.cfg = cfg
	return e, nil
}

func (e *Engine) now() time.Time { return e.opts.Now() }

// Config returns the configuration, or nil when backups are off.
func (e *Engine) Config() *Config {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cfg == nil {
		return nil
	}
	c := *e.cfg
	return &c
}

// Start runs the schedule until Close. The first run is due an interval
// after the last success, or a minute from now when that is already past.
func (e *Engine) Start(ctx context.Context) {
	ctx, e.stop = context.WithCancel(ctx)
	e.closed = make(chan struct{})
	go func() {
		defer close(e.closed)
		for {
			wait := e.schedule()
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-e.wake:
				timer.Stop()
				continue
			case <-timer.C:
			}
			if e.Config() == nil {
				continue
			}
			if _, err := e.Run(ctx); err != nil && !errors.Is(err, ErrBusy) && !errors.Is(err, ErrNotInstalled) && ctx.Err() == nil {
				log.Printf("backup: %v", err)
			}
		}
	}()
}

// schedule works out when the next run is due and how long that is from now.
func (e *Engine) schedule() time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	if e.cfg == nil {
		e.nextRun = time.Time{}
		return time.Hour
	}
	interval := e.cfg.Interval()
	last := e.lastRunLocked()
	next := last.Add(interval)
	if earliest := e.started.Add(firstRunDelay); last.IsZero() || next.Before(earliest) {
		next = earliest
	}
	if next.Before(now) {
		next = now.Add(interval)
	}
	e.nextRun = next
	return next.Sub(now)
}

// lastRunLocked is the last run that ended, well or not.
func (e *Engine) lastRunLocked() time.Time {
	var at int64
	if e.st.LastSuccess != nil {
		at = e.st.LastSuccess.At
	}
	at = max(at, e.st.CheckedAt, e.st.LastAttempt)
	if at == 0 {
		return time.Time{}
	}
	return time.UnixMilli(at)
}

func (e *Engine) poke() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// RunNow starts a run in the background and returns at once; Status follows
// it from the moment RunNow returns. It is ErrBusy while one runs and
// ErrNotConfigured without a backup.
func (e *Engine) RunNow(ctx context.Context) error {
	cfg := e.Config()
	if cfg == nil {
		return ErrNotConfigured
	}
	if !e.run.TryLock() {
		return ErrBusy
	}
	prog := e.begin()
	go func() {
		defer e.run.Unlock()
		if _, err := e.runLocked(ctx, *cfg, prog); err != nil {
			log.Printf("backup: %v", err)
		}
	}()
	return nil
}

// Run is one backup, now, on the caller's goroutine: a snapshot unless
// nothing changed, then pruning and garbage collection when they are due. It
// is ErrBusy while another runs.
func (e *Engine) Run(ctx context.Context) (*RunInfo, error) {
	cfg := e.Config()
	if cfg == nil {
		return nil, ErrNotConfigured
	}
	if !e.run.TryLock() {
		return nil, ErrBusy
	}
	defer e.run.Unlock()
	return e.runLocked(ctx, *cfg, e.begin())
}

// begin makes the progress of a run that is about to start, which Status
// reads from now on.
func (e *Engine) begin() *progress {
	prog := &progress{}
	prog.setPhase("connecting")
	e.mu.Lock()
	e.running = prog
	e.mu.Unlock()
	return prog
}

// runLocked is a run, with the run lock held. A data directory without an
// installation is ErrNotInstalled, and leaves the state as it was.
func (e *Engine) runLocked(ctx context.Context, cfg Config, prog *progress) (*RunInfo, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer func() {
		e.mu.Lock()
		e.running, e.cancel = nil, nil
		e.mu.Unlock()
		e.poke()
	}()
	ok, err := installed(e.opts.DataDir)
	if err == nil && !ok {
		return nil, ErrNotInstalled
	}
	e.mu.Lock()
	e.cancel = cancel
	e.st.LastAttempt = e.now().UnixMilli()
	e.mu.Unlock()

	var info *RunInfo
	if err == nil {
		info, err = e.backup(ctx, cfg, prog)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err != nil {
		e.st.LastError, e.st.LastErrorAt = Explain(err), e.now().UnixMilli()
	} else {
		e.st.LastError, e.st.LastErrorAt = "", 0
		if info.Unchanged {
			e.st.CheckedAt = e.now().UnixMilli()
		} else {
			e.st.LastSuccess = info
		}
	}
	if serr := e.st.save(e.opts.DataDir); serr != nil {
		log.Printf("backup: could not record the run: %v", serr)
	}
	return info, err
}

// repoOf opens the backup a configuration names, or hands back the one opened
// for it before: the passphrase is stretched once, not every ten minutes.
func (e *Engine) repoOf(ctx context.Context, cfg Config) (*repo, error) {
	key := cfg.Place() + "\x00" + cfg.AccessKeyID + "\x00" + cfg.SecretAccessKey + "\x00" + cfg.Passphrase +
		"\x00" + cfg.Region + fmt.Sprint(cfg.PathStyle)
	e.mu.Lock()
	if e.repo != nil && e.repoFor == key {
		r := e.repo
		e.mu.Unlock()
		return r, nil
	}
	e.mu.Unlock()
	r, err := openRepo(ctx, cfg, true, e.opts.Iterations)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	e.repo, e.repoFor = r, key
	e.mu.Unlock()
	return r, nil
}

// backup is the body of Run.
func (e *Engine) backup(ctx context.Context, cfg Config, prog *progress) (*RunInfo, error) {
	r, err := e.repoOf(ctx, cfg)
	if err != nil {
		return nil, err
	}
	idx := loadIndex(e.opts.DataDir, cfg.Place(), r.salt)
	e.mu.Lock()
	listed := e.listedFor == cfg.Place()+r.salt
	e.mu.Unlock()
	if !listed {
		// Once per start of Socrates, the bucket is asked which chunks it
		// has: an index that remembers a chunk somebody deleted would
		// otherwise refer to it for ever.
		prog.setPhase("listing")
		chunks, err := r.listChunks(ctx)
		if err != nil {
			return nil, err
		}
		idx.remote = make(map[string]bool, len(chunks))
		for name := range chunks {
			idx.remote[name] = true
		}
		e.mu.Lock()
		e.listedFor = cfg.Place() + r.salt
		e.mu.Unlock()
	}

	tmp, err := resetTmp(e.opts.DataDir)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	// The walk starts from where the data directory's path leads. A data
	// directory moved onto a volume of its own and left behind as a
	// symbolic link is still the whole installation, and a walk does not
	// descend into a root that is a link: it would back up nothing, and
	// report success.
	root, err := filepath.EvalSymlinks(e.opts.DataDir)
	if err != nil {
		return nil, err
	}
	layout := paths.New(root)
	var exclude []string
	if e.opts.Exclude != nil {
		exclude = excludedRels(paths.New(e.opts.DataDir), layout, e.opts.Exclude())
	}
	prog.setPhase("backing up")
	snap := &snapshotter{layout: layout, repo: r, idx: idx, exclude: exclude, tmp: tmp, prog: prog}
	walkErr := snap.walk(ctx)
	if walkErr != nil {
		// What did get uploaded is in the bucket, and the next run need
		// not send it again; the files it read are not recorded.
		_ = idx.save(e.opts.DataDir)
		return nil, walkErr
	}
	idx.Files = snap.files
	if !installation(snap.entries) {
		_ = idx.save(e.opts.DataDir)
		if slices.Contains(snap.skipped, paths.DB) {
			return nil, fmt.Errorf("%w: %s could not be copied", ErrNoDatabase, paths.DB)
		}
		return nil, ErrNoDatabase
	}

	hostname, _ := os.Hostname()
	e.mu.Lock()
	var last time.Time
	var prevID string
	if e.st.LastSuccess != nil {
		last, prevID = time.UnixMilli(e.st.LastSuccess.At), e.st.LastSuccess.ID
	}
	e.mu.Unlock()
	at := snapshotMoment(e.now(), last)
	m := &Manifest{
		Format: manifestFormat, ID: snapshotID(cfg.HostID, at), Host: cfg.HostID, Hostname: hostname,
		Version: e.opts.Version, CreatedAt: at.UnixMilli(), Entries: snap.entries,
	}
	info := &RunInfo{
		Uploaded: prog.uploaded.Load(), Chunks: int(prog.chunks.Load()),
		Skipped: snap.skipped, Kept: snap.kept,
	}
	if prev := cachedManifest(e.opts.DataDir, prevID); prev != nil && prev.Host == cfg.HostID && sameEntries(prev.Entries, m.Entries) {
		sum := prev.Summary()
		info.ID, info.At, info.Size, info.Files, info.Unchanged = prev.ID, e.now().UnixMilli(), sum.Size, sum.Files, true
	} else {
		prog.setPhase("writing the snapshot")
		if err := r.putManifest(ctx, m); err != nil {
			_ = idx.save(e.opts.DataDir)
			return nil, err
		}
		if err := keepManifest(e.opts.DataDir, m); err != nil {
			log.Printf("backup: could not keep a copy of the manifest: %v", err)
		}
		sum := m.Summary()
		info.ID, info.At, info.Size, info.Files = m.ID, m.CreatedAt, sum.Size, sum.Files
	}
	if err := idx.save(e.opts.DataDir); err != nil {
		return nil, err
	}

	if e.now().Sub(time.UnixMilli(idx.LastGC)) >= gcEvery {
		prog.setPhase("tidying up")
		done, err := e.prune(ctx, r, idx, cfg.HostID)
		if err != nil {
			// The snapshot is written; a collection that failed is tried
			// again after the next run and is not this run's failure.
			log.Printf("backup: pruning: %v", err)
		} else if done.Pruned > 0 || done.Deleted > 0 {
			log.Printf("backup: pruned %d snapshots and %d chunks", done.Pruned, done.Deleted)
		}
		if err := idx.save(e.opts.DataDir); err != nil {
			return nil, err
		}
	}
	if _, err := e.summaries(ctx, r, cfg.HostID); err != nil {
		log.Printf("backup: could not list the snapshots: %v", err)
	}
	return info, nil
}

// installed reports whether a data directory holds an installation to back
// up: one that is set up or restored (HoldsStore), and not about to be
// replaced by a restore that is staged.
func installed(dataDir string) (bool, error) {
	if RestorePending(dataDir) {
		return false, nil
	}
	holds, _, err := HoldsStore(dataDir)
	return holds, err
}

// excludedRels is each of dirs that lies inside the data directory, relative
// to it. A folder may be named by either spelling of the data directory's
// path - the configured one, which may run through a symbolic link, or the
// one it resolves to - or by a link of its own that leads inside.
func excludedRels(configured, resolved paths.Layout, dirs []string) []string {
	var out []string
	for _, p := range dirs {
		rel, ok := configured.Rel(p)
		if !ok {
			rel, ok = resolved.Rel(p)
		}
		if !ok {
			if real, err := filepath.EvalSymlinks(p); err == nil {
				rel, ok = resolved.Rel(real)
			}
		}
		if ok {
			out = append(out, rel)
		}
	}
	return out
}

// manifest is a snapshot's manifest, from the kept copy when there is one.
func (e *Engine) manifest(ctx context.Context, r *repo, id string) (*Manifest, error) {
	if m := cachedManifest(e.opts.DataDir, id); m != nil {
		return m, nil
	}
	m, err := r.getManifest(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := keepManifest(e.opts.DataDir, m); err != nil {
		log.Printf("backup: could not keep a copy of the manifest: %v", err)
	}
	return m, nil
}

// summaries lists every snapshot in the bucket, newest first, reading each
// manifest once (four at a time) and keeping it. host marks this machine's.
func (e *Engine) summaries(ctx context.Context, r *repo, host string) ([]Summary, error) {
	refs, err := r.listSnapshots(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Summary, len(refs))
	errs := make([]error, len(refs))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, ref := range refs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			m, err := e.manifest(ctx, r, ref.ID)
			if err != nil {
				out[i], errs[i] = Summary{ID: ref.ID, Host: ref.Host, At: ref.At.UnixMilli()}, err
				return
			}
			out[i] = m.Summary()
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	others := map[string]bool{}
	for i := range out {
		out[i].Own = host != "" && out[i].Host == host
		if !out[i].Own && host != "" && e.now().Sub(time.UnixMilli(out[i].At)) < e.opts.WarnAfter {
			others[out[i].Host] = true
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].At != out[j].At {
			return out[i].At > out[j].At
		}
		return out[i].ID > out[j].ID
	})
	if host != "" {
		list := make([]string, 0, len(others))
		for h := range others {
			list = append(list, h)
		}
		sort.Strings(list)
		e.mu.Lock()
		e.others = list
		e.mu.Unlock()
	}
	return out, nil
}

// Snapshots lists the snapshots in the configured bucket, newest first.
func (e *Engine) Snapshots(ctx context.Context) ([]Summary, error) {
	cfg := e.Config()
	if cfg == nil {
		return nil, ErrNotConfigured
	}
	r, err := e.repoOf(ctx, *cfg)
	if err != nil {
		return nil, err
	}
	return e.summaries(ctx, r, cfg.HostID)
}

// Close stops the schedule and makes a last backup, bounded by ctx: a run
// that is going on is waited for, and a new one starts unless the last ended
// a moment ago. What does not finish in time leaves no snapshot behind, and
// the next start backs it up.
func (e *Engine) Close(ctx context.Context) error {
	if e.stop != nil {
		e.stop()
		<-e.closed
	}
	cfg := e.Config()
	if cfg == nil {
		return nil
	}
	stop := context.AfterFunc(ctx, func() {
		e.mu.Lock()
		if e.cancel != nil {
			e.cancel()
		}
		e.mu.Unlock()
	})
	defer stop()
	waited := false
	for {
		if e.run.TryLock() {
			defer e.run.Unlock()
			// A run that was going on has just ended: that was the last
			// backup, and another straight after it would find nothing.
			if waited {
				return nil
			}
			_, err := e.runLocked(ctx, *cfg, e.begin())
			return err
		}
		waited = true
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// SetConfig proves a configuration against its bucket - credentials,
// passphrase, a backup there or room for a new one - and saves it. An empty
// secret key or passphrase keeps the one already saved, so that a form never
// has to show them.
func (e *Engine) SetConfig(ctx context.Context, next Config) error {
	next = e.withSecrets(next)
	next.Normalize()
	if err := next.Validate(); err != nil {
		return err
	}
	if _, err := openRepo(ctx, next, true, e.opts.Iterations); err != nil {
		return err
	}
	if err := SaveConfig(e.opts.DataDir, next); err != nil {
		return err
	}
	saved, err := LoadConfig(e.opts.DataDir)
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.cfg = saved
	e.started = e.now()
	e.mu.Unlock()
	e.poke()
	return nil
}

// withSecrets fills a form's empty secrets from the saved configuration, and
// keeps this machine's host id.
func (e *Engine) withSecrets(next Config) Config {
	cur := e.Config()
	if cur == nil {
		return next
	}
	if next.SecretAccessKey == "" {
		next.SecretAccessKey = cur.SecretAccessKey
	}
	if next.Passphrase == "" {
		next.Passphrase = cur.Passphrase
	}
	if next.IntervalMinutes == 0 {
		next.IntervalMinutes = cur.IntervalMinutes
	}
	next.HostID = cur.HostID
	return next
}

// TestResult is what "Test connection" found.
type TestResult struct {
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
	// Found says whether the place already holds a backup (that the
	// passphrase opens), or is empty and would get a new one.
	Found bool `json:"found"`
}

// Test tries a configuration without saving it: the bucket answers, the
// credentials are accepted, and the passphrase opens what is there. Empty
// secrets are the saved ones.
func (e *Engine) Test(ctx context.Context, cfg Config) TestResult {
	cfg = e.withSecrets(cfg)
	cfg.Normalize()
	start := time.Now()
	_, err := openRepo(ctx, cfg, false, 0)
	res := TestResult{LatencyMS: time.Since(start).Milliseconds()}
	switch {
	case err == nil:
		res.OK, res.Found = true, true
	case errors.Is(err, ErrNoBackup):
		res.OK = true
	default:
		res.Error = Explain(err)
	}
	return res
}

// Status is the backup as the admin page shows it.
type Status struct {
	Configured bool    `json:"configured"`
	Config     *Public `json:"config,omitempty"`
	Running    bool    `json:"running"`
	// Phase, Files, Bytes and Uploaded follow a run while it runs.
	Phase       string   `json:"phase,omitempty"`
	Files       int64    `json:"files,omitempty"`
	Bytes       int64    `json:"bytes,omitempty"`
	Uploaded    int64    `json:"uploaded,omitempty"`
	LastSuccess *RunInfo `json:"last_success,omitempty"`
	// CheckedAt is the last run that found nothing new to back up.
	CheckedAt   int64  `json:"checked_at,omitempty"`
	LastAttempt int64  `json:"last_attempt,omitempty"`
	LastError   string `json:"last_error,omitempty"`
	LastErrorAt int64  `json:"last_error_at,omitempty"`
	NextRun     int64  `json:"next_run,omitempty"`
	// Warning is set when no backup has succeeded for WarnAfter.
	Warning string `json:"warning,omitempty"`
	// OtherHosts wrote snapshots into the same bucket in the last
	// WarnAfter.
	OtherHosts []string `json:"other_hosts,omitempty"`
}

// Status says how the backup stands.
func (e *Engine) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	st := Status{
		Configured: e.cfg != nil, LastSuccess: e.st.LastSuccess, CheckedAt: e.st.CheckedAt,
		LastAttempt: e.st.LastAttempt, LastError: e.st.LastError, LastErrorAt: e.st.LastErrorAt,
		OtherHosts: e.others,
	}
	if e.cfg == nil {
		return st
	}
	pub := e.cfg.Public()
	st.Config = &pub
	if !e.nextRun.IsZero() {
		st.NextRun = e.nextRun.UnixMilli()
	}
	if e.running != nil {
		st.Running = true
		st.Phase = e.running.getPhase()
		st.Files, st.Bytes, st.Uploaded = e.running.files.Load(), e.running.bytes.Load(), e.running.uploaded.Load()
	}
	st.Warning = e.warningLocked()
	return st
}

// Warning is Status().Warning, for a page that only needs that.
func (e *Engine) Warning() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cfg == nil {
		return ""
	}
	return e.warningLocked()
}

func (e *Engine) warningLocked() string {
	var good int64
	if e.st.LastSuccess != nil {
		good = e.st.LastSuccess.At
	}
	good = max(good, e.st.CheckedAt)
	since := e.started
	if good > 0 {
		since = time.UnixMilli(good)
	}
	if e.now().Sub(since) < e.opts.WarnAfter {
		return ""
	}
	if good == 0 {
		return "No backup has succeeded since Socrates started " + ago(e.now().Sub(since)) + "."
	}
	return "The last successful backup was " + ago(e.now().Sub(since)) + "."
}

// ago says a duration the way a sentence does.
func ago(d time.Duration) string {
	switch h := int(d.Hours()); {
	case d < time.Minute:
		return "less than a minute ago"
	case h < 2:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	case h < 48:
		return fmt.Sprintf("%d hours ago", h)
	default:
		return fmt.Sprintf("%d days ago", h/24)
	}
}

// Explain is an error as a sentence a person can act on, with the technical
// rest after a colon.
func Explain(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrWrongPassphrase), errors.Is(err, ErrMetaMissing), errors.Is(err, ErrNoBackup),
		errors.Is(err, ErrNotEmpty), errors.Is(err, ErrBusy), errors.Is(err, ErrNotConfigured),
		errors.Is(err, ErrNotInstalled), errors.Is(err, ErrNoDatabase):
		return err.Error()
	case errors.Is(err, s3.ErrAccessDenied):
		return "the bucket refused these credentials: " + err.Error()
	case errors.Is(err, s3.ErrNoSuchBucket):
		return "there is no such bucket: " + err.Error()
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "the backup did not finish in time: " + err.Error()
	case errors.Is(err, ErrCorrupt):
		return "the backup is damaged: " + err.Error()
	}
	var s3err *s3.Error
	if errors.As(err, &s3err) {
		return "the bucket refused a request: " + err.Error()
	}
	return "the backup failed: " + err.Error()
}
