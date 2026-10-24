//go:build !windows

package accounts

import (
	"context"

	"github.com/saschazesiger/SocratesAgent/internal/agent/opencode"
)

// startOpenCodeSignIn starts a serve of the account's own for its sign-in.
func startOpenCodeSignIn(ctx context.Context, dataDir, id string, env []string, binary string) (openCodeSignIn, error) {
	s, err := opencode.StartSignIn(ctx, dataDir, id, env, binary)
	if err != nil {
		return nil, err
	}
	return ocSignIn{s}, nil
}

type ocSignIn struct{ s *opencode.SignIn }

func (o ocSignIn) Methods(ctx context.Context) (map[string][]AuthMethod, error) {
	all, err := o.s.Methods(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string][]AuthMethod{}
	for provider, methods := range all {
		for _, m := range methods {
			one := AuthMethod{Type: m.Type, Label: m.Label}
			for _, p := range m.Prompts {
				prompt := AuthPrompt{Type: p.Type, Key: p.Key, Message: p.Message, Placeholder: p.Placeholder}
				for _, opt := range p.Options {
					prompt.Options = append(prompt.Options, AuthOption{Label: opt.Label, Value: opt.Value, Hint: opt.Hint})
				}
				if p.When != nil {
					prompt.When = &AuthWhen{Key: p.When.Key, Op: p.When.Op, Value: p.When.Value}
				}
				one.Prompts = append(one.Prompts, prompt)
			}
			out[provider] = append(out[provider], one)
		}
	}
	return out, nil
}

func (o ocSignIn) Authorize(ctx context.Context, provider string, index int, inputs map[string]string) (string, string, string, error) {
	a, err := o.s.Authorize(ctx, provider, index, inputs)
	return a.URL, a.Method, a.Instructions, err
}

func (o ocSignIn) Callback(ctx context.Context, provider string, index int, code string) error {
	return o.s.Callback(ctx, provider, index, code)
}

func (o ocSignIn) Close() { o.s.Close() }
