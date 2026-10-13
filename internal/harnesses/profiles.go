package harnesses

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// A profile is a second set of sign-ins for the coding agents: a business
// Claude account beside a private one, a work ChatGPT login beside a personal
// one. A session runs in exactly one profile, and "no profile" is the default
// one - the state directories every CLI uses when nobody says otherwise.
//
// Each CLI already has a single lever that moves everything it signs in with,
// so a profile is nothing more than a directory per CLI and that lever pointed
// at it:
//
//	Claude Code  CLAUDE_CONFIG_DIR  <profile>/claude
//	Codex        CODEX_HOME         <profile>/codex
//	OpenCode     XDG_DATA_HOME      <profile>/xdg-data (OpenCode adds /opencode)
//
// Signing in is done by the CLI itself, in a session started in the profile:
// Claude Code opens on its own login when its directory is empty, and a
// container Shell in the profile carries all three variables for
// `codex login` and `opencode auth login`. Socrates never handles a
// credential.
//
// What a person has configured - settings, instructions, skills, agents,
// plugins, MCP servers - is meant to be the same in every profile, so those
// entries are symlinked from the default directories rather than copied. What
// identifies an account or belongs to its conversations is not shared.

// profileIDPattern is what a profile id may look like. The id is a directory
// name under the data directory, so it is a closed alphabet rather than
// something that could climb out of it.
var profileIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// ValidProfileID reports whether id can name a profile. The empty id is the
// default profile and is valid everywhere a profile is.
func ValidProfileID(id string) bool {
	return id == "" || profileIDPattern.MatchString(id)
}

// ProfileDir is the directory that holds everything of one profile.
func ProfileDir(dataDir, id string) string {
	return filepath.Join(dataDir, "profiles", id)
}

// The three CLI directories inside a profile.
const (
	profileClaude  = "claude"
	profileCodex   = "codex"
	profileXDGData = "xdg-data"
)

// ClaudeConfigDirFor is the Claude Code directory of a profile, and the
// default one for the default profile.
func ClaudeConfigDirFor(dataDir, profile string) string {
	if profile == "" {
		return claudeConfigDir()
	}
	return filepath.Join(ProfileDir(dataDir, profile), profileClaude)
}

// CodexHomeFor is the CODEX_HOME of a profile.
func CodexHomeFor(dataDir, profile string) string {
	if profile == "" {
		return codexHome()
	}
	return filepath.Join(ProfileDir(dataDir, profile), profileCodex)
}

// openCodeXDGDataFor is the XDG data home OpenCode is started with in a
// profile; empty for the default profile, which keeps ours.
func openCodeXDGDataFor(dataDir, profile string) string {
	if profile == "" {
		return ""
	}
	return filepath.Join(ProfileDir(dataDir, profile), profileXDGData)
}

// OpenCodeDataDirFor is OpenCode's own data directory in a profile: where its
// database and its auth.json live.
func OpenCodeDataDirFor(dataDir, profile string) string {
	return openCodeDataDirIn(openCodeXDGDataFor(dataDir, profile))
}

// claudeShared is what every Claude Code profile takes from the default
// directory. The credentials, the global .claude.json, the transcripts and
// the history stay with the account they belong to.
var claudeShared = []string{
	"settings.json", "CLAUDE.md", "agents", "commands", "skills", "plugins",
	"hooks", "output-styles", "keybindings.json",
}

// codexShared is the same for Codex. auth.json, the rollouts and the state
// databases are the account's own.
var codexShared = []string{"config.toml", "AGENTS.md", "prompts", "skills", "rules"}

