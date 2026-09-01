package agent

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/term"
)

// Waiting for a program to finish is the one thing this orchestrator has to
// get right: everything it reports to the user is read off a screen, and a
// screen read one second too early is simply wrong. "It has printed nothing
// for a while" is not the same as "it is finished" - a coding agent that is
// thinking, waiting on the network or redrawing the same spinner frame prints
// nothing new for seconds at a time. So two things decide idleness here:
// whether the *meaningful* text on screen changed, spinner animation
// discounted, and whether the program itself still says it is working.

// Outcomes of a wait, as they are labelled for the model.
const (
	waitIdle    = "idle"           // quiet, and nothing says it is working
	waitBusy    = "still working"  // the busy pattern still matches
	waitNoisy   = "still printing" // it kept printing right up to the timeout
	waitExited  = "exited"         // the program is gone
	waitStopped = "stopped"        // the run was cancelled
)

// spinnerRune reports whether a rune is part of some program's animation
// rather than part of its output. Braille cells are the near universal
// spinner; the block shades are opencode's progress bar; the rest are the
// usual suspects.
func spinnerRune(r rune) bool {
	if r >= 0x2800 && r <= 0x28FF { // braille patterns: ⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏
		return true
	}
	switch r {
	case '◐', '◓', '◑', '◒', '◴', '◷', '◶', '◵', '✶', '✻', '✽', '✳',
		'⬝', '■', '▪', '▫', '█', '▉', '▊', '▋', '▌', '▍', '▎', '▏', '░', '▒', '▓':
		return true
	}
	return false
}

// asciiSpinner are the characters of the oldest spinner of them all. They are
// only dropped when they stand alone as a word, because a slash or a pipe in
// the middle of a line is far more likely to be a path or a table than an
// animation frame.
func asciiSpinner(token string) bool {
	switch token {
	case "|", "/", "-", "\\", "\\|", "*":
		return true
	}
	return false
}

var (
	// Elapsed counters tick once a second while a program works, always
	// inside the brackets next to the spinner: "(12s · ↓ 340 tokens)",
	// "(1m 4s)". Only that bracketed form is discounted - a bare number
	// elsewhere on the screen may well be the output itself, and "v1.2.3s" or
	// "+12 lines" must survive untouched.
	elapsedPattern = regexp.MustCompile(`\(\s*\d+(\.\d+)?\s*(ms|s|m|h)\b[^)]*\)`)
	// The token counter next to it moves for the same reason. It is only
	// discounted with its up or down arrow, which is how the agents draw the
	// running total and nothing else on the screen looks.
	counterPattern = regexp.MustCompile(`[↓↑]\s*[\d.,]+\s*[kKmM]?\s*tokens?`)
)

