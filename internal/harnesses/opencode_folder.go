package harnesses

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// openCodeConfigFiles are the files OpenCode reads its project config from,
// in the folder it runs in and in every folder above it.
var openCodeConfigFiles = []string{"opencode.jsonc", "opencode.json",
	filepath.Join(".opencode", "opencode.jsonc"), filepath.Join(".opencode", "opencode.json")}

// openCodeAgentDirs are the folders of a .opencode/ whose markdown files are
// agents, each of which can name its own model.
var openCodeAgentDirs = []string{filepath.Join(".opencode", "agent"), filepath.Join(".opencode", "agents")}

// openCodeFolderFiles is the OpenCode configuration that applies in dir and
// not everywhere: the config files and agent files of dir and every folder
// above it, nearest last. It runs nothing, so the catalogue can ask it on
// every look at a folder; only a folder with any is worth asking OpenCode
// about.
//
// OpenCode stops at the repository's root. Going on to the file system's
// root costs one listing too many for a repository inside a folder with a
// config of its own, never a model too few. The home folder's .opencode is
// left out: OpenCode reads it in every folder, so it is not this one's.
func openCodeFolderFiles(dir string) []string {
	if dir == "" || !filepath.IsAbs(dir) {
		return nil
	}
	home, _ := os.UserHomeDir()
	var out []string
	for at := filepath.Clean(dir); ; {
		var here []string
		for _, name := range openCodeConfigFiles {
			if at == home && strings.HasPrefix(name, ".opencode") {
				continue
			}
			if fi, err := os.Stat(filepath.Join(at, name)); err == nil && fi.Mode().IsRegular() {
				here = append(here, filepath.Join(at, name))
			}
		}
		if at != home {
			for _, sub := range openCodeAgentDirs {
				here = append(here, markdownUnder(filepath.Join(at, sub))...)
			}
		}
		out = append(here, out...)
		parent := filepath.Dir(at)
		if parent == at {
			break
		}
		at = parent
	}
	return out
}

// markdownUnder is every .md file in dir and its subfolders, as OpenCode
// loads agents from them.
func markdownUnder(dir string) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(d.Name(), ".md") {
			out = append(out, path)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// existingDir is dir, or the nearest folder above it that exists: a folder
// not made yet - a dynamic chat's, a path still being typed - has the
// configuration of the folders above it, and OpenCode cannot be run in it.
func existingDir(dir string) string {
	for at := filepath.Clean(dir); ; at = filepath.Dir(at) {
		if fi, err := os.Stat(at); err == nil && fi.IsDir() {
			return at
		}
		if filepath.Dir(at) == at {
			return at
		}
	}
}

// openCodeFolderModels is what OpenCode can run in dir, asked there as each
// of the accounts in envs, and what the folder's configuration says: the
// model it starts on, the providers' own names, and the agents it puts on a
// model of their own. The folder's model is the catalogue's default, as it
// is the CLI's there.
//
// `opencode debug config` merges every source, the global configuration
// too, so it is also asked at the file system's root: only what the folder
// changes is the folder's.
func openCodeFolderModels(ctx context.Context, bin, dir string, envs [][]string) (Catalog, error) {
	files := openCodeFolderFiles(dir)
	dir = existingDir(dir)
	cat, err := openCodeModelsIn(ctx, bin, dir, envs)
	if err != nil {
		return Catalog{}, err
	}
	var env []string
	if len(envs) > 0 {
		env = envs[0]
	}
	resolved := resolveOpenCodeConfig(ctx, bin, dir, env)
	general := resolveOpenCodeConfig(ctx, bin, string(filepath.Separator), env)
	folder := &FolderConfig{Files: files}
	if folder.Files == nil {
		folder.Files = []string{}
	}
	if model := strings.TrimSpace(resolved.Model); model != strings.TrimSpace(general.Model) {
		folder.Model = model
	}
	for name, a := range resolved.Agent {
		model := strings.TrimSpace(a.Model)
		if model != "" && !a.Disable && model != strings.TrimSpace(general.Agent[name].Model) {
			folder.Agents = append(folder.Agents, AgentModel{Name: name, Model: model})
		}
	}
	sort.Slice(folder.Agents, func(i, j int) bool { return folder.Agents[i].Name < folder.Agents[j].Name })
	cat.Folder = folder

	// A provider the configuration names is called what it says ("Ollama
	// (HAUSMATT)"), not by its id or the models.dev catalogue's name.
	for i := range cat.Models {
		provider, _, _ := strings.Cut(cat.Models[i].ID, "/")
		if p, ok := resolved.Provider[provider]; ok && strings.TrimSpace(p.Name) != "" {
			cat.Models[i].Group = strings.TrimSpace(p.Name)
		}
	}
	if folder.Model != "" {
		listed := false
		for i := range cat.Models {
			if cat.Models[i].ID == folder.Model {
				listed = true
			}
		}
		// The folder's model is where OpenCode starts there. One it does not
		// list - a provider no account signed in to - is one it cannot start
		// on, and the folder starts where any other does.
		if listed {
			for i := range cat.Models {
				cat.Models[i].Default = cat.Models[i].ID == folder.Model
			}
		} else {
			folder.Model = ""
		}
	}
	return cat, nil
}

// openCodeResolved is the little of `opencode debug config` the catalogue
// reads. The rest - keys, MCP servers' environments - is never kept.
type openCodeResolved struct {
	Model    string `json:"model"`
	Provider map[string]struct {
		Name string `json:"name"`
	} `json:"provider"`
	Agent map[string]struct {
		Model   string `json:"model"`
		Disable bool   `json:"disable"`
	} `json:"agent"`
}

// configTimeout bounds `opencode debug config`, which reads files and
// nothing else.
const configTimeout = 15 * time.Second

// resolveOpenCodeConfig is OpenCode's configuration as it resolves it in
// dir: every source merged, variables filled in. A build without the
// command, or one that fails, is a folder whose listing is all there is.
func resolveOpenCodeConfig(ctx context.Context, bin, dir string, env []string) openCodeResolved {
	var out openCodeResolved
	ctx, cancel := context.WithTimeout(ctx, configTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "debug", "config")
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	raw, err := cmd.Output()
	if err != nil {
		return out
	}
	if start := bytes.IndexByte(raw, '{'); start >= 0 {
		_ = json.NewDecoder(bytes.NewReader(raw[start:])).Decode(&out)
	}
	return out
}
