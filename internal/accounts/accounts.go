// Package accounts is what a chat runs on: one agent's subscription login or
// API key (REBUILD.md §4). It replaces the sign-in profiles, which were one
// folder per agent and could not be switched in a running session.
//
// An account is a row in the store (store.Account) and a folder:
//
//	Claude Code  <data>/accounts/<id>/                  CLAUDE_CONFIG_DIR
//	Codex        <data>/accounts/<id>/                  CODEX_HOME
//	OpenCode     <data>/agents/opencode/accounts/<id>/  XDG_DATA_HOME (the adapter's layout)
//
// Every account of an agent shares that agent's conversations
// (<data>/agents/<agent>/, linked into the folder by relative symlinks), so a
// chat can move between them. A subscription's login is written into the
// folder by the agent's own sign-in, and nothing here ever writes, refreshes
// or copies it; Socrates only looks whether it is there. An API key is kept
// in the store, sealed with the data directory's own key (secret.go), and
// reaches the CLI as variables of its environment that are sealed again for
// the one chat that runs it.
//
// This package builds that environment (Env), makes the folders (Prepare),
// reads each account's limits and the one to recommend (limits.go), signs
// accounts in (signin_*.go), and moves the folders of the build before it in
// once (migrate.go, REBUILD.md §8.1-8.2).
package accounts

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/paths"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// The agents an account belongs to. The scripted agent of the tests has
// accounts too while it is switched on, so that switching can be shown with
// it.
const (
	Claude   = "claude"
	Codex    = "codex"
	OpenCode = "opencode"
	Fake     = agent.FakeHarness
)

// The kinds of account.
const (
	// Subscription is a login through the agent's own sign-in: a Claude
	// subscription, ChatGPT for Codex, a provider's OAuth for OpenCode.
	Subscription = "subscription"
	// APIKey is a key typed into the Accounts card.
	APIKey = "api_key"
)

// The states of an account.
const (
	SignedIn    = "signed_in"
	NeedsSignIn = "needs_sign_in"
	Failed      = "error"
)

// Origins of the accounts the migration (migrate.go) makes of the agents'
// own folders in HOME. An account of a profile's folder has the origin
// profile:<profile>:<agent>, and one of a profile whose folders were lost
// lost:<profile>:<agent>.
const (
	OriginClaude   = "~/.claude"
	OriginCodex    = "~/.codex"
	OriginOpenCode = "~/.local/share/opencode"
)

// HomeOrigin reports whether an account holds an agent's own sign-in of this
// machine, which the agent's folder in HOME is a link to.
func HomeOrigin(origin string) bool {
	return origin == OriginClaude || origin == OriginCodex || origin == OriginOpenCode
}

// profileFolders are where an old profile kept each agent's folder, under
// <data>/profiles/<profile>/.
var profileFolders = []struct{ agent, sub string }{
	{Claude, "claude"}, {Codex, "codex"}, {OpenCode, filepath.Join("xdg-data", "opencode")},
}

