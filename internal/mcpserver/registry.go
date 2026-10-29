package mcpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// Store is where the Registry keeps the accounts: the server's key-value
// table. GetKV gives "" for a key that is not there.
type Store interface {
	GetKV(key string) (string, error)
	SetKV(key, value string) error
}

// Sealer encrypts what the Registry stores, bound to a name: the accounts
// hold the keys of other services.
type Sealer interface {
	Seal(object string, plain []byte) ([]byte, error)
	Open(object string, sealed []byte) ([]byte, error)
}

// Registry is the MCP servers built into this Socrates and the accounts
// Admin keeps for them.
type Registry struct {
	store  Store
	sealer Sealer

	mu       sync.Mutex
	servers  []Server
	accounts map[string][]Account // read once per server, then kept
}

// NewRegistry is a registry without servers; store and sealer may be nil
// for one that keeps no accounts (a test, `socrates tests check`).
func NewRegistry(store Store, sealer Sealer) *Registry {
	return &Registry{store: store, sealer: sealer, accounts: map[string][]Account{}}
}

// Register adds a server. A second server of a name is a programming error.
func (r *Registry) Register(s Server) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !NameRE.MatchString(s.Name()) {
		panic("mcpserver: bad server name " + s.Name())
	}
	for _, have := range r.servers {
		if have.Name() == s.Name() {
			panic("mcpserver: two servers named " + s.Name())
		}
	}
	r.servers = append(r.servers, s)
	slices.SortFunc(r.servers, func(a, b Server) int { return strings.Compare(a.Name(), b.Name()) })
}

// Servers is every server, by name.
func (r *Registry) Servers() []Server {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Server(nil), r.servers...)
}

// Get is the server of a name, nil for none.
func (r *Registry) Get(name string) Server {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.servers {
		if s.Name() == name {
			return s
		}
	}
	return nil
}

// Has is whether a server of the name is built in.
func (r *Registry) Has(name string) bool { return r.Get(name) != nil }

// Call runs a tool of a server for a session.
func (r *Registry) Call(ctx context.Context, server string, s Session, tool string, args json.RawMessage) Result {
	srv := r.Get(server)
	if srv == nil {
		return Error("Socrates has no MCP server " + server + ".")
	}
	known := false
	for _, t := range srv.Tools() {
		if t.Name == tool {
			known = true
			break
		}
	}
	if !known {
		return Error(fmt.Sprintf("The MCP server %s has no tool %s.", server, tool))
	}
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}
	return srv.Call(ctx, s, tool, args)
}

// End ends what every server keeps for a session, and says what it closed.
func (r *Registry) End(ctx context.Context, session string) []string {
	var out []string
	for _, s := range r.Servers() {
		if e, ok := s.(Ender); ok {
			if what := e.End(ctx, session); what != "" {
				out = append(out, s.Name()+": "+what)
			}
		}
	}
	return out
}

// ------------------------------------------------------------- accounts

// kvAccounts is the key a server's accounts are stored under, sealed.
func kvAccounts(server string) string { return "mcp." + server + ".accounts" }

// Accounts is the accounts Admin keeps for a server, with their values, in
// the order they were added.
func (r *Registry) Accounts(server string) []Account {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	list, err := r.loadLocked(server)
	if err != nil {
		return nil
	}
	out := make([]Account, len(list))
	for i, a := range list {
		out[i] = a.clone()
	}
	return out
}

func (a Account) clone() Account {
	values := make(map[string]string, len(a.Values))
	for k, v := range a.Values {
		values[k] = v
	}
	a.Values = values
	if a.Check != nil {
		c := *a.Check
		a.Check = &c
	}
	return a
}

func (r *Registry) loadLocked(server string) ([]Account, error) {
	if list, ok := r.accounts[server]; ok {
		return list, nil
	}
	if r.store == nil {
		return nil, nil
	}
	raw, err := r.store.GetKV(kvAccounts(server))
	if err != nil {
		return nil, err
	}
	var list []Account
	if raw != "" {
		sealed, err := hex.DecodeString(raw)
		if err != nil {
			return nil, fmt.Errorf("the accounts of %s are damaged: %w", server, err)
		}
		plain, err := r.sealer.Open(kvAccounts(server), sealed)
		if err != nil {
			return nil, fmt.Errorf("the accounts of %s cannot be opened: %w", server, err)
		}
		if err := json.Unmarshal(plain, &list); err != nil {
			return nil, fmt.Errorf("the accounts of %s are damaged: %w", server, err)
		}
	}
	r.accounts[server] = list
	return list, nil
}

