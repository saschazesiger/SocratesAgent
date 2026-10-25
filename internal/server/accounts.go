package server

// The accounts a chat runs on (internal/accounts, REBUILD.md §4): the list
// the Accounts card manages, the environment a chat's host is launched with,
// each account's limits and the one to recommend, and the sign-ins.
//
//	GET    /api/accounts                   {"accounts":[<account>…],"recommended":{<agent>:<id>},"agents":[…]}
//	POST   /api/accounts                   {"agent","kind","label","key"?,"provider"?} → 201 {"account"}
//	PATCH  /api/accounts/{id}              {"label"?,"key"?,"provider"?} → {"account"}
//	DELETE /api/accounts/{id}              {"ok":true}; 409 while a chat runs on it, or for
//	                                       the agent's own sign-in the migration moved in
//	PUT    /api/accounts/order             {"order":[<id>…]} → {"ok":true}; 409 when the list changed
//	POST   /api/accounts/{id}/signin       {"provider"?,"method"?,"inputs"?} → {"signin":<flow>},
//	                                       or {"session":<terminal>} for Claude Code
//	GET    /api/accounts/{id}/signin       {"signin":<flow>}
//	POST   /api/accounts/{id}/signin/code  {"code"} → {"signin":<flow>}
//	DELETE /api/accounts/{id}/signin       {"ok":true}
//
// No answer ever carries an account's key: it is in the store sealed, and
// reaches a chat's host sealed again for that chat (agenthost.SealEnv).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/accounts"
	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// The accounts' clock.
const (
	// codexUsageEvery is how often Codex is asked for an account's limits:
	// each reading is an app-server started and stopped.
	codexUsageEvery = 5 * time.Minute
	// limitsWait bounds reading every account's limits for one list.
	limitsWait = 6 * time.Second
	// unknownRenewal is how long an account that ran into a limit whose
	// renewal nobody said is passed over by the recommendation.
	unknownRenewal = time.Hour
)

// accountHub is the accounts as this server holds them.
type accountHub struct {
	srv *Server
	// mu makes each change to the list one read and one write.
	mu sync.Mutex

	keyMu   sync.Mutex
	keyring *accounts.Keyring

	signins *accounts.SignIns

	codexMu    sync.Mutex
	codexUsage map[string]codexReading

	// told is, per chat, the account whose limit stopped it and was pushed:
	// a turn that runs into the same limit again before one gets through - a
	// message queued in the CLI behind it, the person trying once more - is
	// not pushed twice.
	toldMu sync.Mutex
	told   map[string]string
}

type codexReading struct {
	at    time.Time
	usage accounts.CodexUsage
}

func newAccountHub(s *Server) *accountHub {
	return &accountHub{srv: s, signins: accounts.NewSignIns(), codexUsage: map[string]codexReading{}, told: map[string]string{}}
}

// keys is the data directory's keyring, made the first time it is needed.
func (h *accountHub) keys() (*accounts.Keyring, error) {
	h.keyMu.Lock()
	defer h.keyMu.Unlock()
	if h.keyring != nil {
		return h.keyring, nil
	}
	k, err := accounts.OpenKeyring(h.srv.dataDir)
	if err != nil {
		return nil, err
	}
	h.keyring = k
	return k, nil
}

// accountView is an account as the dashboard sees it.
type accountView struct {
	ID          string `json:"id"`
	Agent       string `json:"agent"`
	Label       string `json:"label"`
	Kind        string `json:"kind"`
	State       string `json:"state"`
	StateDetail string `json:"state_detail,omitempty"`
	Order       int    `json:"order"`
	CreatedAt   int64  `json:"created_at"`
	// Email, Plan and Provider are behind the account's "i".
	Email    string `json:"email,omitempty"`
	Plan     string `json:"plan,omitempty"`
	Provider string `json:"provider,omitempty"`
	// Origin is the folder the migration moved the account in from.
	Origin string        `json:"origin,omitempty"`
	Limits []usageWindow `json:"limits,omitempty"`
	// LimitedUntil is when a limit the account ran into renews (Unix ms).
	LimitedUntil int64 `json:"limited_until,omitempty"`
	// Chats is how many chats run on it; a chat that does keeps it from
	// being deleted.
	Chats       int            `json:"chats"`
	Recommended bool           `json:"recommended,omitempty"`
	SignIn      *accounts.Flow `json:"signin,omitempty"`
}

