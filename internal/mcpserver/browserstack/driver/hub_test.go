package driver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeHub is a WebDriver hub and BrowserStack API that answers the commands
// one session sends with canned values and records them.
type fakeHub struct {
	t        *testing.T
	mu       sync.Mutex
	calls    []string
	bodies   map[string][]map[string]any
	auth     string
	found    string // the find script's answer
	navigate []string
	shot     []byte // the screenshot's PNG; nil is a blank 4x4
	hit      string // marker.js "hide": where the native touch landed
}

func newFakeHub(t *testing.T) *fakeHub {
	h := &fakeHub{t: t, bodies: map[string][]map[string]any{}}
	h.found = `{"found":true,"nonce":"qa123","name":"Start session","index":0,"role":"button","tag":"button","exact":true,"count":1,"candidates":[],"disabled":false,"covered":null}`
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	oldHub, oldSessions, oldPlan := HubURL, SessionAPI, PlanAPI
	HubURL, SessionAPI, PlanAPI = server.URL+"/wd/hub", server.URL+"/sessions", server.URL+"/plan.json"
	t.Cleanup(func() { HubURL, SessionAPI, PlanAPI = oldHub, oldSessions, oldPlan })
	return h
}

func (h *fakeHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.auth = r.Header.Get("Authorization")
	path := strings.TrimPrefix(r.URL.Path, "/wd/hub")
	key := r.Method + " " + path
	h.calls = append(h.calls, key)
	var body map[string]any
	if data, _ := io.ReadAll(r.Body); len(data) > 0 {
		if err := json.Unmarshal(data, &body); err != nil {
			h.t.Errorf("%s: body %q", key, data)
		}
	}
	h.bodies[key] = append(h.bodies[key], body)
	value := func(v string) { _, _ = io.WriteString(w, `{"value":`+v+`}`) }
	switch {
	case key == "POST /session":
		// The legacy JSON wire answer: the id beside the value.
		_, _ = io.WriteString(w, `{"sessionId":"s1","status":0,"value":{"browserName":"chrome"}}`)
	case key == "POST /session/s1/url":
		h.navigate = append(h.navigate, body["url"].(string))
		value("null")
	case key == "GET /session/s1/url":
		value(`"https://beta.browser.lol/es/create?token=abc"`)
	case key == "POST /session/s1/execute/async":
		value(`{"ms":850,"settled":true,"busy":[],"readyState":"complete"}`)
	case key == "POST /session/s1/execute/sync":
		script := body["script"].(string)
		switch {
		case strings.Contains(script, "function main(policy)"):
			value(`{"open":true,"action":"accepted","label":"Consent"}`)
		case strings.Contains(script, "if (!window.__qa)"):
			value(`{"installed":false,"entries":[{"kind":"console.error","message":"boom","at":1}]}`)
		case strings.Contains(script, "var QA_INTERACTIVE"):
			value(h.found)
		case strings.Contains(script, "headings: pick("):
			value(`{"title":"Create","controls":[{"role":"button","name":"Go"}],"empty":[]}`)
		case strings.Contains(script, `var KEY = "qa-native-hit"`):
			if body["args"].([]any)[0] == "show" {
				value(`{"placed":true}`)
			} else {
				value(h.hit)
			}
		case strings.Contains(script, `command === "env"`):
			value(`{"ua":"UA","width":390}`)
		default:
			value("null")
		}
	case key == "POST /session/s1/element":
		value(`{"ELEMENT":"e1"}`)
	case key == "GET /session/s1/element/active":
		value(`{"element-6066-11e4-a52e-4f735466cecf":"e1"}`)
	case key == "POST /session/s1/element/e1/click":
		value("null")
	case key == "GET /session/s1/screenshot":
		shot := h.shot
		if shot == nil {
			var buf bytes.Buffer
			_ = png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 4, 4)))
			shot = buf.Bytes()
		}
		value(`"` + base64.StdEncoding.EncodeToString(shot) + `"`)
	case key == "GET /session/s1/context":
		value(`"CHROMIUM"`)
	case key == "GET /session/s1/window/rect":
		value(`{"x":0,"y":0,"width":412,"height":915}`)
	case key == "POST /session/s1/element/gone/click":
		w.WriteHeader(http.StatusNotFound)
		value(`{"error":"no such element","message":"gone\nstack trace"}`)
	case key == "DELETE /session/s1":
		value("null")
	case key == "GET /sessions/s1.json":
		_, _ = io.WriteString(w, `{"automation_session":{"browser_url":"https://automate.browserstack.com/s1","video_url":"https://v/s1.mp4","har_logs_url":"","logs":null,"public_url":"https://automate.browserstack.com/s1?auth_token=f00d"}}`)
	case key == "GET /plan.json":
		if r.Header.Get("Authorization") != basicAuth(Account{User: "u", Key: "k"}) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"parallel_sessions_running":1,"queued_sessions":2}`)
	default:
		value("null")
	}
}

func (h *fakeHub) count(key string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, c := range h.calls {
		if c == key {
			n++
		}
	}
	return n
}

func startFake(t *testing.T, h *fakeHub, device string) (*Session, *Evidence) {
	t.Helper()
	ev, err := NewEvidence(t.TempDir(), "browserstack")
	if err != nil {
		t.Fatal(err)
	}
	d, _ := DeviceByID(device)
	s, err := Start(context.Background(), Config{
		Device: d, Locale: "es", Consent: ConsentAccept, BaseURL: "https://beta.browser.lol",
		Locales: []string{"en", "es"}, Account: Account{Member: 2, User: "u", Key: "k"},
		Project: "P", Build: "B", Name: "N", Evidence: ev, Address: "qa{stamp}m{member}-beta@goog.re",
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, ev
}

func TestStartSendsCapabilities(t *testing.T) {
	h := newFakeHub(t)
	s, _ := startFake(t, h, "win-chrome")
	if s.ID() != "s1" {
		t.Errorf("session id %q", s.ID())
	}
	if h.auth != basicAuth(Account{User: "u", Key: "k"}) {
		t.Errorf("auth %q", h.auth)
	}
	caps := h.bodies["POST /session"][0]["capabilities"].(map[string]any)["alwaysMatch"].(map[string]any)
	got, _ := json.Marshal(caps)
	want := `{"browserName":"chrome","bstack:options":{"buildName":"B","consoleLogs":"errors","idleTimeout":300,"networkLogs":true,"os":"Windows","osVersion":"11","projectName":"P","resolution":"1920x1080","sessionName":"N","video":true},"pageLoadStrategy":"eager"}`
	if string(got) != want {
		t.Errorf("capabilities\n got %s\nwant %s", got, want)
	}
	if b := h.bodies["POST /session/s1/timeouts"]; len(b) != 1 || b[0]["script"] != float64(30000) {
		t.Errorf("script timeout %v", b)
	}
	if h.count("POST /session/s1/window/maximize") != 1 {
		t.Error("a desktop was not maximised")
	}
	if a := s.Address(); !strings.HasPrefix(a, "qa") || !strings.HasSuffix(a, "m2-beta@goog.re") || a != s.Address() {
		t.Errorf("address %q", a)
	}
}

func TestOpenSettlesAndRecords(t *testing.T) {
	h := newFakeHub(t)
	s, ev := startFake(t, h, "pixel")
	if h.count("POST /session/s1/window/maximize") != 0 {
		t.Error("a phone was maximised")
	}
	a := s.Open(context.Background(), "/create?token=secret")
	if a.Error != "" {
		t.Fatalf("open failed: %s", a.Error)
	}
	if len(h.navigate) != 1 || h.navigate[0] != "https://beta.browser.lol/es/create?token=secret" {
		t.Errorf("navigated to %v", h.navigate)
	}
	if a.N != 1 || a.Summary != "opened /create?token=…" || a.URL != "https://beta.browser.lol/es/create?token=…" {
		t.Errorf("action %+v", a)
	}
	if a.Consent != "accepted" || a.Settle == nil || !a.Settle.Settled || a.Settle.MS != 850 {
		t.Errorf("consent %q settle %+v", a.Consent, a.Settle)
	}
	// The consent answer settles the page again.
	if n := h.count("POST /session/s1/execute/async"); n != 2 {
		t.Errorf("settled %d times", n)
	}
	if len(a.PageErrors) != 1 || a.PageErrors[0].Message != "boom" {
		t.Errorf("page errors %+v", a.PageErrors)
	}
	if a.Screenshot != "01-open.png" {
		t.Errorf("screenshot %q", a.Screenshot)
	}
	if _, err := os.Stat(filepath.Join(ev.Dir, "01-open.png")); err != nil {
		t.Error(err)
	}
	lines := readLines(t, ev.Dir)
	if len(lines) != 1 || lines[0]["action"] != "open" || lines[0]["tool"] != "browserstack" {
		t.Errorf("evidence %v", lines)
	}
	text := s.Describe(a)
	want := "action 1 open " // the time varies
	if !strings.HasPrefix(text, want) || !strings.Contains(text, "consent dialog: accepted\npage errors:\n  console.error: boom\nurl: https://beta.browser.lol/es/create?token=…\nscreenshot: "+filepath.Join(ev.Dir, "01-open.png")) {
		t.Errorf("describe %q", text)
	}
	if strings.Contains(text, "WARNING") {
		t.Error("a fresh session warns")
	}
}

func TestTapFindsAndClicks(t *testing.T) {
	h := newFakeHub(t)
	s, _ := startFake(t, h, "pixel")
	a := s.Tap(context.Background(), Target{Text: "start session", Role: "button"}, false)
	if a.Error != "" || a.Summary != `tapped button "Start session"` {
		t.Fatalf("tap %+v", a)
	}
	if a.Target == nil || a.Target.Matched != "Start session" || a.Target.Text != "start session" {
		t.Errorf("target %+v", a.Target)
	}
	if b := h.bodies["POST /session/s1/element"]; len(b) != 1 || b[0]["value"] != `[data-qa-hit="qa123"]` || b[0]["using"] != "css selector" {
		t.Errorf("element lookup %v", b)
	}
	if h.count("POST /session/s1/element/e1/click") != 1 {
		t.Error("not clicked")
	}
	query := h.bodies["POST /session/s1/execute/sync"]
	var findArgs []any
	for _, b := range query {
		if strings.Contains(b["script"].(string), "var QA_INTERACTIVE") {
			findArgs = b["args"].([]any)
		}
	}
	if got, _ := json.Marshal(findArgs); string(got) != `[{"role":"button","text":"start session"}]` {
		t.Errorf("find query %s", got)
	}

	// A covered control is refused without force, and a miss lists what is
	// similar.
	h.found = `{"found":true,"nonce":"qa9","name":"Go","index":1,"role":"link","count":2,"candidates":[{"name":"Go","role":"link"},{"name":"Go on","role":"button"}],"covered":{"by":"div#ad","ad":true,"consent":false}}`
	a = s.Tap(context.Background(), Target{Text: "go"}, false)
	if a.Error != `"Go" is covered by div#ad (an ad); use --force to tap anyway` {
		t.Errorf("covered: %q", a.Error)
	}
	a = s.Tap(context.Background(), Target{Text: "go"}, true)
	if a.Error != "" || a.Output != "tapped link \"Go\"\n2 matches, took #2 (--nth): \"Go\", \"Go on\"" {
		t.Errorf("forced: %+v", a)
	}
	h.found = `{"found":false,"count":0,"candidates":[{"name":"Gone","role":"button"}]}`
	a = s.Tap(context.Background(), Target{Text: "go"}, false)
	if a.Error != `no control "go"; similar: button "Gone"` || a.Screenshot == "" {
		t.Errorf("miss: %+v", a)
	}
}

