package server

import (
	"net/http"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/catalog"
	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
)

// handleHarnesses is the whole of what the new-session sheet needs: which of
// the five programs are on this machine, what each can be run on, which of
// them are chats, where a session is allowed to work, and whether terminals
// can be created at all.
//
// It replaces both of the endpoints the chat app had - the agent list and the
// model list - because a model list that is not a field of the harness it
// belongs to is a second thing to keep in step for no gain.
func (s *Server) handleHarnesses(w http.ResponseWriter, r *http.Request) {
	s.answerCatalog(w, s.catalog.Get(r.Context()))
}

// handleRefreshHarnesses throws the cache away and asks every installed CLI
// again. The probes run detached, so a browser that gives up cannot leave a
// half-made catalogue behind.
func (s *Server) handleRefreshHarnesses(w http.ResponseWriter, r *http.Request) {
	s.answerCatalog(w, s.catalog.Refresh(r.Context()))
}

func (s *Server) answerCatalog(w http.ResponseWriter, snap catalog.Snapshot) {
	settings := s.Settings()
	presets := settings.Workspace.Presets
	if presets == nil {
		presets = []config.PresetDir{}
	}
	agents := snap.Agents
	// The scripted agent of the tests has no CLI to probe; it is offered
	// like one when it is switched on, with two models to choose from.
	if _, ok := agent.Lookup(agent.FakeHarness); ok {
		agents = append(append([]catalog.Agent{}, agents...), fakeCatalogEntry())
	}
	payload := map[string]any{
		"harnesses": agents,
		// The agents a session is a chat with rather than a terminal: the
		// harnesses with an adapter (REBUILD.md §3), see chatAgent.
		"chats":        chatAgents(),
		"refreshed_at": snap.RefreshedAt,
		"workspace": map[string]any{
			"root":            settings.Workspace.Root,
			"default_harness": settings.Workspace.DefaultHarness,
			"allow_custom":    settings.Workspace.AllowCustom,
			"presets":         presets,
			"modes":           harnesses.WorkdirModes,
		},
	}
	// Whether a session can be started at all is a property of the machine,
	// not of the catalogue, and the sheet has to be able to say so before
	// somebody fills it in.
	if err := s.manager.Available(); err != nil {
		payload["sessions_available"] = false
		payload["sessions_error"] = err.Error()
	} else {
		payload["sessions_available"] = true
	}
	writeJSON(w, http.StatusOK, payload)
}

// fakeCatalogEntry is the scripted agent as the new-session sheet lists it.
func fakeCatalogEntry() catalog.Agent {
	efforts := []string{"low", "medium", "high"}
	return catalog.Agent{
		ID: agent.FakeHarness, Label: chatLabel(agent.FakeHarness), Enabled: true, Installed: true, Static: true,
		HasEffort: true, DefaultModel: "fake-small", DefaultEffort: "medium",
		Models: []harnesses.Model{
			{ID: "fake-small", Label: "Fake small", Efforts: efforts, DefaultEffort: "medium", Default: true},
			{ID: "fake-large", Label: "Fake large", Efforts: efforts, DefaultEffort: "high"},
		},
	}
}
