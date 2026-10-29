package driver

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestTOTPVectors(t *testing.T) {
	// RFC 6238 SHA-1 vectors (last six digits).
	secret := []byte("12345678901234567890")
	for ms, want := range map[int64]string{59_000: "287082", 1_111_111_109_000: "081804", 20_000_000_000_000: "353130"} {
		if code, _ := totpOf(secret, time.UnixMilli(ms)); code != want {
			t.Errorf("totp at %d = %s, want %s", ms, code, want)
		}
	}
	if code, _ := TOTP("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", time.UnixMilli(59_000)); code != "287082" {
		t.Errorf("TOTP of the base32 key = %s", code)
	}
}

func TestBase32Decode(t *testing.T) {
	if !bytes.Equal(base32Decode("gezd gnbv gy3t qojq"), base32Decode("GEZDGNBVGY3TQOJQ")) {
		t.Error("spaces and lower case change the key")
	}
	if got := string(base32Decode("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ")); got != "12345678901234567890" {
		t.Errorf("decoded %q", got)
	}
}

func TestTOTPSecondsLeft(t *testing.T) {
	if _, left := totpOf([]byte("x"), time.UnixMilli(31_000)); left != 29 {
		t.Errorf("seconds left = %d, want 29", left)
	}
}

var (
	magenta = color.RGBA{255, 0, 255, 255}
	white   = color.RGBA{255, 255, 255, 255}
)

// screen is a phone screen with 21 px marker tiles at the given corners and
// 60 px magenta blobs (an ad) at extra.
func screen(t *testing.T, markers, extra [][2]int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1080, 2400))
	for i := 0; i < len(img.Pix); i += 4 {
		copy(img.Pix[i:], []byte{255, 255, 255, 255})
	}
	fill := func(at [2]int, size int) {
		for y := at[1]; y < at[1]+size; y++ {
			for x := at[0]; x < at[0]+size; x++ {
				img.SetRGBA(x, y, magenta)
			}
		}
	}
	for _, m := range markers {
		fill(m, 21)
	}
	for _, e := range extra {
		fill(e, 60)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestFindMarkers(t *testing.T) {
	found, err := findMarkers(screen(t, [][2]int{{0, 323}, {263, 586}}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if want := (&markers{ImageWidth: 1080, First: point{0, 323}, Second: point{263, 586}}); !reflect.DeepEqual(found, want) {
		t.Errorf("below a toolbar: %+v, want %+v", found, want)
	}
	// Magenta in the page does not fool it.
	found, _ = findMarkers(screen(t, [][2]int{{0, 177}, {300, 477}}, [][2]int{{700, 1500}}))
	if found == nil || found.First != (point{0, 177}) || found.Second != (point{300, 477}) {
		t.Errorf("with an ad: %+v", found)
	}
	// No markers, no calibration.
	if found, _ = findMarkers(screen(t, nil, nil)); found != nil {
		t.Errorf("without markers: %+v", found)
	}
	if _, err := findMarkers([]byte("not a png")); err == nil {
		t.Error("a broken PNG decodes")
	}
}

func readLines(t *testing.T, dir string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "actions.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var v map[string]any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}

func testAction(e *Evidence, name string) *Action {
	return &Action{N: e.Next(), At: "2026-10-29T09:00:00.000Z", Action: name, Summary: name + " done", MS: 12}
}

func asJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestEvidenceLines(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "evidence")
	e, err := NewEvidence(dir, "browserstack")
	if err != nil {
		t.Fatal(err)
	}
	open := testAction(e, "open")
	open.URL = "https://beta.browser.lol/es/auth?token=secret1&lang=es"
	open.Settle = &Settle{MS: 900, Settled: true, Busy: []string{}}
	if err := e.WriteScreenshot(open, []byte("png")); err != nil {
		t.Fatal(err)
	}
	if err := e.Append(open); err != nil {
		t.Fatal(err)
	}
	tap := testAction(e, "tap")
	tap.Target = &Target{Text: "Ajustes avanzados", Native: true, Matched: "Ajustes avanzados"}
	tap.Error = "native: the touch landed on div"
	tap.Output = "tapped button"
	if err := e.Append(tap); err != nil {
		t.Fatal(err)
	}
	lines := readLines(t, dir)
	want := `{"action":"open","at":"2026-10-29T09:00:00.000Z","details":{"settle":{"busy":[],"ms":900,"settled":true}},"error":null,"interaction":false,"ms":12,"screenshot":"01-open.png","summary":"open done","tool":"browserstack","url":"https://beta.browser.lol/es/auth?token=…&lang=es"}`
	if got := strings.ReplaceAll(asJSON(t, lines[0]), `\u0026`, "&"); got != want {
		t.Errorf("first line\n got %s\nwant %s", got, want)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "actions.jsonl"))
	if !strings.HasPrefix(string(raw), `{"at":"2026-10-29T09:00:00.000Z","tool":"browserstack","action":"open","summary":"open done","url":"https://beta.browser.lol/es/auth?token=…&lang=es",`) {
		t.Errorf("the line's fields are not in order or escaped: %s", raw)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "01-open.png")); string(data) != "png" {
		t.Errorf("screenshot holds %q", data)
	}
	second := lines[1]
	if second["interaction"] != true || second["screenshot"] != nil || second["error"] != "native: the touch landed on div" {
		t.Errorf("second line: %v", second)
	}
	if got := asJSON(t, second["details"]); got != `{"output":"tapped button","target":{"matched":"Ajustes avanzados","native":true,"text":"Ajustes avanzados"}}` {
		t.Errorf("second details: %s", got)
	}
}

func TestEvidenceInteractions(t *testing.T) {
	e := &Evidence{tool: "browserstack"}
	var flags []bool
	for _, name := range []string{"tap", "type", "key", "find", "look", "open", "scroll", "keyboard"} {
		flags = append(flags, e.Line(&Action{N: 1, Action: name}).Interaction)
	}
	if want := []bool{true, true, true, false, false, false, false, false}; !reflect.DeepEqual(flags, want) {
		t.Errorf("interactions %v", flags)
	}
}

func TestEvidenceNumbersOn(t *testing.T) {
	dir := t.TempDir()
	first, _ := NewEvidence(dir, "browserstack")
	_ = first.Append(testAction(first, "open"))
	_ = first.Append(testAction(first, "look"))
	again, err := NewEvidence(dir, "browserstack")
	if err != nil {
		t.Fatal(err)
	}
	shot := testAction(again, "shot")
	if shot.N != 3 || again.ScreenshotName(shot) != "03-shot.png" {
		t.Errorf("n %d, name %s", shot.N, again.ScreenshotName(shot))
	}
}

func TestEvidenceLinksAndLogs(t *testing.T) {
	dir := t.TempDir()
	e, _ := NewEvidence(dir, "browserstack")
	if err := e.WriteLinks(map[string]string{"Video": "https://example.test/video.mp4?a=1&b=2", "Network log (HAR)": ""}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "links.json"))
	if string(data) != "{\n \"Video\": \"https://example.test/video.mp4?a=1&b=2\"\n}\n" {
		t.Errorf("links.json: %q", data)
	}
	file, err := e.WriteLog("oneuptime", []string{"a", "b"})
	if err != nil || file != filepath.Join(dir, "logs", "oneuptime.txt") {
		t.Fatalf("log %s: %v", file, err)
	}
	if data, _ := os.ReadFile(file); string(data) != "a\nb\n" {
		t.Errorf("log holds %q", data)
	}
	if _, err := os.Stat(filepath.Join(dir, "actions.jsonl")); !os.IsNotExist(err) {
		t.Error("actions.jsonl exists without an action")
	}
}

