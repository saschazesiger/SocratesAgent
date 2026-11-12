// Package cloudflare makes Socrates reachable from the internet. With an API
// token it creates a Cloudflare Tunnel, its DNS record and the Cloudflare
// Access applications, runs cloudflared, checks from outside that the address
// asks for a login, and checks the Access token of every request that comes
// through the tunnel: whoever Cloudflare Access let in is signed in to
// Socrates, without the password.
//
// Socrates keeps serving on its local address all the time; the tunnel
// reaches it through a Unix socket of its own in the data directory, so a
// request on the local address never counts as one from the tunnel.
package cloudflare

import (
	"context"
	"crypto/rsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// KV is where the state and the sealed secrets are kept. GetKV answers ""
// and no error for a key that is not there.
type KV interface {
	GetKV(key string) (string, error)
	SetKV(key, value string) error
	DeleteKV(key string) error
}

// Sealer encrypts the secrets with the data directory's key. object binds a
// sealed value to the key it is stored under.
type Sealer interface {
	Seal(object string, plain []byte) ([]byte, error)
	Open(object string, sealed []byte) ([]byte, error)
}

const (
	stateKey        = "cloudflare:state"
	secretAPIToken  = "cloudflare:api_token"
	secretTunnelTok = "cloudflare:tunnel_token"
	// recordComment marks the DNS record as ours, so a later Setup may
	// update it and Remove may delete it.
	recordComment = "Managed by Socrates (Cloudflare Tunnel)"
	appPrefix     = "Socrates"
)

// BypassPaths are the paths Cloudflare Access lets through without a login.
// They are the pages a chat shows in a sandboxed frame: requests from such a
// frame carry no cookie, so a login would leave them white. Each guards its
// files with a token of its own.
var BypassPaths = []string{"/docs/", "/artifact-kit/"}

// Bypassed reports whether a path is one of BypassPaths.
func Bypassed(path string) bool {
	for _, p := range BypassPaths {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// Permission names as the dashboard shows them; the interface lists them
// next to the token link.
const (
	permTunnel = "Account > Cloudflare Tunnel > Edit"
	permApps   = "Account > Access: Apps and Policies > Edit"
	permOrg    = "Account > Access: Organizations, Identity Providers, and Groups > Edit"
	permDNS    = "Zone > DNS > Edit"
	permZone   = "Zone > Zone > Read"
	permUser   = "User > User Details > Read"
)

// Permission is one line of the token's permission table.
type Permission struct {
	Scope    string `json:"scope"`
	Name     string `json:"name"`
	Level    string `json:"level"`
	Optional bool   `json:"optional,omitempty"`
}

// Permissions lists what the API token needs.
func Permissions() []Permission {
	return []Permission{
		{"Account", "Cloudflare Tunnel", "Edit", false},
		{"Account", "Access: Apps and Policies", "Edit", false},
		{"Account", "Access: Organizations, Identity Providers, and Groups", "Edit", false},
		{"Zone", "DNS", "Edit", false},
		{"Zone", "Zone", "Read", false},
		{"User", "User Details", "Read", true},
	}
}

// TokenURL opens the dashboard's "create user API token" form with the
// permissions filled in. The keys are the short permission group keys of the
// dashboard's template links; "argotunnel" (Cloudflare Tunnel) is not in
// Cloudflare's key table but is what the dashboard accepts. Unknown keys are
// dropped silently, which is why the interface also shows Permissions().
func TokenURL() string {
	type key struct {
		Key  string `json:"key"`
		Type string `json:"type"`
	}
	keys, _ := json.Marshal([]key{
		{"argotunnel", "edit"},
		{"access", "edit"},
		{"access_acct", "edit"},
		{"dns", "edit"},
		{"zone", "read"},
		{"user_details", "read"},
	})
	q := url.Values{}
	q.Set("permissionGroupKeys", string(keys))
	q.Set("accountId", "*")
	q.Set("zoneId", "all")
	q.Set("name", "Socrates")
	return "https://dash.cloudflare.com/profile/api-tokens?" + q.Encode()
}

// state is what Socrates remembers about its Cloudflare objects. IDs are
// written as soon as an object exists, so a Setup that stopped in the middle
// finds and reuses them.
type state struct {
	AccountID      string   `json:"account_id"`
	AccountName    string   `json:"account_name"`
	ZoneID         string   `json:"zone_id"`
	Zone           string   `json:"zone"`
	Hostname       string   `json:"hostname"`
	Emails         []string `json:"emails"`
	TeamDomain     string   `json:"team_domain"` // team.cloudflareaccess.com
	TunnelID       string   `json:"tunnel_id"`
	Origin         string   `json:"origin"` // what the tunnel's routing points at
	RecordID       string   `json:"record_id"`
	RecordZoneID   string   `json:"record_zone_id"`
	AllowPolicyID  string   `json:"allow_policy_id"`
	BypassPolicyID string   `json:"bypass_policy_id"`
	AppID          string   `json:"app_id"`
	AppAUD         string   `json:"app_aud"`
	BypassAppID    string   `json:"bypass_app_id"`
	// Ready is set once every object exists; only then runs cloudflared.
	Ready bool `json:"ready"`
}

// Options builds a Service.
type Options struct {
	KV     KV
	Sealer Sealer
	// Origin is what cloudflared sends the requests to, "unix:/path/to.sock".
	Origin string
	// BinDir is where Socrates keeps a cloudflared it downloaded itself.
	BinDir string
	Logf   func(string, ...any)
}

// Service is the Cloudflare tunnel of this Socrates.
type Service struct {
	kv     KV
	sealer Sealer
	origin string
	logf   func(string, ...any)

	// API is the base URL of the Cloudflare API.
	API string
	// Command is the cloudflared to run; when it is not found, the copy in
	// BinDir is used, and downloaded first if it is not there.
	Command   string
	installer *Installer
	// HTTP talks to the Cloudflare API and fetches the Access keys.
	HTTP *http.Client
	// Outside checks the public hostname; it must not follow redirects.
	Outside *http.Client
	// CertsURL is where the Access signing keys of a team domain are.
	CertsURL func(teamDomain string) string

	// Timings; tests shorten them.
	VerifyFor      time.Duration // how long Setup retries the check from outside
	VerifyPause    time.Duration // between two attempts
	ReverifyEvery  time.Duration // how often Run checks again
	ConnectFor     time.Duration // how long Setup waits for cloudflared to connect
	MinBackoff     time.Duration
	MaxBackoff     time.Duration
	ReadyPoll      time.Duration // how often the metrics endpoint is asked
	KeysMinRefresh time.Duration // the fastest the Access keys are fetched again
	KeysMaxAge     time.Duration // after this the keys are fetched again anyway
	now            func() time.Time

	op   sync.Mutex    // one Setup, SetEmails or Remove at a time
	wake chan struct{} // the configuration changed

	mu          sync.Mutex
	cur         state
	hasToken    bool
	running     bool
	connected   bool
	verified    bool
	downloading bool
	setupErr    string
	originErr   string // the routing could not follow a moved socket
	procErr     string
	verifyErr   string
	job         Job
	logs        *ring

	keyMu     sync.Mutex // serialises fetching of the Access keys
	keys      map[string]*rsa.PublicKey
	keysTeam  string
	keysAt    time.Time
	keysTryAt time.Time
}

// New returns the service with the stored state.
func New(o Options) *Service {
	logf := o.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	command := strings.TrimSpace(os.Getenv("SOCRATES_CLOUDFLARED"))
	if command == "" {
		command = "cloudflared"
	}
	s := &Service{
		kv: o.KV, sealer: o.Sealer, origin: o.Origin, logf: logf,
		API:       "https://api.cloudflare.com/client/v4",
		Command:   command,
		installer: NewInstaller(o.BinDir),
		HTTP:      &http.Client{Timeout: 30 * time.Second},
		Outside: &http.Client{
			Timeout:       15 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		CertsURL:       func(team string) string { return "https://" + team + "/cdn-cgi/access/certs" },
		VerifyFor:      2 * time.Minute,
		VerifyPause:    5 * time.Second,
		ReverifyEvery:  10 * time.Minute,
		ConnectFor:     90 * time.Second,
		MinBackoff:     time.Second,
		MaxBackoff:     time.Minute,
		ReadyPoll:      2 * time.Second,
		KeysMinRefresh: 30 * time.Second,
		KeysMaxAge:     time.Hour,
		now:            time.Now,
		wake:           make(chan struct{}, 1),
		logs:           newRing(200),
	}
	s.load()
	return s
}

// Origin is what the tunnel's routing points at.
func (s *Service) Origin() string { return s.origin }

// load reads the stored state into memory; Status must not touch the store.
func (s *Service) load() {
	var cur state
	raw, err := s.kv.GetKV(stateKey)
	if err != nil {
		s.logf("cloudflare: cannot read the stored tunnel settings: %v", err)
	} else if raw != "" {
		if err := json.Unmarshal([]byte(raw), &cur); err != nil {
			s.logf("cloudflare: the stored tunnel settings are damaged: %v", err)
		}
	}
	tok, _ := s.secret(secretAPIToken)
	s.mu.Lock()
	s.cur, s.hasToken = cur, tok != ""
	s.mu.Unlock()
}

// save stores the state and, if given, secrets.
func (s *Service) save(cur state, secrets map[string]string) error {
	for name, v := range secrets {
		sealed, err := s.sealer.Seal(name, []byte(v))
		if err != nil {
			return fmt.Errorf("cannot seal the %s: %w", name, err)
		}
		if err := s.kv.SetKV(name, hex.EncodeToString(sealed)); err != nil {
			return fmt.Errorf("cannot save the tunnel settings: %w", err)
		}
	}
	raw, err := json.Marshal(cur)
	if err != nil {
		return err
	}
	if err := s.kv.SetKV(stateKey, string(raw)); err != nil {
		return fmt.Errorf("cannot save the tunnel settings: %w", err)
	}
	s.mu.Lock()
	s.cur = cur
	if _, ok := secrets[secretAPIToken]; ok {
		s.hasToken = true
	}
	s.mu.Unlock()
	return nil
}

// secret is a stored secret, "" when there is none.
func (s *Service) secret(name string) (string, error) {
	raw, err := s.kv.GetKV(name)
	if err != nil || raw == "" {
		return "", err
	}
	sealed, err := hex.DecodeString(raw)
	if err != nil {
		return "", err
	}
	plain, err := s.sealer.Open(name, sealed)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// changed tells the supervisor to look at the configuration again.
func (s *Service) changed() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) log(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	s.mu.Lock()
	s.logs.add(line)
	s.mu.Unlock()
}

// Binary is the cloudflared Socrates runs.
type Binary struct {
	Installed  bool   `json:"installed"`
	Version    string `json:"version"`
	Path       string `json:"path"`
	Managed    bool   `json:"managed"` // the copy Socrates downloaded
	CanInstall bool   `json:"can_install"`
}

// Status is what the interface shows.
type Status struct {
	Configured  bool     `json:"configured"`
	Hostname    string   `json:"hostname"`
	PublicURL   string   `json:"public_url"`
	Emails      []string `json:"emails"`
	Account     string   `json:"account"`
	Zone        string   `json:"zone"`
	ZoneID      string   `json:"zone_id"`
	Running     bool     `json:"running"`
	Connected   bool     `json:"connected"`
	Verified    bool     `json:"verified"`
	Downloading bool     `json:"downloading"`
	HasToken    bool     `json:"has_token"`
	// Partial is a setup that stopped half way, or a removal that could not
	// delete everything: there are objects on Cloudflare, but no tunnel runs.
	Partial     bool     `json:"partial"`
	Error       string   `json:"error"`
	Job         Job      `json:"job"`
	Cloudflared Binary   `json:"cloudflared"`
	Logs        []string `json:"logs"`
}

// Status never blocks on the network.
func (s *Service) Status() Status {
	s.mu.Lock()
	st := Status{
		Configured:  s.cur.Ready,
		Hostname:    s.cur.Hostname,
		Emails:      append([]string{}, s.cur.Emails...),
		Account:     s.cur.AccountName,
		Zone:        s.cur.Zone,
		ZoneID:      s.cur.ZoneID,
		Running:     s.running,
		Connected:   s.connected,
		Verified:    s.verified,
		Downloading: s.downloading,
		HasToken:    s.hasToken,
		Job:         s.job,
		Logs:        s.logs.snapshot(),
	}
	st.Partial = !s.cur.Ready && (s.cur.TunnelID != "" || s.cur.RecordID != "" || s.cur.AppID != "" ||
		s.cur.BypassAppID != "" || s.cur.AllowPolicyID != "" || s.cur.BypassPolicyID != "")
	st.Job.Done = append([]string(nil), s.job.Done...)
	st.Job.Conflicts = append([]Conflict(nil), s.job.Conflicts...)
	switch {
	case s.setupErr != "":
		st.Error = s.setupErr
	case s.cur.Ready && s.originErr != "":
		st.Error = s.originErr
	case s.cur.Ready && s.procErr != "" && !s.connected:
		st.Error = s.procErr
	case s.cur.Ready && s.verifyErr != "":
		st.Error = s.verifyErr
	}
	s.mu.Unlock()
	if st.Hostname != "" {
		st.PublicURL = "https://" + st.Hostname
	}
	st.Cloudflared = s.binary()
	return st
}

// binary reports the cloudflared that would run.
func (s *Service) binary() Binary {
	b := Binary{CanInstall: Supported()}
	if path, err := exec.LookPath(s.Command); err == nil {
		b.Installed, b.Path = true, path
	} else if s.installer.Installed() {
		b.Installed, b.Path = true, s.installer.Path()
	}
	if b.Installed {
		_, b.Version = probe(b.Path)
		b.Managed = b.Path == s.installer.Path()
	}
	return b
}

// resolve returns the cloudflared to run, downloading it when there is none.
func (s *Service) resolve(ctx context.Context) (string, error) {
	if path, err := exec.LookPath(s.Command); err == nil {
		return path, nil
	}
	if s.installer.Installed() {
		return s.installer.Path(), nil
	}
	if !Supported() {
		_, err := assetFor(runtime.GOARCH)
		return "", err
	}
	s.mu.Lock()
	s.downloading = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.downloading = false
		s.mu.Unlock()
	}()
	s.log("cloudflared is not installed, downloading it")
	return s.installer.Ensure(ctx, func(line string) { s.log("%s", line) })
}

// Zone is a domain the API token may use.
type Zone struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	AccountID   string `json:"account_id"`
	AccountName string `json:"account_name"`
}

// TokenInfo is what a checked token may do.
type TokenInfo struct {
	Zones []Zone `json:"zones"`
	// User is the e-mail address of the Cloudflare user the token belongs
	// to, when the token may read it (User Details: Read).
	User string `json:"user"`
	// Stored is set when the check used the stored token.
	Stored bool `json:"stored"`
}

// Check validates a token and lists the zones it may use. An empty token
// checks the stored one.
func (s *Service) Check(ctx context.Context, token string) (TokenInfo, error) {
	token = strings.TrimSpace(token)
	info := TokenInfo{}
	if token == "" {
		stored, err := s.secret(secretAPIToken)
		if err != nil || stored == "" {
			return info, errors.New("paste the API token you created on Cloudflare")
		}
		token, info.Stored = stored, true
	}
	zones, err := list[zoneObj](ctx, s, token, "/zones", nil)
	if err != nil {
		if isDenied(err) {
			return info, fmt.Errorf("Cloudflare does not accept this API token. Check that you copied all of it, "+
				"that it is an API token (not the Global API Key) and that it is active and includes %q. Details: %w", permZone, err)
		}
		return info, err
	}
	if len(zones) == 0 {
		return info, errors.New("the token may not use any domain. When you create it, choose your domain " +
			"(or \"All zones\") under \"Zone Resources\"")
	}
	// Edit includes read, so a read of each kind tells whether a permission
	// is there at all. Organisations are checked in Setup: their endpoint
	// also fails while Zero Trust is not enabled, which needs another answer.
	accounts := map[string]bool{}
	for _, z := range zones {
		if accounts[z.Account.ID] {
			continue
		}
		accounts[z.Account.ID] = true
		one := url.Values{"per_page": {"1"}}
		if err := s.call(ctx, token, http.MethodGet, "/accounts/"+z.Account.ID+"/cfd_tunnel", one, nil, nil); err != nil {
			return info, missing(err, permTunnel)
		}
		if err := s.call(ctx, token, http.MethodGet, "/accounts/"+z.Account.ID+"/access/apps", one, nil, nil); err != nil && isDenied(err) {
			return info, missing(err, permApps)
		}
	}
	if err := s.call(ctx, token, http.MethodGet, "/zones/"+zones[0].ID+"/dns_records", url.Values{"per_page": {"1"}}, nil, nil); err != nil {
		return info, missing(err, permDNS)
	}
	for _, z := range zones {
		info.Zones = append(info.Zones, Zone{ID: z.ID, Name: z.Name, AccountID: z.Account.ID, AccountName: z.Account.Name})
	}
	sort.Slice(info.Zones, func(i, j int) bool { return info.Zones[i].Name < info.Zones[j].Name })
	// Optional: whose token it is. Without the permission the address stays
	// unknown and nothing else changes.
	var u userObj
	if err := s.call(ctx, token, http.MethodGet, "/user", nil, nil, &u); err == nil {
		info.User = strings.ToLower(strings.TrimSpace(u.Email))
	}
	return info, nil
}

// probeCache keeps the result of "cloudflared --version" for a while: the
// interface asks for the status regularly.
var probeCache sync.Map // path -> *probeResult

type probeResult struct {
	installed bool
	version   string
	at        time.Time
}

const probeTTL = 30 * time.Second

// invalidateProbe forgets a cached lookup, used right after an install.
func invalidateProbe(path string) { probeCache.Delete(path) }

// probe reports whether a cloudflared runs and which version it is.
func probe(path string) (bool, string) {
	if cached, ok := probeCache.Load(path); ok {
		if r := cached.(*probeResult); time.Since(r.at) < probeTTL {
			return r.installed, r.version
		}
	}
	r := &probeResult{at: time.Now()}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, path, "--version").CombinedOutput(); err == nil {
		r.installed = true
		r.version = strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	}
	probeCache.Store(path, r)
	return r.installed, r.version
}

// ring is a fixed size log tail.
type ring struct {
	items []string
	size  int
}

func newRing(size int) *ring { return &ring{size: size} }

func (r *ring) add(line string) {
	if len(line) > 500 {
		line = line[:500] + "…"
	}
	r.items = append(r.items, time.Now().Format("15:04:05")+"  "+line)
	if len(r.items) > r.size {
		r.items = r.items[len(r.items)-r.size:]
	}
}

func (r *ring) snapshot() []string {
	out := make([]string, len(r.items))
	copy(out, r.items)
	return out
}

// PublicURL is the tunnel's address, "" while there is no tunnel.
func (s *Service) PublicURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.cur.Ready || s.cur.Hostname == "" {
		return ""
	}
	return "https://" + s.cur.Hostname
}
