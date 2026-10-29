package browserstack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/mcpserver"
	"github.com/saschazesiger/SocratesAgent/internal/mcpserver/browserstack/driver"
)

// A device is what the qa daemon was: one BrowserStack session of one chat
// or test cell, held between the agent's calls, kept alive while it thinks
// and closed on stop, when the cell ends, or after 20 minutes without a
// call. Its calls run one at a time.

// The daemon's limits (config.ts).
const (
	// startWait is how long one start call waits before it answers "still
	// queued" while the device keeps starting.
	startWait = 8 * time.Minute
	// leasePoll is how often a start without a free account looks again.
	leasePoll = 5 * time.Second
	// keepAliveAfter is the silence after which a harmless command is sent,
	// so that thinking never ends a session (BrowserStack's idle timeout
	// is 300 s).
	keepAliveAfter = time.Minute
	// idleExit is how long a device may go without a call.
	idleExit = 20 * time.Minute
	// stopWait is how long closing a session may take.
	stopWait = 2 * time.Minute
	// logLinesMax is how many log lines are kept.
	logLinesMax = 200
)

type device struct {
	srv     *Server
	session string
	holder  string
	account string // the account's id
	user    string
	test    bool
	cell    string
	dev     driver.Device
	locale  string
	consent string
	repo    string
	logConf *RepoLogs

	evidence  *driver.Evidence
	cmd       chan struct{} // one call at a time
	ready     chan struct{} // closed when the start is over, either way
	cancel    context.CancelFunc
	closeOnce sync.Once
	// ownsLease: the account was leased by start, and goes back when the
	// device does (a chat's; a test cell's lease is the runner's).
	ownsLease bool
	stopOnce  sync.Once
	dropOnce  sync.Once

	mu       sync.Mutex
	state    string // starting, running, stopping, failed
	ds       *driver.Session
	err      error
	stopping chan struct{}
	stopText string
	last     time.Time
}

func (d *device) status() (string, time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ds != nil {
		return d.state, d.ds.StartedAt()
	}
	return d.state, time.Time{}
}

// ready text, as qa start printed it.
func (d *device) readyText() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	locale := d.locale
	if locale == "" {
		locale = "none"
	}
	return fmt.Sprintf("device ready: %s (%s), locale %s, consent %s, BrowserStack account %s, session %s",
		d.dev.Label, d.dev.Kind, locale, d.consent, d.user, d.ds.ID())
}

// setup is what a session's start resolves before it leases: the device,
// locale, consent and site, from the cell or from the call.
type setup struct {
	dev      driver.Device
	locale   string
	consent  string
	baseURL  string
	repo     string
	conf     *RepoConfig
	build    string
	name     string
	evidence string
}

func (s *Server) resolve(sess mcpserver.Session, a args) (*setup, error) {
	st := &setup{consent: "accept"}
	if t := sess.Test; t != nil {
		// The cell decides; the call may only say the same.
		id := t.Dims["device"]
		if id == "" {
			return nil, errors.New("this test cell has no device dimension")
		}
		if a.Device != "" && a.Device != id {
			return nil, fmt.Errorf("this cell runs on %s: start takes no device here", id)
		}
		st.locale = t.Dims["locale"]
		if c := t.Params["consent"]; c != "" {
			st.consent = c
		}
		st.baseURL = t.Params["base_url"]
		st.repo = t.Repo
		st.build, st.name, st.evidence = t.Run, t.Cell, t.Evidence
		dev, err := driver.DeviceByID(id)
		if err != nil {
			return nil, err
		}
		st.dev = dev
	} else {
		if a.Device == "" {
			return nil, errors.New("start needs a device: " + deviceIDs())
		}
		dev, err := driver.DeviceByID(a.Device)
		if err != nil {
			return nil, err
		}
		st.dev = dev
		st.locale = a.Locale
		if a.Consent != "" {
			st.consent = a.Consent
		}
		st.baseURL = a.BaseURL
		st.repo = repoRoot(sess.Workdir)
		st.build = "Socrates chats"
		st.name = sess.Title
		if st.name == "" {
			st.name = sess.ID
		}
		st.evidence = s.chatEvidence(sess.ID)
	}
	if !slices.Contains(consents, st.consent) {
		return nil, fmt.Errorf("consent %q: expected one of %s", st.consent, strings.Join(consents, ", "))
	}
	conf, err := LoadRepo(st.repo)
	if err != nil {
		return nil, err
	}
	st.conf = conf
	if st.baseURL == "" {
		st.baseURL = conf.BaseURL
	}
	st.baseURL = strings.TrimRight(st.baseURL, "/")
	if st.locale == "" && sess.Test == nil && len(conf.Locales) > 0 {
		st.locale = conf.Locales[0]
	}
	if st.locale != "" && len(conf.Locales) > 0 && !slices.Contains(conf.Locales, st.locale) {
		return nil, fmt.Errorf("locale %s: expected one of %s", st.locale, strings.Join(conf.Locales, ", "))
	}
	return st, nil
}

