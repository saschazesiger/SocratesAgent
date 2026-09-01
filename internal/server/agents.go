package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/catalog"
	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/harness"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// handleAgents is what the new-chat picker and the admin card are built from.
func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.agents.Get(r.Context()))
}

// handleAgentsRefresh is the dashboard's Refresh button: throw the cache away
// and ask every installed CLI again, synchronously, because the person who
// pressed it is watching.
func (s *Server) handleAgentsRefresh(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.agents.Refresh(r.Context()))
}

// resolveBinding validates what a new chat asks to be bound to, and fills in
// what it left out. Every refusal here is permanent, so the caller answers 422
// and never 409.
func (s *Server) resolveBinding(ctx context.Context, agentID, model, effort string) (string, string, string, error) {
	if agentID == "" {
		return "", "", "", errors.New("a chat has to say which agent answers in it")
	}
	desc, known := harness.Get(agentID)
	if !known {
		return "", "", "", fmt.Errorf("%s is not an agent Socrates knows", agentID)
	}
	entry, inSettings := s.Settings().Agents.Entry(agentID)
	if !inSettings || !entry.Enabled {
		return "", "", "", fmt.Errorf("%s is switched off in the dashboard", desc.Label)
	}

	// The catalogue is consulted, never waited for: a chat created offline and
	// delivered later must not be refused because a discovery has not run.
	snap, haveCatalogue := s.agents.Cached()
	var agent catalog.Agent
	if haveCatalogue {
		agent, haveCatalogue = snap.Agent(agentID)
	}

	if model == "" {
		model = desc.DefaultModel
		if haveCatalogue && agent.DefaultModel != "" {
			model = agent.DefaultModel
		}
	}
	if model == "" {
		return "", "", "", fmt.Errorf("%s needs a model, and Socrates does not know which one to pick", desc.Label)
	}

	checked := false
	if haveCatalogue && !agent.Static && len(agent.Models) > 0 {
		// A discovered list is a real list of what this installation can run,
		// so an id that is not in it is a mistake worth catching here rather
		// than mid-turn.
		entry, ok := agent.Model(model)
		if !ok {
			return "", "", "", fmt.Errorf("%s cannot run %q - pick one of the models it reports", desc.Label, model)
		}
		checked = true
		if effort != "" && !hasEffort(entry.Efforts, effort) {
			return "", "", "", fmt.Errorf("%s does not offer %q on %s", desc.Label, effort, model)
		}
	}
	// A curated list - Claude's - is a convenience, not a whitelist: the CLI
	// validates the id itself and reports a bad one as a clean run error, and
	// a new alias should work the day it ships without a Socrates release.
	if !checked {
		effort = config.NormalizeEffort(effort)
	}
	return agentID, model, config.NormalizeEffort(effort), nil
}

func hasEffort(list []string, want string) bool {
	for _, e := range list {
		if e == want {
			return true
		}
	}
	return false
}

// describeBinding is what the chat header shows: the agent's name, the model's
// name, and whether the binding still works on this machine.
func (s *Server) describeBinding(chat *store.Chat) (string, string, bool) {
	if chat.Agent == "" {
		return "", "", false
	}
	desc, known := harness.Get(chat.Agent)
	label := chat.Agent
	if known {
		label = desc.Label
	}
	modelLabel := chat.Model
	ok := known
	if entry, inSettings := s.Settings().Agents.Entry(chat.Agent); !inSettings || !entry.Enabled {
		ok = false
	}
	if snap, have := s.agents.Cached(); have {
		if agent, found := snap.Agent(chat.Agent); found {
			if !agent.Installed {
				ok = false
			}
			if m, found := agent.Model(chat.Model); found && m.Label != "" {
				modelLabel = m.Label
			}
		}
	}
	return label, modelLabel, ok
}

// agentUnavailable is the permanent-refusal check that runs before a message
// is handed to the engine. It returns the sentence to say and false when the
// chat cannot be answered at all.
func (s *Server) agentUnavailable(chatID string) (string, bool) {
	if runtime.GOOS == "windows" {
		return "agent sessions need a unix socket, which this build does not have; " +
			"run Socrates on Linux or macOS, or in the Docker image", false
	}
	chat, err := s.store.GetChat(chatID)
	if err != nil {
		// Not found is the engine's answer to give, with its own status code.
		return "", true
	}
	if chat.Agent == "" {
		return "", true // ErrNoAgent, with its own sentence
	}
	desc, known := harness.Get(chat.Agent)
	label := chat.Agent
	if known {
		label = desc.Label
	}
	if entry, inSettings := s.Settings().Agents.Entry(chat.Agent); !inSettings || !entry.Enabled {
		return label + " is not available on this machine any more", false
	}
	if snap, have := s.agents.Cached(); have {
		if agent, found := snap.Agent(chat.Agent); found && !agent.Installed {
			return label + " is not available on this machine any more", false
		}
	}
	return "", true
}