func TestLookAndShot(t *testing.T) {
	h := newFakeHub(t)
	s, _ := startFake(t, h, "pixel")
	a := s.Look(context.Background())
	if a.Output != "{\n \"title\": \"Create\",\n \"controls\": [\n  {\n   \"role\": \"button\",\n   \"name\": \"Go\"\n  }\n ],\n \"empty\": []\n}" || a.Summary != "{" {
		t.Errorf("look %q", a.Output)
	}
	if a.Settle != nil || h.count("POST /session/s1/execute/async") != 0 {
		t.Error("look settled")
	}
	shot := s.Shot(context.Background())
	if shot.Summary != "took a screenshot" || shot.Screenshot != "02-shot.png" || shot.URL != "" {
		t.Errorf("shot %+v", shot)
	}
	if _, err := s.Key(context.Background(), "f5"); err == nil || err.Error() != "unknown key f5 (enter, tab, escape, backspace, up, down, left, right, space)" {
		t.Errorf("unknown key: %v", err)
	}
	env, err := s.Env(context.Background())
	if err != nil || env["ua"] != "UA" {
		t.Errorf("env %v %v", env, err)
	}
}

func TestWebDriverErrors(t *testing.T) {
	h := newFakeHub(t)
	s, _ := startFake(t, h, "pixel")
	err := s.wd.click(context.Background(), "gone")
	var wdErr *WebDriverError
	if !errors.As(err, &wdErr) || err.Error() != "no such element: gone" {
		t.Errorf("error %v", err)
	}
}