// start is qa start: it starts the session's device unless one runs, then
// waits for it at most startWait.
func (s *Server) start(ctx context.Context, sess mcpserver.Session, a args) mcpserver.Result {
	until := time.Now().Add(startWait)
	d := s.device(sess.ID)
	if d == nil {
		st, err := s.resolve(sess, a)
		if err != nil {
			return mcpserver.Error(err.Error())
		}
		// An account first: a chat waits here for one to be free; a cell
		// was given its own before its session started, and gives it back
		// through the runner. A lease taken here is the device's to give
		// back when it stops.
		owned := sess.Test == nil || s.pool.leaseOf(sess.Holder()) == ""
		var lease mcpserver.Lease
		for {
			var why string
			lease, why = s.pool.Lease(sess.Holder())
			if lease.Account != "" {
				break
			}
			if time.Now().After(until) {
				return mcpserver.Text("still queued: " + why + "; Socrates has not handed out an account yet. Call start again.")
			}
			select {
			case <-ctx.Done():
				return mcpserver.Error("withdrawn")
			case <-time.After(leasePoll):
			}
		}
		if d, err = s.launch(sess, st, lease, owned); err != nil {
			// Unless a start beside this one runs a device on it.
			if owned && s.device(sess.ID) == nil {
				s.pool.Release(sess.Holder())
			}
			return mcpserver.Error("the device did not start: " + err.Error())
		}
	}
	next := "next: open the first page (open)"
	if sess.Test != nil {
		next = "next: open the first page of the test (open)"
	}
	select {
	case <-d.ready:
	case <-ctx.Done():
		return mcpserver.Error("withdrawn; the device keeps starting")
	case <-time.After(time.Until(until)):
		return mcpserver.Text("still queued: BrowserStack has not handed out the device yet and Socrates keeps waiting. Call start again.")
	}
	d.mu.Lock()
	state, err := d.state, d.err
	d.mu.Unlock()
	switch state {
	case "running":
		return mcpserver.Text(d.readyText() + "\n" + next)
	case "stopping":
		return mcpserver.Error("the device session is closing (stop ran)")
	}
	return mcpserver.Error("the device did not start: " + errText(err))
}

func errText(err error) string {
	if err == nil {
		return "unknown error"
	}
	return err.Error()
}

