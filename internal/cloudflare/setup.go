package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"
)

// sessionDuration is how long an Access login lasts: as long as a Socrates
// login, so the app on a phone does not ask for a code every day.
const sessionDuration = "720h"

// jobTimeout bounds a setup or a removal that hangs on the network.
const jobTimeout = 10 * time.Minute

// errZeroTrust is shown when the account has no Zero Trust organisation.
var errZeroTrust = errors.New("Cloudflare Zero Trust is not enabled on this Cloudflare account yet, and Socrates needs it " +
	"for the login (Cloudflare Access). Open https://one.dash.cloudflare.com/ once, pick this account, choose a team " +
	"name and the Free plan (no payment details are needed for it), then come back and run the setup again")

// Job is a setup or a removal running in the background: both take longer
// than a request may, and a removal ends the connection of a browser that
// came through the tunnel.
type Job struct {
	Kind      string     `json:"kind"` // "setup" or "remove"
	Running   bool       `json:"running"`
	Step      string     `json:"step"`
	Done      []string   `json:"done"`
	Error     string     `json:"error"`
	Conflicts []Conflict `json:"conflicts,omitempty"`
	OK        bool       `json:"ok"`
}

// Conflict is an object on Cloudflare that Socrates did not create and
// would replace.
type Conflict struct {
	Kind   string `json:"kind"` // "dns" or "access"
	Name   string `json:"name"`
	Detail string `json:"detail"`
}

// ConflictError stops a setup until the person agrees to replace what is
// there.
type ConflictError struct{ Conflicts []Conflict }

func (e *ConflictError) Error() string {
	var parts []string
	for _, c := range e.Conflicts {
		parts = append(parts, c.Detail)
	}
	return strings.Join(parts, " ") + " Choose another subdomain, or let Socrates replace it."
}

// verifyError is a failed check from outside. The supervisor checks again
// every few minutes, so it is not kept as the setup's own error.
type verifyError struct{ error }

func (e verifyError) Unwrap() error { return e.error }

// SetupRequest is what the person entered.
type SetupRequest struct {
	APIToken string   `json:"api_token"` // empty: the stored one
	ZoneID   string   `json:"zone_id"`
	Hostname string   `json:"hostname"`
	Emails   []string `json:"emails"`
	// Replace lets the setup delete a DNS record or an Access application
	// of the same name that Socrates did not create.
	Replace bool `json:"replace"`
}

func normHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.TrimPrefix(strings.TrimPrefix(h, "https://"), "http://")
	if i := strings.IndexAny(h, "/?#"); i >= 0 {
		h = h[:i]
	}
	return strings.TrimSuffix(h, ".")
}

// NormEmails checks the addresses and drops duplicates.
func NormEmails(in []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, e := range in {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" || seen[e] {
			continue
		}
		a, err := mail.ParseAddress(e)
		if err != nil || a.Address != e || !strings.Contains(e[strings.IndexByte(e, '@')+1:], ".") {
			return nil, fmt.Errorf("%q is not an e-mail address. Enter one address per line, like name@example.com", e)
		}
		seen[e] = true
		out = append(out, e)
	}
	if len(out) == 0 {
		return nil, errors.New("enter at least one e-mail address that may sign in")
	}
	return out, nil
}

func tunnelName(host string) string { return "socrates-" + host }

// beginJob claims the one job slot.
func (s *Service) beginJob(kind string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.job.Running {
		if s.job.Kind == "remove" {
			return errors.New("the tunnel is being removed; wait until that is finished")
		}
		return errors.New("the setup is already running; wait until it is finished")
	}
	s.job = Job{Kind: kind, Running: true}
	return nil
}

func (s *Service) progress(step string) {
	s.mu.Lock()
	if s.job.Step != "" {
		s.job.Done = append(s.job.Done, s.job.Step)
	}
	s.job.Step = step
	s.logs.add(step)
	s.mu.Unlock()
}

