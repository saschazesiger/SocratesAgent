package term

import (
	"fmt"
	"strings"
)

// namedKeys maps the key names the orchestrator may use to the bytes a real
// terminal would send. The names are deliberately the ones a person would
// write down when describing what they pressed.
var namedKeys = map[string]string{
	"enter":     "\r",
	"return":    "\r",
	"tab":       "\t",
	"backspace": "\x7f",
	"delete":    "\x1b[3~",
	"escape":    "\x1b",
	"esc":       "\x1b",
	"space":     " ",
	"up":        "\x1b[A",
	"down":      "\x1b[B",
	"right":     "\x1b[C",
	"left":      "\x1b[D",
	"home":      "\x1b[H",
	"end":       "\x1b[F",
	"pageup":    "\x1b[5~",
	"pagedown":  "\x1b[6~",
	"insert":    "\x1b[2~",
	"shift+tab": "\x1b[Z",
	// Newline inside a prompt box: most agent CLIs treat a bare Enter as
	// "submit", so a multi line message needs the shift+enter escape.
	"shift+enter": "\x1b\r",
	"alt+enter":   "\x1b\r",
}

// functionKeys are spelled out separately because they are not a range.
var functionKeys = map[string]string{
	"f1": "\x1bOP", "f2": "\x1bOQ", "f3": "\x1bOR", "f4": "\x1bOS",
	"f5": "\x1b[15~", "f6": "\x1b[17~", "f7": "\x1b[18~", "f8": "\x1b[19~",
	"f9": "\x1b[20~", "f10": "\x1b[21~", "f11": "\x1b[23~", "f12": "\x1b[24~",
}

// Key translates one key name into the bytes to write to the pseudo terminal.
// Besides the named keys it understands "ctrl+<letter>", "alt+<letter>" and a
// single literal character.
func Key(name string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		return "", fmt.Errorf("empty key name")
	}
	if seq, ok := namedKeys[key]; ok {
		return seq, nil
	}
	if seq, ok := functionKeys[key]; ok {
		return seq, nil
	}
	if rest, ok := strings.CutPrefix(key, "ctrl+"); ok {
		if len([]rune(rest)) != 1 {
			return "", fmt.Errorf("ctrl+ needs a single character, got %q", rest)
		}
		r := []rune(rest)[0]
		switch {
		case r >= 'a' && r <= 'z':
			return string(rune(r - 'a' + 1)), nil
		case r == '[':
			return "\x1b", nil
		case r == '\\':
			return "\x1c", nil
		case r == ']':
			return "\x1d", nil
		case r == ' ', r == '@':
			return "\x00", nil
		}
		return "", fmt.Errorf("no control code for %q", key)
	}
	if rest, ok := strings.CutPrefix(key, "alt+"); ok {
		if len([]rune(rest)) != 1 {
			return "", fmt.Errorf("alt+ needs a single character, got %q", rest)
		}
		return "\x1b" + rest, nil
	}
	// A single literal character is allowed so the model can answer a menu
	// with "1" or "y" without spelling out a name.
	if len([]rune(name)) == 1 {
		return name, nil
	}
	return "", fmt.Errorf("unknown key %q", name)
}

// Keys translates a sequence of key names in one go.
func Keys(names []string) (string, error) {
	var b strings.Builder
	for _, name := range names {
		seq, err := Key(name)
		if err != nil {
			return "", err
		}
		b.WriteString(seq)
	}
	return b.String(), nil
}

// KeyNames lists every name Key accepts verbatim, for the tool description.
func KeyNames() []string {
	names := make([]string, 0, len(namedKeys)+len(functionKeys))
	for name := range namedKeys {
		names = append(names, name)
	}
	for name := range functionKeys {
		names = append(names, name)
	}
	return names
}
