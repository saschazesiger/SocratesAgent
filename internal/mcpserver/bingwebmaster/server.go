// Package bingwebmaster is the Bing Webmaster Tools MCP server built into
// Socrates: a chat's agent lists the sites an API key may read, their
// clicks and impressions in Bing by query and page, the daily traffic, how
// Bing crawls them, a URL's index facts and the sitemaps.
//
// It calls the Bing Webmaster API (ssl.bing.com/webmaster/api.svc/json) with
// an API key, which Admin keeps sealed as the server's account
// (mcpserver.Accounts); the agent never sees it. The tools only read: none
// submits a URL or changes a site. Like every built-in server it is off in a
// chat until Admin switches it on for every chat or the chat does.
package bingwebmaster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/mcpserver"
)

// Name is the server's name in a chat.
const Name = "bing-webmaster"

// The tools, bare.
const (
	Sites       = "sites"
	Queries     = "queries"
	Pages       = "pages"
	Traffic     = "traffic"
	Crawl       = "crawl"
	CrawlIssues = "crawl_issues"
	URLInfo     = "url_info"
	Sitemaps    = "sitemaps"
)

var tools = []string{Sites, Queries, Pages, Traffic, Crawl, CrawlIssues, URLInfo, Sitemaps}

// Limits of what a call shows.
const (
	defaultDays = 28
	maxDays     = 3 * 366 // Bing keeps about 16 months; more asks for all it has
	defaultRows = 50
	maxRows     = 1000
	callTimeout = 3 * time.Minute
)

// bingAPI is the API's address; Deps may point it elsewhere (tests).
const bingAPI = "https://ssl.bing.com/webmaster/api.svc/json"

// Deps is what the server gets of Socrates.
type Deps struct {
	// Accounts is the accounts Admin keeps for the server.
	Accounts func() []mcpserver.Account
	// Client makes the HTTP requests; nil for one with a minute's timeout.
	Client *http.Client
	// Base is the API's address; "" for Bing's own (tests give their own).
	Base string
	// Now is the time; nil for the clock.
	Now func() time.Time
}

// Server is the Bing Webmaster Tools MCP server.
type Server struct{ deps Deps }

// New is the server.
func New(d Deps) *Server { return &Server{deps: d} }

func (s *Server) Name() string  { return Name }
func (s *Server) Title() string { return "Bing Webmaster Tools" }
func (s *Server) Description() string {
	return "Lets the agent read Bing Webmaster Tools with an API key: the sites, their clicks, impressions and " +
		"positions in Bing by query and page, the daily traffic, crawl statistics and issues, a URL's index facts " +
		"and the sitemaps. Read-only: it submits and changes nothing."
}

var defaultClient = &http.Client{Timeout: time.Minute}

func (s *Server) client() *http.Client {
	if s.deps.Client != nil {
		return s.deps.Client
	}
	return defaultClient
}

func (s *Server) base() string {
	if s.deps.Base != "" {
		return s.deps.Base
	}
	return bingAPI
}

func (s *Server) now() time.Time {
	if s.deps.Now != nil {
		return s.deps.Now()
	}
	return time.Now()
}

// --------------------------------------------------------------- accounts

func (s *Server) AccountFields() []mcpserver.Field {
	return []mcpserver.Field{
		{Name: "name", Label: "Name", Placeholder: "Browser.lol",
			Hint: "What Admin and the agent call this account: the site or project it is for."},
		{Name: "key", Label: "API key", Secret: true, Placeholder: "0123456789abcdef0123456789abcdef",
			Hint: "Bing Webmaster Tools → Settings (the gear) → API access → API key. It reads every site of the " +
				"Bing account that made it."},
	}
}

func (s *Server) AccountLabel(values map[string]string) string {
	if n := strings.TrimSpace(values["name"]); n != "" {
		return n
	}
	return "API key"
}