// chatOptions is what a chat keeps of its account in its row's options.
type chatOptions struct {
	Account    string `json:"account,omitempty"`
	AutoSwitch bool   `json:"auto_switch,omitempty"`
}

func readChatOptions(raw json.RawMessage) chatOptions {
	var o chatOptions
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &o)
	}
	return o
}

// refreshed reads an account's login from its folder and records what
// changed: a login that appeared, or one that went (a sign-out, a refresh
// another client lost). A failure stays a failure until a login is there.
func (h *accountHub) refreshed(a store.Account) store.Account {
	login := accounts.Detect(h.srv.dataDir, &a)
	state, detail := a.State, a.StateDetail
	switch {
	case login.SignedIn && !accounts.Refused(&a):
		state, detail = accounts.SignedIn, ""
	case a.State == accounts.Failed:
	default:
		state = accounts.NeedsSignIn
		if a.State == accounts.SignedIn {
			detail = ""
		}
	}
	if state != a.State || detail != a.StateDetail {
		if err := h.srv.store.SetAccountState(a.ID, state, detail); err != nil {
			log.Printf("accounts: %s: %v", a.ID, err)
		}
		a.State, a.StateDetail = state, detail
	}
	if login.SignedIn && ((login.Email != "" && login.Email != a.Email) || (login.Plan != "" && login.Plan != a.Plan)) {
		if err := h.srv.store.SetAccountIdentity(a.ID, login.Email, login.Plan); err == nil {
			if login.Email != "" {
				a.Email = login.Email
			}
			if login.Plan != "" {
				a.Plan = login.Plan
			}
		}
	}
	return a
}

// chatCounts is how many chats run on each account, by id.
func (h *accountHub) chatCounts() (map[string]int, error) {
	rows, err := h.srv.store.ListSessions()
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for i := range rows {
		if rows[i].IsChat() {
			if id := readChatOptions(rows[i].Options).Account; id != "" {
				counts[id]++
			}
		}
	}
	return counts, nil
}

// views is every account as the dashboard sees it, with its limits read now
// where they can be, and the account to recommend per agent.
func (h *accountHub) views(ctx context.Context) ([]accountView, map[string]string, error) {
	list, err := h.srv.store.ListAccounts()
	if err != nil {
		return nil, nil, err
	}
	counts, err := h.chatCounts()
	if err != nil {
		return nil, nil, err
	}
	views := make([]accountView, len(list))
	lctx, cancel := context.WithTimeout(ctx, limitsWait)
	defer cancel()
	var wg sync.WaitGroup
	for i := range list {
		a := h.refreshed(list[i])
		views[i] = viewOf(a, counts[a.ID])
		if flow, ok := h.signins.Get(a.ID); ok {
			views[i].SignIn = &flow
		}
		wg.Add(1)
		go func(v *accountView, a store.Account) {
			defer wg.Done()
			v.Limits = h.limitsOf(lctx, &a)
		}(&views[i], a)
	}
	wg.Wait()
	now := time.Now()
	recommended := map[string]string{}
	for _, name := range accounts.Agents() {
		if id := recommendAccount(views, name, "", now); id != "" {
			recommended[name] = id
		}
	}
	for i := range views {
		views[i].Recommended = recommended[views[i].Agent] == views[i].ID
	}
	return views, recommended, nil
}

func viewOf(a store.Account, chats int) accountView {
	return accountView{
		ID: a.ID, Agent: a.Agent, Label: a.Label, Kind: a.Kind, State: a.State, StateDetail: a.StateDetail,
		Order: a.Order, CreatedAt: a.CreatedAt, Email: a.Email, Plan: a.Plan, Provider: a.Provider,
		Origin: a.Origin, LimitedUntil: a.LimitedUntil, Chats: chats,
	}
}

// storedLimits is the reading an account's chats last reported.
func storedLimits(a *store.Account) []usageWindow {
	var windows []usageWindow
	if len(a.Limits) > 0 {
		_ = json.Unmarshal(a.Limits, &windows)
	}
	return windows
}

