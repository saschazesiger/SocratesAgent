package piper

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// DefaultReleaseURL is the piper release Socrates installs. The tag is
	// pinned rather than "latest" on purpose: the flags and the layout of the
	// standalone archive are what this package knows, and a voice that stops
	// working because upstream published a new tag is not a failure anyone
	// here could debug for the person sitting in the car.
	DefaultReleaseURL = "https://github.com/rhasspy/piper/releases/download/2023.11.14-2"

	// DefaultVoicesURL is where the rhasspy voice models are published.
	DefaultVoicesURL = "https://huggingface.co/rhasspy/piper-voices/resolve/main"

	// EnvDir points at an installation that is already there, laid out the way
	// this package lays out its own: piper/ beside voices/. The Docker image
	// sets it to the tree it baked in, which is why the container speaks
	// without downloading anything first.
	EnvDir = "SOCRATES_PIPER_DIR"
)

const (
	// minModelBytes is a floor, not a size. The medium models are around
	// 60 MB, so anything close to this is a transfer that stopped early or a
	// login page from a captive portal that arrived with a 200.
	minModelBytes = 1 << 20

	// minConfigBytes guards the small half of a voice the same way. The real
	// config is about 5 KB of JSON.
	minConfigBytes = 200

	// maxDownloadBytes bounds any single file, so a URL that turns out to
	// serve something endless cannot fill the disk.
	maxDownloadBytes = 512 << 20
)

// voice is one model Socrates installs. Path is where it lives in the voice
// repository, which spells out language, locale, speaker and quality; Label is
// how it is named in a sentence someone reads in the dashboard.
type voice struct {
	Name  string
	Path  string
	Label string
}

var allVoices = []voice{
	{Name: VoiceEnglish, Path: "en/en_US/ljspeech/medium", Label: "the English voice"},
	{Name: VoiceGerman, Path: "de/de_DE/thorsten/medium", Label: "the German voice"},
}

// assetFor maps a platform onto the published release asset.
//
// macOS is refused rather than attempted. Both macOS archives of this release
// ship without the dylibs their binary loads through @rpath, and the aarch64
// one is the x86_64 build under another name (rhasspy/piper#284, #321 and
// #404, all still open). Installing that would leave a tree that looks
// finished and then aborts inside dyld, which is the worst way for this to
// fail; a piper the user installed themselves is found on the PATH instead,
// which is what the sentence here asks for.
func assetFor(goos, goarch string) (string, error) {
	switch goos {
	case "linux":
		switch goarch {
		case "amd64":
			return "piper_linux_x86_64.tar.gz", nil
		case "arm64":
			return "piper_linux_aarch64.tar.gz", nil
		case "arm":
			return "piper_linux_armv7l.tar.gz", nil
		}
	case "windows":
		if goarch == "amd64" {
			return "piper_windows_amd64.zip", nil
		}
	case "darwin":
		return "", errors.New("the published Piper builds for macOS are broken - they ship without the libraries the binary needs - so Socrates will not install one; run `brew install piper` and Socrates picks it up from the PATH")
	}
	return "", fmt.Errorf("Piper publishes no build for %s/%s - install piper yourself and Socrates picks it up from the PATH", goos, goarch)
}

// install downloads whatever the resolved installation is still missing.
// Everything lands in the directory Socrates owns: an installation that was
// baked into an image or put on the PATH by a package manager is somebody
// else's and is never written to.
func (e *Engine) install(ctx context.Context) error {
	if err := os.MkdirAll(e.Dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", e.Dir, err)
	}
	inst := e.resolve()
	if inst.binary == "" {
		if err := e.installBinary(ctx); err != nil {
			return err
		}
	}
	for _, v := range allVoices {
		if voiceInstalled(inst.voices, v.Name) {
			continue
		}
		if err := e.installVoice(ctx, v); err != nil {
			return err
		}
	}
	// Everything reported success, so anything still missing here is a bug in
	// this package rather than a download problem - and it is better said out
	// loud than discovered as a render that never works.
	if !e.resolve().ready() {
		return errors.New("Piper was installed but something is still missing from " + e.Dir)
	}
	return nil
}

