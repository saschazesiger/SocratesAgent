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
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent/claude"
	"github.com/saschazesiger/SocratesAgent/internal/agent/codex"
	"github.com/saschazesiger/SocratesAgent/internal/agent/opencode"
	"github.com/saschazesiger/SocratesAgent/internal/paths"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// The migration of the build before accounts (REBUILD.md §8.1-8.2), once, on
// the first start of this one: the agents' own folders in HOME become the
// Default account of each agent, the profiles become accounts - with their
// logins where their folders still exist, marked "sign in again" where they
// were lost - and every account's conversations go into the folders they
// share.
//
// A login is never copied. OAuth refresh tokens rotate: of two copies, the
// first to refresh kills the other, and whatever runs on it is logged out -
// which is what happened to the person every few minutes. So a folder is
// moved, never copied (a rename, which keeps every file's inode), and the
// old path becomes a link to the new place: a Claude Code or Codex still
// running from it, or started later by hand, keeps using the one file. A
// move that would cross file systems, where a rename is a copy, is not made:
// that agent's folder stays where it is and its account has no login.
//
// Before anything moves, all of it - the data directory and the folders
// about to move - is written to <data>.pre-rebuild.tar. Every step is
// idempotent and decides from what is on the disk, so a migration cut short
// anywhere (a crash, a power cut) is finished by the next start without
// losing or doubling anything: the accounts are found again by their origin,
// a folder already moved is not moved again, and a missing link is made.

// MigrateOptions is one migration.
type MigrateOptions struct {
	DataDir string
	Store   *store.Store
	// Env is the service's environment, which says where the agents' own
	// folders are: HOME, CLAUDE_CONFIG_DIR, CODEX_HOME, XDG_DATA_HOME,
	// XDG_CONFIG_HOME.
	Env  []string
	Logf func(format string, args ...any)
	// stopAfter ends the migration after the step of that name as a crash
	// would, for the tests.
	stopAfter string
}

// errStopped is a migration ended by stopAfter.
var errStopped = errors.New("the migration was stopped")

// Origins of the accounts the migration makes.
const (
	OriginClaude   = "~/.claude"
	OriginCodex    = "~/.codex"
	OriginOpenCode = "~/.local/share/opencode"
)

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

// Migrate runs the migration if this data directory has not had it.
func Migrate(opts MigrateOptions) error {
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
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
	marker := m.layout.Path(paths.AccountsMigrated)
	if _, err := os.Stat(marker); err == nil {
		return nil
	}
	sources, lost, err := m.plan()
	if err != nil {
		return err
	}
	if len(sources) > 0 || len(lost) > 0 {
		if err := m.backup(sources); err != nil {
			return fmt.Errorf("the backup before the migration: %w", err)
		}
		if err := m.step("backup"); err != nil {
			return err
		}
	}
	for _, src := range sources {
		if err := m.move(src); err != nil {
			return fmt.Errorf("move %s: %w", src.from, err)
		}
	}
	for _, a := range lost {
		if err := m.opts.Store.CreateAccount(a); err != nil {
			return err
		}
		if err := Prepare(m.layout.Root, a); err != nil {
			return err
		}
		if err := m.step("lost:" + a.Origin); err != nil {
			return err
		}
	}
	if err := os.WriteFile(marker, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600); err != nil {
		return err
	}
	m.opts.Logf("accounts: the agents' folders are in %s now; the old ones are links to them", m.layout.Root)
	return nil
}

func (m *migration) step(name string) error {
	if m.opts.stopAfter == name {
		return errStopped
	}
	return nil
}

