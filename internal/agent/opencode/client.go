//go:build !windows

package opencode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// requestWait bounds every call but the long ones (a slash command, a shell
// line and a summary run until they are done, and run off the host's
// command path).
const requestWait = 30 * time.Second

// client talks to one serve process: Basic auth on every request, and the
// chat's working directory as the instance every call is about.
type client struct {
	base string
	pass string
	dir  string
	hc   *http.Client
}

func newClient(base, password, dir string) *client {
	return &client{base: base, pass: password, dir: dir, hc: &http.Client{}}
}

// apiError is a call OpenCode refused.
type apiError struct {
	Status  int
	Name    string
	Message string
}

func (e *apiError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("opencode: %d %s: %s", e.Status, e.Name, e.Message)
	}
	return fmt.Sprintf("opencode: HTTP %d %s", e.Status, e.Name)
}

func isStatus(err error, status int) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.Status == status
}

func (c *client) url(path string) string {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return c.base + path + sep + "directory=" + url.QueryEscape(c.dir)
}

func (c *client) request(ctx context.Context, method, path string, body any) (*http.Request, error) {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.url(path), rd)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(serverUser, c.pass)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// call does one request and decodes a JSON answer into out (nil: ignored).
// An answer that is not JSON is an error: OpenCode serves its web UI's HTML
// with 200 for any path it has no route for (research/05 §5).
func (c *client) call(ctx context.Context, method, path string, body, out any) error {
	if _, has := ctx.Deadline(); !has {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, requestWait)
		defer cancel()
	}
	req, err := c.request(ctx, method, path, body)
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	isJSON := strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json")
	if resp.StatusCode >= 300 {
		ae := &apiError{Status: resp.StatusCode}
		var named struct {
			Name string `json:"name"`
			Tag  string `json:"_tag"`
			Msg  string `json:"message"`
			Data struct {
				Message string `json:"message"`
			} `json:"data"`
		}
		if isJSON && json.Unmarshal(raw, &named) == nil {
			ae.Name, ae.Message = named.Name, named.Data.Message
			if ae.Name == "" {
				ae.Name, ae.Message = named.Tag, named.Msg
			}
		}
		return ae
	}
	if !isJSON {
		return fmt.Errorf("opencode: %s %s answered %q, not JSON", method, path, resp.Header.Get("Content-Type"))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// events opens /event. The stream has no replay (research/05 §2.2): what
// happens while it is not open is read back with GET …/message.
func (c *client) events(ctx context.Context) (io.ReadCloser, error) {
	req, err := c.request(ctx, http.MethodGet, "/event", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		resp.Body.Close()
		return nil, fmt.Errorf("opencode: /event answered %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	return resp.Body, nil
}

// readSSE calls fn with the data of every event on r until r ends. A frame's
// data may span several data: lines; the other SSE fields are not used.
func readSSE(r io.Reader, fn func(data []byte)) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var data []byte
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			line = bytes.TrimRight(line, "\r\n")
			switch {
			case len(line) == 0:
				if len(data) > 0 {
					fn(data)
					data = nil
				}
			case bytes.HasPrefix(line, []byte("data:")):
				chunk := bytes.TrimPrefix(bytes.TrimPrefix(line, []byte("data:")), []byte(" "))
				if len(data) > 0 {
					data = append(data, '\n')
				}
				data = append(data, chunk...)
			}
		}
		if err != nil {
			if len(data) > 0 {
				fn(data)
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// validID is an OpenCode id with the given prefix. Ids go into paths, and a
// path OpenCode has no route for is not an error but its web UI.
func validID(id, prefix string) bool {
	if !strings.HasPrefix(id, prefix+"_") || len(id) > 64 {
		return false
	}
	for _, r := range id[len(prefix)+1:] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return len(id) > len(prefix)+1
}
