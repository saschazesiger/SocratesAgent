//go:build linux

package accounts

import (
	"archive/tar"
	"bytes"
	"database/sql"
	"encoding/base64"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// The rehearsal of REBUILD.md §8.1-8.2 on a synthetic copy of today's
// installation: a HOME with the three agents' own folders and logins, and a
// data directory of today's main (schema 9) whose profiles are partly there
// and partly lost. Never a real HOME.

// The logins of the fixture, each a file that must exist exactly once after
// the migration.
var fixtureLogins = map[string]string{
	".claude/.credentials.json":       `{"claudeAiOauth":{"accessToken":"at-default","refreshToken":"rt-default","subscriptionType":"max"}}`,
	".claude.json":                    `{"numStartups":5,"oauthAccount":{"emailAddress":"me@example.com"}}`,
	".codex/auth.json":                `{"auth_mode":"chatgpt","tokens":{"id_token":"` + fakeIDToken("codex@example.com") + `","refresh_token":"rt-codex"}}`,
	".local/share/opencode/auth.json": `{"openai":{"type":"oauth","refresh":"rt-opencode","access":"at-opencode","expires":1}}`,
}

// The profile with its folders still there.
var profileLogins = map[string]string{
	"profiles/p-bbbbbbbbbbbb/claude/.credentials.json": `{"claudeAiOauth":{"accessToken":"at-work","refreshToken":"rt-work"}}`,
	"profiles/p-bbbbbbbbbbbb/codex/auth.json":          `{"OPENAI_API_KEY":"sk-work"}`,
}

func fakeIDToken(email string) string {
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(`{"email":"`+email+`"}`)) + ".sig"
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fixture is a HOME and a data directory as today's installation has them.
func fixture(t *testing.T) (data, home string) {
	t.Helper()
	root := t.TempDir()
	data, home = filepath.Join(root, "data"), filepath.Join(root, "home")
	for rel, content := range fixtureLogins {
		write(t, filepath.Join(home, rel), content)
	}
	write(t, filepath.Join(home, ".claude/projects/-srv-repo/s-default.jsonl"), `{"type":"user"}`+"\n")
	write(t, filepath.Join(home, ".claude/settings.json"), `{}`)
	write(t, filepath.Join(home, ".codex/sessions/2026/10/01/rollout-2026-10-01-default.jsonl"), `{"type":"session_meta"}`+"\n")
	write(t, filepath.Join(home, ".codex/config.toml"), "model = \"gpt\"\n")
	write(t, filepath.Join(home, ".local/share/opencode/opencode.db"), "sqlite")
	write(t, filepath.Join(home, ".local/share/opencode/opencode.db-wal"), "wal")
	write(t, filepath.Join(home, ".local/share/opencode/storage/session_diff/ses_1.json"), "[]")
	write(t, filepath.Join(home, ".local/share/opencode/snapshot/proj/abc/HEAD"), "ref")
	write(t, filepath.Join(home, ".config/opencode/opencode.jsonc"), "{}")
	for rel, content := range profileLogins {
		write(t, filepath.Join(data, rel), content)
	}
	write(t, filepath.Join(data, "profiles/p-bbbbbbbbbbbb/claude/projects/-srv-other/s-work.jsonl"), `{"type":"user"}`+"\n")
	write(t, filepath.Join(data, "profiles/p-bbbbbbbbbbbb/codex/sessions/2026/10/02/rollout-2026-10-02-work.jsonl"), `{}`+"\n")

	schema, err := os.ReadFile(filepath.Join("..", "store", "testdata", "schema-v9.sql"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(data, "socrates.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		string(schema),
		`INSERT INTO kv(key, value) VALUES('profiles', '[{"id":"p-aaaaaaaaaaaa","name":"Team Sascha","created_at":1},
		 {"id":"p-bbbbbbbbbbbb","name":"Work","created_at":2},{"id":"p-cccccccccccc","name":"Campus","created_at":3}]')`,
		`INSERT INTO sessions(id, harness, workdir, workdir_mode, options, created_at, updated_at)
		 VALUES('s1', 'claude', '/srv', 'custom', '{"profile":"p-aaaaaaaaaaaa"}', 1, 1)`,
		`INSERT INTO sessions(id, harness, workdir, workdir_mode, options, created_at, updated_at)
		 VALUES('s2', 'codex', '/srv', 'custom', '{"profile":"p-cccccccccccc"}', 1, 1)`,
		`PRAGMA user_version = 9`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return data, home
}

// migrateOnce opens the store as a start of the new build does and runs the
// migration, stopped after stop when it is set.
func migrateOnce(t *testing.T, data, home, stop string) error {
	t.Helper()
	st, err := store.Open(filepath.Join(data, "socrates.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	return Migrate(MigrateOptions{DataDir: data, Store: st, Env: []string{"HOME=" + home}, Logf: t.Logf, stopAfter: stop})
}

// outcome is what a migration left, in words that two runs can be compared
// by.
func outcome(t *testing.T, data string) []string {
	t.Helper()
	st, err := store.Open(filepath.Join(data, "socrates.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	list, err := st.ListAccounts()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for i := range list {
		a := list[i]
		login := Detect(data, &a)
		out = append(out, strings.Join([]string{a.Agent, a.Label, a.Origin, boolWord(login.SignedIn), login.Email, a.StateDetail}, "|"))
	}
	sort.Strings(out)
	return out
}

func boolWord(b bool) string {
	if b {
		return "signed in"
	}
	return "not signed in"
}

func accountByOrigin(t *testing.T, data, origin string) store.Account {
	t.Helper()
	st, err := store.Open(filepath.Join(data, "socrates.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	list, _ := st.ListAccounts()
	for _, a := range list {
		if a.Origin == origin {
			return a
		}
	}
	t.Fatalf("no account from %s", origin)
	return store.Account{}
}

func sameFile(t *testing.T, a, b string) {
	t.Helper()
	fa, err := os.Stat(a)
	if err != nil {
		t.Fatal(err)
	}
	fb, err := os.Stat(b)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(fa, fb) {
		t.Fatalf("%s and %s are two files", a, b)
	}
}

func isLink(t *testing.T, path string) string {
	t.Helper()
	target, err := os.Readlink(path)
	if err != nil {
		t.Fatalf("%s is not a link: %v", path, err)
	}
	return target
}

// copies counts the regular files under the roots whose content is content,
// one per inode.
func copies(t *testing.T, content string, roots ...string) int {
	t.Helper()
	inodes := map[string]bool{}
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !d.Type().IsRegular() {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil || string(raw) != content {
				return nil
			}
			fi, _ := os.Stat(path)
			inodes[inodeOf(fi)] = true
			return nil
		})
	}
	return len(inodes)
}

func TestTheMigrationMovesEveryLoginOnceAndLinksTheOldPaths(t *testing.T) {
	data, home := fixture(t)
	if err := migrateOnce(t, data, home, ""); err != nil {
		t.Fatal(err)
	}
	got := outcome(t, data)
	want := []string{
		"claude|Default|~/.claude|signed in|me@example.com|",
		"claude|Team Sascha|profile:p-aaaaaaaaaaaa:claude|not signed in||" + lostDetail,
		"claude|Work|profile:p-bbbbbbbbbbbb:claude|signed in||",
		"codex|Campus|profile:p-cccccccccccc:codex|not signed in||" + lostDetail,
		"codex|Default|~/.codex|signed in|codex@example.com|",
		"codex|Work|profile:p-bbbbbbbbbbbb:codex|signed in||",
		"opencode|Default|~/.local/share/opencode|signed in||",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("accounts:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// Claude Code: the folder and its global config moved, the old paths are
	// links, and the login is one file seen through both.
	claudeAcct := accountByOrigin(t, data, OriginClaude)
	dir := Folder(data, Claude, claudeAcct.ID)
	if target := isLink(t, filepath.Join(home, ".claude")); target != dir {
		t.Fatalf("~/.claude -> %s, want %s", target, dir)
	}
	isLink(t, filepath.Join(home, ".claude.json"))
	sameFile(t, filepath.Join(home, ".claude/.credentials.json"), filepath.Join(dir, ".credentials.json"))
	sameFile(t, filepath.Join(home, ".claude.json"), filepath.Join(dir, ".claude.json"))
	refreshed := `{"claudeAiOauth":{"accessToken":"at-new","refreshToken":"rt-new"}}`
	write(t, filepath.Join(home, ".claude/.credentials.json"), refreshed)
	if raw, _ := os.ReadFile(filepath.Join(dir, ".credentials.json")); string(raw) != refreshed {
		t.Fatalf("a refresh through the old path is not seen in the account: %s", raw)
	}
	if target := isLink(t, filepath.Join(dir, "projects")); target != "../../agents/claude/projects" {
		t.Fatalf("projects -> %s", target)
	}
	for _, rel := range []string{"agents/claude/projects/-srv-repo/s-default.jsonl", "agents/claude/projects/-srv-other/s-work.jsonl"} {
		if _, err := os.Stat(filepath.Join(data, rel)); err != nil {
			t.Fatalf("a transcript is not in the shared folder: %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".claude/projects/-srv-repo/s-default.jsonl")); err != nil {
		t.Fatalf("a transcript is not reachable through the old path: %v", err)
	}
	work := accountByOrigin(t, data, "profile:p-bbbbbbbbbbbb:claude")
	if target := isLink(t, filepath.Join(data, "profiles/p-bbbbbbbbbbbb/claude")); target != "../../accounts/"+work.ID {
		t.Fatalf("the profile's folder -> %s", target)
	}

	// Codex.
	codexAcct := accountByOrigin(t, data, OriginCodex)
	codexDir := Folder(data, Codex, codexAcct.ID)
	if target := isLink(t, filepath.Join(home, ".codex")); target != codexDir {
		t.Fatalf("~/.codex -> %s", target)
	}
	sameFile(t, filepath.Join(home, ".codex/auth.json"), filepath.Join(codexDir, "auth.json"))
	if _, err := os.Stat(filepath.Join(data, "agents/codex/sessions/2026/10/01/rollout-2026-10-01-default.jsonl")); err != nil {
		t.Fatalf("the rollout is not shared: %v", err)
	}
	if _, err := os.Stat(filepath.Join(data, "agents/codex/sessions/2026/10/02/rollout-2026-10-02-work.jsonl")); err != nil {
		t.Fatalf("the profile's rollout is not shared: %v", err)
	}

	// OpenCode: the data folder is the Default account's, its database the
	// shared one, its configuration the layout's.
	ocAcct := accountByOrigin(t, data, OriginOpenCode)
	ocData := filepath.Join(Folder(data, OpenCode, ocAcct.ID), "opencode")
	if target := isLink(t, filepath.Join(home, ".local/share/opencode")); target != ocData {
		t.Fatalf("~/.local/share/opencode -> %s", target)
	}
	sameFile(t, filepath.Join(home, ".local/share/opencode/auth.json"), filepath.Join(ocData, "auth.json"))
	for _, rel := range []string{"opencode.db", "opencode.db-wal", "storage/session_diff/ses_1.json", "snapshot/proj/abc/HEAD", "config/opencode/opencode.jsonc"} {
		if _, err := os.Stat(filepath.Join(data, "agents/opencode", rel)); err != nil {
			t.Fatalf("OpenCode's %s is not in the layout: %v", rel, err)
		}
	}
	isLink(t, filepath.Join(home, ".config/opencode"))

	// Not one login exists twice (the backup tar aside, which nothing can
	// run on).
	for _, content := range []string{refreshed, fixtureLogins[".claude.json"], fixtureLogins[".codex/auth.json"],
		fixtureLogins[".local/share/opencode/auth.json"], profileLogins["profiles/p-bbbbbbbbbbbb/codex/auth.json"]} {
		if n := copies(t, content, home, data); n != 1 {
			t.Fatalf("a login exists %d times: %s", n, content)
		}
	}

	// The backup holds what was moved, as it was.
	entries := tarEntries(t, data+".pre-rebuild.tar")
	for _, name := range []string{"data/socrates.db", "home/.claude/.credentials.json", "home/.claude.json",
		"home/.codex/auth.json", "home/.local/share/opencode/auth.json", "home/.config/opencode/opencode.jsonc",
		"data/profiles/p-bbbbbbbbbbbb/claude/.credentials.json"} {
		if _, ok := entries[name]; !ok {
			t.Fatalf("the backup has no %s", name)
		}
	}
	if entries["home/.claude/.credentials.json"] != fixtureLogins[".claude/.credentials.json"] {
		t.Fatalf("the backup's login: %q", entries["home/.claude/.credentials.json"])
	}
	if fi, _ := os.Stat(data + ".pre-rebuild.tar"); fi.Mode().Perm() != 0o600 {
		t.Fatalf("the backup is %v", fi.Mode())
	}

	// Once: a second start does nothing, and one without the marker finds
	// everything done.
	if err := migrateOnce(t, data, home, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(data, "accounts.migrated")); err != nil {
		t.Fatal(err)
	}
	if err := migrateOnce(t, data, home, ""); err != nil {
		t.Fatal(err)
	}
	if again := outcome(t, data); strings.Join(again, "\n") != strings.Join(got, "\n") {
		t.Fatalf("a second run changed the accounts:\n%s", strings.Join(again, "\n"))
	}
}

// A migration cut short after any step is finished by the next start, with
// the same result as one that ran through.
func TestAnInterruptedMigrationIsFinishedByTheNextStart(t *testing.T) {
	clean, cleanHome := fixture(t)
	if err := migrateOnce(t, clean, cleanHome, ""); err != nil {
		t.Fatal(err)
	}
	want := outcome(t, clean)
	for _, step := range []string{
		"backup", "account:" + OriginClaude, "moved:" + OriginClaude, "json:" + OriginClaude, "shared:" + OriginClaude,
		"account:" + OriginCodex, "moved:" + OriginCodex, "moved:" + OriginOpenCode, "shared:" + OriginOpenCode,
		"account:profile:p-bbbbbbbbbbbb:claude", "moved:profile:p-bbbbbbbbbbbb:codex", "lost:profile:p-aaaaaaaaaaaa:claude",
	} {
		t.Run(strings.ReplaceAll(step, "/", "_"), func(t *testing.T) {
			data, home := fixture(t)
			if err := migrateOnce(t, data, home, step); !errors.Is(err, errStopped) {
				t.Fatalf("the run stopped after %s with %v", step, err)
			}
			if err := migrateOnce(t, data, home, ""); err != nil {
				t.Fatalf("the next start: %v", err)
			}
			if got := outcome(t, data); strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("after a stop at %s:\n%s\nwant:\n%s", step, strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
			claudeAcct := accountByOrigin(t, data, OriginClaude)
			sameFile(t, filepath.Join(home, ".claude/.credentials.json"), filepath.Join(Folder(data, Claude, claudeAcct.ID), ".credentials.json"))
			sameFile(t, filepath.Join(home, ".claude.json"), filepath.Join(Folder(data, Claude, claudeAcct.ID), ".claude.json"))
			if n := copies(t, fixtureLogins[".claude/.credentials.json"], home, data); n != 1 {
				t.Fatalf("the login exists %d times", n)
			}
			if _, err := os.Stat(filepath.Join(data, "agents/claude/projects/-srv-repo/s-default.jsonl")); err != nil {
				t.Fatalf("a transcript was lost: %v", err)
			}
		})
	}
}

// A HOME with nothing to move and no profiles makes no accounts and no
// backup: a new installation starts empty.
func TestANewInstallationHasNothingToMigrate(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	home := t.TempDir()
	if err := migrateOnce(t, data, home, ""); err != nil {
		t.Fatal(err)
	}
	if got := outcome(t, data); len(got) != 0 {
		t.Fatalf("accounts %v", got)
	}
	if _, err := os.Stat(data + ".pre-rebuild.tar"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a backup of nothing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(data, "accounts.migrated")); err != nil {
		t.Fatalf("no marker: %v", err)
	}
}

func tarEntries(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string]string{}
	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		if _, err := io.Copy(&b, tr); err != nil {
			t.Fatal(err)
		}
		out[strings.TrimSuffix(hdr.Name, "/")] = b.String()
	}
}

func inodeOf(fi fs.FileInfo) string {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fi.Name()
	}
	return strconv.FormatUint(uint64(st.Dev), 10) + ":" + strconv.FormatUint(st.Ino, 10)
}
