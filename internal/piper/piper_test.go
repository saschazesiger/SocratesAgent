package piper

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

/* --------------------------------------------------------------- fixtures */

// isolate makes the lookup in resolve deterministic. A developer machine with
// a piper of its own on the PATH, or with SOCRATES_PIPER_DIR pointing
// somewhere, would otherwise quietly change what every one of these tests is
// testing. It returns the empty directory the PATH now consists of, for the
// tests that want to put a piper in it.
func isolate(t *testing.T) string {
	t.Helper()
	empty := t.TempDir()
	t.Setenv("PATH", empty)
	t.Setenv(EnvDir, "")
	return empty
}

// requireScript skips where the stand in cannot run: it is a POSIX shell
// script, and it deliberately uses only shell builtins so that it still works
// with the emptied PATH above.
func requireScript(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake piper is a POSIX shell script")
	}
}

// requireDownloadable also skips where Socrates installs nothing at all, which
// is every platform with no published build - macOS above all, where the
// published archives are broken and refused on purpose.
func requireDownloadable(t *testing.T) {
	t.Helper()
	requireScript(t)
	if _, err := assetFor(runtime.GOOS, runtime.GOARCH); err != nil {
		t.Skipf("nothing is downloaded on this platform: %v", err)
	}
}

// fakePiper is a stand in for the real binary. It answers --help with
// something that looks like piper's usage, writes down the arguments, the
// library path and the text it was handed, and returns a WAV.
func fakePiper(record string) string {
	return "#!/bin/sh\n" +
		"if [ \"$1\" = \"--help\" ]; then\n" +
		"  echo 'usage: piper -m FILE --model FILE onnx model'\n" +
		"  exit 0\n" +
		"fi\n" +
		": > \"" + record + "/args\"\n" +
		"for arg in \"$@\"; do printf '%s\\n' \"$arg\" >> \"" + record + "/args\"; done\n" +
		"printf '%s' \"$LD_LIBRARY_PATH\" > \"" + record + "/libpath\"\n" +
		"while IFS= read -r line || [ -n \"$line\" ]; do printf '%s\\n' \"$line\"; done > \"" + record + "/stdin\"\n" +
		"printf 'RIFFxxxxWAVEfmt xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx'\n"
}

// recorded reads back one of the files the stand in wrote. The trailing
// newline is the shell's, not the caller's.
func recorded(t *testing.T, record, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(record, name))
	if err != nil {
		t.Fatalf("the fake piper wrote no %s: %v", name, err)
	}
	return strings.TrimSuffix(string(raw), "\n")
}

// arguments is what the stand in was called with.
func arguments(t *testing.T, record string) []string {
	t.Helper()
	return strings.Split(recorded(t, record, "args"), "\n")
}