func TestExtractLinks(t *testing.T) {
	text := "Open https://beta.browser.lol/en/auth/verify?token=abc&amp;x=1."
	html := `<a href="https://evil.example/x">x</a><a href="https://goog.re/y">y</a><a href="https://goog.re/y">again</a>`
	got := ExtractLinks(text, html, []string{"browser.lol", "goog.re"})
	if want := []string{"https://beta.browser.lol/en/auth/verify?token=abc&x=1", "https://goog.re/y"}; !reflect.DeepEqual(got, want) {
		t.Errorf("with hosts: %v", got)
	}
	got = ExtractLinks(text, html, nil)
	if want := []string{"https://beta.browser.lol/en/auth/verify?token=abc&x=1", "https://evil.example/x", "https://goog.re/y"}; !reflect.DeepEqual(got, want) {
		t.Errorf("without hosts: %v", got)
	}
	if got := ExtractLinks("https://notgoog.re/x https://goog.re.evil/x", "", []string{"goog.re"}); len(got) != 0 {
		t.Errorf("look-alike hosts pass: %v", got)
	}
}

func TestFallbackCode(t *testing.T) {
	if got := FallbackCode("Your code is 482913."); got != "482913" {
		t.Errorf("code %q", got)
	}
	if got := FallbackCode("no code"); got != "" {
		t.Errorf("no code gives %q", got)
	}
}

func TestRedactURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://beta.browser.lol/en/s?token=abc&lang=de#code=1":                "https://beta.browser.lol/en/s?token=…&lang=de#code=…",
		"https://automate.browserstack.com/builds/b/sessions/s?auth_token=f00d": "https://automate.browserstack.com/builds/b/sessions/s?auth_token=…",
		"https://example.test/x?ID=7&keep=1":                                    "https://example.test/x?ID=…&keep=1",
	} {
		if got := RedactURL(in); got != want {
			t.Errorf("RedactURL(%s) = %s", in, got)
		}
	}
}

func TestParseAccounts(t *testing.T) {
	got, err := ParseAccounts(" a_1:k1, b_2:k2 ,")
	if err != nil || !reflect.DeepEqual(got, []Account{{1, "a_1", "k1"}, {2, "b_2", "k2"}}) {
		t.Errorf("accounts %v: %v", got, err)
	}
	if a, err := ParseAccount("u:k:with:colons", 3); err != nil || a != (Account{3, "u", "k:with:colons"}) {
		t.Errorf("colons: %v %v", a, err)
	}
	for value, want := range map[string]string{"": "no account", "a:k,b": "entry 2", "a:": "entry 1", ":k": "entry 1"} {
		if _, err := ParseAccounts(value); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseAccounts(%q): %v, want %q", value, err, want)
		}
	}
}

