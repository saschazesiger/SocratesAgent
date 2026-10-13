package harnesses

import (
	"strings"
	"unicode"
)

// The initial prompt of a prepared session is handed to the program on its
// command line - every one of the three CLIs takes a first prompt there and
// starts its interactive session with it already sent - but it does not travel
// through tmux to get there.
//
// tmux carries a new-session command to its server as one message, and a
// message is limited to about sixteen kilobytes. A prompt somebody spent an
// evening preparing is easily longer than that, and the failure is a session
// that does not start, with tmux's words about a command that is too long. So
// the prompt goes into a file in the session's directory, and the pane's
// command becomes a small POSIX shell script that reads it, deletes it, puts
// it into the argv where the harness said it belongs, and execs the program.
// The kernel's own limit on one argument is 128 KB, comfortably above the 100
// KB a draft may be.
//
// exec is not a detail: after it the pane's process is the CLI itself, not a
// shell with a child, and the activity detector reads the pane's process.
//
// The prompt is never interpreted by the shell. It is read with
// "$(cat file)" and only ever used quoted, so quotes, dollars, backticks and
// newlines in it reach the program byte for byte - except trailing newlines,
// which command substitution strips and which no prompt is the worse for.

// InitialPromptFile is the name of the file the prompt waits in. It lives in
// the session's own directory, which is 0700, and it is written 0600.
const InitialPromptFile = "initial-prompt"

// promptMarker stands in the argv where the prompt goes. The script replaces
// an argument that is exactly the marker, or that ends in "=" and the marker
// (OpenCode's --prompt=…), with the prompt.
const promptMarker = "@socrates-initial-prompt@"

// promptScript is the wrapper, run as
//
//	/bin/sh -c promptScript sh <prompt file> <argv...>
//
// It rebuilds "$@" one argument at a time, which is the only way POSIX sh has
// to replace one element of a list without word splitting anything.
const promptScript = `m='` + promptMarker + `'
f=$1; shift
p=$(cat -- "$f") || { echo "socrates: the initial prompt could not be read from $f" >&2; exit 1; }
rm -f -- "$f"
n=$#
for a in "$@"; do
  case $a in
    "$m") a=$p ;;
    *"=$m") a=${a%"$m"}$p ;;
  esac
  set -- "$@" "$a"
done
shift "$n"
exec "$@"`

// withInitialPrompt wraps a fresh plan so that the program starts with the
// prompt sent. place puts the marker into the program's argv where that
// program takes a prompt. An empty prompt leaves the plan exactly as it was:
// a prepared session with nothing typed starts like any other.
func withInitialPrompt(plan LaunchPlan, req PlanRequest, place func(argv []string) []string) LaunchPlan {
	if req.InitialPrompt == "" {
		return plan
	}
	path := SessionFile(req.DataDir, req.SessionID, InitialPromptFile)
	argv := place(append([]string(nil), plan.Argv...))
	plan.Argv = append([]string{"/bin/sh", "-c", promptScript, "sh", path}, argv...)
	plan.Files = append(plan.Files, GenFile{
		Path: path, Mode: 0o600, Data: []byte(req.InitialPrompt), Transient: true,
	})
	return plan
}

// trailingPrompt is Claude Code's and Codex's form: the prompt is the last,
// positional argument, after "--" so that a prompt starting with a dash is a
// prompt and not an option. Both parsers honour "--" - Codex says so itself
// when given a stray dash, `to pass '-x' as a value, use '-- -x'`.
func trailingPrompt(argv []string) []string {
	return append(argv, "--", promptMarker)
}

// guardSingleWord is the one extra guard Claude Code's prompt needs. Its
// parser (commander) still dispatches a subcommand from the first operand
// after "--", so a prompt that is exactly `update` or `doctor` would run that
// command instead of a session. A single word with a space after it is not
// the name of anything, and it is the same prompt to the model.
//
// The word is judged as the program will receive it, not as it was typed:
// the wrapper reads the prompt with $(cat), which strips trailing newlines,
// so `update` followed by an Enter pressed out of habit arrives as exactly
// `update`. The trailing newlines are dropped here for the same reason, and
// the guarding space goes where the program will see it.
func guardSingleWord(prompt string) string {
	p := strings.TrimRight(prompt, "\n")
	if p != "" && strings.IndexFunc(p, unicode.IsSpace) < 0 {
		return p + " "
	}
	return prompt
}

// openCodePrompt is OpenCode's form: --prompt=<text>, joined with "=" so
// that a prompt starting with a dash cannot be read as the next flag, and
// placed before the project path, which has to stay the last argument.
func openCodePrompt(argv []string) []string {
	flag := "--prompt=" + promptMarker
	if len(argv) < 2 {
		return append(argv, flag)
	}
	last := argv[len(argv)-1]
	return append(argv[:len(argv)-1:len(argv)-1], flag, last)
}