// after returns the value of a flag, or "" when the flag was not passed.
func after(args []string, flag string) string {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func has(args []string, flag string) bool {
	for _, arg := range args {
		if arg == flag {
			return true
		}
	}
	return false
}

// voiceConfig is what a .onnx.json really is: a few kilobytes of JSON. The
// size matters here, because a config that arrives under the floor counts as a
// failed download.
func voiceConfig() []byte {
	var b strings.Builder
	b.WriteString(`{"audio":{"sample_rate":22050},"espeak":{"voice":"en-us"},"phoneme_id_map":{`)
	for i := 0; i < 200; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"p%d":[%d]`, i, i)
	}
	b.WriteString("}}")
	return []byte(b.String())
}

// offline points an engine at a server that fails the test if it is asked for
// anything. Every engine in these tests gets one, so that a fixture which is
// subtly wrong shows up as a failed expectation here rather than as a real
// 60 MB download from Hugging Face.
func offline(t *testing.T, e *Engine) *Engine {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("nothing should be downloaded, but %s was requested", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	e.ReleaseURL, e.VoicesURL = server.URL+"/release", server.URL+"/voices"
	return e
}

// installTree puts a complete installation on disk without downloading one,
// for the tests that are about rendering rather than about installing.
func installTree(t *testing.T, root, script string) {
	t.Helper()
	tree := binaryDir(root)
	if err := os.MkdirAll(espeakData(tree), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binaryPath(root), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(espeakData(tree), "phontab"), []byte("phonemes"), 0o644); err != nil {
		t.Fatal(err)
	}
	installVoices(t, voiceDir(root))
}

func installVoices(t *testing.T, voices string) {
	t.Helper()
	if err := os.MkdirAll(voices, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, v := range allVoices {
		if err := os.WriteFile(modelPath(voices, v.Name), bytes.Repeat([]byte("m"), minModelBytes+1), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(configPath(voices, v.Name), voiceConfig(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// archive builds what the release page serves: a gzipped tar whose single top
// level directory carries the executable, the shared libraries with their
// symlink chain, and the espeak data.
func archive(t *testing.T, script string) []byte {
	t.Helper()
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	tarball := tar.NewWriter(gz)

	write := func(header *tar.Header, body []byte) {
		t.Helper()
		header.Size = int64(len(body))
		if err := tarball.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tarball.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	write(&tar.Header{Name: "piper/", Mode: 0o755, Typeflag: tar.TypeDir}, nil)
	write(&tar.Header{Name: "piper/piper", Mode: 0o755, Typeflag: tar.TypeReg}, []byte(script))
	write(&tar.Header{Name: "piper/libonnxruntime.so.1.14.1", Mode: 0o644, Typeflag: tar.TypeReg}, []byte("shared library"))
	write(&tar.Header{
		Name: "piper/libonnxruntime.so", Mode: 0o777, Typeflag: tar.TypeSymlink,
		Linkname: "libonnxruntime.so.1.14.1",
	}, nil)
	write(&tar.Header{Name: "piper/espeak-ng-data/", Mode: 0o755, Typeflag: tar.TypeDir}, nil)
	write(&tar.Header{Name: "piper/espeak-ng-data/phontab", Mode: 0o644, Typeflag: tar.TypeReg}, []byte("phonemes"))

	if err := tarball.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

// stand is the release page and the voice repository at once, and remembers
// every path that was asked for so a test can prove what was and was not
// downloaded.
type stand struct {
	*httptest.Server

	mu       sync.Mutex
	requests []string
	hook     func(http.ResponseWriter, *http.Request) bool

	release []byte
	model   []byte
	config  []byte
}

func newStand(t *testing.T, script string) *stand {
	t.Helper()
	s := &stand{
		release: archive(t, script),
		// The floor a model has to clear is a megabyte, so the stand in has
		// to serve something that size for the download to count as one.
		model:  bytes.Repeat([]byte("onnx"), 300*1024),
		config: voiceConfig(),
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func (s *stand) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, r.URL.Path)
	hook := s.hook
	s.mu.Unlock()
	if hook != nil && !hook(w, r) {
		return
	}
	switch {
	case strings.HasSuffix(r.URL.Path, ".onnx.json"):
		_, _ = w.Write(s.config)
	case strings.HasSuffix(r.URL.Path, ".onnx"):
		_, _ = w.Write(s.model)
	case strings.HasPrefix(r.URL.Path, "/release/"):
		_, _ = w.Write(s.release)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// hits counts the requests whose path contains match.
func (s *stand) hits(match string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, path := range s.requests {
		if strings.Contains(path, match) {
			count++
		}
	}
	return count
}

func (s *stand) forget() {
	s.mu.Lock()
	s.requests = nil
	s.mu.Unlock()
}

// engineOn points an engine at the stand in instead of GitHub and Hugging
// Face.
func engineOn(t *testing.T, s *stand) *Engine {
	t.Helper()
	e := New(filepath.Join(t.TempDir(), "voice"))
	e.ReleaseURL = s.URL + "/release"
	e.VoicesURL = s.URL + "/voices"
	return e
}

/* ---------------------------------------------------------------- install */

// Nobody is ever asked to install piper by hand, so the whole tree - the
// binary, the libraries it loads, the espeak data it phonemises with and both
// voices - has to arrive from one call and be usable afterwards.
func TestEnsureInstallsPiperAndBothVoices(t *testing.T) {
	requireDownloadable(t)
	isolate(t)
	record := t.TempDir()
	s := newStand(t, fakePiper(record))
	e := engineOn(t, s)

	if e.canSpeak() {
		t.Fatal("an empty directory cannot be ready")
	}
	if err := e.Ensure(context.Background()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if !e.canSpeak() {
		t.Fatal("the engine should be ready after a successful install")
	}

	info, err := os.Stat(binaryPath(e.Dir))
	if err != nil {
		t.Fatalf("no binary was installed: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Error("the installed binary is not executable")
	}
	if _, err := os.Stat(filepath.Join(espeakData(binaryDir(e.Dir)), "phontab")); err != nil {
		t.Errorf("the espeak data was not unpacked: %v", err)
	}
	// The libraries ship as a chain of symlinks and the loader asks for the
	// name the binary was linked against, so a tree with the links flattened
	// away or dropped would not start.
	link, err := os.Lstat(filepath.Join(binaryDir(e.Dir), "libonnxruntime.so"))
	if err != nil || link.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the symlinked library did not survive unpacking: %v", err)
	}
	for _, v := range allVoices {
		if !voiceInstalled(voiceDir(e.Dir), v.Name) {
			t.Errorf("%s was not installed", v.Name)
		}
	}

	// Nothing half written is left lying around next to the real thing.
	entries, err := os.ReadDir(e.Dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if len(names) != 2 {
		t.Errorf("the install directory holds %v", names)
	}
}

// The download is 150 MB. Repeating it because a second answer is being read
// out loud would be unforgivable on the phone connection this runs on.
func TestASecondEnsureDownloadsNothingAgain(t *testing.T) {
	requireDownloadable(t)
	isolate(t)
	s := newStand(t, fakePiper(t.TempDir()))
	e := engineOn(t, s)

	if err := e.Ensure(context.Background()); err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	s.forget()
	if err := e.Ensure(context.Background()); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if hits := s.hits("/"); hits != 0 {
		t.Errorf("the second call made %d requests", hits)
	}
}

// Several HTTP handlers can want the voice at the same moment - the first
// answer of a conversation is often read out loud twice over. They have to
// share one download rather than start one each.
func TestConcurrentEnsureDownloadsEverythingExactlyOnce(t *testing.T) {
	requireDownloadable(t)
	isolate(t)
	s := newStand(t, fakePiper(t.TempDir()))
	e := engineOn(t, s)

	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = e.Ensure(context.Background())
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
	}
	if hits := s.hits("/release/"); hits != 1 {
		t.Errorf("the release was downloaded %d times", hits)
	}
	for _, v := range allVoices {
		if hits := s.hits(v.Name + ".onnx"); hits != 2 {
			t.Errorf("%s was fetched %d times, want the model and its config once each", v.Name, hits)
		}
	}
}

// A download that stops halfway has to leave nothing, because something that
// looks installed and is not would fail on every answer from then on with no
// way back other than deleting the directory by hand.
func TestATruncatedArchiveLeavesNothingThatLooksInstalled(t *testing.T) {
	requireDownloadable(t)
	isolate(t)
	s := newStand(t, fakePiper(t.TempDir()))
	full := s.release
	s.release = full[:len(full)/3]
	e := engineOn(t, s)

	if err := e.Ensure(context.Background()); err == nil {
		t.Fatal("a truncated archive must be refused")
	}
	if e.canSpeak() {
		t.Error("the engine reports ready after a failed install")
	}
	if _, err := os.Stat(binaryPath(e.Dir)); err == nil {
		t.Error("a binary was left behind")
	}
	entries, _ := os.ReadDir(e.Dir)
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".") && entry.Name() != "voices" {
			t.Errorf("leftover: %s", entry.Name())
		}
	}

	// And the failure is not permanent: the next attempt finds a healthy
	// server and finishes the job.
	s.release = full
	if err := e.Ensure(context.Background()); err != nil {
		t.Fatalf("retry after a failure: %v", err)
	}
	if !e.canSpeak() {
		t.Error("the retry should have installed everything")
	}
}

// A model that arrives far too small is a connection that died or a portal
// that answered instead of Hugging Face. Keeping it would produce a piper that
// starts and then refuses every sentence.
func TestAVoiceThatArrivesTooSmallIsNotInstalled(t *testing.T) {
	requireDownloadable(t)
	isolate(t)
	s := newStand(t, fakePiper(t.TempDir()))
	s.model = []byte("not really 60 megabytes")
	e := engineOn(t, s)

	err := e.Ensure(context.Background())
	if err == nil || !strings.Contains(err.Error(), "voice") {
		t.Fatalf("err = %v, want one naming the voice", err)
	}
	if _, err := os.Stat(modelPath(voiceDir(e.Dir), VoiceEnglish)); err == nil {
		t.Error("the short model was installed anyway")
	}
}

// A captive portal answers every request with its login page and a 200. Saved
// as a voice config that page would be a working install that never speaks.
func TestAnErrorPageIsNotSavedAsAVoiceConfig(t *testing.T) {
	requireDownloadable(t)
	isolate(t)
	s := newStand(t, fakePiper(t.TempDir()))
	// A real portal page is a real page, so it clears the size floor and the
	// only thing left to catch it is that it is not JSON.
	s.config = []byte("<!doctype html><html><head><title>Sign in</title></head><body>" +
		strings.Repeat("<p>Please sign in to use this network.</p>", 20) + "</body></html>")
	e := engineOn(t, s)

	err := e.Ensure(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not JSON") {
		t.Fatalf("err = %v, want one saying the config is not JSON", err)
	}
	if _, err := os.Stat(configPath(voiceDir(e.Dir), VoiceEnglish)); err == nil {
		t.Error("the login page was installed as a voice config")
	}
}

// An archive is a file from the internet. An entry that climbs out of the
// directory it is unpacked into is a well travelled way to write over
// somebody's shell profile.
func TestArchiveEntriesCannotEscapeTheInstallDirectory(t *testing.T) {
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	tarball := tar.NewWriter(gz)
	body := []byte("owned")
	_ = tarball.WriteHeader(&tar.Header{
		Name: "../escaped", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
	})
	_, _ = tarball.Write(body)
	_ = tarball.Close()
	_ = gz.Close()

	parent := t.TempDir()
	staging := filepath.Join(parent, "staging")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := extractTarGz(bytes.NewReader(raw.Bytes()), staging); err == nil {
		t.Fatal("an entry pointing outside the directory must be refused")
	}
	if _, err := os.Stat(filepath.Join(parent, "escaped")); err == nil {
		t.Fatal("the archive wrote outside the install directory")
	}
}

// A symlink is the other way out, and the entry names give it away to
// nobody: none of the names below climbs anywhere. The link does the
// climbing, and the entry after it is written through the link, so a check
// that reads only the names in the archive watches the wrong thing.
func TestASymlinkEntryCannotCarryAWriteOutOfTheInstallDirectory(t *testing.T) {
	formats := []struct {
		name    string
		build   func(*testing.T, []slipEntry) []byte
		extract func(io.Reader, string) error
	}{
		{"tar", slipTarGz, extractTarGz},
		{"zip", slipZip, extractZip},
	}
	shapes := []struct {
		name string
		// entries is the whole archive, given the directory the unpacking is
		// meant to stay inside, because what gets out is a link and the entry
		// written through it rather than any single name.
		entries func(parent string) []slipEntry
	}{
		// The plain case: whatever the entry name is checked against, the
		// link on disk points at the absolute path the archive named.
		{"an absolute target", func(parent string) []slipEntry {
			return []slipEntry{
				{name: "piper/", dir: true},
				{name: "piper/evil", link: parent},
				{name: "piper/evil/pwned", body: "owned"},
			}
		}},
		// A target that climbs out with .. is the shape the name check was
		// already looking for, and it has to stay refused.
		{"a relative target that climbs out", func(string) []slipEntry {
			return []slipEntry{
				{name: "piper/", dir: true},
				{name: "piper/evil", link: "../.."},
				{name: "piper/evil/pwned", body: "owned"},
			}
		}},
		// Every target below stays inside the directory when it is read as
		// text: up points at the directory itself, and out points at up/..,
		// which reads as piper/sub. Walked rather than read, up is already
		// the directory, so out lands above it. Nothing but looking at what
		// is on disk catches this one.
		{"a chain of links that reads as if it stayed inside", func(string) []slipEntry {
			return []slipEntry{
				{name: "piper/", dir: true},
				{name: "piper/sub/", dir: true},
				{name: "piper/sub/up", link: "../.."},
				{name: "piper/sub/out", link: "up/.."},
				{name: "piper/sub/out/pwned", body: "owned"},
			}
		}},
	}
	for _, format := range formats {
		for _, shape := range shapes {
			t.Run(format.name+"/"+shape.name, func(t *testing.T) {
				parent := t.TempDir()
				staging := filepath.Join(parent, "staging")
				if err := os.MkdirAll(staging, 0o755); err != nil {
					t.Fatal(err)
				}
				raw := format.build(t, shape.entries(parent))

				if err := format.extract(bytes.NewReader(raw), staging); err == nil {
					t.Error("an archive that links out of the install directory must be refused")
				}
				// The error is not the proof: what the guard is for is the
				// state of the disk afterwards.
				nothingEscaped(t, parent, staging)
			})
		}
	}
}

// The shared libraries in the real release are a chain of relative links -
// libespeak-ng.so points at libespeak-ng.so.1 points at the file with the
// version in its name - and the loader asks for the name the binary was
// linked against, so a guard strict enough to refuse them installs a tree
// that cannot start.
func TestTheRelativeSymlinkChainTheReleaseShipsStillUnpacks(t *testing.T) {
	formats := []struct {
		name    string
		build   func(*testing.T, []slipEntry) []byte
		extract func(io.Reader, string) error
	}{
		{"tar", slipTarGz, extractTarGz},
		{"zip", slipZip, extractZip},
	}
	for _, format := range formats {
		t.Run(format.name, func(t *testing.T) {
			staging := t.TempDir()
			raw := format.build(t, []slipEntry{
				{name: "piper/", dir: true},
				{name: "piper/libespeak-ng.so.1.52.0.1", body: "shared library"},
				{name: "piper/libespeak-ng.so.1", link: "libespeak-ng.so.1.52.0.1"},
				{name: "piper/libespeak-ng.so", link: "libespeak-ng.so.1"},
				{name: "piper/espeak-ng-data/", dir: true},
				{name: "piper/espeak-ng-data/phontab", body: "phonemes"},
			})

			if err := format.extract(bytes.NewReader(raw), staging); err != nil {
				t.Fatalf("unpack: %v", err)
			}
			for name, want := range map[string]string{
				"libespeak-ng.so":   "libespeak-ng.so.1",
				"libespeak-ng.so.1": "libespeak-ng.so.1.52.0.1",
			} {
				target, err := os.Readlink(filepath.Join(staging, "piper", name))
				if err != nil {
					t.Fatalf("%s is not a symlink: %v", name, err)
				}
				if target != want {
					t.Errorf("%s points at %q, want %q", name, target, want)
				}
			}
			// Following the chain is the whole point of keeping it.
			body, err := os.ReadFile(filepath.Join(staging, "piper", "libespeak-ng.so"))
			if err != nil || string(body) != "shared library" {
				t.Errorf("the chain does not resolve to the library: %q, %v", body, err)
			}
		})
	}
}

// slipEntry is one entry of an archive built by hand, so that a test can
// write the entries a release never would.
type slipEntry struct {
	name string
	dir  bool
	link string // the target, when the entry is a symlink
	body string
}

func slipTarGz(t *testing.T, entries []slipEntry) []byte {
	t.Helper()
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	tarball := tar.NewWriter(gz)
	for _, entry := range entries {
		header := &tar.Header{Name: entry.name, Mode: 0o755, Typeflag: tar.TypeReg}
		switch {
		case entry.dir:
			header.Typeflag = tar.TypeDir
		case entry.link != "":
			header.Typeflag = tar.TypeSymlink
			header.Linkname = entry.link
		default:
			header.Size = int64(len(entry.body))
		}
		if err := tarball.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg {
			if _, err := tarball.Write([]byte(entry.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarball.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

// slipZip writes the same entries as a zip, where a symlink is a file whose
// contents are the target and whose mode says what it is.
func slipZip(t *testing.T, entries []slipEntry) []byte {
	t.Helper()
	var raw bytes.Buffer
	writer := zip.NewWriter(&raw)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		body := entry.body
		switch {
		case entry.dir:
			header.SetMode(fs.ModeDir | 0o755)
		case entry.link != "":
			header.SetMode(fs.ModeSymlink | 0o777)
			body = entry.link
		default:
			header.SetMode(0o644)
		}
		file, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

// nothingEscaped reports everything that was written outside the staging
// directory, which after a refused unpack has to be nothing at all. It looks
// at the disk rather than at the error, because an unpack that fails halfway
// still returns an error after whatever it already wrote.
func nothingEscaped(t *testing.T, parent, staging string) {
	t.Helper()
	err := filepath.WalkDir(parent, func(path string, entry fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case path == parent:
			return nil
		case path == staging:
			return fs.SkipDir
		}
		t.Errorf("the archive wrote %s, outside the staging directory", path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Windows is served a zip rather than a tarball. It is the one platform none
// of the developers runs, so the unpacking is at least held to the same shape
// as the tar it stands beside.
func TestAZipArchiveUnpacksToTheSameTree(t *testing.T) {
	var raw bytes.Buffer
	writer := zip.NewWriter(&raw)
	add := func(name string, mode os.FileMode, body string) {
		t.Helper()
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(mode)
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	add("piper/"+exeName(), 0o755, "binary")
	add("piper/onnxruntime.dll", 0o644, "library")
	add("piper/espeak-ng-data/phontab", 0o644, "phonemes")
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	staging := t.TempDir()
	if err := extractZip(bytes.NewReader(raw.Bytes()), staging); err != nil {
		t.Fatalf("unpack: %v", err)
	}
	tree, err := treeRoot(staging)
	if err != nil {
		t.Fatalf("the executable was not found: %v", err)
	}
	if tree != filepath.Join(staging, "piper") {
		t.Errorf("tree = %q", tree)
	}
	if _, err := os.Stat(filepath.Join(espeakData(tree), "phontab")); err != nil {
		t.Errorf("the espeak data was not unpacked: %v", err)
	}
	// The spool file the zip was read from is not part of the tree.
	entries, _ := os.ReadDir(staging)
	if len(entries) != 1 {
		t.Errorf("staging holds %d entries, want just the unpacked directory", len(entries))
	}
}

/* ------------------------------------------------------------------ speak */

// The language is the one voice setting Socrates has, and picking the model
// from it is the whole of what it does. Reading German with an English voice
// is the exact complaint this setting exists to answer.
func TestSpeakRendersGermanWithTheGermanVoiceAndScalesTheRate(t *testing.T) {
	requireScript(t)
	isolate(t)
	record := t.TempDir()
	e := offline(t, New(t.TempDir()))
	installTree(t, e.Dir, fakePiper(record))

	audio, contentType, err := e.Speak(context.Background(), "Guten Tag.", "de", 1.25)
	if err != nil {
		t.Fatalf("speak: %v", err)
	}
	if contentType != ContentType || !bytes.HasPrefix(audio, []byte("RIFF")) {
		t.Fatalf("got %d bytes of %q", len(audio), contentType)
	}

	args := arguments(t, record)
	if got, want := after(args, "--model"), modelPath(voiceDir(e.Dir), VoiceGerman); got != want {
		t.Errorf("--model = %q, want %q", got, want)
	}
	if got, want := after(args, "--config"), configPath(voiceDir(e.Dir), VoiceGerman); got != want {
		t.Errorf("--config = %q, want %q", got, want)
	}
	// A rate is how fast someone wants to be spoken to; piper is told how far
	// to stretch each phoneme, which is the same number upside down.
	if got := after(args, "--length_scale"); got != "0.8" {
		t.Errorf("--length_scale = %q, want 0.8", got)
	}
	if got := after(args, "--output_file"); got != "-" {
		t.Errorf("--output_file = %q, want -", got)
	}
	if got := recorded(t, record, "stdin"); got != "Guten Tag." {
		t.Errorf("piper was given %q", got)
	}
}

// English is the default for anything that is not German, including the empty
// language an older settings document still carries.
func TestSpeakUsesTheEnglishVoiceForEverythingElse(t *testing.T) {
	requireScript(t)
	isolate(t)
	record := t.TempDir()
	e := offline(t, New(t.TempDir()))
	installTree(t, e.Dir, fakePiper(record))

	if _, _, err := e.Speak(context.Background(), "All done.", "", 0); err != nil {
		t.Fatalf("speak: %v", err)
	}
	args := arguments(t, record)
	if got, want := after(args, "--model"), modelPath(voiceDir(e.Dir), VoiceEnglish); got != want {
		t.Errorf("--model = %q, want %q", got, want)
	}
	// A rate of zero means "normal", and normal is what piper does when it is
	// told nothing at all.
	if has(args, "--length_scale") {
		t.Errorf("a normal rate should pass no scale: %v", args)
	}
}

// Piper loads onnxruntime, espeak-ng and its phonemizer from beside itself,
// and finds neither its libraries nor its phoneme data without being told
// where the extracted tree is.
func TestSpeakPointsPiperAtItsOwnLibrariesAndData(t *testing.T) {
	requireScript(t)
	isolate(t)
	record := t.TempDir()
	e := offline(t, New(t.TempDir()))
	installTree(t, e.Dir, fakePiper(record))

	if _, _, err := e.Speak(context.Background(), "Hello.", "en", 1); err != nil {
		t.Fatalf("speak: %v", err)
	}
	tree := binaryDir(e.Dir)
	if got := after(arguments(t, record), "--espeak_data"); got != espeakData(tree) {
		t.Errorf("--espeak_data = %q, want %q", got, espeakData(tree))
	}
	if got := recorded(t, record, "libpath"); !strings.Contains(got, tree) {
		t.Errorf("LD_LIBRARY_PATH = %q, want the piper tree in it", got)
	}
	// The library path belongs to the child only; a process that later links
	// an onnxruntime of its own must not find this one first.
	if os.Getenv("LD_LIBRARY_PATH") == tree {
		t.Error("the library path leaked into this process")
	}
}

// When a render fails, the only person who can fix it reads the message. What
// piper said is the whole of the diagnosis, so it has to survive.
func TestSpeakSurfacesWhatPiperComplainedAbout(t *testing.T) {
	requireScript(t)
	isolate(t)
	e := offline(t, New(t.TempDir()))
	installTree(t, e.Dir, "#!/bin/sh\n"+
		"if [ \"$1\" = \"--help\" ]; then echo 'usage: piper --model FILE'; exit 0; fi\n"+
		"echo 'espeak-ng data directory not found' >&2\n"+
		"exit 1\n")

	_, _, err := e.Speak(context.Background(), "Hello.", "en", 1)
	if err == nil || !strings.Contains(err.Error(), "espeak-ng data directory not found") {
		t.Fatalf("err = %v, want piper's own complaint in it", err)
	}
}

// Piper can exit zero and print its trouble instead of audio. A zero exit is
// therefore not proof of anything; the WAV is.
func TestSpeakRefusesOutputThatIsNotAudio(t *testing.T) {
	requireScript(t)
	isolate(t)
	e := offline(t, New(t.TempDir()))
	installTree(t, e.Dir, "#!/bin/sh\n"+
		"if [ \"$1\" = \"--help\" ]; then echo 'usage: piper --model FILE'; exit 0; fi\n"+
		"echo 'model loaded'\n")

	if _, _, err := e.Speak(context.Background(), "Hello.", "en", 1); err == nil ||
		!strings.Contains(err.Error(), "not a WAV") {
		t.Fatalf("err = %v, want one saying no audio came back", err)
	}
}

// Silence and a broken voice sound exactly alike, so an empty text is an error
// rather than an empty WAV - and it is refused before anything is installed,
// because there is nothing to install it for.
func TestSpeakRefusesEmptyText(t *testing.T) {
	isolate(t)
	e := offline(t, New(filepath.Join(t.TempDir(), "voice")))

	for _, text := range []string{"", "   \n\t "} {
		if _, _, err := e.Speak(context.Background(), text, "de", 1); err == nil {
			t.Fatalf("%q should be refused", text)
		}
	}
	if _, err := os.Stat(e.Dir); err == nil {
		t.Error("nothing should have been installed for an empty text")
	}
}

// An answer that ran away is still an answer somebody wants to hear the start
// of, and rendering ten minutes of audio nobody listens to helps no one.
func TestSpeakTrimsTextThatIsFarTooLong(t *testing.T) {
	requireScript(t)
	isolate(t)
	record := t.TempDir()
	e := offline(t, New(t.TempDir()))
	installTree(t, e.Dir, fakePiper(record))

	long := strings.Repeat("ä", MaxTextRunes*3)
	if _, _, err := e.Speak(context.Background(), long, "de", 1); err != nil {
		t.Fatalf("speak: %v", err)
	}
	given := recorded(t, record, "stdin")
	if utf8.RuneCountInString(given) != MaxTextRunes {
		t.Errorf("piper was given %d runes, want %d", utf8.RuneCountInString(given), MaxTextRunes)
	}
	if !utf8.ValidString(given) {
		t.Error("the text was cut in the middle of a character")
	}
}

/* ----------------------------------------------------------- where it lives */

// The Docker image bakes piper and both voices in and points the environment
// variable at them. Downloading 150 MB on top of a tree that is already there
// would be the opposite of what the image is for.
func TestABakedInInstallationIsUsedWithoutDownloading(t *testing.T) {
	requireScript(t)
	isolate(t)
	record := t.TempDir()
	baked := t.TempDir()
	installTree(t, baked, fakePiper(record))
	t.Setenv(EnvDir, baked)

	s := newStand(t, fakePiper(record))
	s.hook = func(http.ResponseWriter, *http.Request) bool {
		t.Error("nothing should be downloaded when an installation is baked in")
		return false
	}
	e := engineOn(t, s)

	if !e.canSpeak() {
		t.Fatal("the baked in installation should be enough")
	}
	if err := e.Ensure(context.Background()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if _, _, err := e.Speak(context.Background(), "Guten Tag.", "de", 1); err != nil {
		t.Fatalf("speak: %v", err)
	}
	if got, want := after(arguments(t, record), "--model"), modelPath(voiceDir(baked), VoiceGerman); got != want {
		t.Errorf("--model = %q, want the baked in one %q", got, want)
	}
	if _, err := os.Stat(e.Dir); err == nil {
		t.Error("the managed directory should not even have been created")
	}
	if detail := e.Status().Detail; !strings.Contains(detail, "baked in") {
		t.Errorf("the setup check should say where piper came from: %q", detail)
	}
}

// A directory that is only half filled in - an image build that copied the
// binary and forgot the espeak data - has to fall back rather than half work.
func TestAnIncompleteBakedInInstallationFallsBackToTheManagedCopy(t *testing.T) {
	requireDownloadable(t)
	isolate(t)
	baked := t.TempDir()
	if err := os.MkdirAll(binaryDir(baked), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binaryPath(baked), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvDir, baked)

	s := newStand(t, fakePiper(t.TempDir()))
	e := engineOn(t, s)
	if err := e.Ensure(context.Background()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if s.hits("/release/") != 1 {
		t.Error("the managed copy should have been downloaded")
	}
	if detail := e.Status().Detail; !strings.Contains(detail, binaryPath(e.Dir)) {
		t.Errorf("status says %q, want the managed copy named", detail)
	}
}

// macOS has no build Socrates will install, so a piper the user installed with
// brew is the way it works there - and on any other machine where one is
// already on the PATH. The voices are still Socrates' own, because a package
// manager ships none.
func TestAPiperOnThePathIsUsedAndOnlyTheVoicesAreDownloaded(t *testing.T) {
	requireScript(t)
	isolate(t)
	record := t.TempDir()
	onPath := isolate(t) // the emptied PATH, which is where the piper goes
	if err := os.WriteFile(filepath.Join(onPath, "piper"), []byte(fakePiper(record)), 0o755); err != nil {
		t.Fatal(err)
	}

	s := newStand(t, fakePiper(record))
	e := engineOn(t, s)
	if err := e.Ensure(context.Background()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if hits := s.hits("/release/"); hits != 0 {
		t.Errorf("the release was downloaded %d times although piper is on the PATH", hits)
	}
	if hits := s.hits(".onnx"); hits != 4 {
		t.Errorf("%d voice files were downloaded, want four", hits)
	}
	if _, _, err := e.Speak(context.Background(), "Hello.", "en", 1); err != nil {
		t.Fatalf("speak: %v", err)
	}
	args := arguments(t, record)
	if got, want := after(args, "--model"), modelPath(voiceDir(e.Dir), VoiceEnglish); got != want {
		t.Errorf("--model = %q, want the managed voice %q", got, want)
	}
	// A piper from a package manager keeps its espeak data where its own build
	// expects it, and pointing it at a directory Socrates does not have would
	// break the one thing that was already working.
	if has(args, "--espeak_data") {
		t.Errorf("a piper from the PATH should be left to find its own data: %v", args)
	}
	if detail := e.Status().Detail; !strings.Contains(detail, "PATH") {
		t.Errorf("the setup check should say where piper came from: %q", detail)
	}
}

// The macOS archives of this release ship without the libraries their binary
// loads, and the arm64 one is the x86_64 build renamed. Installing that would
// leave something that looks finished and aborts in dyld, which is worse than
// a sentence saying what to do instead.
func TestMacOSIsRefusedRatherThanInstalledBroken(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		asset, err := assetFor("darwin", arch)
		if err == nil {
			t.Fatalf("darwin/%s offered %q", arch, asset)
		}
		if !strings.Contains(err.Error(), "brew install piper") {
			t.Errorf("darwin/%s: %v, want a sentence saying what to do instead", arch, err)
		}
	}
	if runtime.GOOS != "darwin" {
		return
	}
	isolate(t)
	s := newStand(t, "")
	s.hook = func(http.ResponseWriter, *http.Request) bool {
		t.Error("nothing should be downloaded on macOS")
		return false
	}
	e := engineOn(t, s)
	if err := e.Ensure(context.Background()); err == nil {
		t.Fatal("installing on macOS must be refused")
	}
}

/* ----------------------------------------------------------------- status */

// The setup check is the only place someone sees why an answer is not being
// read out loud, so every state it can be in has to be one honest sentence.
func TestStatusReportsWhatTheEngineCanDo(t *testing.T) {
	requireDownloadable(t)
	isolate(t)
	s := newStand(t, fakePiper(t.TempDir()))
	e := engineOn(t, s)

	status := e.Status()
	if status.Ready || status.State != StateMissing || status.Detail == "" {
		t.Errorf("an empty directory reports %#v", status)
	}
	if len(status.Voices) != 0 {
		t.Errorf("voices = %v, want none", status.Voices)
	}
	if status.Err != "" {
		t.Errorf("nothing has failed yet: %q", status.Err)
	}

	s.hook = func(w http.ResponseWriter, r *http.Request) bool {
		w.WriteHeader(http.StatusNotFound)
		return false
	}
	if err := e.Ensure(context.Background()); err == nil {
		t.Fatal("the install should have failed")
	}
	status = e.Status()
	if status.Ready || status.State != StateFailed {
		t.Errorf("after a failed install: %#v", status)
	}
	if !strings.Contains(status.Err, "404") {
		t.Errorf("the failure should say what happened: %q", status.Err)
	}

	s.hook = nil
	if err := e.Ensure(context.Background()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	status = e.Status()
	if !status.Ready || status.State != StateReady || status.Err != "" {
		t.Errorf("after a good install: %#v", status)
	}
	if len(status.Voices) != len(allVoices) {
		t.Errorf("voices = %v, want both of them", status.Voices)
	}
	if !strings.Contains(status.Detail, binaryPath(e.Dir)) {
		t.Errorf("the sentence should name the piper in use: %q", status.Detail)
	}
}

// A 150 MB download over a phone connection takes minutes, and a setup check
// that says nothing for minutes reads as one that has hung.
func TestStatusSaysItIsInstallingWhileTheDownloadRuns(t *testing.T) {
	requireDownloadable(t)
	isolate(t)
	s := newStand(t, fakePiper(t.TempDir()))

	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	s.hook = func(http.ResponseWriter, *http.Request) bool {
		once.Do(func() { close(started) })
		<-release
		return true
	}
	e := engineOn(t, s)

	done := make(chan error, 1)
	go func() { done <- e.Ensure(context.Background()) }()

	<-started
	status := e.Status()
	if status.Ready || status.State != StateInstalling || status.Detail == "" {
		t.Errorf("while installing: %#v", status)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if !e.Status().Ready {
		t.Error("the engine should be ready once the download finished")
	}
}

// A render asked for while the install is running must say so at once. If it
// queues behind the download instead, the browser waiting for it gives up
// after its own deadline and tells the listener the voice is broken, which is
// both wrong and unhelpful - and it stays wrong for as long as 150 MB take
// over a phone connection. The elapsed time is asserted on, because a Speak
// that comes back with the right error after two minutes is the bug.
func TestSpeakDoesNotQueueBehindAnInstall(t *testing.T) {
	requireDownloadable(t)
	record := t.TempDir()
	isolate(t)
	s := newStand(t, fakePiper(record))

	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	s.hook = func(http.ResponseWriter, *http.Request) bool {
		once.Do(func() { close(started) })
		<-release
		return true
	}
	e := engineOn(t, s)

	installed := make(chan error, 1)
	go func() { installed <- e.Ensure(context.Background()) }()
	<-started

	// A context that is alive and stays alive: the caller here is a browser
	// still waiting for its answer, not one that has hung up.
	begin := time.Now()
	_, _, err := e.Speak(context.Background(), "All done.", "en", 0)
	waited := time.Since(begin)
	if !errors.Is(err, ErrInstalling) {
		t.Fatalf("speak during an install = %v, want ErrInstalling", err)
	}
	if waited > 2*time.Second {
		t.Fatalf("speak waited %s for the install, it has to answer immediately", waited)
	}

	// And the install it stepped out of the way of still finishes, which is
	// the case this must not have broken: the engine simply needs to install
	// before it can say anything.
	close(release)
	if err := <-installed; err != nil {
		t.Fatalf("ensure: %v", err)
	}
	audio, contentType, err := e.Speak(context.Background(), "All done.", "en", 0)
	if err != nil {
		t.Fatalf("speak after the install: %v", err)
	}
	if contentType != ContentType || !bytes.HasPrefix(audio, []byte("RIFF")) {
		t.Fatalf("speak returned %d bytes of %q", len(audio), contentType)
	}
}

// The scenario this whole design exists for: a first start with no signal, so
// the install at startup fails, and the signal arrives while somebody is
// pressing play. The render has to hand back ErrInstalling at once - it is one
// HTTP request and the download is minutes - and the install it kicked off has
// to outlive that request. A render that owned the download died with the
// browser that gave up on it after twenty seconds, so every press started from
// zero and the voice could never finish installing at all.
func TestARenderAfterAFailedInstallStartsADownloadThatOutlivesTheRequest(t *testing.T) {
	requireDownloadable(t)
	record := t.TempDir()
	isolate(t)
	s := newStand(t, fakePiper(record))

	// The first attempt fails the way a car with no signal fails.
	s.hook = func(w http.ResponseWriter, r *http.Request) bool {
		w.WriteHeader(http.StatusNotFound)
		return false
	}
	e := engineOn(t, s)
	if err := e.Ensure(context.Background()); err == nil {
		t.Fatal("the install at startup should have failed")
	}
	if state := e.Status().State; state != StateFailed {
		t.Fatalf("state after the failed install = %q, want %q", state, StateFailed)
	}

	// There is signal again, and the download is as slow as 150 MB over a phone.
	started := make(chan struct{})
	release := make(chan struct{})
	var once, freed sync.Once
	free := func() { freed.Do(func() { close(release) }) }
	t.Cleanup(free)
	s.hook = func(http.ResponseWriter, *http.Request) bool {
		once.Do(func() { close(started) })
		<-release
		return true
	}

	// A request, on a context that lives exactly as long as the request does.
	ctx, hangUp := context.WithCancel(context.Background())
	waited, err := speakSoon(t, e, ctx)
	if !errors.Is(err, ErrInstalling) {
		t.Fatalf("speak after a failed install = %v, want ErrInstalling", err)
	}
	if waited > 2*time.Second {
		t.Fatalf("speak waited %s for the download, no render may own one", waited)
	}
	<-started

	// While the retry runs, the reason the last attempt failed is still there:
	// a platform where the install can never work must not read as a download
	// that is merely slow.
	if status := e.Status(); status.State != StateInstalling || !strings.Contains(status.Err, "404") {
		t.Errorf("status during the retry: %#v", status)
	}

	// The browser gives up on its answer. The download it started must not go
	// with it - that cancellation is what used to leave the voice half
	// installed for ever.
	hangUp()
	free()

	waitReady(t, e)
	audio, contentType, err := e.Speak(context.Background(), "All done.", "en", 0)
	if err != nil {
		t.Fatalf("speak once the install finished: %v", err)
	}
	if contentType != ContentType || !bytes.HasPrefix(audio, []byte("RIFF")) {
		t.Fatalf("speak returned %d bytes of %q", len(audio), contentType)
	}
}

// Somebody who is not being read to presses play again, and again. Each press
// has to find the install that is already running rather than start one of its
// own: a second download of the same 150 MB would halve a connection that is
// already the bottleneck, and neither would ever finish.
func TestRepeatedRendersDuringAnInstallStartOneDownload(t *testing.T) {
	requireDownloadable(t)
	record := t.TempDir()
	isolate(t)
	s := newStand(t, fakePiper(record))

	started := make(chan struct{})
	release := make(chan struct{})
	var once, freed sync.Once
	free := func() { freed.Do(func() { close(release) }) }
	t.Cleanup(free)
	s.hook = func(http.ResponseWriter, *http.Request) bool {
		once.Do(func() { close(started) })
		<-release
		return true
	}
	e := engineOn(t, s)

	for press := 1; press <= 10; press++ {
		waited, err := speakSoon(t, e, context.Background())
		if !errors.Is(err, ErrInstalling) {
			t.Fatalf("press %d = %v, want ErrInstalling", press, err)
		}
		if waited > 2*time.Second {
			t.Fatalf("press %d waited %s", press, waited)
		}
	}
	<-started
	free()
	waitReady(t, e)

	if hits := s.hits("/release/"); hits != 1 {
		t.Errorf("the release was downloaded %d times, want once for ten presses", hits)
	}
	for _, v := range allVoices {
		if hits := s.hits(v.Name + ".onnx"); hits != 2 {
			t.Errorf("%s was fetched %d times, want the model and its config once each", v.Name, hits)
		}
	}
}

// speakSoon renders and refuses to wait for ever. A Speak that blocks on a
// download is the bug these tests are about, and a test that hangs until the
// package timeout reports it as anything but that.
func speakSoon(t *testing.T, e *Engine, ctx context.Context) (time.Duration, error) {
	t.Helper()
	type outcome struct {
		waited time.Duration
		err    error
	}
	done := make(chan outcome, 1)
	begin := time.Now()
	go func() {
		_, _, err := e.Speak(ctx, "Read this out loud.", "en", 0)
		done <- outcome{time.Since(begin), err}
	}()
	select {
	case got := <-done:
		return got.waited, got.err
	case <-time.After(10 * time.Second):
		t.Fatal("Speak is still waiting on the install, so the render is running the download itself")
		return 0, nil
	}
}

// waitReady waits for an install nobody is holding a handle on any more.
func waitReady(t *testing.T, e *Engine) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		if e.canSpeak() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the install never finished: %#v", e.Status())
}

/* ------------------------------------------------------------------ units */

func TestAssetForEachPublishedPlatform(t *testing.T) {
	cases := []struct{ goos, goarch, want string }{
		{"linux", "amd64", "piper_linux_x86_64.tar.gz"},
		{"linux", "arm64", "piper_linux_aarch64.tar.gz"},
		{"linux", "arm", "piper_linux_armv7l.tar.gz"},
		{"windows", "amd64", "piper_windows_amd64.zip"},
	}
	for _, c := range cases {
		asset, err := assetFor(c.goos, c.goarch)
		if err != nil || asset != c.want {
			t.Errorf("assetFor(%s,%s) = %q, %v", c.goos, c.goarch, asset, err)
		}
	}
	// A platform with no build says so rather than downloading something that
	// cannot run.
	for _, c := range [][2]string{{"linux", "386"}, {"windows", "arm64"}, {"plan9", "amd64"}} {
		if _, err := assetFor(c[0], c[1]); err == nil {
			t.Errorf("%s/%s should have no build", c[0], c[1])
		}
	}
}

// Both voices are installed, so the language setting is a flip and never a
// download - which only holds if the flip actually reaches the other voice.
func TestVoiceForTheSpokenLanguage(t *testing.T) {
	cases := map[string]string{
		"de":    VoiceGerman,
		"DE":    VoiceGerman,
		" de ":  VoiceGerman,
		"de-DE": VoiceGerman,
		"de_CH": VoiceGerman,
		"en":    VoiceEnglish,
		"en-GB": VoiceEnglish,
		"":      VoiceEnglish,
		"auto":  VoiceEnglish,
		"fr":    VoiceEnglish,
	}
	for language, want := range cases {
		if got := voiceFor(language); got != want {
			t.Errorf("voiceFor(%q) = %q, want %q", language, got, want)
		}
	}
	if VoiceEnglish != "en_US-ljspeech-medium" || VoiceGerman != "de_DE-thorsten-medium" {
		// The voices are chosen for their licence as much as their sound, and
		// swapping one in without checking that is how a public repository
		// ends up shipping a research-only model.
		t.Errorf("the voices changed: %q and %q", VoiceEnglish, VoiceGerman)
	}
}

// The rate someone sets is how fast they want to be spoken to; piper takes the
// reciprocal, and getting that backwards would make the fast setting slow.
func TestLengthScaleIsTheInverseOfTheRate(t *testing.T) {
	cases := []struct {
		rate  float64
		want  string
		given bool
	}{
		{1.25, "0.8", true},
		{0.8, "1.25", true},
		{2, "0.5", true},
		{0.5, "2", true},
		{1, "", false},
		{0, "", false},
		{-1, "", false},
		{9, "0.5", true},  // clamped rather than refused
		{0.01, "2", true}, // the same at the other end
	}
	for _, c := range cases {
		got, given := lengthScale(c.rate)
		if got != c.want || given != c.given {
			t.Errorf("lengthScale(%v) = %q,%v, want %q,%v", c.rate, got, given, c.want, c.given)
		}
	}
}
