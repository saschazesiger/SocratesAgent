package tunnel

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/config"
)

// fakeBinary is a script that behaves enough like cloudflared, padded past the
// installer's minimum size check.
func fakeBinary(version string) []byte {
	var buf bytes.Buffer
	buf.WriteString("#!/bin/sh\n")
	buf.WriteString("echo \"cloudflared version " + version + " (built test)\"\n")
	buf.WriteString("exit 0\n")
	buf.WriteString("# " + strings.Repeat("padding ", 160000))
	return buf.Bytes()
}

func releaseServer(t *testing.T, payload []byte, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		_, _ = w.Write(payload)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestInstallerDownloadsAndVerifies(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake binary is a POSIX script")
	}
	var hits atomic.Int32
	payload := fakeBinary("9.9.9")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	installer := NewInstaller(filepath.Join(t.TempDir(), "bin"))
	installer.BaseURL = server.URL

	var progress []string
	path, err := installer.Ensure(context.Background(), func(line string) { progress = append(progress, line) })
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if !installer.Installed() {
		t.Fatal("the binary should be reported as installed")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Error("the installed binary is not executable")
	}
	if len(progress) == 0 || !strings.Contains(strings.Join(progress, "\n"), "installed") {
		t.Errorf("progress was not reported: %#v", progress)
	}

	// A second call reuses what is already there.
	if _, err := installer.Ensure(context.Background(), nil); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if hits.Load() != 1 {
		t.Errorf("expected exactly one download, got %d", hits.Load())
	}
}

func TestInstallerRejectsSomethingElse(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake binary is a POSIX script")
	}
	payload := append([]byte("#!/bin/sh\necho \"totally different tool\"\n"),
		[]byte("# "+strings.Repeat("x", 1200000))...)
	server := releaseServer(t, payload, nil)

	installer := NewInstaller(filepath.Join(t.TempDir(), "bin"))
	installer.BaseURL = server.URL
	if _, err := installer.Ensure(context.Background(), nil); err == nil {
		t.Fatal("a binary that is not cloudflared must be rejected")
	}
	if installer.Installed() {
		t.Error("nothing should be left behind after a failed install")
	}
	entries, _ := os.ReadDir(installer.Dir)
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".") {
			t.Errorf("leftover file: %s", entry.Name())
		}
	}
}

func TestInstallerRejectsTruncatedDownload(t *testing.T) {
	server := releaseServer(t, []byte("nope"), nil)
	installer := NewInstaller(filepath.Join(t.TempDir(), "bin"))
	installer.BaseURL = server.URL
	if _, err := installer.Ensure(context.Background(), nil); err == nil {
		t.Fatal("a tiny download must be rejected")
	}
}

func TestInstallerReportsServerErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	installer := NewInstaller(filepath.Join(t.TempDir(), "bin"))
	installer.BaseURL = server.URL
	_, err := installer.Ensure(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v", err)
	}
}

func TestBinaryFromArchive(t *testing.T) {
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	archive := tar.NewWriter(gz)
	content := []byte("the real binary")
	_ = archive.WriteHeader(&tar.Header{Name: "README", Mode: 0o644, Size: 4, Typeflag: tar.TypeReg})
	_, _ = archive.Write([]byte("junk"))
	_ = archive.WriteHeader(&tar.Header{Name: "cloudflared", Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg})
	_, _ = archive.Write(content)
	_ = archive.Close()
	_ = gz.Close()

	reader, err := binaryFromArchive(bytes.NewReader(raw.Bytes()))
	if err != nil {
		t.Fatalf("unpack: %v", err)
	}
	got, _ := io.ReadAll(reader)
	if string(got) != string(content) {
		t.Fatalf("got %q", got)
	}
}

func TestAssetFor(t *testing.T) {
	cases := []struct {
		goos, goarch, want string
		archive            bool
	}{
		{"linux", "amd64", "cloudflared-linux-amd64", false},
		{"linux", "arm64", "cloudflared-linux-arm64", false},
		{"darwin", "arm64", "cloudflared-darwin-arm64.tgz", true},
		{"windows", "amd64", "cloudflared-windows-amd64.exe", false},
	}
	for _, c := range cases {
		asset, archive, err := assetFor(c.goos, c.goarch)
		if err != nil || asset != c.want || archive != c.archive {
			t.Errorf("assetFor(%s,%s) = %q,%v,%v", c.goos, c.goarch, asset, archive, err)
		}
	}
	if _, _, err := assetFor("plan9", "mips"); err == nil {
		t.Error("unsupported platforms must be reported")
	}
}

func TestManagerInstallsCloudflaredOnDemand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake binary is a POSIX script")
	}
	// An empty PATH makes the lookup miss deterministically.
	t.Setenv("PATH", t.TempDir())

	payload := append(fakeBinary("9.9.9"), []byte("\n")...)
	server := releaseServer(t, payload, nil)

	settings := config.Default()
	settings.Tunnel = config.TunnelSettings{Enabled: true, Mode: config.TunnelQuick, Command: "cloudflared"}
	settings.Normalize()

	dir := filepath.Join(t.TempDir(), "bin")
	m := New(func() config.Settings { return settings }, func() string { return "http://127.0.0.1:9999" }, dir)
	m.installer.BaseURL = server.URL
	t.Cleanup(m.Stop)

	path, err := m.Install(context.Background())
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if path != filepath.Join(dir, "cloudflared") {
		t.Fatalf("installed to %q", path)
	}
	installed, version, resolved := m.Probe()
	if !installed || !strings.Contains(version, "9.9.9") || resolved != path {
		t.Fatalf("probe = %v %q %q", installed, version, resolved)
	}
	status := m.Status()
	if !status.Managed || !status.Installed {
		t.Fatalf("status = %#v", status)
	}
}

func TestResolveKeepsExplicitCommand(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	settings := config.Default()
	settings.Tunnel = config.TunnelSettings{Enabled: true, Mode: config.TunnelQuick, Command: "my-own-cloudflared"}
	m := New(func() config.Settings { return settings }, func() string { return "" }, t.TempDir())
	m.installer.BaseURL = "http://127.0.0.1:1" // must never be reached
	if _, err := m.Resolve(context.Background()); err == nil {
		t.Fatal("an explicit command that is missing must be reported, not replaced by a download")
	}
}