func (s *Service) endJob(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.job.Running = false
	if err == nil {
		if s.job.Step != "" {
			s.job.Done = append(s.job.Done, s.job.Step)
		}
		s.job.Step, s.job.OK = "", true
		return
	}
	s.job.Error = err.Error()
	var ce *ConflictError
	if errors.As(err, &ce) {
		s.job.Conflicts = ce.Conflicts
	}
	s.logs.add("failed: " + err.Error())
}

// StartSetup checks the request and runs the setup in the background; the
// progress is in Status().Job.
func (s *Service) StartSetup(req SetupRequest) error {
	req.Hostname = normHost(req.Hostname)
	emails, err := NormEmails(req.Emails)
	if err != nil {
		return err
	}
	req.Emails = emails
	if req.ZoneID == "" || req.Hostname == "" {
		return errors.New("choose a domain and enter a subdomain")
	}
	req.APIToken = strings.TrimSpace(req.APIToken)
	if req.APIToken == "" {
		if tok, _ := s.secret(secretAPIToken); tok == "" {
			return errors.New("paste the Cloudflare API token")
		}
	}
	if err := s.beginJob("setup"); err != nil {
		return err
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), jobTimeout)
		defer cancel()
		err := s.Setup(ctx, req)
		s.endJob(err)
	}()
	return nil
}

// Setup creates or repairs tunnel, DNS record, routing and the Access
// applications, starts cloudflared and checks from outside. Every step finds
// what an earlier run made, so running it again repairs or updates.
func (s *Service) Setup(ctx context.Context, req SetupRequest) (err error) {
	s.op.Lock()
	defer s.op.Unlock()
	s.mu.Lock()
	s.setupErr, s.verified, s.verifyErr = "", false, ""
	s.mu.Unlock()
	defer func() {
		var ve verifyError
		if err != nil && !errors.As(err, &ve) {
			s.mu.Lock()
			s.setupErr = err.Error()
			s.mu.Unlock()
		}
	}()

	token := strings.TrimSpace(req.APIToken)
	if token == "" {
		if token, _ = s.secret(secretAPIToken); token == "" {
			return errors.New("paste the Cloudflare API token")
		}
	}
	host := normHost(req.Hostname)
	emails, err := NormEmails(req.Emails)
	if err != nil {
		return err
	}
	if req.ZoneID == "" || host == "" {
		return errors.New("choose a domain and enter a subdomain")
	}
	if !strings.HasPrefix(s.origin, "unix:") && !strings.HasPrefix(s.origin, "http") {
		return errors.New("Socrates has no address for the tunnel to reach it: " + s.origin)
	}

	s.mu.Lock()
	cur := s.cur
	s.mu.Unlock()

	s.progress("Checking the API token and the domain")
	var zone zoneObj
	if err := s.call(ctx, token, http.MethodGet, "/zones/"+url.PathEscape(req.ZoneID), nil, nil, &zone); err != nil {
		if isDenied(err) || isNotFound(err) {
			return fmt.Errorf("the API token cannot read the chosen domain. It needs %q for that domain. Details: %w", permZone, err)
		}
		return err
	}
	if host != zone.Name && !strings.HasSuffix(host, "."+zone.Name) {
		return fmt.Errorf("%s is not part of the domain %s. Enter a name like socrates.%s", host, zone.Name, zone.Name)
	}
	if cur.AccountID != "" && cur.AccountID != zone.Account.ID {
		// Another account: the stored IDs mean nothing there.
		s.log("the domain is in another Cloudflare account than before; objects in the old account are left as they are")
		cur = state{}
	}
	cur.AccountID, cur.AccountName, cur.ZoneID, cur.Zone = zone.Account.ID, zone.Account.Name, zone.ID, zone.Name
	acct := "/accounts/" + cur.AccountID

	s.progress("Getting cloudflared")
	if _, err := s.resolve(ctx); err != nil {
		return fmt.Errorf("cloudflared is needed for the tunnel and could not be found or downloaded: %w", err)
	}
	if err := s.save(cur, map[string]string{secretAPIToken: token}); err != nil {
		return err
	}

	s.progress("Checking Cloudflare Zero Trust")
	team, err := s.teamDomain(ctx, token, acct)
	if err != nil {
		return err
	}
	cur.TeamDomain = team

	s.progress("Checking " + host)
	conflicts, err := s.conflicts(ctx, token, cur, host)
	if err != nil {
		return err
	}
	if len(conflicts) > 0 && !req.Replace {
		return &ConflictError{Conflicts: conflicts}
	}
	if err := s.ensureOTP(ctx, token, acct); err != nil {
		return err
	}

	s.progress("Creating the tunnel")
	tunnelID, err := s.ensureTunnel(ctx, token, acct, cur.TunnelID, host)
	if err != nil {
		return err
	}
	cur.TunnelID = tunnelID
	if err := s.save(cur, nil); err != nil {
		return err
	}
	var tunnelTok string
	if err := s.call(ctx, token, http.MethodGet, acct+"/cfd_tunnel/"+tunnelID+"/token", nil, nil, &tunnelTok); err != nil {
		return missing(err, permTunnel)
	}
	if tunnelTok == "" {
		return errors.New("Cloudflare did not return a token for the tunnel. Run the setup again")
	}
	if err := s.save(cur, map[string]string{secretTunnelTok: tunnelTok}); err != nil {
		return err
	}

	s.progress("Routing " + host + " to Socrates")
	if err := s.route(ctx, token, acct, tunnelID, host); err != nil {
		return err
	}
	cur.Origin = s.origin
	if err := s.save(cur, nil); err != nil {
		return err
	}

	s.progress("Pointing " + host + " at the tunnel")
	recID, err := s.ensureRecord(ctx, token, cur, host, tunnelID+".cfargotunnel.com", req.Replace)
	if err != nil {
		return err
	}
	if cur.RecordID != "" && cur.RecordID != recID {
		// The hostname changed: the old record pointed at our tunnel.
		err := s.call(ctx, token, http.MethodDelete, "/zones/"+cur.RecordZoneID+"/dns_records/"+cur.RecordID, nil, nil, nil)
		if err != nil && !isNotFound(err) {
			s.log("could not delete the old DNS record: %v", err)
		}
	}
	cur.RecordID, cur.RecordZoneID = recID, cur.ZoneID
	if err := s.save(cur, nil); err != nil {
		return err
	}

	s.progress("Protecting " + host + " with Cloudflare Access")
	if err := s.ensureAccess(ctx, token, &cur, host, emails, req.Replace); err != nil {
		return err
	}

	s.progress("Starting cloudflared")
	cur.Hostname, cur.Emails, cur.Ready = host, emails, true
	if err := s.save(cur, nil); err != nil {
		return err
	}
	// A new tunnel token or routing restarts cloudflared; until the
	// supervisor sees it connected again, the old connection does not count.
	s.mu.Lock()
	s.connected = false
	s.mu.Unlock()
	s.changed()
	if err := s.waitConnected(ctx); err != nil {
		return err
	}

	s.progress("Checking from outside that " + host + " asks for a login")
	if err := s.verifyRetry(ctx, host); err != nil {
		return verifyError{err}
	}
	return nil
}

