package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harness"
	"github.com/saschazesiger/SocratesAgent/internal/proc"
)

// discoverTimeout bounds the whole spawn-handshake-ask-kill round trip.
const discoverTimeout = 20 * time.Second

// Discover asks a short-lived `codex app-server` for its model list. There is
// no offline source for it: the list depends on the account, so the CLI has to
// be asked. The process is killed as soon as the answer is in.
func Discover(ctx context.Context, bin string) (harness.Catalog, error) {
	ctx, cancel := context.WithTimeout(ctx, discoverTimeout)
	defer cancel()

	if bin == "" {
		bin = "codex"
	}
	cmd := exec.CommandContext(ctx, bin, "app-server", "--listen", "stdio://")
	cmd.Env = os.Environ()
	proc.Configure(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return harness.Catalog{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return harness.Catalog{}, err
	}
	cmd.Stderr = newTail(4 << 10)
	if err := cmd.Start(); err != nil {
		return harness.Catalog{}, fmt.Errorf("starting codex: %w", err)
	}
	// done is closed by the reader below; the deferred cleanup waits for it.
	done := make(chan struct{})
	defer func() {
		_ = stdin.Close()
		_ = proc.Terminate(cmd)
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		<-done // the reader is finished with the pipe Wait has just closed
	}()

	r := newRPC(stdin)
	go func() {
		defer close(done)
		sc := scanLines(stdout)
		for sc.Scan() {
			line := bytes.TrimSpace(sc.Bytes())
			if len(line) == 0 {
				continue
			}
			var fr frame
			if json.Unmarshal(line, &fr) != nil {
				continue
			}
			if fr.Method == "" && len(fr.ID) > 0 {
				r.deliver(&fr)
			}
			// Notifications and ServerRequests cannot happen before a thread
			// exists, and none of them would change the model list.
		}
		r.shutdown(errClosed)
	}()

	if _, err := r.call(ctx, "initialize", map[string]any{
		"clientInfo": map[string]any{"name": "socrates", "version": Version},
	}); err != nil {
		return harness.Catalog{}, fmt.Errorf("codex refused the handshake: %w", err)
	}
	if err := r.notify("initialized", map[string]any{}); err != nil {
		return harness.Catalog{}, err
	}
	raw, err := r.call(ctx, "model/list", map[string]any{})
	if err != nil {
		return harness.Catalog{}, fmt.Errorf("codex would not list its models: %w", err)
	}
	return parseModels(raw)
}

// modelEntry is one entry of model/list's data array.
type modelEntry struct {
	ID                        string            `json:"id"`
	DisplayName               string            `json:"displayName"`
	Description               string            `json:"description"`
	DefaultReasoningEffort    string            `json:"defaultReasoningEffort"`
	SupportedReasoningEfforts []json.RawMessage `json:"supportedReasoningEfforts"`
	IsDefault                 bool              `json:"isDefault"`
	Hidden                    bool              `json:"hidden"`
}

func parseModels(raw json.RawMessage) (harness.Catalog, error) {
	var res struct {
		Data []modelEntry `json:"data"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return harness.Catalog{}, fmt.Errorf("codex's model list is not what this adapter expects: %w", err)
	}
	cat := harness.Catalog{Models: make([]harness.Model, 0, len(res.Data))}
	for _, m := range res.Data {
		if m.ID == "" || m.Hidden {
			continue
		}
		label := m.DisplayName
		if label == "" {
			label = m.ID
		}
		efforts := harness.OrderEfforts(effortsOf(m.SupportedReasoningEfforts))
		// The default is carried over when it is one of the levels the model
		// lists, and medium stands in when the list does not name it.
		def := ""
		for _, e := range efforts {
			if e == m.DefaultReasoningEffort {
				def = e
			}
		}
		if def == "" && len(efforts) > 0 {
			for _, e := range efforts {
				if e == "medium" {
					def = e
				}
			}
		}
		cat.Models = append(cat.Models, harness.Model{
			ID: m.ID, Label: label, Hint: m.Description,
			Efforts: efforts, DefaultEffort: def, Default: m.IsDefault,
		})
	}
	return cat, nil
}

// effortsOf reads supportedReasoningEfforts, which the research printed both
// as an array of strings and as an array of objects with a reasoningEffort
// key (F-13). Both are accepted; the fake emits the harder one.
func effortsOf(parts []json.RawMessage) []string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := bytes.TrimSpace(p)
		if len(trimmed) == 0 {
			continue
		}
		if trimmed[0] == '"' {
			var s string
			if json.Unmarshal(trimmed, &s) == nil && s != "" {
				out = append(out, s)
			}
			continue
		}
		var obj struct {
			ReasoningEffort string `json:"reasoningEffort"`
		}
		if json.Unmarshal(trimmed, &obj) == nil && obj.ReasoningEffort != "" {
			out = append(out, obj.ReasoningEffort)
		}
	}
	return out
}
