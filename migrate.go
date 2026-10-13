package main

// Moving an installation to another machine is moving two things: the settings
// document the dashboard writes, and the password that opens it. Everything
// else in the data directory is either rebuilt on the next start (the agent
// catalogue), belongs to a tmux server that does not exist there (the
// sessions), or is the work itself (the workspaces).
//
// `socrates export` writes those two as one JSON document and `socrates
// import` writes them into another data directory, which is how a container
// started as
//
//	docker run -p 8080:8080 -v socrates-data:/data socrates
//
// comes up already configured: the volume is seeded once, by an import run in
// a throwaway container against the same volume, and nothing has to be passed
// on the command line afterwards. scripts/migrate-to-docker.sh is that in one
// step.
//
// The one thing an import has to think about is paths. A settings document
// names absolute directories - the workspace root, the preset quick picks -
// and the data directory they were written against is almost never the one
// they are being read into: ~/.socrates on this machine, /data in a container.
// Every path that sat inside the exported data directory is moved to the same
// place inside the new one; a path outside it is left exactly as written,
// because it names somewhere on the host that only a mount can provide, and
// silently rewriting it would turn a missing mount into a working directory
// nobody meant.

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/catalog"
	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/server"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// migrationVersion is the shape of the document below. An import refuses a
// number it does not know rather than guessing at the fields.
const migrationVersion = 1

// migration is what `socrates export` writes and `socrates import` reads.
type migration struct {
	Version    int       `json:"version"`
	ExportedAt time.Time `json:"exported_at"`
	// SourceVersion is only ever printed. It is here so that a file found
	// later says which build wrote it.
	SourceVersion string `json:"source_version"`
	// DataDir is the directory the settings below were written against, and
	// it is what makes the paths in them portable: see the note at the top.
	DataDir  string          `json:"data_dir"`
	Settings config.Settings `json:"settings"`
	// PasswordHash is the stored PBKDF2 hash, not the password. It carries its
	// own salt, so it verifies the same password on any machine - which is the
	// point: an import that left it out would land on /setup.
	PasswordHash string `json:"password_hash,omitempty"`
}

// exportSettings writes the settings document and the password hash of a data
// directory to stdout, or to a file.
func exportSettings(args []string) {
	fs := flag.NewFlagSet("socrates export", flag.ExitOnError)
	dataDir := fs.String("data", config.DataDir(), "the data directory to read")
	out := fs.String("o", "-", "write to this file instead of stdout")
	noPassword := fs.Bool("no-password", false, "leave the password hash out, so the other side asks for a new one")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "Usage:\n  socrates export [flags] > socrates-settings.json\n\n"+
			"Writes this installation's settings and password as one JSON document,\n"+
			"which `socrates import` reads. The file holds your OpenRouter key, your\n"+
			"Google voice key and your Cloudflare tunnel token: keep it as you would\n"+
			"keep those.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "socrates export: %q is not a flag.\n", fs.Arg(0))
		os.Exit(2)
	}

	doc, err := readMigration(*dataDir, !*noPassword)
	if err != nil {
		fmt.Fprintf(os.Stderr, "socrates export: %v\n", err)
		os.Exit(1)
	}
	blob, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "socrates export: %v\n", err)
		os.Exit(1)
	}
	blob = append(blob, '\n')

	if *out == "-" {
		if _, err := os.Stdout.Write(blob); err != nil {
			fmt.Fprintf(os.Stderr, "socrates export: %v\n", err)
			os.Exit(1)
		}
		return
	}
	// 0600, because of what is in it.
	if err := os.WriteFile(*out, blob, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "socrates export: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "wrote %s (%d bytes) from %s\n", *out, len(blob), doc.DataDir)
}

