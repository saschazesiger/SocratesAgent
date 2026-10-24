//go:build !windows

package accounts

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"

	"github.com/saschazesiger/SocratesAgent/internal/agent/claude"
	"github.com/saschazesiger/SocratesAgent/internal/agent/codex"
	"github.com/saschazesiger/SocratesAgent/internal/agent/opencode"
	"github.com/saschazesiger/SocratesAgent/internal/paths"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// The migration of the build before accounts (REBUILD.md §8.1-8.3), once, on
// the first start of this one: the agents' terminals go (the clean cut,
// cleancut.go), the agents' own folders in HOME become the Default account
// of each agent, the profiles become accounts - with their logins where
// their folders still exist, marked "sign in again" where they were lost -
// and every account's conversations go into the folders they share.
//
// It runs only on a data directory of the build before: the store marks a
// database it brings up to the schema of accounts (store.go), and nothing
// else. A data directory this build makes - a new installation, a trial
// with -data /tmp/x, an e2e run - never takes anything from HOME, and
// neither does one in the temporary directory while HOME is not: a copy of
// the old data directory rehearsed there would otherwise carry the person's
// real logins into a folder about to be deleted.
//
// A login is never copied. OAuth refresh tokens rotate: of two copies, the
// first to refresh kills the other, and whatever runs on it is logged out -
// which is what happened to the person every few minutes. So a folder is
// moved, never copied (a rename, which keeps every file's inode), and the
// old path becomes a link to the new place, in one step (swap): a Claude
// Code or Codex still running from it, or started later by hand, keeps
// using the one file and never finds the old path missing. A move that
// would cross file systems, where a rename is a copy, is not made, and
// neither is one of a link the person made to somewhere else: that folder
// stays where it is and its account has no login.
//
// Before anything moves or goes, all of it - the data directory and the
// folders about to move - is written to <data>.pre-rebuild.tar. Every step is
// idempotent and decides from what is on the disk and from the accounts
// already made, so a migration cut short anywhere (a crash, a power cut) is
// finished by the next start without losing or doubling anything: the
// accounts are found again by their origin, a folder already moved is not
// moved again, and a missing link is made. A source that fails is said in
// the log and tried again at the next start; the others go on.

// MigrateOptions is one migration.
type MigrateOptions struct {
	DataDir string
	Store   *store.Store
	// Env is the service's environment, which says where the agents' own
	// folders are: HOME, CLAUDE_CONFIG_DIR, CODEX_HOME, XDG_DATA_HOME,
	// XDG_CONFIG_HOME.
	Env  []string
	Logf func(format string, args ...any)
	// StopTerminal stops one session the way a delete does - its tmux
	// session killed, its row and its folder removed - which is what the
	// clean cut does to an agent's terminal. main hands the terminal
	// substrate's own delete.
	StopTerminal func(id string) error
	// stopAfter ends the migration after the step of that name as a crash
	// would, and tempDir stands in for the temporary directory, for the
	// tests.
	stopAfter string
	tempDir   string
}

// errStopped is a migration ended by stopAfter.
var errStopped = errors.New("the migration was stopped")

// errNoExchange is a file system that cannot swap two names in one step
// (exchange).
var errNoExchange = errors.New("the file system cannot exchange two names")

// errForeignLink is an agent's folder that is a link of the person's own to
// somewhere outside the data directory: what it points at is not this
// migration's to move.
var errForeignLink = errors.New("a link to somewhere else")

// lostDetail is why an account made from a profile whose folders are gone
// cannot be used yet.
const lostDetail = "The sign-in was lost with the old profile's folder. Sign in again."

type migration struct {
	opts   MigrateOptions
	layout paths.Layout
	home   string
	env    map[string]string
}

// source is one folder (or file) to move into the data directory.
type source struct {
	origin string
	agent  string
	label  string
	// from is the folder in HOME or the old profile's folder; link is what
	// the link left at from points at: absolute for HOME, relative inside
	// the data directory.
	from string
	// claudeJSON is ~/.claude.json, Claude Code's global config beside its
	// folder when CLAUDE_CONFIG_DIR is unset.
	claudeJSON string
	// relative links (a profile's folder, inside the data directory).
	relative bool
}

