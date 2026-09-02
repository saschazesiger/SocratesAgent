package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/catalog"
	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
	"github.com/saschazesiger/SocratesAgent/internal/openrouter"
	"github.com/saschazesiger/SocratesAgent/internal/piper"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
	"github.com/saschazesiger/SocratesAgent/internal/tunnel"
)

func orLocal(url string) string {
	if url == "" {
		return "http://127.0.0.1:8080"
	}
	return url
}

// handlePreferences exposes the few settings the session page needs at
// runtime. They are the ones the browser applies to itself - how the terminal
// is drawn, and how the voice behaves - rather than the whole document, which
// only the dashboard is allowed to read.
func (s *Server) handlePreferences(w http.ResponseWriter, r *http.Request) {
	settings := s.Settings()
	writeJSON(w, http.StatusOK, map[string]any{
		"speak_in_auto_mode": settings.Voice.SpeakInAutoMode,
		"speak_in_chat_mode": settings.Voice.SpeakInChatMode,
		"language":           settings.Voice.Language,
		"terminal": map[string]any{
			"scrollback": settings.Terminal.Scrollback,
			"font_size":  settings.Terminal.FontSize,
			"webgl":      settings.Terminal.WebGL,
		},
	})
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	settings := s.Settings()
	writeJSON(w, http.StatusOK, map[string]any{
		"settings":  settings,
		"defaults":  config.Default(),
		"version":   Version,
		"local_url": s.LocalURL(),
	})
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	// The decode starts from the live document, not from a zero value. A
	// section the request leaves out - an older dashboard, a curl call, a
	// field this page has never heard of - keeps what it has; decoded into a
	// zero value it would arrive with every switch off, and Normalize does not
	// put switches back. That is how Shell would vanish from the picker and
	// Codex would come up blocked on its trust prompt, with nothing logged.
	body := struct {
		Settings config.Settings `json:"settings"`
	}{Settings: s.Settings()}
	if !readJSON(w, r, &body) {
		return
	}
	next := body.Settings
	next.Normalize()
	if err := next.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// The workspace root is the one path the server has to be able to use, so
	// it is refused here rather than saved and discovered later by somebody
	// pressing Start.
	if !filepath.IsAbs(next.Workspace.Root) {
		writeError(w, http.StatusBadRequest, "the workspace root has to be an absolute path")
		return
	}
	if err := writableDir(next.Workspace.Root); err != nil {
		writeError(w, http.StatusBadRequest, "the workspace root cannot be used: "+err.Error())
		return
	}
	previous := s.Settings().Terminal
	if err := s.saveSettings(next); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	saved := s.Settings()

	// A preset that does not exist is a warning and not a refusal: a mount
	// that is not up yet is the ordinary reason, and refusing the save would
	// take the other nine presets with it.
	warnings := []string{}
	for _, preset := range saved.Workspace.Presets {
		if _, err := os.Stat(preset.Path); err != nil {
			warnings = append(warnings, preset.Label+": "+preset.Path+" is not there yet")
		}
	}
	if terminalChanged(previous, saved.Terminal) {
		if err := s.applyTerminal(r.Context(), saved.Terminal); err != nil {
			warnings = append(warnings,
				"the terminal settings were saved, but tmux would not take them live: "+err.Error())
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": saved, "warnings": warnings})
}

// terminalChanged reports whether anything the tmux server holds has moved.
// The browser-side settings - the scrollback, the font, the renderer - are
// applied by the page itself and are no business of tmux's.
func terminalChanged(a, b config.TerminalSettings) bool {
	return a.WindowSize != b.WindowSize || a.HistoryLimit != b.HistoryLimit ||
		a.Mouse != b.Mouse || a.ExtendedKeys != b.ExtendedKeys
}

// applyTerminal rewrites the generated tmux configuration and applies live
// what tmux will take live. What it cannot apply - the terminal type, the
// pane that stays after its program exits - reaches the sessions started from
// now on, which is what the card's hint says.
func (s *Server) applyTerminal(ctx context.Context, terminal config.TerminalSettings) error {
	return s.manager.ApplyTerminal(ctx, termux.ConfOptions{
		HistoryLimit: terminal.HistoryLimit,
		Mouse:        terminal.Mouse,
		ExtendedKeys: terminal.ExtendedKeys,
	}, terminal.WindowSize)
}

// checkResult is one row of the setup check. It is split in two because
// §E.10.3 is a rule about this list as much as about the session page: the
// verdict a person reads is short and plain, and the version, the path, the
// socket and the error text live behind the row's "i".
type checkResult struct {
	Name string `json:"name"`
	OK   bool   `json:"ok"`
	// Summary is the visible half: three or four words, no paths, no
	// versions, no stderr.
	Summary string `json:"summary"`
	// Detail is the hover-only half. It may be empty, and then the row has no
	// "i" at all.
	Detail string `json:"detail"`
}

// handleDiagnostics powers the "check my setup" button in the admin
// dashboard. It is §F.7's list, in its order: the terminal engine first,
// because nothing works without it, then the place sessions run, then the four
// programs and the credentials each of them needs, and finally the disk they
// all write to.
func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	settings := s.Settings()
	results := []checkResult{}
	add := func(name string, ok bool, summary, detail string) {
		results = append(results, checkResult{Name: name, OK: ok, Summary: summary, Detail: detail})
	}

	results = append(results, s.tmuxChecks(r.Context())...)

	// Where sessions work. Creating it is the check: a root that cannot be
	// made is a new-session sheet that fails on Start.
	root := settings.Workspace.Root
	if err := writableDir(root); err != nil {
		add("Workspace", false, "not writable", err.Error())
	} else {
		add("Workspace", true, "writable", root)
	}

	// The four programs, each with the state directory it cannot work without.
	snapshot := s.catalog.Get(r.Context())
	for _, id := range config.KnownHarnesses {
		agent, ok := snapshot.Agent(id)
		if !ok {
			continue
		}
		results = append(results, harnessCheck(agent))
		if extra, ok := harnessStateCheck(id); ok {
			results = append(results, extra)
		}
	}

	// OpenRouter. Since Socrates became a harness this key answers nothing
	// except dictation and chat titles, so its row and the transcription row
	// below are one story told twice.
	openrouterOK := false
	if strings.TrimSpace(settings.OpenRouter.APIKey) == "" {
		add("OpenRouter", false, "no API key", "")
	} else {
		client := openrouter.New(settings.OpenRouter.BaseURL, settings.OpenRouter.APIKey)
		if info, err := client.CheckKey(r.Context()); err != nil {
			add("OpenRouter", false, "the key was refused", err.Error())
		} else {
			openrouterOK = true
			add("OpenRouter", true, "the key works", info.Label)
		}
	}
	transcribe := strings.TrimSpace(settings.OpenRouter.TranscribeModel)
	switch {
	case !openrouterOK:
		add("Speech to text", false, "waiting on OpenRouter",
			"it listens through OpenRouter, so that check has to pass first")
	case transcribe == "":
		add("Speech to text", false, "no model picked", "")
	default:
		add("Speech to text", true, "ready", "OpenRouter · "+transcribe)
	}
	results = append(results, voiceCheck(s.voice.Status()))

	// Remote access, and then the disk everything above writes to.
	results = append(results, s.tunnelCheck(settings))
	results = append(results, diskCheck(s.dataDir))

	writeJSON(w, http.StatusOK, map[string]any{"checks": results})
}

// tmuxChecks are the terminal engine's three rows: the binary and its version,
// the socket this Socrates owns, and how many sessions are on it.
func (s *Server) tmuxChecks(ctx context.Context) []checkResult {
	report := s.tmuxAdmin.detect(ctx, false)
	engine := checkResult{Name: "tmux", OK: report.OK}
	switch {
	case report.OK:
		engine.Summary = "ready"
		engine.Detail = clean(report.Version) + " · " + report.Path
	case report.Installed:
		engine.Summary = "too old"
		engine.Detail = report.Reason
	default:
		engine.Summary = "not installed"
		engine.Detail = report.Reason
	}
	out := []checkResult{engine}

	socket := checkResult{Name: "tmux socket", Detail: s.manager.Socket()}
	if err := s.manager.Available(); err != nil {
		socket.Summary = "no server"
		socket.Detail = err.Error()
		return append(out, socket)
	}
	names, err := s.manager.LiveSessionNames(ctx)
	if err != nil {
		socket.Summary = "unreachable"
		socket.Detail = s.manager.Socket() + " · " + err.Error()
		return append(out, socket)
	}
	socket.OK = true
	socket.Summary = fmt.Sprintf("%d session%s", len(names), plural(len(names)))
	socket.Detail = s.manager.Socket()
	return append(out, socket)
}

// harnessCheck is one program: is it here, which build, and can its model list
// be reached at all.
func harnessCheck(agent catalog.Agent) checkResult {
	row := checkResult{Name: agent.Label}
	switch {
	case !agent.Enabled:
		row.OK = true
		row.Summary = "turned off"
	case !agent.Installed:
		row.Summary = "not installed"
		row.Detail = agent.Error
	default:
		row.OK = true
		row.Summary = "ready"
		row.Detail = clean(agent.Version)
		if agent.Path != "" {
			row.Detail += " · " + agent.Path
		}
		if agent.ID == config.HarnessShell {
			break
		}
		count := len(agent.Models)
		switch {
		case agent.Error != "":
			row.OK = false
			row.Summary = "no model list"
			row.Detail += " · " + agent.Error
		case count == 0:
			row.OK = false
			row.Summary = "no models reported"
		default:
			row.Summary = fmt.Sprintf("ready, %d model%s", count, plural(count))
			if agent.Static {
				row.Summary += " (curated)"
			}
		}
	}
	return row
}

// clean is what stands between a program's idea of `--version` and this page.
// A CLI that prints a colour query or an escape sequence into its version -
// which the suite's own fake does - would otherwise put raw escapes into the
// dashboard, and a very long line into every row it appears in.
func clean(text string) string {
	text = strings.TrimSpace(StripANSI(text))
	text = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, text)
	if len(text) > 120 {
		text = strings.TrimSpace(text[:120]) + "…"
	}
	return text
}