// readMigration opens a data directory and takes the two documents out of it.
func readMigration(dataDir string, withPassword bool) (migration, error) {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		return migration{}, errors.New("-data is empty")
	}
	dbPath := filepath.Join(dataDir, "socrates.db")
	if _, err := os.Stat(dbPath); err != nil {
		return migration{}, fmt.Errorf("no database at %s: is %s really a Socrates data directory?", dbPath, dataDir)
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return migration{}, fmt.Errorf("open %s: %w", dbPath, err)
	}
	defer st.Close()

	// A data directory that has never been through a first run has no settings
	// document, and its defaults are not worth carrying anywhere.
	settings := config.Default()
	if err := st.GetJSON(server.SettingsKey, &settings); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return migration{}, fmt.Errorf("%s has no settings yet: start Socrates there once and go through /setup", dataDir)
		}
		return migration{}, fmt.Errorf("read the settings: %w", err)
	}
	settings.Normalize()

	doc := migration{
		Version:       migrationVersion,
		ExportedAt:    time.Now().UTC(),
		SourceVersion: version,
		DataDir:       filepath.Clean(dataDir),
		Settings:      settings,
	}
	if withPassword {
		hash, err := st.GetKV(server.PasswordKey)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return migration{}, fmt.Errorf("read the password: %w", err)
		}
		doc.PasswordHash = hash
	}
	return doc, nil
}

// importSettings writes an exported document into a data directory, creating
// it when it is not there. It is deliberately safe to run against a data
// directory that is already in use: only the settings and the password are
// touched, and the sessions, the workspaces and the agent catalogue are left
// exactly as they are.
func importSettings(args []string) {
	fs := flag.NewFlagSet("socrates import", flag.ExitOnError)
	dataDir := fs.String("data", config.DataDir(), "the data directory to write")
	in := fs.String("i", "-", "read from this file instead of stdin")
	keepPaths := fs.Bool("keep-paths", false, "write the paths exactly as exported, without moving them into this data directory")
	keepPassword := fs.Bool("keep-password", false, "leave the password of this installation alone")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "Usage:\n  socrates import [flags] < socrates-settings.json\n\n"+
			"Writes the settings and the password of an exported installation into this\n"+
			"data directory. Sessions, workspaces and the agent catalogue are untouched.\n\n"+
			"Paths that sat inside the exported data directory are moved into this one,\n"+
			"so a workspace root of ~/.socrates/workspaces becomes /data/workspaces in a\n"+
			"container. Paths outside it are left alone: they name somewhere on the host\n"+
			"and only a mount can provide them.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "socrates import: %q is not a flag.\n", fs.Arg(0))
		os.Exit(2)
	}

	blob, err := readAll(*in)
	if err != nil {
		fmt.Fprintf(os.Stderr, "socrates import: %v\n", err)
		os.Exit(1)
	}
	var doc migration
	if err := json.Unmarshal(blob, &doc); err != nil {
		fmt.Fprintf(os.Stderr, "socrates import: this is not an export document: %v\n", err)
		os.Exit(1)
	}
	if doc.Version != migrationVersion {
		fmt.Fprintf(os.Stderr, "socrates import: the document is version %d and this build writes %d.\n", doc.Version, migrationVersion)
		os.Exit(1)
	}

	target := filepath.Clean(strings.TrimSpace(*dataDir))
	if target == "" || target == "." {
		fmt.Fprintln(os.Stderr, "socrates import: -data is empty")
		os.Exit(1)
	}
	settings := doc.Settings
	var moves []string
	if !*keepPaths {
		moves = rehomeSettings(&settings, doc.DataDir, target)
	}
	settings.Normalize()
	if err := settings.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "socrates import: the settings are not usable here: %v\n", err)
		os.Exit(1)
	}

	if err := os.MkdirAll(target, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "socrates import: could not create %s: %v\n", target, err)
		os.Exit(1)
	}
	st, err := store.Open(filepath.Join(target, "socrates.db"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "socrates import: open the database: %v\n", err)
		os.Exit(1)
	}
	defer st.Close()
	if err := st.SetJSON(server.SettingsKey, settings); err != nil {
		fmt.Fprintf(os.Stderr, "socrates import: write the settings: %v\n", err)
		os.Exit(1)
	}
	wrotePassword := false
	if doc.PasswordHash != "" && !*keepPassword {
		if err := st.SetKV(server.PasswordKey, doc.PasswordHash); err != nil {
			fmt.Fprintf(os.Stderr, "socrates import: write the password: %v\n", err)
			os.Exit(1)
		}
		wrotePassword = true
	}
	// The bucket is the store of record, and a start makes the local copy
	// equal to it. What was just written is marked as changed and not yet
	// uploaded, so a data directory that already belongs to a bucket sends
	// the imported settings up on its next start instead of having the
	// bucket's copy restored over them. (A fresh data directory connected to
	// a bucket that already holds state takes the bucket's: that is the
	// restore, and the import is the thing that loses there.)
	imported := []string{store.KVObject(server.SettingsKey)}
	if wrotePassword {
		imported = append(imported, store.KVObject(server.PasswordKey))
	}
	st.MarkPending(imported...)
	// The agent catalogue is a fact about the machine it was discovered on -
	// which binaries are installed, where, and at which version - so a stale
	// one from the other side would name paths that do not exist here. It is
	// rebuilt on the next start; dropping it makes sure that happens.
	if err := st.DeleteKV(catalog.CacheKey); err != nil {
		fmt.Fprintf(os.Stderr, "socrates import: clear the agent catalogue: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "imported into %s (exported %s from %s)\n",
		target, doc.ExportedAt.Local().Format("2006-01-02 15:04"), doc.DataDir)
	for _, m := range moves {
		fmt.Fprintf(os.Stderr, "  moved  %s\n", m)
	}
	if wrotePassword {
		fmt.Fprintln(os.Stderr, "  the password of the exported installation now opens this one")
	} else if doc.PasswordHash == "" {
		fmt.Fprintln(os.Stderr, "  no password in the document: this installation will ask for one at /setup")
	}
	for _, warning := range offMachinePaths(settings, target) {
		fmt.Fprintf(os.Stderr, "  note   %s\n", warning)
	}
}

