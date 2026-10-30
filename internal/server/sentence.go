package server

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// sentence is how an error the API sends reads to the person who sees it: a
// sentence, with a capital first letter and a full stop. The texts inside the
// server stay Go's own (lowercase, no full stop, wrapped with ": "), so
// errors.Is and wrapping are untouched; this is applied where an error is
// written to a response (writeError and the few bodies that carry an "error"
// beside other fields), and the web client shows what it gets as it comes.
//
// A text that starts with a name written in lowercase - tmux, code-server, a
// path, a quoted value - keeps it: "Tmux" or "Code-server" is not the
// program's name any more.
func sentence(msg string) string {
	text := strings.TrimRightFunc(msg, unicode.IsSpace)
	if text == "" {
		return msg
	}
	if first, size := utf8.DecodeRuneInString(text); unicode.IsLower(first) && !keepsLowercase(text) {
		text = string(unicode.ToUpper(first)) + text[size:]
	}
	if last, _ := utf8.DecodeLastRuneInString(text); !strings.ContainsRune(".!?…", last) {
		text += "."
	}
	return text
}

// lowercaseNames are the words an error may start with that are names, not
// the start of a sentence.
var lowercaseNames = map[string]bool{
	"tmux": true, "git": true, "gh": true, "json": true, "http": true, "https": true,
	"cloudflared": true, "opencode": true, "systemd": true, "systemctl": true,
	"sh": true, "bash": true, "zsh": true, "ssh": true, "sqlite": true, "npm": true, "node": true,
}

// keepsLowercase is whether the text's first word is a name: one of the
// above, or a word that is not plain letters (code-server, s3:, a.json).
func keepsLowercase(text string) bool {
	word, _, _ := strings.Cut(text, " ")
	word = strings.TrimRight(word, ":,;")
	if lowercaseNames[strings.ToLower(word)] {
		return true
	}
	for _, r := range word {
		if !unicode.IsLetter(r) {
			return true
		}
	}
	return false
}
