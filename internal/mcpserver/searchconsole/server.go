// Package searchconsole is the Google Search Console MCP server built into
// Socrates: a chat's agent lists the properties a service account may read,
// queries their search performance (clicks, impressions, CTR, position by
// query, page, country, device, date …), inspects a URL's index status and
// lists the sitemaps.
//
// It signs in as a Google Cloud service account, whose JSON key Admin keeps
// sealed as the server's account (mcpserver.Accounts); the agent never sees
// it. The scope is read-only: it changes nothing in Search Console. Like
// every built-in server it is off in a chat until Admin switches it on for
// every chat or the chat does.
package searchconsole

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/mcpserver"
)

// Name is the server's name in a chat.
const Name = "search-console"

// The tools, bare.
const (
	Sites    = "sites"
	Query    = "query"
	Inspect  = "inspect"
	Sitemaps = "sitemaps"
)

// Limits of what a call reads and shows.
const (
	defaultRows = 100
	maxRows     = 25000
	inlineRows  = 500 // more than these go into a CSV file
	previewRows = 50  // shown of a CSV file
	defaultDays = 28
	maxDays     = 16 * 31 // Search Console keeps 16 months
	callTimeout = 3 * time.Minute
)

// Deps is what the server gets of Socrates.
type Deps struct {
	// DataDir is the data directory: a chat's saved query results go below
	// it. "" for a server that runs nothing (the names read before).
	DataDir string
	// Accounts is the accounts Admin keeps for the server.
	Accounts func() []mcpserver.Account
	// Client makes the HTTP requests; nil for one with a minute's timeout.
	Client *http.Client
	// TokenURL, APIBase and InspectBase are Google's addresses; "" for
	// Google's own (tests give their own).
	TokenURL, APIBase, InspectBase string
	// Now is the time; nil for the clock.
	Now func() time.Time
}

// Server is the Google Search Console MCP server.
type Server struct {
	deps Deps

	mu     sync.Mutex
	tokens map[string]token // by service account and key id
}

// New is the server.
func New(d Deps) *Server { return &Server{deps: d, tokens: map[string]token{}} }

func (s *Server) Name() string  { return Name }
func (s *Server) Title() string { return "Google Search Console" }
func (s *Server) Description() string {
	return "Lets the agent read Google Search Console with a service account: the properties, their clicks, " +
		"impressions, CTR and positions by query, page, country, device and date, a URL's index status and the " +
		"sitemaps. Read-only: it changes nothing in Search Console."
}

var defaultClient = &http.Client{Timeout: time.Minute}

func (s *Server) client() *http.Client {
	if s.deps.Client != nil {
		return s.deps.Client
	}
	return defaultClient
}

func (s *Server) tokenURL() string    { return or(s.deps.TokenURL, googleToken) }
func (s *Server) apiBase() string     { return or(s.deps.APIBase, googleAPI) }
func (s *Server) inspectBase() string { return or(s.deps.InspectBase, googleInspect) }

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (s *Server) now() time.Time {
	if s.deps.Now != nil {
		return s.deps.Now()
	}
	return time.Now()
}

// pacific is Search Console's time zone: its days are Pacific Time's.
var pacific = func() *time.Location {
	if l, err := time.LoadLocation("America/Los_Angeles"); err == nil {
		return l
	}
	return time.FixedZone("PT", -8*3600)
}()

// --------------------------------------------------------------- accounts

func (s *Server) AccountFields() []mcpserver.Field {
	return []mcpserver.Field{
		{Name: "name", Label: "Name", Placeholder: "Browser.lol",
			Hint: "What Admin and the agent call this account: the site or project it is for."},
		{Name: "key", Label: "Service account key (JSON)", Secret: true, Placeholder: `{"type": "service_account", …}`,
			Hint: "Paste the whole JSON key file of a Google Cloud service account (IAM → Service accounts → Keys → " +
				"Add key → JSON). In Search Console, add the service account's address as a user of each property " +
				"(Settings → Users and permissions); Restricted is enough."},
	}
}

func (s *Server) AccountLabel(values map[string]string) string {
	if n := strings.TrimSpace(values["name"]); n != "" {
		return n
	}
	if e := emailOf(values["key"]); e != "" {
		return e
	}
	return "service account"
}