// limitsOf is an account's usage windows as they stand now: Claude's from
// Anthropic's usage endpoint with the account's own login (read, never
// refreshed), Codex's from codex itself, and otherwise what the account's
// chats last reported (rate_limit events). A reading that cannot be taken
// falls back to the stored one.
func (h *accountHub) limitsOf(ctx context.Context, a *store.Account) []usageWindow {
	if a.State == accounts.SignedIn && a.Kind == accounts.Subscription {
		switch a.Agent {
		case accounts.Claude:
			if windows := h.srv.claudeLimits(ctx, accounts.Folder(h.srv.dataDir, a.Agent, a.ID)).Windows; len(windows) > 0 {
				return current(windows, time.Now())
			}
		case accounts.Codex:
			if windows := h.codexLimits(ctx, a); len(windows) > 0 {
				return current(windows, time.Now())
			}
		}
	}
	return current(storedLimits(a), time.Now())
}

// codexLimits is the last reading Codex gave of an account, taken again when
// it is older than codexUsageEvery.
func (h *accountHub) codexLimits(ctx context.Context, a *store.Account) []usageWindow {
	h.codexMu.Lock()
	r, ok := h.codexUsage[a.ID]
	h.codexMu.Unlock()
	if !ok || time.Since(r.at) > codexUsageEvery {
		bin, env, err := h.codexCLI(a)
		if err != nil {
			return nil
		}
		usage, err := accounts.ReadCodexUsage(ctx, bin, env)
		if err != nil {
			log.Printf("accounts: the limits of %s: %v", a.ID, err)
		}
		r = codexReading{at: time.Now(), usage: usage}
		h.codexMu.Lock()
		h.codexUsage[a.ID] = r
		h.codexMu.Unlock()
		if usage.Email != "" || usage.Plan != "" {
			_ = h.srv.store.SetAccountIdentity(a.ID, usage.Email, usage.Plan)
		}
	}
	out := make([]usageWindow, 0, len(r.usage.Windows))
	for _, w := range r.usage.Windows {
		out = append(out, usageWindow{Label: w.Label, UsedPercent: w.UsedPercent, ResetsAt: epochTime(w.ResetsAt)})
	}
	return out
}

// codexCLI is the codex an account's sign-in and readings run: the
// dashboard's binary setting, else SOCRATES_CODEX_BIN, else codex on the
// PATH, with the account's environment.
func (h *accountHub) codexCLI(a *store.Account) (string, []string, error) {
	if err := accounts.Prepare(h.srv.dataDir, a); err != nil {
		return "", nil, err
	}
	env := accounts.Env(h.srv.dataDir, a, os.Environ())
	entry, _ := h.srv.Settings().Harnesses.Entry(accounts.Codex)
	bin, err := agent.ResolveBinary(env, "codex", entry.Binary, os.Getenv("SOCRATES_CODEX_BIN"))
	return bin, env, err
}

// recommendAccount is the account of an agent a chat should run on: of the
// signed-in accounts other than exclude that have not just run into a limit,
// the one with the most allowance left for the time until it renews
// (headroom), and one with no reading - an API key has no windows - only when
// every account with a reading is at its limit. "" when there is none.
func recommendAccount(views []accountView, agentName, exclude string, now time.Time) string {
	best, bestRate, unknown := "", 0.0, ""
	for _, v := range views {
		if v.Agent != agentName || v.ID == exclude || v.State != accounts.SignedIn {
			continue
		}
		if v.LimitedUntil > now.UnixMilli() {
			continue
		}
		rate, known := headroom(v.Limits, now)
		switch {
		case !known:
			if unknown == "" {
				unknown = v.ID
			}
		case rate > 0 && (best == "" || rate > bestRate):
			best, bestRate = v.ID, rate
		}
	}
	if best != "" {
		return best
	}
	return unknown
}