// installBinary fetches the release archive and unpacks it into piper/.
//
// The archive is unpacked into a staging directory beside the destination and
// moved into place in one rename, so a download that stops halfway leaves a
// temporary directory to delete rather than a tree that looks installed and is
// not.
func (e *Engine) installBinary(ctx context.Context) error {
	asset, err := assetFor(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	url := strings.TrimRight(e.ReleaseURL, "/") + "/" + asset
	e.report("Downloading Piper from " + url)

	staging, err := os.MkdirTemp(e.Dir, ".piper-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	resp, err := e.fetch(ctx, url, "Piper")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body := e.counting(resp, "Piper")

	if strings.HasSuffix(asset, ".zip") {
		err = extractZip(body, staging)
	} else {
		err = extractTarGz(body, staging)
	}
	if err != nil {
		return err
	}

	tree, err := treeRoot(staging)
	if err != nil {
		return err
	}
	if err := os.Chmod(filepath.Join(tree, exeName()), 0o755); err != nil {
		return err
	}
	if err := verifyBinary(ctx, tree); err != nil {
		return err
	}

	// A tree from an earlier attempt would make the rename fail; it is already
	// known to be unusable, because that is what brought us here.
	dest := binaryDir(e.Dir)
	if err := os.RemoveAll(dest); err != nil {
		return fmt.Errorf("replace %s: %w", dest, err)
	}
	if err := os.Rename(tree, dest); err != nil {
		return fmt.Errorf("install Piper into %s: %w", dest, err)
	}
	e.report("Installed Piper in " + dest)
	return nil
}

// installVoice fetches one model and its config.
func (e *Engine) installVoice(ctx context.Context, v voice) error {
	dir := voiceDir(e.Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	base := strings.TrimRight(e.VoicesURL, "/") + "/" + v.Path + "/" + v.Name
	if err := e.downloadFile(ctx, base+".onnx", modelPath(dir, v.Name), v.Label, minModelBytes, nil); err != nil {
		return err
	}
	return e.downloadFile(ctx, base+".onnx.json", configPath(dir, v.Name), v.Label+" config", minConfigBytes, validJSON)
}

// downloadFile fetches one file into place. It writes next to the destination
// and renames, so nothing ever sees a half written file and mistakes it for a
// finished one.
func (e *Engine) downloadFile(ctx context.Context, url, dest, label string, min int64, validate func(string) error) error {
	e.report("Downloading " + label + "…")
	resp, err := e.fetch(ctx, url, label)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	temp, err := os.CreateTemp(filepath.Dir(dest), ".download-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)

	written, err := io.Copy(temp, io.LimitReader(e.counting(resp, label), maxDownloadBytes))
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("download %s: %w", label, err)
	}
	if written < min {
		return fmt.Errorf("download %s: the file is only %d bytes, that cannot be right", label, written)
	}
	// A connection that dies mid transfer often looks like a clean end of
	// body, and a 60 MB model missing its last megabyte would still pass the
	// size floor and then fail at the point where somebody is waiting to hear
	// an answer.
	if resp.ContentLength > 0 && written != resp.ContentLength {
		return fmt.Errorf("download %s: it stopped after %d of %d bytes", label, written, resp.ContentLength)
	}
	if validate != nil {
		if err := validate(tempName); err != nil {
			return fmt.Errorf("download %s: %w", label, err)
		}
	}
	if err := os.Rename(tempName, dest); err != nil {
		return fmt.Errorf("install %s: %w", label, err)
	}
	return nil
}

// validJSON checks that what arrived is what was asked for. A hotel wifi login
// page is a perfectly valid 200 response, and saved as a voice config it would
// turn into a piper that starts and refuses every sentence for reasons nobody
// would think to look for here.
func validJSON(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !json.Valid(raw) {
		return errors.New("what arrived is not JSON, so it is not the voice config")
	}
	return nil
}

// fetch performs the GET and turns anything that is not a 200 into a sentence
// naming what was being downloaded, because "404" on its own says nothing
// about which of the files is missing.
func (e *Engine) fetch(ctx context.Context, url, label string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := e.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", label, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("download %s: %s returned %s", label, url, resp.Status)
	}
	return resp, nil
}

func (e *Engine) client() *http.Client {
	if e.HTTP != nil {
		return e.HTTP
	}
	return http.DefaultClient
}

// counting wraps a response body so the dashboard can show how far along a
// download is. On a phone connection these are minutes, and a setup check that
// says nothing for minutes reads as one that has hung.
func (e *Engine) counting(resp *http.Response, label string) io.Reader {
	return &countingReader{
		reader: resp.Body,
		total:  resp.ContentLength,
		label:  label,
		report: e.report,
		last:   time.Now(),
	}
}

// verifyBinary runs the freshly unpacked piper once. A truncated download, a
// build for the wrong architecture and a tree missing one of its shared
// libraries all fail here, during the install, instead of in the middle of the
// first answer somebody asked to have read to them.
//
// It is the usage text that is trusted and not the exit status: printing the
// flags and leaving is what --help does, and whether it leaves with a zero is
// a detail of an argument parser worth depending on in neither direction.
func verifyBinary(ctx context.Context, tree string) error {
	check, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(check, filepath.Join(tree, exeName()), "--help")
	cmd.Env = childEnv(tree)
	out, err := cmd.CombinedOutput()
	text := string(out)
	if strings.Contains(text, "--model") || strings.Contains(strings.ToLower(text), "piper") {
		return nil
	}
	if err != nil {
		return fmt.Errorf("the downloaded Piper does not run: %v: %s", err, flatten(text))
	}
	return fmt.Errorf("the downloaded file does not look like Piper: %s", flatten(text))
}

// treeRoot finds the directory the executable ended up in. The published
// archives hold a single top-level piper/ directory, and an archive that one
// day does not would otherwise install a tree with no binary in it.
func treeRoot(staging string) (string, error) {
	if isFile(filepath.Join(staging, exeName())) {
		return staging, nil
	}
	entries, err := os.ReadDir(staging)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		candidate := filepath.Join(staging, entry.Name())
		if isFile(filepath.Join(candidate, exeName())) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("the Piper archive has no %s in it", exeName())
}

// isFile reports whether path is a regular file. The archive unpacks into a
// directory called piper that holds a binary called piper, so a check that
// only asks whether the name exists finds the directory and then tries to run
// it.
func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// extractTarGz unpacks the Linux and macOS archives.
func extractTarGz(r io.Reader, dest string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("unpack Piper: %w", err)
	}
	defer gz.Close()

	archive := tar.NewReader(gz)
	for {
		header, err := archive.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("unpack Piper: %w", err)
		}
		path, err := safeJoin(dest, header.Name)
		if err != nil {
			return err
		}
		if err := refuseSymlinkedPath(dest, path); err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := writeFile(path, archive, os.FileMode(header.Mode).Perm()); err != nil {
				return err
			}
		case tar.TypeSymlink:
			// The shared libraries ship as a chain of links -
			// libonnxruntime.so points at libonnxruntime.so.1.14.1 and so on -
			// and the loader asks for the name the binary was linked against,
			// so a tree without them cannot start.
			link, err := checkedLink(dest, path, header.Linkname)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			os.Remove(path)
			if err := os.Symlink(link, path); err != nil {
				return fmt.Errorf("unpack Piper: %w", err)
			}
		}
		// Everything else - devices, hard links, whatever else a tar can
		// carry - has no business in this archive and is skipped rather than
		// recreated.
	}
}

