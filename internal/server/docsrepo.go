package server

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/docs"
	"github.com/saschazesiger/SocratesAgent/internal/repos"
)

// The docs of a repository's group (DOCS.md §7): the sidebar's group of the
// chats in a GitHub repository is keyed github:<owner>/<name> (REPOS.md
// §5.3), and its Docs are the Markdown files and pictures of the
// repository's default branch as GitHub has them. The repository's cache is
// fetched when the listing is asked for, at most once a minute
// (docsRepoAge), and the files of the newest commit are written into a
// folder of their own (repos.Manager.Docs), which every route of the Docs
// view then reads as it reads a folder. The token signs the key, not that
// folder, so that a page's address outlives the next commit.

// docsRepoAge is how long a repository's docs are shown as fetched before
// the listing asks GitHub again.
const docsRepoAge = time.Minute

// docsShownOnly is docsRoot's age for a page or a stat: the docs as last
// fetched, GitHub asked only when there are none yet.
const docsShownOnly = -1

// errNoDocs is a folder or a repository whose docs may not be shown.
var errNoDocs = errors.New("there are no docs for that folder")

// docsRepoInfo is where a repository's docs come from, for the page's head.
type docsRepoInfo struct {
	// Name is the repository as GitHub spells it, Branch the branch shown.
	Name   string `json:"name"`
	Branch string `json:"branch"`
	// Web is the branch's files on GitHub ("https://github.com/<owner>/
	// <name>/blob/<branch>/"), "" for a remote that is no web address.
	Web string `json:"web,omitempty"`
}

// docsRoot is the folder a docs key shows the files of: a project folder
// itself (docsAllowed), or the folder of a repository's docs. age is how
// old a repository's fetch may be before GitHub is asked again: the
// listing's docsRepoAge, 0 for the page's *Read the files again*; a page or
// a stat passes docsShownOnly and reads what was fetched last. info is the
// repository's, nil for a folder or when it is not known.
func (s *Server) docsRoot(ctx context.Context, key string, workdirs map[string]bool, age time.Duration) (string, *docsRepoInfo, error) {
	repo, ok := strings.CutPrefix(key, "github:")
	if !ok {
		if !s.docsAllowed(key, workdirs) {
			return "", nil, errNoDocs
		}
		return key, nil, nil
	}
	if !s.repoDocsAllowed(repo) {
		return "", nil, errNoDocs
	}
	if age == docsShownOnly {
		if dir := s.repos.DocsShown(repo); dir != "" {
			return dir, nil, nil
		}
	}
	gh := s.github()
	remote, ok, err := gh.Reaches(ctx, repo)
	if err != nil || !ok || remote.DefaultBranch == "" {
		// GitHub out of reach: what was fetched last, if anything.
		if dir := s.repos.DocsShown(repo); dir != "" {
			return dir, nil, nil
		}
		if err == nil {
			err = errNoDocs
		}
		return "", nil, err
	}
	name := repo
	if repos.ValidName(remote.FullName) {
		name = remote.FullName
	}
	// A first look clones the repository, which must not stop half way
	// because the browser gave up.
	gitCtx, cancel := detached(ctx)
	defer cancel()
	dir, _, err := s.repos.Docs(gitCtx, name, remote.DefaultBranch, age, docs.Listed)
	if err != nil {
		return "", nil, err
	}
	info := &docsRepoInfo{Name: name, Branch: remote.DefaultBranch}
	if web, ok := strings.CutSuffix(s.repoURL(name), ".git"); ok && strings.HasPrefix(web, "https://") {
		info.Web = web + "/blob/" + remote.DefaultBranch + "/"
	}
	return dir, info, nil
}

// repoDocsAllowed is whether a repository's docs may be shown: GitHub is
// connected and the repository is one some chat works in, as only those
// have a group in the sidebar.
func (s *Server) repoDocsAllowed(repo string) bool {
	if !repos.ValidName(repo) {
		return false
	}
	if gh := s.github(); gh == nil || !gh.Connected() {
		return false
	}
	list, err := s.store.ListSessions()
	if err != nil {
		return false
	}
	for _, sess := range list {
		if sess.Repo != "" && strings.EqualFold(sess.Repo, repo) {
			return true
		}
	}
	return false
}

// repoDocsProbe is the probe's answer for a repository's group: whether its
// docs may be shown and, where they were fetched already, whether they have
// Markdown. A repository not fetched yet is said to have docs: the probe
// does not ask GitHub.
func (s *Server) repoDocsProbe(repo string) bool {
	if !s.repoDocsAllowed(repo) {
		return false
	}
	if dir := s.repos.DocsShown(repo); dir != "" {
		return docs.Has(dir)
	}
	return true
}

// docsRootMissing is what a 404 says about a docs key docsRoot refused.
func docsRootMissing(err error) string {
	switch {
	case errors.Is(err, errNoDocs):
		return errNoDocs.Error()
	case errors.Is(err, repos.ErrNoBranch):
		return "The repository has no files on GitHub yet."
	case errors.Is(err, fs.ErrNotExist):
		return errNoDocs.Error()
	}
	return "The repository's docs could not be read from GitHub: " + err.Error()
}