// forChat is the account a chat runs on: the one its row names, and for a
// row that names none (or one deleted since) the recommended account of its
// agent, else its first, else a new Default one; the choice is kept on the
// row.
func (h *accountHub) forChat(row *store.Session) (*store.Account, error) {
	opts := readChatOptions(row.Options)
	if opts.Account != "" {
		if a, err := h.srv.store.GetAccount(opts.Account); err == nil && a.Agent == row.Harness {
			return a, nil
		} else if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
	}
	a, err := h.pick(row.Harness)
	if err != nil {
		return nil, err
	}
	if _, err := h.srv.setChatOption(row.ID, "account", a.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	return a, nil
}

// pick is the account a new chat of an agent runs on when nobody chose one.
func (h *accountHub) pick(agentName string) (*store.Account, error) {
	list, err := h.srv.store.ListAccounts()
	if err != nil {
		return nil, err
	}
	var first *store.Account
	views := []accountView{}
	for i := range list {
		if list[i].Agent != agentName {
			continue
		}
		a := h.refreshed(list[i])
		if first == nil {
			first = &a
		}
		v := viewOf(a, 0)
		v.Limits = current(storedLimits(&a), time.Now())
		views = append(views, v)
	}
	if id := recommendAccount(views, agentName, "", time.Now()); id != "" {
		return h.srv.store.GetAccount(id)
	}
	if first != nil {
		return first, nil
	}
	return h.ensureDefault(agentName)
}

// ensureDefault makes the Default account of an agent that has none.
func (h *accountHub) ensureDefault(agentName string) (*store.Account, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	list, err := h.srv.store.ListAccounts()
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].Agent == agentName {
			return &list[i], nil
		}
	}
	a := &store.Account{ID: accounts.NewID(), Agent: agentName, Label: accounts.DefaultLabel,
		Kind: accounts.Subscription, State: accounts.NeedsSignIn}
	if err := accounts.Prepare(h.srv.dataDir, a); err != nil {
		return nil, err
	}
	if err := h.srv.store.CreateAccount(a); err != nil {
		return nil, err
	}
	refreshed := h.refreshed(*a)
	return &refreshed, nil
}

// chatEnv is a chat's environment on an account: the account's folders made,
// its whole environment without the key, and the key's variables sealed for
// the chat.
func (h *accountHub) chatEnv(a *store.Account, chatID string) ([]string, string, error) {
	if err := accounts.Prepare(h.srv.dataDir, a); err != nil {
		return nil, "", fmt.Errorf("prepare the account's folder: %w", err)
	}
	env := accounts.Env(h.srv.dataDir, a, os.Environ())
	if len(a.Secret) == 0 {
		return env, "", nil
	}
	keys, err := h.keys()
	if err != nil {
		return nil, "", err
	}
	key, err := keys.Open(a.ID, a.Secret)
	if err != nil {
		return nil, "", fmt.Errorf("the account's key cannot be opened: %w", err)
	}
	sealed, err := agenthost.SealEnv(keys.Key(), chatID, accounts.SecretEnv(a.Agent, a.Provider, key))
	if err != nil {
		return nil, "", err
	}
	return env, sealed, nil
}

// --------------------------------------------------- what the chats report

// accountOfTurn is the account a chat's journal says it runs on: the
// running or last turn's, else the last switch's, else the row's.
func accountOfTurn(p *agent.Projection, row *store.Session) string {
	if p.Turn != nil && p.Turn.Account != "" {
		return p.Turn.Account
	}
	if p.Account != "" {
		return p.Account
	}
	if row != nil {
		return readChatOptions(row.Options).Account
	}
	return ""
}

// noteRateLimit keeps a usage window a chat reported as its account's.
func (h *accountHub) noteRateLimit(id string, ev agent.Event) {
	if id == "" || ev.Window == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	a, err := h.srv.store.GetAccount(id)
	if err != nil {
		return
	}
	label := accounts.WindowLabel(ev.Window)
	windows := storedLimits(a)
	w := usageWindow{Label: label, UsedPercent: ev.Used * 100, ResetsAt: epochTime(ev.ResetsAt)}
	found := false
	for i := range windows {
		if windows[i].Label == label {
			windows[i], found = w, true
		}
	}
	if !found {
		windows = append(windows, w)
	}
	raw, _ := json.Marshal(windows)
	_ = h.srv.store.SetAccountLimits(id, raw, time.Now().UnixMilli())
}

// noteIdentity keeps who the agent said an account is (Claude's agent.info
// account), for the account's "i".
func (h *accountHub) noteIdentity(id string, content json.RawMessage) {
	if id == "" || len(content) == 0 {
		return
	}
	var info struct {
		Account *struct {
			Email string `json:"email"`
			Plan  string `json:"plan"`
		} `json:"account"`
	}
	if json.Unmarshal(content, &info) != nil || info.Account == nil {
		return
	}
	if info.Account.Email == "" && info.Account.Plan == "" {
		return
	}
	_ = h.srv.store.SetAccountIdentity(id, info.Account.Email, info.Account.Plan)
}

