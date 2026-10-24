package server

// What a chat cost, for the cost ledger (costs.go).
//
// One source per chat: its journal. Every turn's end carries what it used -
// Claude's cost_usd (the CLI's own price), Codex's tokens (priced here by the
// ledger's Codex rates), OpenCode's cost (OpenCode prices its answers itself)
// - and every turn has the account it ran on (the account.switched events),
// which the CLIs' transcripts cannot say once every account's transcripts
// are in one shared folder. The transcripts of a chat's conversations are
// therefore never counted as well: every conversation id a chat's journal
// names, and the row's own, is the chat's, kept in the store for good so
// that a chat that is deleted does not hand its transcripts back to the
// scanner, and a ledger the scanner made for one before is taken away.
//
// A chat's ledger is named by the chat, not by its conversation: /clear, a
// Codex thread copied for a stripped retry and a conversation lost and
// started again are one chat. Each account a chat ran on is a ledger of its
// own (chat-<id> for the account it started on, chat-<id>@<account> after a
// switch), so the costs page splits them by account the way it splits the
// transcripts by profile.

import (
	"crypto/sha256"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/costs"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// chatOwnedKey is the kv row of the conversations the chats count: the
// transcript scanner's ledger key of each, and the chat it belongs to.
const chatOwnedKey = "costs.chat_conversations"

// chatLedgerID is the ledger's conversation id of a chat on one account.
func chatLedgerID(chatID, account string) string {
	if account == "" {
		return "chat-" + chatID
	}
	return "chat-" + chatID + "@" + account
}

// chatCosts is a chat's journal summed up: what each account spent, and
// every conversation the chat had.
type chatCosts struct {
	spends map[string][]costs.Spend
	convs  []string
}

// readChatCosts reads a chat's journal. model is the chat's model before
// its journal names one.
func readChatCosts(path, harness, model string) (chatCosts, error) {
	out := chatCosts{spends: map[string][]costs.Spend{}}
	account := ""
	switched := false
	turnModel := map[string]string{}
	seen := map[string]bool{}
	err := agent.ScanJournal(path, func(ev agent.Event) {
		switch ev.Type {
		case agent.TypeMessage:
			if ev.Role == agent.RoleAssistant && ev.Model != "" {
				model = ev.Model
				if ev.TurnID != "" {
					turnModel[ev.TurnID] = ev.Model
				}
			}
		case agent.TypeModelChanged:
			if ev.Model != "" {
				model = ev.Model
			}
		case agent.TypeConversation:
			if id := strings.TrimSpace(ev.ConversationID); id != "" && !seen[id] {
				seen[id] = true
				out.convs = append(out.convs, id)
			}
		case agent.TypeAccountSwitched:
			// The turns before the first switch ran on the account it
			// switched from.
			if !switched && account == "" && ev.From != "" {
				if before := out.spends[""]; len(before) > 0 {
					out.spends[ev.From] = append(out.spends[ev.From], before...)
					delete(out.spends, "")
				}
			}
			switched, account = true, ev.To
		case agent.TypeTurnEnded:
			m := turnModel[ev.TurnID]
			if m == "" {
				m = model
			}
			if s, ok := turnSpend(harness, m, ev); ok {
				out.spends[account] = append(out.spends[account], s)
			}
		}
	})
	return out, err
}

// turnSpend is what one turn's end says it used, priced the way the ledger
// prices the agent, and false for a turn that used nothing.
func turnSpend(harness, model string, ev agent.Event) (costs.Spend, bool) {
	u := ev.Usage
	if u == nil {
		u = &agent.Usage{}
	}
	if ev.CostUSD == 0 && u.Input+u.Output+u.CacheRead+u.CacheWrite+u.Reasoning == 0 {
		return costs.Spend{}, false
	}
	if model == "" {
		model = "unknown"
	}
	s := costs.Spend{At: ev.At, Model: model, Input: float64(u.Input), Output: float64(u.Output + u.Reasoning),
		CacheRead: float64(u.CacheRead), CacheWrite: float64(u.CacheWrite), CostUSD: ev.CostUSD, Priced: true,
		Requests: 1}
	switch harness {
	case "codex":
		// Codex reports no price; its tokens are priced by the ledger's
		// rates, which count the cached input inside the input.
		s.CostUSD, s.Priced = costs.CodexCost(model, s.Input+s.CacheRead+s.CacheWrite, s.CacheRead, s.CacheWrite, s.Output)
	case "opencode":
		// OpenCode's own model names go without their provider, as its
		// transcripts are counted.
		if i := strings.LastIndexByte(model, '/'); i >= 0 {
			s.Model = model[i+1:]
		}
	}
	return s, true
}

// chatLedgers brings the chats' ledgers up to date: a chat whose journal
// changed since its last pass is read again and its ledger per account
// written. It answers every conversation the chats count, by the scanner's
// ledger key, and the ones that became the chats' in this pass.
func (c *costLedger) chatLedgers(rows []store.Session, seen map[string]string) (owned, fresh map[string]bool) {
	st := c.s.store
	kept := map[string]string{}
	if err := st.GetJSON(chatOwnedKey, &kept); err != nil && err != store.ErrNotFound {
		log.Printf("costs: %v", err)
	}
	changed := false
	fresh = map[string]bool{}
	own := func(harness, conv, chat string) {
		key := store.CostKey(harness, conv)
		if conv == "" || kept[key] != "" {
			return
		}
		kept[key], fresh[key], changed = chat, true, true
	}
	for i := range rows {
		row := &rows[i]
		if !row.IsChat() {
			continue
		}
		own(row.Harness, row.CLISessionID, row.ID)
		path := agenthost.JournalPath(c.s.dataDir, row.ID)
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		key := store.CostKey(row.Harness, chatLedgerID(row.ID, ""))
		// The name and the folder go into every ledger of the chat, so a
		// rename is a change as much as a turn is.
		print := fmt.Sprintf("chat:v%s:%d:%d:%x", costs.Version, info.Size(), info.ModTime().UnixNano(),
			sha256.Sum256([]byte(row.Title+"\x00"+row.Workdir)))
		if seen[key] == print {
			continue
		}
		read, err := readChatCosts(path, row.Harness, row.Model)
		if err != nil {
			log.Printf("costs: %s: %v", row.ID, err)
			continue
		}
		for _, conv := range read.convs {
			own(row.Harness, conv, row.ID)
		}
		for account, list := range read.spends {
			ledger := store.CostLedger{CostConversation: store.CostConversation{
				Harness: row.Harness, ConvID: chatLedgerID(row.ID, account), Profile: account,
				Workdir: row.Workdir, Title: row.Title, SessionID: row.ID,
			}}
			ledger.Rows, ledger.FirstAt, ledger.LastAt = costs.Hourly(list)
			if _, err := st.PutCostLedger(ledger); err != nil {
				log.Printf("costs: %s: %v", ledger.Key(), err)
			}
		}
		if err := st.SetCostFingerprint(key, print); err != nil {
			log.Printf("costs: %s: %v", key, err)
		}
	}
	if changed {
		if err := st.SetJSON(chatOwnedKey, kept); err != nil {
			log.Printf("costs: %v", err)
		}
	}
	owned = map[string]bool{}
	for key := range kept {
		owned[key] = true
	}
	return owned, fresh
}
