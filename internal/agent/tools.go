package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/config"
	"github.com/saschazesiger/SocratesAgent/internal/openrouter"
	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/term"
)

// The orchestrator's whole capability set is a terminal. Everything else -
// running a build, driving Claude Code, reading a file - is something you do
// *in* a terminal, which is why there is no tool per coding agent.
const (
	toolShellRun      = "shell_run"
	toolTerminalOpen  = "terminal_open"
	toolTerminalSend  = "terminal_send"
	toolTerminalWait  = "terminal_wait"
	toolTerminalRead  = "terminal_read"
	toolTerminalClose = "terminal_close"
	toolAsk           = "ask_user"
)

// buildTools describes those capabilities to the model.
func buildTools(s config.Settings) []openrouter.Tool {
	fn := func(name, description, params string) openrouter.Tool {
		return openrouter.Tool{
			Type: "function",
			Function: openrouter.ToolFunction{
				Name:        name,
				Description: description,
				Parameters:  json.RawMessage(params),
			},
		}
	}

	var openDesc strings.Builder
	openDesc.WriteString("Start a program in a new terminal session and keep it running. Use it for " +
		"anything interactive: a coding agent, a REPL, a dev server, a command that will ask questions. " +
		"Give either `tool` (one of the configured programs below, started with the right arguments for you) " +
		"or `command` (any command line), not both. With neither you get a plain interactive shell.\n" +
		"The session stays open across your turns and across messages from the user, so open one agent " +
		"session and keep talking to it rather than starting a new one for every instruction.\n" +
		"A coding agent is always started with `tool`, never as a `command` in a headless mode: no `-p`, " +
		"`--print`, `--output-format`, `codex exec`, `opencode run`, no prompt piped in on stdin. Those are " +
		"refused. The user is watching this terminal and wants to be able to take the keyboard, which only " +
		"the real interactive session gives them.")
	enabled := s.EnabledTools()
	if len(enabled) > 0 {
		openDesc.WriteString("\n\nConfigured programs for `tool`:\n")
		for _, t := range enabled {
			fmt.Fprintf(&openDesc, "- %s (%s): %s\n", t.ID, t.Name, strings.TrimSpace(t.Description))
		}
	}
	ids := make([]string, 0, len(enabled))
	for _, t := range enabled {
		ids = append(ids, t.ID)
	}
	enumJSON, _ := json.Marshal(ids)
	toolProperty := `"tool": {"type": "string", "description": "One of the configured programs."},`
	if len(ids) > 0 {
		toolProperty = fmt.Sprintf(`"tool": {"type": "string", "enum": %s, "description": "One of the configured programs."},`, enumJSON)
	}

	keyList := term.KeyNames()
	sort.Strings(keyList)

	return []openrouter.Tool{
		fn(toolShellRun,
			"Run one shell command and wait for it to finish. This is the quick path for anything that "+
				"does not need a conversation: git, ls, cat, grep, npm, a build, a test run. It returns the "+
				"exit code and the output. Do not use it for a program that will ask you something or that "+
				"never exits - open a terminal session for those.\n"+
				"Never run a coding agent (claude, codex, opencode) here, not even in a print, exec or "+
				"--output-format mode and not with a prompt piped in: it is refused. Those programs are only "+
				"ever used interactively through terminal_open, because the user is watching the session and "+
				"wants to be able to take over.",
			`{
  "type": "object",
  "properties": {
    "command": {"type": "string", "description": "The command line, interpreted by the shell."},
    "directory": {"type": "string", "description": "Where to run it. Relative paths are resolved against the chat's working directory. Optional."},
    "timeout_seconds": {"type": "integer", "description": "How long to allow, 1-1800. Default 120."}
  },
  "required": ["command"],
  "additionalProperties": false
}`),

		fn(toolTerminalOpen, openDesc.String(), `{
  "type": "object",
  "properties": {
    `+toolProperty+`
    "command": {"type": "string", "description": "Any command line to start instead of a configured program."},
    "name": {"type": "string", "description": "Short label shown to the user, for example \"claude · refactor auth\"."},
    "directory": {"type": "string", "description": "Working directory. Relative paths are resolved against the chat's working directory. Optional."}
  },
  "additionalProperties": false
}`),

		fn(toolTerminalSend,
			"Type into a terminal session, exactly as a person at the keyboard would. Use `text` for what "+
				"you want to type and `keys` for anything that is not a character: enter, escape, arrow keys, "+
				"ctrl+c. When you send both, the text is typed first. Setting `submit` presses enter after the "+
				"text, which is how you hand a brief to a coding agent. Always look at the screen the call "+
				"returns before you send the next thing.\n"+
				"Key names: "+strings.Join(keyList, ", ")+", plus ctrl+<letter>, alt+<letter> and any single character.",
			`{
  "type": "object",
  "properties": {
    "session": {"type": "string", "description": "The session id from terminal_open."},
    "text": {"type": "string", "description": "Characters to type."},
    "keys": {"type": "array", "items": {"type": "string"}, "description": "Keys to press, in order."},
    "submit": {"type": "boolean", "description": "Press enter after the text. Default false."},
    "settle_seconds": {"type": "integer", "description": "How long to let the screen settle before reading it back, 0-30. Default 2."}
  },
  "required": ["session"],
  "additionalProperties": false
}`),

		fn(toolTerminalWait,
			"Wait for a terminal session and return its screen. With `until` set to \"idle\" (the default) "+
				"it returns once the program has stopped printing for a while, which is how you tell that a "+
				"coding agent has finished its turn. With `until` set to \"text\" it returns as soon as the "+
				"screen matches `text`, which is a regular expression - useful for catching a specific "+
				"question. Trailing spaces are trimmed from every line, so do not match on them.",
			`{
  "type": "object",
  "properties": {
    "session": {"type": "string", "description": "The session id."},
    "until": {"type": "string", "enum": ["idle", "text"], "description": "What to wait for. Default idle."},
    "text": {"type": "string", "description": "Regular expression to look for, when until is \"text\"."},
    "seconds": {"type": "integer", "description": "How long to wait at most, 1-3600. Defaults to the program's configured timeout."},
    "quiet_seconds": {"type": "integer", "description": "How long the program must stay silent to count as idle, 1-120. Defaults to the program's setting."}
  },
  "required": ["session"],
  "additionalProperties": false
}`),

		fn(toolTerminalRead,
			"Look at a terminal session without touching it. Returns the screen as it is right now. "+
				"Ask for the transcript as well when the answer you need has already scrolled off the screen.",
			`{
  "type": "object",
  "properties": {
    "session": {"type": "string", "description": "The session id."},
    "transcript": {"type": "boolean", "description": "Also return everything the program has printed, with the screen drawing removed. Default false."}
  },
  "required": ["session"],
  "additionalProperties": false
}`),

		fn(toolTerminalClose,
			"End a terminal session. The program is asked to quit the polite way first. Close sessions you "+
				"are finished with, but leave one open if you are likely to give it more work in this chat.",
			`{
  "type": "object",
  "properties": {
    "session": {"type": "string", "description": "The session id."}
  },
  "required": ["session"],
  "additionalProperties": false
}`),

		fn(toolAsk,
			"Ask the user a question and wait for their answer. Use it when a decision is genuinely theirs "+
				"to make, and offer 2 to 4 short options. The question and the options may be read out loud.",
			`{
  "type": "object",
  "properties": {
    "question": {"type": "string", "description": "The question, one sentence."},
    "options": {
      "type": "array",
      "maxItems": 4,
      "description": "Selectable answers. Keep every label to a few words.",
      "items": {
        "type": "object",
        "properties": {
          "label": {"type": "string"},
          "description": {"type": "string", "description": "Optional one line explanation."}
        },
        "required": ["label"],
        "additionalProperties": false
      }
    },
    "allow_free_text": {"type": "boolean", "description": "Also let the user type their own answer. Default true."}
  },
  "required": ["question"],
  "additionalProperties": false
}`),
	}
}

