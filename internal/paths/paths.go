// Package paths names the layout of the data directory.
//
// The data directory (SOCRATES_DATA_DIR, /var/lib/socrates by default) is the
// only place Socrates keeps state, and the only truth: the bucket holds a
// backup of it (internal/backup) and is never read while Socrates runs. Its
// layout is docs/design/REBUILD.md §6.1:
//
//	socrates.db               settings, logins, sessions, drafts, push, cost ledger
//	chats/<id>/               a chat's journal, launch spec, attachments and adapter state
//	agents/claude|codex|opencode/  the agents' shared transcripts and data
//	accounts/<id>/            one account's CLAUDE_CONFIG_DIR or CODEX_HOME (an OpenCode
//	                          account's data home is agents/opencode/accounts/<id>/)
//	secrets.key               the key that seals the accounts' API keys (0600)
//	accounts.migrated         the agents' folders have been moved in from HOME, once
//	terminals/<id>/           a Shell terminal's journal
//	sessions/<id>/            today's per-session files (plan.json, journal.raw, ...)
//	profiles/<id>/            today's per-profile CLI state, until accounts replace it
//	vscode/, vscode-extensions/  the editor's state per folder and its extensions
//	backup.json               where the backup goes and its passphrase (not backed up)
//	run/                      sockets, locks, pids (not backed up)
//	cache/                    anything that can be made again (not backed up)
//
// Every package that puts a file into the data directory takes its name from
// here, so that the backup's rules about what is regenerable (NotBackedUp)
// and the code that writes those files cannot drift apart. Some of today's
// files still sit at the top level where earlier builds put them (tmux.sock,
// bin/, updates.json); they are named here as they are.
package paths