// changeModel applies a model or effort change to a chat. It is only allowed
// between turns, and it closes the chat's agent host so the next turn opens a
// fresh one on the new model against a fresh journal. The native session id is
// kept, so the conversation resumes rather than starting over.
//
// It reports whether the caller may carry on; a refusal has already been
// written.
func (s *Server) changeModel(w http.ResponseWriter, r *http.Request, chat *store.Chat, model, effort *string) bool {
	nextModel, nextEffort := chat.Model, chat.Effort
	if model != nil {
		nextModel = strings.TrimSpace(*model)
	}
	if effort != nil {
		nextEffort = strings.TrimSpace(*effort)
	}
	if nextModel == chat.Model && config.NormalizeEffort(nextEffort) == chat.Effort {
		return true
	}
	if chat.Agent == "" {
		writeError(w, http.StatusUnprocessableEntity,
			"this chat was made before Socrates talked to agents directly - start a new chat")
		return false
	}
	// The same rule as sending, and for the same reason: a change that lands
	// in the shutdown drain window would pass the busy check, clear host_dir,
	// and leave a host that is mid-turn to be reconciled away underneath it.
	if s.engine.IsShuttingDown() {
		writeError(w, http.StatusServiceUnavailable,
			"Socrates is restarting - try again in a moment")
		return false
	}
	if s.engine.Busy(chat.ID) {
		writeError(w, http.StatusConflict, "the model can only be changed between turns")
		return false
	}
	_, resolvedModel, resolvedEffort, err := s.resolveBinding(r.Context(), chat.Agent, nextModel, nextEffort)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return false
	}
	if err := s.store.UpdateChatModel(chat.ID, resolvedModel, resolvedEffort); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return false
	}
	chat.Model, chat.Effort = resolvedModel, resolvedEffort
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s.engine.CloseChat(ctx, chat.ID)
	return true
}

// agentHostCheck is the diagnostics row that replaced the pseudo terminal one:
// a socket Socrates can actually create, at a path short enough for the kernel
// to accept, and how many sessions are alive right now.
func (s *Server) agentHostCheck() checkResult {
	if runtime.GOOS == "windows" {
		return checkResult{Name: "Agent hosts", OK: false,
			Detail: "agent sessions are not supported on this platform"}
	}
	path, err := agenthost.SocketPath("probe")
	if err != nil {
		return checkResult{Name: "Agent hosts", OK: false, Detail: err.Error()}
	}
	_ = os.Remove(path)
	listener, err := net.Listen("unix", path)
	if err != nil {
		return checkResult{Name: "Agent hosts", OK: false, Detail: err.Error()}
	}
	listener.Close()
	_ = os.Remove(path)
	live := 0
	for _, h := range s.hosts.List("") {
		if h.Alive() {
			live++
		}
	}
	return checkResult{Name: "Agent hosts", OK: true,
		Detail: fmt.Sprintf("%s · %d session(s) open", filepath.Dir(path), live)}
}

// agentChecks is one row per enabled agent. There is deliberately no smoke
// turn: a real turn costs money, needs the network and leaves a stray session
// behind, and the model list /api/agents already fetched is the stronger
// signal that the CLI is usable - so it is shown here for free.
func (s *Server) agentChecks(ctx context.Context, settings config.Settings) []checkResult {
	cached, haveCatalogue := s.agents.Cached()
	var out []checkResult
	for _, id := range harness.IDs() {
		desc, ok := harness.Get(id)
		if !ok {
			continue
		}
		entry, known := settings.Agents.Entry(id)
		if !known || !entry.Enabled {
			continue
		}
		bin := strings.TrimSpace(entry.Binary)
		if bin == "" {
			bin = desc.Binary
		}
		path, err := exec.LookPath(bin)
		if err != nil {
			out = append(out, checkResult{Name: desc.Label, OK: false,
				Detail: "command " + bin + " not found in PATH"})
			continue
		}
		args := desc.VersionArgs
		if len(args) == 0 {
			args = []string{"--version"}
		}
		vctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		raw, err := exec.CommandContext(vctx, path, args...).CombinedOutput()
		cancel()
		version := strings.TrimSpace(strings.SplitN(stripControl(string(raw)), "\n", 2)[0])
		if err != nil {
			out = append(out, checkResult{Name: desc.Label, OK: false,
				Detail: path + " failed to report a version: " + strings.TrimSpace(err.Error()+" "+version)})
			continue
		}
		detail := version
		if haveCatalogue {
			if agent, found := cached.Agent(id); found && len(agent.Models) > 0 {
				detail += fmt.Sprintf(" · %d models", len(agent.Models))
			}
		}
		out = append(out, checkResult{Name: desc.Label, OK: true, Detail: detail})
	}
	return out
}
