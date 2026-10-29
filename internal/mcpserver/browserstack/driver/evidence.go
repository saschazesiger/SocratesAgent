package driver

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Target is how an action named its control, and what it hit.
type Target struct {
	Text string `json:"text,omitempty"`
	Role string `json:"role,omitempty"`
	CSS  string `json:"css,omitempty"`
	// Nth picks among the matches, from 0.
	Nth    *int `json:"nth,omitempty"`
	Exact  bool `json:"exact,omitempty"`
	Any    bool `json:"any,omitempty"`
	Native bool `json:"native,omitempty"`
	// Matched is the accessible name of the control that was hit.
	Matched string `json:"matched,omitempty"`
}

// PageEntry is a page error, rejection or console line the collector saw.
type PageEntry struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	At      int64  `json:"at,omitempty"`
}

// Settle is how the page came to rest after an action.
type Settle struct {
	MS      int64    `json:"ms"`
	Settled bool     `json:"settled"`
	Busy    []string `json:"busy"`
}

// Action is one driver command on the device (open, tap, type, look, ...)
// with what followed it.
type Action struct {
	N      int    `json:"n"`
	At     string `json:"at"`
	Action string `json:"action"`
	// Summary is one line on what was done, never a secret's value.
	Summary    string      `json:"summary"`
	MS         int64       `json:"ms"`
	Target     *Target     `json:"target,omitempty"`
	URL        string      `json:"url,omitempty"`
	Screenshot string      `json:"screenshot,omitempty"`
	Settle     *Settle     `json:"settle,omitempty"`
	Consent    string      `json:"consent,omitempty"`
	PageErrors []PageEntry `json:"pageErrors,omitempty"`
	Output     string      `json:"output,omitempty"`
	Error      string      `json:"error,omitempty"`
}

// EvidenceDetails is the details object of an evidence line.
type EvidenceDetails struct {
	Target     *Target     `json:"target,omitempty"`
	Settle     *Settle     `json:"settle,omitempty"`
	Consent    string      `json:"consent,omitempty"`
	PageErrors []PageEntry `json:"pageErrors,omitempty"`
	Output     string      `json:"output,omitempty"`
}

// EvidenceLine is one line of actions.jsonl, the evidence Socrates reads.
type EvidenceLine struct {
	At          string          `json:"at"`
	Tool        string          `json:"tool"`
	Action      string          `json:"action"`
	Summary     string          `json:"summary"`
	URL         *string         `json:"url"`
	Screenshot  *string         `json:"screenshot"`
	MS          int64           `json:"ms"`
	Error       *string         `json:"error"`
	Interaction bool            `json:"interaction"`
	Details     EvidenceDetails `json:"details"`
}

// interactions are the actions that act on the page as a person would;
// Socrates' review rule reads the flag.
var interactions = map[string]bool{"tap": true, "type": true, "key": true}

// secretParams are query values that carry credentials (magic links, OAuth,
// share links, BrowserStack's public link); they are cut from every recorded
// URL.
var secretParams = regexp.MustCompile(`(?i)([?&#](?:token|auth_token|code|key|auth|state|sig|signature|secret|password|t|link|vm|id)=)[^&#]*`)

// RedactURL cuts the values of credential parameters from a URL.
func RedactURL(u string) string {
	return secretParams.ReplaceAllString(u, "${1}…")
}

// Evidence is an attempt's evidence folder: one JSON line per action in
// actions.jsonl with its screenshot beside it, the BrowserStack session's
// links in links.json and logs in logs/*.txt.
type Evidence struct {
	Dir  string
	tool string

	mu    sync.Mutex
	count int
}

// NewEvidence opens the folder (creating it) for the tool named in every
// line; numbering continues after the actions already in it, so no
// screenshot is overwritten.
func NewEvidence(dir, tool string) (*Evidence, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	e := &Evidence{Dir: dir, tool: tool}
	f, err := os.Open(e.ActionsFile())
	if err == nil {
		defer f.Close()
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), 64*1024*1024)
		for scanner.Scan() {
			if len(scanner.Bytes()) > 0 {
				e.count++
			}
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return e, nil
}

// ActionsFile is the path of actions.jsonl.
func (e *Evidence) ActionsFile() string { return filepath.Join(e.Dir, "actions.jsonl") }

// Next returns the number of the next action.
func (e *Evidence) Next() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.count++
	return e.count
}

// ScreenshotName is the file name of an action's screenshot.
func (e *Evidence) ScreenshotName(a *Action) string {
	return fmt.Sprintf("%02d-%s.png", a.N, a.Action)
}

// WriteScreenshot writes the action's screenshot and notes its name in the
// action.
func (e *Evidence) WriteScreenshot(a *Action, png []byte) error {
	name := e.ScreenshotName(a)
	if err := os.WriteFile(filepath.Join(e.Dir, name), png, 0o644); err != nil {
		return err
	}
	a.Screenshot = name
	return nil
}

// Line is the evidence line of an action.
func (e *Evidence) Line(a *Action) EvidenceLine {
	line := EvidenceLine{
		At:          a.At,
		Tool:        e.tool,
		Action:      a.Action,
		Summary:     a.Summary,
		MS:          a.MS,
		Interaction: interactions[a.Action],
		Details: EvidenceDetails{
			Target:     a.Target,
			Settle:     a.Settle,
			Consent:    a.Consent,
			PageErrors: a.PageErrors,
			Output:     a.Output,
		},
	}
	if a.URL != "" {
		u := RedactURL(a.URL)
		line.URL = &u
	}
	if a.Screenshot != "" {
		s := a.Screenshot
		line.Screenshot = &s
	}
	if a.Error != "" {
		s := a.Error
		line.Error = &s
	}
	return line
}

// Append adds the action's line to actions.jsonl.
func (e *Evidence) Append(a *Action) error {
	data, err := marshalJSON(e.Line(a), "")
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	f, err := os.OpenFile(e.ActionsFile(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// WriteLinks writes links.json: label to URL, empty values left out.
func (e *Evidence) WriteLinks(links map[string]string) error {
	kept := map[string]string{}
	for label, url := range links {
		if url != "" {
			kept[label] = url
		}
	}
	data, err := marshalJSON(kept, " ")
	if err != nil {
		return err
	}
	file := filepath.Join(e.Dir, "links.json")
	if err := os.WriteFile(file+".tmp", data, 0o644); err != nil {
		return err
	}
	return os.Rename(file+".tmp", file)
}

// WriteLog writes logs/<name>.txt, shown on the cell page, and returns its
// path.
func (e *Evidence) WriteLog(name string, lines []string) (string, error) {
	dir := filepath.Join(e.Dir, "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	file := filepath.Join(dir, name+".txt")
	text := ""
	if len(lines) > 0 {
		text = strings.Join(lines, "\n") + "\n"
	}
	return file, os.WriteFile(file, []byte(text), 0o644)
}

// marshalJSON encodes like JSON.stringify (no HTML escaping), indented by
// indent when it is not "", with a final newline.
func marshalJSON(v any, indent string) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if indent != "" {
		enc.SetIndent("", indent)
	}
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// jsString is JSON.stringify of a string.
func jsString(s string) string {
	data, _ := marshalJSON(s, "")
	return strings.TrimSuffix(string(data), "\n")
}
