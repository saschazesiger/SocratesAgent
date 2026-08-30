package tunnel

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// DefaultReleaseURL is where Cloudflare publishes the connector binaries.
const DefaultReleaseURL = "https://github.com/cloudflare/cloudflared/releases/latest/download"

// Installer fetches cloudflared into a directory Socrates owns, so remote
// access works without asking anyone to install anything by hand. The binary
// comes straight from Cloudflare's own release URL over TLS and is verified by
// running it once before it is used.
type Installer struct {
	Dir     string
	BaseURL string
	HTTP    *http.Client

	mu sync.Mutex
}

// NewInstaller creates an installer that keeps the binary in dir.
func NewInstaller(dir string) *Installer {
	return &Installer{
		Dir:     dir,
		BaseURL: DefaultReleaseURL,
		HTTP:    &http.Client{Timeout: 15 * time.Minute},
	}
}

// Path is where the managed binary lives.
func (i *Installer) Path() string {
	name := "cloudflared"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(i.Dir, name)
}

// Installed reports whether a usable managed binary is already there.
func (i *Installer) Installed() bool {
	path := i.Path()
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() < 1<<20 {
		return false
	}
	if runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
		return false
	}
	return true
}

// Supported reports whether this platform has a published build.
func Supported() bool {
	_, _, err := assetFor(runtime.GOOS, runtime.GOARCH)
	return err == nil
}

// Ensure returns the path to a working cloudflared, downloading it if needed.
// progress is called with human readable status lines.
func (i *Installer) Ensure(ctx context.Context, progress func(string)) (string, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if progress == nil {
		progress = func(string) {}
	}
	if i.Installed() {
		return i.Path(), nil
	}

	asset, archive, err := assetFor(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(i.Dir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", i.Dir, err)
	}

	url := strings.TrimRight(i.BaseURL, "/") + "/" + asset
	progress("downloading cloudflared from " + url)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := i.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("download cloudflared: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download cloudflared: %s returned %s", url, resp.Status)
	}

	temp, err := os.CreateTemp(i.Dir, ".cloudflared-*")
	if err != nil {
		return "", err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)

	body := io.Reader(&countingReader{
		reader: resp.Body,
		total:  resp.ContentLength,
		report: progress,
		last:   time.Now(),
	})
	if archive {
		body, err = binaryFromArchive(body)
		if err != nil {
			temp.Close()
			return "", err
		}
	}
	written, err := io.Copy(temp, body)
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", fmt.Errorf("download cloudflared: %w", err)
	}
	if written < 1<<20 {
		return "", fmt.Errorf("download cloudflared: the file is only %d bytes, that cannot be right", written)
	}
	if err := os.Chmod(tempName, 0o755); err != nil {
		return "", err
	}

	// Run it once: a truncated or wrong-architecture download fails here
	// instead of halfway through a tunnel run.
	check, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(check, tempName, "--version").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("the downloaded cloudflared does not run: %v", err)
	}
	if !strings.Contains(strings.ToLower(string(out)), "cloudflared") {
		return "", fmt.Errorf("the downloaded file does not look like cloudflared")
	}

	if err := os.Rename(tempName, i.Path()); err != nil {
		return "", fmt.Errorf("install cloudflared: %w", err)
	}
	progress("installed " + strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0]) + " to " + i.Path())
	invalidateProbe(i.Path())
	return i.Path(), nil
}

// binaryFromArchive pulls the cloudflared entry out of the macOS tarball.
func binaryFromArchive(r io.Reader) (io.Reader, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("unpack cloudflared: %w", err)
	}
	archive := tar.NewReader(gz)
	for {
		header, err := archive.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("unpack cloudflared: no binary inside the archive")
		}
		if err != nil {
			return nil, fmt.Errorf("unpack cloudflared: %w", err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		if filepath.Base(header.Name) == "cloudflared" {
			return io.LimitReader(archive, 512<<20), nil
		}
	}
}

// assetFor maps a platform onto the published release asset.
func assetFor(goos, goarch string) (asset string, archive bool, err error) {
	switch goos {
	case "linux":
		switch goarch {
		case "amd64":
			return "cloudflared-linux-amd64", false, nil
		case "arm64":
			return "cloudflared-linux-arm64", false, nil
		case "arm":
			return "cloudflared-linux-arm", false, nil
		case "386":
			return "cloudflared-linux-386", false, nil
		}
	case "darwin":
		switch goarch {
		case "amd64":
			return "cloudflared-darwin-amd64.tgz", true, nil
		case "arm64":
			return "cloudflared-darwin-arm64.tgz", true, nil
		}
	case "windows":
		switch goarch {
		case "amd64":
			return "cloudflared-windows-amd64.exe", false, nil
		case "386":
			return "cloudflared-windows-386.exe", false, nil
		}
	}
	return "", false, fmt.Errorf("Cloudflare publishes no cloudflared build for %s/%s - install it yourself and set the path in the admin dashboard", goos, goarch)
}

// countingReader reports download progress at a readable pace.
type countingReader struct {
	reader io.Reader
	total  int64
	read   int64
	report func(string)
	last   time.Time
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.reader.Read(p)
	c.read += int64(n)
	if time.Since(c.last) > 900*time.Millisecond {
		c.last = time.Now()
		if c.total > 0 {
			c.report(fmt.Sprintf("downloading cloudflared… %d%% (%.1f of %.1f MB)",
				c.read*100/c.total, mb(c.read), mb(c.total)))
		} else {
			c.report(fmt.Sprintf("downloading cloudflared… %.1f MB", mb(c.read)))
		}
	}
	return n, err
}

func mb(bytes int64) float64 { return float64(bytes) / (1 << 20) }
