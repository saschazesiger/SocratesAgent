// Package github is Socrates' connection to GitHub through a GitHub App of
// its own (docs/design/REPOS.md §3): the App's JWT, installation tokens that
// reach one repository each, the repositories the installations grant, the
// manifest flow that creates the App, and pull requests.
//
// The App is public, so anyone may install it on their own account. Only the
// installations the person accepted count (SetAccepted): the repositories,
// tokens and pull requests come from those alone, and the others are pending.
//
// It uses the standard library only. The API, web and git bases come from the
// environment (ConfigFromEnv), so the tests run against a fake.
package github

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Defaults of the three bases.
const (
	DefaultAPI = "https://api.github.com"
	DefaultWeb = "https://github.com"
	DefaultGit = "https://github.com"
)

// listFor is how long the installations and their repositories are believed
// before GitHub is asked again (§3.2).
const listFor = time.Minute

// tokenMargin is how long before its expiry an installation token is
// replaced: a token handed to git a second before it expires fails a push
// halfway.
const tokenMargin = 5 * time.Minute

// pageSize is how many entries a listing asks for per page; the tests make
// it small to see the paging.
var pageSize = 100

// Config is where GitHub is.
type Config struct {
	// API is the REST API's base, Web the site's (where the App is created
	// and installed), Git the base of the clone URLs.
	API, Web, Git string
	// HTTP is the client every request goes through; nil is a client with a
	// timeout of thirty seconds.
	HTTP *http.Client
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// ConfigFromEnv is the configuration SOCRATES_GITHUB_API,
// SOCRATES_GITHUB_WEB and SOCRATES_GITHUB_GIT give, GitHub itself for those
// not set.
func ConfigFromEnv() Config {
	get := func(name, fallback string) string {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
		return fallback
	}
	return Config{
		API: get("SOCRATES_GITHUB_API", DefaultAPI),
		Web: get("SOCRATES_GITHUB_WEB", DefaultWeb),
		Git: get("SOCRATES_GITHUB_GIT", DefaultGit),
	}
}

// App is the GitHub App this Socrates created for itself: its public facts
// and the private key its JWT is signed with.
type App struct {
	ID      int64
	Slug    string
	Name    string
	Owner   string
	HTMLURL string
	// PEM is the private key as GitHub handed it out.
	PEM []byte
}

// Repo is a repository an installation of the App grants.
type Repo struct {
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	Installation  int64  `json:"installation"`
}

// Installation is the App installed on an account.
type Installation struct {
	ID int64 `json:"id"`
	// Account is the login of the user or organisation it is installed on,
	// AccountType GitHub's "User" or "Organization".
	Account     string `json:"account"`
	AccountType string `json:"account_type"`
}

// PullRequest is a pull request opened, or the one that was already open for
// the branch (Existing).
type PullRequest struct {
	Number   int    `json:"number"`
	URL      string `json:"url"`
	Existing bool   `json:"existing"`
}

// APIError is an answer of GitHub's API that was not a success.
type APIError struct {
	Status  int
	Message string
	// Details are the messages of the "errors" list a 422 carries.
	Details []string
}

func (e *APIError) Error() string {
	text := e.Message
	if text == "" {
		text = http.StatusText(e.Status)
	}
	if len(e.Details) > 0 {
		text += " (" + strings.Join(e.Details, "; ") + ")"
	}
	return fmt.Sprintf("GitHub answered %d: %s", e.Status, text)
}

// ErrNotConnected is the answer of everything that needs the App while there
// is none.
var ErrNotConnected = errors.New("GitHub is not connected: create the App under Admin → GitHub")

// ErrNotReached is the answer for a repository no installation grants.
var ErrNotReached = errors.New("the GitHub App does not reach this repository")

type token struct {
	value string
	until time.Time
}

// listing is what the installations granted the last time GitHub was asked.
type listing struct {
	installs []Installation
	repos    []Repo
	at       time.Time
	err      error
}

// Service is the connection to GitHub. Its zero value is not usable; New
// makes one, without an App until SetApp.
type Service struct {
	cfg Config

	mu  sync.Mutex
	app *App
	key *rsa.PrivateKey
	// tokens are the installation tokens by lower-case repository name, and
	// by "#<installation>" for the unscoped ones the listing uses.
	tokens map[string]token
	list   listing
	// gen counts the App's changes, so that a listing that was under way
	// while the App changed is not kept.
	gen int

	// accepted are the installations the person accepted; only their
	// repositories are listed and reached.
	accepted map[int64]bool
	// adopt is told the installations accepted on their own (adoptOwner).
	adopt func(ids []int64)

	// listMu makes a refresh of the listing one request at a time: the
	// callers that arrive meanwhile wait for it and take its result.
	listMu sync.Mutex

	// pub is what GitHub last said about whether the App is public (Public),
	// pubMu makes asking it one request at a time.
	pub   publicity
	pubMu sync.Mutex
}

// publicity is whether the App was public when GitHub was last asked.
type publicity struct {
	public bool
	at     time.Time
	err    error
}

// New is a service for cfg, without an App.
func New(cfg Config) *Service {
	cfg.API = strings.TrimRight(orDefault(cfg.API, DefaultAPI), "/")
	cfg.Web = strings.TrimRight(orDefault(cfg.Web, DefaultWeb), "/")
	cfg.Git = strings.TrimRight(orDefault(cfg.Git, DefaultGit), "/")
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg, tokens: map[string]token{}}
}