// route points the tunnel's public hostname at Socrates.
func (s *Service) route(ctx context.Context, token, acct, tunnelID, host string) error {
	cfg := map[string]any{"config": map[string]any{"ingress": []ingressRule{
		{Hostname: host, Service: s.origin},
		{Service: "http_status:404"}, // cloudflared requires a catch-all last rule
	}}}
	return missing(s.call(ctx, token, http.MethodPut, acct+"/cfd_tunnel/"+tunnelID+"/configurations", nil, cfg, nil), permTunnel)
}

// teamDomain returns the Access team domain, or a clear error if Zero Trust
// was never enabled on the account.
func (s *Service) teamDomain(ctx context.Context, token, acct string) (string, error) {
	var org orgObj
	err := s.call(ctx, token, http.MethodGet, acct+"/access/organizations", nil, nil, &org)
	switch {
	case err == nil && org.AuthDomain != "":
		return strings.TrimPrefix(strings.ToLower(org.AuthDomain), "https://"), nil
	case err == nil, isNotFound(err):
		return "", errZeroTrust
	case isDenied(err):
		// A missing organisation can also come back as 403, so say both.
		return "", fmt.Errorf("Cloudflare refused to show the Zero Trust organisation. Either the API token lacks %q "+
			"(create a new token with the link in Admin → Remote access), or Zero Trust is not enabled yet: open "+
			"https://one.dash.cloudflare.com/ once, choose a team name and the Free plan, then run the setup again. Details: %w", permOrg, err)
	default:
		var e *apiError
		if errors.As(err, &e) && e.Status < 500 {
			return "", fmt.Errorf("%w. Details: %v", errZeroTrust, err)
		}
		return "", err
	}
}