// ProfileLink is the old profile's folder that the migration left as a link
// to the account made of it, or "" for an account made otherwise.
func ProfileLink(dataDir string, a *store.Account) string {
	rest, ok := strings.CutPrefix(a.Origin, "profile:")
	if !ok {
		return ""
	}
	id, agentName, ok := strings.Cut(rest, ":")
	if !ok || id == "" || id == "." || id == ".." || strings.ContainsAny(id, `/\`) {
		return ""
	}
	for _, one := range profileFolders {
		if one.agent == agentName {
			return filepath.Join(paths.New(dataDir).Path(paths.Profiles), id, one.sub)
		}
	}
	return ""
}

// Why an account the provider refused cannot be used until it signs in again
// or gets a new key (agent.ErrorAuth).
const (
	RefusedDetail    = "The sign-in was refused when a chat used it - another chat on this account may have renewed it at the same moment. Sign in again."
	KeyRefusedDetail = "The key was refused when a chat used it. Replace it."
)

// Refused reports whether an account is out of use because the provider
// refused it: its login may still be on disk, but it does not work.
func Refused(a *store.Account) bool {
	return a.State == Failed && (a.StateDetail == RefusedDetail || a.StateDetail == KeyRefusedDetail)
}

// DefaultLabel is what the account the migration makes of an agent's own
// sign-in is called, and the account a chat of an agent without any gets.
const DefaultLabel = "Default"

// MaxLabel bounds a label, which is a word on a button.
const MaxLabel = 40

// Agents is every agent an account can belong to, in the order the Accounts
// card shows them, the scripted one only when it is switched on.
func Agents() []string {
	out := []string{Claude, Codex, OpenCode}
	if _, ok := agent.Lookup(Fake); ok {
		out = append(out, Fake)
	}
	return out
}

// ValidAgent reports whether an account can belong to the agent.
func ValidAgent(name string) bool {
	for _, a := range Agents() {
		if a == name {
			return true
		}
	}
	return false
}

var idPattern = regexp.MustCompile(`^a-[0-9a-f]{12}$`)

// ValidID reports whether id is an account id: a name of the server's
// choosing, which becomes a folder name and an OpenCode account.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// NewID is a fresh account id.
func NewID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "a-" + hex.EncodeToString(b[:])
}

// Label tidies a label and refuses one that cannot be one.
func Label(raw string) (string, error) {
	label := strings.Join(strings.Fields(raw), " ")
	switch {
	case label == "":
		return "", errors.New("an account needs a name")
	case utf8.RuneCountInString(label) > MaxLabel:
		return "", errors.New("an account's name has at most 40 characters")
	}
	return label, nil
}

// Folder is the folder an account of an agent lives in: the agent's
// CLAUDE_CONFIG_DIR or CODEX_HOME, OpenCode's data home of the account, and
// for the scripted agent a folder of its own. It is absolute, because a CLI
// runs in the chat's working directory.
func Folder(dataDir, agentName, id string) string {
	if abs, err := filepath.Abs(dataDir); err == nil {
		dataDir = abs
	}
	l := paths.New(dataDir)
	if agentName == OpenCode {
		return filepath.Join(l.Agent(OpenCode), "accounts", id)
	}
	return l.Account(id)
}

// SharedFolder is where every account of an agent keeps the conversations
// it shares: <data>/agents/<agent>.
func SharedFolder(dataDir, agentName string) string {
	if abs, err := filepath.Abs(dataDir); err == nil {
		dataDir = abs
	}
	return paths.New(dataDir).Agent(agentName)
}

// passed are the variables of the service's own environment a chat's CLI
// gets: where programs are, whose home it is, the language and time zone,
// the temporary directory, the certificates and proxies a network may need,
// and the binary overrides of the agents. Nothing else of the service's -
// its systemd variables, other accounts' keys, Socrates' own settings - ever
// reaches a CLI.
var passed = []string{
	"PATH", "HOME", "USER", "LOGNAME", "SHELL", "LANG", "LANGUAGE", "TZ", "TMPDIR",
	"SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS", "REQUESTS_CA_BUNDLE", "CURL_CA_BUNDLE",
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "no_proxy", "all_proxy",
	"SOCRATES_CODEX_BIN", "SOCRATES_OPENCODE_BIN",
}

// passedPrefixes are the families of variables passed the same way.
var passedPrefixes = []string{"LC_"}

func passes(key string) bool {
	for _, k := range passed {
		if key == k {
			return true
		}
	}
	for _, p := range passedPrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

// Env is the whole environment a CLI of the account runs with, without its
// key (SecretEnv): what of base (the service's own environment) passes, and
// the variable that points the agent at the account's folder. Codex refuses
// an empty environment, and every adapter takes this as all there is.
func Env(dataDir string, a *store.Account, base []string) []string {
	var env []string
	home := ""
	for _, kv := range base {
		key, value, ok := strings.Cut(kv, "=")
		if !ok || !passes(key) {
			continue
		}
		if key == "HOME" {
			home = value
		}
		env = append(env, kv)
	}
	switch a.Agent {
	case Claude:
		env = append(env, "CLAUDE_CONFIG_DIR="+Folder(dataDir, Claude, a.ID))
	case Codex:
		env = append(env, "CODEX_HOME="+Folder(dataDir, Codex, a.ID))
	case OpenCode:
		// OpenCode's adapter points XDG_CONFIG_HOME at its own layout, which
		// would hide the person's gh login and git configuration from the
		// tools a chat runs; their own variables bring them back.
		env = append(env, toolConfig(base, home)...)
	}
	return env
}

// toolConfig is GH_CONFIG_DIR and GIT_CONFIG_GLOBAL for an OpenCode chat,
// where the person's configuration is only found through XDG_CONFIG_HOME.
// Each one the service sets itself is passed as it is.
func toolConfig(base []string, home string) []string {
	configHome, gh, git := "", "", ""
	for _, kv := range base {
		if k, v, ok := strings.Cut(kv, "="); ok {
			switch k {
			case "XDG_CONFIG_HOME":
				configHome = v
			case "GH_CONFIG_DIR":
				gh = kv
			case "GIT_CONFIG_GLOBAL":
				git = kv
			}
		}
	}
	if configHome == "" && home != "" {
		configHome = filepath.Join(home, ".config")
	}
	if gh == "" && configHome != "" {
		if fi, err := os.Stat(filepath.Join(configHome, "gh")); err == nil && fi.IsDir() {
			gh = "GH_CONFIG_DIR=" + filepath.Join(configHome, "gh")
		}
	}
	// GIT_CONFIG_GLOBAL replaces ~/.gitconfig as well, so it is only set
	// where that does not exist and the XDG one does.
	if git == "" && configHome != "" && home != "" {
		gitXDG := filepath.Join(configHome, "git", "config")
		if _, err := os.Stat(gitXDG); err == nil {
			if _, err := os.Stat(filepath.Join(home, ".gitconfig")); errors.Is(err, os.ErrNotExist) {
				git = "GIT_CONFIG_GLOBAL=" + gitXDG
			}
		}
	}
	var out []string
	for _, kv := range []string{gh, git} {
		if kv != "" {
			out = append(out, kv)
		}
	}
	return out
}

// SecretEnv is the variables an API-key account's key reaches its CLI in:
// SOCRATES_CLAUDE_API_KEY, which Claude's adapter turns into
// ANTHROPIC_API_KEY; OPENCODE_AUTH_CONTENT with the one provider's key, read
// only, never written back (research/06 §3.2); the scripted agent's own.
// Codex has none: its key is logged in into the account's CODEX_HOME by codex
// itself (SignInCodexKey).
func SecretEnv(agentName, provider, key string) []string {
	if key == "" {
		return nil
	}
	switch agentName {
	case Claude:
		return []string{"SOCRATES_CLAUDE_API_KEY=" + key}
	case OpenCode:
		if provider == "" {
			return nil
		}
		raw, err := jsonObject(provider, map[string]string{"type": "api", "key": key})
		if err != nil {
			return nil
		}
		return []string{"OPENCODE_AUTH_CONTENT=" + raw}
	case Fake:
		return []string{agent.FakeKeyEnv + "=" + key}
	}
	return nil
}