func orDefault(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return strings.TrimSpace(v)
}

// Config is the service's configuration, with the defaults filled in.
func (s *Service) Config() Config { return s.cfg }

// SetApp makes app the App the service acts as; nil forgets it. Every token
// and listing of the App before goes with the change.
func (s *Service) SetApp(app *App) error {
	var key *rsa.PrivateKey
	if app != nil {
		k, err := ParsePrivateKey(app.PEM)
		if err != nil {
			return err
		}
		key = k
		copied := *app
		app = &copied
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.app, s.key = app, key
	s.tokens = map[string]token{}
	s.list = listing{}
	s.pub = publicity{}
	s.gen++
	return nil
}

// App is the App the service acts as, without its key; false without one.
func (s *Service) App() (App, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.app == nil {
		return App{}, false
	}
	a := *s.app
	a.PEM = nil
	return a, true
}

// Connected is whether there is an App to act as.
func (s *Service) Connected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.app != nil && s.key != nil
}

// Forget drops what GitHub said about the installations, so the next
// question asks again: after the person changed an installation on GitHub.
func (s *Service) Forget() {
	s.mu.Lock()
	s.list = listing{}
	s.mu.Unlock()
}

// SetAccepted makes ids the installations the person accepted, and drops the
// listing, which only has the repositories of the ones accepted before.
//
// While none is accepted, the next listing accepts the installations on the
// App's owner's account on its own and tells OnAdopt: only that account's
// owners can install the App there, and a connection made before
// installations had to be accepted keeps working.
func (s *Service) SetAccepted(ids []int64) {
	set := map[int64]bool{}
	for _, id := range ids {
		if id > 0 {
			set[id] = true
		}
	}
	s.mu.Lock()
	s.accepted = set
	s.list = listing{}
	s.gen++
	s.mu.Unlock()
}

// Accepted is whether installation id is one the person accepted.
func (s *Service) Accepted(id int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accepted[id]
}

// OnAdopt sets what is told the installations a listing accepted on its own
// (SetAccepted), to keep them.
func (s *Service) OnAdopt(fn func(ids []int64)) {
	s.mu.Lock()
	s.adopt = fn
	s.mu.Unlock()
}

// Repos is every repository the accepted installations grant, sorted by
// name, from a listing at most a minute old.
func (s *Service) Repos(ctx context.Context) ([]Repo, error) {
	l, err := s.listing(ctx, false)
	return l.repos, err
}

