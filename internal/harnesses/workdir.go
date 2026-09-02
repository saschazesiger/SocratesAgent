package harnesses

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"
)

// The three ways a session decides where it works.
const (
	// WorkdirDynamic gives every session a fresh directory of its own under
	// the workspace root. It is the default because two agents in one
	// directory is how a morning gets lost.
	WorkdirDynamic = "dynamic"
	// WorkdirPreset is one of the places the dashboard named.
	WorkdirPreset = "preset"
	// WorkdirCustom is a path typed into the sheet, and it is only offered
	// when the dashboard allows it: on a machine published through a tunnel,
	// "anywhere on the disk" is a decision rather than a default.
	WorkdirCustom = "custom"
)

// WorkdirModes is every mode, in the order the sheet offers them.
var WorkdirModes = []string{WorkdirDynamic, WorkdirPreset, WorkdirCustom}

// blockedRoots are the directories a custom path may never resolve to. They
// are the ones where an agent with write access is not a mistake but an
// incident, and none of them is ever what somebody meant to type.
var blockedRoots = []string{"/", "/etc", "/usr", "/bin", "/sbin", "/boot"}

// blockedTrees are refused with everything under them. They are the kernel's
// own filesystems: a workspace cannot live there, and a path that reaches into
// one is either a mistake or an attempt.
var blockedTrees = []string{"/proc", "/sys", "/dev"}

// ResolveWorkdir turns a mode and a path from the new-session sheet into the
// directory a session actually runs in, creating it where the mode says to.
//
// Every rule here is enforced on the server. Hiding a control in the sheet is
// a presentation choice; this function is the boundary, and the HTTP layer
// answers 400 with whatever it says.
func ResolveWorkdir(settings config.Settings, mode, path, harness, id string) (string, error) {
	switch mode {
	case "", WorkdirDynamic:
		return dynamicWorkdir(settings, harness, id)
	case WorkdirPreset:
		return presetWorkdir(settings, path)
	case WorkdirCustom:
		return customWorkdir(settings, path)
	}
	return "", fmt.Errorf("%q is not a way of choosing a working directory", mode)
}

// dynamicWorkdir creates <root>/<harness>-<yyyymmdd-hhmmss>-<id[:8]>. The name
// is readable in a shell prompt and unique in a listing, which is the whole of
// what it is for.
func dynamicWorkdir(settings config.Settings, harness, id string) (string, error) {
	root, err := checkPath(settings.Workspace.Root)
	if err != nil {
		return "", err
	}
	if harness == "" {
		harness = "session"
	}
	short := id
	if len(short) > 8 {
		short = short[:8]
	}
	name := fmt.Sprintf("%s-%s-%s", harness, time.Now().Format("20060102-150405"), short)
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("the workspace directory could not be created: %w", err)
	}
	if err := insideRoot(root, dir); err != nil {
		return "", err
	}
	return dir, nil
}

// presetWorkdir accepts one of the dashboard's own presets and nothing else.
// The path is not created: a preset that is gone is a configuration that no
// longer describes this machine, and quietly making the directory would hide
// that.
func presetWorkdir(settings config.Settings, path string) (string, error) {
	clean, err := checkPath(path)
	if err != nil {
		return "", err
	}
	known := false
	for _, p := range settings.Workspace.Presets {
		if filepath.Clean(strings.TrimSpace(p.Path)) == clean {
			known = true
			break
		}
	}
	if !known {
		return "", fmt.Errorf("%s is not one of the preset directories", clean)
	}
	info, err := os.Stat(clean)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("the preset directory %s is gone", clean)
	}
	return clean, nil
}

// customWorkdir accepts a path the person typed. There is no containment rule
// - they asked for it explicitly - but the handful of directories that are
// never an answer are refused, and the sheet has to be allowed to ask at all.
func customWorkdir(settings config.Settings, path string) (string, error) {
	if !settings.Workspace.AllowCustom {
		return "", fmt.Errorf("this Socrates does not allow a typed-in working directory")
	}
	clean, err := checkPath(path)
	if err != nil {
		return "", err
	}
	// Both the path as written and the path it resolves to are judged.
	// Resolving matters because MkdirAll follows a symlink, so /tmp/somewhere
	// -> / would otherwise put an agent at the root of the disk under a
	// harmless-looking name. Judging the written form matters too, because on
	// a merged-usr machine /bin resolves to /usr/bin, and "/bin" is still not
	// somewhere anybody meant to work.
	for _, candidate := range []string{clean, resolveExisting(clean)} {
		if blockedPath(candidate) {
			return "", fmt.Errorf("%s is not a place to run an agent", clean)
		}
	}
	if err := os.MkdirAll(clean, 0o755); err != nil {
		return "", fmt.Errorf("the working directory could not be created: %w", err)
	}
	return clean, nil
}

// blockedPath reports whether a path is one of the places an agent may never
// be given, either exactly or - for the kernel's own filesystems, where
// nothing at all is a workspace - anywhere underneath.
func blockedPath(path string) bool {
	for _, blocked := range blockedRoots {
		if path == blocked {
			return true
		}
	}
	for _, tree := range blockedTrees {
		if path == tree || strings.HasPrefix(path, tree+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// resolveExisting follows the symlinks of the longest part of a path that
// exists, and puts the rest back on the end. A custom directory is usually
// asked for before it exists, so resolving the whole path would answer
// nothing; resolving the part that is there is what catches a link in it.
func resolveExisting(path string) string {
	rest := ""
	for current := path; ; {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return path
		}
		rest = filepath.Join(filepath.Base(current), rest)
		current = parent
	}
}

// checkPath is the shape every path has to have: absolute, clean, and with no
// parent-directory element left in it after cleaning.
func checkPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("a working directory is needed")
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("the working directory %s is not an absolute path", path)
	}
	clean := filepath.Clean(path)
	for _, part := range strings.Split(clean, string(os.PathSeparator)) {
		if part == ".." {
			return "", fmt.Errorf("the working directory %s walks out of itself", path)
		}
	}
	return clean, nil
}

// insideRoot checks containment through the symlinks, so that a root which is
// itself a link, or a session directory that turned out to be one, is compared
// as the place it really is.
func insideRoot(root, dir string) error {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		realRoot = root
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		realDir = dir
	}
	if realDir == realRoot {
		return nil
	}
	if !strings.HasPrefix(realDir, realRoot+string(os.PathSeparator)) {
		return fmt.Errorf("%s is outside the workspace root %s", dir, root)
	}
	return nil
}
