package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/mirror"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/web"
)

// Connect mode is the server Socrates runs while it has no bucket to keep its
// state in: no configuration yet (a fresh data directory), or a configured bucket that
// cannot be used right now - unreachable with no local copy to start from, in
// use by another machine, or refusing the passphrase.
//
// It serves the setup page, its assets, /api/health (200, because
// scripts/update.sh asks it of an installation nobody has connected yet), /api/state with "mode":"connect", and the
// three storage actions:
//
//	POST /api/storage/connect   connect a bucket (only while nothing is configured)
//	POST /api/storage/takeover  take a configured bucket over from another
//	                            Socrates, proved with the Socrates password
//	POST /api/storage/retry     try the configured bucket again now
//
// Everything else under /api is 503, and every other page is a redirect to
// /setup.
//
// It is unauthenticated, exactly like the first-run password page it comes
// before: on a fresh data directory there is no password yet to check against. That
// is only safe while nothing is configured and nothing is there to take: a
// data directory that already holds an installation with a password (an
// upgrade that has no bucket configured yet) connects only with that
// password, because connecting it uploads its sign-ins and secrets to the
// bucket named, or replaces its state with that bucket's. Once s3.json or the environment names a bucket, the
// page offers status, retry and takeover and nothing else: re-pointing a
// configured machine at another bucket would hand it to whoever reached the
// page first.
//
// A successful connect or takeover writes what it has to and then restarts
// the process (Restart: an exec of the same binary), so the normal startup
// path runs with the new configuration; the tmux server, which is a process of
// its own, keeps every running session through it.

// ConnectState is what the connect page is told about the wait.
type ConnectState struct {
	// Reason is "unconfigured", "unreachable", "leased", "wrong_passphrase",
	// "config" (s3.json or the environment is unusable) or "error".
	Reason string `json:"reason"`
	Error  string `json:"error,omitempty"`
	// Configured: a bucket is named, and the page must not offer another.
	Configured bool           `json:"configured"`
	Source     mirror.Source  `json:"source,omitempty"`
	Storage    *mirror.Public `json:"storage,omitempty"`
	// Lease is who holds the bucket, for "leased".
	Lease *mirror.LeaseStatus `json:"lease,omitempty"`
	// Retrying: an attempt is running right now. NextRetryAt is when the
	// next one is due, in ms, or 0.
	Retrying    bool  `json:"retrying"`
	NextRetryAt int64 `json:"next_retry_at,omitempty"`
	// PasswordRequired: this data directory already holds an installation
	// with a Socrates password, and connecting a bucket to it takes that
	// password (local_password). Filled in by handleState.
	PasswordRequired bool `json:"password_required,omitempty"`
	// Restarting: a connect or takeover has been committed and the process
	// is about to restart; nothing else is accepted.
	Restarting bool `json:"restarting,omitempty"`
}

// StateFor describes a failed start for the connect page.
func StateFor(cfg mirror.Config, src mirror.Source, err error) ConnectState {
	public := cfg.Public(src)
	st := ConnectState{Configured: true, Source: src, Storage: &public, Error: mirror.Explain(err)}
	var held *mirror.LeaseHeldError
	switch {
	case errors.As(err, &held):
		st.Reason = "leased"
		st.Lease = &mirror.LeaseStatus{
			Instance: held.Lease.Instance, Host: held.Lease.Host, Version: held.Lease.Version,
			AcquiredAt: held.Lease.AcquiredAt, HeartbeatAt: held.Lease.HeartbeatAt,
		}
	case errors.Is(err, mirror.ErrWrongPassphrase):
		st.Reason = "wrong_passphrase"
	case mirror.Transient(err):
		st.Reason = "unreachable"
	default:
		st.Reason = "error"
	}
	return st
}

// ConnectOptions are what connect mode needs from main.
type ConnectOptions struct {
	Store   *store.Store
	DataDir string
	// Mirror carries the lease identity (instance, host, version) the
	// connect attempt writes.
	Mirror mirror.Options
	// Config is the configuration in force, when there is one.
	Config mirror.Config
	// Restart replaces the process with a fresh start of the same binary.
	// It is called after the answer has been written.
	Restart func()
	// Retry asks the waiting loop in main to try the configuration now.
	Retry func()
}

// ConnectServer is connect mode.
type ConnectServer struct {
	opts ConnectOptions
	mux  *http.ServeMux

	mu    sync.Mutex
	state ConnectState
	// busy makes connecting one attempt at a time - the page's connect and
	// takeover, and main's waiting loop (BeginAttempt) - so that no two of
	// them take the lease, hydrate or write s3.json at once.
	busy bool
	// committed: a connect or takeover succeeded and the restart is under
	// way. It is final: nothing may start after it, or the process could
	// restart into a configuration other than the one the page was told
	// about, or the loop could start a server whose store the restart then
	// closes under it.
	committed bool
	// fails is the same brake on guessing as the login has, for the
	// passphrase and the takeover password.
	fails map[string]*attempt
}