// ensureOTP adds "One-time PIN" as a login method. New Zero Trust
// organisations only get the Cloudflare identity provider, which admits
// members of the Cloudflare account, not arbitrary e-mail addresses.
func (s *Service) ensureOTP(ctx context.Context, token, acct string) error {
	idps, err := list[idpObj](ctx, s, token, acct+"/access/identity_providers", nil)
	if err != nil {
		return missing(err, permOrg)
	}
	for _, p := range idps {
		if p.Type == "onetimepin" {
			return nil
		}
	}
	body := map[string]any{"name": "One-time PIN", "type": "onetimepin", "config": map[string]any{}}
	return missing(s.call(ctx, token, http.MethodPost, acct+"/access/identity_providers", nil, body, nil), permOrg)
}

func (s *Service) ensureTunnel(ctx context.Context, token, acct, known, host string) (string, error) {
	if known != "" {
		var t tunnelObj
		err := s.call(ctx, token, http.MethodGet, acct+"/cfd_tunnel/"+known, nil, nil, &t)
		switch {
		case err == nil && t.DeletedAt == nil && t.ConfigSrc == "cloudflare":
			return t.ID, nil
		case err != nil && !isNotFound(err):
			return "", missing(err, permTunnel)
		}
	}
	name := tunnelName(host)
	found, err := list[tunnelObj](ctx, s, token, acct+"/cfd_tunnel", url.Values{"name": {name}, "is_deleted": {"false"}})
	if err != nil {
		return "", missing(err, permTunnel)
	}
	for _, t := range found {
		if t.Name == name && t.DeletedAt == nil && t.ConfigSrc == "cloudflare" {
			return t.ID, nil
		}
	}
	var t tunnelObj
	body := map[string]any{"name": name, "config_src": "cloudflare"}
	if err := s.call(ctx, token, http.MethodPost, acct+"/cfd_tunnel", nil, body, &t); err != nil {
		var e *apiError
		if errors.As(err, &e) && e.Status == http.StatusConflict {
			return "", fmt.Errorf("a tunnel named %s exists in Cloudflare but is managed by a configuration file, "+
				"not by Socrates. Delete it under Zero Trust > Networks > Tunnels, then run the setup again", name)
		}
		return "", missing(err, permTunnel)
	}
	if t.ID == "" {
		return "", errors.New("Cloudflare created the tunnel but did not say its ID. Run the setup again")
	}
	return t.ID, nil
}

// ours reports whether a DNS record is the one Socrates made for this
// tunnel.
func ourRecord(r recordObj, cur state, target string) bool {
	return r.Type == "CNAME" && (strings.EqualFold(r.Content, target) ||
		(r.ID == cur.RecordID && cur.RecordID != "") ||
		(r.Comment == recordComment && strings.HasSuffix(strings.ToLower(r.Content), ".cfargotunnel.com")))
}

func sameName(a, b string) bool {
	return strings.EqualFold(strings.TrimSuffix(a, "."), strings.TrimSuffix(b, "."))
}