// execTool runs one tool call and returns the string handed back to the model.
func (e *Engine) execTool(ctx context.Context, chat *store.Chat, run *store.Run, call openrouter.ToolCall) string {
	args := orDefault(call.Function.Arguments, "{}")
	switch call.Function.Name {
	case toolShellRun:
		return e.execShellRun(ctx, chat, run, args)
	case toolTerminalOpen:
		return e.execTerminalOpen(ctx, chat, run, args)
	case toolTerminalSend:
		return e.execTerminalSend(ctx, chat, args)
	case toolTerminalWait:
		return e.execTerminalWait(ctx, chat, args)
	case toolTerminalRead:
		return e.execTerminalRead(ctx, chat, args)
	case toolTerminalClose:
		return e.execTerminalClose(ctx, chat, args)
	case toolAsk:
		return e.execAsk(ctx, run, args)
	default:
		return fmt.Sprintf("There is no tool called %q.", call.Function.Name)
	}
}

func badArgs(err error, hint string) string {
	return fmt.Sprintf("Could not parse the arguments (%v). %s", err, hint)
}

func (e *Engine) execShellRun(ctx context.Context, chat *store.Chat, run *store.Run, raw string) string {
	var args struct {
		Command   string `json:"command"`
		Directory string `json:"directory"`
		Timeout   int    `json:"timeout_seconds"`
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return badArgs(err, "Send valid JSON with a `command`.")
	}
	if strings.TrimSpace(args.Command) == "" {
		return "The `command` argument was empty."
	}
	if refusal := guardShellCommand(args.Command, newHarnessRules(e.Settings())); refusal != "" {
		return refusal
	}
	timeout := time.Duration(args.Timeout) * time.Second
	if args.Timeout <= 0 {
		timeout = 2 * time.Minute
	}
	if timeout > 30*time.Minute {
		timeout = 30 * time.Minute
	}
	result, err := e.runShellCommand(ctx, chat, run, args.Command, args.Directory, timeout)
	if err != nil {
		if ctx.Err() != nil {
			return "The run was stopped by the user."
		}
		return fmt.Sprintf("The command could not be run: %v", err)
	}
	return result
}