// plan is what there is to move, and the profiles whose folders are gone.
func (m *migration) plan() ([]source, []*store.Account, error) {
	var sources []source
	add := func(src source) {
		if src.from == "" {
			return
		}
		if fi, err := os.Lstat(src.from); err == nil && (fi.IsDir() || m.movedHere(src.from)) {
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
		for _, one := range []struct{ agent, sub string }{
			{Claude, "claude"}, {Codex, "codex"}, {OpenCode, filepath.Join("xdg-data", "opencode")},
		} {
			from := filepath.Join(dir, one.sub)
			if fi, err := os.Lstat(from); err != nil || (!fi.IsDir() && !m.movedHere(from)) {
				continue
			}
			found++
			sources = append(sources, source{origin: "profile:" + p.ID + ":" + one.agent, agent: one.agent,
				label: label, from: from, relative: true})
		}
		if found > 0 {
			continue
		}
		// The folders went with the move from Docker (HANDOFF.md §5): the
		// profile becomes an account of each agent its sessions ran, or of
		// Claude Code, to be signed in again.
		agents := used[p.ID]
		if len(agents) == 0 {
			agents = []string{Claude}
		}
		for _, name := range agents {
			lost = append(lost, &store.Account{ID: NewID(), Agent: name, Label: label, Kind: Subscription,
				State: NeedsSignIn, StateDetail: lostDetail, Origin: "profile:" + p.ID + ":" + name})
		}
	}
	return sources, lost, nil
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
// points into the data directory.
func (m *migration) movedHere(path string) bool {
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		return false
	}
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
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

// move moves one source into its account and links the old path to it.
func (m *migration) move(src source) error {
	a := &store.Account{ID: NewID(), Agent: src.agent, Label: src.label, Kind: Subscription,
		State: NeedsSignIn, Origin: src.origin}
	if err := m.opts.Store.CreateAccount(a); err != nil {
		return err
	}
	if err := m.step("account:" + src.origin); err != nil {
		return err
	}
	dst := m.dest(src, a.ID)
	if err := m.relocate(src.from, dst, src.relative); err != nil {
		if errors.Is(err, syscall.EXDEV) {
			// A rename across file systems would be a copy of the login.
			m.opts.Logf("accounts: %s is on another file system than %s; it stays where it is, and %s has to be signed in",
				src.from, m.layout.Root, src.label)
			_ = m.opts.Store.SetAccountState(a.ID, NeedsSignIn,
				"The agent's own folder is on another file system, so it was not moved. Sign in here.")
			return Prepare(m.layout.Root, a)
		}
		return err
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
				if !errors.Is(err, syscall.EXDEV) {
					return err
				}
				m.opts.Logf("accounts: OpenCode's configuration is on another file system; it stays where it is")
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

// relocate moves the folder from to dst - a rename, never a copy - and
// leaves a link at from that points at dst. Each part is skipped when it is
// done: from already a link here, dst already there.
func (m *migration) relocate(from, dst string, relative bool) error {
	if from == "" {
		return nil
	}
	fi, err := os.Lstat(from)
	switch {
	case err == nil && fi.Mode()&fs.ModeSymlink != 0:
		if m.movedHere(from) {
			return nil
		}
		// A link of the person's own to somewhere else: what it points at
		// is the folder, and it is not this migration's to move.
		return fmt.Errorf("%s is a link to somewhere else; move it by hand", from)
	case err == nil:
		if _, derr := os.Lstat(dst); derr == nil {
			return fmt.Errorf("both %s and %s exist", from, dst)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		if err := os.Rename(from, dst); err != nil {
			var linkErr *os.LinkError
			if errors.As(err, &linkErr) && errors.Is(linkErr.Err, syscall.EXDEV) {
				return syscall.EXDEV
			}
			return err
		}
	case errors.Is(err, fs.ErrNotExist):
		if _, derr := os.Lstat(dst); derr != nil {
			// Nothing to move, and nothing moved.
			return nil
		}
	default:
		return err
	}
	target := dst
	if relative {
		rel, err := filepath.Rel(filepath.Dir(from), dst)
		if err != nil {
			return err
		}
		target = rel
	}
	return os.Symlink(target, from)
}

// relocateClaudeJSON moves ~/.claude.json into the account's folder, where
// Claude Code reads its global config once CLAUDE_CONFIG_DIR is set, and
// links it back. A .claude.json the folder already had is one nothing read
// while CLAUDE_CONFIG_DIR was unset; it is kept beside, under a name nothing
// reads either.
func (m *migration) relocateClaudeJSON(from, dst string) error {
	fi, err := os.Lstat(from)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if _, err := os.Lstat(dst); err == nil {
			return os.Symlink(dst, from)
		}
		return nil
	case err != nil:
		return err
	case fi.Mode()&fs.ModeSymlink != 0:
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if _, err := os.Lstat(dst); err == nil {
		aside := dst + ".pre-rebuild"
		for i := 2; ; i++ {
			if _, err := os.Lstat(aside); errors.Is(err, fs.ErrNotExist) {
				break
			}
			aside = fmt.Sprintf("%s.pre-rebuild.%d", dst, i)
		}
		if err := os.Rename(dst, aside); err != nil {
			return err
		}
	}
	if err := os.Rename(from, dst); err != nil {
		return err
	}
	return os.Symlink(dst, from)
}

// share moves what an account has of the folders every account of its agent
// shares into the shared ones, so that the adapter's own linking finds
// nothing in its way. A file both have stays the account's, in a folder of
// the same name with .pre-rebuild added, which nothing reads: nothing is
// overwritten and nothing is deleted.
func (m *migration) share(accountDir, shared string, names []string) error {
	for _, name := range names {
		own := filepath.Join(accountDir, name)
		fi, err := os.Lstat(own)
		if err != nil || !fi.IsDir() {
			continue
		}
		into := filepath.Join(shared, name)
		if err := os.MkdirAll(into, 0o700); err != nil {
			return err
		}
		if err := mergeTree(own, into); err != nil {
			return err
		}
		if empty, _ := isEmpty(own); empty {
			if err := os.Remove(own); err != nil {
				return err
			}
			continue
		}
		aside := own + ".pre-rebuild"
		if _, err := os.Lstat(aside); err == nil {
			continue
		}
		if err := os.Rename(own, aside); err != nil {
			return err
		}
	}
	return nil
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
func (m *migration) openCodeStore(accountData string) error {
	layout := opencode.NewLayout(m.layout.Root)
	if _, err := os.Lstat(layout.DB()); err == nil {
		return nil
	}
	if _, err := os.Lstat(filepath.Join(accountData, "opencode.db")); err != nil {
		return nil
	}
	if err := os.MkdirAll(layout.Root, 0o700); err != nil {
		return err
	}
	// The companions first: a crash in between leaves the main file where
	// the next run looks for it.
	for _, suffix := range []string{"-wal", "-shm", ""} {
		from := filepath.Join(accountData, "opencode.db"+suffix)
		if _, err := os.Lstat(from); err != nil {
			continue
		}
		if err := os.Rename(from, layout.DB()+suffix); err != nil {
			return err
		}
	}
	return nil
}

// --------------------------------------------------------------- the backup

// backup writes <data>.pre-rebuild.tar: the data directory (the database as a
// consistent copy, without what the backup engine leaves out and without
// sockets) and every folder about to move, under the names it has now. It
// is written beside itself and renamed into place whole, so a tar that is
// there is complete; one that is there already is not written again.
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
	dbCopy := filepath.Join(filepath.Dir(dest), filepath.Base(dest)+".db.tmp")
	_ = os.Remove(dbCopy)
	if err := m.opts.Store.CopyTo(dbCopy); err != nil {
		return fail(fmt.Errorf("copy the database: %w", err))
	}
	defer os.Remove(dbCopy)
	if err := addFile(tw, dbCopy, "data/"+paths.DB); err != nil {
		return fail(err)
	}
	skip := func(rel string) bool {
		if rel == paths.DB || strings.HasPrefix(rel, paths.DB+"-") {
			return true
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
	m.opts.Logf("accounts: everything the migration moves is backed up in %s", dest)
	return nil
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