// Installations is the App's accepted installations, from a listing at most
// a minute old.
func (s *Service) Installations(ctx context.Context) ([]Installation, error) {
	l, err := s.listing(ctx, false)
	return s.split(l.installs, true), err
}

// Pending is the App's installations the person has not accepted, from a
// listing at most a minute old: anyone may install a public App on their
// own account, so they count for nothing until accepted.
func (s *Service) Pending(ctx context.Context) ([]Installation, error) {
	l, err := s.listing(ctx, false)
	return s.split(l.installs, false), err
}

// split is the installations of all that are accepted, or those that are
// not.
func (s *Service) split(all []Installation, accepted bool) []Installation {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Installation{}
	for _, in := range all {
		if s.accepted[in.ID] == accepted {
			out = append(out, in)
		}
	}
	return out
}

// Refresh asks GitHub again now and answers the accepted installations and
// the repositories. Whether the App is public (Public) is asked again too,
// the next time it is wanted.
func (s *Service) Refresh(ctx context.Context) ([]Installation, []Repo, error) {
	s.mu.Lock()
	s.pub = publicity{}
	s.mu.Unlock()
	l, err := s.listing(ctx, true)
	return s.split(l.installs, true), l.repos, err
}

// Public is whether the App is public, so that GitHub installs it on other
// accounts than its owner's (an organisation). GitHub's API answers
// GET /apps/{slug} without authentication for a public App only, and 404 for
// a private one (with the App's JWT, GET /app answers either way). The answer
// is believed for a minute, a failure for a few seconds; Refresh asks again.
// An error (GitHub not reached, a rate limit) means it is not known.
func (s *Service) Public(ctx context.Context) (bool, error) {
	fresh := func() (publicity, string, int, bool) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.app == nil {
			return publicity{}, "", s.gen, false
		}
		p := s.pub
		return p, s.app.Slug, s.gen, !p.at.IsZero() && s.cfg.Now().Sub(p.at) < listFor
	}
	p, slug, _, ok := fresh()
	if slug == "" {
		return false, ErrNotConnected
	}
	if ok {
		return p.public, p.err
	}
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	// An answer that came while this one waited is as good.
	p, slug, gen, ok := fresh()
	if slug == "" {
		return false, ErrNotConnected
	}
	if ok {
		return p.public, p.err
	}
	err := s.call(ctx, http.MethodGet, "/apps/"+url.PathEscape(slug), "", nil, nil)
	p = publicity{public: err == nil, at: s.cfg.Now()}
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
		err = nil
	}
	if err != nil {
		p.err = err
		p.at = p.at.Add(-listFor + 5*time.Second)
	}
	s.mu.Lock()
	if gen == s.gen {
		s.pub = p
	}
	s.mu.Unlock()
	return p.public, err
}

// Installation asks GitHub now whether id is an installation of the App,
// accepted or not, and answers it.
func (s *Service) Installation(ctx context.Context, id int64) (Installation, bool, error) {
	l, err := s.listing(ctx, true)
	if err != nil {
		return Installation{}, false, err
	}
	for _, in := range l.installs {
		if in.ID == id {
			return in, true, nil
		}
	}
	return Installation{}, false, nil
}

// Uninstall removes installation id from its account on GitHub
// (DELETE /app/installations/{id} with the App's JWT). An installation
// GitHub no longer has is gone already, which is not an error.
func (s *Service) Uninstall(ctx context.Context, id int64) error {
	jwt, err := s.appJWT()
	if err != nil {
		return err
	}
	err = s.call(ctx, http.MethodDelete, "/app/installations/"+strconv.FormatInt(id, 10), "Bearer "+jwt, nil, nil)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
		err = nil
	}
	s.Forget()
	return err
}

