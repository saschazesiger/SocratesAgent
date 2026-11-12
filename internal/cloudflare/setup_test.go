package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestSetupFull(t *testing.T) {
	h := newHarness(t, true)
	if err := h.svc.Setup(context.Background(), setupReq()); err != nil {
		t.Fatal(err)
	}
	if job := h.svc.Status().Job; len(job.Done) != 9 || !strings.Contains(job.Step, "from outside") {
		t.Errorf("progress = %q, then %q", job.Done, job.Step)
	}
	st := h.svc.Status()
	if !st.Configured || !st.Running || !st.Connected || !st.Verified || st.Error != "" || !st.HasToken {
		t.Errorf("status = %+v", st)
	}
	if st.Hostname != testHost || st.PublicURL != "https://"+testHost || strings.Join(st.Emails, ",") != "alice@example.com,bob@example.com" {
		t.Errorf("status = %+v", st)
	}

	cf := h.cf
	cf.mu.Lock()
	defer cf.mu.Unlock()
	if len(cf.tunnels) != 1 || len(cf.records) != 1 || len(cf.policies) != 2 || len(cf.apps) != 2 || len(cf.idps) != 1 {
		t.Fatalf("objects: %d tunnels, %d records, %d policies, %d apps, %d idps", len(cf.tunnels), len(cf.records), len(cf.policies), len(cf.apps), len(cf.idps))
	}
	if cf.idps[0].Type != "onetimepin" {
		t.Errorf("idp = %+v", cf.idps[0])
	}
	var tun tunnelObj
	for _, x := range cf.tunnels {
		tun = *x
	}
	if tun.Name != "socrates-"+testHost || tun.ConfigSrc != "cloudflare" {
		t.Errorf("tunnel = %+v", tun)
	}
	ing := cf.ingress[tun.ID]
	if len(ing) != 2 || ing[0].Hostname != testHost || ing[0].Service != testOrigin || ing[1].Service != "http_status:404" || ing[1].Hostname != "" {
		t.Errorf("ingress = %+v", ing)
	}
	for _, r := range cf.records {
		if r.Type != "CNAME" || r.Name != testHost || r.Content != tun.ID+".cfargotunnel.com" || !r.Proxied || r.TTL != 1 {
			t.Errorf("record = %+v", r)
		}
	}
	byDomain := map[string]appObj{}
	for _, a := range cf.apps {
		byDomain[a.Domain] = *a
	}
	main, hook := byDomain[testHost], byDomain[testHost+"/docs"]
	if main.Type != "self_hosted" || hook.Type != "self_hosted" || main.SessionDuration != "720h" || !main.AppLauncher {
		t.Errorf("apps = %+v", byDomain)
	}
	if len(hook.Destinations) != 2 || hook.Destinations[0].URI != testHost+"/docs" || hook.Destinations[1].URI != testHost+"/artifact-kit" {
		t.Errorf("apps = %+v", byDomain)
	}
	pol := func(a appObj) policyObj {
		b, _ := json.Marshal(a.Policies)
		var refs []policyRef
		json.Unmarshal(b, &refs)
		if len(refs) != 1 {
			t.Fatalf("app %s has policies %s", a.Domain, b)
		}
		return *cf.policies[refs[0].ID]
	}
	allow, bypass := pol(main), pol(hook)
	b, _ := json.Marshal(allow.Include)
	if allow.Decision != "allow" || string(b) != `[{"email":{"email":"alice@example.com"}},{"email":{"email":"bob@example.com"}}]` {
		t.Errorf("allow policy = %+v %s", allow, b)
	}
	b, _ = json.Marshal(bypass.Include)
	if bypass.Decision != "bypass" || string(b) != `[{"everyone":{}}]` {
		t.Errorf("bypass policy = %+v %s", bypass, b)
	}

	// Secrets are sealed in the store, and the token reached cloudflared
	// only through the environment.
	for _, k := range []string{secretAPIToken, secretTunnelTok} {
		raw, _ := h.kv.GetKV(k)
		if raw == "" || strings.Contains(raw, testToken) || strings.Contains(raw, "tunnel-token-") {
			t.Errorf("%s not stored sealed: %q", k, raw)
		}
	}
	if raw, _ := h.kv.GetKV(stateKey); strings.Contains(raw, testToken) || strings.Contains(raw, "tunnel-token-") {
		t.Errorf("a secret in the state: %s", raw)
	}
	log, _ := os.ReadFile(h.cfdLog)
	if !strings.Contains(string(log), `args=["tunnel" "--no-autoupdate" "--metrics"`) || !strings.Contains(string(log), "token=true") {
		t.Errorf("cloudflared log: %s", log)
	}
	if strings.Contains(string(log), "tunnel-token-") || strings.Contains(h.allLogs(), "tunnel-token-") {
		t.Error("tunnel token leaked into arguments or logs")
	}
}