// Migrate runs the migration if this data directory is one of the build
// before accounts and has not had it.
func Migrate(opts MigrateOptions) error {
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	pending, err := opts.Store.AccountsMigrationPending()
	if err != nil || !pending {
		return err
	}
	abs, err := filepath.Abs(opts.DataDir)
	if err != nil {
		return err
	}
	m := &migration{opts: opts, layout: paths.New(abs), env: map[string]string{}}
	for _, kv := range opts.Env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m.env[k] = v
		}
	}
	m.home = m.env["HOME"]
	sources, lost, err := m.plan()
	if err != nil {
		return err
	}
	cut, err := m.agentTerminals()
	if err != nil {
		return err
	}
	if len(sources) > 0 || len(lost) > 0 || len(cut) > 0 {
		if err := m.backup(sources); err != nil {
			return fmt.Errorf("the backup before the migration: %w", err)
		}
		if err := m.step("backup"); err != nil {
			return err
		}
	}
	// The lost profiles' accounts come first: which agents a lost profile
	// ran is read off its sessions (plan), and the clean cut removes the
	// agents' terminals among them, so a run cut short after it would read
	// nothing there.
	var failed []error
	for _, a := range lost {
		err := m.opts.Store.CreateAccount(a)
		if err == nil {
			err = Prepare(m.layout.Root, a)
		}
		if err == nil {
			err = m.step("lost:" + a.Origin)
		}
		if errors.Is(err, errStopped) {
			return err
		}
		if err != nil {
			failed = append(failed, fmt.Errorf("the account of the lost profile %s: %w", a.Label, err))
		}
	}
	// Then the clean cut, before anything moves: a terminal still running
	// an agent would write into a folder in the middle of its move.
	if err := m.cleanCut(cut); err != nil {
		if errors.Is(err, errStopped) {
			return err
		}
		failed = append(failed, fmt.Errorf("the clean cut: %w", err))
	}
	for _, src := range sources {
		if err := m.move(src); err != nil {
			if errors.Is(err, errStopped) {
				return err
			}
			failed = append(failed, fmt.Errorf("move %s: %w", src.from, err))
		}
	}
	if len(failed) > 0 {
		return errors.Join(failed...)
	}
	if err := m.lostLast(); err != nil {
		return err
	}
	// Last, once every account is made: a prepared chat whose profile's
	// account is missing is removed, and a move that failed this time may
	// make it at the next start.
	if err := m.bindPrepared(); err != nil {
		return err
	}
	if err := m.opts.Store.FinishAccountsMigration(); err != nil {
		return err
	}
	if len(sources) > 0 {
		m.opts.Logf("accounts: the agents' folders are in %s now; the old ones are links to them", m.layout.Root)
	}
	return nil
}

// lostLast puts the lost profiles' accounts below the others. They were made
// first (see Migrate) and say "Sign in again", and the lists offer accounts in
// their order: above every Default account, they would be the first thing
// each agent's list offered.
func (m *migration) lostLast() error {
	list, err := m.opts.Store.ListAccounts()
	if err != nil {
		return err
	}
	var working, lost []string
	for _, a := range list {
		if strings.HasPrefix(a.Origin, "lost:") {
			lost = append(lost, a.ID)
		} else {
			working = append(working, a.ID)
		}
	}
	if len(lost) == 0 || len(working) == 0 {
		return nil
	}
	return m.opts.Store.ReorderAccounts(append(working, lost...))
}

func (m *migration) step(name string) error {
	if m.opts.stopAfter == name {
		return errStopped
	}
	return nil
}

