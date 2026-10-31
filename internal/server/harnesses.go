package server

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/catalog"
	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/harnesses"
	"github.com/saschazesiger/SocratesAgent/internal/store"
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
	agents := withDefaults(settings, snap.Agents)
	// The scripted agent of the tests has no CLI to probe; it is offered
	// like one when it is switched on, with two models to choose from.
	if _, ok := agent.Lookup(agent.FakeHarness); ok {
		agents = append(agents, fakeCatalogEntry())
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

// withDefaults puts the dashboard's Default model and Default effort on the
// agents: what the sheet starts an agent's chat on, as a chat made without a
// choice of its own runs on them (chatModel). A folder that names the model
// the agent starts on there keeps it (chatModelIn).
func withDefaults(settings config.Settings, list []catalog.Agent) []catalog.Agent {
	agents := append([]catalog.Agent{}, list...)
	for i := range agents {
		if f := agents[i].Folder; f != nil && f.Model != "" {
			agents[i].DefaultModel, agents[i].DefaultEffort = f.Model, ""
			continue
		}
		model, effort := settings.Harnesses.Defaults(agents[i].ID)
		if model != "" {
			agents[i].DefaultModel = model
		}
		if effort != "" {
			agents[i].DefaultEffort = effort
		}
	}
	return agents
}

// handleFolderHarnesses is the agents that are something else in one folder
// than everywhere - OpenCode where an opencode.json of the folder adds
// providers, names the model it starts on or puts its subagents on models of
// their own - each as a whole entry of the catalogue, discovered in that
// folder. The sheet asks for the folder it is about to start a chat in, a
// chat for its own:
//
//	GET /api/harnesses/folder?workdir_mode=…&workdir=…
//	GET /api/harnesses/folder?session=<id>
//	  {"workdir":"/abs/dir","harnesses":[…]}
//
// An agent that is not in the answer is in that folder what the general
// catalogue says. A folder the sheet cannot have yet (a path half typed) is
// no error: it changes nothing.
func (s *Server) handleFolderHarnesses(w http.ResponseWriter, r *http.Request) {
	settings := s.Settings()
	q := r.URL.Query()
	var dir string
	if id := strings.TrimSpace(q.Get("session")); id != "" {
		row, err := s.store.GetSession(id)
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "there is no session with that id")
			return
		case err != nil:
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		dir = row.Workdir
	} else if checked, err := harnesses.CheckWorkdir(settings, strings.TrimSpace(q.Get("workdir_mode")), strings.TrimSpace(q.Get("workdir"))); err == nil {
		dir = checked
		if dir == "" {
			// A dynamic chat's folder is made inside the workspace, whose own
			// configuration it has.
			dir = filepath.Join(settings.Workspace.Root, "new")
		}
	}
	agents := []catalog.Agent{}
	if dir != "" {
		agents = withDefaults(settings, s.catalog.Folder(r.Context(), dir))
	}
	writeJSON(w, http.StatusOK, map[string]any{"workdir": dir, "harnesses": agents})
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