func TestStopAndLinks(t *testing.T) {
	h := newFakeHub(t)
	s, _ := startFake(t, h, "pixel")
	links := s.Stop(context.Background())
	if h.count("DELETE /session/s1") != 1 {
		t.Error("not quit")
	}
	want := map[string]string{
		"BrowserStack session": "https://automate.browserstack.com/s1",
		"Video":                "https://v/s1.mp4",
		"Public link":          "https://automate.browserstack.com/s1?auth_token=…",
	}
	if got, _ := json.Marshal(links); string(got) != mustJSON(want) {
		t.Errorf("links %s", got)
	}
	if err := QuitSession(context.Background(), Account{User: "u", Key: "k"}, "s1"); err != nil || h.count("DELETE /session/s1") != 2 {
		t.Errorf("QuitSession: %v", err)
	}
}

func mustJSON(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}

func TestPlanOf(t *testing.T) {
	newFakeHub(t)
	plan, err := PlanOf(context.Background(), Account{User: "u", Key: "k"})
	if err != nil || *plan != (Plan{Running: 1, Max: 1, Queued: 2}) {
		t.Errorf("plan %+v %v", plan, err)
	}
	if _, err := PlanOf(context.Background(), Account{User: "u", Key: "wrong"}); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("a refused key: %v", err)
	}
	PlanAPI = "http://127.0.0.1:1/plan.json"
	if _, err := PlanOf(context.Background(), Account{User: "u", Key: "k"}); err == nil || errors.Is(err, ErrUnauthorized) {
		t.Errorf("an unreachable API: %v", err)
	}
}

