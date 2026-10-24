package server

// A chat's account (REBUILD.md §4.3): which one it runs on, a switch to
// another while it is open, and whether it switches by itself when a limit
// is reached.
//
//	POST /api/chats/{id}/account       {"client_id","account","when"?,"continue"?}   as messages
//	                                   (a switch_account command); when is "now" (a
//	                                   running turn is interrupted first) or
//	                                   "after_turn"; continue tells the new account
//	                                   to go on. 409 for an account of another agent
//	                                   or one that is not signed in
//	PUT  /api/chats/{id}/auto-switch   {"on"} → {"session"}

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/accounts"
	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

func (s *Server) handleChatAccount(w http.ResponseWriter, r *http.Request) {
	row, p, ok := s.chatOf(w, r)
	if !ok {
		return
	}
	var body struct {
		ClientID string `json:"client_id"`
		Account  string `json:"account"`
		When     string `json:"when"`
		Continue bool   `json:"continue"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	body.ClientID = strings.TrimSpace(body.ClientID)
	if body.ClientID == "" {
		writeError(w, http.StatusBadRequest, "a switch needs a client_id")
		return
	}
	// A repeat of a switch the journal has is answered from it.
	p.mu.Lock()
	seq, seen := p.proj.Seen(body.ClientID)
	from := accountOfTurn(p.proj, row)
	limited := p.proj.Turn == nil && p.proj.LastTurn != nil && p.proj.LastTurn.Outcome == agent.OutcomeLimited
	p.mu.Unlock()
	if seen {
		writeJSON(w, http.StatusOK, map[string]any{"client_id": body.ClientID, "seq": seq, "dup": true})
		return
	}
	switch body.When {
	case "":
		body.When = agenthost.WhenNow
	case agenthost.WhenNow, agenthost.WhenAfterTurn:
	default:
		writeError(w, http.StatusBadRequest, "a switch happens now or after the turn")
		return
	}
	target, err := s.store.GetAccount(body.Account)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "there is no such account")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if target.Agent != row.Harness {
		writeError(w, http.StatusConflict, target.Label+" is an account of another agent")
		return
	}
	refreshed := s.accounts.refreshed(*target)
	if refreshed.State != accounts.SignedIn {
		writeError(w, http.StatusConflict, "Sign in to "+target.Label+" first")
		return
	}
	reason := ""
	if body.Continue && limited && from != "" {
		// A tap on "Continue with" under a limit says why; a switch in the
		// middle of a turn the person stopped for it is no limit.
		label := from
		if a, err := s.store.GetAccount(from); err == nil {
			label = a.Label
		}
		reason = "The limit of " + label + " was reached."
	}
	code, answer := s.accounts.switchChat(r.Context(), p, row, &refreshed, body.ClientID, body.When, body.Continue, reason)
	writeJSON(w, code, answer)
}

func (s *Server) handleChatAutoSwitch(w http.ResponseWriter, r *http.Request) {
	row, _, ok := s.chatOf(w, r)
	if !ok {
		return
	}
	var body struct {
		On bool `json:"on"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if err := s.store.SetSessionOptions(row.ID, withOption(row.Options, "auto_switch", body.On)); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.answerWithSession(w, row.ID)
}

// checkChatAccount is the account a new or prepared chat asked for: one of
// its agent's, or none (then the one recommended when it starts).
func (s *Server) checkChatAccount(harness, id string) *launchError {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	a, err := s.store.GetAccount(id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return refuse(http.StatusBadRequest, "there is no such account")
	case err != nil:
		return refuse(http.StatusInternalServerError, "%s", err.Error())
	case a.Agent != harness:
		return refuse(http.StatusBadRequest, "%s is an account of another agent", a.Label)
	}
	return nil
}

// withChatAccount is options with a chat's account and its automatic
// switch.
func withChatAccount(options json.RawMessage, account string, autoSwitch bool) json.RawMessage {
	return withOption(withOption(options, "account", strings.TrimSpace(account)), "auto_switch", autoSwitch)
}

// noteAccount takes what an event of a chat says about its account: the row
// follows the journal's switches, a usage window and the agent's word on
// who the account is are kept with the account, and a turn's end may be a
// limit to act on. account is the account the chat ran on before the event.
func (p *chatPeer) noteAccount(ev agent.Event, account string, limits map[string]agent.RateLimit) {
	h := p.hub.srv.accounts
	if account == "" {
		if row, err := p.hub.srv.store.GetSession(p.id); err == nil {
			account = readChatOptions(row.Options).Account
		}
	}
	switch ev.Type {
	case agent.TypeAccountSwitched:
		row, err := p.hub.srv.store.GetSession(p.id)
		if err == nil && ev.To != "" && readChatOptions(row.Options).Account != ev.To {
			_ = p.hub.srv.store.SetSessionOptions(p.id, withOption(row.Options, "account", ev.To))
			p.hub.srv.events.poke()
		}
	case agent.TypeRateLimit:
		h.noteRateLimit(account, ev)
	case agent.TypeAgentInfo:
		h.noteIdentity(account, ev.Content)
	case agent.TypeTurnEnded:
		h.noteTurnEnd(p, account, ev, limits)
	case agent.TypeError:
		if ev.Code == agent.ErrorAuth && account != "" {
			h.noteRefused(account)
		}
	}
}
