package bingwebmaster

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/mcpserver"
)

const testKey = "0123456789abcdef0123456789abcdef"

// fakeBing is enough of the Bing Webmaster API's JSON endpoint.
type fakeBing struct {
	srv *httptest.Server

	mu    sync.Mutex
	calls []string // method?params without the key
}

func msAt(day string) string {
	t, _ := time.Parse("2006-01-02", day)
	return fmt.Sprintf(`"\/Date(%d)\/"`, t.UnixMilli())
}

func newFakeBing(t *testing.T) *fakeBing {
	f := &fakeBing{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("apikey") != testKey {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"ErrorCode":3,"Message":"ERROR!!! InvalidApiKey"}`)
			return
		}
		q.Del("apikey")
		method := strings.TrimPrefix(r.URL.Path, "/")
		f.mu.Lock()
		f.calls = append(f.calls, method+"?"+q.Encode())
		f.mu.Unlock()
		if s := q.Get("siteUrl"); s != "" && s != "https://www.example.com/" && s != "https://blog.example.org/" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"ErrorCode":14,"Message":"ERROR!!! NotAuthorized"}`)
			return
		}
		var d string
		switch method {
		case "GetUserSites":
			d = `[{"Url":"https://www.example.com/","IsVerified":true},{"Url":"https://blog.example.org/","IsVerified":false}]`
		case "GetQueryStats", "GetPageQueryStats":
			d = `[` +
				`{"Query":"old","Date":` + msAt("2026-09-01") + `,"Clicks":999,"Impressions":1000,"AvgImpressionPosition":1,"AvgClickPosition":-1},` +
				`{"Query":"shoes","Date":` + msAt("2026-11-06") + `,"Clicks":10,"Impressions":100,"AvgImpressionPosition":2,"AvgClickPosition":-1},` +
				`{"Query":"shoes","Date":` + msAt("2026-11-13") + `,"Clicks":20,"Impressions":300,"AvgImpressionPosition":4,"AvgClickPosition":-1},` +
				`{"Query":"red | shoes","Date":` + msAt("2026-11-13") + `,"Clicks":20,"Impressions":50,"AvgImpressionPosition":3,"AvgClickPosition":-1},` +
				`{"Query":"socks","Date":` + msAt("2026-11-13") + `,"Clicks":1,"Impressions":9,"AvgImpressionPosition":7,"AvgClickPosition":-1}]`
		case "GetPageStats", "GetQueryPageStats":
			d = `[{"Query":"https://www.example.com/a","Date":` + msAt("2026-11-13") + `,"Clicks":5,"Impressions":50,"AvgImpressionPosition":3,"AvgClickPosition":-1}]`
		case "GetRankAndTrafficStats":
			d = `[{"Date":` + msAt("2026-11-15") + `,"Clicks":30,"Impressions":300},{"Date":` + msAt("2026-11-14") + `,"Clicks":10,"Impressions":100},` +
				`{"Date":` + msAt("2026-10-01") + `,"Clicks":5,"Impressions":5}]`
		case "GetCrawlStats":
			d = `[{"Date":` + msAt("2026-11-15") + `,"CrawledPages":201,"CrawlErrors":14,"InIndex":256,"InLinks":1587,"BlockedByRobotsTxt":179,` +
				`"Code2xx":151,"Code301":58,"Code302":1,"Code4xx":7,"Code5xx":0,"AllOtherCodes":327,"ConnectionTimeout":0,"DnsFailures":0,"ContainsMalware":0}]`
		case "GetCrawlIssues":
			d = `[{"Url":"https://www.example.com/broken","Issues":"Code4xx","HttpCode":404,"InLinks":3}]`
		case "GetUrlInfo":
			if strings.HasSuffix(q.Get("url"), "/new") {
				d = `{"Url":"x","DiscoveryDate":"\/Date(-62135596800000)\/","LastCrawledDate":"\/Date(-62135596800000)\/","IsPage":true}`
			} else {
				d = `{"Url":"x","HttpStatus":200,"DiscoveryDate":` + msAt("2023-01-16") + `,"LastCrawledDate":"\/Date(1794883941000-0700)\/",` +
					`"DocumentSize":391,"AnchorCount":386,"IsPage":true}`
			}
		case "GetFeeds":
			d = `[{"Url":"https://www.example.com/sitemap.xml","Type":"Sitemap","Status":"Failed","Submitted":` + msAt("2026-04-27") +
				`,"LastCrawled":` + msAt("2026-11-09") + `,"UrlCount":0,"FileSize":0}]`
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, `{"d":`+d+`}`)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func newTestServer(t *testing.T, accounts ...map[string]string) (*Server, *fakeBing) {
	f := newFakeBing(t)
	if len(accounts) == 0 {
		accounts = []map[string]string{{"name": "Browser.lol", "key": testKey}}
	}
	s := New(Deps{
		Accounts: func() []mcpserver.Account {
			var out []mcpserver.Account
			for i, v := range accounts {
				out = append(out, mcpserver.Account{ID: string(rune('a' + i)), Values: v})
			}
			return out
		},
		Base: f.srv.URL,
		Now:  func() time.Time { return time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC) },
	})
	return s, f
}