// fakeMail is a mailbox with one mail.
type fakeMail struct{ to string }

func (f *fakeMail) Read(_ context.Context, to string, since, limit int) ([]Mail, error) {
	f.to = to
	return []Mail{{Received: "2026-10-29T09:00:00", From: "a@b.c", To: to, Title: "Sign in", Code: "123456", Links: []string{"https://beta.browser.lol/en/auth?token=abc"}}}, nil
}

func TestMailAndMailLink(t *testing.T) {
	h := newFakeHub(t)
	s, _ := startFake(t, h, "pixel")
	box := &fakeMail{}
	s.cfg.Mail = box
	a := s.Mail(context.Background(), "", 0)
	if a.Error != "" || box.to != s.Address() || a.Screenshot != "" || a.Settle != nil {
		t.Fatalf("mail %+v (to %s)", a, box.to)
	}
	want := "mail 2026-10-29T09:00:00 from a@b.c to " + s.Address() + "\nsubject: Sign in\ncode: 123456\nlinks (open with qa open --mail-link <n>):\n  [1] https://beta.browser.lol/en/auth?token=…"
	if a.Output != want {
		t.Errorf("mail output %q", a.Output)
	}
	open := s.OpenMailLink(context.Background(), 1)
	if open.Error != "" || open.Summary != "opened https://beta.browser.lol/en/auth?token=…" || h.navigate[0] != "https://beta.browser.lol/en/auth?token=abc" {
		t.Errorf("mail link %+v", open)
	}
	if a := s.OpenMailLink(context.Background(), 2); a.Error != "the last mail has no link 2 (read one with qa mail first)" {
		t.Errorf("missing link: %q", a.Error)
	}
	s.cfg.Mail = nil
	if a := s.Mail(context.Background(), "x@goog.re", 0); a.Error != errNoMailbox.Error() {
		t.Errorf("no mailbox: %q", a.Error)
	}
}

