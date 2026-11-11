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
	config := filepath.Join(gitDir, "config")
	if fi, err := os.Stat(config); err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	return config
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
