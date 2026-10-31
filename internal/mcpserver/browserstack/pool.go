package browserstack

import (
	"context"
	"sync"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/mcpserver"
	"github.com/saschazesiger/SocratesAgent/internal/mcpserver/browserstack/driver"
)

// The pool: every account Admin keeps carries one session at a time
// (BrowserStack's plans of these accounts allow one parallel session), held
// by a lease. A chat leases when it starts a device and gives it back when
// it stops. Accounts BrowserStack
// itself calls busy (a team member uses one, outside Socrates) go last: a
// session started on one waits in BrowserStack's own queue.

// busyEvery is how long a reading of the accounts' plans is used.
const busyEvery = time.Minute

type pool struct {
	srv *Server

	mu      sync.Mutex
	leases  map[string]string // holder -> account id
	busy    map[string]bool   // account id -> every parallel session taken
	plans   map[string]*driver.Plan
	readAt  time.Time
	reading bool
	// planOf reads an account\'s plan (driver.PlanOf; a test\'s own).
	planOf func(context.Context, driver.Account) (*driver.Plan, error)
}

func newPool(srv *Server) *pool {
	return &pool{srv: srv, leases: map[string]string{}, busy: map[string]bool{}, plans: map[string]*driver.Plan{},
		planOf: driver.PlanOf}
}

// usable is the accounts a lease may take: every account Admin keeps but
// those BrowserStack refused at their last check.
func (p *pool) usable() []mcpserver.Account {
	var out []mcpserver.Account
	for _, a := range p.srv.accounts() {
		if a.Check != nil && a.Check.State == mcpserver.AccountInvalid {
			continue
		}
		out = append(out, a)
	}
	return out
}

// Lease takes a free account for holder: the one it holds, else the first
// free one BrowserStack does not call busy, else the first free one.
func (p *pool) Lease(holder string) (mcpserver.Lease, string) {
	list := p.usable()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refreshLocked()
	held := map[string]bool{}
	for h, id := range p.leases {
		held[id] = true
		if h == holder {
			for _, a := range list {
				if a.ID == id {
					return mcpserver.Lease{Account: id, Label: label(a.Values)}, ""
				}
			}
		}
	}
	if len(list) == 0 {
		return mcpserver.Lease{}, "BrowserStack has no account: add one in Admin, MCP servers"
	}
	for pass := 0; pass < 2; pass++ {
		for _, a := range list {
			if held[a.ID] || pass == 0 && p.busy[a.ID] {
				continue
			}
			p.leases[holder] = a.ID
			return mcpserver.Lease{Account: a.ID, Label: label(a.Values)}, ""
		}
	}
	return mcpserver.Lease{}, "every BrowserStack account is in use"
}

// Release gives back what holder holds.
func (p *pool) Release(holder string) {
	p.mu.Lock()
	delete(p.leases, holder)
	p.mu.Unlock()
}

// holderOf is who holds an account, "" for nobody.
func (p *pool) holderOf(account string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	for h, id := range p.leases {
		if id == account {
			return h
		}
	}
	return ""
}

// leaseOf is the account holder holds, "" for none.
func (p *pool) leaseOf(holder string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.leases[holder]
}

// refreshLocked reads the accounts' plans again in the background once the
// last reading is older than busyEvery: a lease never waits for the network.
func (p *pool) refreshLocked() {
	if p.reading || time.Since(p.readAt) < busyEvery {
		return
	}
	p.reading = true
	list, planOf := p.usable(), p.planOf
	go func() {
		busy := map[string]bool{}
		plans := map[string]*driver.Plan{}
		for _, a := range list {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			plan, err := planOf(ctx, account(a, 0))
			cancel()
			if err == nil && plan != nil {
				plans[a.ID] = plan
				busy[a.ID] = plan.Running >= plan.Max
			}
		}
		p.mu.Lock()
		p.busy, p.plans, p.readAt, p.reading = busy, plans, time.Now(), false
		p.mu.Unlock()
	}()
}

// plan is the last reading of an account's plan, nil for none.
func (p *pool) plan(id string) *driver.Plan {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refreshLocked()
	return p.plans[id]
}