func (e *Engine) execTerminalOpen(ctx context.Context, chat *store.Chat, run *store.Run, raw string) string {
	var args struct {
		Tool      string `json:"tool"`
		Command   string `json:"command"`
		Name      string `json:"name"`
		Directory string `json:"directory"`
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return badArgs(err, "Send valid JSON with a `tool` or a `command`.")
	}
	if strings.TrimSpace(args.Tool) != "" && strings.TrimSpace(args.Command) != "" {
		return "Give either `tool` or `command`, not both."
	}
	if strings.TrimSpace(args.Command) != "" {
		if refusal := guardTerminalCommand(args.Command, newHarnessRules(e.Settings())); refusal != "" {
			return refusal
		}
	}
	return e.openTerminal(ctx, chat, run, args.Tool, args.Command, args.Name, args.Directory)
}

func (e *Engine) execTerminalSend(ctx context.Context, chat *store.Chat, raw string) string {
	var args struct {
		Session string   `json:"session"`
		Text    string   `json:"text"`
		Keys    []string `json:"keys"`
		Submit  bool     `json:"submit"`
		Settle  *int     `json:"settle_seconds"`
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return badArgs(err, "Send valid JSON with a `session`.")
	}
	handle, problem := e.session(chat, args.Session)
	if problem != "" {
		return problem
	}
	if args.Text == "" && len(args.Keys) == 0 && !args.Submit {
		return "Nothing to send: give `text`, `keys` or `submit`."
	}
	retyped := false
	if args.Text != "" {
		var err error
		retyped, err = typeInto(ctx, handle, args.Text)
		if err != nil {
			return fmt.Sprintf("Could not type into %s: %v", args.Session, err)
		}
	}
	if args.Submit {
		if err := handle.SendKeys(ctx, []string{"enter"}); err != nil {
			return fmt.Sprintf("Could not press enter in %s: %v", args.Session, err)
		}
	}
	if len(args.Keys) > 0 {
		if err := handle.SendKeys(ctx, args.Keys); err != nil {
			return fmt.Sprintf("Could not press those keys in %s: %v", args.Session, err)
		}
	}
	settle := 2 * time.Second
	if args.Settle != nil {
		settle = clampDuration(time.Duration(*args.Settle)*time.Second, 0, 30*time.Second)
	}
	if settle > 0 {
		_, _, _ = handle.WaitIdle(ctx, 400*time.Millisecond, settle)
	}
	note := ""
	if retyped {
		note = "The first attempt at typing did not appear on screen - the program was still starting up - " +
			"so it was typed again. Check the screen below before you send anything else."
	}
	return e.describeSession(ctx, handle, note, false)
}

func (e *Engine) execTerminalWait(ctx context.Context, chat *store.Chat, raw string) string {
	var args struct {
		Session string `json:"session"`
		Until   string `json:"until"`
		Text    string `json:"text"`
		Seconds int    `json:"seconds"`
		Quiet   int    `json:"quiet_seconds"`
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return badArgs(err, "Send valid JSON with a `session`.")
	}
	handle, problem := e.session(chat, args.Session)
	if problem != "" {
		return problem
	}
	tool := e.toolOfSession(handle)
	limit := tool.Timeout()
	if args.Seconds > 0 {
		limit = clampDuration(time.Duration(args.Seconds)*time.Second, time.Second, time.Hour)
	}

	if strings.EqualFold(args.Until, "text") {
		if strings.TrimSpace(args.Text) == "" {
			return "Waiting for text needs the `text` argument, a regular expression to look for."
		}
		matched, _, err := handle.WaitFor(ctx, args.Text, limit)
		if err != nil {
			return e.waitError(ctx, handle, args.Session, err)
		}
		note := fmt.Sprintf("The screen matched %q.", args.Text)
		if !matched {
			note = fmt.Sprintf("Gave up after %s without the screen ever matching %q.", limit, args.Text)
		}
		return e.describeSession(ctx, handle, note, false)
	}

	quiet := tool.Idle()
	if args.Quiet > 0 {
		quiet = clampDuration(time.Duration(args.Quiet)*time.Second, time.Second, 2*time.Minute)
	}
	idle, _, err := handle.WaitIdle(ctx, quiet, limit)
	if err != nil {
		return e.waitError(ctx, handle, args.Session, err)
	}
	note := fmt.Sprintf("The program has printed nothing for %s, so it is waiting for you.", quiet)
	if !idle {
		note = fmt.Sprintf("Still working after %s - it has not been quiet for %s yet. "+
			"Wait again if the screen shows it is making progress.", limit, quiet)
	}
	return e.describeSession(ctx, handle, note, false)
}

