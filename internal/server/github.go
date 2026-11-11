package server

import (
	"context"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strconv"
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
// The App is public, so that it can be installed on the person's
// organisations too, and so anyone may install it. An installation counts
// once the person accepted it (settings.github.accepted_installations): by
// coming back signed in from installing it, or with Accept on Admin's card.
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

// githubInstallFor is how long an installation may take from the install
// page to GitHub's redirect back to the setup URL.
const githubInstallFor = 15 * time.Minute

// maxSetupScript is the longest setup script a repository may have.
const maxSetupScript = 64 << 10

// githubFlow is the manifest flows and the installations under way: their
// state values and when each was handed out.
type githubFlow struct {
	mu     sync.Mutex
	states map[string]time.Time
	// installs are the states of the install pages Socrates sent a browser
	// to (/api/github/app/install, and after the App was created). GitHub
	// hands an install page's state back to the setup URL, and only an
	// installation that comes back with one is accepted on its own.
	installs map[string]time.Time
	// edit makes each change of settings.github one read and one write, so
	// two of them at once do not lose one.
	edit sync.Mutex
}

// add keeps a new manifest state and drops the expired ones.
func (f *githubFlow) add(state string, now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	keepState(&f.states, state, now, githubStateFor)
}

// take is whether state was handed out in the last ten minutes; it is good
// for one redirect only.
func (f *githubFlow) take(state string, now time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return takeState(f.states, state, now, githubStateFor)
}

// addInstall keeps a new install page's state and drops the expired ones.
func (f *githubFlow) addInstall(state string, now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	keepState(&f.installs, state, now, githubInstallFor)
}

// takeInstall is whether state was handed out for an install page in the
// last fifteen minutes; it is good for one redirect only.
func (f *githubFlow) takeInstall(state string, now time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return takeState(f.installs, state, now, githubInstallFor)
}

func keepState(states *map[string]time.Time, state string, now time.Time, life time.Duration) {
	if *states == nil {
		*states = map[string]time.Time{}
	}
	for k, at := range *states {
		if now.Sub(at) > life {
			delete(*states, k)
		}
	}
	(*states)[state] = now
}

func takeState(states map[string]time.Time, state string, now time.Time, life time.Duration) bool {
	at, ok := states[state]
	delete(states, state)
	return ok && state != "" && now.Sub(at) <= life
}

// installPage is GitHub's page that installs the App, with a new state that
// brings the installation back accepted (handleGitHubAppInstalled).
func (s *Server) installPage(slug string) string {
	state := randomState()
	s.githubFlow.addInstall(state, time.Now())
	return s.ghApp.InstallURL(slug) + "?state=" + url.QueryEscape(state)
}

// newGitHub is the GitHub connection with the App the data directory keeps,
// if it has one. A key that does not open leaves Socrates without the App
// and GET /api/github says so; it is not a reason not to start.
func (s *Server) newGitHub() *github.Service {
	svc := github.New(github.ConfigFromEnv())
	g := s.Settings().GitHub
	svc.SetAccepted(g.AcceptedInstallations)
	svc.OnAdopt(s.adoptGitHubInstallations)
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
	next.GitHub.AcceptedInstallations = slices.Clone(next.GitHub.AcceptedInstallations)
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

// adoptGitHubInstallations keeps the installations the service accepted on
// its own: those on the App's owner's account, while none was accepted (a
// connection from before installations had to be accepted).
func (s *Server) adoptGitHubInstallations(ids []int64) {
	err := s.editGitHub(func(g *config.GitHubSettings) {
		for _, id := range ids {
			if !g.Accepts(id) {
				g.AcceptedInstallations = append(g.AcceptedInstallations, id)
			}
		}
	})
	if err != nil {
		log.Printf("github: the owner's installations %v could not be kept as accepted: %v", ids, err)
	}
}

// acceptGitHubInstallation makes in one of the installations Socrates uses.
func (s *Server) acceptGitHubInstallation(in github.Installation) error {
	if err := s.editGitHub(func(g *config.GitHubSettings) {
		if !g.Accepts(in.ID) {
			g.AcceptedInstallations = append(g.AcceptedInstallations, in.ID)
		}
	}); err != nil {
		return err
	}
	s.ghApp.SetAccepted(s.Settings().GitHub.AcceptedInstallations)
	log.Printf("github: installation %d on %s accepted", in.ID, in.Account)
	return nil
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
	// InstallURL is Socrates' own address that sends the browser on to
	// GitHub's install page with a state of its own.
	InstallURL string `json:"install_url"`
	// AdvancedURL is the App's "Advanced" settings on GitHub, where an App
	// created private before Socrates made them public is made public.
	AdvancedURL string `json:"advanced_url"`
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
	// Pending are installations of the App nobody accepted here: their
	// repositories are not used until they are.
	Pending []githubInstallView `json:"pending"`
	Repos   []githubRepoView    `json:"repos"`
	Problem string              `json:"problem"`
}

// githubState is what GET /api/github answers; refresh asks GitHub now
// rather than taking a listing up to a minute old.
func (s *Server) githubState(ctx context.Context, refresh bool) githubView {
	g := s.Settings().GitHub
	out := githubView{Installations: []githubInstallView{}, Pending: []githubInstallView{}, Repos: []githubRepoView{}}
	if !s.ghApp.Connected() {
		if g.Connected() {
			out.Problem = "The App " + g.Name + " is set up, but its private key cannot be opened on this machine. Disconnect and connect GitHub again."
		}
		return out
	}
	out.Connected = true
	out.App = &githubAppView{
		Name: g.Name, Slug: g.Slug, Owner: g.Owner, HTMLURL: g.HTMLURL,
		SettingsURL: s.ghApp.AppSettingsURL(g.Slug, g.Owner, g.OwnerType),
		InstallURL:  "/api/github/app/install",
		AdvancedURL: s.ghApp.AppAdvancedURL(g.Slug, g.Owner, g.OwnerType),
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var (
		installs, pending []github.Installation
		repos             []github.Repo
		err               error
	)
	if refresh {
		installs, repos, err = s.ghApp.Refresh(ctx)
	} else if installs, err = s.ghApp.Installations(ctx); err == nil {
		repos, err = s.ghApp.Repos(ctx)
	}
	if err == nil {
		pending, err = s.ghApp.Pending(ctx)
	}
	if err != nil {
		out.Problem = err.Error()
	}
	view := func(in github.Installation) githubInstallView {
		return githubInstallView{ID: in.ID, Account: in.Account, Type: in.AccountType, SettingsURL: s.ghApp.InstallationSettingsURL(in)}
	}
	for _, in := range installs {
		out.Installations = append(out.Installations, view(in))
	}
	for _, in := range pending {
		out.Pending = append(out.Pending, view(in))
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
	if s.ghApp.Connected() {
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
		"action":   s.ghApp.ManifestAction(org, state),
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
	conv, err := s.ghApp.ConvertManifest(ctx, q.Get("code"))
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
		g.AcceptedInstallations = nil
	}); err != nil {
		githubBack(w, r, "The App could not be saved: "+err.Error())
		return
	}
	s.ghApp.SetAccepted(nil)
	if err := s.ghApp.SetApp(conv.App()); err != nil {
		githubBack(w, r, err.Error())
		return
	}
	http.Redirect(w, r, s.installPage(conv.Slug), http.StatusSeeOther)
}

// handleGitHubAppInstall sends the browser to GitHub's page that installs
// the App, where GitHub asks for the account (an organisation, say), with a
// state that GitHub hands back to the setup URL.
//
//	GET /api/github/app/install   303 to GitHub's install page
func (s *Server) handleGitHubAppInstall(w http.ResponseWriter, r *http.Request) {
	app, ok := s.ghApp.App()
	if !ok {
		githubBack(w, r, github.ErrNotConnected.Error())
		return
	}
	http.Redirect(w, r, s.installPage(app.Slug), http.StatusSeeOther)
}

// githubBack takes the browser back to Admin's GitHub card with a problem
// to show.
func githubBack(w http.ResponseWriter, r *http.Request, problem string) {
	http.Redirect(w, r, "/admin?"+url.Values{"github_error": {problem}}.Encode()+"#github", http.StatusSeeOther)
}

// handleGitHubAppInstalled is where GitHub sends the browser after an
// installation or a change of its repositories. It accepts the installation
// only when the browser is signed in and brings back the state of an install
// page this server sent it to (installPage), once: so it is the person who
// installed it, and not a link someone else made up with their own
// installation's id. The id is not believed from the address either: it
// must be an installation of the App as GitHub lists them with the App's own
// JWT. Anything else is only sent on; the installation then waits on
// Admin's card to be accepted.
//
//	GET /api/github/app/installed?installation_id=…&state=…   303 to /admin#github
func (s *Server) handleGitHubAppInstalled(w http.ResponseWriter, r *http.Request) {
	if !s.authenticated(r) {
		http.Redirect(w, r, "/admin#github", http.StatusSeeOther)
		return
	}
	s.ghApp.Forget()
	q := r.URL.Query()
	id, err := strconv.ParseInt(q.Get("installation_id"), 10, 64)
	if err != nil || id <= 0 || !s.ghApp.Connected() || s.ghApp.Accepted(id) || !s.githubFlow.takeInstall(q.Get("state"), time.Now()) {
		http.Redirect(w, r, "/admin#github", http.StatusSeeOther)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	in, ok, err := s.ghApp.Installation(ctx, id)
	switch {
	case err != nil:
		githubBack(w, r, "GitHub could not be asked about the new installation: "+err.Error()+". Accept it on this card once GitHub answers.")
	case !ok:
		githubBack(w, r, "GitHub does not know the installation this page was sent back with.")
	default:
		if err := s.acceptGitHubInstallation(in); err != nil {
			githubBack(w, r, "The installation could not be accepted: "+err.Error())
			return
		}
		http.Redirect(w, r, "/admin#github", http.StatusSeeOther)
	}
}

// installationID is the {id} of the path, false (with the answer written)
// when it is not one or there is no App.
func (s *Server) installationID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "that is not an installation's id")
		return 0, false
	}
	if !s.ghApp.Connected() {
		writeError(w, http.StatusConflict, github.ErrNotConnected.Error())
		return 0, false
	}
	return id, true
}

// handleGitHubInstallationAccept accepts an installation of the App, after
// GitHub confirmed it is one: its repositories are used from now on.
//
//	POST /api/github/installations/{id}/accept   the card's view
func (s *Server) handleGitHubInstallationAccept(w http.ResponseWriter, r *http.Request) {
	id, ok := s.installationID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	in, found, err := s.ghApp.Installation(ctx, id)
	switch {
	case err != nil:
		writeError(w, http.StatusBadGateway, err.Error())
		return
	case !found:
		writeError(w, http.StatusNotFound, "the App has no such installation")
		return
	}
	if err := s.acceptGitHubInstallation(in); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.githubState(r.Context(), false))
}

// handleGitHubInstallationDelete uninstalls the App from an account, an
// accepted installation or a pending one, and forgets that it was accepted.
//
//	DELETE /api/github/installations/{id}   the card's view
func (s *Server) handleGitHubInstallationDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := s.installationID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.ghApp.Uninstall(ctx, id); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if err := s.editGitHub(func(g *config.GitHubSettings) {
		g.AcceptedInstallations = slices.DeleteFunc(g.AcceptedInstallations, func(have int64) bool { return have == id })
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.ghApp.SetAccepted(s.Settings().GitHub.AcceptedInstallations)
	log.Printf("github: installation %d removed", id)
	writeJSON(w, http.StatusOK, s.githubState(r.Context(), false))
}

// handleGitHubAppDelete forgets the App here: its keys and its facts. The
// App on GitHub is the person's to delete; the repositories' setup scripts
// stay for an App connected again.
//
//	DELETE /api/github/app
func (s *Server) handleGitHubAppDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.ghApp.SetApp(nil); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.ghApp.SetAccepted(nil)
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
	if !s.ghApp.Connected() {
		writeError(w, http.StatusConflict, github.ErrNotConnected.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	repo, ok, err := s.ghApp.Reaches(ctx, fullName)
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
