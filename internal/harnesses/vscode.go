package harnesses

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// VSCode runs code-server in the same tmux lifecycle as a Shell, but an
// editor is ephemeral: it is opened, used and closed, and closing it - the
// last tab on it going away (server ideReaper) or "Close VS Code" - deletes
// the session and everything the editor remembered with it. Its user data
// dir is inside the session's own directory, and its id is new every time, so
// the browser, which keeps the workbench's layout and open editors under a
// name made from the editor's address /ide/<id>/, has never seen it either.
//
// What the person set up carries over from one editor to the next: the user
// settings, keybindings, tasks and snippets are kept in VSCodeProfileDir and
// copied into every new editor (SyncVSCodeProfile takes them back), and the
// extensions are in one directory shared by all of them.
type VSCode struct{ Shell }

func (VSCode) Kind() Kind            { return KindVSCode }
func (VSCode) Label() string         { return "VS Code" }
func (VSCode) DefaultBinary() string { return "code-server" }

// VSCodeSocket is private to the session, inside Socrates' protected data dir.
func VSCodeSocket(dataDir, id string) string {
	return SessionFile(dataDir, id, "ide.sock")
}

// VSCodeDataDir is the user-data-dir of one editor. It is in the session's
// directory, which deleting the session removes.
func VSCodeDataDir(dataDir, id string) string {
	return SessionFile(dataDir, id, "vscode-data")
}

// VSCodeProfileDir is where what the person set up in VS Code is kept
// between editors, laid out like a user-data-dir: User/settings.json,
// User/keybindings.json, User/tasks.json and User/snippets/.
func VSCodeProfileDir(dataDir string) string {
	return filepath.Join(dataDir, "vscode")
}

// vscodeKept are the parts of a user-data-dir that outlive an editor,
// relative to it. Everything else in it - workspace state, history, hot exit
// backups, logs, caches - goes with the editor.
var vscodeKept = []string{
	filepath.Join("User", "settings.json"),
	filepath.Join("User", "keybindings.json"),
	filepath.Join("User", "tasks.json"),
	filepath.Join("User", "snippets"),
}

// NewVSCodeID is the id of a new editor: random, because the browser keeps
// what a workbench remembers under its address, and an editor that is gone
// must not hand its open files and layout to the next one.
func NewVSCodeID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

// vscodeDefaults is the settings.json the person's first editor starts with.
// An editor is thrown away when its tab closes, so hot exit - which keeps
// unsaved changes in a backup and lets the tab close without a word - would
// lose them silently; off, the browser asks before a tab with unsaved
// changes is closed. It is an application setting, which VS Code reads from
// the user settings only. From then on the file is the person's.
const vscodeDefaults = `{
  "files.hotExit": "off"
}
`

// vscodeFolderDefaults is what editors were given while every folder kept an
// editor of its own. A folder's settings.json that still reads exactly this
// was never changed by anybody, and is not worth carrying over.
const vscodeFolderDefaults = `{
  "files.hotExit": "onExitAndWindowClose",
  "terminal.integrated.enablePersistentSessions": true,
  "terminal.integrated.persistentSessionReviveProcess": "onExitAndWindowClose"
}
`

func (v VSCode) Plan(_ context.Context, req PlanRequest) (LaunchPlan, error) {
	bin, err := resolveBinary(req.Settings.Harnesses.VSCode.Binary, v.DefaultBinary())
	if err != nil {
		return LaunchPlan{}, err
	}
	socket := VSCodeSocket(req.DataDir, req.SessionID)
	if len(socket) > 100 {
		return LaunchPlan{}, fmt.Errorf("VS Code socket path is too long; use a shorter Socrates data directory")
	}
	envBin, err := resolveBinary("", "env")
	if err != nil {
		return LaunchPlan{}, err
	}
	cfg := SessionFile(req.DataDir, req.SessionID, "code-server.yaml")
	userData := VSCodeDataDir(req.DataDir, req.SessionID)
	profile := VSCodeProfileDir(req.DataDir)
	migrateVSCodeFolders(profile)
	files := []GenFile{{Path: cfg, Mode: 0600, Data: []byte("{}\n")}}
	files = append(files, vscodeSeed(profile, userData)...)
	env := baseEnv(req)
	// An invocation from an existing VS Code terminal must start a new server,
	// rather than forward the folder to that terminal's parent editor.
	for _, key := range []string{"VSCODE_IPC_HOOK_CLI", "VSCODE_OPTIONS", "VSCODE_PROXY_URI", "CODE_SERVER_SESSION_SOCKET", "CODE_SERVER_IDLE_TIMEOUT_SECONDS"} {
		env[key] = ""
	}
	return LaunchPlan{
		Argv: []string{
			envBin, "-u", "CODE_SERVER_PARENT_PID", bin,
			"--config", cfg, "--auth", "none",
			"--socket", socket, "--socket-mode", "0600",
			"--session-socket", SessionFile(req.DataDir, req.SessionID, "ipc.sock"),
			"--user-data-dir", userData,
			"--extensions-dir", filepath.Join(req.DataDir, "vscode-extensions"),
			"--disable-telemetry", "--disable-update-check",
			"--abs-proxy-base-path", "/ide/" + req.SessionID, req.Cwd,
		},
		Env: env, Cwd: req.Cwd, Files: files,
	}, nil
}

