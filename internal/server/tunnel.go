package server

import (
	"net/http"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/config"
)

// handleTunnelStatus reports what the Cloudflare tunnel is doing.
func (s *Server) handleTunnelStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"tunnel":  s.Settings().Tunnel,
		"status":  s.tunnel.Status(),
		"install": installHint(),
	})
}

// handleTunnelStart saves the submitted configuration and brings the tunnel up.
func (s *Server) handleTunnelStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Tunnel *config.TunnelSettings `json:"tunnel"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	settings := s.Settings()
	if body.Tunnel != nil {
		settings.Tunnel = *body.Tunnel
	}
	settings.Tunnel.Enabled = true
	settings.Normalize()
	if settings.Tunnel.Mode == config.TunnelToken && settings.Tunnel.Token == "" {
		writeError(w, http.StatusBadRequest, "a named tunnel needs its tunnel token")
		return
	}
	if err := s.saveSettings(settings); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !s.tunnel.Running() {
		if err := s.tunnel.Start(); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tunnel": s.Settings().Tunnel,
		"status": s.tunnel.Status(),
	})
}

// handleTunnelStop takes the tunnel down and remembers that it should stay down.
func (s *Server) handleTunnelStop(w http.ResponseWriter, r *http.Request) {
	settings := s.Settings()
	settings.Tunnel.Enabled = false
	if err := s.saveSettings(settings); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.tunnel.Stop()
	writeJSON(w, http.StatusOK, map[string]any{
		"tunnel": s.Settings().Tunnel,
		"status": s.tunnel.Status(),
	})
}

// installHint tells the browser how to get cloudflared on this platform.
func installHint() map[string]string {
	return map[string]string{
		"macos":   "brew install cloudflared",
		"linux":   "curl -L https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64 -o /usr/local/bin/cloudflared && chmod +x /usr/local/bin/cloudflared",
		"windows": "winget install --id Cloudflare.cloudflared",
		"docs":    "https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/",
	}
}

// tunnelWarning is shown once a tunnel is live: the app is then reachable from
// the internet by anyone who knows the URL and the password.
func tunnelWarning(settings config.Settings) string {
	if !settings.Tunnel.Enabled {
		return ""
	}
	auto := 0
	for _, b := range settings.Backends {
		if b.Enabled && b.Approval != "ask" {
			auto++
		}
	}
	if auto == 0 {
		return ""
	}
	return strings.TrimSpace(`Your Socrates instance is published on the internet and ` +
		`delegate agents run commands without asking. Anyone who gets past the password ` +
		`effectively has a shell on this machine - put Cloudflare Access in front of the ` +
		`hostname, or switch the agents to "Ask me in the web interface".`)
}