import (
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// The names inside the data directory, relative to it.
const (
	DB               = "socrates.db"
	Chats            = "chats"
	Agents           = "agents"
	Accounts         = "accounts"
	Terminals        = "terminals"
	Sessions         = "sessions"
	Profiles         = "profiles"
	VSCode           = "vscode"
	VSCodeExtensions = "vscode-extensions"
	Workspaces       = "workspaces"
	BackupConfig     = "backup.json"
	Run              = "run"
	Cache            = "cache"

	// LegacyS3Config is the bootstrap file of the bucket mirror that came
	// before the backup. It is read once, to carry its bucket over into
	// BackupConfig, and left where it is for a build that is rolled back.
	LegacyS3Config = "s3.json"
	// BackupMigrated marks a data directory whose mirror configuration has
	// been carried over, or found missing, so that it is never carried over
	// again (internal/backup).
	BackupMigrated = "backup.migrated"
	// SecretsKey is the data directory's own key, which seals what Socrates
	// keeps of the accounts: an API key in the store, and the environment a
	// chat's agent host is handed (internal/accounts). It is backed up with
	// the database it opens, so that a restore keeps the keys.
	SecretsKey = "secrets.key"
	// AccountsMigrated marks a data directory whose agents' folders have been
	// moved in from HOME and whose profiles have become accounts, so that it
	// happens once (internal/accounts, REBUILD.md §8.1-8.2).
	AccountsMigrated = "accounts.migrated"
	// PreRebuildBackup is the name the migration's backup gets beside the
	// data directory: <data>.pre-rebuild.tar.
	PreRebuildBackup = ".pre-rebuild.tar"
	// Bin is where Socrates downloads programs of its own (cloudflared).
	Bin = "bin"
	// TmuxSocket, TmuxConf and HookSocket are the private tmux server's.
	TmuxSocket = "tmux.sock"
	TmuxConf   = "tmux.conf"
	HookSocket = "hook.sock"
	// UpdateState, UpdatesStatus and UpdatesLog are the updater's
	// (scripts/update.sh).
	UpdateState   = "update-state"
	UpdatesStatus = "updates.json"
	UpdatesLog    = "updates.log"
	// RestorePending marks a restore whose files are staged and not yet
	// moved into place; RestoreStagingPrefix starts the name of the
	// directory they are staged in (internal/backup).
	RestorePending       = "restore.pending"
	RestoreStagingPrefix = ".restore-"
	// Lock is the file a running Socrates holds locked, so that a second
	// one - or a restore - cannot work on the same data directory.
	Lock = Run + "/socrates.lock"
	// BackupCache is the backup's own bookkeeping.
	BackupCache = Cache + "/backup"

	// The files of a chat's agent host inside chats/<id>/ (internal/agenthost):
	// the launch spec the server writes before every launch, the lock the
	// live host holds, and the host's own log. The host's socket is under
	// ChatSockets, in run/.
	ChatSpec    = "host.json"
	ChatLock    = "host.lock"
	ChatLog     = "host.log"
	ChatSockets = Run + "/chats"
)

// Redacted are files that are backed up without some of what they hold, as
// path.Match patterns relative to the data directory and the top-level JSON
// fields left out. A chat's launch spec carries the environment built from
// its account, and an API key in it only sealed (secret_env); neither is
// kept, as a second line of defence, because the server writes the spec
// again before every launch and a restored chat loses nothing by it.
var Redacted = map[string][]string{
	Chats + "/*/" + ChatSpec: {"env", "secret_env"},
}

// NotBackedUp are the parts of the data directory the backup leaves out, as
// slash separated patterns of path.Match relative to the data directory. A
// pattern that matches a directory leaves out everything below it.
//
// Each is something a new machine makes again by itself, or something that
// is only true of this machine: sockets and locks, caches, downloaded
// binaries, the updater's state, the terminal byte journals (screen output,
// replayed only into a pane that is still running here), the copies a schema
// migration leaves beside the database (the build that is rolled back to on
// this machine reads them; a restore never does), and the backup's own
// configuration, which holds the passphrase that opens the backup. The
// SQLite companions (-wal, -shm, -journal) are left out by the backup itself,
// because every database is copied consistently instead.
var NotBackedUp = []string{
	Run,
	Cache,
	Bin,
	UpdateState,
	UpdatesStatus,
	UpdatesLog,
	BackupConfig,
	BackupMigrated,
	LegacyS3Config,
	TmuxSocket,
	TmuxConf,
	HookSocket,
	RestorePending,
	RestoreStagingPrefix + "*",
	DB + ".pre-*.bak",
	Sessions + "/*/journal.raw*",
	VSCode + "/*/logs",
	VSCode + "/*/CachedData",
	VSCode + "/*/CachedProfilesData",
	// OpenCode's shared XDG_CACHE_HOME, which the tools an OpenCode chat runs
	// inherit too (Go's build cache, pip's, uv's).
	Agents + "/opencode/cache",
	Chats + "/*/" + ChatLock,
	Chats + "/*/" + ChatLog,
	Chats + "/*/" + ChatSpec + ".tmp",
	// Codex's own logs of an account (log/ and its logs database), the
	// "codex logs" of REBUILD.md §6.1.
	Accounts + "/*/log",
	Accounts + "/*/logs_*.sqlite*",
	"lost+found",
}

// BackedUpOnly are folders of which the backup takes only the entries named,
// as path.Match patterns of the folder relative to the data directory. An
// OpenCode account's XDG_DATA_HOME and XDG_STATE_HOME are also those of
// every tool an OpenCode chat runs (uv's Pythons and tools, pipx's
// environments), which a new machine installs again; OpenCode keeps what is
// the account's in its own folder of each.
var BackedUpOnly = map[string][]string{
	Agents + "/opencode/accounts/*":       {"opencode", "state"},
	Agents + "/opencode/accounts/*/state": {"opencode"},
}

// own are the parts of the data directory that are Socrates' state: a folder
// inside one of them is never a working directory to leave out of a backup,
// whatever a session row says.
var own = []string{DB, Chats, Agents, Accounts, Terminals, Sessions, Profiles, VSCode, VSCodeExtensions, SecretsKey}

// Own reports whether rel - a slash separated path relative to the data
// directory - is one of Socrates' own parts or inside one.
func Own(rel string) bool {
	first, _, _ := strings.Cut(rel, "/")
	for _, name := range own {
		if first == name {
			return true
		}
	}
	return false
}

// Excluded reports whether rel - a slash separated path relative to the data
// directory - is one of NotBackedUp, or an entry of a BackedUpOnly folder
// that it does not name.
func Excluded(rel string) bool {
	for _, pattern := range NotBackedUp {
		if ok, _ := path.Match(pattern, rel); ok {
			return true
		}
	}
	dir, name := path.Split(rel)
	dir = strings.TrimSuffix(dir, "/")
	for pattern, kept := range BackedUpOnly {
		if ok, _ := path.Match(pattern, dir); ok && !slices.Contains(kept, name) {
			return true
		}
	}
	return false
}

// Layout is a data directory, with the absolute path of each part.
type Layout struct {
	Root string
}

// New is the layout of the data directory at root.
func New(root string) Layout { return Layout{Root: filepath.Clean(root)} }

// Path is the absolute path of rel, a slash separated name relative to the
// data directory.
func (l Layout) Path(rel string) string { return filepath.Join(l.Root, filepath.FromSlash(rel)) }

// DB is the database file.
func (l Layout) DB() string { return l.Path(DB) }

// Chat is the folder of one chat.
func (l Layout) Chat(id string) string { return filepath.Join(l.Path(Chats), id) }

// Agent is the shared folder of one agent (claude, codex, opencode).
func (l Layout) Agent(name string) string { return filepath.Join(l.Path(Agents), name) }

// Account is the folder of one account: its CLAUDE_CONFIG_DIR or CODEX_HOME.
func (l Layout) Account(id string) string { return filepath.Join(l.Path(Accounts), id) }

// SecretsKey is the data directory's own key file.
func (l Layout) SecretsKey() string { return l.Path(SecretsKey) }

// PreRebuildBackup is the migration's backup beside the data directory.
func (l Layout) PreRebuildBackup() string { return l.Root + PreRebuildBackup }

// Terminal is the folder of one Shell terminal.
func (l Layout) Terminal(id string) string { return filepath.Join(l.Path(Terminals), id) }

// Session is the folder of one session's generated files.
func (l Layout) Session(id string) string { return filepath.Join(l.Path(Sessions), id) }

// BackupConfig is the backup's configuration file.
func (l Layout) BackupConfig() string { return l.Path(BackupConfig) }

// Run is the folder of sockets and locks.
func (l Layout) Run() string { return l.Path(Run) }

// Cache is the folder of what can be made again.
func (l Layout) Cache() string { return l.Path(Cache) }

// Rel is p relative to the data directory, slash separated, and whether p is
// inside it at all (the data directory itself is not).
func (l Layout) Rel(p string) (string, bool) {
	rel, err := filepath.Rel(l.Root, filepath.Clean(p))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}
