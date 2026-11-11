package docs

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Repo is where a folder's files are on a forge: `host/owner/name` of the
// repository's origin (lower case, without `.git`), and Prefix the folder's
// path inside the repository ("" at its top). A chat's agent that links a
// file of its folder by its forge address (github.com/.../blob/main/x.md)
// has the file opened beside the chat as a relative link would.
type Repo struct {
	Remote string `json:"remote"`
	Prefix string `json:"prefix"`
}

// maxRepoUp is how many folders above the folder a .git is looked for.
const maxRepoUp = 32

// RepoOf is the repository a folder is in, read from its git config without
// running git: the origin remote, else the first remote. Zero when the
// folder is in no repository or the repository has no remote it can read.
func RepoOf(folder string) Repo {
	dir := filepath.Clean(folder)
	for range maxRepoUp {
		if config := gitConfig(filepath.Join(dir, ".git")); config != "" {
			remote := normalizeRemote(remoteURL(config))
			if remote == "" {
				return Repo{}
			}
			rel, err := filepath.Rel(dir, filepath.Clean(folder))
			if err != nil {
				return Repo{}
			}
			if rel == "." {
				rel = ""
			}
			return Repo{Remote: remote, Prefix: filepath.ToSlash(rel)}
		}
		up := filepath.Dir(dir)
		if up == dir {
			break
		}
		dir = up
	}
	return Repo{}
}

// gitConfig is the config file of the repository whose `.git` is at dotGit:
// a directory, or a file `gitdir: <dir>` (a worktree or submodule), whose
// directory may name the shared one in `commondir`. "" when there is none.
func gitConfig(dotGit string) string {
	common := gitCommonDir(dotGit)
	if common == "" {
		return ""
	}
	config := filepath.Join(common, "config")
	if fi, err := os.Stat(config); err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	return config
}

// gitCommonDir is the repository's own directory for the `.git` at dotGit
// (gitConfig): the one every working tree of the repository shares,
// cleaned. "" when there is none.
func gitCommonDir(dotGit string) string {
	fi, err := os.Stat(dotGit)
	if err != nil {
		return ""
	}
	gitDir := dotGit
	if !fi.IsDir() {
		b, err := os.ReadFile(dotGit)
		if err != nil || len(b) > 4096 {
			return ""
		}
		line, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
		if !ok {
			return ""
		}
		gitDir = strings.TrimSpace(line)
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(filepath.Dir(dotGit), gitDir)
		}
	}
	if b, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil && len(b) < 4096 {
		common := strings.TrimSpace(string(b))
		if !filepath.IsAbs(common) {
			common = filepath.Join(gitDir, common)
		}
		gitDir = common
	}
	if fi, err := os.Stat(gitDir); err != nil || !fi.IsDir() {
		return ""
	}
	return filepath.Clean(gitDir)
}

// maxWorktrees is the most working trees Worktrees reads.
const maxWorktrees = 500

// Worktrees is the folder as it stands in every working tree of its git
// repository: the main one and each `git worktree add` made, read from the
// repository's directory without running git, the folder itself first.
// An agent that works in another working tree than its chat's folder links
// the files it writes there by paths that mean that tree.
//
// Only what the repository itself names counts: the tree the folder is in
// must be one of its trees (a `.git` that merely points at a repository
// makes no tree of it), each tree's own `.git` must lead back to the same
// repository, and the folder must be a directory inside each tree, its
// symbolic links followed (a link in one branch's tree leads nowhere else).
// nil when the folder is in no such tree.
func Worktrees(folder string) []string {
	folder = filepath.Clean(folder)
	dir := folder
	for range maxRepoUp {
		if common := gitCommonDir(filepath.Join(dir, ".git")); common != "" {
			rel, err := filepath.Rel(dir, folder)
			if err != nil || !filepath.IsLocal(rel) && rel != "." {
				return nil
			}
			return worktreesOf(common, dir, rel)
		}
		up := filepath.Dir(dir)
		if up == dir {
			break
		}
		dir = up
	}
	return nil
}

