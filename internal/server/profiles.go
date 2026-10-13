package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// Sign-in profiles: a second (third, …) set of accounts for the coding agents,
// chosen per session. The directories and the environment that make one are
// internal/harnesses/profiles.go; this file is the list of them and the routes
// that manage it:
//
//	GET    /api/profiles        {"profiles":[<profile>…],"recommended":{<harness>:<id>}},
//	                            the default one first; see headroom.go
//	POST   /api/profiles        {"name":"…"} → 201 {"profile":<profile>}
//	PATCH  /api/profiles/{id}   {"name":"…"} → {"profile":<profile>}
//	DELETE /api/profiles/{id}   {"ok":true}; 409 while a session still uses it
//	PUT    /api/profiles/order  {"order":["",<id>…]} → {"ok":true}, every id
//	                            once, the default one as ""
//
// The list lives under a key of its own rather than in the settings document:
// the dashboard saves that document whole, and a page opened before a profile
// was added would otherwise take it away again on its next save.

// ProfilesKey is the store key the list is kept under.
const ProfilesKey = "profiles"

// ProfileOrderKey is the order the dashboard put the profiles in, by id with
// the default one as "". It is kept apart from the list because the default
// profile is not in the list but can be moved like any other.
const ProfileOrderKey = "profiles.order"

// defaultProfileName is what the profile with no id is called. It is the
// sign-in every CLI had before profiles existed, and it cannot be deleted.
const defaultProfileName = "Default"

// maxProfileName bounds a name, which is a label on a button.
const maxProfileName = 40

// profile is one stored entry.
type profile struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt int64  `json:"created_at"`
}

// profileView is a profile as the dashboard and the new-session sheet see it:
// the entry, which account each agent is signed in with there, and - in the
// list the sheet asks for - how much of each account is left.
type profileView struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	Default  bool          `json:"default,omitempty"`
	Sessions int           `json:"sessions"`
	Accounts profileLogins `json:"accounts"`
	Limits   profileLimits `json:"limits"`
}

type profileLogins struct {
	Claude   agentLogin `json:"claude"`
	Codex    agentLogin `json:"codex"`
	OpenCode agentLogin `json:"opencode"`
}

// agentLogin is one agent's sign-in in one profile. Account is an e-mail
// address where the CLI stores one, and the provider names for OpenCode.
type agentLogin struct {
	SignedIn bool   `json:"signed_in"`
	Account  string `json:"account,omitempty"`
}

func (s *Server) loadProfiles() ([]profile, error) {
	var list []profile
	if err := s.store.GetJSON(ProfilesKey, &list); err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	return list, nil
}

// profileExists reports whether id names a stored profile; the empty id is
// the default one and always exists.
func (s *Server) profileExists(id string) (bool, error) {
	if id == "" {
		return true, nil
	}
	list, err := s.loadProfiles()
	if err != nil {
		return false, err
	}
	for _, p := range list {
		if p.ID == id {
			return true, nil
		}
	}
	return false, nil
}

// profileViews is the default profile and every stored one, with what each is
// signed in to and how many sessions it holds.
func (s *Server) profileViews() ([]profileView, error) {
	list, err := s.loadProfiles()
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListSessions()
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for i := range rows {
		counts[sessionProfile(&rows[i])]++
	}
	views := []profileView{s.profileView(profile{Name: defaultProfileName}, counts)}
	views[0].Default = true
	for _, p := range list {
		views = append(views, s.profileView(p, counts))
	}
	var order []string
	if err := s.store.GetJSON(ProfileOrderKey, &order); err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	return orderProfiles(views, order), nil
}

// orderProfiles puts views in the order the person chose. A profile the order
// does not name - one added since - keeps its place after the ones it does,
// so a new profile comes last and an installation that never reordered has
// the default one first.
func orderProfiles(views []profileView, order []string) []profileView {
	rank := map[string]int{}
	for i, id := range order {
		if _, seen := rank[id]; !seen {
			rank[id] = i
		}
	}
	position := make(map[string]int, len(views))
	for i, v := range views {
		if r, ok := rank[v.ID]; ok {
			position[v.ID] = r
		} else {
			position[v.ID] = len(order) + i
		}
	}
	sort.SliceStable(views, func(i, j int) bool { return position[views[i].ID] < position[views[j].ID] })
	return views
}