func TestSetupAgainChangesNothing(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	if err := h.svc.Setup(ctx, setupReq()); err != nil {
		t.Fatal(err)
	}
	h.cf.mu.Lock()
	before := fmt.Sprint(h.cf.creates)
	n := len(h.cf.log)
	h.cf.mu.Unlock()
	if err := h.svc.Setup(ctx, setupReq()); err != nil {
		t.Fatal(err)
	}
	h.cf.mu.Lock()
	after := fmt.Sprint(h.cf.creates)
	for _, c := range h.cf.log[n:] {
		if strings.HasPrefix(c, "POST") || strings.HasPrefix(c, "DELETE") || strings.Contains(c, "dns_records/") {
			t.Errorf("second Setup sent %s", c)
		}
	}
	h.cf.mu.Unlock()
	if before != after {
		t.Errorf("creates %s, then %s", before, after)
	}
	if !h.svc.Status().Verified {
		t.Error("not verified after second setup")
	}

	// A new hostname reuses tunnel, policies and applications and replaces
	// the DNS record.
	req := setupReq()
	req.Hostname = "build.example.com"
	if err := h.svc.Setup(ctx, req); err != nil {
		t.Fatal(err)
	}
	tun, rec, pol, apps := h.counts()
	if tun != 1 || rec != 1 || pol != 2 || apps != 2 {
		t.Errorf("after new hostname: %d tunnels, %d records, %d policies, %d apps", tun, rec, pol, apps)
	}
	h.cf.mu.Lock()
	for _, r := range h.cf.records {
		if r.Name != "build.example.com" {
			t.Errorf("record = %+v", r)
		}
	}
	for _, a := range h.cf.apps {
		if !strings.HasPrefix(a.Domain, "build.example.com") {
			t.Errorf("app = %+v", a)
		}
	}
	h.cf.mu.Unlock()
}

// TestSetupResume fails each API call of a Setup once, both before and
// after Cloudflare carried it out, and checks that running Setup again ends
// with exactly one of everything.
func TestSetupResume(t *testing.T) {
	probe := newHarness(t, true)
	if err := probe.svc.Setup(context.Background(), setupReq()); err != nil {
		t.Fatal(err)
	}
	total := probe.cf.calls
	probe.stopRun()
	for _, lose := range []bool{false, true} {
		for n := 1; n <= total; n++ {
			t.Run(fmt.Sprintf("lose=%v/call=%d", lose, n), func(t *testing.T) {
				h := newHarness(t, true)
				h.cf.failAt, h.cf.lose = n, lose
				if err := h.svc.Setup(context.Background(), setupReq()); err == nil {
					t.Fatalf("Setup passed although call %d failed", n)
				} else if h.svc.Status().Error == "" {
					t.Error("Status shows no error after a failed Setup")
				}
				h.cf.mu.Lock()
				h.cf.failAt = 0
				h.cf.mu.Unlock()
				// A restart in between: a new service reads what was stored.
				h.stopRun()
				h.svc = h.newService()
				h.startRun()
				if err := h.svc.Setup(context.Background(), setupReq()); err != nil {
					t.Fatal(err)
				}
				tun, rec, pol, apps := h.counts()
				if tun != 1 || rec != 1 || pol != 2 || apps != 2 || len(h.cf.idps) != 1 {
					t.Errorf("%d tunnels, %d records, %d policies, %d apps, %d idps", tun, rec, pol, apps, len(h.cf.idps))
				}
				if st := h.svc.Status(); !st.Verified || st.Error != "" {
					t.Errorf("status = %+v", st)
				}
			})
		}
	}
}