// vscodeSeed is what an editor's user data dir starts with: the person's
// kept files, or the defaults for an editor that is the very first. A file
// the editor already has is left alone: a resume of a running editor plans it
// again, and what the editor changed since it was opened is newer than the
// profile until SyncVSCodeProfile has taken it back.
func vscodeSeed(profile, userData string) []GenFile {
	var files []GenFile
	have := func(path string) bool {
		_, err := os.Lstat(path)
		return !errors.Is(err, fs.ErrNotExist)
	}
	_ = walkVSCodeKept(profile, func(rel string, data []byte) {
		if dst := filepath.Join(userData, rel); !have(dst) {
			files = append(files, GenFile{Path: dst, Mode: 0600, Data: data})
		}
	})
	settings := filepath.Join("User", "settings.json")
	if !have(filepath.Join(profile, settings)) && !have(filepath.Join(userData, settings)) {
		files = append(files, GenFile{Path: filepath.Join(userData, settings), Mode: 0600, Data: []byte(vscodeDefaults)})
	}
	return files
}

// walkVSCodeKept calls fn with every regular file among the kept parts of
// the user data dir root, by its path relative to root.
func walkVSCodeKept(root string, fn func(rel string, data []byte)) error {
	for _, kept := range vscodeKept {
		err := filepath.WalkDir(filepath.Join(root, kept), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			if !d.Type().IsRegular() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			fn(rel, data)
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// SyncVSCodeProfile takes what the editor of session id changed among the
// kept files back into the profile, for the editors opened after it. A file
// is taken when it is newer than the profile's copy: one the editor was only
// given at its start is as old as that or reads the same, and two editors
// open at once do not undo each other's changes by merely having been open. A file the editor
// deleted stays in the profile.
func SyncVSCodeProfile(dataDir, id string) error {
	userData := VSCodeDataDir(dataDir, id)
	profile := VSCodeProfileDir(dataDir)
	var first error
	err := walkVSCodeKept(userData, func(rel string, data []byte) {
		src, err := os.Stat(filepath.Join(userData, rel))
		if err != nil {
			return
		}
		dst := filepath.Join(profile, rel)
		if have, err := os.Stat(dst); err == nil {
			if !src.ModTime().After(have.ModTime()) {
				return
			}
			if old, err := os.ReadFile(dst); err == nil && bytes.Equal(old, data) {
				return
			}
		}
		if err := writeFileAtomic(dst, data); err != nil && first == nil {
			first = err
		}
	})
	if err != nil {
		return err
	}
	return first
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp.Name(), 0o600)
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), path)
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
	}
	return werr
}

// migrateVSCodeFolders retires the editors' state from when every folder
// kept an editor of its own, in profile/<sha256(folder)[:8]>: what the person
// set up in the folder used last is carried into the profile, unless the
// profile has settings already, and the rest - open editors, layout, hot
// exit backups, terminals - is what an editor no longer keeps, so it goes.
func migrateVSCodeFolders(profile string) {
	entries, err := os.ReadDir(profile)
	if err != nil {
		return
	}
	var folders []string
	for _, entry := range entries {
		if entry.IsDir() && isFolderHash(entry.Name()) {
			folders = append(folders, filepath.Join(profile, entry.Name()))
		}
	}
	if len(folders) == 0 {
		return
	}
	settings := filepath.Join("User", "settings.json")
	if _, err := os.Stat(filepath.Join(profile, settings)); errors.Is(err, fs.ErrNotExist) {
		var newest string
		var at int64
		for _, dir := range folders {
			info, err := os.Stat(filepath.Join(dir, settings))
			if err != nil || !vscodeChanged(dir) {
				continue
			}
			if newest == "" || info.ModTime().UnixNano() > at {
				newest, at = dir, info.ModTime().UnixNano()
			}
		}
		if newest != "" {
			_ = walkVSCodeKept(newest, func(rel string, data []byte) {
				_ = writeFileAtomic(filepath.Join(profile, rel), data)
			})
		}
	}
	for _, dir := range folders {
		_ = os.RemoveAll(dir)
	}
}

// vscodeChanged says whether the person set anything up in a folder's old
// editor: settings other than the defaults it was given, or any of the other
// kept files.
func vscodeChanged(dir string) bool {
	changed := false
	_ = walkVSCodeKept(dir, func(rel string, data []byte) {
		if rel != filepath.Join("User", "settings.json") || string(data) != vscodeFolderDefaults {
			changed = true
		}
	})
	return changed
}

func isFolderHash(name string) bool {
	if len(name) != 16 {
		return false
	}
	_, err := hex.DecodeString(name)
	return err == nil
}
