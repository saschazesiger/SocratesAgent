package config

import (
	"slices"
	"strings"
)

// GitHubSettings is the GitHub App this Socrates created for itself
// (docs/design/REPOS.md §3) as far as its facts are public, and the settings
// of each repository it works in. The App's secrets (its private key, client
// secret and webhook secret) are not here: they are sealed into the store's
// key/value table, because the settings document is shown to the browser.
//
// The App's fields are written by the server's GitHub endpoints only; a save
// of the whole document from Admin keeps the live ones (admin.go).
type GitHubSettings struct {
	AppID   int64  `json:"app_id,omitempty"`
	Slug    string `json:"slug,omitempty"`
	Name    string `json:"name,omitempty"`
	Owner   string `json:"owner,omitempty"`
	HTMLURL string `json:"html_url,omitempty"`
	// OwnerType is GitHub's "User" or "Organization" for the App's owner:
	// the App's settings page, where it is deleted, is a different address
	// for each.
	OwnerType string `json:"owner_type,omitempty"`
	// AcceptedInstallations are the ids of the App's installations the
	// person accepted. The App is public, so anyone may install it on their
	// own account; only these installations' repositories are used.
	AcceptedInstallations []int64 `json:"accepted_installations,omitempty"`
	// Repos are the settings of each repository by "owner/name".
	Repos map[string]RepoSettings `json:"repos,omitempty"`
}

// RepoSettings is what Socrates keeps about one repository: only the setup
// script, as everything else comes from the repository itself.
type RepoSettings struct {
	// Setup is a shell script run with `sh -e` in a new chat's worktree
	// before the agent's first turn (§4.3).
	Setup string `json:"setup"`
}

// Accepts is whether installation id is accepted.
func (g GitHubSettings) Accepts(id int64) bool {
	for _, have := range g.AcceptedInstallations {
		if have == id {
			return true
		}
	}
	return false
}

// Connected is whether an App has been created.
func (g GitHubSettings) Connected() bool { return g.AppID != 0 && g.Slug != "" }

// RepoSetup is the setup script of fullName, found whatever its case: GitHub
// takes names in any case, and the key is the case it had when saved.
func (g GitHubSettings) RepoSetup(fullName string) string {
	if r, ok := g.Repos[fullName]; ok {
		return r.Setup
	}
	for name, r := range g.Repos {
		if strings.EqualFold(name, fullName) {
			return r.Setup
		}
	}
	return ""
}

// ValidRepoName is whether name is "owner/name" as GitHub allows them:
// letters, digits, "-", "_" and "." on each side of one slash, and neither
// side "." or "..".
func ValidRepoName(name string) bool {
	owner, repo, ok := strings.Cut(name, "/")
	if !ok {
		return false
	}
	for _, part := range []string{owner, repo} {
		if part == "" || part == "." || part == ".." || len(part) > 100 {
			return false
		}
		for _, r := range part {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
				return false
			}
		}
	}
	return true
}

// normalize trims the App's facts and drops the repository settings that say
// nothing or are not keyed by a repository's name. A script's line ends are
// made Unix ones, as a textarea in a browser on Windows sends CRLF and sh
// reads the CR as part of each command.
func (g *GitHubSettings) normalize() {
	g.Slug = strings.TrimSpace(g.Slug)
	g.Name = strings.TrimSpace(g.Name)
	g.Owner = strings.TrimSpace(g.Owner)
	g.HTMLURL = strings.TrimSpace(g.HTMLURL)
	g.OwnerType = strings.TrimSpace(g.OwnerType)
	if g.AppID < 0 {
		g.AppID = 0
	}
	var accepted []int64
	for _, id := range g.AcceptedInstallations {
		if id > 0 && !slices.Contains(accepted, id) {
			accepted = append(accepted, id)
		}
	}
	g.AcceptedInstallations = accepted
	var repos map[string]RepoSettings
	for name, r := range g.Repos {
		name = strings.TrimSpace(name)
		setup := strings.ReplaceAll(r.Setup, "\r\n", "\n")
		if !ValidRepoName(name) || strings.TrimSpace(setup) == "" {
			continue
		}
		if repos == nil {
			repos = map[string]RepoSettings{}
		}
		repos[name] = RepoSettings{Setup: setup}
	}
	g.Repos = repos
}