func (r *Registry) saveLocked(server string, list []Account) error {
	if r.store == nil || r.sealer == nil {
		return errors.New("accounts cannot be kept here")
	}
	plain, err := json.Marshal(list)
	if err != nil {
		return err
	}
	sealed, err := r.sealer.Seal(kvAccounts(server), plain)
	if err != nil {
		return err
	}
	if err := r.store.SetKV(kvAccounts(server), hex.EncodeToString(sealed)); err != nil {
		return err
	}
	r.accounts[server] = list
	return nil
}

// keeper is the server of a name that has accounts.
func (r *Registry) keeper(server string) (Accounts, error) {
	s := r.Get(server)
	if s == nil {
		return nil, fmt.Errorf("Socrates has no MCP server %q", server)
	}
	k, ok := s.(Accounts)
	if !ok {
		return nil, fmt.Errorf("the MCP server %s has no accounts", server)
	}
	return k, nil
}

// AddAccount checks an account and keeps it. One the service refuses is not
// kept; one that could not be checked (the service did not answer) is, with
// that check.
func (r *Registry) AddAccount(ctx context.Context, server string, values map[string]string) (Account, error) {
	k, err := r.keeper(server)
	if err != nil {
		return Account{}, err
	}
	clean := map[string]string{}
	for _, f := range k.AccountFields() {
		v := strings.TrimSpace(values[f.Name])
		if v == "" {
			return Account{}, fmt.Errorf("%s is missing", f.Label)
		}
		clean[f.Name] = v
	}
	label := k.AccountLabel(clean)
	r.mu.Lock()
	list, err := r.loadLocked(server)
	r.mu.Unlock()
	if err != nil {
		return Account{}, err
	}
	for _, a := range list {
		if k.AccountLabel(a.Values) == label {
			return Account{}, fmt.Errorf("there is an account %s already", label)
		}
	}
	check := k.CheckAccount(ctx, clean)
	check.At = time.Now().UnixMilli()
	if check.State == AccountInvalid {
		return Account{}, fmt.Errorf("%s refused the account: %s", r.Get(server).Title(), check.Detail)
	}
	a := Account{ID: newID(), Values: clean, Check: &check}
	r.mu.Lock()
	defer r.mu.Unlock()
	list, err = r.loadLocked(server)
	if err != nil {
		return Account{}, err
	}
	if err := r.saveLocked(server, append(slices.Clone(list), a)); err != nil {
		return Account{}, err
	}
	return a.clone(), nil
}

// RemoveAccount forgets an account. A lease on it stays until its holder
// gives it back.
func (r *Registry) RemoveAccount(server, id string) error {
	if _, err := r.keeper(server); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	list, err := r.loadLocked(server)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(list, func(a Account) bool { return a.ID == id })
	if i < 0 {
		return errors.New("there is no such account")
	}
	return r.saveLocked(server, slices.Delete(slices.Clone(list), i, i+1))
}

// CheckAccount asks the service about a kept account again and keeps what
// it said.
func (r *Registry) CheckAccount(ctx context.Context, server, id string) (Account, error) {
	k, err := r.keeper(server)
	if err != nil {
		return Account{}, err
	}
	var a *Account
	for _, have := range r.Accounts(server) {
		if have.ID == id {
			a = &have
			break
		}
	}
	if a == nil {
		return Account{}, errors.New("there is no such account")
	}
	check := k.CheckAccount(ctx, a.Values)
	check.At = time.Now().UnixMilli()
	r.mu.Lock()
	defer r.mu.Unlock()
	list, err := r.loadLocked(server)
	if err != nil {
		return Account{}, err
	}
	list = slices.Clone(list)
	for i := range list {
		if list[i].ID == id {
			list[i].Check = &check
			if err := r.saveLocked(server, list); err != nil {
				return Account{}, err
			}
			return list[i].clone(), nil
		}
	}
	return Account{}, errors.New("there is no such account")
}

func newID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
