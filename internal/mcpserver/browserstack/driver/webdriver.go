package driver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// elementKey is the W3C element reference key; Appium's Safari driver still
// answers the legacy JSON wire key ELEMENT.
const elementKey = "element-6066-11e4-a52e-4f735466cecf"

// WebDriverError is an error the hub answered: its code and the first line of
// its message.
type WebDriverError struct {
	Code    string
	Message string
}

func newWebDriverError(code, message string) *WebDriverError {
	first, _, _ := strings.Cut(message, "\n")
	return &WebDriverError{Code: code, Message: truncate(first, 400)}
}

func (e *WebDriverError) Error() string { return e.Code + ": " + e.Message }

// truncate cuts s to at most n UTF-16 code units' worth of runes (JavaScript's
// slice counts those; runes are close enough).
func truncate(s string, n int) string {
	i := 0
	for at := range s {
		if i == n {
			return s[:at]
		}
		i++
	}
	return s
}

type rect struct {
	X      *float64 `json:"x"`
	Y      *float64 `json:"y"`
	Width  *float64 `json:"width"`
	Height *float64 `json:"height"`
}

func (r rect) values() (x, y, w, h float64, ok bool) {
	if r.X == nil || r.Y == nil || r.Width == nil || r.Height == nil {
		return 0, 0, 0, 0, false
	}
	return *r.X, *r.Y, *r.Width, *r.Height, true
}

// webDriver is a minimal W3C WebDriver client for BrowserStack's hub, plus the
// Appium endpoints real devices add.
type webDriver struct {
	sessionID string
	auth      string
}

var httpClient = &http.Client{}

func createWebDriver(ctx context.Context, a Account, capabilities map[string]any, timeout time.Duration) (*webDriver, error) {
	auth := basicAuth(a)
	body := map[string]any{"capabilities": map[string]any{"alwaysMatch": capabilities}}
	raw, err := request(ctx, auth, http.MethodPost, HubURL+"/session", body, timeout)
	if err != nil {
		return nil, err
	}
	var session struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(raw, &session)
	if session.SessionID == "" {
		return nil, newWebDriverError("session not created", truncate(compactJSON(raw), 400))
	}
	return &webDriver{sessionID: session.SessionID, auth: auth}, nil
}

// attachWebDriver is a handle on a session another process started, to close
// it when that process cannot.
func attachWebDriver(a Account, sessionID string) *webDriver {
	return &webDriver{sessionID: sessionID, auth: basicAuth(a)}
}

func (w *webDriver) call(ctx context.Context, method, path string, body any, timeout time.Duration) (json.RawMessage, error) {
	return request(ctx, w.auth, method, HubURL+"/session/"+w.sessionID+path, body, timeout)
}

// callInto calls and decodes the value into out.
func (w *webDriver) callInto(ctx context.Context, method, path string, body any, timeout time.Duration, out any) error {
	raw, err := w.call(ctx, method, path, body, timeout)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("unexpected answer to %s %s: %s", method, path, truncate(compactJSON(raw), 300))
	}
	return nil
}

var empty = map[string]any{}

func (w *webDriver) navigate(ctx context.Context, url string) error {
	return w.callInto(ctx, http.MethodPost, "/url", map[string]any{"url": url}, callTimeout, nil)
}

func (w *webDriver) currentURL(ctx context.Context) (string, error) {
	var url string
	err := w.callInto(ctx, http.MethodGet, "/url", nil, callTimeout, &url)
	return url, err
}

func (w *webDriver) back(ctx context.Context) error {
	return w.callInto(ctx, http.MethodPost, "/back", empty, callTimeout, nil)
}

func (w *webDriver) forward(ctx context.Context) error {
	return w.callInto(ctx, http.MethodPost, "/forward", empty, callTimeout, nil)
}

func (w *webDriver) refresh(ctx context.Context) error {
	return w.callInto(ctx, http.MethodPost, "/refresh", empty, callTimeout, nil)
}

func (w *webDriver) setScriptTimeout(ctx context.Context, d time.Duration) error {
	return w.callInto(ctx, http.MethodPost, "/timeouts", map[string]any{"script": d.Milliseconds()}, callTimeout, nil)
}