// NewConnect builds connect mode in its initial state.
func NewConnect(opts ConnectOptions, state ConnectState) *ConnectServer {
	c := &ConnectServer{opts: opts, state: state, fails: map[string]*attempt{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /static/", http.StripPrefix("/static/", web.Static()).ServeHTTP)
	mux.HandleFunc("GET /favicon.png", web.Favicon)
	mux.HandleFunc("GET /apple-touch-icon.png", web.AppleTouchIcon)
	mux.HandleFunc("GET /apple-touch-icon-precomposed.png", web.AppleTouchIcon)
	mux.HandleFunc("GET /sw.js", web.ServiceWorker)
	mux.HandleFunc("GET /setup", func(w http.ResponseWriter, r *http.Request) { web.ServePage(w, r, "setup.html") })
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": Version, "mode": "connect"})
	})
	mux.HandleFunc("GET /api/state", c.handleState)
	mux.HandleFunc("POST /api/storage/connect", c.handleConnect)
	mux.HandleFunc("POST /api/storage/takeover", c.handleTakeover)
	mux.HandleFunc("POST /api/storage/retry", c.handleRetry)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": "Socrates is waiting for its storage bucket", "mode": "connect",
		})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/setup", http.StatusFound)
	})
	c.mux = mux
	return c
}

// Handler is connect mode's root handler.
func (c *ConnectServer) Handler() http.Handler { return c.mux }

// SetConfig replaces the configuration a takeover uses: main reads s3.json
// again before every attempt, so a file fixed by hand is picked up.
func (c *ConnectServer) SetConfig(cfg mirror.Config) {
	c.mu.Lock()
	c.opts.Config = cfg
	c.mu.Unlock()
}

// SetState replaces what the page is told, after each attempt of main's
// waiting loop.
func (c *ConnectServer) SetState(st ConnectState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.committed {
		// The restart is under way; the waiting loop has nothing to add.
		return
	}
	c.state = st
}

// State is what the page is told right now.
func (c *ConnectServer) State() ConnectState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

func (c *ConnectServer) handleState(w http.ResponseWriter, r *http.Request) {
	state := c.State()
	if !state.Configured {
		hash, _ := c.localPasswordHash()
		state.PasswordRequired = hash != ""
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mode":           "connect",
		"setup_required": false,
		"authenticated":  false,
		"version":        Version,
		"connect":        state,
	})
}

// begin claims the one connect attempt, or answers 409.
func (c *ConnectServer) begin(w http.ResponseWriter) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.committed {
		writeError(w, http.StatusConflict, restartingMsg)
		return false
	}
	if c.busy {
		writeError(w, http.StatusConflict, "a connection attempt is already running")
		return false
	}
	c.busy = true
	return true
}

func (c *ConnectServer) end() {
	c.mu.Lock()
	c.busy = false
	c.mu.Unlock()
}

// restartingMsg is the refusal of anything after a committed connect.
const restartingMsg = "storage is connected; Socrates is restarting"

// commit marks a connect or takeover as done: from here only the restart
// happens. It is called with the attempt still claimed.
func (c *ConnectServer) commit() {
	c.mu.Lock()
	c.committed = true
	c.state.Configured = true
	c.state.Restarting = true
	c.state.Retrying = false
	c.mu.Unlock()
}

// BeginAttempt claims the one attempt for main's waiting loop, and reports
// false while the page has one running (a takeover waiting out the old
// holder) or has committed one. An attempt that fails gives the claim back
// with EndAttempt; one that succeeds keeps it for good, so that a takeover
// arriving between the loop's success and the switch to the full server
// cannot restart the process under it.
func (c *ConnectServer) BeginAttempt() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.busy || c.committed {
		return false
	}
	c.busy = true
	return true
}

// EndAttempt gives back the claim of an attempt of the loop that failed.
func (c *ConnectServer) EndAttempt() { c.end() }

// committedNow reports whether a connect or takeover has been committed.
func (c *ConnectServer) committedNow() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.committed
}

// localPasswordHash is the Socrates password this data directory already
// has, or "".
func (c *ConnectServer) localPasswordHash() (string, error) {
	hash, err := c.opts.Store.GetKV(PasswordKey)
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	return strings.TrimSpace(hash), err
}

func (c *ConnectServer) throttle(ip string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if a := c.fails[ip]; a != nil && time.Now().Before(a.until) {
		return errors.New("too many attempts, try again in a little while")
	}
	return nil
}

func (c *ConnectServer) noteFail(ip string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	a := c.fails[ip]
	if a == nil {
		a = &attempt{}
		c.fails[ip] = a
	}
	a.count++
	if a.count >= 5 {
		a.until = time.Now().Add(min(time.Duration(a.count-4)*15*time.Second, 10*time.Minute))
	}
}

