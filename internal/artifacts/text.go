package artifacts

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// The answers the socrates-artifacts tools give, as Markdown text: short,
// with what the agent needs next.

// formatWord is a format as the answers say it.
func formatWord(f string) string {
	if f == Markdown {
		return "Markdown"
	}
	return "HTML"
}

func when(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 UTC") }

// ListText is every artifact of a chat.
func ListText(list []Artifact) string {
	if len(list) == 0 {
		return "This chat has no artifacts yet. artifact_create makes one."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d artifact%s:\n", len(list), plural(len(list)))
	for _, a := range list {
		v := a.Latest()
		fmt.Fprintf(&b, "- %s “%s”: %s, version %d (%s)", a.ID, a.Title, formatWord(a.Format), v.N, when(v.At))
		if len(a.Files) > 0 {
			fmt.Fprintf(&b, ", files: %s", fileWords(a))
		}
		if a.Description != "" {
			fmt.Fprintf(&b, "\n  %s", a.Description)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// CreatedText is the answer to a created artifact.
func CreatedText(a Artifact) string {
	text := fmt.Sprintf("Created %s “%s” (%s, version 1). The person sees it in the chat's Artifacts panel; mention it by its title.", a.ID, a.Title, formatWord(a.Format))
	if len(a.Files) > 0 {
		text += " Files: " + fileWords(a) + " (name them relative in the page, as src=\"" + a.Files[0].Name + "\")."
	}
	return text
}

// UpdatedText is the answer to an update.
func UpdatedText(a Artifact, ch Change) string {
	var parts []string
	if ch.Version != 0 {
		parts = append(parts, fmt.Sprintf("version %d saved (%s)", ch.Version, formatWord(a.Format)))
	}
	if len(ch.Fields) > 0 {
		parts = append(parts, strings.Join(ch.Fields, " and ")+" changed")
	}
	if len(ch.Attached) > 0 {
		parts = append(parts, "attached "+strings.Join(ch.Attached, ", "))
	}
	if len(ch.Removed) > 0 {
		parts = append(parts, "removed "+strings.Join(ch.Removed, ", "))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%s “%s” is unchanged: nothing in the update differs from version %d.", a.ID, a.Title, a.Latest().N)
	}
	return fmt.Sprintf("%s “%s”: %s. The person's view shows it now.", a.ID, a.Title, strings.Join(parts, "; "))
}

// ReadText is an artifact with the content of one version.
func ReadText(a Artifact, v Version, content []byte, filesDir string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s “%s”\n\n", a.ID, a.Title)
	if a.Description != "" {
		fmt.Fprintf(&b, "%s\n\n", a.Description)
	}
	latest := a.Latest()
	if v.N == latest.N {
		fmt.Fprintf(&b, "Version %d (the latest), %s, %s, %s.\n", v.N, formatWord(v.Format), size(v.Size), when(v.At))
	} else {
		fmt.Fprintf(&b, "Version %d, %s, %s, %s; the latest is version %d. Edits always apply to the latest.\n", v.N, formatWord(v.Format), size(v.Size), when(v.At), latest.N)
	}
	if len(a.Versions) > 1 {
		b.WriteString("\nVersions:\n")
		for _, x := range a.Versions {
			fmt.Fprintf(&b, "- %d: %s", x.N, when(x.At))
			if x.Note != "" {
				fmt.Fprintf(&b, ", %s", x.Note)
			}
			b.WriteString("\n")
		}
	}
	if len(a.Files) > 0 {
		b.WriteString("\nFiles (the page names them relative):\n")
		for _, f := range a.Files {
			fmt.Fprintf(&b, "- %s, %s: %s\n", f.Name, size(f.Size), filepath.Join(filesDir, f.Name))
		}
	}
	fence := "```"
	for strings.Contains(string(content), fence) {
		fence += "`"
	}
	lang := "html"
	if v.Format == Markdown {
		lang = "markdown"
	}
	fmt.Fprintf(&b, "\nContent:\n\n%s%s\n%s\n%s\n", fence, lang, strings.TrimRight(string(content), "\n"), fence)
	return b.String()
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