// CheckAccount signs in as the service account and lists its properties.
func (s *Server) CheckAccount(ctx context.Context, values map[string]string) mcpserver.Check {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	k, err := parseKey(values["key"])
	if err != nil {
		return mcpserver.Check{State: mcpserver.AccountInvalid, Detail: err.Error()}
	}
	s.forget(k) // a check signs in anew: a kept token outlives a deleted key
	list, err := api{s: s, key: k}.sites(ctx)
	if err != nil {
		if isRefused(err) {
			return mcpserver.Check{State: mcpserver.AccountInvalid, Detail: err.Error()}
		}
		var ae apiError
		if errors.As(err, &ae) && ae.code == http.StatusForbidden {
			return mcpserver.Check{State: mcpserver.AccountInvalid, Detail: err.Error() +
				" (is the Search Console API enabled in the service account's Google Cloud project?)"}
		}
		return mcpserver.Check{State: mcpserver.AccountUnknown, Detail: "Search Console could not be asked: " + err.Error()}
	}
	if len(list) == 0 {
		return mcpserver.Check{State: mcpserver.AccountValid, Detail: "signed in; no property yet: add " + k.ClientEmail +
			" as a user of a property in Search Console"}
	}
	names := make([]string, 0, len(list))
	for _, p := range list {
		names = append(names, p.URL)
	}
	sort.Strings(names)
	what := "properties"
	if len(names) == 1 {
		what = "property"
	}
	shown := names
	if len(shown) > 5 {
		shown = append(shown[:5:5], "…")
	}
	return mcpserver.Check{State: mcpserver.AccountValid,
		Detail: fmt.Sprintf("signed in; %d %s: %s", len(names), what, strings.Join(shown, ", "))}
}

// ------------------------------------------------------------------ tools

func (s *Server) Instructions() string {
	return "Google Search Console: how the person's sites do in Google Search. Read-only: you cannot change a " +
		"property, its users or its sitemaps.\n" +
		"- " + Sites + " lists the properties you may read: a domain property (sc-domain:example.com, every " +
		"protocol and subdomain) or a URL-prefix property (https://www.example.com/). Every other tool takes a " +
		"property as site: its exact name, or a domain or URL, which picks the property that covers it; with only " +
		"one property it may be left out.\n" +
		"- " + Query + " is the Performance report: clicks, impressions, CTR and average position, grouped by " +
		"dimensions (query, page, country, device, searchAppearance, date, hour) and filtered. Without dimensions " +
		"it gives the totals; totals of rows grouped by query leave out anonymized queries, so ask for totals " +
		"without dimensions. Dates are Pacific Time; the default is the last 28 days of final data (it lags two to " +
		"three days; data_state \"all\" adds the fresh days). Rows come sorted by clicks. A big result is saved " +
		"as a CSV file whose path the answer gives.\n" +
		"- " + Inspect + " is the URL Inspection: whether Google indexed a URL, why not, when it crawled it, the " +
		"canonical it chose, its rich results. Google allows about 2000 inspections a day per property.\n" +
		"- " + Sitemaps + " lists a property's submitted sitemaps with their errors, warnings and counts.\n" +
		"- Say which property and which dates a number is for; CTR and position are averages, not sums."
}

