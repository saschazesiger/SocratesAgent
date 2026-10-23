package tunnel

import (
	"bytes"
	"context"
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

// releasePayload builds what Cloudflare would serve for this architecture.
// Every Linux asset is a bare binary, so there is nothing to wrap.
func releasePayload(t *testing.T, version string) []byte {
	t.Helper()
	if _, err := assetFor(runtime.GOARCH); err != nil {
		t.Skipf("no published build for this architecture: %v", err)
	}
	return fakeBinary(version)
}

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
	var hits atomic.Int32
	payload := releasePayload(t, "9.9.9")
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
	other := append([]byte("#!/bin/sh\necho \"totally different tool\"\n"),
		[]byte("# "+strings.Repeat("x", 1200000))...)
	server := releaseServer(t, other, nil)

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

func TestAssetFor(t *testing.T) {
	cases := map[string]string{
		"amd64": "cloudflared-linux-amd64",
		"arm64": "cloudflared-linux-arm64",
		"arm":   "cloudflared-linux-arm",
		"386":   "cloudflared-linux-386",
	}
	for goarch, want := range cases {
		asset, err := assetFor(goarch)
		if err != nil || asset != want {
			t.Errorf("assetFor(%s) = %q, %v; want %q", goarch, asset, err, want)
		}
	}
	if _, err := assetFor("mips"); err == nil {
		t.Error("an architecture Cloudflare does not publish must be reported")
	}
}

func TestManagerInstallsCloudflaredOnDemand(t *testing.T) {
	// An empty PATH makes the lookup miss deterministically.
	t.Setenv("PATH", t.TempDir())

	server := releaseServer(t, releasePayload(t, "9.9.9"), nil)

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

// A configured path that does not exist must not become a dead end: the whole
// point of the managed copy is that remote access works anyway.
func TestResolveFallsBackWhenConfiguredCommandIsMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	server := releaseServer(t, releasePayload(t, "9.9.9"), nil)

	// A path that is certainly missing: /usr/local/bin/cloudflared is there
	// on many a machine the tests run on.
	missing := filepath.Join(t.TempDir(), "cloudflared")
	settings := config.Default()
	settings.Tunnel = config.TunnelSettings{
		Enabled: true, Mode: config.TunnelQuick, Command: missing,
	}
	dir := filepath.Join(t.TempDir(), "bin")
	m := New(func() config.Settings { return settings }, func() string { return "" }, dir)
	m.installer.BaseURL = server.URL

	path, err := m.Resolve(context.Background())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if path != filepath.Join(dir, "cloudflared") {
		t.Fatalf("resolved to %q", path)
	}
	logs := strings.Join(m.Status().Logs, "\n")
	if !strings.Contains(logs, missing+" was not found") {
		t.Errorf("the fallback should be visible in the log: %s", logs)
	}
}