// noteTurnEnd is a chat's turn ending on an account: a completed one clears
// the limit the account had run into, and one the limit stopped switches the
// chat when it asked for that, or stops with the card and a push.
func (h *accountHub) noteTurnEnd(p *chatPeer, id string, ev agent.Event, limits map[string]agent.RateLimit) {
	if id == "" {
		return
	}
	if ev.Outcome != agent.OutcomeLimited {
		h.toldMu.Lock()
		delete(h.told, p.id)
		h.toldMu.Unlock()
	}
	switch ev.Outcome {
	case agent.OutcomeCompleted:
		if a, err := h.srv.store.GetAccount(id); err == nil {
			if a.LimitedUntil != 0 {
				_ = h.srv.store.SetAccountLimitedUntil(id, 0)
			}
			// A turn that got through says the login works after all.
			if accounts.Refused(a) {
				_ = h.srv.store.SetAccountState(id, accounts.SignedIn, "")
			}
		}
	case agent.OutcomeLimited:
		var until int64
		for _, l := range limits {
			if l.Used >= 1 && l.ResetsAt*1000 > until {
				until = l.ResetsAt * 1000
			}
		}
		if now := time.Now().UnixMilli(); until <= now {
			// No window says when it renews: the time guessed when the
			// account ran into it stands while it lasts.
			if a, err := h.srv.store.GetAccount(id); err == nil && a.LimitedUntil > now {
				until = a.LimitedUntil
			} else {
				until = time.Now().Add(unknownRenewal).UnixMilli()
			}
		}
		_ = h.srv.store.SetAccountLimitedUntil(id, until)
		go h.onLimit(p, id, ev.TurnID, until)
	}
}

// noteRefused is the provider refusing an account's sign-in or key while a
// chat used it: the account is out of use, saying why, until it signs in
// again, gets a new key, or a turn on it gets through.
func (h *accountHub) noteRefused(id string) {
	a, err := h.srv.store.GetAccount(id)
	if err != nil || accounts.Refused(a) {
		return
	}
	detail := accounts.RefusedDetail
	if a.Kind == accounts.APIKey {
		detail = accounts.KeyRefusedDetail
	}
	if err := h.srv.store.SetAccountState(id, accounts.Failed, detail); err != nil {
		log.Printf("accounts: %s: %v", id, err)
		return
	}
	h.srv.events.poke()
}

// onLimit is REBUILD.md §4.3's limit: with the chat's automatic switch on,
// the chat goes on with the recommended account of its agent that still has
// headroom, telling it to continue; otherwise - or with no such account -
// it stops with the card, and the person is told.
func (h *accountHub) onLimit(p *chatPeer, from, turnID string, until int64) {
	row, err := h.srv.store.GetSession(p.id)
	if err != nil {
		return
	}
	fromLabel := from
	if a, err := h.srv.store.GetAccount(from); err == nil {
		fromLabel = a.Label
	}
	if readChatOptions(row.Options).AutoSwitch {
		ctx, cancel := context.WithTimeout(context.Background(), limitsWait+time.Minute)
		defer cancel()
		views, _, err := h.views(ctx)
		if err == nil {
			if to := recommendAccount(views, row.Harness, from, time.Now()); to != "" {
				target, err := h.srv.store.GetAccount(to)
				if err == nil {
					code, answer := h.switchChat(ctx, p, row, target, "auto-"+turnID, agenthost.WhenNow, true,
						fmt.Sprintf("The limit of %s was reached.", fromLabel))
					if code < 300 {
						return
					}
					log.Printf("chats: %s: the automatic switch to %s: %v", p.id, to, answer)
				}
			}
		}
	}
	// A chat filed background is there so as not to interrupt anyone: its
	// limit is no notification, as no turn's end of it is (pusher.observe).
	if row.Status == store.StatusBackground {
		return
	}
	h.toldMu.Lock()
	again := h.told[p.id] == from
	h.told[p.id] = from
	h.toldMu.Unlock()
	if again {
		return
	}
	body := "Limit reached"
	if left := time.Until(time.UnixMilli(until)); left > 0 {
		body += " — renews in " + renewsIn(left)
	}
	body += " (" + fromLabel + ")"
	h.srv.push.notify(p.id, row.Title, body, pushFinished)
	// The page's own notification, on a device without Web Push, says the
	// same at the same moment: the turn's end was no news (newsOf).
	h.srv.events.publish(map[string]any{"t": "news", "id": p.id, "body": body, "kind": pushFinished})
}