// conflicts lists what is on host that Socrates did not make: DNS records
// and Access applications. Nothing is changed.
func (s *Service) conflicts(ctx context.Context, token string, cur state, host string) ([]Conflict, error) {
	var out []Conflict
	recs, err := list[recordObj](ctx, s, token, "/zones/"+cur.ZoneID+"/dns_records", url.Values{"name.exact": {host}})
	if err != nil {
		return nil, missing(err, permDNS)
	}
	target := ""
	if cur.TunnelID != "" {
		target = cur.TunnelID + ".cfargotunnel.com"
	}
	for _, r := range recs {
		if !sameName(r.Name, host) || ourRecord(r, cur, target) {
			continue
		}
		detail := fmt.Sprintf("The DNS record %s already exists (%s %s) and was not made by Socrates.", host, r.Type, r.Content)
		if id, ok := strings.CutSuffix(strings.ToLower(r.Content), ".cfargotunnel.com"); ok && r.Type == "CNAME" {
			var t tunnelObj
			if s.call(ctx, token, http.MethodGet, "/accounts/"+cur.AccountID+"/cfd_tunnel/"+id, nil, nil, &t) == nil && t.Name != "" {
				detail = fmt.Sprintf("%s points at the tunnel %q, which Socrates did not make. Replacing the record leaves that "+
					"tunnel itself alone; delete it under Zero Trust > Networks > Tunnels once nothing uses it.", host, t.Name)
			}
		}
		out = append(out, Conflict{Kind: "dns", Name: host, Detail: detail})
	}
	apps, err := list[appObj](ctx, s, token, "/accounts/"+cur.AccountID+"/access/apps", nil)
	if err != nil {
		return nil, missing(err, permApps)
	}
	for _, want := range []appObj{mainApp(host, ""), bypassApp(host, "")} {
		for _, a := range apps {
			if a.Name != want.Name && coversSame(a, want) {
				out = append(out, Conflict{Kind: "access", Name: a.Name, Detail: fmt.Sprintf(
					"Cloudflare Access already has an application %q for %s that Socrates did not make.", a.Name, a.Domain)})
			}
		}
	}
	return out, nil
}

// coversSame reports whether two Access applications protect one of the
// same addresses.
func coversSame(a, b appObj) bool {
	uris := func(x appObj) []string {
		out := []string{x.Domain}
		for _, d := range x.Destinations {
			out = append(out, d.URI)
		}
		return out
	}
	for _, u := range uris(a) {
		for _, v := range uris(b) {
			if u != "" && strings.EqualFold(strings.TrimSuffix(u, "/"), strings.TrimSuffix(v, "/")) {
				return true
			}
		}
	}
	return false
}

// ensureRecord creates or updates the proxied CNAME. A record of that name
// that is not ours is deleted when the person agreed to replace it.
func (s *Service) ensureRecord(ctx context.Context, token string, cur state, host, target string, replace bool) (string, error) {
	zone := "/zones/" + cur.ZoneID + "/dns_records"
	recs, err := list[recordObj](ctx, s, token, zone, url.Values{"name.exact": {host}})
	if err != nil {
		return "", missing(err, permDNS)
	}
	want := recordObj{Type: "CNAME", Name: host, Content: target, Proxied: true, TTL: 1, Comment: recordComment}
	var ours *recordObj
	for i, r := range recs {
		if !sameName(r.Name, host) {
			continue
		}
		if !ourRecord(r, cur, target) {
			if !replace {
				return "", &ConflictError{Conflicts: []Conflict{{Kind: "dns", Name: host, Detail: fmt.Sprintf(
					"The DNS record %s already exists (%s %s) and was not made by Socrates.", host, r.Type, r.Content)}}}
			}
			if err := s.call(ctx, token, http.MethodDelete, zone+"/"+r.ID, nil, nil, nil); err != nil && !isNotFound(err) {
				return "", missing(err, permDNS)
			}
			s.log("replaced the DNS record %s %s %s", host, r.Type, r.Content)
			continue
		}
		if ours != nil {
			continue
		}
		ours = &recs[i]
	}
	if ours == nil {
		var r recordObj
		if err := s.call(ctx, token, http.MethodPost, zone, nil, want, &r); err != nil {
			return "", missing(err, permDNS)
		}
		return r.ID, nil
	}
	if ours.Content == target && ours.Proxied && ours.Comment == recordComment {
		return ours.ID, nil
	}
	if err := s.call(ctx, token, http.MethodPut, zone+"/"+ours.ID, nil, want, nil); err != nil {
		return "", missing(err, permDNS)
	}
	return ours.ID, nil
}