// rehomeSettings moves every path in the document that sat inside the exported
// data directory to the same place inside the new one, and returns a line per
// move for the person watching. A source and a target that are the same
// directory move nothing.
func rehomeSettings(s *config.Settings, from, to string) []string {
	from = filepath.Clean(strings.TrimSpace(from))
	to = filepath.Clean(strings.TrimSpace(to))
	if from == "" || from == "." || from == to {
		return nil
	}
	var moves []string
	if next, ok := rehomePath(s.Workspace.Root, from, to); ok {
		moves = append(moves, fmt.Sprintf("workspace root %s -> %s", s.Workspace.Root, next))
		s.Workspace.Root = next
	}
	for i, p := range s.Workspace.Presets {
		if next, ok := rehomePath(p.Path, from, to); ok {
			moves = append(moves, fmt.Sprintf("preset %q %s -> %s", p.Label, p.Path, next))
			s.Workspace.Presets[i].Path = next
		}
	}
	return moves
}

// rehomePath reports the path p would have if the directory from were the
// directory to, and whether p was inside from at all.
func rehomePath(p, from, to string) (string, bool) {
	p = filepath.Clean(strings.TrimSpace(p))
	if p == "" || p == "." {
		return "", false
	}
	if p == from {
		return to, true
	}
	if !strings.HasPrefix(p, from+string(os.PathSeparator)) {
		return "", false
	}
	return filepath.Join(to, strings.TrimPrefix(p, from+string(os.PathSeparator))), true
}

// offMachinePaths names the directories the imported settings still point at
// that are not here. A preset is refused at session creation when its
// directory is gone rather than quietly created, so saying it now - while the
// person is still at the terminal that could mount it - is the difference
// between a note and a puzzle.
func offMachinePaths(s config.Settings, dataDir string) []string {
	var out []string
	for _, p := range s.Workspace.Presets {
		if info, err := os.Stat(p.Path); err != nil || !info.IsDir() {
			out = append(out, fmt.Sprintf("the preset %q points at %s, which is not here: mount it, or drop the preset in /admin", p.Label, p.Path))
		}
	}
	if root := s.Workspace.Root; !strings.HasPrefix(filepath.Clean(root)+string(os.PathSeparator), filepath.Clean(dataDir)+string(os.PathSeparator)) {
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			out = append(out, fmt.Sprintf("the workspace root %s is outside %s and not here: it will be created on the first dynamic session, and it will not survive this container", root, dataDir))
		}
	}
	return out
}

// readAll reads a whole file, or stdin for "-".
func readAll(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}