// launch makes the device of a session and starts it in the background.
func (s *Server) launch(sess mcpserver.Session, st *setup, lease mcpserver.Lease, owned bool) (*device, error) {
	var acct *mcpserver.Account
	member := 0
	for i, a := range s.accounts() {
		if a.ID == lease.Account {
			acct, member = &a, i+1
			break
		}
	}
	if acct == nil {
		return nil, errors.New("the leased BrowserStack account is gone from Admin")
	}
	secrets, err := st.conf.readSecrets(context.Background(), st.repo)
	if err != nil {
		return nil, err
	}
	ev, err := driver.NewEvidence(st.evidence, Name)
	if err != nil {
		return nil, err
	}
	cfg := driver.Config{
		Device: st.dev, Locale: st.locale, Consent: st.consent, BaseURL: st.baseURL, Locales: st.conf.Locales,
		Account: account(*acct, member), Project: st.conf.Project, Build: st.build, Name: st.name,
		Secrets: secrets, TOTP: st.conf.TOTP, Evidence: ev,
	}
	if cfg.Project == "" {
		cfg.Project = "Socrates"
	}
	if m := st.conf.Mail; m != nil && strings.TrimSpace(m.Command) != "" {
		cfg.Mail = repoMail{root: st.repo, conf: m}
		cfg.Address = m.Address
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := &device{
		srv: s, session: sess.ID, holder: sess.Holder(), account: acct.ID, user: acct.Values["user"],
		test: sess.Test != nil, ownsLease: owned, dev: st.dev, locale: st.locale, consent: st.consent, repo: st.repo, logConf: st.conf.Logs,
		evidence: ev, cmd: make(chan struct{}, 1), ready: make(chan struct{}), cancel: cancel,
		state: "starting", stopping: make(chan struct{}), last: time.Now(),
	}
	if sess.Test != nil {
		d.cell = sess.Test.Cell
	}
	s.mu.Lock()
	if have := s.devices[sess.ID]; have != nil {
		// Two starts at once: the first one's device.
		s.mu.Unlock()
		cancel()
		return have, nil
	}
	s.devices[sess.ID] = d
	s.mu.Unlock()
	go d.run(ctx, cfg)
	return d, nil
}

// run starts the session, then keeps it alive until it stops.
func (d *device) run(ctx context.Context, cfg driver.Config) {
	ds, err := driver.Start(ctx, cfg)
	d.mu.Lock()
	if err != nil {
		if d.state != "stopping" {
			d.state = "failed"
		}
		d.err = err
		d.mu.Unlock()
		close(d.ready)
		d.srv.drop(d)
		return
	}
	d.ds = ds
	if d.state == "stopping" {
		// stop came while BrowserStack was still queueing: close what it
		// handed over after all.
		d.mu.Unlock()
		close(d.ready)
		d.close(context.Background(), ds)
		return
	}
	d.state, d.last = "running", time.Now()
	d.mu.Unlock()
	d.srv.remember(d)
	close(d.ready)
	tick := time.NewTicker(keepAliveAfter / 2)
	defer tick.Stop()
	for {
		select {
		case <-d.stopping:
			return
		case <-tick.C:
		}
		d.mu.Lock()
		idle := time.Since(d.last)
		d.mu.Unlock()
		switch {
		case idle > idleExit:
			go d.srv.stop(context.Background(), d.session, fmt.Sprintf("no command for %d min", int(idle.Minutes())))
			return
		case idle > keepAliveAfter:
			select {
			case d.cmd <- struct{}{}:
				kctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				ds.KeepAlive(kctx)
				cancel()
				<-d.cmd
			default:
			}
		}
	}
}

// command runs one call on the running device, after the one before it.
func (d *device) command(ctx context.Context, f func(*driver.Session) mcpserver.Result) mcpserver.Result {
	select {
	case d.cmd <- struct{}{}:
	case <-ctx.Done():
		return mcpserver.Error("withdrawn")
	}
	defer func() { <-d.cmd }()
	d.mu.Lock()
	state, ds := d.state, d.ds
	d.last = time.Now()
	d.mu.Unlock()
	switch state {
	case "running":
	case "stopping":
		return mcpserver.Error("the device session is closing")
	case "failed":
		return mcpserver.Error("the device did not start: " + errText(d.err) + "; call start again")
	default:
		return mcpserver.Error("the device is still starting: call start until it says the device is ready")
	}
	res := f(ds)
	d.mu.Lock()
	d.last = time.Now()
	d.mu.Unlock()
	return res
}

// stop is qa stop: it closes the session's device, whatever state it is in,
// writes its links into the evidence and gives back a chat's account.
func (s *Server) stop(ctx context.Context, session, reason string) string {
	d := s.device(session)
	if d == nil {
		return "no device session"
	}
	d.mu.Lock()
	if d.state == "stopping" {
		d.mu.Unlock()
		<-d.stoppedOrTimeout()
		d.mu.Lock()
		text := d.stopText
		d.mu.Unlock()
		if text == "" {
			text = "the device session is closing"
		}
		return text
	}
	d.state = "stopping"
	d.stopOnce.Do(func() { close(d.stopping) })
	ds := d.ds
	d.mu.Unlock()
	d.cancel()
	var text string
	if ds == nil {
		// Still queued at BrowserStack: what it hands over is closed by run.
		select {
		case <-d.ready:
			d.mu.Lock()
			ds = d.ds
			d.mu.Unlock()
		case <-time.After(5 * time.Second):
		}
	}
	if ds == nil {
		text = fmt.Sprintf("stopped before the device started (%s)", reason)
	} else {
		d.close(ctx, ds)
		text = fmt.Sprintf("stopped: the BrowserStack session %s is closed (%s); its video and logs are in links.json", ds.ID(), reason)
	}
	d.mu.Lock()
	d.stopText = text
	d.mu.Unlock()
	s.drop(d)
	return text
}

// close quits the BrowserStack session once and writes its links into the
// evidence.
func (d *device) close(ctx context.Context, ds *driver.Session) {
	d.closeOnce.Do(func() {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopWait)
		defer cancel()
		_ = d.evidence.WriteLinks(ds.Stop(sctx))
	})
}