func allowPolicy(host string, emails []string) policyObj {
	inc := make([]any, 0, len(emails))
	for _, e := range emails {
		var r ruleEmail
		r.Email.Email = e
		inc = append(inc, r)
	}
	return policyObj{Name: appPrefix + " " + host + ": allowed e-mails", Decision: "allow", Include: inc, SessionDuration: sessionDuration}
}

func bypassPolicy(host string) policyObj {
	return policyObj{Name: appPrefix + " " + host + ": sandboxed pages for everyone", Decision: "bypass",
		Include: []any{map[string]any{"everyone": map[string]any{}}}}
}

func mainApp(host, policy string) appObj {
	return appObj{
		Name: appPrefix + " " + host, Domain: host, Type: "self_hosted",
		Destinations:    []destination{{Type: "public", URI: host}},
		SessionDuration: sessionDuration,
		Policies:        []any{policyRef{ID: policy, Precedence: 1}},
		AppLauncher:     true,
	}
}

// bypassApp lets the sandboxed pages through. Access picks the application
// with the most specific path, so host/docs wins over host for /docs and
// everything below it.
func bypassApp(host, policy string) appObj {
	var dests []destination
	for _, p := range BypassPaths {
		dests = append(dests, destination{Type: "public", URI: host + strings.TrimSuffix(p, "/")})
	}
	return appObj{
		Name: appPrefix + " " + host + " sandboxed pages", Domain: dests[0].URI, Type: "self_hosted",
		Destinations:    dests,
		SessionDuration: sessionDuration,
		Policies:        []any{policyRef{ID: policy, Precedence: 1}},
	}
}

// ensurePolicy creates or updates a reusable Access policy. Reusable
// (account-level) policies are what Cloudflare offers today; legacy
// per-application policies cannot be added to new applications.
func (s *Service) ensurePolicy(ctx context.Context, token, acct, known string, p policyObj) (string, error) {
	base := acct + "/access/policies"
	id := ""
	if known != "" {
		var got policyObj
		err := s.call(ctx, token, http.MethodGet, base+"/"+known, nil, nil, &got)
		if err == nil {
			id = got.ID
		} else if !isNotFound(err) {
			return "", missing(err, permApps)
		}
	}
	if id == "" {
		all, err := list[policyObj](ctx, s, token, base, nil)
		if err != nil {
			return "", missing(err, permApps)
		}
		for _, x := range all {
			if x.Name == p.Name {
				id = x.ID
				break
			}
		}
	}
	if id == "" {
		var got policyObj
		if err := s.call(ctx, token, http.MethodPost, base, nil, p, &got); err != nil {
			return "", missing(err, permApps)
		}
		return got.ID, nil
	}
	if err := s.call(ctx, token, http.MethodPut, base+"/"+id, nil, p, nil); err != nil {
		return "", missing(err, permApps)
	}
	return id, nil
}

// ensureApp creates or updates a self-hosted Access application. Another
// application on the same address is deleted when the person agreed to
// replace it, and stops the setup otherwise.
func (s *Service) ensureApp(ctx context.Context, token, acct, known string, want appObj, replace bool) (appObj, error) {
	base := acct + "/access/apps"
	var have appObj
	if known != "" {
		err := s.call(ctx, token, http.MethodGet, base+"/"+known, nil, nil, &have)
		if err != nil && !isNotFound(err) {
			return appObj{}, missing(err, permApps)
		}
		if err != nil {
			have = appObj{}
		}
	}
	all, err := list[appObj](ctx, s, token, base, nil)
	if err != nil {
		return appObj{}, missing(err, permApps)
	}
	for _, a := range all {
		if a.ID == have.ID && have.ID != "" {
			continue
		}
		if a.Name == want.Name {
			if have.ID == "" {
				have = a
			}
			continue
		}
		if !coversSame(a, want) {
			continue
		}
		if !replace {
			return appObj{}, &ConflictError{Conflicts: []Conflict{{Kind: "access", Name: a.Name, Detail: fmt.Sprintf(
				"Cloudflare Access already has an application %q for %s that Socrates did not make.", a.Name, a.Domain)}}}
		}
		if err := s.call(ctx, token, http.MethodDelete, base+"/"+a.ID, nil, nil, nil); err != nil && !isNotFound(err) {
			return appObj{}, missing(err, permApps)
		}
		s.log("replaced the Access application %q", a.Name)
	}
	var got appObj
	if have.ID == "" {
		if err := s.call(ctx, token, http.MethodPost, base, nil, want, &got); err != nil {
			return appObj{}, missing(err, permApps)
		}
	} else if err := s.call(ctx, token, http.MethodPut, base+"/"+have.ID, nil, want, &got); err != nil {
		return appObj{}, missing(err, permApps)
	}
	if got.ID == "" {
		got.ID = have.ID
	}
	if got.AUD == "" {
		got.AUD = have.AUD
	}
	return got, nil
}

