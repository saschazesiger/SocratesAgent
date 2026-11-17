package searchconsole

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/mcpserver"
)

// fakeGoogle is Google's token endpoint and enough of the Search Console
// API: it checks the signed assertion, then answers for its properties.
type fakeGoogle struct {
	t   *testing.T
	pub *rsa.PublicKey
	srv *httptest.Server

	mu       sync.Mutex
	tokens   int
	revoke   bool // the next API call answers 401 once
	sites    []site
	rows     int // rows a query answers
	bodies   []map[string]any
	paths    []string
	refuseID bool // the token endpoint refuses the key
}

func newFakeGoogle(t *testing.T, pub *rsa.PublicKey) *fakeGoogle {
	f := &fakeGoogle{t: t, pub: pub, sites: []site{
		{URL: "sc-domain:example.com", Permission: "siteFullUser"},
		{URL: "https://shop.example.org/", Permission: "siteRestrictedUser"},
		{URL: "https://gone.example.net/", Permission: "siteUnverifiedUser"},
	}, rows: 3}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGoogle) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == "/token" {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" || !f.verify(r.Form.Get("assertion")) || f.refuseID {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"Invalid JWT Signature."}`)
			return
		}
		f.tokens++
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok" + string(rune('0'+f.tokens)), "expires_in": 3600})
		return
	}
	want := "Bearer tok" + string(rune('0'+f.tokens))
	if r.Header.Get("Authorization") != want || f.revoke {
		f.revoke = false
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"code":401,"message":"Request had invalid authentication credentials."}}`)
		return
	}
	f.paths = append(f.paths, r.Method+" "+r.URL.EscapedPath())
	var body map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	f.bodies = append(f.bodies, body)
	switch {
	case strings.Contains(r.URL.Path, "nope"):
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"code":403,"message":"User does not have sufficient permission"}}`)
	case r.URL.Path == "/api/sites":
		_ = json.NewEncoder(w).Encode(map[string]any{"siteEntry": f.sites})
	case strings.HasSuffix(r.URL.Path, "/searchAnalytics/query"):
		var rows []map[string]any
		for i := 0; i < f.rows; i++ {
			rows = append(rows, map[string]any{"keys": []string{"word|" + string(rune('a'+i%26)), "MOBILE"},
				"clicks": 10 - i%10, "impressions": 100, "ctr": 0.1, "position": 2.345})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"rows": rows, "responseAggregationType": "byProperty"})
	case strings.HasSuffix(r.URL.Path, "/sitemaps"):
		_, _ = io.WriteString(w, `{"sitemap":[{"path":"https://example.com/sitemap.xml","lastSubmitted":"2026-11-01T10:00:00Z",
			"isPending":false,"isSitemapsIndex":true,"type":"sitemap","lastDownloaded":"2026-11-16T03:00:00Z","warnings":"2","errors":"1",
			"contents":[{"type":"web","submitted":"120","indexed":"0"}]}]}`)
	case r.URL.Path == "/v1/urlInspection/index:inspect":
		_, _ = io.WriteString(w, `{"inspectionResult":{"inspectionResultLink":"https://search.google.com/x",
			"indexStatusResult":{"verdict":"PASS","coverageState":"Submitted and indexed","robotsTxtState":"ALLOWED",
			"indexingState":"INDEXING_ALLOWED","lastCrawlTime":"2026-11-15T10:00:00Z","pageFetchState":"SUCCESSFUL",
			"googleCanonical":"https://example.com/a","userCanonical":"https://example.com/a","crawledAs":"MOBILE"},
			"richResultsResult":{"verdict":"PARTIAL","detectedItems":[{"richResultType":"FAQ","items":[{"name":"Unnamed item",
			"issues":[{"issueMessage":"Missing field \"name\"","severity":"WARNING"}]}]}]}}}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// verify checks an assertion's signature and claims.
func (f *fakeGoogle) verify(jwt string) bool {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(f.pub, crypto.SHA256, sum[:], sig) != nil {
		return false
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var c map[string]any
	_ = json.Unmarshal(raw, &c)
	return c["iss"] == "seo@proj.iam.gserviceaccount.com" && c["scope"] == scope && c["aud"] == f.srv.URL+"/token"
}

var (
	testKeyOnce sync.Once
	testKey     *rsa.PrivateKey
)

func keyFile(t *testing.T) (string, *rsa.PublicKey) {
	testKeyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		testKey = k
	})
	der, _ := x509.MarshalPKCS8PrivateKey(testKey)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	raw, _ := json.MarshalIndent(map[string]string{
		"type": "service_account", "project_id": "proj", "private_key_id": "kid1", "private_key": pemKey,
		"client_email": "seo@proj.iam.gserviceaccount.com", "token_uri": "https://oauth2.googleapis.com/token",
	}, "", "  ")
	return string(raw), &testKey.PublicKey
}

func newTestServer(t *testing.T, accounts ...map[string]string) (*Server, *fakeGoogle) {
	key, pub := keyFile(t)
	f := newFakeGoogle(t, pub)
	if len(accounts) == 0 {
		accounts = []map[string]string{{"name": "Browser.lol", "key": key}}
	}
	s := New(Deps{
		DataDir: t.TempDir(),
		Accounts: func() []mcpserver.Account {
			var out []mcpserver.Account
			for i, v := range accounts {
				out = append(out, mcpserver.Account{ID: string(rune('a' + i)), Values: v})
			}
			return out
		},
		TokenURL: f.srv.URL + "/token", APIBase: f.srv.URL + "/api", InspectBase: f.srv.URL + "/v1",
		Now: func() time.Time { return time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC) },
	})
	return s, f
}

func run(t *testing.T, s *Server, tool, args string) (string, bool) {
	t.Helper()
	r := s.Call(context.Background(), mcpserver.Session{ID: "chat1"}, tool, json.RawMessage(args))
	return r.Content[0].Text, r.IsError
}

func TestCheckAccount(t *testing.T) {
	s, f := newTestServer(t)
	key, _ := keyFile(t)
	c := s.CheckAccount(context.Background(), map[string]string{"name": "Browser.lol", "key": key})
	if c.State != mcpserver.AccountValid || !strings.Contains(c.Detail, "3 properties: https://gone.example.net/, https://shop.example.org/, sc-domain:example.com") {
		t.Fatalf("check: %+v", c)
	}
	if got := s.AccountLabel(map[string]string{"name": " Browser.lol ", "key": key}); got != "Browser.lol" {
		t.Errorf("label %q", got)
	}
	if got := s.AccountLabel(map[string]string{"key": key}); got != "seo@proj.iam.gserviceaccount.com" {
		t.Errorf("label without a name %q", got)
	}
	for _, bad := range []string{"not json", `{"type":"authorized_user"}`, `{"type":"service_account","client_email":"a@b","private_key":"x"}`} {
		if c := s.CheckAccount(context.Background(), map[string]string{"key": bad}); c.State != mcpserver.AccountInvalid {
			t.Errorf("%s: %+v", bad, c)
		}
	}
	f.refuseID = true
	if c := s.CheckAccount(context.Background(), map[string]string{"key": key}); c.State != mcpserver.AccountInvalid || !strings.Contains(c.Detail, "invalid_grant") {
		t.Errorf("a refused key: %+v", c)
	}
}

func TestSitesAndTokenReuse(t *testing.T) {
	s, f := newTestServer(t)
	text, isErr := run(t, s, Sites, `{}`)
	if isErr || !strings.Contains(text, "3 properties the account Browser.lol (service account seo@proj.iam.gserviceaccount.com)") ||
		!strings.Contains(text, "- sc-domain:example.com (full user)") || !strings.Contains(text, "unverified (no data)") {
		t.Fatalf("sites: %s", text)
	}
	run(t, s, Sites, `{}`)
	if f.tokens != 1 {
		t.Errorf("a token per call: %d", f.tokens)
	}
	f.revoke = true
	if text, isErr := run(t, s, Sites, `{}`); isErr {
		t.Fatalf("a refused token is not fetched again: %s", text)
	}
	if f.tokens != 2 {
		t.Errorf("tokens after a 401: %d", f.tokens)
	}
}

func TestPickSite(t *testing.T) {
	list := []site{{URL: "sc-domain:example.com"}, {URL: "https://www.example.com/blog/"}, {URL: "https://shop.example.org/"}}
	for arg, want := range map[string]string{
		"sc-domain:example.com":              "sc-domain:example.com",
		"example.com":                        "sc-domain:example.com",
		"www.example.com":                    "sc-domain:example.com",
		"https://www.example.com/blog/post":  "https://www.example.com/blog/",
		"https://www.example.com/other":      "sc-domain:example.com",
		"shop.example.org":                   "https://shop.example.org/",
		"https://shop.example.org":           "https://shop.example.org/",
		"HTTPS://SHOP.EXAMPLE.ORG/":          "https://shop.example.org/",
		"https://deep.sub.example.com/x?y=1": "sc-domain:example.com",
	} {
		got, err := pickSite(list, arg, "e")
		if err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", arg, got, err, want)
		}
	}
	if _, err := pickSite(list, "", "e"); err == nil || !strings.Contains(err.Error(), "say which property") {
		t.Errorf("none named of three: %v", err)
	}
	if _, err := pickSite(list, "other.net", "e"); err == nil || !strings.Contains(err.Error(), "no property covers other.net") {
		t.Errorf("an unknown one: %v", err)
	}
	if got, _ := pickSite(list[:1], "", "e"); got != "sc-domain:example.com" {
		t.Errorf("the only one: %q", got)
	}
	if _, err := pickSite(nil, "", "seo@x"); err == nil || !strings.Contains(err.Error(), "seo@x may read no property") {
		t.Errorf("none: %v", err)
	}
}

func TestQuery(t *testing.T) {
	s, f := newTestServer(t)
	text, isErr := run(t, s, Query, `{"site":"example.com","dimensions":["query","device"],"filters":[{"dimension":"country","expression":"che"}],"row_limit":3}`)
	if isErr {
		t.Fatal(text)
	}
	for _, want := range []string{
		"Search performance of sc-domain:example.com, web search, 2026-10-19 to 2026-11-15 (Pacific Time, final data), by query, device; country equals \"che\"; counted by property.",
		"3 rows (the row_limit; there may be more: start_row 3 gives the next).",
		"| query | device | clicks | impressions | ctr | position |",
		`| word\|a | MOBILE | 10 | 100 | 10.00 | 2.3 |`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
	last := f.paths[len(f.paths)-1]
	if last != "POST /api/sites/sc-domain%3Aexample.com/searchAnalytics/query" {
		t.Errorf("path %s", last)
	}
	body := f.bodies[len(f.bodies)-1]
	if body["startDate"] != "2026-10-19" || body["endDate"] != "2026-11-15" || body["type"] != "web" || body["dataState"] != "final" ||
		body["rowLimit"] != float64(3) {
		t.Errorf("body %v", body)
	}
	groups, _ := json.Marshal(body["dimensionFilterGroups"])
	if string(groups) != `[{"filters":[{"dimension":"country","expression":"che","operator":"equals"}],"groupType":"and"}]` {
		t.Errorf("filters %s", groups)
	}

	// Fresh data ends today; dates given are taken as they are.
	run(t, s, Query, `{"site":"example.com","data_state":"all","days":7}`)
	if b := f.bodies[len(f.bodies)-1]; b["startDate"] != "2026-11-11" || b["endDate"] != "2026-11-17" || b["dimensions"] != nil {
		t.Errorf("fresh: %v", b)
	}
	run(t, s, Query, `{"site":"https://shop.example.org/","start_date":"2026-01-01","end_date":"2026-01-31","type":"discover"}`)
	if b := f.bodies[len(f.bodies)-1]; b["startDate"] != "2026-01-01" || b["endDate"] != "2026-01-31" || b["type"] != "discover" {
		t.Errorf("dates: %v", b)
	}
	if p := f.paths[len(f.paths)-1]; p != "POST /api/sites/https%3A%2F%2Fshop.example.org%2F/searchAnalytics/query" {
		t.Errorf("a URL property's path %s", p)
	}

	for args, want := range map[string]string{
		`{"site":"example.com","dimensions":["city"]}`:                                               `no dimension "city"`,
		`{"site":"example.com","start_date":"2026-11-10","end_date":"2026-11-01"}`:                   "is after end_date",
		`{"site":"example.com","start_date":"2026-11-01","days":3}`:                                  "not both",
		`{"site":"example.com","start_date":"1.11.2026"}`:                                            "YYYY-MM-DD",
		`{"site":"example.com","filters":[{"dimension":"date","expression":"x"}]}`:                   "cannot test",
		`{"site":"example.com","filters":[{"dimension":"page","operator":"like","expression":"x"}]}`: `no operator "like"`,
		`{"site":"example.com","row_limit":30000}`:                                                   "row_limit is between",
		`{"site":"example.com","colour":"red"}`:                                                      `unknown field "colour"`,
		`{}`:                                                                                         "say which property",
		`{"site":"https://gone.example.net/"}`:                                                       "no property covers",
	} {
		if text, isErr := run(t, s, Query, args); !isErr || !strings.Contains(text, want) {
			t.Errorf("%s: %v %s", args, isErr, text)
		}
	}
}

func TestQuerySavesBigResults(t *testing.T) {
	s, f := newTestServer(t)
	f.rows = 600
	text, isErr := run(t, s, Query, `{"site":"example.com","dimensions":["query","device"],"row_limit":1000}`)
	if isErr {
		t.Fatal(text)
	}
	dir := filepath.Join(s.deps.DataDir, "mcp", Name, "chats", "chat1")
	path := filepath.Join(dir, "query-20261117-120000.csv")
	if !strings.Contains(text, "600 rows, saved as CSV: "+path+"; the first 50 here.") {
		t.Fatalf("answer:\n%s", text[:400])
	}
	if n := strings.Count(text, "\n| word"); n != 50 {
		t.Errorf("%d rows shown", n)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 601 || lines[0] != "query,device,clicks,impressions,ctr,position" || lines[1] != "word|a,MOBILE,10,100,0.1,2.345" {
		t.Errorf("csv: %d lines, %q %q", len(lines), lines[0], lines[1])
	}
	// A second one the same second gets a name of its own.
	f.rows = 2
	text, _ = run(t, s, Query, `{"site":"example.com","save":true}`)
	if !strings.Contains(text, "query-20261117-120000-2.csv") {
		t.Errorf("second file: %s", text)
	}
	if got := s.End(context.Background(), "chat1"); got != "the saved query results were removed" {
		t.Errorf("end: %q", got)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the chat's folder is still there")
	}
	if got := s.End(context.Background(), "../x"); got != "" {
		t.Errorf("end of a bad id: %q", got)
	}
}

func TestInspectAndSitemaps(t *testing.T) {
	s, f := newTestServer(t)
	text, isErr := run(t, s, Inspect, `{"url":"https://www.example.com/a"}`)
	if isErr {
		t.Fatal(text)
	}
	for _, want := range []string{
		"URL Inspection of https://www.example.com/a (property sc-domain:example.com)",
		"- Verdict: pass", "- Coverage: Submitted and indexed", "- Crawled as: Googlebot smartphone",
		"- Rich results: partial", "  - FAQ: Unnamed item", `    - WARNING: Missing field "name"`,
		"- In Search Console: https://search.google.com/x",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
	if b := f.bodies[len(f.bodies)-1]; b["inspectionUrl"] != "https://www.example.com/a" || b["siteUrl"] != "sc-domain:example.com" || b["languageCode"] != "en-US" {
		t.Errorf("inspect body %v", b)
	}
	if text, isErr := run(t, s, Inspect, `{"url":"example.com/a"}`); !isErr || !strings.Contains(text, "full URL") {
		t.Errorf("a bare URL: %s", text)
	}

	text, isErr = run(t, s, Sitemaps, `{"site":"example.com"}`)
	if isErr || !strings.Contains(text, "1 sitemap of sc-domain:example.com:\n- https://example.com/sitemap.xml (sitemap index), 1 error, 2 warnings, submitted 2026-11-01T10:00:00Z, read 2026-11-16T03:00:00Z\n  - web: 120 submitted\n") {
		t.Errorf("sitemaps:\n%s", text)
	}
}

func TestAccountsAndErrors(t *testing.T) {
	key, _ := keyFile(t)
	s, f := newTestServer(t, map[string]string{"name": "First", "key": key}, map[string]string{"name": "Browser.lol", "key": key})
	if text, isErr := run(t, s, Sites, `{"account":"browser.LOL"}`); isErr || !strings.Contains(text, "account Browser.lol") {
		t.Errorf("by name: %s", text)
	}
	if text, isErr := run(t, s, Sites, `{"account":"seo@proj.iam.gserviceaccount.com"}`); isErr || !strings.Contains(text, "account First") {
		t.Errorf("by address: %s", text)
	}
	if text, isErr := run(t, s, Sites, `{"account":"other"}`); !isErr || !strings.Contains(text, "there is First, Browser.lol") {
		t.Errorf("unknown: %s", text)
	}
	f.sites = append(f.sites, site{URL: "sc-domain:nope.com", Permission: "siteOwner"})
	if text, isErr := run(t, s, Sitemaps, `{"site":"nope.com"}`); !isErr || !strings.Contains(text, "Search Console refused it (403): User does not have sufficient permission") {
		t.Errorf("403: %s", text)
	}
	if text, isErr := run(t, s, "delete", `{}`); !isErr || !strings.Contains(text, "no tool delete") {
		t.Errorf("unknown tool: %s", text)
	}
	none := New(Deps{})
	if r := none.Call(context.Background(), mcpserver.Session{}, Sites, nil); !r.IsError || !strings.Contains(r.Content[0].Text, "Admin → MCP servers → Google Search Console") {
		t.Errorf("no account: %v", r)
	}
	broken := New(Deps{Accounts: func() []mcpserver.Account { return []mcpserver.Account{{Values: map[string]string{"key": "{}"}}} }})
	if r := broken.Call(context.Background(), mcpserver.Session{}, Sites, nil); !r.IsError || !strings.Contains(r.Content[0].Text, "cannot be used") {
		t.Errorf("a broken key: %v", r)
	}
	if _, err := url.Parse(f.srv.URL); err != nil {
		t.Fatal(err)
	}
}
