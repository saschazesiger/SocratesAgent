package harnesses

import (
	"context"
	"os"
	"strings"
)

// Shell is a plain login shell in a working directory. It is the harness with
// nothing to configure and nothing to resume, and it is the one that proves
// the terminal works without any agent in the way.
type Shell struct{}

func (Shell) Kind() Kind            { return KindShell }
func (Shell) Label() string         { return "Shell" }
func (Shell) VersionArgs() []string { return []string{"--version"} }

// DefaultBinary is the user's own shell, then the two that are on nearly every
// machine. A shell is never "not installed": there is always something to fall
// back to.
func (Shell) DefaultBinary() string {
	for _, candidate := range []string{strings.TrimSpace(os.Getenv("SHELL")), "/bin/bash", "/bin/sh"} {
		if candidate == "" {
			continue
		}
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return "/bin/sh"
}

// Plan is the whole of the shell launcher. If the resolved program is not one
// of the shells this app has heard of it is still run: the user configured it,
// and they know what they configured.
func (s Shell) Plan(ctx context.Context, req PlanRequest) (LaunchPlan, error) {
	opts := req.Settings.Harnesses.Shell
	bin, err := resolveBinary(opts.Binary, s.DefaultBinary())
	if err != nil {
		return LaunchPlan{}, err
	}
	// A login shell reads the profile that sets up PATH and the prompt, which
	// is what makes the pane look like the machine's own terminal. It is not a
	// setting: a shell that did not read the profile is a shell whose PATH is
	// missing the very programs the person opened it for.
	argv := []string{bin, "-l"}

	env := baseEnv(req)

	// A shell that signs an account in runs the agent's own program first,
	// and is the person's shell after it, in the account's environment:
	// what went wrong can be read, and the sign-in typed again.
	if req.SignIn != nil && len(req.SignIn.Command) > 0 {
		for k, v := range req.SignIn.Env {
			env[k] = v
		}
		quoted := make([]string, len(req.SignIn.Command))
		for i, word := range req.SignIn.Command {
			quoted[i] = shellQuote(word)
		}
		argv = []string{bin, "-l", "-c", strings.Join(quoted, " ") + "; exec " + shellQuote(bin) + " -l"}
	}

	return LaunchPlan{Argv: argv, Env: env, Cwd: req.Cwd}, nil
}

// shellQuote is word as one single-quoted word of a POSIX shell.
func shellQuote(word string) string {
	return "'" + strings.ReplaceAll(word, "'", `'\''`) + "'"
}

// DiscoverModels answers that there is no model step for a shell.
func (Shell) DiscoverModels(ctx context.Context, bin string) (Catalog, error) {
	return Catalog{}, ErrNoModels
}