// handleReorderProfiles stores the order the dashboard's list was dragged
// into. It has to name every profile exactly once: an order from a page that
// has not seen a profile added or deleted since is refused, and the page
// loads the list again.
func (s *Server) handleReorderProfiles(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Order []string `json:"order"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	s.profilesMu.Lock()
	defer s.profilesMu.Unlock()
	list, err := s.loadProfiles()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	want := map[string]bool{"": true}
	for _, p := range list {
		want[p.ID] = true
	}
	seen := map[string]bool{}
	for _, id := range body.Order {
		if !want[id] || seen[id] {
			writeError(w, http.StatusConflict, "the profiles have changed; load the list again")
			return
		}
		seen[id] = true
	}
	if len(seen) != len(want) {
		writeError(w, http.StatusConflict, "the profiles have changed; load the list again")
		return
	}
	if err := s.store.SetJSON(ProfileOrderKey, body.Order); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) profileView(p profile, counts map[string]int) profileView {
	v := profileView{ID: p.ID, Name: p.Name, Sessions: counts[p.ID]}
	if onboarded, account := harnesses.ClaudeSignIn(s.dataDir, p.ID); onboarded || account != "" {
		v.Accounts.Claude = agentLogin{SignedIn: account != "", Account: account}
	}
	signedIn, account := harnesses.CodexSignIn(s.dataDir, p.ID)
	v.Accounts.Codex = agentLogin{SignedIn: signedIn, Account: account}
	if providers := harnesses.OpenCodeProviders(s.dataDir, p.ID); len(providers) > 0 {
		v.Accounts.OpenCode = agentLogin{SignedIn: true, Account: strings.Join(providers, ", ")}
	}
	return v
}

// sessionProfile is the profile a session was started in, from the options it
// stores. A row from before profiles, or one whose options are not an object,
// is in the default one.
func sessionProfile(row *store.Session) string {
	if len(row.Options) == 0 {
		return ""
	}
	var options struct {
		Profile string `json:"profile"`
	}
	if json.Unmarshal(row.Options, &options) != nil {
		return ""
	}
	return options.Profile
}

func (s *Server) handleListProfiles(w http.ResponseWriter, r *http.Request) {
	views, err := s.profileViews()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.fillLimits(r.Context(), views)
	writeJSON(w, http.StatusOK, map[string]any{
		"profiles":    views,
		"recommended": recommendProfiles(views, time.Now()),
	})
}

func (s *Server) handleCreateProfile(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	name, err := profileName(body.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.profilesMu.Lock()
	defer s.profilesMu.Unlock()
	list, err := s.loadProfiles()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if taken(list, "", name) {
		writeError(w, http.StatusConflict, fmt.Sprintf("there is already a profile called %q", name))
		return
	}
	id, err := newProfileID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	p := profile{ID: id, Name: name, CreatedAt: time.Now().UnixMilli()}
	if err := os.MkdirAll(harnesses.ProfileDir(s.dataDir, id), 0o700); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.store.SetJSON(ProfilesKey, append(list, p)); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"profile": s.profileView(p, nil)})
}

func (s *Server) handleRenameProfile(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	name, err := profileName(body.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	id := r.PathValue("id")
	s.profilesMu.Lock()
	defer s.profilesMu.Unlock()
	list, err := s.loadProfiles()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	i := indexOfProfile(list, id)
	if i < 0 {
		writeError(w, http.StatusNotFound, "profile not found")
		return
	}
	if taken(list, id, name) {
		writeError(w, http.StatusConflict, fmt.Sprintf("there is already a profile called %q", name))
		return
	}
	list[i].Name = name
	if err := s.store.SetJSON(ProfilesKey, list); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profile": s.profileView(list[i], nil)})
}

// handleDeleteProfile removes a profile and everything in its directory: the
// sign-ins, and the conversations its sessions had. The shared configuration
// in it is links, which are removed rather than followed.
//
// A profile some session still runs in is refused rather than pulled out from
// under it: a resume would come back signed out, in a directory that no
// longer exists.
func (s *Server) handleDeleteProfile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.profilesMu.Lock()
	defer s.profilesMu.Unlock()
	list, err := s.loadProfiles()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	i := indexOfProfile(list, id)
	if i < 0 {
		writeError(w, http.StatusNotFound, "profile not found")
		return
	}
	rows, err := s.store.ListSessions()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	using := 0
	for j := range rows {
		if sessionProfile(&rows[j]) == id {
			using++
		}
	}
	if using > 0 {
		writeError(w, http.StatusConflict, fmt.Sprintf(
			"%d session(s) still run in %q; delete them first", using, list[i].Name))
		return
	}
	if err := s.store.SetJSON(ProfilesKey, append(list[:i:i], list[i+1:]...)); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.RemoveAll(harnesses.ProfileDir(s.dataDir, id)); err != nil {
		writeError(w, http.StatusInternalServerError, "the profile is gone from the list, but its files could not be removed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// profileName tidies a name and refuses one that cannot be a label.
func profileName(raw string) (string, error) {
	name := strings.Join(strings.Fields(raw), " ")
	switch {
	case name == "":
		return "", errors.New("a profile needs a name")
	case utf8.RuneCountInString(name) > maxProfileName:
		return "", fmt.Errorf("a profile name has at most %d characters", maxProfileName)
	case strings.EqualFold(name, defaultProfileName):
		return "", fmt.Errorf("%q is the name of the profile every CLI already has", defaultProfileName)
	}
	return name, nil
}

// taken reports whether another profile than id is already called name.
func taken(list []profile, id, name string) bool {
	for _, p := range list {
		if p.ID != id && strings.EqualFold(p.Name, name) {
			return true
		}
	}
	return false
}

func indexOfProfile(list []profile, id string) int {
	for i, p := range list {
		if p.ID == id {
			return i
		}
	}
	return -1
}

// newProfileID is a directory name nobody chose: a name typed into the
// dashboard is not something that should ever reach a path.
func newProfileID() (string, error) {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "p-" + hex.EncodeToString(buf), nil
}

// checkProfile says whether a session of a harness can run in a profile, as
// an HTTP status and a sentence, and zero when it can. The default profile
// suits every session.
func (s *Server) checkProfile(harnessID, label, shellExecution, id string) (int, string) {
	if id == "" {
		return 0, ""
	}
	switch harnessID {
	case string(harnesses.KindClaude), string(harnesses.KindCodex), string(harnesses.KindOpenCode):
	case string(harnesses.KindShell):
		if shellExecution == harnesses.ShellExecutionHost {
			return http.StatusBadRequest, "a profile is this container's sign-ins, so it needs a container shell"
		}
	default:
		return http.StatusBadRequest, label + " has no sign-in to run in a profile"
	}
	if !harnesses.ValidProfileID(id) {
		return http.StatusBadRequest, fmt.Sprintf("%q is not one of the profiles", id)
	}
	exists, err := s.profileExists(id)
	if err != nil {
		return http.StatusInternalServerError, err.Error()
	}
	if !exists {
		return http.StatusBadRequest, fmt.Sprintf("%q is not one of the profiles", id)
	}
	return 0, ""
}