// Reaches is whether an installation grants fullName ("owner/name", in any
// case), and the repository as GitHub names it. A repository the listing
// does not have makes it ask GitHub again once, as one granted a moment ago
// would not be in it yet.
func (s *Service) Reaches(ctx context.Context, fullName string) (Repo, bool, error) {
	if !s.Connected() {
		return Repo{}, false, ErrNotConnected
	}
	l, err := s.listing(ctx, false)
	if err != nil {
		return Repo{}, false, err
	}
	if r, ok := findRepo(l.repos, fullName); ok {
		return r, true, nil
	}
	if s.cfg.Now().Sub(l.at) < time.Second {
		return Repo{}, false, nil
	}
	l, err = s.listing(ctx, true)
	if err != nil {
		return Repo{}, false, err
	}
	r, ok := findRepo(l.repos, fullName)
	return r, ok, nil
}

func findRepo(repos []Repo, fullName string) (Repo, bool) {
	for _, r := range repos {
		if strings.EqualFold(r.FullName, strings.TrimSpace(fullName)) {
			return r, true
		}
	}
	return Repo{}, false
}

// RepoToken is an installation token that reaches fullName and no other
// repository, valid for at least five more minutes.
func (s *Service) RepoToken(ctx context.Context, fullName string) (string, error) {
	repo, ok, err := s.Reaches(ctx, fullName)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrNotReached, fullName)
	}
	_, name, _ := strings.Cut(repo.FullName, "/")
	tok, err := s.installationToken(ctx, repo.Installation, strings.ToLower(repo.FullName), []string{name})
	var apiErr *APIError
	if errors.As(err, &apiErr) && (apiErr.Status == http.StatusNotFound || apiErr.Status == http.StatusUnprocessableEntity) {
		// The repository left the installation since the listing.
		s.Forget()
	}
	return tok, err
}

// CloneURL is the address git clones fullName from: <git>/<owner>/<name>.git.
func (s *Service) CloneURL(fullName string) string {
	return s.cfg.Git + "/" + strings.Trim(strings.TrimSpace(fullName), "/") + ".git"
}

// InstallationSettingsURL is the page on GitHub where an installation's
// repositories are chosen.
func (s *Service) InstallationSettingsURL(in Installation) string {
	if strings.EqualFold(in.AccountType, "Organization") {
		return s.cfg.Web + "/organizations/" + url.PathEscape(in.Account) + "/settings/installations/" + strconv.FormatInt(in.ID, 10)
	}
	return s.cfg.Web + "/settings/installations/" + strconv.FormatInt(in.ID, 10)
}

// InstallURL is the page that installs the App on an account.
func (s *Service) InstallURL(slug string) string {
	return s.cfg.Web + "/apps/" + url.PathEscape(slug) + "/installations/new"
}

// AppSettingsURL is the App's own settings page on GitHub, where it is
// deleted; ownerType is the owner's "User" or "Organization".
func (s *Service) AppSettingsURL(slug, owner, ownerType string) string {
	if strings.EqualFold(ownerType, "Organization") {
		return s.cfg.Web + "/organizations/" + url.PathEscape(owner) + "/settings/apps/" + url.PathEscape(slug)
	}
	return s.cfg.Web + "/settings/apps/" + url.PathEscape(slug)
}

// AppAdvancedURL is the App's "Advanced" settings page, where an App created
// private is made public so that it can be installed on other accounts.
func (s *Service) AppAdvancedURL(slug, owner, ownerType string) string {
	return s.AppSettingsURL(slug, owner, ownerType) + "/advanced"
}