// CheckAccount lists the key's sites.
func (s *Server) CheckAccount(ctx context.Context, values map[string]string) mcpserver.Check {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	list, err := (call{s: s, key: values["key"]}).sites(ctx)
	if err != nil {
		var r errRefused
		if errors.As(err, &r) {
			return mcpserver.Check{State: mcpserver.AccountInvalid, Detail: err.Error()}
		}
		return mcpserver.Check{State: mcpserver.AccountUnknown, Detail: "Bing could not be asked: " + err.Error()}
	}
	if len(list) == 0 {
		return mcpserver.Check{State: mcpserver.AccountValid, Detail: "the key works; its Bing account has no site yet"}
	}
	names := make([]string, 0, len(list))
	for _, p := range list {
		names = append(names, p.URL)
	}
	sort.Strings(names)
	if len(names) > 5 {
		names = append(names[:5:5], "…")
	}
	return mcpserver.Check{State: mcpserver.AccountValid,
		Detail: "the key works; " + plural(len(list), "site", "sites") + ": " + strings.Join(names, ", ")}
}

// ------------------------------------------------------------------ tools

func (s *Server) Instructions() string {
	return "Bing Webmaster Tools: how the person's sites do in Bing (and the search engines Bing feeds). " +
		"Read-only: you cannot submit URLs or change a site.\n" +
		"- " + Sites + " lists the sites. Every other tool takes a site: as listed (https://www.example.com/), or " +
		"a domain or URL of it; with only one site it may be left out.\n" +
		"- " + Queries + " and " + Pages + " are the top search queries and pages with their clicks, impressions, " +
		"CTR and average position, over the last days (28 by default). Bing counts them by week and keeps its top " +
		"rows of each week, so they are not complete totals. " + Queries + " with a page gives the queries of that " +
		"page, " + Pages + " with a query the pages of that query.\n" +
		"- " + Traffic + " is clicks and impressions of the whole site by day, " + Crawl + " Bing's crawling by day " +
		"(pages crawled, errors, in the index, HTTP codes), " + CrawlIssues + " the URLs it had trouble with, " +
		URLInfo + " what Bing knows of one URL, " + Sitemaps + " the submitted sitemaps.\n" +
		"- Say which site and which days a number is for."
}