// harnessStateCheck is the one directory or file each coding agent keeps its
// own state in. They are checked because each is a silent failure otherwise:
// an unwritable CLAUDE_CONFIG_DIR is a theme that never pins, and an
// unreadable Codex or OpenCode database is a session that can never be
// resumed after a reboot.
func harnessStateCheck(id string) (checkResult, bool) {
	switch id {
	case config.HarnessClaude:
		dir := harnesses.ClaudeConfigDir()
		row := checkResult{Name: "Claude Code state", Summary: "writable", Detail: dir}
		if err := writableDir(dir); err != nil {
			row.Summary, row.Detail = "not writable", err.Error()
		} else {
			row.OK = true
		}
		return row, true
	case config.HarnessCodex:
		path := filepath.Join(harnesses.CodexHome(), "state_5.sqlite")
		row := checkResult{Name: "Codex state", Summary: "readable", Detail: path}
		if err := readable(path); err != nil {
			row.Summary, row.Detail = "not readable", err.Error()
		} else {
			row.OK = true
		}
		return row, true
	case config.HarnessOpenCode:
		path := harnesses.OpenCodeDBPath()
		row := checkResult{Name: "OpenCode state", Summary: "readable", Detail: path}
		if err := readable(path); err != nil {
			row.Summary, row.Detail = "not readable", err.Error()
		} else {
			row.OK = true
		}
		return row, true
	}
	return checkResult{}, false
}