// normaliseScreen reduces a screen to the text a person would call its
// content: no spinner frames, no ticking counters, no trailing whitespace, no
// blank lines. Two screens that normalise to the same string mean the program
// has done nothing since; anything else counts as progress.
func normaliseScreen(screen string) string {
	var out []string
	for _, line := range strings.Split(screen, "\n") {
		line = elapsedPattern.ReplaceAllString(line, "(<t>)")
		line = counterPattern.ReplaceAllString(line, "<n>")
		line = strings.Map(func(r rune) rune {
			if spinnerRune(r) {
				return -1
			}
			return r
		}, line)
		fields := strings.Fields(line)
		kept := fields[:0]
		for _, f := range fields {
			if asciiSpinner(f) {
				continue
			}
			kept = append(kept, f)
		}
		line = strings.Join(kept, " ")
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// busyLines is how much of the bottom of the screen the busy pattern is
// matched against. Every program here draws its "esc to interrupt" hint in a
// footer pinned to the last line or two; the same words inside a file the
// agent is printing, higher up the screen, are quotation and not a status.
const busyLines = 8

// busyNow reports whether the program is telling us it is working. Blank lines
// are dropped first so a footer padded out with them still counts.
func busyNow(screen string, busy *regexp.Regexp) bool {
	if busy == nil {
		return false
	}
	var kept []string
	for _, line := range strings.Split(screen, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		kept = append(kept, line)
	}
	if len(kept) > busyLines {
		kept = kept[len(kept)-busyLines:]
	}
	return busy.MatchString(strings.Join(kept, "\n"))
}

// busyFreeze caps how long the busy pattern alone may keep a wait going. A
// screen that has not changed at all for this long is far more likely to be a
// dialog whose footer still says "esc to interrupt" than a program that is
// thinking, and the model is better off being shown it.
var busyFreeze = 90 * time.Second

// frozenFor is the freeze window for a given quiet setting: never shorter than
// the quiet window itself.
func frozenFor(quiet time.Duration) time.Duration {
	if quiet > busyFreeze {
		return quiet
	}
	return busyFreeze
}

// waitResult is what one wait came to.
type waitResult struct {
	// Label is the word the model is given: idle, still working, exited.
	Label string
	// Pattern is the busy pattern that was still matching, when one was.
	Pattern string
	// Elapsed is how long the wait took.
	Elapsed time.Duration
	// Frozen is how long the meaningful screen had been unchanged when the
	// wait gave up, and is only set when the busy pattern was what kept it
	// waiting.
	Frozen time.Duration
	// Changed is when the meaningful screen last changed.
	Changed time.Time
}

// waitQuiet blocks until the session is genuinely idle: its meaningful screen
// has not changed for quiet *and* the program is not saying it is busy. While
// the busy pattern matches it keeps waiting even though nothing is changing,
// which is exactly the case that used to make Socrates answer too early.
func waitQuiet(ctx context.Context, handle *term.Handle, busy *regexp.Regexp, quiet, limit time.Duration) waitResult {
	if quiet <= 0 {
		quiet = 2 * time.Second
	}
	start := time.Now()
	deadline := start.Add(limit)
	previous := normaliseScreen(handle.State().Screen)
	changed := start

	pattern := ""
	if busy != nil {
		pattern = busy.String()
	}
	for {
		round := time.Now()
		state := handle.State()
		if !handle.Alive() {
			return waitResult{Label: waitExited, Elapsed: time.Since(start)}
		}
		if current := normaliseScreen(state.Screen); current != previous {
			previous, changed = current, round
		}
		working := busyNow(state.Screen, busy)
		still := round.Sub(changed)
		if !working && still >= quiet {
			return waitResult{Label: waitIdle, Elapsed: time.Since(start), Changed: changed}
		}
		// A busy pattern over a screen that has not moved in a very long time
		// has stopped being evidence. Say so rather than waiting out the
		// whole timeout on it.
		if working && still >= frozenFor(quiet) {
			return waitResult{Label: waitBusy, Pattern: pattern, Elapsed: time.Since(start),
				Frozen: still, Changed: changed}
		}
		if !round.Before(deadline) {
			if working {
				return waitResult{Label: waitBusy, Pattern: pattern, Elapsed: time.Since(start),
					Changed: changed}
			}
			return waitResult{Label: waitNoisy, Elapsed: time.Since(start), Changed: changed}
		}

		// Sleep until the next thing that could change the answer: the quiet
		// window running out, or the deadline - but look again often enough
		// that a busy pattern disappearing is noticed promptly.
		step := quiet - round.Sub(changed)
		if left := time.Until(deadline); left < step {
			step = left
		}
		if working || step > 250*time.Millisecond {
			step = 250 * time.Millisecond
		}
		if step < 25*time.Millisecond {
			step = 25 * time.Millisecond
		}
		handle.WaitChange(ctx, state.Revision, step)
		if ctx.Err() != nil {
			return waitResult{Label: waitStopped, Elapsed: time.Since(start)}
		}
		// A program printing without pause would otherwise spin this loop as
		// fast as it can write.
		if rest := 25*time.Millisecond - time.Since(round); rest > 0 {
			select {
			case <-ctx.Done():
				return waitResult{Label: waitStopped, Elapsed: time.Since(start)}
			case <-time.After(rest):
			}
		}
	}
}

// stillWorking answers the question the run loop asks before it lets Socrates
// speak: is the program in this session still going? Only a skill's own busy
// pattern can answer that - an ad hoc shell has no way of saying "wait", and
// guessing from screen activity would hold back answers for no good reason.
//
// changed is when this session's meaningful screen was last seen to change,
// as far as anything in this run knows. A busy pattern sitting over a screen
// that has been frozen for longer than the freeze window is not treated as
// working: that is the shape of a dialog waiting for an answer.
func stillWorking(handle *term.Handle, busy *regexp.Regexp, changed time.Time, quiet time.Duration) (bool, string) {
	if handle == nil || busy == nil || !handle.Alive() {
		return false, ""
	}
	if !busyNow(handle.State().Screen, busy) {
		return false, ""
	}
	if !changed.IsZero() && time.Since(changed) >= frozenFor(quiet) {
		return false, ""
	}
	return true, busy.String()
}