func (e *Engine) waitError(ctx context.Context, handle *term.Handle, id string, err error) string {
	if ctx.Err() != nil {
		return "The run was stopped by the user."
	}
	if handle != nil && !handle.Alive() {
		return e.describeSession(ctx, handle, "The program has exited.", false)
	}
	return fmt.Sprintf("Waiting on %s failed: %v", id, err)
}

func (e *Engine) execTerminalRead(ctx context.Context, chat *store.Chat, raw string) string {
	var args struct {
		Session    string `json:"session"`
		Transcript bool   `json:"transcript"`
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return badArgs(err, "Send valid JSON with a `session`.")
	}
	handle, problem := e.session(chat, args.Session)
	if problem != "" {
		return problem
	}
	return e.describeSession(ctx, handle, "", args.Transcript)
}

func (e *Engine) execTerminalClose(ctx context.Context, chat *store.Chat, raw string) string {
	var args struct {
		Session string `json:"session"`
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return badArgs(err, "Send valid JSON with a `session`.")
	}
	handle, problem := e.session(chat, args.Session)
	if problem != "" {
		return problem
	}
	name := handle.Name()
	if err := e.closeTerminal(handle); err != nil {
		return fmt.Sprintf("Could not close %s: %v", args.Session, err)
	}
	return fmt.Sprintf("Closed the %s session (%s).", name, args.Session)
}

func (e *Engine) execAsk(ctx context.Context, run *store.Run, raw string) string {
	var args struct {
		Question string `json:"question"`
		Options  []struct {
			Label       string `json:"label"`
			Description string `json:"description"`
		} `json:"options"`
		AllowFreeText *bool `json:"allow_free_text"`
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return badArgs(err, "Send valid JSON with a `question`.")
	}
	if strings.TrimSpace(args.Question) == "" {
		return "The `question` argument was empty."
	}
	options := make([]store.Option, 0, len(args.Options))
	for _, o := range args.Options {
		label := strings.TrimSpace(o.Label)
		if label == "" {
			continue
		}
		options = append(options, store.Option{Value: label, Label: label, Description: strings.TrimSpace(o.Description)})
	}
	freeText := true
	if args.AllowFreeText != nil {
		freeText = *args.AllowFreeText
	}
	answer, err := e.Ask(ctx, run, "", "ask", strings.TrimSpace(args.Question), options, freeText, e.askerName(run))
	if err != nil {
		if ctx.Err() != nil {
			return "The run was stopped by the user."
		}
		return fmt.Sprintf("Could not ask the user: %v", err)
	}
	return fmt.Sprintf("The user answered: %s", answer)
}

// askerName names whoever the question really comes from. When a coding agent
// is on screen the orchestrator is usually relaying its question, and the user
// should be told that Claude Code is asking rather than Socrates. An empty
// name means Socrates is asking on its own account.
func (e *Engine) askerName(run *store.Run) string {
	if e.Terminals == nil || run == nil {
		return ""
	}
	settings := e.Settings()
	// List is newest first, so the newest live session that came from a
	// configured tool is the one the user is looking at.
	for _, h := range e.Terminals.List(run.ChatID) {
		if !h.Alive() {
			continue
		}
		if tool, ok := settings.Tool(h.Meta(metaTool)); ok {
			return tool.Name
		}
	}
	return ""
}

// typeInto types text and checks that it actually arrived. A full screen
// program can look finished while it is still setting up its keyboard
// handling, and characters sent in that window are dropped without a trace -
// Claude Code does exactly this in the second after it starts. A person
// notices and types again; so does this. It reports whether it had to.
func typeInto(ctx context.Context, handle *term.Handle, text string) (bool, error) {
	before := handle.State()
	if err := handle.Type(ctx, text); err != nil {
		return false, err
	}
	// Waiting for the program to go idle is no use here: a program that
	// dropped the characters is idle immediately, and so is one that has not
	// echoed them yet. What tells them apart is whether anything came back.
	after, reacted := handle.WaitChange(ctx, before.Revision, 2*time.Second)
	if reacted && landed(after.Screen, before.Screen, text) {
		return false, nil
	}
	if err := handle.Type(ctx, text); err != nil {
		return false, err
	}
	handle.WaitChange(ctx, after.Revision, 3*time.Second)
	_, _, _ = handle.WaitIdle(ctx, 250*time.Millisecond, 3*time.Second)
	return true, nil
}