func (s *Service) ensureAccess(ctx context.Context, token string, cur *state, host string, emails []string, replace bool) error {
	acct := "/accounts/" + cur.AccountID
	var err error
	if cur.AllowPolicyID, err = s.ensurePolicy(ctx, token, acct, cur.AllowPolicyID, allowPolicy(host, emails)); err != nil {
		return err
	}
	if cur.BypassPolicyID, err = s.ensurePolicy(ctx, token, acct, cur.BypassPolicyID, bypassPolicy(host)); err != nil {
		return err
	}
	if err := s.save(*cur, nil); err != nil {
		return err
	}
	bypass, err := s.ensureApp(ctx, token, acct, cur.BypassAppID, bypassApp(host, cur.BypassPolicyID), replace)
	if err != nil {
		return err
	}
	cur.BypassAppID = bypass.ID
	if err := s.save(*cur, nil); err != nil {
		return err
	}
	app, err := s.ensureApp(ctx, token, acct, cur.AppID, mainApp(host, cur.AllowPolicyID), replace)
	if err != nil {
		return err
	}
	if app.AUD == "" {
		return errors.New("Cloudflare created the Access application but did not return its AUD tag. Run the setup again")
	}
	cur.AppID, cur.AppAUD = app.ID, app.AUD
	return s.save(*cur, nil)
}