// worktreesOf is Worktrees for the repository whose directory is common,
// the folder being rel inside the tree at top.
func worktreesOf(common, top, rel string) []string {
	tops := worktreeTops(common)
	own := -1
	realTop, _ := filepath.EvalSymlinks(top)
	for i, t := range tops {
		if t == top {
			own = i
			break
		}
		if real, err := filepath.EvalSymlinks(t); err == nil && real == realTop {
			own = i
		}
	}
	if own < 0 {
		return nil
	}
	tops = append([]string{top}, append(tops[:own:own], tops[own+1:]...)...)
	var out []string
	seen := map[string]bool{}
	for i, t := range tops {
		at := filepath.Join(t, rel)
		if !inTree(t, at) {
			if i == 0 {
				return nil
			}
			continue
		}
		real, _ := filepath.EvalSymlinks(at)
		if seen[real] {
			continue
		}
		seen[real] = true
		out = append(out, at)
	}
	return out
}

// worktreeTops is the top of every working tree of the repository whose
// directory is common: the main tree, whose .git it is, and each one
// .git/worktrees names, as long as the tree's own .git leads back here.
func worktreeTops(common string) []string {
	var tops []string
	seen := map[string]bool{}
	add := func(top string) {
		top = filepath.Clean(top)
		if !filepath.IsAbs(top) || seen[top] || gitCommonDir(filepath.Join(top, ".git")) != common {
			return
		}
		seen[top] = true
		tops = append(tops, top)
	}
	if filepath.Base(common) == ".git" {
		add(filepath.Dir(common))
	}
	entries, err := os.ReadDir(filepath.Join(common, "worktrees"))
	if err != nil {
		return tops
	}
	for i, e := range entries {
		if i >= maxWorktrees {
			break
		}
		own := filepath.Join(common, "worktrees", e.Name())
		b, err := os.ReadFile(filepath.Join(own, "gitdir"))
		if err != nil || len(b) > 4096 {
			continue
		}
		// gitdir names the tree's .git file, relative to this entry when
		// the repository writes relative paths.
		dotGit := strings.TrimSpace(string(b))
		if !filepath.IsAbs(dotGit) {
			dotGit = filepath.Join(own, dotGit)
		}
		add(filepath.Dir(dotGit))
	}
	return tops
}

// inTree is whether at is a directory inside the tree at top once every
// symbolic link of both is followed.
func inTree(top, at string) bool {
	realTop, err := filepath.EvalSymlinks(top)
	if err != nil {
		return false
	}
	real, err := filepath.EvalSymlinks(at)
	if err != nil {
		return false
	}
	if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
		return false
	}
	rel, err := filepath.Rel(realTop, real)
	return err == nil && (rel == "." || filepath.IsLocal(rel))
}

// remoteURL is the url of the origin remote in a git config file, else of
// the first remote it names.
func remoteURL(config string) string {
	f, err := os.Open(config)
	if err != nil {
		return ""
	}
	defer f.Close()
	var section, first, origin string
	sc := bufio.NewScanner(f)
	for lines := 0; sc.Scan() && lines < 5000; lines++ {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			section = strings.ToLower(strings.Join(strings.Fields(strings.Trim(line, "[]")), " "))
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "url") || !strings.HasPrefix(section, "remote ") {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"`)
		if section == `remote "origin"` && origin == "" {
			origin = value
		}
		if first == "" {
			first = value
		}
	}
	if origin != "" {
		return origin
	}
	return first
}

// normalizeRemote turns a remote's url into `host/owner/name`:
// https://github.com/o/r.git, git@github.com:o/r.git and
// ssh://git@github.com:22/o/r all become github.com/o/r. A local path or
// anything it cannot read is "".
func normalizeRemote(url string) string {
	url = strings.TrimSpace(url)
	var host, path string
	if scheme, rest, ok := strings.Cut(url, "://"); ok {
		switch strings.ToLower(scheme) {
		case "https", "http", "ssh", "git", "git+ssh", "ssh+git":
		default:
			return ""
		}
		host, path, _ = strings.Cut(rest, "/")
	} else if h, p, ok := strings.Cut(url, ":"); ok && !strings.Contains(h, "/") {
		host, path = h, p
	} else {
		return ""
	}
	if at := strings.LastIndex(host, "@"); at >= 0 {
		host = host[at+1:]
	}
	if colon := strings.Index(host, ":"); colon >= 0 {
		host = host[:colon]
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	path = strings.Trim(path, "/")
	if host == "" || !strings.Contains(path, "/") || strings.Contains(path, "..") {
		return ""
	}
	return strings.ToLower(host + "/" + path)
}