// landed decides whether typing reached the program. Anything appearing on the
// screen counts, because a program that took the characters has to redraw; the
// start of the text is looked for as well, for the rare program that redraws
// constantly on its own.
func landed(after, before, text string) bool {
	if after != before {
		return true
	}
	return strings.Contains(after, fragmentOf(text))
}

// fragmentOf is a short piece from the start of the text, short enough that a
// prompt box will not have wrapped it onto a second line.
func fragmentOf(text string) string {
	trimmed := strings.TrimSpace(text)
	if line, _, found := strings.Cut(trimmed, "\n"); found {
		trimmed = line
	}
	runes := []rune(trimmed)
	if len(runes) > 12 {
		runes = runes[:12]
	}
	return string(runes)
}

func clampDuration(d, low, high time.Duration) time.Duration {
	if d < low {
		return low
	}
	if d > high {
		return high
	}
	return d
}

// --- Harness guard -----------------------------------------------------------
//
// The coding agents are meant to be watched. The user opens Socrates to look
// over the shoulder of a real Claude Code, Codex or OpenCode session and to
// take the keyboard when they want to, which only works if the session is the
// interactive TUI in a terminal the browser can show. A model that reaches for
// `claude -p "..."` or `codex exec ...` gets the answer but hides the work, so
// those invocations are refused here rather than only discouraged in the
// prompt.
//
// This is a guard-rail for a well meaning model, not a security boundary. It
// reads the command line the way a shell would in the common cases and errs
// towards refusing, but anything determined to get past it will: an
// interpreter one liner (`python -c`, `perl -e`), a script file written first
// and then run, an alias or a copy of the binary under another name, a PATH
// entry that shadows it, `npx`, `ssh`, `tmux send-keys`, `find -exec`, and the
// body of a heredoc are all out of scope. The point is that the model never
// reaches for a headless run by accident, and is told what to do instead when
// it does.

// knownHarnesses are the agents Socrates ships with. They are guarded by name
// even when the settings have renamed them or the tool is disabled, because a
// disabled tool is still installed on the machine.
var knownHarnesses = []string{"claude", "codex", "opencode"}

// shellSegment is one command of a pipeline or list, plus whether its standard
// input comes from somewhere other than the terminal.
type shellSegment struct {
	words     []string
	stdinFrom bool
}

// harnessRules is what the guard needs to know about the configured tools:
// which commands are coding agents, and which of their flags swallow the word
// after them.
type harnessRules struct {
	// names maps a command to the id of the configured tool that runs it, so
	// the refusal can name the tool to open.
	names map[string]string
	// valueFlags take a separate value, which must not be mistaken for a
	// subcommand: `codex --model o3 exec x` is still headless.
	valueFlags map[string]bool
}

func newHarnessRules(s config.Settings) harnessRules {
	rules := harnessRules{
		names:      map[string]string{},
		valueFlags: map[string]bool{"-m": true, "--model": true},
	}
	for _, n := range knownHarnesses {
		rules.names[n] = ""
	}
	for _, t := range s.Tools {
		if flag := strings.TrimSpace(t.ModelFlag); flag != "" {
			rules.valueFlags[flag] = true
		}
		base := commandBase(t.Command)
		if base == "" {
			continue
		}
		if id, seen := rules.names[base]; !seen || id == "" {
			rules.names[base] = t.ID
		}
	}
	// Prefer an enabled tool's id when several tools share a command.
	for _, t := range s.EnabledTools() {
		if base := commandBase(t.Command); base != "" {
			rules.names[base] = t.ID
		}
	}
	return rules
}

// commandBase reduces `/usr/local/bin/Claude.exe` to `claude`.
func commandBase(command string) string {
	command = strings.ToLower(strings.TrimSpace(command))
	if command == "" {
		return ""
	}
	command = strings.ReplaceAll(command, "\\", "/")
	if i := strings.LastIndex(command, "/"); i >= 0 {
		command = command[i+1:]
	}
	return strings.TrimSuffix(command, ".exe")
}

// wrappers are words that stand in front of the command that really runs:
// launchers, and the shell's own reserved words.
var wrappers = map[string]bool{
	"sudo": true, "doas": true, "env": true, "nohup": true, "setsid": true,
	"exec": true, "time": true, "stdbuf": true, "timeout": true, "nice": true,
	"ionice": true,
	// Reserved words, so a loop or a conditional body is still inspected.
	"do": true, "then": true, "else": true, "elif": true, "if": true,
	"while": true, "until": true, "!": true,
}