// waitConnected waits until the supervisor reports edge connections.
func (s *Service) waitConnected(ctx context.Context) error {
	deadline := s.now().Add(s.ConnectFor)
	for {
		s.mu.Lock()
		ok, running, downloading, perr := s.connected, s.running, s.downloading, s.procErr
		s.mu.Unlock()
		if ok {
			return nil
		}
		if downloading {
			// The download is not the connection's time.
			deadline = s.now().Add(s.ConnectFor)
		}
		if s.now().After(deadline) {
			msg := "cloudflared did not connect to Cloudflare within " + s.ConnectFor.String() + "."
			switch {
			case perr != "":
				msg += " " + perr
			case !running:
				msg += " It is not running."
			default:
				msg += " Check that this machine may open outgoing connections on port 7844 (UDP and TCP)."
			}
			return errors.New(msg + " Then run the setup again")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// SetEmails replaces who may sign in.
func (s *Service) SetEmails(ctx context.Context, emails []string) error {
	s.op.Lock()
	defer s.op.Unlock()
	emails, err := NormEmails(emails)
	if err != nil {
		return err
	}
	s.mu.Lock()
	cur := s.cur
	s.mu.Unlock()
	if !cur.Ready {
		return errors.New("the tunnel is not set up yet")
	}
	token, err := s.secret(secretAPIToken)
	if err != nil || token == "" {
		return errors.New("the Cloudflare API token is no longer stored. Run the setup again with a token")
	}
	id, err := s.ensurePolicy(ctx, token, "/accounts/"+cur.AccountID, cur.AllowPolicyID, allowPolicy(cur.Hostname, emails))
	if err != nil {
		return err
	}
	if id != cur.AllowPolicyID {
		// The policy was gone and is new: attach it again.
		cur.AllowPolicyID = id
		if err := s.ensureAccess(ctx, token, &cur, cur.Hostname, emails, false); err != nil {
			return err
		}
	}
	cur.Emails = emails
	return s.save(cur, nil)
}

// StartRemove removes the tunnel in the background.
func (s *Service) StartRemove() error {
	if err := s.beginJob("remove"); err != nil {
		return err
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), jobTimeout)
		defer cancel()
		s.endJob(s.Remove(ctx))
	}()
	return nil
}

// Remove stops cloudflared and deletes what Setup created. Anything that is
// already gone is skipped; if a deletion fails, the rest stays recorded so
// Remove can be run again.
func (s *Service) Remove(ctx context.Context) error {
	s.op.Lock()
	defer s.op.Unlock()
	s.mu.Lock()
	cur := s.cur
	s.mu.Unlock()

	// Stop cloudflared first: Cloudflare refuses to delete a connected tunnel.
	s.progress("Stopping cloudflared")
	cur.Ready = false
	if err := s.save(cur, nil); err != nil {
		return err
	}
	s.changed()
	s.waitStopped(ctx, 20*time.Second)

	token, _ := s.secret(secretAPIToken)
	if token != "" && cur.AccountID != "" {
		s.progress("Deleting the tunnel, its DNS record and the Access applications on Cloudflare")
		acct := "/accounts/" + cur.AccountID
		gone := func(err error) bool { return err == nil || isNotFound(err) }
		del := func(path string, perm string, clear *string) error {
			if *clear == "" {
				return nil
			}
			err := s.call(ctx, token, http.MethodDelete, path, nil, nil, nil)
			if !gone(err) {
				return missing(err, perm)
			}
			*clear = ""
			return s.save(cur, nil)
		}
		steps := []func() error{
			// Applications before policies: a policy in use cannot be deleted.
			func() error { return del(acct+"/access/apps/"+cur.AppID, permApps, &cur.AppID) },
			func() error { return del(acct+"/access/apps/"+cur.BypassAppID, permApps, &cur.BypassAppID) },
			func() error { return del(acct+"/access/policies/"+cur.AllowPolicyID, permApps, &cur.AllowPolicyID) },
			func() error { return del(acct+"/access/policies/"+cur.BypassPolicyID, permApps, &cur.BypassPolicyID) },
			func() error {
				return del("/zones/"+cur.RecordZoneID+"/dns_records/"+cur.RecordID, permDNS, &cur.RecordID)
			},
			func() error { return s.deleteTunnel(ctx, token, acct, &cur) },
		}
		for _, step := range steps {
			if err := step(); err != nil {
				return fmt.Errorf("could not remove everything on Cloudflare: %w. Choose \"Remove\" again to retry", err)
			}
		}
	} else if cur.TunnelID != "" || cur.RecordID != "" || cur.AppID != "" {
		s.log("the API token is no longer stored; delete the tunnel %s, the DNS record %s and the Access "+
			"applications by hand in the Cloudflare dashboard", tunnelName(cur.Hostname), cur.Hostname)
	}
	s.progress("Forgetting the tunnel")
	for _, k := range []string{secretAPIToken, secretTunnelTok, stateKey} {
		if err := s.kv.DeleteKV(k); err != nil {
			return fmt.Errorf("cannot forget the tunnel settings: %w", err)
		}
	}
	s.mu.Lock()
	s.cur, s.hasToken = state{}, false
	s.setupErr, s.procErr, s.verifyErr, s.originErr, s.verified = "", "", "", "", false
	s.mu.Unlock()
	s.changed()
	return nil
}

func (s *Service) deleteTunnel(ctx context.Context, token, acct string, cur *state) error {
	if cur.TunnelID == "" {
		return nil
	}
	path := acct + "/cfd_tunnel/" + cur.TunnelID
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		// Cloudflare keeps a stopped connector listed for a while; clearing
		// the connections lets the deletion go through.
		_ = s.call(ctx, token, http.MethodDelete, path+"/connections", nil, nil, nil)
		err = s.call(ctx, token, http.MethodDelete, path, nil, nil, nil)
		if err == nil || isNotFound(err) {
			cur.TunnelID = ""
			return s.save(*cur, nil)
		}
		if isDenied(err) {
			return missing(err, permTunnel)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.MinBackoff * 2):
		}
	}
	return err
}

func (s *Service) waitStopped(ctx context.Context, max time.Duration) {
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		r := s.running
		s.mu.Unlock()
		if !r {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
}