func obj(props map[string]any, required ...string) map[string]any {
	out := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

func num(desc string, min, max int) map[string]any {
	return map[string]any{"type": "integer", "minimum": min, "maximum": max, "description": desc}
}

var (
	accountArg = str("The account to use, by its name (as sites lists it), when Admin set up more than one; the first when left out.")
	siteArg    = str("The site, as sites lists it (https://www.example.com/), or a domain or URL of it. May be left out when there is only one.")
	daysArg    = num("How many days back from today (28).", 1, maxDays)
	maxArg     = num("How many rows to show (50).", 1, maxRows)
)

func (s *Server) Tools() []mcpserver.Tool {
	return []mcpserver.Tool{
		{Name: Sites, Title: "List sites", Description: "The sites of the Bing Webmaster Tools account, verified or not.",
			Schema: obj(map[string]any{"account": accountArg})},
		{Name: Queries, Title: "Top queries", Description: "The search queries the site was shown for in Bing, with " +
			"clicks, impressions, CTR and average position over the days, sorted by clicks; with page, those of one page.",
			Schema: obj(map[string]any{"site": siteArg, "page": str("A page's full URL: the queries of that page only."),
				"days": daysArg, "max": maxArg, "account": accountArg})},
		{Name: Pages, Title: "Top pages", Description: "The site's pages shown in Bing, with clicks, impressions, CTR " +
			"and average position over the days, sorted by clicks; with query, those shown for one query.",
			Schema: obj(map[string]any{"site": siteArg, "query": str("A search query: the pages shown for it only."),
				"days": daysArg, "max": maxArg, "account": accountArg})},
		{Name: Traffic, Title: "Traffic by day", Description: "Clicks and impressions of the whole site in Bing, day by day, with the totals.",
			Schema: obj(map[string]any{"site": siteArg, "days": daysArg, "account": accountArg})},
		{Name: Crawl, Title: "Crawl statistics", Description: "Bing's crawling of the site day by day: pages crawled, " +
			"crawl errors, pages in the index, blocked by robots.txt, HTTP status codes, inbound links.",
			Schema: obj(map[string]any{"site": siteArg, "days": daysArg, "account": accountArg})},
		{Name: CrawlIssues, Title: "Crawl issues", Description: "The URLs Bing had trouble crawling, with the issue and HTTP code.",
			Schema: obj(map[string]any{"site": siteArg, "max": maxArg, "account": accountArg})},
		{Name: URLInfo, Title: "URL facts", Description: "What Bing knows of one URL: when it found and last crawled it, " +
			"its HTTP status, size and links.",
			Schema: obj(map[string]any{"url": str("The page's full URL."), "site": str("The site it belongs to; the one it is on when left out."),
				"account": accountArg}, "url")},
		{Name: Sitemaps, Title: "List sitemaps", Description: "The sitemaps and feeds submitted for the site: status, " +
			"when submitted and crawled, how many URLs.",
			Schema: obj(map[string]any{"site": siteArg, "account": accountArg})},
	}
}

func decode(args json.RawMessage, v any) error {
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}
	dec := json.NewDecoder(strings.NewReader(string(args)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("the arguments could not be read: %s", strings.TrimPrefix(err.Error(), "json: "))
	}
	return nil
}

func (s *Server) Call(ctx context.Context, sess mcpserver.Session, tool string, args json.RawMessage) mcpserver.Result {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	if !slices.Contains(tools, tool) {
		return mcpserver.Error("Bing Webmaster Tools has no tool " + tool + ".")
	}
	var acct struct {
		Account string `json:"account"`
	}
	_ = json.Unmarshal(args, &acct)
	a, err := s.account(acct.Account)
	if err != nil {
		return mcpserver.Error(err.Error())
	}
	c := call{s: s, key: a.Values["key"], name: s.AccountLabel(a.Values)}
	var text string
	switch tool {
	case Sites:
		text, err = c.listSites(ctx, args)
	case Queries:
		text, err = c.stats(ctx, args, false)
	case Pages:
		text, err = c.stats(ctx, args, true)
	case Traffic:
		text, err = c.traffic(ctx, args)
	case Crawl:
		text, err = c.crawl(ctx, args)
	case CrawlIssues:
		text, err = c.crawlIssues(ctx, args)
	case URLInfo:
		text, err = c.urlInfo(ctx, args)
	case Sitemaps:
		text, err = c.sitemaps(ctx, args)
	}
	if err != nil {
		var r errRefused
		if errors.As(err, &r) {
			return mcpserver.Error(err.Error() + ". The person checks the account in Admin → MCP servers → Bing Webmaster Tools.")
		}
		return mcpserver.Error(err.Error())
	}
	return mcpserver.Text(text)
}

// account is the account a call uses: the one named, or the first.
func (s *Server) account(name string) (mcpserver.Account, error) {
	var list []mcpserver.Account
	if s.deps.Accounts != nil {
		list = s.deps.Accounts()
	}
	if len(list) == 0 {
		return mcpserver.Account{}, errors.New("no API key is set up: the person adds one in Admin → MCP servers → Bing Webmaster Tools")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return list[0], nil
	}
	var have []string
	for _, a := range list {
		if strings.EqualFold(s.AccountLabel(a.Values), name) {
			return a, nil
		}
		have = append(have, s.AccountLabel(a.Values))
	}
	return mcpserver.Account{}, fmt.Errorf("there is no account %s; there is %s", name, strings.Join(have, ", "))
}

// -------------------------------------------------------------------- API

// errRefused is Bing refusing the API key: Admin's check calls it invalid.
type errRefused struct{ why string }

func (e errRefused) Error() string { return e.why }

// call is one tool call: the server and the account's key and name.
type call struct {
	s    *Server
	key  string
	name string
}

// get calls a method of the API with its parameters and decodes its "d".
// The key is in the address, so no error ever shows the address.
func (c call) get(ctx context.Context, method string, params url.Values, out any) error {
	if params == nil {
		params = url.Values{}
	}
	params.Set("apikey", strings.TrimSpace(c.key))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.s.base()+"/"+method+"?"+params.Encode(), nil)
	if err != nil {
		return errors.New("the request could not be made")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.s.client().Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("Bing could not be reached: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return errors.New("Bing's answer could not be read")
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			ErrorCode int    `json:"ErrorCode"`
			Message   string `json:"Message"`
		}
		_ = json.Unmarshal(raw, &e)
		msg := strings.TrimSpace(strings.TrimPrefix(e.Message, "ERROR!!!"))
		switch {
		case msg == "InvalidApiKey" || resp.StatusCode == http.StatusUnauthorized:
			return errRefused{"Bing refused the API key (InvalidApiKey)"}
		case msg == "NotAuthorized":
			return errors.New("Bing does not let this account read that site (NotAuthorized): it is not one of its sites")
		case msg != "":
			return fmt.Errorf("Bing refused it: %s (error %d)", msg, e.ErrorCode)
		}
		return fmt.Errorf("Bing answered %s", resp.Status)
	}
	var wrap struct {
		D json.RawMessage `json:"d"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil {
		return errors.New("Bing's answer could not be read")
	}
	if out == nil || len(wrap.D) == 0 || string(wrap.D) == "null" {
		return nil
	}
	if err := json.Unmarshal(wrap.D, out); err != nil {
		return fmt.Errorf("Bing's answer could not be read: %v", err)
	}
	return nil
}

// msDate is a date as the API writes it: /Date(1754006400000)/, perhaps
// with an offset (/Date(1316156400000-0700)/). The zero time is "never".
type msDate time.Time

func (d *msDate) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*d = msDate{}
		return nil
	}
	s = strings.TrimSuffix(strings.TrimPrefix(s, `\/Date(`), `)\/`)
	s = strings.TrimSuffix(strings.TrimPrefix(s, "/Date("), ")/")
	digits := s
	if i := strings.IndexAny(s[1:], "+-"); i >= 0 {
		digits = s[:i+1]
	}
	ms, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return fmt.Errorf("a date %q", string(b))
	}
	if ms <= 0 {
		*d = msDate{}
		return nil
	}
	*d = msDate(time.UnixMilli(ms).UTC())
	return nil
}

func (d msDate) t() time.Time { return time.Time(d) }

func (d msDate) day() string {
	if d.t().IsZero() {
		return "never"
	}
	return d.t().Format("2006-01-02")
}

func (d msDate) when() string {
	if d.t().IsZero() {
		return "never"
	}
	return d.t().Format("2006-01-02 15:04 UTC")
}

type site struct {
	URL      string `json:"Url"`
	Verified bool   `json:"IsVerified"`
}

func (c call) sites(ctx context.Context) ([]site, error) {
	var list []site
	if err := c.get(ctx, "GetUserSites", nil, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// ------------------------------------------------------------------ sites

func (c call) listSites(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Account string `json:"account"`
	}
	if err := decode(args, &in); err != nil {
		return "", err
	}
	list, err := c.sites(ctx)
	if err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "The Bing account of " + c.name + " has no site.", nil
	}
	sort.Slice(list, func(i, j int) bool { return list[i].URL < list[j].URL })
	var b strings.Builder
	fmt.Fprintf(&b, "%s of the account %s:\n", plural(len(list), "site", "sites"), c.name)
	for _, p := range list {
		state := "verified"
		if !p.Verified {
			state = "not verified (no data)"
		}
		fmt.Fprintf(&b, "- %s (%s)\n", p.URL, state)
	}
	return b.String(), nil
}

// resolve is the site a call means: the one named, else the one whose host
// the domain or URL given has (www. or not), else the only one.
func (c call) resolve(ctx context.Context, arg string) (string, error) {
	list, err := c.sites(ctx)
	if err != nil {
		return "", err
	}
	return pickSite(list, arg)
}

func hostOf(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if !strings.Contains(v, "://") {
		v = "https://" + v
	}
	u, err := url.Parse(v)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(u.Hostname(), "www.")
}

func pickSite(list []site, arg string) (string, error) {
	names := func() string {
		var out []string
		for _, p := range list {
			out = append(out, p.URL)
		}
		sort.Strings(out)
		return strings.Join(out, ", ")
	}
	if len(list) == 0 {
		return "", errors.New("the Bing account has no site")
	}
	arg = strings.TrimSpace(arg)
	if arg == "" {
		if len(list) == 1 {
			return list[0].URL, nil
		}
		return "", errors.New("say which site: " + names())
	}
	for _, p := range list {
		if strings.EqualFold(p.URL, arg) {
			return p.URL, nil
		}
	}
	// The longest site whose address the URL starts with, else one of its host.
	lower := strings.ToLower(arg)
	best := ""
	for _, p := range list {
		pu := strings.ToLower(p.URL)
		if (strings.HasPrefix(lower, pu) || lower+"/" == pu) && len(pu) > len(best) {
			best = p.URL
		}
	}
	if best != "" {
		return best, nil
	}
	host := hostOf(arg)
	for _, p := range list {
		if host != "" && hostOf(p.URL) == host {
			return p.URL, nil
		}
	}
	return "", fmt.Errorf("there is no site %s; there is %s", arg, names())
}

// ------------------------------------------------------------------ stats

// stat is a row of query and page statistics: one query (or page) in one
// week.
type stat struct {
	Key         string  `json:"Query"`
	Date        msDate  `json:"Date"`
	Clicks      int64   `json:"Clicks"`
	Impressions int64   `json:"Impressions"`
	ImprPos     float64 `json:"AvgImpressionPosition"`
}

// sum is a query's or page's statistics over the days.
type sum struct {
	key         string
	clicks      int64
	impressions int64
	posWeighted float64 // average position × impressions
	posWeight   int64
}

func (c call) days(n int) (int, time.Time, error) {
	if n == 0 {
		n = defaultDays
	}
	if n < 1 || n > maxDays {
		return 0, time.Time{}, fmt.Errorf("days is between 1 and %d", maxDays)
	}
	today := c.s.now().UTC().Truncate(24 * time.Hour)
	return n, today.AddDate(0, 0, -n), nil
}

func (c call) stats(ctx context.Context, args json.RawMessage, pages bool) (string, error) {
	var in struct {
		Site    string `json:"site"`
		Page    string `json:"page"`
		Query   string `json:"query"`
		Days    int    `json:"days"`
		Max     int    `json:"max"`
		Account string `json:"account"`
	}
	if err := decode(args, &in); err != nil {
		return "", err
	}
	if pages && in.Page != "" {
		return "", errors.New(`the arguments could not be read: unknown field "page" (queries takes a page)`)
	}
	if !pages && in.Query != "" {
		return "", errors.New(`the arguments could not be read: unknown field "query" (pages takes a query)`)
	}
	if in.Max < 0 || in.Max > maxRows {
		return "", fmt.Errorf("max is between 1 and %d", maxRows)
	}
	if in.Max == 0 {
		in.Max = defaultRows
	}
	days, since, err := c.days(in.Days)
	if err != nil {
		return "", err
	}
	siteArg := in.Site
	if siteArg == "" && in.Page != "" {
		siteArg = in.Page
	}
	siteURL, err := c.resolve(ctx, siteArg)
	if err != nil {
		return "", err
	}
	params := url.Values{"siteUrl": {siteURL}}
	method, what, of := "GetQueryStats", "queries", ""
	switch {
	case pages && in.Query != "":
		method, what, of = "GetQueryPageStats", "pages", fmt.Sprintf(" shown for the query %q", in.Query)
		params.Set("query", in.Query)
	case pages:
		method, what = "GetPageStats", "pages"
	case in.Page != "":
		method, of = "GetPageQueryStats", " of the page "+in.Page
		params.Set("page", in.Page)
	}
	var rows []stat
	if err := c.get(ctx, method, params, &rows); err != nil {
		return "", err
	}
	byKey := map[string]*sum{}
	var weeks []time.Time
	for _, r := range rows {
		if r.Date.t().Before(since) {
			continue
		}
		if !slices.ContainsFunc(weeks, func(t time.Time) bool { return t.Equal(r.Date.t()) }) {
			weeks = append(weeks, r.Date.t())
		}
		s := byKey[r.Key]
		if s == nil {
			s = &sum{key: r.Key}
			byKey[r.Key] = s
		}
		s.clicks += r.Clicks
		s.impressions += r.Impressions
		if r.ImprPos > 0 && r.Impressions > 0 {
			s.posWeighted += r.ImprPos * float64(r.Impressions)
			s.posWeight += r.Impressions
		}
	}
	list := make([]*sum, 0, len(byKey))
	for _, s := range byKey {
		list = append(list, s)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].clicks != list[j].clicks {
			return list[i].clicks > list[j].clicks
		}
		if list[i].impressions != list[j].impressions {
			return list[i].impressions > list[j].impressions
		}
		return list[i].key < list[j].key
	})
	sort.Slice(weeks, func(i, j int) bool { return weeks[i].Before(weeks[j]) })
	shown := list
	if len(shown) > in.Max {
		shown = shown[:in.Max]
	}
	return table(what, of, siteURL, days, weeks, list, shown), nil
}

// table writes the statistics' answer.
func table(what, of, siteURL string, days int, weeks []time.Time, list, shown []*sum) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Top %s%s of %s in Bing, the last %d days", what, of, siteURL, days)
	if len(weeks) > 0 {
		fmt.Fprintf(&b, " (%s, from the week of %s to that of %s)", plural(len(weeks), "week", "weeks"),
			weeks[0].Format("2006-01-02"), weeks[len(weeks)-1].Format("2006-01-02"))
	}
	b.WriteString(".\n")
	if len(list) == 0 {
		b.WriteString("No data in these days.\n")
		return b.String()
	}
	one := "query"
	if what == "pages" {
		one = "page"
	}
	fmt.Fprintf(&b, "%s", plural(len(list), one, what))
	if len(shown) < len(list) {
		fmt.Fprintf(&b, ", the first %d here", len(shown))
	}
	b.WriteString(". Bing keeps its top rows of each week only. CTR in percent, position is the average (1 is the top).\n\n")
	fmt.Fprintf(&b, "| %s | clicks | impressions | ctr | position |\n| --- | --- | --- | --- | --- |\n", one)
	for _, s := range shown {
		ctr, pos := "", ""
		if s.impressions > 0 {
			ctr = strconv.FormatFloat(float64(s.clicks)*100/float64(s.impressions), 'f', 2, 64)
		}
		if s.posWeight > 0 {
			pos = strconv.FormatFloat(s.posWeighted/float64(s.posWeight), 'f', 1, 64)
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %s | %s |\n", cell(s.key), s.clicks, s.impressions, ctr, pos)
	}
	return b.String()
}

func cell(v string) string {
	v = strings.Join(strings.Fields(v), " ")
	return strings.ReplaceAll(v, "|", `\|`)
}

// ---------------------------------------------------------------- traffic

func (c call) traffic(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Site    string `json:"site"`
		Days    int    `json:"days"`
		Account string `json:"account"`
	}
	if err := decode(args, &in); err != nil {
		return "", err
	}
	days, since, err := c.days(in.Days)
	if err != nil {
		return "", err
	}
	siteURL, err := c.resolve(ctx, in.Site)
	if err != nil {
		return "", err
	}
	var rows []struct {
		Date        msDate `json:"Date"`
		Clicks      int64  `json:"Clicks"`
		Impressions int64  `json:"Impressions"`
	}
	if err := c.get(ctx, "GetRankAndTrafficStats", url.Values{"siteUrl": {siteURL}}, &rows); err != nil {
		return "", err
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Date.t().Before(rows[j].Date.t()) })
	var b strings.Builder
	fmt.Fprintf(&b, "Traffic of %s in Bing by day, the last %d days.\n", siteURL, days)
	var clicks, impressions int64
	var lines []string
	for _, r := range rows {
		if r.Date.t().Before(since) {
			continue
		}
		clicks += r.Clicks
		impressions += r.Impressions
		lines = append(lines, fmt.Sprintf("| %s | %d | %d | %s |", r.Date.day(), r.Clicks, r.Impressions, pct(r.Clicks, r.Impressions)))
	}
	if len(lines) == 0 {
		b.WriteString("No data in these days.\n")
		return b.String(), nil
	}
	fmt.Fprintf(&b, "In all, %s: %d clicks, %d impressions, CTR %s%%.\n\n", plural(len(lines), "day", "days"), clicks, impressions, pct(clicks, impressions))
	b.WriteString("| day | clicks | impressions | ctr |\n| --- | --- | --- | --- |\n")
	b.WriteString(strings.Join(lines, "\n") + "\n")
	return b.String(), nil
}

func pct(part, whole int64) string {
	if whole == 0 {
		return ""
	}
	return strconv.FormatFloat(float64(part)*100/float64(whole), 'f', 2, 64)
}

// ------------------------------------------------------------------ crawl

func (c call) crawl(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Site    string `json:"site"`
		Days    int    `json:"days"`
		Account string `json:"account"`
	}
	if err := decode(args, &in); err != nil {
		return "", err
	}
	days, since, err := c.days(in.Days)
	if err != nil {
		return "", err
	}
	siteURL, err := c.resolve(ctx, in.Site)
	if err != nil {
		return "", err
	}
	var rows []struct {
		Date      msDate `json:"Date"`
		Crawled   int64  `json:"CrawledPages"`
		Errors    int64  `json:"CrawlErrors"`
		InIndex   int64  `json:"InIndex"`
		InLinks   int64  `json:"InLinks"`
		Robots    int64  `json:"BlockedByRobotsTxt"`
		Malware   int64  `json:"ContainsMalware"`
		Code2xx   int64  `json:"Code2xx"`
		Code301   int64  `json:"Code301"`
		Code302   int64  `json:"Code302"`
		Code4xx   int64  `json:"Code4xx"`
		Code5xx   int64  `json:"Code5xx"`
		Other     int64  `json:"AllOtherCodes"`
		Timeouts  int64  `json:"ConnectionTimeout"`
		DNSFailed int64  `json:"DnsFailures"`
	}
	if err := c.get(ctx, "GetCrawlStats", url.Values{"siteUrl": {siteURL}}, &rows); err != nil {
		return "", err
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Date.t().Before(rows[j].Date.t()) })
	var b strings.Builder
	fmt.Fprintf(&b, "Bing's crawling of %s by day, the last %d days (Bing keeps about six months).\n\n", siteURL, days)
	b.WriteString("| day | crawled | errors | in index | robots.txt blocked | 2xx | 301 | 302 | 4xx | 5xx | other | timeouts | DNS failures | malware | inbound links |\n")
	b.WriteString("|" + strings.Repeat(" --- |", 15) + "\n")
	n := 0
	for _, r := range rows {
		if r.Date.t().Before(since) {
			continue
		}
		n++
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d |\n", r.Date.day(),
			r.Crawled, r.Errors, r.InIndex, r.Robots, r.Code2xx, r.Code301, r.Code302, r.Code4xx, r.Code5xx, r.Other,
			r.Timeouts, r.DNSFailed, r.Malware, r.InLinks)
	}
	if n == 0 {
		return fmt.Sprintf("Bing's crawling of %s: no data in the last %d days.\n", siteURL, days), nil
	}
	return b.String(), nil
}

func (c call) crawlIssues(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Site    string `json:"site"`
		Max     int    `json:"max"`
		Account string `json:"account"`
	}
	if err := decode(args, &in); err != nil {
		return "", err
	}
	if in.Max < 0 || in.Max > maxRows {
		return "", fmt.Errorf("max is between 1 and %d", maxRows)
	}
	if in.Max == 0 {
		in.Max = defaultRows
	}
	siteURL, err := c.resolve(ctx, in.Site)
	if err != nil {
		return "", err
	}
	var rows []struct {
		URL      string          `json:"Url"`
		Issues   json.RawMessage `json:"Issues"`
		HTTPCode int             `json:"HttpCode"`
		InLinks  int64           `json:"InLinks"`
	}
	if err := c.get(ctx, "GetCrawlIssues", url.Values{"siteUrl": {siteURL}}, &rows); err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "Bing reports no crawl issue for " + siteURL + ".", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s Bing had trouble crawling on %s", plural(len(rows), "URL", "URLs"), siteURL)
	if len(rows) > in.Max {
		fmt.Fprintf(&b, ", the first %d here", in.Max)
		rows = rows[:in.Max]
	}
	b.WriteString(":\n\n| URL | issues | HTTP code | inbound links |\n| --- | --- | --- | --- |\n")
	for _, r := range rows {
		issues := strings.Trim(string(r.Issues), `"`)
		fmt.Fprintf(&b, "| %s | %s | %d | %d |\n", cell(r.URL), cell(issues), r.HTTPCode, r.InLinks)
	}
	return b.String(), nil
}