// connectBody is the storage step of the setup page.
type connectBody struct {
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region"`
	Bucket          string `json:"bucket"`
	Prefix          string `json:"prefix"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	PathStyle       *bool  `json:"path_style"`
	Passphrase      string `json:"passphrase"`
	// PassphraseConfirm is sent once the page has been told the bucket is
	// new; it has to match.
	PassphraseConfirm string `json:"passphrase_confirm"`
	// Resolve answers a conflict: "bucket" or "overwrite".
	Resolve string `json:"resolve"`
	// Password takes over a bucket another Socrates holds.
	Password string `json:"password"`
	// LocalPassword is the Socrates password of the installation this data
	// directory already holds, when it holds one. It is a field of its own:
	// Password is checked against the bucket's hash (a takeover), and the
	// two installations may have different passwords.
	LocalPassword string `json:"local_password"`
}

// attachTimeout bounds one connect: a takeover waits for the old holder's
// last uploads, and an overwrite uploads a whole installation.
const attachTimeout = 3 * time.Minute

func (c *ConnectServer) handleConnect(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross origin request rejected")
		return
	}
	if c.committedNow() {
		writeError(w, http.StatusConflict, restartingMsg)
		return
	}
	if c.State().Configured {
		writeError(w, http.StatusConflict, "storage is already configured for this Socrates; "+
			"it can only be retried or taken over here. To point it at another bucket, change s3.json or the SOCRATES_S3_* environment and restart.")
		return
	}
	ip := clientIP(r)
	if err := c.throttle(ip); err != nil {
		writeError(w, http.StatusTooManyRequests, err.Error())
		return
	}
	var body connectBody
	if !readJSON(w, r, &body) {
		return
	}
	// A data directory that already holds an installation with a password -
	// an existing Socrates upgraded to a build that needs a bucket, without
	// one configured yet - is not a fresh data directory. Connecting it is either
	// an upload of all of it (the password hash, live sign-ins, push keys)
	// to whatever bucket is named, or a restore over it from that bucket;
	// from an unauthenticated page that would hand the machine to whoever
	// reached it first. So it takes the password it already has, whatever
	// the bucket and the resolution.
	hash, err := c.localPasswordHash()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if hash != "" {
		if body.LocalPassword == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"password_required": true,
				"error":             "this Socrates already has a password; enter it to connect storage",
			})
			return
		}
		if !verifyPassword(hash, body.LocalPassword) {
			c.noteFail(ip)
			writeJSON(w, http.StatusForbidden, map[string]any{
				"password_required": true, "error": "wrong password",
			})
			return
		}
	}
	cfg := mirror.Config{
		Endpoint: body.Endpoint, Region: body.Region, Bucket: body.Bucket, Prefix: body.Prefix,
		AccessKeyID: body.AccessKeyID, SecretAccessKey: body.SecretAccessKey,
		PathStyle: body.PathStyle == nil || *body.PathStyle, Passphrase: body.Passphrase,
	}
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	confirmNew := body.PassphraseConfirm != ""
	if confirmNew && body.PassphraseConfirm != body.Passphrase {
		writeError(w, http.StatusBadRequest, "the two passphrases do not match")
		return
	}
	if confirmNew && len([]rune(body.Passphrase)) < 8 {
		writeError(w, http.StatusBadRequest, "the passphrase needs at least 8 characters")
		return
	}
	if body.Resolve != "" && body.Resolve != mirror.ResolveBucket && body.Resolve != mirror.ResolveOverwrite {
		writeError(w, http.StatusBadRequest, "resolve is either bucket or overwrite")
		return
	}
	if !c.begin(w) {
		return
	}
	defer c.end()

	ctx, cancel := context.WithTimeout(r.Context(), attachTimeout)
	defer cancel()
	if body.Resolve == mirror.ResolveBucket {
		// The restart that follows replaces this data directory's state
		// with the bucket's. A copy first, so that a wrong choice can be
		// undone by hand.
		if !c.backupLocal(w) {
			return
		}
	}
	res, err := mirror.Attach(ctx, c.opts.Store, cfg, c.opts.Mirror, mirror.AttachRequest{
		ConfirmNew: confirmNew, Resolve: body.Resolve,
		Password: body.Password, VerifyPassword: verifyPassword,
	})
	if c.answerAttach(w, ip, err) {
		return
	}
	if err := mirror.SaveConfig(c.opts.DataDir, cfg); err != nil {
		writeError(w, http.StatusInternalServerError, "could not save the storage configuration: "+err.Error())
		return
	}
	log.Printf("storage: connected to %s (created %v, overwrote %v, took over %v); restarting", cfg.Bucket, res.Created, res.Overwrote, res.TookOver)
	c.commit()
	c.restart(w, res)
}

// answerAttach writes the answer for a failed Attach and reports whether it
// did.
func (c *ConnectServer) answerAttach(w http.ResponseWriter, ip string, err error) bool {
	if err == nil {
		return false
	}
	var held *mirror.LeaseHeldError
	var conflict *mirror.ConflictError
	switch {
	case errors.Is(err, mirror.ErrMetaMissing):
		writeJSON(w, http.StatusConflict, map[string]any{"meta_missing": true, "error": mirror.Explain(err)})
	case errors.Is(err, mirror.ErrNewBucket):
		// Not a failure: the page asks for the passphrase a second time.
		writeJSON(w, http.StatusOK, map[string]any{"new_bucket": true})
	case errors.As(err, &conflict):
		writeJSON(w, http.StatusConflict, map[string]any{
			"conflict": true, "error": err.Error(),
			"bucket_sessions": conflict.BucketSessions, "local_sessions": conflict.LocalSessions,
		})
	case errors.As(err, &held):
		writeJSON(w, http.StatusConflict, map[string]any{
			"leased": true, "error": held.Error(),
			"lease": map[string]any{"host": held.Lease.Host, "version": held.Lease.Version,
				"heartbeat_at": held.Lease.HeartbeatAt, "acquired_at": held.Lease.AcquiredAt},
		})
	case errors.Is(err, mirror.ErrWrongPassword):
		c.noteFail(ip)
		// 403 rather than 401: the page is not signed out of anything.
		writeError(w, http.StatusForbidden, "wrong password")
	case errors.Is(err, mirror.ErrWrongPassphrase):
		c.noteFail(ip)
		writeError(w, http.StatusBadRequest, mirror.Explain(err))
	case mirror.Transient(err):
		writeError(w, http.StatusBadGateway, mirror.Explain(err))
	default:
		writeError(w, http.StatusBadRequest, mirror.Explain(err))
	}
	return true
}

func (c *ConnectServer) restart(w http.ResponseWriter, res mirror.AttachResult) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "restarting": true,
		"created": res.Created, "overwrote": res.Overwrote, "took_over": res.TookOver,
	})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	if c.opts.Restart != nil {
		go func() {
			// A moment for the answer to reach the browser before the
			// socket it travels on goes away with the process.
			time.Sleep(300 * time.Millisecond)
			c.opts.Restart()
		}()
	}
}

func (c *ConnectServer) handleTakeover(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross origin request rejected")
		return
	}
	state := c.State()
	if !state.Configured {
		writeError(w, http.StatusConflict, "no storage is configured yet; connect a bucket first")
		return
	}
	ip := clientIP(r)
	if err := c.throttle(ip); err != nil {
		writeError(w, http.StatusTooManyRequests, err.Error())
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Password) == "" {
		writeError(w, http.StatusBadRequest, "taking the bucket over needs the Socrates password")
		return
	}
	if !c.begin(w) {
		return
	}
	defer c.end()
	ctx, cancel := context.WithTimeout(r.Context(), attachTimeout)
	defer cancel()
	// Resolve "bucket": a configured server starting from this bucket would
	// take the bucket's state anyway, so the takeover never asks.
	c.mu.Lock()
	cfg := c.opts.Config
	c.mu.Unlock()
	if !c.backupLocal(w) {
		return
	}
	res, err := mirror.Attach(ctx, c.opts.Store, cfg, c.opts.Mirror, mirror.AttachRequest{
		Resolve: mirror.ResolveBucket, Password: body.Password, VerifyPassword: verifyPassword,
	})
	if c.answerAttach(w, ip, err) {
		return
	}
	log.Printf("storage: took the bucket over; restarting")
	c.commit()
	c.restart(w, res)
}

func (c *ConnectServer) handleRetry(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross origin request rejected")
		return
	}
	if c.opts.Retry != nil && c.State().Configured && !c.committedNow() {
		c.opts.Retry()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "connect": c.State()})
}

// backupLocal copies the database next to itself before a connect that lets
// the bucket's state replace this data directory's, and answers the request
// itself when that fails. A data directory with nothing in it has nothing to
// keep and gets no copy.
func (c *ConnectServer) backupLocal(w http.ResponseWriter) bool {
	rows, err := c.opts.Store.ListSessions()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return false
	}
	hash, err := c.localPasswordHash()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return false
	}
	if len(rows) == 0 && hash == "" {
		return true
	}
	dest := filepath.Join(c.opts.DataDir, "socrates.db.pre-connect.bak")
	if err := c.opts.Store.Backup(dest); err != nil {
		writeError(w, http.StatusInternalServerError, "could not copy the database before replacing it: "+err.Error())
		return false
	}
	log.Printf("storage: the database was copied to %s before the bucket's state replaces it", dest)
	return true
}
