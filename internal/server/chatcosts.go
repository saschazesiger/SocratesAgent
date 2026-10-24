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
// transcripts by profile. A ledger is the journal's sum as it reads now: one
// the read no longer makes (the account it started on, once the first switch
// says which one that was) is taken away, so that no turn is counted twice.
//
// A journal that was lost with its folder is a new one: the history its host
// writes back was counted when it ran, in the lost journal's ledgers, which
// stay as they are; what the new journal adds goes into ledgers of its own
// (chat-<id>~<when it was rebuilt>[@<account>]).

import (
	"crypto/sha256"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/costs"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// chatOwnedKey is the kv row of the conversations the chats count: the
// transcript scanner's ledger key of each, and the chat it belongs to.
const chatOwnedKey = "costs.chat_conversations"

// chatLedgerID is the ledger's conversation id of a chat on one account, in
// one of its journals: gen is empty for the journal the chat began with, and
// says when a lost one was rebuilt otherwise.
func chatLedgerID(chatID, gen, account string) string {
	id := "chat-" + chatID
	if gen != "" {
		id += "~" + gen
	}
	if account != "" {
		id += "@" + account
	}
	return id
}

// chatCosts is a chat's journal summed up: what each account spent, every
// conversation the chat had, and which of its journals this is (gen, for
// chatLedgerID).
type chatCosts struct {
	spends map[string][]costs.Spend
	convs  []string
	gen    string
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
		case agent.TypeNotice:
			// The journal was lost and begins again: the history written
			// back (or the word that it could not be) was counted when it
			// ran, in the ledgers of the journal that was lost.
			if ev.Code == agent.NoticeRebuilt || ev.Code == agent.NoticeHistoryUnavailable {
				out.spends = map[string][]costs.Spend{}
				out.gen = strconv.FormatInt(ev.At, 10)
				account, switched = "", false
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
	if ev.CostUSD == 0 && u.Input+u.Output+u.CacheRead+u.CacheWrite == 0 {
		return costs.Spend{}, false
	}
	if model == "" {
		model = "unknown"
	}
	// The journal's output counts the reasoning in, and its input is the
	// input that was neither read from the cache nor written to it
	// (AGENT-HOST.md §2), so neither is priced twice.
	s := costs.Spend{At: ev.At, Model: model, Input: float64(u.Input), Output: float64(u.Output),
		CacheRead: float64(u.CacheRead), CacheWrite: float64(u.CacheWrite), CostUSD: ev.CostUSD, Priced: true,
		Requests: 1}
	switch harness {
	case "codex":
		// Codex reports no price; its tokens are priced by the ledger's
		// rates, which take the input the way Codex reports it, the cached
		// and the cache-written tokens inside it.
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
// changed since its last pass is read again, its ledger per account written
// and a ledger of the same journal that the read no longer makes taken away.
// It answers every conversation the chats count, by the scanner's
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
		key := store.CostKey(row.Harness, chatLedgerID(row.ID, "", ""))
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
		wrote := map[string]bool{}
		for account, list := range read.spends {
			ledger := store.CostLedger{CostConversation: store.CostConversation{
				Harness: row.Harness, ConvID: chatLedgerID(row.ID, read.gen, account), Profile: account,
				Workdir: row.Workdir, Title: row.Title, SessionID: row.ID,
			}}
			ledger.Rows, ledger.FirstAt, ledger.LastAt = costs.Hourly(list)
			wrote[ledger.Key()] = true
			if _, err := st.PutCostLedger(ledger); err != nil {
				log.Printf("costs: %s: %v", ledger.Key(), err)
			}
		}
		if !c.dropStaleChatLedgers(store.CostKey(row.Harness, chatLedgerID(row.ID, read.gen, "")), wrote) {
			// Read again at the next pass, so that what is left over goes
			// then.
			continue
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

// dropStaleChatLedgers takes away the ledgers of one journal of a chat (base,
// its ledger on the account it started on, and base@<account>) that its read
// did not write, and reports whether it could look. The ledgers of the chat's
// other journals are not this read's to judge.
func (c *costLedger) dropStaleChatLedgers(base string, wrote map[string]bool) bool {
	st := c.s.store
	keys, err := st.CostKeysWithPrefix(base)
	if err != nil {
		log.Printf("costs: %s: %v", base, err)
		return false
	}
	done := true
	for _, key := range keys {
		if wrote[key] || key != base && !strings.HasPrefix(key, base+"@") {
			continue
		}
		if _, err := st.DeleteCostLedger(key); err != nil {
			log.Printf("costs: %s: %v", key, err)
			done = false
		}
	}
	return done
}