func TestBusyMembers(t *testing.T) {
	accounts, _ := ParseAccounts("a:1,b:2,c:3,d:4")
	plans := []*Plan{{Running: 1, Max: 1}, {Running: 0, Max: 1}, nil, {Running: 2, Max: 2, Queued: 3}}
	if got := BusyMembers(accounts, plans); !reflect.DeepEqual(got, []int{1, 4}) {
		t.Errorf("busy %v", got)
	}
}

func TestDevices(t *testing.T) {
	if len(Devices) != 11 {
		t.Fatalf("%d devices", len(Devices))
	}
	d, err := DeviceByID("pixel")
	if err != nil || d.Kind != Android || d.Options["deviceName"] != "Google Pixel 9" {
		t.Errorf("pixel: %+v %v", d, err)
	}
	if _, err := DeviceByID("nokia"); err == nil || !strings.HasPrefix(err.Error(), "unknown device nokia (known: iphone, iphone-old, ipad,") {
		t.Errorf("unknown device: %v", err)
	}
}

func TestURL(t *testing.T) {
	locales := []string{"en", "de", "fr", "es", "it", "pt", "ja"}
	s := &Session{cfg: Config{BaseURL: "https://beta.browser.lol", Locale: "es", Locales: locales}}
	for path, want := range map[string]string{
		"/create":                  "https://beta.browser.lol/es/create",
		"create":                   "https://beta.browser.lol/es/create",
		"/":                        "https://beta.browser.lol/es",
		"/de/create":               "https://beta.browser.lol/de/create",
		"/de":                      "https://beta.browser.lol/de",
		"/dev":                     "https://beta.browser.lol/es/dev",
		"/_next/x":                 "https://beta.browser.lol/_next/x",
		"https://example.test/a?b": "https://example.test/a?b",
	} {
		if got, err := s.url(path); err != nil || got != want {
			t.Errorf("url(%s) = %s, %v; want %s", path, got, err, want)
		}
	}
	s.cfg.Locale = ""
	if got, _ := s.url("/create"); got != "https://beta.browser.lol/create" {
		t.Errorf("without a locale: %s", got)
	}
	s.cfg.BaseURL = ""
	if _, err := s.url("/create"); err == nil || !strings.Contains(err.Error(), "full URL") {
		t.Errorf("a path without a base URL: %v", err)
	}
	if got, err := s.url("http://localhost:3000/x"); err != nil || got != "http://localhost:3000/x" {
		t.Errorf("a full URL without a base URL: %s %v", got, err)
	}
}

func TestPreview(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 1080, 2400))
	for i := range img.Pix {
		img.Pix[i] = 200
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	out, err := Preview(buf.Bytes(), 800)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width != 360 || cfg.Height != 800 {
		t.Errorf("preview is %dx%d, want 360x800", cfg.Width, cfg.Height)
	}
	small := image.NewNRGBA(image.Rect(0, 0, 100, 50))
	buf.Reset()
	_ = png.Encode(&buf, small)
	out, _ = Preview(buf.Bytes(), 800)
	if cfg, _ := jpeg.DecodeConfig(bytes.NewReader(out)); cfg.Width != 100 || cfg.Height != 50 {
		t.Errorf("a small image changed size: %dx%d", cfg.Width, cfg.Height)
	}
}

func TestDescribe(t *testing.T) {
	s := &Session{cfg: Config{Evidence: &Evidence{Dir: "/ev"}}, startedAt: time.Now().Add(-101 * time.Minute)}
	a := &Action{N: 4, Action: "tap", MS: 120, Error: "boom", Output: "tapped", Settle: &Settle{MS: 20000, Busy: []string{"div.skeleton"}},
		Consent: "accepted", PageErrors: []PageEntry{{Kind: "error", Message: "x"}}, URL: "https://a/b", Screenshot: "04-tap.png"}
	want := "action 4 tap 120 ms ERROR boom\ntapped\nnot settled after 20000 ms: div.skeleton\nconsent dialog: accepted\npage errors:\n  error: x\nurl: https://a/b\nscreenshot: /ev/04-tap.png\nWARNING: the session nears BrowserStack's 2 h limit; finish this cell soon"
	if got := s.Describe(a); got != want {
		t.Errorf("describe\n got %q\nwant %q", got, want)
	}
}

func TestDescribeTarget(t *testing.T) {
	nth := 1
	if got := describeTarget(Target{Role: "button", Text: `Go "now"`, CSS: "main", Nth: &nth, Native: true}); got != `button "Go \"now\"" css main #2 native` {
		t.Errorf("target %s", got)
	}
	if got := describeTarget(Target{}); got != "the focused field" {
		t.Errorf("empty target %s", got)
	}
}
