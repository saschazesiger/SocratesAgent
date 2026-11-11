package server

import (
	"context"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/github"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// Admin → GitHub (docs/design/REPOS.md §3): the GitHub App this Socrates
// creates for itself through GitHub's manifest flow, its installations and
// the repositories they grant, and the setup script of each repository.
//
// The App's public facts live in the settings document (settings.github);
// its secrets are sealed with the data directory's key into the kv table,
// one object each, so that the settings the browser reads never carry them.

// The kv keys of the App's secrets; each is also the object it is sealed
// for, so a sealed value moved onto another key does not open.
const (
	kvGitHubPEM           = "github:pem"
	kvGitHubClientSecret  = "github:client_secret"
	kvGitHubWebhookSecret = "github:webhook_secret"
)

// githubStateFor is how long a manifest flow may take from Connect to
// GitHub's redirect back.
const githubStateFor = 10 * time.Minute

// maxSetupScript is the longest setup script a repository may have.
const maxSetupScript = 64 << 10

// githubFlow is the manifest flows under way: their state values and when
// each was handed out.
type githubFlow struct {
	mu     sync.Mutex
	states map[string]time.Time
	// edit makes each change of settings.github one read and one write, so
	// two of them at once do not lose one.
	edit sync.Mutex
}

// add keeps a new state and drops the expired ones.
func (f *githubFlow) add(state string, now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.states == nil {
		f.states = map[string]time.Time{}
	}
	for k, at := range f.states {
		if now.Sub(at) > githubStateFor {
			delete(f.states, k)
		}
	}
	f.states[state] = now
}

// take is whether state was handed out in the last ten minutes; it is good
// for one redirect only.
func (f *githubFlow) take(state string, now time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	at, ok := f.states[state]
	delete(f.states, state)
	return ok && state != "" && now.Sub(at) <= githubStateFor
}

// newGitHub is the GitHub connection with the App the data directory keeps,
// if it has one. A key that does not open leaves Socrates without the App
// and GET /api/github says so; it is not a reason not to start.
func (s *Server) newGitHub() *github.Service {
	svc := github.New(github.ConfigFromEnv())
	g := s.Settings().GitHub
	if !g.Connected() {
		return svc
	}
	pem, err := s.openGitHubSecret(kvGitHubPEM)
	if err == nil && len(pem) == 0 {
		err = errors.New("its private key is not kept here")
	}
	if err == nil {
		err = svc.SetApp(appOf(g, pem))
	}
	if err != nil {
		log.Printf("github: the App %s cannot be used: %v", g.Slug, err)
	}
	return svc
}

func appOf(g config.GitHubSettings, pem []byte) *github.App {
	return &github.App{ID: g.AppID, Slug: g.Slug, Name: g.Name, Owner: g.Owner, HTMLURL: g.HTMLURL, PEM: pem}
}

func (s *Server) sealGitHubSecret(key string, plain []byte) error {
	sealed, err := keySealer{s}.Seal(key, plain)
	if err != nil {
		return err
	}
	return s.store.SetKV(key, hex.EncodeToString(sealed))
}

// openGitHubSecret is a sealed secret of the App, nil when there is none.
func (s *Server) openGitHubSecret(key string) ([]byte, error) {
	raw, err := s.store.GetKV(key)
	if errors.Is(err, store.ErrNotFound) || (err == nil && raw == "") {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sealed, err := hex.DecodeString(raw)
	if err != nil {
		return nil, err
	}
	return keySealer{s}.Open(key, sealed)
}

// editGitHub changes settings.github and saves the document.
func (s *Server) editGitHub(change func(g *config.GitHubSettings)) error {
	s.githubFlow.edit.Lock()
	defer s.githubFlow.edit.Unlock()
	next := s.Settings()
	if next.GitHub.Repos != nil {
		repos := make(map[string]config.RepoSettings, len(next.GitHub.Repos))
		for k, v := range next.GitHub.Repos {
			repos[k] = v
		}
		next.GitHub.Repos = repos
	}
	change(&next.GitHub)
	return s.saveSettings(next)
}

// pageOrigin is the origin the browser has this page at: what it sent as
// Origin, or the scheme and host it reached the server with.
func pageOrigin(r *http.Request) string {
	origin := strings.TrimRight(r.Header.Get("Origin"), "/")
	if origin == "" || origin == "null" {
		scheme := "http"
		if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
			scheme = "https"
		}
		origin = scheme + "://" + r.Host
	}
	return origin
}

// ------------------------------------------------------------------- views

type githubAppView struct {
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Owner       string `json:"owner"`
	HTMLURL     string `json:"html_url"`
	SettingsURL string `json:"settings_url"`
	InstallURL  string `json:"install_url"`
}

type githubInstallView struct {
	ID          int64  `json:"id"`
	Account     string `json:"account"`
	Type        string `json:"type"`
	SettingsURL string `json:"settings_url"`
}

type githubRepoView struct {
	github.Repo
	Setup string `json:"setup"`
}

type githubView struct {
	Connected     bool                `json:"connected"`
	App           *githubAppView      `json:"app"`
	Installations []githubInstallView `json:"installations"`
	Repos         []githubRepoView    `json:"repos"`
	Problem       string              `json:"problem"`
}

// githubState is what GET /api/github answers; refresh asks GitHub now
// rather than taking a listing up to a minute old.
func (s *Server) githubState(ctx context.Context, refresh bool) githubView {
	g := s.Settings().GitHub
	out := githubView{Installations: []githubInstallView{}, Repos: []githubRepoView{}}
	if !s.github.Connected() {
		if g.Connected() {
			out.Problem = "The App " + g.Name + " is set up, but its private key cannot be opened on this machine. Disconnect and connect GitHub again."
		}
		return out
	}
	out.Connected = true
	out.App = &githubAppView{
		Name: g.Name, Slug: g.Slug, Owner: g.Owner, HTMLURL: g.HTMLURL,
		SettingsURL: s.github.AppSettingsURL(g.Slug, g.Owner, g.OwnerType),
		InstallURL:  s.github.InstallURL(g.Slug),
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var (
		installs []github.Installation
		repos    []github.Repo
		err      error
	)
	if refresh {
		installs, repos, err = s.github.Refresh(ctx)
	} else if installs, err = s.github.Installations(ctx); err == nil {
		repos, err = s.github.Repos(ctx)
	}
	if err != nil {
		out.Problem = err.Error()
	}
	for _, in := range installs {
		out.Installations = append(out.Installations, githubInstallView{
			ID: in.ID, Account: in.Account, Type: in.AccountType, SettingsURL: s.github.InstallationSettingsURL(in),
		})
	}
	for _, r := range repos {
		out.Repos = append(out.Repos, githubRepoView{Repo: r, Setup: g.RepoSetup(r.FullName)})
	}
	return out
}

// ---------------------------------------------------------------- handlers

// handleGitHub is the connection as Admin and the new-chat sheet show it.
//
//	GET /api/github[?refresh=1]
func (s *Server) handleGitHub(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.githubState(r.Context(), r.URL.Query().Get("refresh") != ""))
}

// handleGitHubAppStart begins the manifest flow: the page posts the manifest
// as a form to the action, which is GitHub's, so the person's own GitHub
// login creates the App.
//
//	POST /api/github/app/start   {"org"}   {"action", "manifest", "state"}
func (s *Server) handleGitHubAppStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Org string `json:"org"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if s.github.Connected() {
		writeError(w, http.StatusConflict, "GitHub is already connected; disconnect it first to create another App")
		return
	}
	org := strings.TrimSpace(body.Org)
	if org != "" && !validGitHubLogin(org) {
		writeError(w, http.StatusBadRequest, "the organisation is not a GitHub account name")
		return
	}
	state := randomState()
	s.githubFlow.add(state, time.Now())
	writeJSON(w, http.StatusOK, map[string]any{
		"action":   s.github.ManifestAction(org, state),
		"manifest": github.NewManifest(pageOrigin(r)),
		"state":    state,
	})
}

// validGitHubLogin is whether name can be an account on GitHub: letters,
// digits and single hyphens, at most 39 characters.
func validGitHubLogin(name string) bool {
	if name == "" || len(name) > 39 || strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// handleGitHubAppCreated is where GitHub sends the browser once the App is
// created: the code is traded for the App and its keys, which are kept, and
// the browser goes on to install the App.
//
//	GET /api/github/app/created?code=…&state=…   303 to the installation page
func (s *Server) handleGitHubAppCreated(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if !s.githubFlow.take(q.Get("state"), time.Now()) {
		githubBack(w, r, "The App was not created from this page, or it took longer than ten minutes. Connect GitHub again; an App GitHub created anyway can be deleted in your GitHub settings.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	conv, err := s.github.ConvertManifest(ctx, q.Get("code"))
	if err != nil {
		githubBack(w, r, "GitHub's App could not be fetched: "+err.Error())
		return
	}
	for key, value := range map[string]string{
		kvGitHubPEM: conv.PEM, kvGitHubClientSecret: conv.ClientSecret, kvGitHubWebhookSecret: conv.WebhookSecret,
	} {
		if err := s.sealGitHubSecret(key, []byte(value)); err != nil {
			githubBack(w, r, "The App's keys could not be kept: "+err.Error())
			return
		}
	}
	if err := s.editGitHub(func(g *config.GitHubSettings) {
		g.AppID, g.Slug, g.Name, g.HTMLURL = conv.ID, conv.Slug, conv.Name, conv.HTMLURL
		g.Owner, g.OwnerType = conv.Owner.Login, conv.Owner.Type
	}); err != nil {
		githubBack(w, r, "The App could not be saved: "+err.Error())
		return
	}
	if err := s.github.SetApp(conv.App()); err != nil {
		githubBack(w, r, err.Error())
		return
	}
	http.Redirect(w, r, s.github.InstallURL(conv.Slug), http.StatusSeeOther)
}

// githubBack takes the browser back to Admin's GitHub card with a problem
// to show.
func githubBack(w http.ResponseWriter, r *http.Request, problem string) {
	http.Redirect(w, r, "/admin?"+url.Values{"github_error": {problem}}.Encode()+"#github", http.StatusSeeOther)
}

// handleGitHubAppInstalled is where GitHub sends the browser after an
// installation or a change of its repositories. The installation id it
// carries is not taken from the address: what the App reaches is asked of
// GitHub with the App's own JWT, so the listing is only dropped here.
//
//	GET /api/github/app/installed   303 to /admin#github
func (s *Server) handleGitHubAppInstalled(w http.ResponseWriter, r *http.Request) {
	// It changes nothing but how fresh a listing is, so a browser that is
	// not signed in is sent on too: Admin asks it to sign in.
	if s.authenticated(r) {
		s.github.Forget()
	}
	http.Redirect(w, r, "/admin#github", http.StatusSeeOther)
}

// handleGitHubAppDelete forgets the App here: its keys and its facts. The
// App on GitHub is the person's to delete; the repositories' setup scripts
// stay for an App connected again.
//
//	DELETE /api/github/app
func (s *Server) handleGitHubAppDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.github.SetApp(nil); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, key := range []string{kvGitHubPEM, kvGitHubClientSecret, kvGitHubWebhookSecret} {
		if err := s.store.DeleteKV(key); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if err := s.editGitHub(func(g *config.GitHubSettings) {
		*g = config.GitHubSettings{Repos: g.Repos}
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.githubState(r.Context(), false))
}

// handleGitHubRepoSettings keeps a repository's setup script; an empty one
// removes it.
//
//	PUT /api/github/repos/{owner}/{name}/settings   {"setup"}
func (s *Server) handleGitHubRepoSettings(w http.ResponseWriter, r *http.Request) {
	fullName := r.PathValue("owner") + "/" + r.PathValue("name")
	if !config.ValidRepoName(fullName) {
		writeError(w, http.StatusBadRequest, "that is not a repository's name")
		return
	}
	var body struct {
		Setup string `json:"setup"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if len(body.Setup) > maxSetupScript {
		writeError(w, http.StatusRequestEntityTooLarge, "the setup script is longer than 64 KB")
		return
	}
	if !s.github.Connected() {
		writeError(w, http.StatusConflict, github.ErrNotConnected.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	repo, ok, err := s.github.Reaches(ctx, fullName)
	switch {
	case err == nil && !ok:
		writeError(w, http.StatusNotFound, "the GitHub App does not reach "+fullName+"; choose it under the installation's repositories on GitHub")
		return
	case err == nil:
		fullName = repo.FullName
	default:
		// GitHub out of reach is no reason to lose a script being saved: it
		// is kept under the name as given, which RepoSetup finds whatever
		// its case.
		log.Printf("github: saving the setup of %s without asking GitHub: %v", fullName, err)
	}
	setup := strings.ReplaceAll(body.Setup, "\r\n", "\n")
	if err := s.editGitHub(func(g *config.GitHubSettings) {
		for name := range g.Repos {
			if strings.EqualFold(name, fullName) {
				delete(g.Repos, name)
			}
		}
		if strings.TrimSpace(setup) == "" {
			return
		}
		if g.Repos == nil {
			g.Repos = map[string]config.RepoSettings{}
		}
		g.Repos[fullName] = config.RepoSettings{Setup: setup}
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"full_name": fullName, "setup": s.Settings().GitHub.RepoSetup(fullName)})
}