func (w *webDriver) windowRect(ctx context.Context) (rect, error) {
	var r rect
	err := w.callInto(ctx, http.MethodGet, "/window/rect", nil, callTimeout, &r)
	return r, err
}

func (w *webDriver) maximize(ctx context.Context) error {
	return w.callInto(ctx, http.MethodPost, "/window/maximize", empty, callTimeout, nil)
}

// run runs a page script (browser/*.js: a function main) and decodes its
// result into out (nil: dropped).
func (w *webDriver) run(ctx context.Context, out any, source string, args ...any) error {
	if args == nil {
		args = []any{}
	}
	script := source + "\nreturn main.apply(null, arguments);"
	return w.callInto(ctx, http.MethodPost, "/execute/sync", map[string]any{"script": script, "args": args}, executeTimeout, out)
}

// runRaw is run with the result as the hub sent it.
func (w *webDriver) runRaw(ctx context.Context, source string, args ...any) (json.RawMessage, error) {
	if args == nil {
		args = []any{}
	}
	script := source + "\nreturn main.apply(null, arguments);"
	return w.call(ctx, http.MethodPost, "/execute/sync", map[string]any{"script": script, "args": args}, executeTimeout)
}

// runAsync runs a page script whose main returns a promise, bounded by the
// session's script timeout.
func (w *webDriver) runAsync(ctx context.Context, out any, source string, args ...any) error {
	if args == nil {
		args = []any{}
	}
	script := source + "\nvar done = arguments[arguments.length - 1];\nmain.apply(null, Array.prototype.slice.call(arguments, 0, -1)).then(done, function (e) { done({ __error: String((e && e.message) || e) }); });"
	return w.callInto(ctx, http.MethodPost, "/execute/async", map[string]any{"script": script, "args": args}, executeTimeout, out)
}

func elementID(raw json.RawMessage) (string, error) {
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("unexpected element reference: %s", truncate(compactJSON(raw), 300))
	}
	if id, ok := value[elementKey].(string); ok {
		return id, nil
	}
	if id, ok := value["ELEMENT"].(string); ok {
		return id, nil
	}
	return "", fmt.Errorf("unexpected element reference: %s", truncate(compactJSON(raw), 300))
}

func (w *webDriver) find(ctx context.Context, css string) (string, error) {
	raw, err := w.call(ctx, http.MethodPost, "/element", map[string]any{"using": "css selector", "value": css}, callTimeout)
	if err != nil {
		return "", err
	}
	return elementID(raw)
}