// renewsIn is a wait as "2 h 10 min".
func renewsIn(d time.Duration) string {
	minutes := int((d + time.Minute - 1) / time.Minute)
	if minutes < 60 {
		return fmt.Sprintf("%d min", minutes)
	}
	if minutes%60 == 0 {
		return fmt.Sprintf("%d h", minutes/60)
	}
	return fmt.Sprintf("%d h %d min", minutes/60, minutes%60)
}

// switchChat sends a chat the switch to an account, through its outbox like
// every command.
func (h *accountHub) switchChat(ctx context.Context, p *chatPeer, row *store.Session, target *store.Account,
	clientID, when string, cont bool, reason string) (int, map[string]any) {
	env, sealed, err := h.chatEnv(target, row.ID)
	if err != nil {
		return http.StatusInternalServerError, map[string]any{"error": err.Error()}
	}
	return p.command(ctx, agenthost.Command{Op: agenthost.OpSwitchAccount, ClientID: clientID, Account: target.ID,
		Env: env, SecretEnv: sealed, Reason: reason, When: when, Continue: cont})
}

// ------------------------------------------------------------- the routes

func (s *Server) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	views, recommended, err := s.accounts.views(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": views, "recommended": recommended, "agents": accounts.Agents()})
}

// accountOf loads the account a request names, answering 404 itself.
func (s *Server) accountOf(w http.ResponseWriter, r *http.Request) (*store.Account, bool) {
	a, err := s.store.GetAccount(r.PathValue("id"))
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "there is no such account")
		return nil, false
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	return a, true
}

func (s *Server) answerAccount(w http.ResponseWriter, code int, id string) {
	a, err := s.store.GetAccount(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	counts, _ := s.accounts.chatCounts()
	v := viewOf(s.accounts.refreshed(*a), counts[a.ID])
	v.Limits = current(storedLimits(a), time.Now())
	writeJSON(w, code, map[string]any{"account": v})
}

type accountBody struct {
	Agent    string  `json:"agent"`
	Kind     string  `json:"kind"`
	Label    *string `json:"label"`
	Key      *string `json:"key"`
	Provider *string `json:"provider"`
}

func (s *Server) handleCreateAccount(w http.ResponseWriter, r *http.Request) {
	var body accountBody
	if !readJSON(w, r, &body) {
		return
	}
	if !accounts.ValidAgent(body.Agent) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("%q has no accounts", body.Agent))
		return
	}
	if body.Kind != accounts.Subscription && body.Kind != accounts.APIKey {
		writeError(w, http.StatusBadRequest, "an account is a subscription or an API key")
		return
	}
	raw := ""
	if body.Label != nil {
		raw = *body.Label
	}
	label, err := accounts.Label(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a := &store.Account{ID: accounts.NewID(), Agent: body.Agent, Label: label, Kind: body.Kind, State: accounts.NeedsSignIn}
	if body.Kind == accounts.APIKey {
		if status, msg := checkKey(a, body); status != 0 {
			writeError(w, status, msg)
			return
		}
	}
	if err := accounts.Prepare(s.dataDir, a); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.accounts.mu.Lock()
	err = s.store.CreateAccount(a)
	s.accounts.mu.Unlock()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if body.Kind == accounts.APIKey {
		if status, msg := s.accounts.setKey(r.Context(), a, *body.Key, providerOf(body)); status != 0 {
			// The account is there, and says why it cannot be used; the
			// key can be typed again.
			_ = s.store.SetAccountState(a.ID, accounts.Failed, msg)
		}
	}
	s.answerAccount(w, http.StatusCreated, a.ID)
}

