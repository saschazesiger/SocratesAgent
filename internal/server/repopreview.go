package server

import (
	"context"
	"log"
	"path/filepath"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/catalog"
	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
	"github.com/saschazesiger/SocratesAgent/internal/repos"
)

// repoPreview is what the new-chat sheet looks at for a chat about to be made
// in a repository (REPOS.md §4.1): a folder with the agent configuration of
// the base branch's newest commit (repos.Manager.Preview) - what the folder
// probes read in a worktree: OpenCode's config and agent files, .mcp.json and
// .socrates/mcp.json - so that the probes answer for it as for any folder.
type repoPreview struct {
	dir  string // the folder, "" for none
	repo string // the repository as GitHub spells it
	base string // the branch
}

// previewRoots and previewFile are the paths a repository's preview copies:
// what the catalogue (harnesses.FolderConfigFile) and the MCP probe
// (agent.ReadFolderMCP) read in a folder.
func previewRoots() []string {
	return append(harnesses.FolderConfigRoots(), agent.FolderMCPFile, agent.SocratesMCPFile)
}

func previewFile(rel string) bool {
	return harnesses.FolderConfigFile(rel) || rel == agent.FolderMCPFile || rel == agent.SocratesMCPFile
}

// previewRepo makes the preview of repo at base (its default branch when
// empty). A repository it cannot look at - GitHub not connected, a repository
// the App does not reach, a branch it does not have, git failing - is a
// preview without a folder: the sheet then shows what it would show without
// one, and the chat itself reads its worktree once it is made.
func (s *Server) previewRepo(ctx context.Context, repo, base string) repoPreview {
	repo, base = strings.TrimSpace(repo), strings.TrimSpace(base)
	gh := s.github()
	if gh == nil || !gh.Connected() || !repos.ValidName(repo) {
		return repoPreview{}
	}
	remote, ok, err := gh.Reaches(ctx, repo)
	if err != nil || !ok {
		return repoPreview{}
	}
	if repos.ValidName(remote.FullName) {
		repo = remote.FullName
	}
	if base == "" {
		base = remote.DefaultBranch
	}
	if base == "" {
		return repoPreview{}
	}
	// A first look clones the repository, which must not stop half way
	// because the browser gave up.
	gitCtx, cancel := detached(ctx)
	defer cancel()
	dir, _, err := s.repos.Preview(gitCtx, repo, base, previewRoots(), previewFile)
	if err != nil {
		log.Printf("repos: the configuration of %s at %s could not be looked at: %v", repo, base, err)
		return repoPreview{}
	}
	return repoPreview{dir: dir, repo: repo, base: base}
}

// name is a path in the preview as the repository has it ("owner/name/
// opencode.json"); a path outside it, as it is.
func (p repoPreview) name(path string) string {
	if p.dir == "" {
		return path
	}
	if rel, err := filepath.Rel(p.dir, path); err == nil && filepath.IsLocal(rel) {
		return p.repo + "/" + filepath.ToSlash(rel)
	}
	return path
}

// names is a text with the preview's paths in it as the repository has them.
func (p repoPreview) names(text string) string {
	if p.dir == "" {
		return text
	}
	return strings.ReplaceAll(text, p.dir+string(filepath.Separator), p.repo+"/")
}

// agents is the catalogue's agents in the preview, with their config files
// named as the repository has them. The catalogue's own entries are not
// changed.
func (p repoPreview) agents(list []catalog.Agent) []catalog.Agent {
	out := make([]catalog.Agent, len(list))
	for i, a := range list {
		if a.Folder != nil {
			folder := *a.Folder
			folder.Files = make([]string, len(a.Folder.Files))
			for j, f := range a.Folder.Files {
				folder.Files[j] = p.name(f)
			}
			a.Folder = &folder
		}
		out[i] = a
	}
	return out
}