// OpenPullRequest opens a pull request of head into base in fullName. When
// one is already open for head, that one is the answer, with Existing set.
func (s *Service) OpenPullRequest(ctx context.Context, fullName, head, base, title, body string, draft bool) (PullRequest, error) {
	tok, err := s.RepoToken(ctx, fullName)
	if err != nil {
		return PullRequest{}, err
	}
	repo, _, _ := s.Reaches(ctx, fullName)
	if repo.FullName == "" {
		repo.FullName = fullName
	}
	path := "/repos/" + repoPath(repo.FullName) + "/pulls"
	in := map[string]any{"title": title, "body": body, "head": head, "base": base, "draft": draft}
	var out struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	err = s.call(ctx, http.MethodPost, path, "token "+tok, in, &out)
	if err == nil {
		return PullRequest{Number: out.Number, URL: out.HTMLURL}, nil
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnprocessableEntity || !alreadyExists(apiErr) {
		return PullRequest{}, err
	}
	owner, _, _ := strings.Cut(repo.FullName, "/")
	q := url.Values{"head": {owner + ":" + head}, "state": {"open"}}
	var open []struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	if err := s.call(ctx, http.MethodGet, path+"?"+q.Encode(), "token "+tok, nil, &open); err != nil {
		return PullRequest{}, err
	}
	if len(open) == 0 {
		return PullRequest{}, apiErr
	}
	return PullRequest{Number: open[0].Number, URL: open[0].HTMLURL, Existing: true}, nil
}

func alreadyExists(e *APIError) bool {
	for _, text := range append([]string{e.Message}, e.Details...) {
		if strings.Contains(strings.ToLower(text), "already exists") {
			return true
		}
	}
	return false
}

// repoPath is "owner/name" with each part escaped for a URL path.
func repoPath(fullName string) string {
	owner, name, _ := strings.Cut(fullName, "/")
	return url.PathEscape(owner) + "/" + url.PathEscape(name)
}

// ------------------------------------------------------------------ listing

func (s *Service) listing(ctx context.Context, fresh bool) (listing, error) {
	s.mu.Lock()
	if s.app == nil {
		s.mu.Unlock()
		return listing{}, ErrNotConnected
	}
	l := s.list
	s.mu.Unlock()
	if !fresh && !l.at.IsZero() && s.cfg.Now().Sub(l.at) < listFor {
		return l, l.err
	}
	asked := s.cfg.Now()
	s.listMu.Lock()
	defer s.listMu.Unlock()
	// A refresh that finished while this one waited is as fresh as asked.
	s.mu.Lock()
	l, gen := s.list, s.gen
	s.mu.Unlock()
	if l.at.After(asked) {
		return l, l.err
	}
	installs, repos, err := s.fetchListing(ctx)
	l = listing{installs: installs, repos: repos, at: s.cfg.Now(), err: err}
	s.mu.Lock()
	if gen == s.gen {
		if err != nil {
			// A failure is believed for a few seconds only, and keeps what
			// was known before so that a hiccup does not empty the lists.
			l.installs, l.repos = s.list.installs, s.list.repos
			l.at = l.at.Add(-listFor + 5*time.Second)
		}
		s.list = l
	}
	s.mu.Unlock()
	return l, err
}

func (s *Service) fetchListing(ctx context.Context) ([]Installation, []Repo, error) {
	jwt, err := s.appJWT()
	if err != nil {
		return nil, nil, err
	}
	var installs []Installation
	for page := 1; ; page++ {
		var batch []struct {
			ID      int64 `json:"id"`
			Account struct {
				Login string `json:"login"`
				Type  string `json:"type"`
			} `json:"account"`
		}
		path := fmt.Sprintf("/app/installations?per_page=%d&page=%d", pageSize, page)
		if err := s.call(ctx, http.MethodGet, path, "Bearer "+jwt, nil, &batch); err != nil {
			return nil, nil, err
		}
		for _, b := range batch {
			installs = append(installs, Installation{ID: b.ID, Account: b.Account.Login, AccountType: b.Account.Type})
		}
		if len(batch) < pageSize {
			break
		}
	}
	sort.Slice(installs, func(i, j int) bool {
		return strings.ToLower(installs[i].Account) < strings.ToLower(installs[j].Account)
	})
	accepted := s.adoptOwner(installs)
	var repos []Repo
	for _, in := range installs {
		if !accepted[in.ID] {
			continue
		}
		tok, err := s.installationToken(ctx, in.ID, "#"+strconv.FormatInt(in.ID, 10), nil)
		if err != nil {
			return nil, nil, err
		}
		for page := 1; ; page++ {
			var batch struct {
				Repositories []struct {
					FullName      string `json:"full_name"`
					DefaultBranch string `json:"default_branch"`
					Private       bool   `json:"private"`
				} `json:"repositories"`
			}
			path := fmt.Sprintf("/installation/repositories?per_page=%d&page=%d", pageSize, page)
			if err := s.call(ctx, http.MethodGet, path, "token "+tok, nil, &batch); err != nil {
				return nil, nil, err
			}
			for _, r := range batch.Repositories {
				repos = append(repos, Repo{FullName: r.FullName, DefaultBranch: r.DefaultBranch, Private: r.Private, Installation: in.ID})
			}
			if len(batch.Repositories) < pageSize {
				break
			}
		}
	}
	sort.Slice(repos, func(i, j int) bool { return strings.ToLower(repos[i].FullName) < strings.ToLower(repos[j].FullName) })
	return installs, repos, nil
}

// adoptOwner is the accepted installations as a listing of installs finds
// them: while none is accepted, those on the App's owner's account are
// accepted on their own (SetAccepted) and OnAdopt is told.
func (s *Service) adoptOwner(installs []Installation) map[int64]bool {
	s.mu.Lock()
	accepted := s.accepted
	var owner string
	if s.app != nil {
		owner = s.app.Owner
	}
	adopt := s.adopt
	var adopted []int64
	if len(accepted) == 0 && owner != "" {
		for _, in := range installs {
			if strings.EqualFold(in.Account, owner) {
				adopted = append(adopted, in.ID)
			}
		}
		if len(adopted) > 0 {
			accepted = map[int64]bool{}
			for _, id := range adopted {
				accepted[id] = true
			}
			s.accepted = accepted
		}
	}
	s.mu.Unlock()
	if len(adopted) > 0 && adopt != nil {
		adopt(adopted)
	}
	return accepted
}

// installationToken is a token of installation id, scoped to repos (nil:
// every repository of the installation), cached under key.
func (s *Service) installationToken(ctx context.Context, id int64, key string, repos []string) (string, error) {
	s.mu.Lock()
	t, ok := s.tokens[key]
	gen := s.gen
	s.mu.Unlock()
	if ok && s.cfg.Now().Before(t.until) {
		return t.value, nil
	}
	jwt, err := s.appJWT()
	if err != nil {
		return "", err
	}
	var in any
	if len(repos) > 0 {
		in = map[string]any{"repositories": repos}
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	path := "/app/installations/" + strconv.FormatInt(id, 10) + "/access_tokens"
	if err := s.call(ctx, http.MethodPost, path, "Bearer "+jwt, in, &out); err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", errors.New("GitHub gave no installation token")
	}
	s.mu.Lock()
	if gen == s.gen {
		s.tokens[key] = token{value: out.Token, until: out.ExpiresAt.Add(-tokenMargin)}
	}
	s.mu.Unlock()
	return out.Token, nil
}

func (s *Service) appJWT() (string, error) {
	s.mu.Lock()
	app, key := s.app, s.key
	s.mu.Unlock()
	if app == nil || key == nil {
		return "", ErrNotConnected
	}
	return AppJWT(key, app.ID, s.cfg.Now())
}

// ------------------------------------------------------------------- HTTP

// call sends one request to the API and decodes its answer into out (nil:
// the answer is dropped). auth is the Authorization header, "" for none.
func (s *Service) call(ctx context.Context, method, path, auth string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.cfg.API+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "Socrates")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	res, err := s.cfg.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub could not be reached: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return fmt.Errorf("GitHub's answer could not be read: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		e := &APIError{Status: res.StatusCode}
		var msg struct {
			Message string `json:"message"`
			Errors  []struct {
				Message string `json:"message"`
				Code    string `json:"code"`
			} `json:"errors"`
		}
		if json.Unmarshal(raw, &msg) == nil {
			e.Message = msg.Message
			for _, d := range msg.Errors {
				if d.Message != "" {
					e.Details = append(e.Details, d.Message)
				} else if d.Code != "" {
					e.Details = append(e.Details, d.Code)
				}
			}
		}
		return e
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("GitHub's answer could not be decoded: %w", err)
	}
	return nil
}