// stoppedOrTimeout is closed once the device is gone from the server, or
// after stopWait.
func (d *device) stoppedOrTimeout() <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		until := time.Now().Add(stopWait)
		for time.Now().Before(until) {
			if d.srv.device(d.session) != d {
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
	}()
	return done
}

// drop takes a device off the server, gives back a chat's account and
// forgets the session's facts.
func (s *Server) drop(d *device) {
	s.mu.Lock()
	if s.devices[d.session] == d {
		delete(s.devices, d.session)
	}
	s.mu.Unlock()
	if d.ownsLease {
		d.dropOnce.Do(func() { s.pool.Release(d.holder) })
	}
	s.forget(d.session)
}

// logs is qa logs: the application's warnings and errors since the device
// started, kept in the evidence.
func (d *device) logs(ctx context.Context) mcpserver.Result {
	d.mu.Lock()
	d.last = time.Now()
	d.mu.Unlock()
	if d.logConf == nil || strings.TrimSpace(d.logConf.Command) == "" {
		return mcpserver.Error("this repository names no logs (" + RepoFile + ", logs)")
	}
	_, since := d.status()
	if since.IsZero() {
		return mcpserver.Error("no device session yet: the window starts when start reports the device ready")
	}
	out, err := run(ctx, d.repo, d.logConf.Command, "--from", since.UTC().Format(time.RFC3339), "--to", time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return mcpserver.Error(err.Error())
	}
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, driver.RedactURL(line))
		}
	}
	if len(lines) > logLinesMax {
		lines = lines[:logLinesMax]
	}
	name := d.logConf.Name
	if name == "" {
		name = "app"
	}
	file, err := d.evidence.WriteLog(name, lines)
	if err != nil {
		return mcpserver.Error(err.Error())
	}
	var b strings.Builder
	if d.logConf.Note != "" {
		b.WriteString("note: " + d.logConf.Note + "\n")
	}
	if len(lines) == 0 {
		b.WriteString("no WARN or ERROR lines since the device started\n")
	} else {
		b.WriteString(strings.Join(lines, "\n") + "\n")
	}
	b.WriteString("kept in " + file)
	return mcpserver.Text(b.String())
}

// ------------------------------------------------------------- orphans

// The sessions open now are written down, so that a server that stopped
// without closing them (an update, a crash) closes them when it starts
// again, as `qa stop` closed what a dead daemon left.

type openSession struct {
	Session string `json:"session"`
	Account string `json:"account"`
}

func (s *Server) openFile() string {
	return filepath.Join(s.deps.DataDir, "run", "mcp", Name, "sessions.json")
}

var openMu sync.Mutex

func (s *Server) editOpen(f func(map[string]openSession)) {
	if s.deps.DataDir == "" {
		return
	}
	openMu.Lock()
	defer openMu.Unlock()
	open := map[string]openSession{}
	if raw, err := os.ReadFile(s.openFile()); err == nil {
		_ = json.Unmarshal(raw, &open)
	}
	f(open)
	_ = os.MkdirAll(filepath.Dir(s.openFile()), 0o700)
	raw, _ := json.Marshal(open)
	tmp := s.openFile() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err == nil {
		_ = os.Rename(tmp, s.openFile())
	}
}

func (s *Server) remember(d *device) {
	s.editOpen(func(open map[string]openSession) {
		open[d.session] = openSession{Session: d.ds.ID(), Account: d.account}
	})
}

func (s *Server) forget(session string) {
	s.editOpen(func(open map[string]openSession) { delete(open, session) })
}

func (s *Server) closeOrphans() {
	if _, err := os.Stat(s.openFile()); err != nil {
		return
	}
	var left map[string]openSession
	s.editOpen(func(open map[string]openSession) {
		left = map[string]openSession{}
		for k, v := range open {
			left[k] = v
			delete(open, k)
		}
	})
	for _, o := range left {
		for _, a := range s.accounts() {
			if a.ID != o.Account {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), stopWait)
			if err := driver.QuitSession(ctx, account(a, 0), o.Session); err == nil {
				log.Printf("browserstack: closed the session %s a server before this one left open", o.Session)
			}
			cancel()
		}
	}
}