// checkKey is the rules an API key is held to.
func checkKey(a *store.Account, body accountBody) (int, string) {
	if body.Key == nil || strings.TrimSpace(*body.Key) == "" {
		return http.StatusBadRequest, "an API-key account needs its key"
	}
	if a.Agent == accounts.OpenCode && providerOf(body) == "" {
		return http.StatusBadRequest, "an OpenCode key needs the provider it is for, such as openrouter"
	}
	return 0, ""
}

func providerOf(body accountBody) string {
	if body.Provider == nil {
		return ""
	}
	return strings.TrimSpace(*body.Provider)
}

// setKey keeps an API-key account's key, sealed, and for Codex has codex log
// it in into the account's home, where codex keeps it itself.
func (h *accountHub) setKey(ctx context.Context, a *store.Account, key, provider string) (int, string) {
	key = strings.TrimSpace(key)
	keys, err := h.keys()
	if err != nil {
		return http.StatusInternalServerError, err.Error()
	}
	sealed, err := keys.Seal(a.ID, key)
	if err != nil {
		return http.StatusInternalServerError, err.Error()
	}
	if err := h.srv.store.SetAccountSecret(a.ID, sealed, provider); err != nil {
		return http.StatusInternalServerError, err.Error()
	}
	a.Secret, a.Provider = sealed, provider
	if a.Agent == accounts.Codex {
		bin, env, err := h.codexCLI(a)
		if err == nil {
			lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			err = accounts.CodexKeyLogin(lctx, bin, env, key)
			cancel()
		}
		if err != nil {
			return http.StatusBadGateway, "Codex did not take the key: " + err.Error()
		}
	}
	_ = h.srv.store.SetAccountState(a.ID, accounts.SignedIn, "")
	return 0, ""
}