// numericArgWrappers take a plain number before the command: `timeout 10 ...`.
var numericArgWrappers = map[string]bool{"timeout": true, "nice": true, "ionice": true}

// shellRunners run a script handed to them as a string, which has to be
// inspected in its own right.
var shellRunners = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "ash": true,
}

// headlessFlags are the flags that turn a coding agent into a batch program.
var headlessFlags = map[string]bool{
	"-p": true, "--print": true, "--non-interactive": true, "--noninteractive": true,
	"-q": true, "--quiet": true, "--json": true, "--headless": true, "--no-tui": true,
	"--output-format": true, "--input-format": true,
}

// headlessPrefixes catch the `--flag=value` spelling of the same thing.
var headlessPrefixes = []string{"--output-format=", "--input-format=", "--print=", "--json="}

// headlessSubcommands are the first positional argument that means "do it once
// and exit": `codex exec`, `opencode run`.
var headlessSubcommands = map[string]bool{"exec": true, "run": true}

// harmlessFlags ask the program a question about itself and exit. They start
// no session, so there is nothing to watch and nothing to refuse.
var harmlessFlags = map[string]bool{
	"--version": true, "-v": true, "-V": true, "--help": true, "-h": true,
	"version": true, "help": true,
}

// splitShellSegments breaks a command line into its pipeline and list members
// with a deliberately small shell parser. It only has to be right about where
// commands begin; anything it cannot make sense of becomes a separate segment,
// which is the safe direction.
func splitShellSegments(command string) []shellSegment {
	command = flattenSubstitutions(command)

	var (
		segments []shellSegment
		cur      shellSegment
		buf      strings.Builder
		started  bool
	)
	flushWord := func() {
		if started {
			cur.words = append(cur.words, buf.String())
			buf.Reset()
			started = false
		}
	}
	// A file descriptor in front of a redirection (`2>&1`) is part of the
	// redirection, not an argument of the command.
	dropDescriptor := func() {
		if started && isAllDigits(buf.String()) {
			buf.Reset()
			started = false
			return
		}
		flushWord()
	}
	flushSegment := func(nextPiped bool) {
		flushWord()
		if len(cur.words) > 0 {
			segments = append(segments, cur)
		}
		cur = shellSegment{stdinFrom: nextPiped}
	}

	runes := []rune(command)
	// skipTarget swallows the file a redirection writes to, so it is never
	// read as a command.
	skipTarget := func(i int) int {
		for i+1 < len(runes) && (runes[i+1] == ' ' || runes[i+1] == '\t') {
			i++
		}
		for i+1 < len(runes) && !strings.ContainsRune(" \t\n;|&<>", runes[i+1]) {
			i++
		}
		return i
	}
	redirectOut := func(i int) int {
		dropDescriptor()
		if i+1 < len(runes) && runes[i+1] == '>' {
			i++
		}
		if i+1 < len(runes) && runes[i+1] == '&' {
			// `>&1`, `>&2`, `>&-`: a descriptor, not a file.
			i++
			for i+1 < len(runes) && (isDigit(runes[i+1]) || runes[i+1] == '-') {
				i++
			}
			return i
		}
		return skipTarget(i)
	}

	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case c == '\'':
			started = true
			for i++; i < len(runes) && runes[i] != '\''; i++ {
				buf.WriteRune(runes[i])
			}
		case c == '"':
			started = true
			for i++; i < len(runes) && runes[i] != '"'; i++ {
				if runes[i] == '\\' && i+1 < len(runes) {
					i++
				}
				buf.WriteRune(runes[i])
			}
		case c == '\\' && i+1 < len(runes):
			i++
			if runes[i] != '\n' {
				started = true
				buf.WriteRune(runes[i])
			}
		case c == '|':
			if i+1 < len(runes) && runes[i+1] == '|' {
				i++
				flushSegment(false)
			} else {
				flushSegment(true)
			}
		case c == '&':
			switch {
			case i+1 < len(runes) && runes[i+1] == '>':
				// `&>file` and `&>>file` redirect, they do not separate.
				i++
				i = redirectOut(i)
			case i+1 < len(runes) && runes[i+1] == '&':
				i++
				flushSegment(false)
			default:
				flushSegment(false)
			}
		case c == ';' || c == '\n':
			flushSegment(false)
		case c == '<':
			dropDescriptor()
			cur.stdinFrom = true
			for i+1 < len(runes) && runes[i+1] == '<' {
				i++
			}
			if i+1 < len(runes) && runes[i+1] == '&' {
				i++
				for i+1 < len(runes) && (isDigit(runes[i+1]) || runes[i+1] == '-') {
					i++
				}
			}
		case c == '>':
			i = redirectOut(i)
		case c == ' ' || c == '\t' || c == '\r':
			flushWord()
		default:
			started = true
			buf.WriteRune(c)
		}
	}
	flushSegment(false)
	return segments
}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !isDigit(r) {
			return false
		}
	}
	return true
}