func TestSetupWithoutZeroTrust(t *testing.T) {
	h := newHarness(t, false)
	h.cf.noOrg = true
	err := h.svc.Setup(context.Background(), setupReq())
	if err == nil || !strings.Contains(err.Error(), "Zero Trust is not enabled") || !strings.Contains(err.Error(), "Free plan") ||
		!strings.Contains(err.Error(), "run the setup again") {
		t.Fatalf("err = %v", err)
	}
	if tun, _, _, _ := h.counts(); tun != 0 {
		t.Error("a tunnel was created without Zero Trust")
	}
	if !strings.Contains(h.svc.Status().Error, "Zero Trust") {
		t.Errorf("status = %+v", h.svc.Status())
	}
}

func TestSetupDNSConflict(t *testing.T) {
	h := newHarness(t, false)
	h.cf.records["rec-x"] = &recordObj{ID: "rec-x", Type: "A", Name: testHost, Content: "192.0.2.1", TTL: 1}
	err := h.svc.Setup(context.Background(), setupReq())
	if err == nil || !strings.Contains(err.Error(), "already exists") || !strings.Contains(err.Error(), "192.0.2.1") {
		t.Fatalf("err = %v", err)
	}
	var ce *ConflictError
	if !errors.As(err, &ce) || len(ce.Conflicts) != 1 || ce.Conflicts[0].Kind != "dns" {
		t.Fatalf("conflicts = %+v", ce)
	}
	if r := h.cf.records["rec-x"]; r.Type != "A" || r.Content != "192.0.2.1" || len(h.cf.records) != 1 {
		t.Errorf("foreign record touched: %+v", h.cf.records)
	}
	if tun, _, pol, apps := h.counts(); tun+pol+apps != 0 {
		t.Errorf("objects made before the conflict was settled: %d tunnels, %d policies, %d apps", tun, pol, apps)
	}

	// Agreed: the record is replaced by Socrates' own.
	h.startRun()
	req := setupReq()
	req.Replace = true
	if err := h.svc.Setup(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	h.cf.mu.Lock()
	defer h.cf.mu.Unlock()
	if _, ok := h.cf.records["rec-x"]; ok || len(h.cf.records) != 1 {
		t.Errorf("records = %+v", h.cf.records)
	}
	for _, r := range h.cf.records {
		if r.Type != "CNAME" || r.Comment != recordComment {
			t.Errorf("record = %+v", r)
		}
	}
}

// A hand-made tunnel and Access application on the address are named, and
// replaced only when the person agrees.
func TestSetupReplacesHandMadeTunnelAndApp(t *testing.T) {
	h := newHarness(t, true)
	h.cf.tunnels["old-tunnel"] = &tunnelObj{ID: "old-tunnel", Name: "SocratesApp", ConfigSrc: "local"}
	h.cf.records["rec-old"] = &recordObj{ID: "rec-old", Type: "CNAME", Name: testHost, Content: "old-tunnel.cfargotunnel.com", Proxied: true, TTL: 1}
	h.cf.apps["app-old"] = &appObj{ID: "app-old", AUD: "aud-old", Name: "Socrates", Domain: testHost, Type: "self_hosted"}
	err := h.svc.Setup(context.Background(), setupReq())
	var ce *ConflictError
	if !errors.As(err, &ce) || len(ce.Conflicts) != 2 {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(ce.Conflicts[0].Detail, `"SocratesApp"`) || ce.Conflicts[1].Kind != "access" || ce.Conflicts[1].Name != "Socrates" {
		t.Errorf("conflicts = %+v", ce.Conflicts)
	}
	req := setupReq()
	req.Replace = true
	if err := h.svc.Setup(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	h.cf.mu.Lock()
	defer h.cf.mu.Unlock()
	if _, ok := h.cf.apps["app-old"]; ok {
		t.Error("the hand-made Access application is still there")
	}
	if _, ok := h.cf.records["rec-old"]; ok {
		t.Error("the hand-made DNS record is still there")
	}
	if _, ok := h.cf.tunnels["old-tunnel"]; !ok {
		t.Error("the hand-made tunnel itself must be left alone")
	}
}

func TestSetupVerifyFails(t *testing.T) {
	h := newHarness(t, true)
	h.edge.bypassOK = false
	err := h.svc.Setup(context.Background(), setupReq())
	if err == nil || !strings.Contains(err.Error(), "/artifact-kit/ asks for a login") {
		t.Fatalf("err = %v", err)
	}
	h.edge.mu.Lock()
	h.edge.bypassOK, h.edge.loginOK = true, false
	h.edge.mu.Unlock()
	err = h.svc.Setup(context.Background(), setupReq())
	if err == nil || !strings.Contains(err.Error(), "without asking for a login") {
		t.Fatalf("err = %v", err)
	}
	if st := h.svc.Status(); st.Verified || st.Error == "" {
		t.Errorf("status = %+v", st)
	}
}

func TestSetEmails(t *testing.T) {
	h := newHarness(t, true)
	if err := h.svc.Setup(context.Background(), setupReq()); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.SetEmails(context.Background(), []string{"not an address"}); err == nil {
		t.Error("bad address accepted")
	}
	if err := h.svc.SetEmails(context.Background(), []string{" Carol@Example.org "}); err != nil {
		t.Fatal(err)
	}
	if got := h.svc.Status().Emails; len(got) != 1 || got[0] != "carol@example.org" {
		t.Errorf("emails = %q", got)
	}
	h.cf.mu.Lock()
	defer h.cf.mu.Unlock()
	pol := h.cf.policies[h.cur().AllowPolicyID]
	b, _ := json.Marshal(pol.Include)
	if string(b) != `[{"email":{"email":"carol@example.org"}}]` {
		t.Errorf("include = %s", b)
	}
	if len(h.cf.policies) != 2 {
		t.Errorf("%d policies", len(h.cf.policies))
	}
}

func TestRemovePartlyGone(t *testing.T) {
	h := newHarness(t, true)
	if err := h.svc.Setup(context.Background(), setupReq()); err != nil {
		t.Fatal(err)
	}
	// Someone already deleted the main application and the DNS record.
	h.cf.mu.Lock()
	delete(h.cf.apps, h.cur().AppID)
	delete(h.cf.records, h.cur().RecordID)
	h.cf.mu.Unlock()
	if err := h.svc.Remove(context.Background()); err != nil {
		t.Fatal(err)
	}
	tun, rec, pol, apps := h.counts()
	if tun+rec+pol+apps != 0 {
		t.Errorf("left: %d tunnels, %d records, %d policies, %d apps", tun, rec, pol, apps)
	}
	st := h.svc.Status()
	if st.Configured || st.Running || st.HasToken || st.Hostname != "" {
		t.Errorf("status = %+v", st)
	}
	for _, k := range []string{stateKey, secretAPIToken, secretTunnelTok} {
		if v, _ := h.kv.GetKV(k); v != "" {
			t.Errorf("%s still stored", k)
		}
	}
	// Removing again is harmless.
	if err := h.svc.Remove(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCheck(t *testing.T) {
	h := newHarness(t, false)
	info, err := h.svc.Check(context.Background(), testToken)
	if err != nil || len(info.Zones) != 1 || info.Zones[0].Name != "example.com" || info.Zones[0].AccountID != testAcct ||
		info.Zones[0].AccountName != "Acme" || info.User != testUser || info.Stored {
		t.Fatalf("info = %+v, %v", info, err)
	}
	if _, err := h.svc.Check(context.Background(), "wrong"); err == nil || !strings.Contains(err.Error(), "does not accept this API token") {
		t.Fatalf("err = %v", err)
	}
	// Without "User Details: Read" the user stays unknown, nothing else.
	h.cf.noUser = true
	if info, err := h.svc.Check(context.Background(), testToken); err != nil || info.User != "" || len(info.Zones) != 1 {
		t.Fatalf("info = %+v, %v", info, err)
	}
	// An empty token is the stored one, and there is none yet.
	if _, err := h.svc.Check(context.Background(), ""); err == nil {
		t.Fatal("an empty token passed without a stored one")
	}
	h.svc.save(state{}, map[string]string{secretAPIToken: testToken})
	if info, err := h.svc.Check(context.Background(), " "); err != nil || !info.Stored {
		t.Fatalf("info = %+v, %v", info, err)
	}
}

func TestTokenURL(t *testing.T) {
	u, err := url.Parse(TokenURL())
	if err != nil || u.Host != "dash.cloudflare.com" || u.Path != "/profile/api-tokens" {
		t.Fatalf("url = %v", u)
	}
	var keys []struct{ Key, Type string }
	if err := json.Unmarshal([]byte(u.Query().Get("permissionGroupKeys")), &keys); err != nil || len(keys) != 6 {
		t.Fatalf("keys = %+v, %v", keys, err)
	}
	if u.Query().Get("accountId") != "*" || u.Query().Get("zoneId") != "all" {
		t.Errorf("url = %v", u)
	}
	if len(Permissions()) != 6 {
		t.Error("permissions")
	}
}

// The background job: progress while it runs, conflicts when it stops for
// them, one job at a time.
func TestStartSetupJob(t *testing.T) {
	h := newHarness(t, true)
	if err := h.svc.StartSetup(SetupRequest{ZoneID: testZone, Hostname: testHost, Emails: []string{"a@example.com"}}); err == nil {
		t.Error("a setup without any token started")
	}
	if err := h.svc.StartSetup(SetupRequest{APIToken: testToken, ZoneID: testZone, Hostname: testHost}); err == nil {
		t.Error("a setup without addresses started")
	}
	if err := h.svc.StartSetup(setupReq()); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.StartSetup(setupReq()); err == nil {
		t.Error("a second setup started beside the first")
	}
	waitFor(t, "the setup", func() bool { return !h.svc.Status().Job.Running })
	if job := h.svc.Status().Job; !job.OK || job.Error != "" || job.Step != "" || len(job.Done) != 10 {
		t.Fatalf("job = %+v", job)
	}
	if err := h.svc.StartRemove(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the removal", func() bool { return !h.svc.Status().Job.Running })
	if st := h.svc.Status(); !st.Job.OK || st.Configured || st.Partial {
		t.Fatalf("status = %+v", st)
	}
}

// A data directory that moved: the routing follows the socket at the start.
func TestRunFollowsAMovedSocket(t *testing.T) {
	h := newHarness(t, true)
	if err := h.svc.Setup(context.Background(), setupReq()); err != nil {
		t.Fatal(err)
	}
	h.stopRun()
	moved := h.newService()
	moved.origin = "unix:/srv/socrates/run/tunnel.sock"
	h.svc = moved
	h.startRun()
	waitFor(t, "the new routing", func() bool { return h.cur().Origin == moved.origin })
	h.cf.mu.Lock()
	defer h.cf.mu.Unlock()
	ing := h.cf.ingress[h.cur().TunnelID]
	if len(ing) != 2 || ing[0].Service != moved.origin {
		t.Errorf("ingress = %+v", ing)
	}
}

func TestBypassed(t *testing.T) {
	for path, want := range map[string]bool{
		"/docs/tok/a.html": true, "/artifact-kit/kit.js": true, "/": false, "/docs": false,
		"/api/docs": false, "/admin": false, "/static/docs/x": false,
	} {
		if Bypassed(path) != want {
			t.Errorf("Bypassed(%q) = %v", path, !want)
		}
	}
}

func (h *harness) cur() state {
	h.svc.mu.Lock()
	defer h.svc.mu.Unlock()
	return h.svc.cur
}