// extractZip unpacks the Windows archive.
//
// Windows gets its own branch because it costs a few lines of archive/zip and
// nothing else: the DLLs sit beside piper.exe, which is the first place
// Windows looks, so there is no library path to arrange. It is offered rather
// than promised though - the image this app ships in is Linux and the people
// writing it are on Linux and macOS, so the honest failure if it is wrong is
// the verification run at the end of the install.
func extractZip(r io.Reader, dest string) error {
	// archive/zip reads the directory at the end of the file and seeks back,
	// so the download is spooled to disk first. The tar path streams, which is
	// why it does not do this.
	spool, err := os.CreateTemp(dest, ".zip-*")
	if err != nil {
		return err
	}
	defer os.Remove(spool.Name())
	defer spool.Close()

	size, err := io.Copy(spool, io.LimitReader(r, maxDownloadBytes))
	if err != nil {
		return fmt.Errorf("download Piper: %w", err)
	}
	archive, err := zip.NewReader(spool, size)
	if err != nil {
		return fmt.Errorf("unpack Piper: %w", err)
	}
	for _, entry := range archive.File {
		path, err := safeJoin(dest, entry.Name)
		if err != nil {
			return err
		}
		if err := refuseSymlinkedPath(dest, path); err != nil {
			return err
		}
		mode := entry.Mode()
		switch {
		case entry.FileInfo().IsDir():
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
		case mode&os.ModeSymlink != 0:
			if err := copyZipSymlink(entry, dest, path); err != nil {
				return err
			}
		case mode.IsRegular():
			file, err := entry.Open()
			if err != nil {
				return fmt.Errorf("unpack Piper: %w", err)
			}
			err = writeFile(path, file, mode.Perm())
			file.Close()
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func copyZipSymlink(entry *zip.File, dest, path string) error {
	file, err := entry.Open()
	if err != nil {
		return fmt.Errorf("unpack Piper: %w", err)
	}
	defer file.Close()
	target, err := io.ReadAll(io.LimitReader(file, 4<<10))
	if err != nil {
		return fmt.Errorf("unpack Piper: %w", err)
	}
	link, err := checkedLink(dest, path, string(target))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	os.Remove(path)
	return os.Symlink(link, path)
}

// writeFile puts one archive entry on disk, capped so an archive that claims
// an implausible size cannot fill the disk.
func writeFile(path string, content io.Reader, mode os.FileMode) error {
	if mode == 0 {
		mode = 0o644
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(file, io.LimitReader(content, maxDownloadBytes))
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("unpack Piper: %w", err)
	}
	return nil
}

// safeJoin turns an entry name from an archive into a path inside dest and
// refuses one that would climb out of it. An archive is a file from the
// internet: "../../../etc/cron.d/anything" is a thing archives have really
// carried, and coming from a release page makes an entry no more trustworthy.
func safeJoin(dest, name string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unpack Piper: the archive contains %q, which points outside the install directory", name)
	}
	return filepath.Join(dest, clean), nil
}

// checkedLink turns the target of a symlink entry into the value the link is
// created with, and refuses one that leaves dest. It resolves the target the
// way the kernel will - from the directory the link is written into, not from
// the name the archive gave it - and it returns that same value rather than
// leaving the caller to pass something else to os.Symlink: checking one string
// and writing another is how an archive gets to name /root as a target and
// have the check agree with itself.
func checkedLink(dest, path, target string) (string, error) {
	link := filepath.FromSlash(target)
	if link == "" || filepath.IsAbs(link) {
		return "", fmt.Errorf("unpack Piper: the archive links %q to %q, which is not a path inside the install directory",
			path, target)
	}
	resolved := filepath.Join(filepath.Dir(path), link)
	if resolved != dest && !strings.HasPrefix(resolved, dest+string(filepath.Separator)) {
		return "", fmt.Errorf("unpack Piper: the archive links %q to %q, which points outside the install directory",
			path, target)
	}
	return link, nil
}

// refuseSymlinkedPath refuses an entry whose path runs through something
// already unpacked that is a symlink. safeJoin reads the name the archive
// gives; the kernel walks the directories that name goes through, and those
// are two different questions once a link has been unpacked. An archive that
// carries a link called piper/lib and then a file called piper/lib/x has no
// entry that climbs anywhere, and the file still lands wherever the link
// pointed - which is why this is checked for every entry and not only for the
// links. The final component counts too: writing a regular file opens the
// name, and opening a symlink writes through it.
func refuseSymlinkedPath(dest, path string) error {
	rest, err := filepath.Rel(dest, path)
	if err != nil {
		return fmt.Errorf("unpack Piper: %w", err)
	}
	walked := dest
	for _, part := range strings.Split(rest, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		walked = filepath.Join(walked, part)
		info, err := os.Lstat(walked)
		if errors.Is(err, os.ErrNotExist) {
			// Nothing below an absent directory exists either, so there is
			// nothing further to look at.
			return nil
		}
		if err != nil {
			return fmt.Errorf("unpack Piper: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unpack Piper: the archive unpacks %q through %q, which it made a symlink",
				path, walked)
		}
	}
	return nil
}

// countingReader reports download progress at a pace a human can read.
type countingReader struct {
	reader io.Reader
	total  int64
	read   int64
	label  string
	report func(string)
	last   time.Time
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.reader.Read(p)
	c.read += int64(n)
	if time.Since(c.last) > 900*time.Millisecond {
		c.last = time.Now()
		if c.total > 0 {
			c.report(fmt.Sprintf("Downloading %s… %d%% (%.1f of %.1f MB)",
				c.label, c.read*100/c.total, mb(c.read), mb(c.total)))
		} else {
			c.report(fmt.Sprintf("Downloading %s… %.1f MB", c.label, mb(c.read)))
		}
	}
	return n, err
}

func mb(bytes int64) float64 { return float64(bytes) / (1 << 20) }