// tunnelCheck is the remote-access row, unchanged in substance from the one
// the dashboard has always shown.
func (s *Server) tunnelCheck(settings config.Settings) checkResult {
	installed, version, _ := s.tunnel.Probe()
	switch {
	case !settings.Tunnel.Enabled:
		return checkResult{Name: "Remote access", OK: true, Summary: "tunnel off",
			Detail: "reachable at " + orLocal(s.LocalURL())}
	case !installed:
		return checkResult{Name: "Remote access", OK: false, Summary: "cloudflared missing",
			Detail: "it is downloaded when the tunnel starts"}
	}
	status := s.tunnel.Status()
	var detail []string
	if status.URL != "" {
		detail = append(detail, status.URL)
	}
	if version != "" {
		detail = append(detail, clean(version))
	}
	if status.Error != "" {
		detail = append(detail, status.Error)
	}
	return checkResult{Name: "Remote access", OK: status.State == tunnel.StateRunning,
		Summary: status.State, Detail: strings.Join(detail, " · ")}
}

// diskCheck is how much room the journals, the generated files and the
// database have left. A terminal that fills the disk stops recording what it
// showed, which is the one failure nobody notices until they need the replay.
func diskCheck(dir string) checkResult {
	// A data directory that does not exist yet - a Socrates that has never
	// started a session - is still on a disk, and that disk is the answer.
	probe := dir
	for probe != "" {
		if _, err := os.Stat(probe); err == nil {
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			break
		}
		probe = parent
	}
	free, total, err := diskFree(probe)
	if err != nil {
		return checkResult{Name: "Disk", Summary: "unknown", Detail: err.Error()}
	}
	// Half a gigabyte is roughly one full journal plus room to write the
	// database out; below that a session is one long paste from failing.
	return checkResult{
		Name:    "Disk",
		OK:      free >= 512<<20,
		Summary: humanBytes(free) + " free",
		Detail:  fmt.Sprintf("%s of %s under %s", humanBytes(free), humanBytes(total), dir),
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// humanBytes is a size somebody reads rather than counts.
func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value, exp := float64(n)/unit, 0
	for value >= unit && exp < 4 {
		value /= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", value, "KMGTP"[exp])
}

// writableDir makes a directory if it is not there and proves it can be
// written to, which is the only claim worth making about one.
func writableDir(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("no directory is configured")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	probe := filepath.Join(dir, ".socrates-write-test")
	if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
		return err
	}
	return os.Remove(probe)
}

// readable proves a file exists and can be opened, which for a state database
// is the whole question.
func readable(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("this machine has no home directory to look in")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return f.Close()
}

// voiceCheck says what the local voice can do right now. An install that is
// still running is the interesting case: it is neither working nor broken, and
// reporting it as either is what would send someone looking for a setting to
// fix when the honest answer is that 150 MB are on their way.
func voiceCheck(voice piper.Status) checkResult {
	switch {
	case voice.Ready:
		detail := voice.Detail
		if len(voice.Voices) > 0 {
			detail += " · " + strings.Join(voice.Voices, ", ")
		}
		return checkResult{Name: "Text to speech", OK: true, Summary: "ready", Detail: detail}
	case voice.Err != "":
		return checkResult{Name: "Text to speech", OK: false, Summary: "failed",
			Detail: voice.Detail + " · " + voice.Err}
	default:
		return checkResult{Name: "Text to speech", OK: false, Summary: "not installed",
			Detail: voice.Detail}
	}
}

// escape sequence parser states.
const (
	stText = iota
	stEsc
	stCSI
	stOSC
	stOSCEsc
	stCharset
)

// stripper removes terminal escape sequences from a byte stream that arrives
// in arbitrary chunks, which is why it has to remember where it stopped. The
// pseudo terminal it was written for is gone; a coding agent that prints its
// version with a colour in it is what is left, and that is enough to keep it.
type stripper struct {
	state int
}

func (s *stripper) filter(in string) string {
	var out strings.Builder
	out.Grow(len(in))
	for _, r := range in {
		switch s.state {
		case stText:
			switch r {
			case 0x1b:
				s.state = stEsc
			case 0x00, 0x07:
				// NUL and BEL carry no text.
			default:
				out.WriteRune(r)
			}
		case stEsc:
			switch r {
			case '[':
				s.state = stCSI
			case ']':
				s.state = stOSC
			case 'P', '^', '_': // DCS, PM, APC all end like an OSC string
				s.state = stOSC
			case '(', ')', '*', '+': // character set selection, one more byte
				s.state = stCharset
			default:
				s.state = stText
			}
		case stCSI:
			// Parameter and intermediate bytes, then a final byte.
			if r >= 0x40 && r <= 0x7e {
				s.state = stText
			}
		case stOSC:
			switch r {
			case 0x07:
				s.state = stText
			case 0x1b:
				s.state = stOSCEsc
			}
		case stOSCEsc:
			// ESC \ terminates the string; anything else was part of it.
			s.state = stText
			if r != '\\' {
				s.state = stOSC
			}
		case stCharset:
			s.state = stText
		}
	}
	return out.String()
}

// StripANSI removes escape sequences from a complete string.
func StripANSI(s string) string {
	var st stripper
	return st.filter(s)
}
