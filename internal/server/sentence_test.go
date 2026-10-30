package server

import "testing"

// What the API answers with is a sentence; a name written in lowercase stays
// as it is written.
func TestSentenceSaysAnErrorAsASentence(t *testing.T) {
	for in, want := range map[string]string{
		"the current password is wrong":           "The current password is wrong.",
		"This session was deleted.":               "This session was deleted.",
		"is it there?":                            "Is it there?",
		"open /x: permission denied\n":            "Open /x: permission denied.",
		"tmux is too old":                         "tmux is too old.",
		"code-server is not on this machine":      "code-server is not on this machine.",
		"systemd would not start the updater: no": "systemd would not start the updater: no.",
		`"x" is not a session status`:             `"x" is not a session status.`,
		"/data is full":                           "/data is full.",
		"s3: the bucket said no":                  "s3: the bucket said no.",
		"Codex QA holds the sign-in, so it stays": "Codex QA holds the sign-in, so it stays.",
		"über alles":                              "Über alles.",
		"":                                        "",
	} {
		if got := sentence(in); got != want {
			t.Errorf("sentence(%q) = %q, want %q", in, got, want)
		}
	}
}