func (s *Server) handlePatchAccount(w http.ResponseWriter, r *http.Request) {
	a, ok := s.accountOf(w, r)
	if !ok {
		return
	}
	var body accountBody
	if !readJSON(w, r, &body) {
		return
	}
	if body.Label != nil {
		label, err := accounts.Label(*body.Label)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.store.RenameAccount(a.ID, label); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if body.Key != nil {
		if a.Kind != accounts.APIKey {
			writeError(w, http.StatusBadRequest, "a subscription signs in with Sign in, not with a key")
			return
		}
		if body.Provider == nil && a.Provider != "" {
			provider := a.Provider
			body.Provider = &provider
		}
		if status, msg := checkKey(a, body); status != 0 {
			writeError(w, status, msg)
			return
		}
		if status, msg := s.accounts.setKey(r.Context(), a, *body.Key, providerOf(body)); status != 0 {
			_ = s.store.SetAccountState(a.ID, accounts.Failed, msg)
			writeError(w, status, msg)
			return
		}
	}
	s.answerAccount(w, http.StatusOK, a.ID)
}

// handleDeleteAccount removes an account and its folder. An account a chat
// runs on is refused rather than pulled out from under it, and so is one the
// migration moved an agent's own sign-in into: ~/.claude, ~/.codex and the
// OpenCode folders are links to it, and every agent started by hand on this
// machine signs in with it. One made of an old profile goes like any other,
// with the link the migration left in the profile's place.
func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	a, ok := s.accountOf(w, r)
	if !ok {
		return
	}
	s.accounts.mu.Lock()
	defer s.accounts.mu.Unlock()
	counts, err := s.accounts.chatCounts()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n := counts[a.ID]; n > 0 {
		writeError(w, http.StatusConflict, fmt.Sprintf("%d chat(s) run on %s; move them to another account or delete them first", n, a.Label))
		return
	}
	if accounts.HomeOrigin(a.Origin) {
		writeError(w, http.StatusConflict, fmt.Sprintf("%s holds the sign-in the agent itself uses on this machine (%s), so it stays", a.Label, a.Origin))
		return
	}
	s.accounts.signins.Cancel(a.ID)
	if err := s.store.DeleteAccount(a.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.RemoveAll(accounts.Folder(s.dataDir, a.Agent, a.ID)); err != nil {
		writeError(w, http.StatusInternalServerError, "the account is gone from the list, but its folder could not be removed: "+err.Error())
		return
	}
	if link := accounts.ProfileLink(s.dataDir, a); link != "" {
		if fi, err := os.Lstat(link); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			_ = os.Remove(link)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleReorderAccounts(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Order []string `json:"order"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	s.accounts.mu.Lock()
	err := s.store.ReorderAccounts(body.Order)
	s.accounts.mu.Unlock()
	switch {
	case errors.Is(err, store.ErrOrderMismatch):
		writeError(w, http.StatusConflict, "the accounts have changed; load the list again")
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
	default:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// handleAccountSignIn starts an account's sign-in: Claude Code's own
// `claude auth login` on a terminal of the server's (the page it shows, and
// the code that page gives typed into it), a device code for Codex, a
// provider's OAuth for OpenCode (first the list of providers, then, with
// provider and method, the page).
// The scripted agent has nothing to sign in to.
func (s *Server) handleAccountSignIn(w http.ResponseWriter, r *http.Request) {
	a, ok := s.accountOf(w, r)
	if !ok {
		return
	}
	var body struct {
		Provider string            `json:"provider"`
		Method   *int              `json:"method"`
		Inputs   map[string]string `json:"inputs"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if a.Kind != accounts.Subscription {
		writeError(w, http.StatusBadRequest, "an API-key account signs in with its key")
		return
	}
	onDone := func(err error) {
		if err != nil {
			log.Printf("accounts: the sign-in of %s: %v", a.ID, err)
			return
		}
		_ = s.store.SetAccountState(a.ID, accounts.SignedIn, "")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	switch a.Agent {
	case accounts.Claude:
		if err := accounts.Prepare(s.dataDir, a); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		binary := "claude"
		if entry, ok := s.Settings().Harnesses.Entry(accounts.Claude); ok && strings.TrimSpace(entry.Binary) != "" {
			binary = strings.TrimSpace(entry.Binary)
		}
		env := accounts.Env(s.dataDir, a, os.Environ())
		flow, err := s.accounts.signins.StartClaude(ctx, a.ID, binary, env, onDone)
		if err != nil {
			writeError(w, http.StatusBadGateway, "Claude Code did not start a sign-in: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"signin": flow})
	case accounts.Codex:
		bin, env, err := s.accounts.codexCLI(a)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		flow, err := s.accounts.signins.StartCodex(ctx, a.ID, bin, env, onDone)
		if err != nil {
			writeError(w, http.StatusBadGateway, "Codex did not start a sign-in: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"signin": flow})
	case accounts.OpenCode:
		if body.Provider != "" && body.Method != nil {
			flow, err := s.accounts.signins.Choose(ctx, a.ID, body.Provider, *body.Method, body.Inputs)
			if err != nil {
				writeError(w, http.StatusBadGateway, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"signin": flow})
			return
		}
		if err := accounts.Prepare(s.dataDir, a); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		env := accounts.Env(s.dataDir, a, os.Environ())
		entry, _ := s.Settings().Harnesses.Entry(accounts.OpenCode)
		flow, err := s.accounts.signins.StartOpenCode(ctx, s.dataDir, a.ID, env, strings.TrimSpace(entry.Binary), onDone)
		if err != nil {
			writeError(w, http.StatusBadGateway, "OpenCode did not start a sign-in: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"signin": flow})
	case accounts.Fake:
		_ = s.store.SetAccountState(a.ID, accounts.SignedIn, "")
		writeJSON(w, http.StatusOK, map[string]any{"signin": accounts.Flow{Account: a.ID, Agent: a.Agent,
			State: accounts.FlowDone, Started: time.Now().UnixMilli()}})
	default:
		writeError(w, http.StatusBadRequest, "this account has nothing to sign in to")
	}
}

func (s *Server) handleGetAccountSignIn(w http.ResponseWriter, r *http.Request) {
	a, ok := s.accountOf(w, r)
	if !ok {
		return
	}
	flow, found := s.accounts.signins.Get(a.ID)
	if !found {
		writeError(w, http.StatusNotFound, "no sign-in was started for this account")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"signin": flow})
}

func (s *Server) handleAccountSignInCode(w http.ResponseWriter, r *http.Request) {
	a, ok := s.accountOf(w, r)
	if !ok {
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	flow, err := s.accounts.signins.Code(ctx, a.ID, body.Code)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"signin": flow})
}

func (s *Server) handleCancelAccountSignIn(w http.ResponseWriter, r *http.Request) {
	a, ok := s.accountOf(w, r)
	if !ok {
		return
	}
	s.accounts.signins.Cancel(a.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
