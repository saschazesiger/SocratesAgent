package server

import (
	"context"

	"github.com/saschazesiger/SocratesAgent/internal/github"
	"github.com/saschazesiger/SocratesAgent/internal/repos"
)

// githubRepos lets the chats in repositories (repos.go) use the GitHub App's
// connection (github.go): the two packages keep their own types, so neither
// depends on the other, and this is the one place that translates.
type githubRepos struct{ svc *github.Service }

func (a githubRepos) Connected() bool { return a.svc.Connected() }

func (a githubRepos) Reaches(ctx context.Context, fullName string) (repos.Remote, bool, error) {
	r, ok, err := a.svc.Reaches(ctx, fullName)
	return repos.Remote{FullName: r.FullName, DefaultBranch: r.DefaultBranch, Private: r.Private}, ok, err
}

func (a githubRepos) RepoToken(ctx context.Context, fullName string) (string, error) {
	return a.svc.RepoToken(ctx, fullName)
}

func (a githubRepos) OpenPullRequest(ctx context.Context, fullName, head, base, title, body string, draft bool) (repos.PullRequest, error) {
	p, err := a.svc.OpenPullRequest(ctx, fullName, head, base, title, body, draft)
	return repos.PullRequest{Number: p.Number, URL: p.URL, Existing: p.Existing}, err
}

func (a githubRepos) CloneURL(fullName string) string { return a.svc.CloneURL(fullName) }

// useGitHubApp connects the chats in repositories to the App, with each
// repository's setup script read from the settings at the moment it runs.
func (s *Server) useGitHubApp() {
	s.UseGitHub(githubRepos{s.ghApp}, func(fullName string) string {
		return s.Settings().GitHub.RepoSetup(fullName)
	})
}