// flattenSubstitutions turns command substitutions and subshells into plain
// separators, so `$(claude -p x)` is inspected as a command of its own. A
// substitution opened inside double quotes closes the quoting first, because
// what is in it is a command line and not a string. Single quoted text is left
// alone: nothing in it ever runs.
func flattenSubstitutions(command string) string {
	var (
		b        strings.Builder
		inDouble bool
		stack    []bool
	)
	open := func() {
		if inDouble {
			b.WriteRune('"')
		}
		b.WriteRune('\n')
		stack = append(stack, inDouble)
		inDouble = false
	}
	closeSub := func() {
		b.WriteRune('\n')
		if n := len(stack); n > 0 {
			inDouble = stack[n-1]
			stack = stack[:n-1]
			if inDouble {
				b.WriteRune('"')
			}
		}
	}

	runes := []rune(command)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case c == '\'' && !inDouble:
			b.WriteRune(c)
			for i++; i < len(runes) && runes[i] != '\''; i++ {
				b.WriteRune(runes[i])
			}
			b.WriteRune('\'')
		case c == '\\' && i+1 < len(runes):
			b.WriteRune(c)
			i++
			b.WriteRune(runes[i])
		case c == '"':
			inDouble = !inDouble
			b.WriteRune(c)
		case c == '$' && i+1 < len(runes) && runes[i+1] == '(':
			i++
			open()
		case c == '`':
			// A backtick both opens and closes; treating it as an opener and
			// the next one as a closer keeps the quoting balanced.
			if len(stack) > 0 {
				closeSub()
			} else {
				open()
			}
		// Inside double quotes only `$(` and a backtick run anything: a bare
		// bracket there is ordinary text, as in `-m "add feature (wip)"`.
		case (c == '(' || c == '{') && !inDouble:
			open()
		case (c == ')' || c == '}') && !inDouble:
			closeSub()
		default:
			b.WriteRune(c)
		}
	}
	return b.String()
}

// harnessUse is what the guard found in one command line.
type harnessUse struct {
	name     string // the command as written, for the message
	toolID   string // the configured tool that runs it, when there is one
	headless bool   // it was invoked in a batch mode rather than as a TUI
	reason   string // the part of the command line that gave it away
}

// findHarnessUse looks for a coding agent being started by a command line. It
// returns the first one it finds, preferring a headless invocation so the
// message names the real problem.
func findHarnessUse(command string, rules harnessRules) *harnessUse {
	return findHarnessUseDepth(command, rules, 0)
}

// findHarnessUseDepth carries the recursion budget for `sh -c`, `eval` and
// `xargs`, which are inspected by looking at the command line inside them.
func findHarnessUseDepth(command string, rules harnessRules, depth int) *harnessUse {
	if depth > 4 || strings.TrimSpace(command) == "" {
		return nil
	}
	var first *harnessUse
	for _, seg := range splitShellSegments(command) {
		use := inspectSegment(seg, rules, depth)
		if use == nil {
			continue
		}
		if use.headless {
			return use
		}
		if first == nil {
			first = use
		}
	}
	return first
}

func inspectSegment(seg shellSegment, rules harnessRules, depth int) *harnessUse {
	words := seg.words
	// Step over environment assignments and wrapper commands.
	for len(words) > 0 {
		base := commandBase(words[0])
		switch {
		case base == "" || isAssignment(words[0]):
			words = words[1:]
		case wrappers[base]:
			words = stripWrapperArgs(base, words[1:])
		default:
			goto resolved
		}
	}
resolved:
	if len(words) == 0 {
		return nil
	}
	base, rest := commandBase(words[0]), words[1:]

	switch {
	case shellRunners[base]:
		if script, ok := shellScriptArg(rest); ok {
			return findHarnessUseDepth(script, rules, depth+1)
		}
		return nil
	case base == "eval":
		return findHarnessUseDepth(strings.Join(rest, " "), rules, depth+1)
	case base == "xargs":
		return findHarnessUseDepth(strings.Join(stripLeadingFlags(rest), " "), rules, depth+1)
	}

	toolID, ok := rules.names[base]
	if !ok {
		return nil
	}
	if len(rest) > 0 && harmlessFlags[rest[0]] {
		// `claude --version` starts no session, so there is nothing to watch.
		return nil
	}
	use := &harnessUse{name: base, toolID: toolID}
	if use.toolID == "" {
		use.toolID = base
	}
	if seg.stdinFrom {
		use.headless, use.reason = true, "a prompt on standard input"
		return use
	}
	positional := 0
	for i := 0; i < len(rest); i++ {
		arg := rest[i]
		if strings.HasPrefix(arg, "-") {
			if headlessFlags[arg] {
				use.headless, use.reason = true, fmt.Sprintf("the %s flag", arg)
				return use
			}
			for _, prefix := range headlessPrefixes {
				if strings.HasPrefix(arg, prefix) {
					use.headless, use.reason = true, fmt.Sprintf("the %s flag", arg)
					return use
				}
			}
			if rules.valueFlags[arg] && i+1 < len(rest) {
				// The value of `--model o3` is not the subcommand.
				i++
			}
			continue
		}
		positional++
		if positional == 1 && headlessSubcommands[arg] {
			use.headless, use.reason = true, fmt.Sprintf("the %s subcommand", arg)
			return use
		}
	}
	return use
}

