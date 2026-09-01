// Package claude speaks Claude Code's stream-json protocol.
//
// This file is the stub WP1 leaves behind so that the registry, the picker,
// the host and the engine can be built and tested end to end before the
// adapter itself exists. WP2 replaces the body; the Descriptor and the
// curated model list in discover.go are already the real ones.
package claude

import (
	"context"
	"errors"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harness"
)

func init() {
	harness.Register(harness.Descriptor{
		ID:            "claude",
		Label:         "Claude Code",
		Binary:        "claude",
		VersionArgs:   []string{"--version"},
		DefaultModel:  "sonnet",
		DefaultEffort: "medium",
		HasEffort:     true,
		New:           func() harness.Adapter { return &adapter{events: make(chan harness.Event)} },
		Discover:      Discover,
	})
}

// errNotImplemented is what every method of the stub returns. It reaches the
// user as a plain run error, which is the point: the whole path from the
// composer to a host process and back is exercised before there is an agent at
// the end of it.
var errNotImplemented = errors.New("the Claude Code adapter is not implemented yet")

type adapter struct {
	events chan harness.Event
	once   func()
}

func (a *adapter) Start(ctx context.Context, spec harness.Spec) error { return errNotImplemented }

func (a *adapter) Send(ctx context.Context, turnID, text string) error { return errNotImplemented }

func (a *adapter) Interrupt(ctx context.Context) error { return errNotImplemented }

func (a *adapter) Events() <-chan harness.Event { return a.events }

func (a *adapter) Close(ctx context.Context, grace time.Duration) error {
	// Events is closed exactly once, whatever happened before, because the
	// host's pump ranges over it and a channel left open is a host that never
	// exits.
	if a.once == nil {
		a.once = func() {}
		close(a.events)
	}
	return nil
}