// -------------------------------------------------------------------- url

func (c call) urlInfo(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		URL     string `json:"url"`
		Site    string `json:"site"`
		Account string `json:"account"`
	}
	if err := decode(args, &in); err != nil {
		return "", err
	}
	in.URL = strings.TrimSpace(in.URL)
	if u, err := url.Parse(in.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("url %q is not a page's full URL (https://…)", in.URL)
	}
	site := in.Site
	if site == "" {
		site = in.URL
	}
	siteURL, err := c.resolve(ctx, site)
	if err != nil {
		return "", err
	}
	var r struct {
		URL         string `json:"Url"`
		HTTPStatus  int    `json:"HttpStatus"`
		LastCrawled msDate `json:"LastCrawledDate"`
		Discovered  msDate `json:"DiscoveryDate"`
		Size        int64  `json:"DocumentSize"`
		Anchors     int64  `json:"AnchorCount"`
		Children    int64  `json:"TotalChildUrlCount"`
		IsPage      bool   `json:"IsPage"`
	}
	if err := c.get(ctx, "GetUrlInfo", url.Values{"siteUrl": {siteURL}, "url": {in.URL}}, &r); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "What Bing knows of %s (site %s):\n", in.URL, siteURL)
	if r.Discovered.t().IsZero() && r.LastCrawled.t().IsZero() {
		b.WriteString("- Bing has not found or crawled it.\n")
		return b.String(), nil
	}
	fmt.Fprintf(&b, "- Found: %s\n- Last crawled: %s\n", r.Discovered.day(), r.LastCrawled.when())
	if r.HTTPStatus != 0 {
		fmt.Fprintf(&b, "- HTTP status: %d\n", r.HTTPStatus)
	}
	fmt.Fprintf(&b, "- Size: %d bytes\n- Links to it (anchors): %d\n", r.Size, r.Anchors)
	if !r.IsPage {
		fmt.Fprintf(&b, "- A folder of %s\n", plural(int(r.Children), "URL", "URLs"))
	}
	return b.String(), nil
}