func run(t *testing.T, s *Server, tool, args string) (string, bool) {
	t.Helper()
	r := s.Call(context.Background(), mcpserver.Session{ID: "chat1"}, tool, json.RawMessage(args))
	return r.Content[0].Text, r.IsError
}

func TestCheckAccount(t *testing.T) {
	s, _ := newTestServer(t)
	c := s.CheckAccount(context.Background(), map[string]string{"name": "Browser.lol", "key": testKey})
	if c.State != mcpserver.AccountValid || c.Detail != "the key works; 2 sites: https://blog.example.org/, https://www.example.com/" {
		t.Errorf("check: %+v", c)
	}
	c = s.CheckAccount(context.Background(), map[string]string{"name": "x", "key": "wrong"})
	if c.State != mcpserver.AccountInvalid || !strings.Contains(c.Detail, "InvalidApiKey") {
		t.Errorf("a wrong key: %+v", c)
	}
	unreachable := New(Deps{Base: "http://127.0.0.1:1"})
	c = unreachable.CheckAccount(context.Background(), map[string]string{"key": "secretkey123"})
	if c.State != mcpserver.AccountUnknown || strings.Contains(c.Detail, "secretkey123") {
		t.Errorf("unreachable (and the key must not show): %+v", c)
	}
	if got := s.AccountLabel(map[string]string{"name": " Browser.lol "}); got != "Browser.lol" {
		t.Errorf("label %q", got)
	}
}

func TestSitesAndPick(t *testing.T) {
	s, _ := newTestServer(t)
	text, isErr := run(t, s, Sites, `{}`)
	if isErr || text != "2 sites of the account Browser.lol:\n- https://blog.example.org/ (not verified (no data))\n- https://www.example.com/ (verified)\n" {
		t.Errorf("sites: %q", text)
	}
	list := []site{{URL: "https://www.example.com/"}, {URL: "https://www.example.com/shop/"}, {URL: "https://blog.example.org/"}}
	for arg, want := range map[string]string{
		"example.com":                    "https://www.example.com/",
		"https://www.example.com/shop/x": "https://www.example.com/shop/",
		"https://www.example.com/a":      "https://www.example.com/",
		"http://example.com/a":           "https://www.example.com/",
		"BLOG.example.org":               "https://blog.example.org/",
		"https://blog.example.org":       "https://blog.example.org/",
		"https://www.example.com/shop/":  "https://www.example.com/shop/",
	} {
		if got, err := pickSite(list, arg); err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", arg, got, err, want)
		}
	}
	if _, err := pickSite(list, ""); err == nil || !strings.Contains(err.Error(), "say which site") {
		t.Errorf("none named: %v", err)
	}
	if _, err := pickSite(list, "other.net"); err == nil || !strings.Contains(err.Error(), "no site other.net") {
		t.Errorf("unknown: %v", err)
	}
}