// plan is what there is to move, and the profiles whose folders are gone. A
// source is there while its folder is, and also once its account has been
// made: a run cut short after the move and before the link has left nothing
// at the old path, and the account is what says the rest is still to do.
func (m *migration) plan() ([]source, []*store.Account, error) {
	list, err := m.opts.Store.ListAccounts()
	if err != nil {
		return nil, nil, err
	}
	begun := map[string]bool{}
	for i := range list {
		if list[i].Origin != "" {
			begun[list[i].Origin] = true
		}
	}
	var sources []source
	add := func(src source) {
		if src.from == "" {
			return
		}
		if why := m.leftAlone(src.from); why != "" {
			m.opts.Logf("accounts: %s stays where it is: %s", src.from, why)
			return
		}
		if begun[src.origin] || m.present(src.from) {
			sources = append(sources, src)
			return
		}
		if src.claudeJSON != "" {
			if _, err := os.Lstat(src.claudeJSON); err == nil {
				sources = append(sources, src)
			}
		}
	}
	claudeDir, claudeJSON := m.env["CLAUDE_CONFIG_DIR"], ""
	if claudeDir == "" && m.home != "" {
		claudeDir, claudeJSON = filepath.Join(m.home, ".claude"), filepath.Join(m.home, ".claude.json")
	}
	add(source{origin: OriginClaude, agent: Claude, label: DefaultLabel, from: claudeDir, claudeJSON: claudeJSON})
	codexHome := m.env["CODEX_HOME"]
	if codexHome == "" && m.home != "" {
		codexHome = filepath.Join(m.home, ".codex")
	}
	add(source{origin: OriginCodex, agent: Codex, label: DefaultLabel, from: codexHome})
	add(source{origin: OriginOpenCode, agent: OpenCode, label: DefaultLabel, from: m.xdg("XDG_DATA_HOME", ".local/share")})

	var profiles []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := m.opts.Store.GetJSON("profiles", &profiles); err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, nil, err
	}
	used, err := m.profileAgents()
	if err != nil {
		return nil, nil, err
	}
	var lost []*store.Account
	for _, p := range profiles {
		if p.ID == "" || strings.ContainsAny(p.ID, `/\`) || p.ID == "." || p.ID == ".." {
			continue
		}
		label, err := Label(p.Name)
		if err != nil {
			label = p.ID
		}
		dir := filepath.Join(m.layout.Path(paths.Profiles), p.ID)
		found := 0
		for _, one := range profileFolders {
			from, origin := filepath.Join(dir, one.sub), "profile:"+p.ID+":"+one.agent
			if !begun[origin] && !m.present(from) {
				continue
			}
			found++
			sources = append(sources, source{origin: origin, agent: one.agent, label: label, from: from, relative: true})
		}
		if found > 0 {
			continue
		}
		// The folders went with the move from Docker (HANDOFF.md §5): the
		// profile becomes an account of each agent its sessions ran, or of
		// Claude Code, to be signed in again. A run cut short after it made
		// them has said which: the clean cut may have removed the sessions
		// since.
		agents := lostAgents(begun, p.ID, used[p.ID])
		if len(agents) == 0 {
			agents = []string{Claude}
		}
		for _, name := range agents {
			lost = append(lost, &store.Account{ID: NewID(), Agent: name, Label: label, Kind: Subscription,
				State: NeedsSignIn, StateDetail: lostDetail, Origin: "lost:" + p.ID + ":" + name})
		}
	}
	return sources, lost, nil
}

// lostAgents is the agents a lost profile becomes accounts of: those its
// sessions ran, and those an earlier run made its accounts for.
func lostAgents(begun map[string]bool, profile string, ran []string) []string {
	var out []string
	for _, name := range []string{Claude, Codex, OpenCode} {
		if begun["lost:"+profile+":"+name] || slices.Contains(ran, name) {
			out = append(out, name)
		}
	}
	return out
}

// present reports whether an agent's folder is there to move, or was moved
// here already: a folder, or a link to one.
func (m *migration) present(path string) bool {
	fi, err := os.Lstat(path)
	if err != nil {
		return false
	}
	if fi.IsDir() || m.movedHere(path) {
		return true
	}
	// A link of the person's own: its account is made and says why the
	// folder stayed where it is.
	target, err := os.Stat(path)
	return fi.Mode()&fs.ModeSymlink != 0 && err == nil && target.IsDir()
}

// leftAlone says why a folder outside the data directory is not moved into
// it, or "" when it may be.
func (m *migration) leftAlone(path string) string {
	if _, inside := m.layout.Rel(path); inside {
		return ""
	}
	for _, multi := range m.multiAuth() {
		if within(multi, path) {
			return "it is part of " + multi + ", which Socrates leaves alone"
		}
	}
	tmp := m.opts.tempDir
	if tmp == "" {
		tmp = os.TempDir()
	}
	if within(tmp, m.layout.Root) && !within(tmp, path) {
		return "the data directory is in the temporary directory, and nothing from outside it moves into one there"
	}
	return ""
}

// multiAuth are the folders of the multi-account tools the person used
// before accounts, which are left alone (REBUILD.md §8.3).
func (m *migration) multiAuth() []string {
	var out []string
	if m.home != "" {
		out = append(out, filepath.Join(m.home, ".codex-multi"))
	}
	config := m.env["XDG_CONFIG_HOME"]
	if config == "" && m.home != "" {
		config = filepath.Join(m.home, ".config")
	}
	if config != "" {
		out = append(out, filepath.Join(config, "opencode-multi-auth"))
	}
	return out
}

// within reports whether path is dir or inside it, as written and with every
// link resolved.
func within(dir, path string) bool {
	inside := func(dir, path string) bool {
		rel, err := filepath.Rel(dir, path)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	dir, path = filepath.Clean(dir), filepath.Clean(path)
	if inside(dir, path) {
		return true
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	if inside(realDir, path) {
		return true
	}
	// The path itself may not be there (yet); its parent is enough.
	for p := path; ; p = filepath.Dir(p) {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			rel, _ := filepath.Rel(p, path)
			return inside(realDir, filepath.Join(real, rel))
		}
		if p == filepath.Dir(p) {
			return false
		}
	}
}

// xdg is OpenCode's folder under an XDG base directory.
func (m *migration) xdg(name, fallback string) string {
	if base := m.env[name]; base != "" {
		return filepath.Join(base, "opencode")
	}
	if m.home == "" {
		return ""
	}
	return filepath.Join(m.home, fallback, "opencode")
}

// profileAgents is, per profile id, the agents its sessions ran.
func (m *migration) profileAgents() (map[string][]string, error) {
	rows, err := m.opts.Store.ListSessions()
	if err != nil {
		return nil, err
	}
	seen := map[string]map[string]bool{}
	for i := range rows {
		var o struct {
			Profile string `json:"profile"`
		}
		if len(rows[i].Options) == 0 || json.Unmarshal(rows[i].Options, &o) != nil || o.Profile == "" {
			continue
		}
		switch rows[i].Harness {
		case Claude, Codex, OpenCode:
		default:
			continue
		}
		if seen[o.Profile] == nil {
			seen[o.Profile] = map[string]bool{}
		}
		seen[o.Profile][rows[i].Harness] = true
	}
	out := map[string][]string{}
	for id, agents := range seen {
		for _, name := range []string{Claude, Codex, OpenCode} {
			if agents[name] {
				out[id] = append(out[id], name)
			}
		}
	}
	return out, nil
}

// movedHere reports whether path is a link the migration left: one that
// points into the data directory. It reads what the link says rather than
// following it, so a link whose folder is not there yet counts as well.
func (m *migration) movedHere(path string) bool {
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		return false
	}
	target, err := os.Readlink(path)
	if err != nil {
		return false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	if _, inside := m.layout.Rel(target); inside {
		return true
	}
	root, err := filepath.EvalSymlinks(m.layout.Root)
	if err != nil {
		return false
	}
	_, inside := paths.New(root).Rel(target)
	return inside
}

// dest is where a source goes.
func (m *migration) dest(src source, id string) string {
	if src.agent == OpenCode {
		return filepath.Join(Folder(m.layout.Root, OpenCode, id), "opencode")
	}
	return Folder(m.layout.Root, src.agent, id)
}

// movedBefore is the id of the account an earlier run of the migration moved
// src's folder into when the database no longer has that account: the old
// path links to an account's folder of this data directory, the folder is
// there, and no account has its id. That is a migration whose database was
// rolled back since. The first deploy of this build is run by the updater of
// the build before, and when the new build migrates and then fails its health
// check, that updater puts back the previous binary and database and nothing
// else (scripts/update.sh, rollback): the moves stay done, the build before
// runs on the folders through the links, and the next start of a new build
// finds the moves made and no account of them. The account is made again
// under the id its folder carries, so it is that folder, with the login the
// link has kept current - not a new, empty one beside it. "" is a source no
// run has moved, or one whose account the database still has, which
// CreateAccount finds by its origin.
func (m *migration) movedBefore(src source) string {
	if !m.movedHere(src.from) {
		return ""
	}
	target, err := os.Readlink(src.from)
	if err != nil {
		return ""
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(src.from), target)
	}
	target = filepath.Clean(target)
	id := filepath.Base(target)
	if src.agent == OpenCode {
		id = filepath.Base(filepath.Dir(target))
	}
	if !ValidID(id) {
		return ""
	}
	fi, err := os.Stat(target)
	if err != nil || !fi.IsDir() {
		return ""
	}
	if dst, err := os.Stat(m.dest(src, id)); err != nil || !os.SameFile(fi, dst) {
		return ""
	}
	if _, err := m.opts.Store.GetAccount(id); !errors.Is(err, store.ErrNotFound) {
		return ""
	}
	return id
}

// move moves one source into its account and links the old path to it.
func (m *migration) move(src source) error {
	id := m.movedBefore(src)
	if id == "" {
		id = NewID()
	} else {
		m.opts.Logf("accounts: %s was moved into %s by a migration whose database was rolled back since; its account is made on that folder again",
			src.from, m.dest(src, id))
	}
	a := &store.Account{ID: id, Agent: src.agent, Label: src.label, Kind: Subscription,
		State: NeedsSignIn, Origin: src.origin}
	if err := m.opts.Store.CreateAccount(a); err != nil {
		return err
	}
	if err := m.step("account:" + src.origin); err != nil {
		return err
	}
	dst := m.dest(src, a.ID)
	if err := m.relocate(src.from, dst, src.relative); err != nil {
		detail := ""
		switch {
		case errors.Is(err, syscall.EXDEV):
			// A rename across file systems would be a copy of the login.
			m.opts.Logf("accounts: %s is on another file system than %s; it stays where it is, and %s has to be signed in",
				src.from, m.layout.Root, src.label)
			detail = "The agent's own folder is on another file system, so it was not moved. Sign in here."
		case errors.Is(err, errForeignLink):
			m.opts.Logf("accounts: %s is %v; it stays where it is, and %s has to be signed in", src.from, err, src.label)
			detail = "The agent's own folder is a link of yours to another place, so it was not moved. Sign in here, or move it by hand."
		default:
			return err
		}
		// Said once, by the run that made the account: a sign-in since then
		// is not undone by a later run.
		if a.ID == id {
			if err := m.opts.Store.SetAccountState(a.ID, NeedsSignIn, detail); err != nil {
				return err
			}
		}
		return Prepare(m.layout.Root, a)
	}
	if err := m.step("moved:" + src.origin); err != nil {
		return err
	}
	if src.claudeJSON != "" {
		if err := m.relocateClaudeJSON(src.claudeJSON, filepath.Join(dst, ".claude.json")); err != nil {
			return err
		}
		if err := m.step("json:" + src.origin); err != nil {
			return err
		}
	}
	switch src.agent {
	case Claude:
		if err := m.share(dst, SharedFolder(m.layout.Root, Claude), claude.SharedDirs); err != nil {
			return err
		}
	case Codex:
		if err := m.share(dst, SharedFolder(m.layout.Root, Codex), codex.SharedDirs); err != nil {
			return err
		}
	case OpenCode:
		if err := m.openCodeStore(dst); err != nil {
			return err
		}
		if err := m.share(dst, SharedFolder(m.layout.Root, OpenCode), openCodeShared); err != nil {
			return err
		}
		if src.origin == OriginOpenCode {
			if err := m.relocate(m.xdg("XDG_CONFIG_HOME", ".config"), filepath.Join(opencode.NewLayout(m.layout.Root).ConfigHome(), "opencode"), false); err != nil {
				if !errors.Is(err, syscall.EXDEV) && !errors.Is(err, errForeignLink) {
					return err
				}
				m.opts.Logf("accounts: OpenCode's configuration stays where it is (%v)", err)
			}
		}
	}
	if err := m.step("shared:" + src.origin); err != nil {
		return err
	}
	return Prepare(m.layout.Root, a)
}

// openCodeShared are the parts of an OpenCode data folder every account
// shares (the layout's).
var openCodeShared = []string{"snapshot", "storage", "tool-output", "plans", "worktree", "repos"}

// relocate moves the folder from to dst and leaves a link at from that
// points at dst (swap). Each part is skipped when it is done: from already
// a link here, dst already there.
func (m *migration) relocate(from, dst string, relative bool) error {
	if from == "" {
		return nil
	}
	target := dst
	if relative {
		rel, err := filepath.Rel(filepath.Dir(from), dst)
		if err != nil {
			return err
		}
		target = rel
	}
	fi, err := os.Lstat(from)
	switch {
	case err == nil && fi.Mode()&fs.ModeSymlink != 0:
		if m.movedHere(from) {
			return nil
		}
		link, _ := os.Readlink(from)
		return fmt.Errorf("%w (%s)", errForeignLink, link)
	case err == nil:
		if err := clearPlaceholder(from, dst); err != nil {
			return err
		}
		return swap(from, dst, target)
	case errors.Is(err, fs.ErrNotExist):
		// Moved by a run cut short before its link, where the file system
		// could not swap - or nothing to move, and nothing moved.
		if di, derr := os.Lstat(dst); derr != nil || di.Mode()&fs.ModeSymlink != 0 {
			return nil
		}
		return os.Symlink(target, from)
	default:
		return err
	}
}

// clearPlaceholder removes the link a swap cut short left at dst before its
// exchange, and refuses a real dst: then both are there, and neither is the
// migration's to throw away.
func clearPlaceholder(from, dst string) error {
	fi, err := os.Lstat(dst)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case fi.Mode()&fs.ModeSymlink == 0:
		return fmt.Errorf("both %s and %s exist", from, dst)
	}
	return os.Remove(dst)
}

// swap moves the file or folder from to dst - a rename, never a copy - and
// leaves at from a link whose text is target, in one step: a link is made
// at dst first (pointing nowhere useful until then) and the two names are
// exchanged, so at every moment from is either the folder or the link to
// it. A Claude Code that writes by path meanwhile never finds from missing,
// and never makes a second folder - with a second login in it - there.
// Where the file system cannot exchange, it is a rename and then the link.
func swap(from, dst, target string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if err := os.Symlink(target, dst); err != nil {
		return err
	}
	err := exchange(from, dst)
	if err == nil {
		return nil
	}
	if rerr := os.Remove(dst); rerr != nil {
		return rerr
	}
	if !errors.Is(err, errNoExchange) {
		return err
	}
	if err := os.Rename(from, dst); err != nil {
		var linkErr *os.LinkError
		if errors.As(err, &linkErr) && errors.Is(linkErr.Err, syscall.EXDEV) {
			return syscall.EXDEV
		}
		return err
	}
	return os.Symlink(target, from)
}

// relocateClaudeJSON moves ~/.claude.json into the account's folder, where
// Claude Code reads its global config once CLAUDE_CONFIG_DIR is set, and
// links it back (swap). A .claude.json the folder already had is one
// nothing read while CLAUDE_CONFIG_DIR was unset; it is kept beside, under a
// name nothing reads either.
func (m *migration) relocateClaudeJSON(from, dst string) error {
	fi, err := os.Lstat(from)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if di, err := os.Lstat(dst); err == nil && di.Mode()&fs.ModeSymlink == 0 {
			return os.Symlink(dst, from)
		}
		return nil
	case err != nil:
		return err
	case fi.Mode()&fs.ModeSymlink != 0:
		return nil
	}
	if di, err := os.Lstat(dst); err == nil {
		if di.Mode()&fs.ModeSymlink != 0 {
			// The link of a swap cut short.
			if err := os.Remove(dst); err != nil {
				return err
			}
		} else if err := os.Rename(dst, free(dst+".pre-rebuild")); err != nil {
			return err
		}
	}
	return swap(from, dst, dst)
}

// free is name, or name.2, name.3 ... whichever is not taken.
func free(name string) string {
	aside := name
	for i := 2; ; i++ {
		if _, err := os.Lstat(aside); errors.Is(err, fs.ErrNotExist) {
			return aside
		}
		aside = fmt.Sprintf("%s.%d", name, i)
	}
}

// share puts what an account has of the folders every account of its agent
// shares into the shared ones. The account's own folder becomes the link
// the adapter makes (swap, so a CLI still writing into it by path writes
// into the shared one from then on), what was in it is set aside as
// <name>.pre-rebuild and moved into the shared folder. A file both have
// stays the account's, in that folder aside, which nothing reads: nothing is
// overwritten and nothing is deleted. A run cut short is finished by the
// next, which merges what is still aside.
func (m *migration) share(accountDir, shared string, names []string) error {
	for _, name := range names {
		own, into := filepath.Join(accountDir, name), filepath.Join(shared, name)
		rel, err := filepath.Rel(accountDir, into)
		if err != nil {
			return err
		}
		asides, err := asidesOf(accountDir, name)
		if err != nil {
			return err
		}
		for _, aside := range asides {
			// The link of a swap cut short before its exchange.
			if fi, err := os.Lstat(aside); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
				if err := os.Remove(aside); err != nil {
					return err
				}
			}
		}
		if fi, err := os.Lstat(own); err == nil && fi.IsDir() {
			if err := os.MkdirAll(into, 0o700); err != nil {
				return err
			}
			if err := swap(own, free(own+".pre-rebuild"), rel); err != nil {
				return err
			}
		}
		if asides, err = asidesOf(accountDir, name); err != nil {
			return err
		}
		for _, aside := range asides {
			if fi, err := os.Lstat(aside); err != nil || !fi.IsDir() {
				continue
			}
			if err := os.MkdirAll(into, 0o700); err != nil {
				return err
			}
			if err := mergeTree(aside, into); err != nil {
				return err
			}
			if empty, _ := isEmpty(aside); empty {
				if err := os.Remove(aside); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// asidesOf are the folders share set aside of name in dir.
func asidesOf(dir, name string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	base := name + ".pre-rebuild"
	for _, e := range entries {
		if e.Name() == base || strings.HasPrefix(e.Name(), base+".") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out, nil
}

// mergeTree moves every entry of src that dst does not have into dst,
// descending into folders both have.
func mergeTree(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		from, to := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
		fi, err := os.Lstat(to)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if err := os.Rename(from, to); err != nil {
				return err
			}
		case err != nil:
			return err
		case fi.IsDir() && e.IsDir():
			if err := mergeTree(from, to); err != nil {
				return err
			}
			if empty, _ := isEmpty(from); empty {
				_ = os.Remove(from)
			}
		}
	}
	return nil
}

func isEmpty(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	return len(entries) == 0, err
}

// openCodeStore makes the Default OpenCode account's database the one every
// account shares (OPENCODE_DB), when there is none yet; a second account's
// stays in its folder, read by nothing. The three files are renamed
// together: a process that still has them open keeps the same inodes.
//
// Where the database was, a link to it is left: HOME's ~/.local/share/opencode
// is this folder now, and an opencode started by hand - in a Shell, with no
// OPENCODE_DB - opens the database there, which would otherwise be a new,
// empty one beside the conversations. SQLite follows the link and keeps its
// -wal and -shm beside the file it names, so both are one database.
func (m *migration) openCodeStore(accountData string) error {
	layout := opencode.NewLayout(m.layout.Root)
	old := filepath.Join(accountData, "opencode.db")
	if _, err := os.Lstat(layout.DB()); errors.Is(err, fs.ErrNotExist) {
		if _, err := os.Lstat(old); err != nil {
			return nil
		}
		if err := os.MkdirAll(layout.Root, 0o700); err != nil {
			return err
		}
		// The companions first: a crash in between leaves the main file
		// where the next run looks for it.
		for _, suffix := range []string{"-wal", "-shm", ""} {
			from := old + suffix
			if _, err := os.Lstat(from); err != nil {
				continue
			}
			if err := os.Rename(from, layout.DB()+suffix); err != nil {
				return err
			}
		}
	} else if err != nil {
		return err
	}
	// A run cut short after the rename makes the link here too.
	if _, err := os.Lstat(old); !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	target, err := filepath.Rel(accountData, layout.DB())
	if err != nil {
		return err
	}
	return os.Symlink(target, old)
}

// --------------------------------------------------------------- the backup

// backup writes <data>.pre-rebuild.tar, the way back to the build before: the
// data directory as that build had it and every folder about to move, under
// the names they have now. The database in it is the copy the store made
// before it upgraded the file (store.PreUpgradeCopy) - the upgraded one is a
// schema the build before refuses - and of the rest it leaves out only what
// the backup engine does and that build does not read either (sockets, caches,
// downloaded binaries), and the working directories (workingDirs), which the
// migration does not touch: the mirror's s3.json and the agent terminals'
// folders with their journals, which the clean cut deletes, stay in. It is
// written beside itself and renamed into place whole, so a tar that is there
// is complete; one that is there already is not written again.
func (m *migration) backup(sources []source) error {
	dest := m.layout.PreRebuildBackup()
	if _, err := os.Stat(dest); err == nil {
		return nil
	}
	tmp := dest + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(f)
	fail := func(err error) error {
		tw.Close()
		f.Close()
		os.Remove(tmp)
		return err
	}
	old := store.PreUpgradeCopy(m.layout.DB())
	if _, err := os.Stat(old); err == nil {
		if err := addFile(tw, old, "data/"+paths.DB); err != nil {
			return fail(err)
		}
	} else {
		// Upgraded without one - from before the terminal rewrite, which
		// left a copy of its own - the database goes in as it is.
		dbCopy := filepath.Join(filepath.Dir(dest), filepath.Base(dest)+".db.tmp")
		_ = os.Remove(dbCopy)
		if err := m.opts.Store.CopyTo(dbCopy); err != nil {
			return fail(fmt.Errorf("copy the database: %w", err))
		}
		defer os.Remove(dbCopy)
		if err := addFile(tw, dbCopy, "data/"+paths.DB); err != nil {
			return fail(err)
		}
	}
	working, err := m.workingDirs()
	if err != nil {
		return fail(err)
	}
	skip := func(rel string) bool {
		switch {
		case working[rel]:
			return true
		case rel == paths.DB || strings.HasPrefix(rel, paths.DB+"-") || strings.HasPrefix(rel, paths.DB+".pre-"):
			return true
		case rel == paths.LegacyS3Config || rel == paths.Sessions || strings.HasPrefix(rel, paths.Sessions+"/"):
			return false
		}
		return paths.Excluded(rel)
	}
	if err := addTree(tw, m.layout.Root, "data", skip); err != nil {
		return fail(err)
	}
	seen := map[string]bool{}
	for _, src := range sources {
		for _, path := range []string{src.from, src.claudeJSON} {
			if path == "" || seen[path] || src.relative {
				continue
			}
			seen[path] = true
			if m.movedHere(path) {
				continue
			}
			if err := addTree(tw, path, m.tarName(path), nil); err != nil {
				return fail(err)
			}
		}
		if src.origin == OriginOpenCode {
			if config := m.xdg("XDG_CONFIG_HOME", ".config"); config != "" && !m.movedHere(config) && !seen[config] {
				seen[config] = true
				if err := addTree(tw, config, m.tarName(config), nil); err != nil {
					return fail(err)
				}
			}
		}
	}
	if err := tw.Close(); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return err
	}
	m.opts.Logf("accounts: everything the migration moves is backed up in %s; it holds the logins unencrypted, so delete it once the accounts work", dest)
	return nil
}

// workingDirs are the working directories inside the data directory, by
// their path relative to it: workspaces/, where a dynamic session's folder is
// made by default, the workspace root and the presets the settings name, and
// every session's own folder - those of them that are not one of Socrates'
// own parts (paths.Own) or the data directory itself. The backup engine
// leaves out the same (server.BackupExclusions). The tar does too: the
// migration does not touch them, so neither build needs them back from it,
// and they are what makes a data directory big - a copy of them would need
// that much free disk at the first start, and hold its listener, and with it
// the updater's health check, for as long as writing it takes.
func (m *migration) workingDirs() (map[string]bool, error) {
	candidates := []string{m.layout.Path(paths.Workspaces)}
	var settings struct {
		Workspace struct {
			Root    string `json:"root"`
			Presets []struct {
				Path string `json:"path"`
			} `json:"presets"`
		} `json:"workspace"`
	}
	if err := m.opts.Store.GetJSON("settings", &settings); err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	candidates = append(candidates, settings.Workspace.Root)
	for _, p := range settings.Workspace.Presets {
		candidates = append(candidates, p.Path)
	}
	rows, err := m.opts.Store.ListSessions()
	if err != nil {
		return nil, err
	}
	for i := range rows {
		candidates = append(candidates, rows[i].Workdir)
	}
	out := map[string]bool{}
	for _, dir := range candidates {
		if strings.TrimSpace(dir) == "" || !filepath.IsAbs(dir) {
			continue
		}
		rel, inside := m.layout.Rel(filepath.Clean(dir))
		if !inside || rel == "" || rel == "." || paths.Own(rel) {
			continue
		}
		out[rel] = true
	}
	return out, nil
}

// tarName is where a folder outside the data directory goes in the tar:
// under home/ when it is in HOME, under abs/ otherwise.
func (m *migration) tarName(path string) string {
	if m.home != "" {
		if rel, err := filepath.Rel(m.home, path); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return "home/" + filepath.ToSlash(rel)
		}
	}
	return "abs/" + strings.TrimPrefix(filepath.ToSlash(path), "/")
}

// addTree writes root and everything under it as name, links as links,
// skipping what skip says (by the path relative to root) and what a tar
// cannot hold (sockets, pipes).
func addTree(tw *tar.Writer, root, name string, skip func(rel string) bool) error {
	info, err := os.Lstat(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return addEntry(tw, root, name, info)
	}
	var entries []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel != "." && skip != nil && skip(filepath.ToSlash(rel)) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		entries = append(entries, rel)
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(entries)
	for _, rel := range entries {
		path := filepath.Join(root, rel)
		info, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		entry := name
		if rel != "." {
			entry = name + "/" + filepath.ToSlash(rel)
		}
		if err := addEntry(tw, path, entry, info); err != nil {
			return err
		}
	}
	return nil
}

func addEntry(tw *tar.Writer, path, name string, info fs.FileInfo) error {
	mode := info.Mode()
	link := ""
	switch {
	case mode&fs.ModeSymlink != 0:
		target, err := os.Readlink(path)
		if err != nil {
			return err
		}
		link = target
	case mode.IsRegular(), mode.IsDir():
	default:
		return nil
	}
	hdr, err := tar.FileInfoHeader(info, link)
	if err != nil {
		return err
	}
	hdr.Name = name
	if mode.IsDir() {
		hdr.Name += "/"
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if !mode.IsRegular() {
		return nil
	}
	return copyInto(tw, path, hdr.Size)
}

func addFile(tw *tar.Writer, path, name string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	return addEntry(tw, path, name, info)
}

// copyInto writes size bytes of path into the tar: a file that grows while
// it is read is cut at the size the header says, and one that shrinks is
// padded, so the archive stays readable.
func copyInto(tw *tar.Writer, path string, size int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	n, err := io.Copy(tw, io.LimitReader(f, size))
	if err != nil {
		return err
	}
	if n < size {
		_, err = io.CopyN(tw, zeroReader{}, size-n)
	}
	return err
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
