package agent

// DocsGuidance is the instruction every chat gets about pointing to a
// Markdown file of its folder: a relative link, which the chat page opens in
// its Docs view beside the chat, rendered, rather than as a path to copy.
// Agents tend to name files as `code` paths out of habit (1513 such mentions
// against 17 links in the first day of the earlier, milder wording), so it
// says "every time" and names what not to write. A file written in another
// git worktree is linked by its absolute path, which Socrates finds there
// (docs.Worktrees, GET /api/docs/locate). An image file shown as a Markdown
// image is drawn in the chat (md.js, server handleChatShownImage): the way an
// agent hands the person a picture it made.
const DocsGuidance = "Linking Markdown files: every time you mention a Markdown file (.md) inside the working " +
	"directory - one you wrote, changed, read or recommend, in a list, a table or the summary at the end - write " +
	"it as a Markdown link whose target is its path relative to the working directory, e.g. " +
	"[ARCHITECTURE.md](docs/ARCHITECTURE.md) or [the setup](README.md#setup). Socrates opens such a link beside " +
	"the chat, rendered as it is on disk now, with its Mermaid diagrams. Never name such a file only as a `code` " +
	"path or a bare name, and never link it with a GitHub or other web address or a file:// address: the person " +
	"cannot open those beside the chat. A Markdown file you wrote outside the working directory, in another " +
	"working tree of its git repository (a `git worktree` of your own), is linked by its absolute path, e.g. " +
	"[DEV.md](/root/wt/feature/docs/DEV.md). This takes precedence over writing file paths as `code`; files " +
	"that are not Markdown stay plain `code` paths.\n\n" +
	"Showing images: to show the person an image file on this machine - a screenshot you saved, a chart, a " +
	"diagram or a picture you made - write it in your reply, or in a question you ask (its context, steps, sections or an option's details), " +
	"as a Markdown image with its absolute path, e.g. ![The login page](/tmp/login.png) (PNG, JPEG, GIF or WebP). " +
	"Socrates draws it small there and the person opens it large with a click, to zoom in and move around. An image a tool returns (a browser's screenshot) is shown by itself."