// --------------------------------------------------------------- sitemaps

func (c call) sitemaps(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Site    string `json:"site"`
		Account string `json:"account"`
	}
	if err := decode(args, &in); err != nil {
		return "", err
	}
	siteURL, err := c.resolve(ctx, in.Site)
	if err != nil {
		return "", err
	}
	var rows []struct {
		URL         string `json:"Url"`
		Type        string `json:"Type"`
		Status      string `json:"Status"`
		Submitted   msDate `json:"Submitted"`
		LastCrawled msDate `json:"LastCrawled"`
		URLCount    int64  `json:"UrlCount"`
		Compressed  bool   `json:"Compressed"`
		FileSize    int64  `json:"FileSize"`
	}
	if err := c.get(ctx, "GetFeeds", url.Values{"siteUrl": {siteURL}}, &rows); err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "No sitemap is submitted for " + siteURL + " in Bing.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s of %s in Bing:\n", plural(len(rows), "sitemap", "sitemaps"), siteURL)
	for _, r := range rows {
		fmt.Fprintf(&b, "- %s (%s): %s, %s, submitted %s, crawled %s, %d bytes\n", r.URL, strings.ToLower(r.Type),
			r.Status, plural(int(r.URLCount), "URL", "URLs"), r.Submitted.when(), r.LastCrawled.when(), r.FileSize)
	}
	return b.String(), nil
}

// plural is a count with its noun.
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
