//go:build !windows

package accounts

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// The clean cut of REBUILD.md §8.3, the part of the migration that is about
// sessions: an agent runs as a chat now, and its terminals do not come over.
// Every one of them is stopped the way a delete stops it - its tmux session
// killed, its row and its folder removed - and what it said stays where the
// agent wrote it, in the folders the moves share. One that was only prepared
// and never ran becomes the prepared chat it would be made as now, with its
// first prompt; it is given the account its profile became once the moves
// have made the accounts (bindPrepared). A Shell is not an agent and stays
// as it is, running and attached.

// preRebuildProfile is the option a prepared terminal's profile is carried
// in from the clean cut to bindPrepared: the profile's id, empty for the
// default one. A prepared session that left its profile to its start has
// none, and leaves its account to its start the same way. The key is the
// migration's own, so a prepared chat made later is never taken for one.
const preRebuildProfile = "pre_rebuild_profile"

// agentTerminals is every session that is an agent in a terminal.
func (m *migration) agentTerminals() ([]store.Session, error) {
	rows, err := m.opts.Store.ListSessions()
	if err != nil {
		return nil, err
	}
	var out []store.Session
	for _, row := range rows {
		if row.Kind == store.KindTerminal && agentLabel(row.Harness) != "" {
			out = append(out, row)
		}
	}
	return out, nil
}

// cleanCut stops every agent terminal in rows and makes every prepared one a
// prepared chat. What it removed is said once, in one line: a run cut short
// leaves the rest to the next, which says those.
func (m *migration) cleanCut(rows []store.Session) error {
	var removed, prepared []string
	for i := range rows {
		row := &rows[i]
		if row.State == store.StatePrestart {
			err := m.opts.Store.MakePreparedChat(row.ID, preparedChatOptions(row))
			if err != nil && !errors.Is(err, store.ErrNotFound) && !errors.Is(err, store.ErrNotPrestart) {
				return fmt.Errorf("the prepared %s terminal %q: %w", agentLabel(row.Harness), row.Title, err)
			}
			prepared = append(prepared, sessionWords(row))
		} else {
			if m.opts.StopTerminal == nil {
				return errors.New("there is nothing to stop the agents' terminals with")
			}
			if err := m.opts.StopTerminal(row.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("the %s terminal %q: %w", agentLabel(row.Harness), row.Title, err)
			}
			removed = append(removed, sessionWords(row))
		}
		if err := m.step("cut:" + row.ID); err != nil {
			return err
		}
	}
	if len(removed) > 0 {
		m.opts.Logf("rebuild: the agents run as chats now, so their terminals are gone: removed %s; "+
			"what they said is still in the agents' folders", strings.Join(removed, ", "))
	}
	if len(prepared) > 0 {
		m.opts.Logf("rebuild: the prepared agent terminals are prepared chats now, with their first prompts: %s",
			strings.Join(prepared, ", "))
	}
	return nil
}

// preparedChatOptions is what a prepared terminal's options become on the
// chat: Ultracode for Claude Code, and the profile to bind the account by.
func preparedChatOptions(row *store.Session) json.RawMessage {
	var old struct {
		Profile     string `json:"profile"`
		ProfileAuto bool   `json:"profile_auto"`
		Ultracode   bool   `json:"ultracode"`
	}
	if len(row.Options) > 0 {
		_ = json.Unmarshal(row.Options, &old)
	}
	options := map[string]any{}
	if old.Ultracode && row.Harness == Claude {
		options["ultracode"] = true
	}
	if !old.ProfileAuto {
		options[preRebuildProfile] = old.Profile
	}
	raw, _ := json.Marshal(options)
	return raw
}

// bindPrepared gives each prepared chat the clean cut made the account its
// profile became: the agent's Default account for the default profile, the
// profile's own for any other. A default profile whose agent had no folder
// in HOME has no Default account yet, and the chat makes one when it starts,
// as any chat with none does. A profile with no account of the agent at all
// had nothing to run that agent on, and nothing the chat could be given
// instead says the same, so that prepared session is removed and said.
func (m *migration) bindPrepared() error {
	rows, err := m.opts.Store.ListSessions()
	if err != nil {
		return err
	}
	list, err := m.opts.Store.ListAccounts()
	if err != nil {
		return err
	}
	byOrigin := map[string]string{}
	for i := range list {
		if list[i].Origin != "" {
			byOrigin[list[i].Origin] = list[i].ID
		}
	}
	for i := range rows {
		row := &rows[i]
		profile, marked := carriedProfile(row)
		if !marked || !row.IsChat() || row.State != store.StatePrestart {
			continue
		}
		account := ""
		if profile == "" {
			account = byOrigin[homeOrigin(row.Harness)]
		} else if account = byOrigin["profile:"+profile+":"+row.Harness]; account == "" {
			account = byOrigin["lost:"+profile+":"+row.Harness]
		}
		if profile != "" && account == "" {
			if err := m.opts.Store.DeleteSession(row.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
			m.opts.Logf("rebuild: removed the prepared %s: its profile %q has no %s account to run on",
				sessionWords(row), profile, agentLabel(row.Harness))
		} else if _, err := m.opts.Store.UpdateChatOptions(row.ID, func(raw json.RawMessage) json.RawMessage {
			return bound(raw, account)
		}); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if err := m.step("bind:" + row.ID); err != nil {
			return err
		}
	}
	return nil
}

// carriedProfile is the profile a prepared chat carries from the clean cut,
// and whether it carries one.
func carriedProfile(row *store.Session) (string, bool) {
	var options map[string]json.RawMessage
	if len(row.Options) == 0 || json.Unmarshal(row.Options, &options) != nil {
		return "", false
	}
	raw, ok := options[preRebuildProfile]
	if !ok {
		return "", false
	}
	var profile string
	_ = json.Unmarshal(raw, &profile)
	return profile, true
}

// bound is a prepared chat's options with its account and without the
// profile it was bound by.
func bound(raw json.RawMessage, account string) json.RawMessage {
	options := map[string]json.RawMessage{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &options)
	}
	delete(options, preRebuildProfile)
	if account != "" {
		value, _ := json.Marshal(account)
		options["account"] = value
	}
	out, _ := json.Marshal(options)
	return out
}

// homeOrigin is the origin of an agent's Default account.
func homeOrigin(agent string) string {
	switch agent {
	case Claude:
		return OriginClaude
	case Codex:
		return OriginCodex
	case OpenCode:
		return OriginOpenCode
	}
	return ""
}

// agentLabel is an agent's name, and empty for anything that is not one.
func agentLabel(harness string) string {
	switch harness {
	case Claude:
		return "Claude Code"
	case Codex:
		return "Codex"
	case OpenCode:
		return "OpenCode"
	}
	return ""
}

// sessionWords is a session as the log names it.
func sessionWords(row *store.Session) string {
	return fmt.Sprintf("%s %q (%s)", agentLabel(row.Harness), row.Title, row.ID)
}
