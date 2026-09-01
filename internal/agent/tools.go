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

// The orchestrator's whole capability set is a terminal. It does none of the
// work itself: driving Claude Code, running a build, reading a file are all
// things that happen *in* a session it opened for the program that does them,
// which is why there is no tool per coding agent.
const (
	toolShellRun      = "shell_run"
	toolTerminalOpen  = "terminal_open"
	toolTerminalSend  = "terminal_send"
	toolTerminalWait  = "terminal_wait"
	toolTerminalRead  = "terminal_read"
	toolTerminalClose = "terminal_close"
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
		"Give either `skill` (one of the configured skills below, started with the right arguments for " +
		"you) or `command` (any command line), not both. With neither you get a plain interactive shell.\n" +
		"Each chat has exactly one terminal - the screen the user is watching - so this call fails while " +
		"a session is still running. That is not a dead end: call terminal_close on it and then " +
		"terminal_open again, which is how you reset the terminal or swap one program for another.\n" +
		"The session stays open across your turns and across messages from the user, so open one session " +
		"and keep talking to it rather than starting a new one for every instruction.\n" +
		"Each skill's entry in the system prompt says how to drive that program and whether it may be " +
		"used any other way. Most may not: start those here, never through shell_run.")
	enabled := s.EnabledSkills()
	if len(enabled) > 0 {
		openDesc.WriteString("\n\nConfigured skills for `skill`, each with the models it may be started " +
			"with. Pick the model the same way you pick the skill - by reading what it is for - and name " +
			"it in `model`. The first model of a skill is what you get if you leave `model` out.\n")
		for _, sk := range enabled {
			fmt.Fprintf(&openDesc, "- %s (%s): %s\n", sk.ID, sk.Name, strings.TrimSpace(sk.Description))
			for _, m := range sk.Models {
				fmt.Fprintf(&openDesc, "  - model `%s`", m.ID)
				if effort := config.NormalizeEffort(m.Effort); effort != "" {
					fmt.Fprintf(&openDesc, ", effort %s", effort)
				}
				if when := strings.TrimSpace(m.UseWhen); when != "" {
					fmt.Fprintf(&openDesc, ": %s", when)
				}
				openDesc.WriteString("\n")
			}
		}
	}
	ids := make([]string, 0, len(enabled))
	for _, sk := range enabled {
		ids = append(ids, sk.ID)
	}
	enumJSON, _ := json.Marshal(ids)
	skillProperty := `"skill": {"type": "string", "description": "One of the configured skills."},`
	if len(ids) > 0 {
		skillProperty = fmt.Sprintf(`"skill": {"type": "string", "enum": %s, "description": "One of the configured skills."},`, enumJSON)
	}

	keyList := term.KeyNames()
	sort.Strings(keyList)

	tools := []openrouter.Tool{
		fn(toolShellRun,
			"Run one shell command and wait for it to finish. It returns the exit code and the output. "+
				"This is for orchestration mechanics only: seeing whether a process is still alive, "+
				"listing a directory so your brief can name the right paths, checking that a repository is "+
				"where you think it is.\n"+
				"It is never where the task gets done. A build, a test run, an edit, a search through the "+
				"code to work an answer out - all of that belongs to a skill in a terminal session, and so "+
				"does checking the result: you ask the agent to run the tests and read what it shows you, "+
				"rather than running them here.\n"+
				"Do not use it for a program that will ask you something or that never exits - open a "+
				"terminal session for those. A skill marked interactive only is never run here, in any "+
				"form: it belongs in a terminal session where the user can watch it and take over.",
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
    `+skillProperty+`
    "model": {"type": "string", "description": "Which of that skill's configured models to start it with, by its id from the list above. Only meaningful together with a skill. Leave it out for the skill's first model."},
    "command": {"type": "string", "description": "Any command line to start instead of a configured skill."},
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
				"it returns once the program has really finished its turn: the screen has stopped changing "+
				"(a spinner or a ticking timer does not count as a change) and nothing on it says the "+
				"program is still working. The result starts with a status line: \"idle\" means it is your "+
				"turn, \"still working\" means it is not - call this again, never report a result off a "+
				"still working screen. With `until` set to \"text\" it returns as soon as the screen matches "+
				"`text`, which is a regular expression - useful for catching a specific question. Trailing "+
				"spaces are trimmed from every line, so do not match on them.",
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
	}

	return tools
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
		Skill     string `json:"skill"`
		Model     string `json:"model"`
		Command   string `json:"command"`
		Name      string `json:"name"`
		Directory string `json:"directory"`
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return badArgs(err, "Send valid JSON with a `skill` or a `command`.")
	}
	skill := strings.TrimSpace(args.Skill)
	if skill != "" && strings.TrimSpace(args.Command) != "" {
		return "Give either `skill` or `command`, not both."
	}
	if skill == "" && strings.TrimSpace(args.Model) != "" {
		return "`model` names one of a skill's configured models, so it only means something together " +
			"with `skill`. A command line started with `command` carries its own arguments."
	}
	return e.openTerminal(ctx, chat, run, skill, args.Model, args.Command, args.Name, args.Directory)
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
	skill := e.skillOfSession(handle)
	limit := skill.Timeout()
	if args.Seconds > 0 {
		limit = clampDuration(time.Duration(args.Seconds)*time.Second, time.Second, time.Hour)
	}

	busy := skill.Busy()

	if strings.EqualFold(args.Until, "text") {
		if strings.TrimSpace(args.Text) == "" {
			return "Waiting for text needs the `text` argument, a regular expression to look for."
		}
		matched, _, err := handle.WaitFor(ctx, args.Text, limit)
		if err != nil {
			return e.waitError(ctx, handle, args.Session, err)
		}
		note := fmt.Sprintf("Status: matched %q.", args.Text)
		if !matched {
			note = fmt.Sprintf("Status: no match. Gave up after %s without the screen ever matching %q.",
				limit, args.Text)
			if busyNow(handle.State().Screen, busy) {
				note += fmt.Sprintf(" The program is still working (the screen matches %q), so wait again "+
					"instead of reporting anything.", busy.String())
			}
		}
		return e.describeSession(ctx, handle, note, false)
	}

	quiet := skill.Idle()
	if args.Quiet > 0 {
		quiet = clampDuration(time.Duration(args.Quiet)*time.Second, time.Second, 2*time.Minute)
	}
	// A quiet window longer than the wait itself could only ever time out, so
	// a short wait gets a correspondingly short definition of quiet rather
	// than a guaranteed "still working".
	if quiet > limit {
		quiet = limit
	}
	result := waitQuiet(ctx, handle, busy, quiet, limit)
	if ctx.Err() != nil {
		return "The run was stopped by the user."
	}
	// What this wait saw is remembered, so that the guard on the final answer
	// knows how long this screen has been standing still.
	e.markScreen(handle.ID(), normaliseScreen(handle.State().Screen), result.Changed)
	var note string
	switch result.Label {
	case waitExited:
		note = "Status: exited. The program is no longer running."
	case waitIdle:
		note = fmt.Sprintf("Status: idle. Nothing on the screen has changed for %s "+
			"(a ticking spinner or timer does not count) and nothing says it is still working, "+
			"so it is finished with the last thing you gave it.", quiet)
	case waitBusy:
		note = fmt.Sprintf("Status: still working after %s - the screen still matches %q, "+
			"which is this program's way of saying it has not finished. "+
			"Do not read a result off this screen and do not answer the user yet: "+
			"call terminal_wait again.", roundSeconds(result.Elapsed), result.Pattern)
		if result.Frozen > 0 {
			note = fmt.Sprintf("Status: still working after %s - the screen matches %q but nothing on it "+
				"has changed for %s. Check whether it is a dialog waiting for an answer, or a question "+
				"only the user can answer, rather than waiting again.",
				roundSeconds(result.Elapsed), result.Pattern, roundSeconds(result.Frozen))
		}
	default:
		note = fmt.Sprintf("Status: still working after %s - it has not gone quiet for %s yet. "+
			"Call terminal_wait again rather than answering.", roundSeconds(result.Elapsed), quiet)
	}
	note += "\nIf the screen shows a question or a menu, answer it instead of waiting."
	return e.describeSession(ctx, handle, note, false)
}

// roundSeconds prints a duration the way a person would say it.
func roundSeconds(d time.Duration) time.Duration { return d.Round(time.Second) }

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
	e.forgetScreen(handle.ID())
	if err := e.closeTerminal(handle); err != nil {
		return fmt.Sprintf("Could not close %s: %v", args.Session, err)
	}
	return fmt.Sprintf("Closed the %s session (%s).", name, args.Session)
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
