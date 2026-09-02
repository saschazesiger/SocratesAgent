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
	argv := []string{bin}
	// A login shell reads the profile that sets up PATH and the prompt, which
	// is what makes the pane look like the machine's own terminal.
	argv = switchFlag(argv, "-l", opts.Login)
	argv = append(argv, opts.ExtraArgs...)

	env := baseEnv(req)
	addExtraEnv(env, opts.ExtraEnv)

	return LaunchPlan{
		Argv:     argv,
		Env:      env,
		Cwd:      req.Cwd,
		Discover: DiscoverNone,
	}, nil
}

// ResumePlan says there is nothing to resume. A shell restarted in the same
// directory is the same shell as far as anybody is concerned.
func (Shell) ResumePlan(ctx context.Context, req PlanRequest) (LaunchPlan, error) {
	return LaunchPlan{}, ErrNoResume
}

// VerifyCLISession answers "gone", because a shell never had one.
func (Shell) VerifyCLISession(ctx context.Context, req PlanRequest) (bool, error) {
	return false, nil
}

// DiscoverModels answers that there is no model step for a shell.
func (Shell) DiscoverModels(ctx context.Context, bin string) (Catalog, error) {
	return Catalog{}, ErrNoModels
}