func (w *webDriver) findXPath(ctx context.Context, xpath string) ([]string, error) {
	var values []json.RawMessage
	if err := w.callInto(ctx, http.MethodPost, "/elements", map[string]any{"using": "xpath", "value": xpath}, callTimeout, &values); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(values))
	for _, v := range values {
		id, err := elementID(v)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (w *webDriver) click(ctx context.Context, element string) error {
	return w.callInto(ctx, http.MethodPost, "/element/"+element+"/click", empty, callTimeout, nil)
}

func (w *webDriver) clear(ctx context.Context, element string) error {
	return w.callInto(ctx, http.MethodPost, "/element/"+element+"/clear", empty, callTimeout, nil)
}

func (w *webDriver) sendKeys(ctx context.Context, element, text string) error {
	return w.callInto(ctx, http.MethodPost, "/element/"+element+"/value", map[string]any{"text": text}, callTimeout, nil)
}

func (w *webDriver) activeElement(ctx context.Context) (string, error) {
	raw, err := w.call(ctx, http.MethodGet, "/element/active", nil, callTimeout)
	if err != nil {
		return "", err
	}
	return elementID(raw)
}

func (w *webDriver) rect(ctx context.Context, element string) (rect, error) {
	var r rect
	err := w.callInto(ctx, http.MethodGet, "/element/"+element+"/rect", nil, callTimeout, &r)
	return r, err
}

func (w *webDriver) actions(ctx context.Context, actions []map[string]any) error {
	if err := w.callInto(ctx, http.MethodPost, "/actions", map[string]any{"actions": actions}, callTimeout, nil); err != nil {
		return err
	}
	_, _ = w.call(ctx, http.MethodDelete, "/actions", nil, callTimeout)
	return nil
}

func (w *webDriver) screenshot(ctx context.Context) ([]byte, error) {
	var encoded string
	if err := w.callInto(ctx, http.MethodGet, "/screenshot", nil, callTimeout, &encoded); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(encoded)
}

// setOrientation rotates a real device (Appium).
func (w *webDriver) setOrientation(ctx context.Context, orientation string) error {
	return w.callInto(ctx, http.MethodPost, "/orientation", map[string]any{"orientation": orientation}, callTimeout, nil)
}

// deviceKeys types into whatever the device has focused, through its
// on-screen keyboard (Appium, native context).
func (w *webDriver) deviceKeys(ctx context.Context, text string) error {
	value := []string{}
	for _, r := range text {
		value = append(value, string(r))
	}
	return w.callInto(ctx, http.MethodPost, "/keys", map[string]any{"value": value}, callTimeout, nil)
}

// hideKeyboard closes the on-screen keyboard the platform's way (Appium).
func (w *webDriver) hideKeyboard(ctx context.Context) error {
	return w.callInto(ctx, http.MethodPost, "/appium/device/hide_keyboard", empty, callTimeout, nil)
}

func (w *webDriver) context(ctx context.Context) (string, error) {
	var name string
	err := w.callInto(ctx, http.MethodGet, "/context", nil, callTimeout, &name)
	return name, err
}

func (w *webDriver) setContext(ctx context.Context, name string) error {
	return w.callInto(ctx, http.MethodPost, "/context", map[string]any{"name": name}, callTimeout, nil)
}

func (w *webDriver) quit(ctx context.Context) error {
	return w.callInto(ctx, http.MethodDelete, "", nil, callTimeout, nil)
}

// request sends one WebDriver command and returns the answer's value.
func request(ctx context.Context, auth, method, url string, body any, timeout time.Duration) (json.RawMessage, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, method, url, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err == nil {
		defer resp.Body.Close()
	}
	var text []byte
	if err == nil {
		text, err = io.ReadAll(resp.Body)
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("timeout: BrowserStack did not answer within %d s", int(timeout.Seconds()))
		}
		return nil, fmt.Errorf("network: %v", err)
	}
	status := fmt.Sprintf("http %d", resp.StatusCode)
	var parsed struct {
		Value     json.RawMessage `json:"value"`
		SessionID string          `json:"sessionId"`
	}
	if err := json.Unmarshal(text, &parsed); err != nil {
		return nil, newWebDriverError(status, truncate(string(text), 300))
	}
	var problem struct {
		Error   *string `json:"error"`
		Message *string `json:"message"`
	}
	isObject := len(bytes.TrimSpace(parsed.Value)) > 0 && bytes.TrimSpace(parsed.Value)[0] == '{'
	if isObject {
		_ = json.Unmarshal(parsed.Value, &problem)
	}
	ok := resp.StatusCode >= 200 && resp.StatusCode <= 299
	if !ok || (problem.Error != nil && *problem.Error != "") {
		code, message := status, truncate(string(text), 300)
		if problem.Error != nil && *problem.Error != "" {
			code = *problem.Error
		}
		if problem.Message != nil {
			message = *problem.Message
		}
		return nil, newWebDriverError(code, message)
	}
	// The legacy JSON wire answer of a new session carries the id beside the
	// value.
	if parsed.SessionID != "" && isObject {
		var keys map[string]json.RawMessage
		if json.Unmarshal(parsed.Value, &keys) == nil {
			if _, has := keys["sessionId"]; !has {
				return json.Marshal(map[string]any{"sessionId": parsed.SessionID, "capabilities": parsed.Value})
			}
		}
	}
	if parsed.Value == nil {
		return json.RawMessage("null"), nil
	}
	return parsed.Value, nil
}

func compactJSON(raw []byte) string {
	var buf bytes.Buffer
	if json.Compact(&buf, raw) != nil {
		return string(raw)
	}
	return buf.String()
}