// profileEnv is the environment a session in req's profile is started with,
// for the CLIs named. It creates the profile's directories and links the
// shared configuration into them first, so that the program finds a directory
// that already looks like the default one apart from the sign-in.
//
// The default profile answers with nothing: the CLIs keep the directories
// they would use anyway.
func profileEnv(req PlanRequest, kinds ...Kind) (map[string]string, error) {
	if req.Profile == "" {
		return nil, nil
	}
	if !ValidProfileID(req.Profile) {
		return nil, fmt.Errorf("%q is not a profile id", req.Profile)
	}
	if req.DataDir == "" {
		return nil, errors.New("profiles need a data directory to live in")
	}
	env := map[string]string{}
	for _, kind := range kinds {
		switch kind {
		case KindClaude:
			dir := ClaudeConfigDirFor(req.DataDir, req.Profile)
			if err := prepareProfileDir(dir, claudeConfigDir(), claudeShared); err != nil {
				return nil, err
			}
			env["CLAUDE_CONFIG_DIR"] = dir
		case KindCodex:
			dir := CodexHomeFor(req.DataDir, req.Profile)
			if err := prepareProfileDir(dir, codexHome(), codexShared); err != nil {
				return nil, err
			}
			env["CODEX_HOME"] = dir
		case KindOpenCode:
			dir := openCodeXDGDataFor(req.DataDir, req.Profile)
			// XDG_DATA_HOME is read by every program the agent starts, not by
			// OpenCode alone, so the profile's data home is the default one
			// with OpenCode's own directory taken out: everything else in it
			// is a link to what those programs would have seen anyway.
			shared := defaultXDGDataHome()
			names, err := entriesExcept(shared, "opencode")
			if err != nil {
				return nil, err
			}
			if err := prepareProfileDir(dir, shared, names); err != nil {
				return nil, err
			}
			if err := os.MkdirAll(filepath.Join(dir, "opencode"), 0o700); err != nil {
				return nil, err
			}
			env["XDG_DATA_HOME"] = dir
		}
	}
	return env, nil
}

// prepareProfileDir creates dir and links the named entries of shared into
// it.
//
// An entry that is already there - a link from an earlier launch, or a real
// file the CLI wrote in place of one - is left alone: the profile's own
// version of something is a choice somebody made, and this runs before every
// launch.
func prepareProfileDir(dir, shared string, names []string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if shared == "" || filepath.Clean(shared) == filepath.Clean(dir) {
		return nil
	}
	for _, name := range names {
		source := filepath.Join(shared, name)
		if _, err := os.Lstat(source); err != nil {
			continue
		}
		target := filepath.Join(dir, name)
		if _, err := os.Lstat(target); err == nil {
			continue
		}
		if err := os.Symlink(source, target); err != nil && !os.IsExist(err) {
			return err
		}
	}
	return nil
}

// entriesExcept is every name in dir but one. A directory that does not exist
// has nothing to share.
func entriesExcept(dir, except string) ([]string, error) {
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if entry.Name() != except {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}

// defaultXDGDataHome is the XDG data home a program sees when nothing sets
// one.
func defaultXDGDataHome() string {
	if dir := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); dir != "" {
		return dir
	}
	home := homeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".local", "share")
}

// mergeEnv adds extra to env, which is how a profile's variables join the
// terminal environment a harness builds.
func mergeEnv(env, extra map[string]string) {
	for k, v := range extra {
		env[k] = v
	}
}

// CodexSignIn reports whether a profile has a Codex sign-in, and the e-mail
// address of the ChatGPT account when it is one. The address is read from the
// claims part of the stored id token, which is what Codex shows as the
// account itself; the token is never verified, sent or kept, because the
// answer only labels a row in the dashboard.
func CodexSignIn(dataDir, profile string) (signedIn bool, account string) {
	home := CodexHomeFor(dataDir, profile)
	if home == "" {
		return false, ""
	}
	raw, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil {
		return false, ""
	}
	var doc struct {
		APIKey string `json:"OPENAI_API_KEY"`
		Tokens *struct {
			IDToken string `json:"id_token"`
		} `json:"tokens"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return false, ""
	}
	if doc.Tokens == nil {
		return strings.TrimSpace(doc.APIKey) != "", ""
	}
	parts := strings.Split(doc.Tokens.IDToken, ".")
	if len(parts) == 3 {
		if payload, err := base64.RawURLEncoding.DecodeString(parts[1]); err == nil {
			var claims struct {
				Email string `json:"email"`
			}
			if json.Unmarshal(payload, &claims) == nil {
				account = strings.TrimSpace(claims.Email)
			}
		}
	}
	return true, account
}

// OpenCodeProviders is the providers a profile has credentials stored for in
// OpenCode, by name and in order.
func OpenCodeProviders(dataDir, profile string) []string {
	dir := OpenCodeDataDirFor(dataDir, profile)
	if dir == "" {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(dir, "auth.json"))
	if err != nil {
		return nil
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil {
		return nil
	}
	names := make([]string, 0, len(doc))
	for name := range doc {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