func TestTypeSecret(t *testing.T) {
	h := newFakeHub(t)
	s, ev := startFake(t, h, "pixel")
	s.cfg.Secrets = map[string]string{"QA_GOOGLE_PASSWORD": "hunter2"}
	a := s.Type(context.Background(), "", TypeOptions{Into: &Target{Text: "Password"}, Secret: "QA_GOOGLE_PASSWORD", Submit: true})
	if a.Error != "" || a.Summary != "typed <secret QA_GOOGLE_PASSWORD> via WebDriver" {
		t.Fatalf("type %+v", a)
	}
	sent := h.bodies["POST /session/s1/element/e1/value"]
	if len(sent) != 2 || sent[0]["text"] != "hunter2" || sent[1]["text"] != "" {
		t.Errorf("sent %v", sent)
	}
	data, _ := os.ReadFile(ev.ActionsFile())
	if strings.Contains(string(data), "hunter2") {
		t.Error("the secret is in the evidence")
	}
	if a := s.Type(context.Background(), "", TypeOptions{Secret: "QA_NONE"}); a.Error != "secret QA_NONE is not in the vault" {
		t.Errorf("missing secret: %q", a.Error)
	}
	plain := s.Type(context.Background(), "hello", TypeOptions{})
	if plain.Error != "" || plain.Summary != "typed hello via WebDriver" {
		t.Errorf("plain %+v", plain)
	}
}

func TestNativeTap(t *testing.T) {
	h := newFakeHub(t)
	s, _ := startFake(t, h, "pixel")
	h.shot = screen(t, [][2]int{{0, 323}, {263, 586}}, nil)
	h.hit = `{"on":true,"by":"button","name":"Start session"}`
	a := s.Tap(context.Background(), Target{Text: "start", Native: true}, false)
	if a.Error != "" {
		t.Fatalf("native tap: %s", a.Error)
	}
	acts := h.bodies["POST /session/s1/actions"]
	if len(acts) != 1 {
		t.Fatalf("actions %v", acts)
	}
	move := acts[0]["actions"].([]any)[0].(map[string]any)["actions"].([]any)[0].(map[string]any)
	// The markers' midpoint (131.5, 454.5) in a 1080 px screenshot of a 412 pt window.
	if move["x"] != float64(50) || move["y"] != float64(173) {
		t.Errorf("touched %v", move)
	}
	contexts := h.bodies["POST /session/s1/context"]
	if len(contexts) != 2 || contexts[0]["name"] != "NATIVE_APP" || contexts[1]["name"] != "CHROMIUM" {
		t.Errorf("contexts %v", contexts)
	}
	if h.count("POST /session/s1/element/e1/click") != 0 {
		t.Error("a native tap clicked through WebDriver")
	}

	h.hit = `{"on":false,"by":"div.ad","name":"Ad"}`
	if a := s.Tap(context.Background(), Target{Text: "start", Native: true}, false); a.Error != `native: the touch landed on div.ad "Ad", not on "Start session"` {
		t.Errorf("a missed touch: %q", a.Error)
	}
	h.shot = nil
	if a := s.Tap(context.Background(), Target{Text: "start", Native: true}, false); !strings.HasPrefix(a.Error, `native: "Start session" is not visible on the screen`) {
		t.Errorf("no markers: %q", a.Error)
	}
}