func obj(props map[string]any, required ...string) map[string]any {
	out := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

func enum(desc string, values ...string) map[string]any {
	return map[string]any{"type": "string", "enum": values, "description": desc}
}

var (
	accountArg = str("The account to use, by its name (as sites lists it) or its service account's address, when Admin set up more than one; the first when left out.")
	siteArg    = str("The property: as sites lists it (sc-domain:example.com, https://www.example.com/), or a domain or URL " +
		"it covers. May be left out when there is only one.")
	dimensions = []string{"query", "page", "country", "device", "searchAppearance", "date", "hour"}
	operators  = []string{"equals", "notEquals", "contains", "notContains", "includingRegex", "excludingRegex"}
	searchType = []string{"web", "image", "video", "news", "discover", "googleNews"}
)

func (s *Server) Tools() []mcpserver.Tool {
	return []mcpserver.Tool{
		{Name: Sites, Title: "List properties", Description: "The Search Console properties the service account may " +
			"read, with its permission on each.",
			Schema: obj(map[string]any{"account": accountArg})},
		{Name: Query, Title: "Search performance", Description: "Clicks, impressions, CTR and average position of a " +
			"property, grouped by the dimensions asked for and filtered: the Performance report. Rows sorted by clicks.",
			Schema: obj(map[string]any{
				"site":       siteArg,
				"start_date": str("The first day, YYYY-MM-DD (Pacific Time). Left out: days before end_date."),
				"end_date":   str("The last day, YYYY-MM-DD. Left out: the latest final day (two days ago), or today with data_state all."),
				"days":       map[string]any{"type": "integer", "minimum": 1, "maximum": maxDays, "description": "How many days up to end_date, when start_date is left out (28)."},
				"dimensions": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": dimensions},
					"description": "What to group by, in order: query, page, country (ISO 3166-1 alpha-3, deu), device (DESKTOP, MOBILE, TABLET), searchAppearance, date, hour (needs data_state hourly_all). None: the totals."},
				"filters": map[string]any{"type": "array", "description": "Conditions every row meets (all of them).",
					"items": obj(map[string]any{
						"dimension":  enum("The dimension it tests.", "query", "page", "country", "device", "searchAppearance"),
						"operator":   enum("How it tests (equals when left out); a regex is RE2.", operators...),
						"expression": str("The value: a query, a page URL, a country code (che), a device (MOBILE) …"),
					}, "dimension", "expression")},
				"type":        enum("Which results: web search (the default), image, video, news (the News tab), discover, googleNews (news.google.com).", searchType...),
				"data_state":  enum("final (the default): only finished days; all: with the fresh days; hourly_all: by hour.", "final", "all", "hourly_all"),
				"aggregation": enum("How rows are counted: auto (the default), byProperty, byPage.", "auto", "byProperty", "byPage"),
				"row_limit":   map[string]any{"type": "integer", "minimum": 1, "maximum": maxRows, "description": "How many rows (100)."},
				"start_row":   map[string]any{"type": "integer", "minimum": 0, "description": "Rows to skip, to page through more (0)."},
				"save":        map[string]any{"type": "boolean", "description": "Save the rows as a CSV file too (more than 500 rows are always saved)."},
				"account":     accountArg,
			})},
		{Name: Inspect, Title: "Inspect a URL", Description: "The URL Inspection of one page: index verdict and " +
			"coverage, robots.txt, last crawl, the canonical Google chose and the page's own, the sitemaps and links " +
			"it was found by, rich results.",
			Schema: obj(map[string]any{
				"url":      str("The page's full URL."),
				"site":     str("The property it belongs to; the one that covers the URL when left out."),
				"language": str("The language of the answer's messages, as a BCP-47 code (en-US, de-CH); en-US when left out."),
				"account":  accountArg,
			}, "url")},
		{Name: Sitemaps, Title: "List sitemaps", Description: "The sitemaps submitted for a property: when they were " +
			"submitted and read, pending or not, errors, warnings, and how many URLs of each kind they list.",
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

// call is one tool call: the server, the chat, the account's way to the API.
type call struct {
	s     *Server
	sess  mcpserver.Session
	api   api
	email string
	name  string // the account's name in Admin
}

func (s *Server) Call(ctx context.Context, sess mcpserver.Session, tool string, args json.RawMessage) mcpserver.Result {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	if !slices.Contains([]string{Sites, Query, Inspect, Sitemaps}, tool) {
		return mcpserver.Error("Google Search Console has no tool " + tool + ".")
	}
	var acct struct {
		Account string `json:"account"`
	}
	_ = json.Unmarshal(args, &acct)
	a, err := s.account(acct.Account)
	if err != nil {
		return mcpserver.Error(err.Error())
	}
	k, err := parseKey(a.Values["key"])
	if err != nil {
		return mcpserver.Error("The service account's key in Admin cannot be used: " + err.Error() +
			". The person replaces it in Admin → MCP servers → Google Search Console.")
	}
	c := &call{s: s, sess: sess, api: api{s: s, key: k}, email: k.ClientEmail, name: s.AccountLabel(a.Values)}
	var text string
	switch tool {
	case Sites:
		text, err = c.sites(ctx, args)
	case Query:
		text, err = c.query(ctx, args)
	case Inspect:
		text, err = c.inspect(ctx, args)
	case Sitemaps:
		text, err = c.sitemaps(ctx, args)
	}
	if err != nil {
		if isRefused(err) {
			return mcpserver.Error(err.Error() + ". The person checks the account in Admin → MCP servers → Google Search Console.")
		}
		return mcpserver.Error(err.Error())
	}
	return mcpserver.Text(text)
}

// account is the account a call uses: the one named (by its name or its
// service account's address), or the first.
func (s *Server) account(name string) (mcpserver.Account, error) {
	var list []mcpserver.Account
	if s.deps.Accounts != nil {
		list = s.deps.Accounts()
	}
	if len(list) == 0 {
		return mcpserver.Account{}, errors.New("no service account is set up: the person adds one in Admin → MCP servers → Google Search Console")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return list[0], nil
	}
	var have []string
	for _, a := range list {
		if strings.EqualFold(s.AccountLabel(a.Values), name) || strings.EqualFold(emailOf(a.Values["key"]), name) {
			return a, nil
		}
		have = append(have, s.AccountLabel(a.Values))
	}
	return mcpserver.Account{}, fmt.Errorf("there is no account %s; there is %s", name, strings.Join(have, ", "))
}

// End removes the query results saved for a chat.
func (s *Server) End(ctx context.Context, session string) string {
	dir := s.chatDir(session)
	if dir == "" {
		return ""
	}
	if _, err := os.Stat(dir); err != nil {
		return ""
	}
	if err := os.RemoveAll(dir); err != nil {
		return "the saved query results could not be removed: " + err.Error()
	}
	return "the saved query results were removed"
}

func (s *Server) chatDir(session string) string {
	if s.deps.DataDir == "" || session == "" || strings.ContainsAny(session, `/\`) || session == "." || session == ".." {
		return ""
	}
	return filepath.Join(s.deps.DataDir, "mcp", Name, "chats", session)
}

// ------------------------------------------------------------- properties

var permissionWords = map[string]string{
	"siteOwner":          "owner",
	"siteFullUser":       "full user",
	"siteRestrictedUser": "restricted user",
	"siteUnverifiedUser": "unverified (no data)",
}

func (c *call) sites(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Account string `json:"account"`
	}
	if err := decode(args, &in); err != nil {
		return "", err
	}
	list, err := c.api.sites(ctx)
	if err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "The service account " + c.email + " may read no property yet. The person adds it as a user of a " +
			"property in Search Console (Settings → Users and permissions).", nil
	}
	sort.Slice(list, func(i, j int) bool { return list[i].URL < list[j].URL })
	var b strings.Builder
	fmt.Fprintf(&b, "%s the account %s (service account %s) may read:\n", plural(len(list), "property", "properties"), c.name, c.email)
	for _, p := range list {
		fmt.Fprintf(&b, "- %s (%s)\n", p.URL, or(permissionWords[p.Permission], p.Permission))
	}
	return b.String(), nil
}

// resolve is the property a call means: the one named, else the one that
// covers the domain or URL given, else the only one.
func (c *call) resolve(ctx context.Context, arg string) (string, error) {
	list, err := c.api.sites(ctx)
	if err != nil {
		return "", err
	}
	var usable []site
	for _, p := range list {
		if p.Permission != "siteUnverifiedUser" {
			usable = append(usable, p)
		}
	}
	return pickSite(usable, arg, c.email)
}

func pickSite(list []site, arg, email string) (string, error) {
	names := func() string {
		var out []string
		for _, p := range list {
			out = append(out, p.URL)
		}
		sort.Strings(out)
		return strings.Join(out, ", ")
	}
	arg = strings.TrimSpace(arg)
	if len(list) == 0 {
		return "", errors.New("the service account " + email + " may read no property: the person adds it as a user " +
			"of the property in Search Console (Settings → Users and permissions)")
	}
	if arg == "" {
		if len(list) == 1 {
			return list[0].URL, nil
		}
		return "", errors.New("say which property (site): " + names())
	}
	for _, p := range list {
		if strings.EqualFold(p.URL, arg) {
			return p.URL, nil
		}
	}
	lower := strings.ToLower(arg)
	hasScheme := strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
	bare := strings.TrimPrefix(lower, "sc-domain:")
	var host string
	if hasScheme {
		if u, err := url.Parse(arg); err == nil {
			host = strings.ToLower(u.Hostname())
		}
	} else {
		host = strings.SplitN(strings.SplitN(bare, "/", 2)[0], ":", 2)[0]
	}
	if host == "" {
		return "", fmt.Errorf("there is no property %s; there is %s", arg, names())
	}
	// A URL-prefix property covers a URL that starts with it; without a
	// scheme either scheme will do.
	urls := []string{lower}
	if !hasScheme {
		urls = []string{"https://" + bare, "http://" + bare}
	}
	prefix, prefixLen := "", 0
	for _, p := range list {
		pu := strings.ToLower(p.URL)
		if strings.HasPrefix(pu, "sc-domain:") {
			continue
		}
		for _, u := range urls {
			if (strings.HasPrefix(u, pu) || u+"/" == pu) && len(pu) > prefixLen {
				prefix, prefixLen = p.URL, len(pu)
			}
		}
	}
	// A domain property covers its domain and every subdomain.
	domain, domainLen := "", 0
	for _, p := range list {
		pu := strings.ToLower(p.URL)
		d, ok := strings.CutPrefix(pu, "sc-domain:")
		if !ok {
			continue
		}
		if (host == d || strings.HasSuffix(host, "."+d)) && len(d) > domainLen {
			domain, domainLen = p.URL, len(d)
		}
	}
	// A URL means its URL-prefix property; a bare domain the domain
	// property, which has every protocol and subdomain.
	if hasScheme && prefix != "" {
		return prefix, nil
	}
	if domain != "" {
		return domain, nil
	}
	if prefix != "" {
		return prefix, nil
	}
	return "", fmt.Errorf("no property covers %s; there is %s", arg, names())
}

// ------------------------------------------------------------------ query

type filter struct {
	Dimension  string `json:"dimension"`
	Operator   string `json:"operator,omitempty"`
	Expression string `json:"expression"`
}

type queryArgs struct {
	Site        string   `json:"site"`
	StartDate   string   `json:"start_date"`
	EndDate     string   `json:"end_date"`
	Days        int      `json:"days"`
	Dimensions  []string `json:"dimensions"`
	Filters     []filter `json:"filters"`
	Type        string   `json:"type"`
	DataState   string   `json:"data_state"`
	Aggregation string   `json:"aggregation"`
	RowLimit    int      `json:"row_limit"`
	StartRow    int      `json:"start_row"`
	Save        bool     `json:"save"`
	Account     string   `json:"account"`
}

type row struct {
	Keys        []string `json:"keys"`
	Clicks      float64  `json:"clicks"`
	Impressions float64  `json:"impressions"`
	CTR         float64  `json:"ctr"`
	Position    float64  `json:"position"`
}

const day = "2006-01-02"

// period is the days a query covers.
func (c *call) period(in queryArgs) (string, string, error) {
	today := c.s.now().In(pacific)
	end := today.AddDate(0, 0, -2)
	if in.DataState == "all" || in.DataState == "hourly_all" {
		end = today
	}
	if in.EndDate != "" {
		t, err := time.Parse(day, in.EndDate)
		if err != nil {
			return "", "", fmt.Errorf("end_date %q is not a day as YYYY-MM-DD", in.EndDate)
		}
		end = t
	}
	var start time.Time
	if in.StartDate != "" {
		if in.Days != 0 {
			return "", "", errors.New("give start_date or days, not both")
		}
		t, err := time.Parse(day, in.StartDate)
		if err != nil {
			return "", "", fmt.Errorf("start_date %q is not a day as YYYY-MM-DD", in.StartDate)
		}
		start = t
	} else {
		days := in.Days
		if days == 0 {
			days = defaultDays
		}
		if days < 1 || days > maxDays {
			return "", "", fmt.Errorf("days is between 1 and %d", maxDays)
		}
		start = end.AddDate(0, 0, -(days - 1))
	}
	if start.Format(day) > end.Format(day) {
		return "", "", fmt.Errorf("start_date %s is after end_date %s", start.Format(day), end.Format(day))
	}
	return start.Format(day), end.Format(day), nil
}

func (c *call) query(ctx context.Context, args json.RawMessage) (string, error) {
	var in queryArgs
	if err := decode(args, &in); err != nil {
		return "", err
	}
	for _, d := range in.Dimensions {
		if !slices.Contains(dimensions, d) {
			return "", fmt.Errorf("there is no dimension %q; there is %s", d, strings.Join(dimensions, ", "))
		}
	}
	if in.Type != "" && !slices.Contains(searchType, in.Type) {
		return "", fmt.Errorf("there is no type %q; there is %s", in.Type, strings.Join(searchType, ", "))
	}
	if in.DataState != "" && !slices.Contains([]string{"final", "all", "hourly_all"}, in.DataState) {
		return "", fmt.Errorf("data_state is final, all or hourly_all, not %q", in.DataState)
	}
	if in.Aggregation != "" && !slices.Contains([]string{"auto", "byProperty", "byPage"}, in.Aggregation) {
		return "", fmt.Errorf("aggregation is auto, byProperty or byPage, not %q", in.Aggregation)
	}
	if in.RowLimit < 0 || in.RowLimit > maxRows {
		return "", fmt.Errorf("row_limit is between 1 and %d", maxRows)
	}
	if in.StartRow < 0 {
		return "", errors.New("start_row is 0 or more")
	}
	if in.RowLimit == 0 {
		in.RowLimit = defaultRows
	}
	var filters []map[string]string
	for _, f := range in.Filters {
		if !slices.Contains([]string{"query", "page", "country", "device", "searchAppearance"}, f.Dimension) {
			return "", fmt.Errorf("a filter cannot test %q; it tests query, page, country, device or searchAppearance", f.Dimension)
		}
		op := or(f.Operator, "equals")
		if !slices.Contains(operators, op) {
			return "", fmt.Errorf("there is no operator %q; there is %s", op, strings.Join(operators, ", "))
		}
		filters = append(filters, map[string]string{"dimension": f.Dimension, "operator": op, "expression": f.Expression})
	}
	start, end, err := c.period(in)
	if err != nil {
		return "", err
	}
	siteURL, err := c.resolve(ctx, in.Site)
	if err != nil {
		return "", err
	}
	body := map[string]any{
		"startDate": start, "endDate": end,
		"type":     or(in.Type, "web"),
		"rowLimit": in.RowLimit, "startRow": in.StartRow,
		"dataState": or(in.DataState, "final"),
	}
	if len(in.Dimensions) > 0 {
		body["dimensions"] = in.Dimensions
	}
	if len(filters) > 0 {
		body["dimensionFilterGroups"] = []map[string]any{{"groupType": "and", "filters": filters}}
	}
	if in.Aggregation != "" {
		body["aggregationType"] = in.Aggregation
	}
	var out struct {
		Rows        []row  `json:"rows"`
		Aggregation string `json:"responseAggregationType"`
	}
	addr := c.s.apiBase() + "/sites/" + sitePath(siteURL) + "/searchAnalytics/query"
	if err := c.api.do(ctx, http.MethodPost, addr, body, &out); err != nil {
		return "", err
	}

	var b strings.Builder
	by := "totals"
	if len(in.Dimensions) > 0 {
		by = "by " + strings.Join(in.Dimensions, ", ")
	}
	fmt.Fprintf(&b, "Search performance of %s, %s, %s to %s (Pacific Time, %s data), %s",
		siteURL, typeWords(or(in.Type, "web")), start, end, or(in.DataState, "final"), by)
	for _, f := range filters {
		fmt.Fprintf(&b, "; %s %s %q", f["dimension"], f["operator"], f["expression"])
	}
	if out.Aggregation != "" {
		fmt.Fprintf(&b, "; counted by %s", strings.ToLower(strings.TrimPrefix(out.Aggregation, "by")))
	}
	b.WriteString(".\n")
	if len(out.Rows) == 0 {
		if in.StartRow > 0 {
			b.WriteString("No more rows.\n")
		} else {
			b.WriteString("No data: nothing was shown in Google for this in these days.\n")
		}
		return b.String(), nil
	}
	head := append(append([]string{}, in.Dimensions...), "clicks", "impressions", "ctr", "position")
	saved := ""
	shown := out.Rows
	if in.Save || len(out.Rows) > inlineRows {
		path, err := c.saveCSV(head, out.Rows)
		if err != nil {
			return "", err
		}
		saved = path
		if len(shown) > previewRows {
			shown = shown[:previewRows]
		}
	}
	b.WriteString(plural(len(out.Rows), "row", "rows"))
	if in.StartRow > 0 {
		fmt.Fprintf(&b, " from row %d", in.StartRow+1)
	}
	if len(out.Rows) == in.RowLimit {
		fmt.Fprintf(&b, " (the row_limit; there may be more: start_row %d gives the next)", in.StartRow+len(out.Rows))
	}
	if saved != "" {
		fmt.Fprintf(&b, ", saved as CSV: %s", saved)
		if len(shown) < len(out.Rows) {
			fmt.Fprintf(&b, "; the first %d here", len(shown))
		}
	}
	b.WriteString(". CTR in percent, position is the average (1 is the top).\n\n")
	b.WriteString("| " + strings.Join(head, " | ") + " |\n")
	b.WriteString("|" + strings.Repeat(" --- |", len(head)) + "\n")
	for _, r := range shown {
		cells := cellsOf(r, len(in.Dimensions))
		for i := range cells {
			cells[i] = strings.ReplaceAll(cells[i], "|", `\|`)
		}
		b.WriteString("| " + strings.Join(cells, " | ") + " |\n")
	}
	return b.String(), nil
}

func cellsOf(r row, keys int) []string {
	cells := make([]string, 0, keys+4)
	for i := 0; i < keys; i++ {
		k := ""
		if i < len(r.Keys) {
			k = r.Keys[i]
		}
		cells = append(cells, k)
	}
	return append(cells,
		strconv.FormatFloat(r.Clicks, 'f', 0, 64),
		strconv.FormatFloat(r.Impressions, 'f', 0, 64),
		strconv.FormatFloat(r.CTR*100, 'f', 2, 64),
		strconv.FormatFloat(r.Position, 'f', 1, 64))
}

func typeWords(t string) string {
	switch t {
	case "web":
		return "web search"
	case "image", "video":
		return t + " search"
	case "news":
		return "the News tab"
	case "discover":
		return "Discover"
	case "googleNews":
		return "Google News"
	}
	return t
}

// saveCSV writes rows into the chat's folder of saved results and answers
// the file's path.
func (c *call) saveCSV(head []string, rows []row) (string, error) {
	dir := c.s.chatDir(c.sess.ID)
	if dir == "" {
		return "", errors.New("the rows cannot be saved here; ask for fewer (row_limit 500 or less)")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("the rows could not be saved: %w", err)
	}
	stamp := c.s.now().UTC().Format("20060102-150405")
	var f *os.File
	var err error
	for i := 1; ; i++ {
		name := "query-" + stamp + ".csv"
		if i > 1 {
			name = fmt.Sprintf("query-%s-%d.csv", stamp, i)
		}
		f, err = os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil || !os.IsExist(err) || i > 100 {
			break
		}
	}
	if err != nil {
		return "", fmt.Errorf("the rows could not be saved: %w", err)
	}
	w := csv.NewWriter(f)
	_ = w.Write(head)
	for _, r := range rows {
		cells := cellsOf(r, len(head)-4)
		cells[len(cells)-2] = strconv.FormatFloat(r.CTR, 'f', -1, 64) // a fraction in the file, as the API has it
		cells[len(cells)-1] = strconv.FormatFloat(r.Position, 'f', -1, 64)
		_ = w.Write(cells)
	}
	w.Flush()
	if err := w.Error(); err != nil {
		f.Close()
		return "", fmt.Errorf("the rows could not be saved: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("the rows could not be saved: %w", err)
	}
	return f.Name(), nil
}

// ---------------------------------------------------------------- inspect

type issue struct {
	Type     string `json:"issueType"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	// Rich results name these differently.
	IssueMessage string `json:"issueMessage"`
}

type inspection struct {
	Link  string `json:"inspectionResultLink"`
	Index *struct {
		Verdict         string   `json:"verdict"`
		Coverage        string   `json:"coverageState"`
		Robots          string   `json:"robotsTxtState"`
		Indexing        string   `json:"indexingState"`
		LastCrawl       string   `json:"lastCrawlTime"`
		PageFetch       string   `json:"pageFetchState"`
		GoogleCanonical string   `json:"googleCanonical"`
		UserCanonical   string   `json:"userCanonical"`
		Sitemaps        []string `json:"sitemap"`
		Referring       []string `json:"referringUrls"`
		CrawledAs       string   `json:"crawledAs"`
	} `json:"indexStatusResult"`
	Mobile *struct {
		Verdict string  `json:"verdict"`
		Issues  []issue `json:"issues"`
	} `json:"mobileUsabilityResult"`
	Rich *struct {
		Verdict  string `json:"verdict"`
		Detected []struct {
			Type  string `json:"richResultType"`
			Items []struct {
				Name   string  `json:"name"`
				Issues []issue `json:"issues"`
			} `json:"items"`
		} `json:"detectedItems"`
	} `json:"richResultsResult"`
	AMP *struct {
		Verdict string  `json:"verdict"`
		URL     string  `json:"ampUrl"`
		Issues  []issue `json:"issues"`
	} `json:"ampResult"`
}

var words = map[string]string{
	"PASS": "pass", "PARTIAL": "partial", "FAIL": "fail", "NEUTRAL": "neutral (excluded)",
	"VERDICT_UNSPECIFIED": "unknown",
	"ALLOWED":             "allowed", "DISALLOWED": "blocked by robots.txt",
	"INDEXING_ALLOWED": "allowed", "BLOCKED_BY_META_TAG": "blocked by a noindex meta tag",
	"BLOCKED_BY_HTTP_HEADER": "blocked by an X-Robots-Tag noindex header", "BLOCKED_BY_ROBOTS_TXT": "blocked by robots.txt",
	"SUCCESSFUL": "successful", "SOFT_404": "soft 404", "BLOCKED_ROBOTS_TXT": "blocked by robots.txt",
	"NOT_FOUND": "not found (404)", "ACCESS_DENIED": "access denied (401)", "SERVER_ERROR": "server error (5xx)",
	"REDIRECT_ERROR": "redirect error", "ACCESS_FORBIDDEN": "forbidden (403)", "BLOCKED_4XX": "blocked (4xx)",
	"INTERNAL_CRAWL_ERROR": "internal crawl error", "INVALID_URL": "invalid URL",
	"DESKTOP": "Googlebot desktop", "MOBILE": "Googlebot smartphone",
}

func word(v string) string {
	if w, ok := words[v]; ok {
		return w
	}
	return v
}

func (c *call) inspect(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		URL      string `json:"url"`
		Site     string `json:"site"`
		Language string `json:"language"`
		Account  string `json:"account"`
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
	var out struct {
		Result inspection `json:"inspectionResult"`
	}
	body := map[string]string{"inspectionUrl": in.URL, "siteUrl": siteURL, "languageCode": or(in.Language, "en-US")}
	if err := c.api.do(ctx, http.MethodPost, c.s.inspectBase()+"/urlInspection/index:inspect", body, &out); err != nil {
		return "", err
	}
	r := out.Result
	var b strings.Builder
	fmt.Fprintf(&b, "URL Inspection of %s (property %s), Google's indexed version:\n", in.URL, siteURL)
	line := func(label, v string) {
		if v != "" {
			fmt.Fprintf(&b, "- %s: %s\n", label, v)
		}
	}
	if x := r.Index; x != nil {
		line("Verdict", word(x.Verdict))
		line("Coverage", x.Coverage)
		line("Indexing", word(x.Indexing))
		line("robots.txt", word(x.Robots))
		line("Page fetch", word(x.PageFetch))
		line("Last crawl", x.LastCrawl)
		line("Crawled as", word(x.CrawledAs))
		line("Google's canonical", x.GoogleCanonical)
		line("The page's canonical", x.UserCanonical)
		line("In sitemaps", strings.Join(x.Sitemaps, ", "))
		line("Linked from", strings.Join(x.Referring, ", "))
	} else {
		b.WriteString("- No index status.\n")
	}
	issues := func(list []issue) {
		for _, i := range list {
			msg := or(i.Message, i.IssueMessage)
			fmt.Fprintf(&b, "  - %s%s\n", or(i.Severity, "ISSUE")+": ", or(msg, i.Type))
		}
	}
	if m := r.Mobile; m != nil && m.Verdict != "" && m.Verdict != "VERDICT_UNSPECIFIED" {
		line("Mobile usability", word(m.Verdict))
		issues(m.Issues)
	}
	if x := r.Rich; x != nil {
		line("Rich results", word(x.Verdict))
		for _, d := range x.Detected {
			for _, it := range d.Items {
				fmt.Fprintf(&b, "  - %s: %s\n", d.Type, or(it.Name, "(unnamed)"))
				for _, i := range it.Issues {
					fmt.Fprintf(&b, "    - %s: %s\n", or(i.Severity, "ISSUE"), or(i.IssueMessage, i.Message))
				}
			}
		}
	}
	if a := r.AMP; a != nil && a.Verdict != "" {
		line("AMP", word(a.Verdict)+" "+a.URL)
		issues(a.Issues)
	}
	line("In Search Console", r.Link)
	return b.String(), nil
}

// --------------------------------------------------------------- sitemaps

// count is a number the API writes as a string (int64 in JSON).
type count int64

func (n *count) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return err
	}
	*n = count(v)
	return nil
}

func (c *call) sitemaps(ctx context.Context, args json.RawMessage) (string, error) {
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
	var out struct {
		Sitemap []struct {
			Path           string `json:"path"`
			LastSubmitted  string `json:"lastSubmitted"`
			LastDownloaded string `json:"lastDownloaded"`
			Pending        bool   `json:"isPending"`
			Index          bool   `json:"isSitemapsIndex"`
			Type           string `json:"type"`
			Warnings       count  `json:"warnings"`
			Errors         count  `json:"errors"`
			Contents       []struct {
				Type      string `json:"type"`
				Submitted count  `json:"submitted"`
				Indexed   count  `json:"indexed"`
			} `json:"contents"`
		} `json:"sitemap"`
	}
	if err := c.api.do(ctx, http.MethodGet, c.s.apiBase()+"/sites/"+sitePath(siteURL)+"/sitemaps", nil, &out); err != nil {
		return "", err
	}
	if len(out.Sitemap) == 0 {
		return "No sitemap is submitted for " + siteURL + ".", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s of %s:\n", plural(len(out.Sitemap), "sitemap", "sitemaps"), siteURL)
	for _, m := range out.Sitemap {
		kind := strings.ToLower(or(m.Type, "sitemap"))
		if m.Index {
			kind = "sitemap index"
		}
		fmt.Fprintf(&b, "- %s (%s)", m.Path, kind)
		if m.Pending {
			b.WriteString(", pending")
		}
		fmt.Fprintf(&b, ", %s, %s", plural(int(m.Errors), "error", "errors"), plural(int(m.Warnings), "warning", "warnings"))
		if m.LastSubmitted != "" {
			fmt.Fprintf(&b, ", submitted %s", m.LastSubmitted)
		}
		if m.LastDownloaded != "" {
			fmt.Fprintf(&b, ", read %s", m.LastDownloaded)
		}
		b.WriteString("\n")
		for _, ct := range m.Contents {
			fmt.Fprintf(&b, "  - %s: %d submitted", ct.Type, ct.Submitted)
			if ct.Indexed > 0 {
				fmt.Fprintf(&b, ", %d indexed", ct.Indexed)
			}
			b.WriteString("\n")
		}
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
