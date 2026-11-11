# Chat pages: Markdown an agent writes to show things (2026-11-11)

Asked for by the owner on 2026-11-11: instead of the built-in MCP server *Socrates Artifacts*
(2026-11-08, below), "it is enough to give the agent a short instruction that it can write a Markdown
file in a folder and link it in the chat". Decided with the owner the same day:

| Topic | Decision |
|---|---|
| What | A **pages folder per chat**, `<data>/chats/<chat>/pages/`. The agent writes Markdown files there with its own file tools and links them in its reply; Socrates shows them beside the chat with the Docs viewer (DOCS.md) |
| Not the repository, not `/tmp` | A file in the repository shows up in `git status` and in the working tree other chats share; `/tmp` is outside what the viewer may serve and is often full. The chat's own folder goes with the chat and is backed up with it |
| Socrates Artifacts | Removed: the MCP server, its store, its API, its pages and its column. The 7 artifacts there were (in 5 chats) were discarded |
| Listed | The chat's head has a **Pages** button while the chat has pages: a list, newest first, with a badge of the pages this device has not seen since they changed |
| Formats | Markdown only, with what the Docs viewer draws: GitHub Markdown, tables, Mermaid diagrams (flowcharts, sequence, class, ER, Gantt, pie, xy charts ...), GitHub's alerts, pictures beside the page. No scripts of the page's own |

## Why not Artifacts

Artifacts had five tools (`artifact_create`, `artifact_update` with exact edits, versions, attached
files), a store with limits and keys, its own sandboxed pages and a column with versions: about 3000
lines, and its tools in every chat that had it on. What agents made with it in its first days was
documents ("handbook", "overall concept", "test plan", "audit"), nearly all in a first version only,
none of them interactive. A Markdown file does the same with what an agent already knows how to do
(write and edit a file), the viewer was already there and draws a page again when it changes, and
no tool schemas sit in the agent's context. What went: HTML pages with scripts of their own (Chart.js
and the like; Mermaid's pie and xy charts cover the usual figures), the versions, deleting a page from
the UI (the person asks the agent).

## The agent

Every chat's agent is told (`agent.PagesGuidance`, after `agent.DocsGuidance`: appended to Claude
Code's system prompt, in Codex's developer instructions and in the instructions of `socrates mcp-ask`
for the other agents) where its pages folder is and when to use it: something easier to take in as a
document than as chat text (an explanation, a plan, a comparison, a report, a diagram), not a few lines
of text; start the page with a `# title`; link it with its absolute path; edit the same file rather
than write a new one for the same subject. The folder exists from the moment the chat's agent starts.

## The chat page

- **A link** to a page (its absolute path, `[The plan](/…/chats/<chat>/pages/plan.md)`) opens it
  beside the chat as a link to a Markdown file of the chat's working directory does (DOCS.md §3): the
  chat knows its pages folder (`pages_dir` of the session).
- **The Pages button** in the chat's head (pages.js) is shown while the folder has Markdown. It opens
  a menu of the pages, newest first, by their `# title` (else the file name) with when they last
  changed; choosing one opens it beside the chat. Its badge counts the pages new or changed since this
  device last opened them (kept per chat in `localStorage`).
- **Current**: while a chat is on the stage and the tab is visible, its pages are read again every
  5 s (`GET /api/docs?folder=<pages_dir>&titles=1`); a page open beside the chat is drawn again when it
  changes on disk, as every doc is.

## Server

- `paths.ChatPages` (`pages`) inside the chat's directory; made when the chat is made and when its
  agent host starts; removed with the chat and backed up with it.
- The Docs routes (DOCS.md §4) serve a chat's pages folder as they serve a project folder: it is an
  allowed folder when it is the pages folder of a chat there is. `GET /api/docs?…&titles=1` adds each
  file's `title` (its first `# ` heading in the first 8 KB) for a listing of at most 300 files.
- The first start of this build deletes every `<data>/chats/*/artifacts/` (what Socrates Artifacts
  kept). A stored choice of the server `socrates-artifacts` (Admin's *In every chat*, a chat's MCP
  servers) is dropped.
- The page kit stays at `/artifact-kit/` under its old name: the Docs viewer draws with it.

## Code and tests

| Where | What |
|---|---|
| `internal/agent/docsguide.go`, `pagesguide.go` | `DocsGuidance`, `PagesGuidance` |
| `internal/server/docs.go` | The pages folder among the allowed folders, the titles |
| `internal/web/static/js/pages.js` | The Pages button and its list |
| `internal/web/static/js/session.js` | `docLink`: a link into the pages folder |

Tested by `go test ./internal/agent/... ./internal/server -run 'Docs|Pages'` and `node e2e/run.mjs chatpages`.
