// Package codex speaks Codex's JSON-RPC app-server protocol.
//
// This file is the stub WP1 leaves behind so that the registry, the picker,
// the host and the engine can be built and tested end to end before the
// adapter itself exists. WP3 replaces the body.
package codex

import (
	"context"
	"errors"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harness"
)

func init() {
	harness.Register(harness.Descriptor{
		ID:            "codex",
		Label:         "Codex",
		Binary:        "codex",
		VersionArgs:   []string{"--version"},
		DefaultModel:  "",
		DefaultEffort: "medium",
		HasEffort:     true,
		New:           func() harness.Adapter { return &adapter{events: make(chan harness.Event)} },
		Discover:      Discover,
	})
}

var errNotImplemented = errors.New("the Codex adapter is not implemented yet")

type adapter struct {
	events chan harness.Event
	once   func()
}

func (a *adapter) Start(ctx context.Context, spec harness.Spec) error { return errNotImplemented }

func (a *adapter) Send(ctx context.Context, turnID, text string) error { return errNotImplemented }

func (a *adapter) Interrupt(ctx context.Context) error { return errNotImplemented }

func (a *adapter) Events() <-chan harness.Event { return a.events }

func (a *adapter) Close(ctx context.Context, grace time.Duration) error {
	if a.once == nil {
		a.once = func() {}
		close(a.events)
	}
	return nil
}

// Discover asks a short-lived `codex app-server` for its model list. WP3
// implements it; until then an empty catalogue means "nothing discovered",
// which POST /api/chats treats as "accept the id and let the agent judge it".
func Discover(ctx context.Context, bin string) (harness.Catalog, error) {
	return harness.Catalog{}, errNotImplemented
}
