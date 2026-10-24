package agenthost

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/paths"
)

// The files of one chat, named in internal/paths. The chat's directory is
// part of the data folder and is backed up with it - all but the lock and the
// log, and the spec without its environment (paths.Redacted); the socket is
// under run/, which is not.
//
//	<data>/chats/<id>/events.jsonl   the journal (agent.JournalFile)
//	<data>/chats/<id>/host.json      the launch spec the server writes
//	<data>/chats/<id>/host.lock      held by the live host, and only by it
//	<data>/chats/<id>/host.log       the host's own output
//	<data>/chats/<id>/agent/         the adapter's own state
//	<data>/run/chats/<id>.sock       the host's socket
const (
	chatsDir  = paths.Chats
	specName  = paths.ChatSpec
	lockName  = paths.ChatLock
	logName   = paths.ChatLog
	stateDir  = "agent"
	socketDir = paths.ChatSockets
)

// sockPathLimit is how long a Unix socket path may be, the terminating NUL
// included: Linux allows 108 bytes in sun_path, the BSDs and macOS 104, and
// the smaller is used everywhere (termux.CheckSocketPaths does the same).
const sockPathLimit = 104

// chatIDPattern is what a chat id may look like. It becomes a file name, a
// socket name and a systemd unit name, so nothing that could be read as a
// path or an option is let through.
var chatIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ValidChatID reports whether id is usable as a chat id.
func ValidChatID(id string) bool { return chatIDPattern.MatchString(id) }

// ChatDir is the directory of one chat.
func ChatDir(dataDir, chatID string) string { return filepath.Join(dataDir, chatsDir, chatID) }

// JournalPath is the chat's journal.
func JournalPath(dataDir, chatID string) string {
	return filepath.Join(ChatDir(dataDir, chatID), agent.JournalFile)
}

// SocketPath is where the chat's host listens. It is <data>/run/chats/<id>.sock
// when that fits in a socket address, and otherwise a short path in the
// temporary directory that is the same for the same data folder and chat, so
// the server and the host always agree on it without being told.
func SocketPath(dataDir, chatID string) string {
	path := filepath.Join(dataDir, socketDir, chatID+".sock")
	if len(path) < sockPathLimit {
		return path
	}
	sum := sha256.Sum256([]byte(dataDir))
	return filepath.Join(os.TempDir(), fmt.Sprintf("socrates-%d", os.Getuid()), hex.EncodeToString(sum[:6]), chatID+".sock")
}

// Spec is what the server tells a host about the chat it is to run. It is
// written to host.json before every launch, so a host started after a park
// reads the chat's settings as they are now.
type Spec struct {
	ChatID  string `json:"chat_id"`
	Harness string `json:"harness"`
	Workdir string `json:"workdir"`
	Model   string `json:"model,omitempty"`
	Effort  string `json:"effort,omitempty"`
	// Account and Env are the chat's account and the CLI environment built
	// for it (WP7). Env empty is the host's own environment, scrubbed.
	Account string   `json:"account,omitempty"`
	Env     []string `json:"env,omitempty"`
	// Binary is the harness's configured program (the dashboard's binary
	// setting), empty for the one on the PATH.
	Binary string `json:"binary,omitempty"`
	// Conversation is the CLI conversation the server knows for the chat. A
	// host whose journal names none resumes it, and an empty journal gets
	// the history back from it (StartSpec.Rebuild, REBUILD.md §2.2).
	Conversation string `json:"conversation,omitempty"`
	// Ultracode is the session option of a Claude Code chat: dynamic
	// workflows on every task. The host says it to the adapter in the CLI's
	// environment (UltracodeEnv), and nowhere else.
	Ultracode bool `json:"ultracode,omitempty"`
	// ParkAfterMS is how long the host may be idle before it parks.
	ParkAfterMS int64 `json:"park_after_ms"`
}

// WriteSpec writes a chat's launch spec, atomically.
func WriteSpec(dataDir string, spec Spec) error {
	dir := ChatDir(dataDir, spec.ChatID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, specName+".tmp")
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, specName))
}

// ReadSpec reads a chat's launch spec.
func ReadSpec(dataDir, chatID string) (Spec, error) {
	var spec Spec
	raw, err := os.ReadFile(filepath.Join(ChatDir(dataDir, chatID), specName))
	if err != nil {
		return spec, err
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		return spec, fmt.Errorf("the launch spec of chat %s is damaged: %w", chatID, err)
	}
	return spec, nil
}