func TestQueriesAndPages(t *testing.T) {
	s, f := newTestServer(t)
	text, isErr := run(t, s, Queries, `{"site":"example.com","max":2}`)
	if isErr {
		t.Fatal(text)
	}
	for _, want := range []string{
		"Top queries of https://www.example.com/ in Bing, the last 28 days (2 weeks, from the week of 2026-11-06 to that of 2026-11-13).",
		"3 queries, the first 2 here.",
		"| shoes | 30 | 400 | 7.50 | 3.5 |", // position weighted by impressions: (2×100 + 4×300) / 400
		`| red \| shoes | 20 | 50 | 40.00 | 3.0 |`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
	if strings.Contains(text, "socks") || strings.Contains(text, "old") {
		t.Errorf("more than max, or older than the days:\n%s", text)
	}
	run(t, s, Queries, `{"page":"https://www.example.com/a"}`)
	if c := f.calls[len(f.calls)-1]; c != "GetPageQueryStats?page=https%3A%2F%2Fwww.example.com%2Fa&siteUrl=https%3A%2F%2Fwww.example.com%2F" {
		t.Errorf("a page's queries: %s", c)
	}
	text, _ = run(t, s, Pages, `{"site":"https://www.example.com/","query":"shoes"}`)
	if c := f.calls[len(f.calls)-1]; c != "GetQueryPageStats?query=shoes&siteUrl=https%3A%2F%2Fwww.example.com%2F" ||
		!strings.Contains(text, `Top pages shown for the query "shoes"`) || !strings.Contains(text, "1 page.") {
		t.Errorf("a query's pages: %s\n%s", c, text)
	}
	text, _ = run(t, s, Pages, `{"site":"example.com","days":3}`)
	if !strings.Contains(text, "No data in these days.") {
		t.Errorf("days without data:\n%s", text)
	}
	for args, want := range map[string]string{
		`{"site":"example.com","days":0.5}`:  "could not be read",
		`{"site":"example.com","max":5000}`:  "max is between",
		`{"site":"example.com","days":5000}`: "days is between",
		`{}`:                                 "say which site",
		`{"site":"https://other.example/"}`:  "no site",
		`{"site":"example.com","query":"x"}`: `unknown field "query"`,
	} {
		if text, isErr := run(t, s, Queries, args); !isErr || !strings.Contains(text, want) {
			t.Errorf("%s: %v %s", args, isErr, text)
		}
	}
}

func TestTrafficCrawlURLSitemaps(t *testing.T) {
	s, _ := newTestServer(t)
	text, isErr := run(t, s, Traffic, `{"site":"example.com","days":7}`)
	if isErr || !strings.Contains(text, "In all, 2 days: 40 clicks, 400 impressions, CTR 10.00%.") ||
		!strings.Contains(text, "| 2026-11-14 | 10 | 100 | 10.00 |\n| 2026-11-15 | 30 | 300 | 10.00 |") {
		t.Errorf("traffic:\n%s", text)
	}
	text, _ = run(t, s, Crawl, `{"site":"example.com"}`)
	if !strings.Contains(text, "| 2026-11-15 | 201 | 14 | 256 | 179 | 151 | 58 | 1 | 7 | 0 | 327 | 0 | 0 | 0 | 1587 |") {
		t.Errorf("crawl:\n%s", text)
	}
	text, _ = run(t, s, CrawlIssues, `{"site":"example.com"}`)
	if !strings.Contains(text, "1 URL Bing had trouble crawling on https://www.example.com/") || !strings.Contains(text, "| https://www.example.com/broken | Code4xx | 404 | 3 |") {
		t.Errorf("issues:\n%s", text)
	}
	text, _ = run(t, s, URLInfo, `{"url":"https://www.example.com/"}`)
	if text != "What Bing knows of https://www.example.com/ (site https://www.example.com/):\n- Found: 2023-01-16\n- Last crawled: 2026-11-17 02:52 UTC\n"+
		"- HTTP status: 200\n- Size: 391 bytes\n- Links to it (anchors): 386\n" {
		t.Errorf("url info: %q", text)
	}
	text, _ = run(t, s, URLInfo, `{"url":"https://www.example.com/new"}`)
	if !strings.Contains(text, "Bing has not found or crawled it.") {
		t.Errorf("an unknown URL: %q", text)
	}
	if text, isErr := run(t, s, URLInfo, `{"url":"www.example.com/a"}`); !isErr || !strings.Contains(text, "full URL") {
		t.Errorf("a bare URL: %s", text)
	}
	text, _ = run(t, s, Sitemaps, `{"site":"example.com"}`)
	if text != "1 sitemap of https://www.example.com/ in Bing:\n- https://www.example.com/sitemap.xml (sitemap): Failed, 0 URLs, submitted 2026-04-27 00:00 UTC, crawled 2026-11-09 00:00 UTC, 0 bytes\n" {
		t.Errorf("sitemaps: %q", text)
	}
}

func TestAccountsAndErrors(t *testing.T) {
	s, f := newTestServer(t, map[string]string{"name": "Other", "key": "wrong"}, map[string]string{"name": "Browser.lol", "key": testKey})
	if text, isErr := run(t, s, Sites, `{"account":"browser.lol"}`); isErr || !strings.Contains(text, "account Browser.lol") {
		t.Errorf("by name: %s", text)
	}
	text, isErr := run(t, s, Sites, `{}`)
	if !isErr || !strings.Contains(text, "Bing refused the API key (InvalidApiKey). The person checks the account in Admin") || strings.Contains(text, "wrong") {
		t.Errorf("the first, a wrong key: %s", text)
	}
	if text, isErr := run(t, s, Sites, `{"account":"nobody"}`); !isErr || !strings.Contains(text, "there is Other, Browser.lol") {
		t.Errorf("unknown: %s", text)
	}
	c := call{s: s, key: testKey}
	if err := c.get(context.Background(), "GetFeeds", map[string][]string{"siteUrl": {"https://not.mine/"}}, nil); err == nil || !strings.Contains(err.Error(), "NotAuthorized") {
		t.Errorf("not one's site: %v", err)
	}
	if text, isErr := run(t, s, "SubmitUrl", `{}`); !isErr || !strings.Contains(text, "no tool SubmitUrl") {
		t.Errorf("unknown tool: %s", text)
	}
	for _, call := range f.calls {
		if strings.Contains(call, "Submit") {
			t.Errorf("a call that changes something: %s", call)
		}
	}
	none := New(Deps{})
	if r := none.Call(context.Background(), mcpserver.Session{}, Sites, nil); !r.IsError || !strings.Contains(r.Content[0].Text, "Admin → MCP servers → Bing Webmaster Tools") {
		t.Errorf("no account: %v", r)
	}
}

func TestDates(t *testing.T) {
	for raw, want := range map[string]string{
		`"\/Date(1754006400000)\/"`:    "2025-08-01",
		`"/Date(1316156400000-0700)/"`: "2011-09-16",
		`"\/Date(-62135596800000)\/"`:  "never",
		`null`:                         "never",
	} {
		var d msDate
		if err := json.Unmarshal([]byte(raw), &d); err != nil || d.day() != want {
			t.Errorf("%s: %q %v, want %s", raw, d.day(), err, want)
		}
	}
}