// stripWrapperArgs drops the options of a wrapper command, and the plain
// number that `timeout` and `nice` take, leaving the command it will run.
func stripWrapperArgs(wrapper string, words []string) []string {
	for len(words) > 0 {
		switch {
		case strings.HasPrefix(words[0], "-"):
			words = words[1:]
		case numericArgWrappers[wrapper] && isDuration(words[0]):
			words = words[1:]
		default:
			return words
		}
	}
	return words
}

// stripLeadingFlags drops the options in front of the command xargs will run,
// including the argument of the few that take one.
func stripLeadingFlags(words []string) []string {
	takesValue := map[string]bool{"-I": true, "-i": true, "-n": true, "-P": true, "-L": true,
		"-s": true, "-d": true, "-E": true, "-a": true, "--replace": true}
	for len(words) > 0 && strings.HasPrefix(words[0], "-") {
		flag := words[0]
		words = words[1:]
		if takesValue[flag] && len(words) > 0 && !strings.HasPrefix(words[0], "-") {
			words = words[1:]
		}
	}
	return words
}

// shellScriptArg returns the script `sh -c` was given.
func shellScriptArg(words []string) (string, bool) {
	for i, w := range words {
		if strings.HasPrefix(w, "-") && !strings.HasPrefix(w, "--") && strings.Contains(w, "c") {
			if i+1 < len(words) {
				return words[i+1], true
			}
			return "", false
		}
	}
	return "", false
}

// isDuration recognises the `10`, `1.5` and `30s` that timeout accepts.
func isDuration(word string) bool {
	word = strings.TrimRight(word, "smhd")
	if word == "" {
		return false
	}
	dots := 0
	for _, r := range word {
		switch {
		case isDigit(r):
		case r == '.':
			dots++
			if dots > 1 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// isAssignment recognises the `FOO=bar` prefix of a command.
func isAssignment(word string) bool {
	eq := strings.Index(word, "=")
	if eq <= 0 {
		return false
	}
	for i, r := range word[:eq] {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// guardShellCommand refuses to run a coding agent from the shell tool at all.
// Even the bare command is refused: without a PTY the TUI either hangs or
// degrades, and there is nothing for the user to watch either way.
func guardShellCommand(command string, rules harnessRules) string {
	use := findHarnessUse(command, rules)
	if use == nil {
		return ""
	}
	detail := "shell_run has no terminal for it to draw in"
	if use.headless {
		detail = fmt.Sprintf("with %s it is a headless run", use.reason)
	}
	return fmt.Sprintf("Refusing to run %s through shell_run: %s. %s is only ever used "+
		"interactively, the way a person uses it: open it with terminal_open (`tool: %q`), wait for "+
		"it to finish painting, send the brief with terminal_send and read the screen back with "+
		"terminal_wait. The user is watching this session and wants to be able to take over, which "+
		"a print, exec or piped run takes away from them.",
		use.name, detail, use.name, use.toolID)
}

// guardTerminalCommand allows a coding agent started as its TUI but refuses a
// headless one, which would print and exit with nothing to co-drive.
func guardTerminalCommand(command string, rules harnessRules) string {
	use := findHarnessUse(command, rules)
	if use == nil || !use.headless {
		return ""
	}
	return fmt.Sprintf("Refusing to start that command: it runs %s with %s, a headless run. "+
		"Open the interactive session instead - terminal_open with `tool: %q` - then drive it with "+
		"terminal_send and terminal_wait. The user is watching this session and wants to be able to "+
		"take over, which a print, exec or piped run takes away from them.",
		use.name, use.reason, use.toolID)
}
