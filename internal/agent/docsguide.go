package agent

// DocsGuidance is the instruction every chat gets about pointing to a
// Markdown file of its folder: a relative link, which the chat page opens in
// its Docs view beside the chat, rendered, rather than as a path to copy.
const DocsGuidance = "Linking Markdown files: when you point the person to a Markdown file (.md) inside the working " +
	"directory, write a Markdown link whose target is its path relative to the working directory, e.g. " +
	"[the architecture](docs/ARCHITECTURE.md), optionally with a #heading anchor. Socrates opens such a link " +
	"rendered beside the chat, with its Mermaid diagrams. Other files stay plain `code` paths."
