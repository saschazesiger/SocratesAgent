package server

import (
	"net/http"

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

// handleTunnelInstall downloads cloudflared without starting a tunnel, so the
// dashboard can prepare remote access ahead of time.
func (s *Server) handleTunnelInstall(w http.ResponseWriter, r *http.Request) {
	path, err := s.tunnel.Install(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": path, "status": s.tunnel.Status()})
}
