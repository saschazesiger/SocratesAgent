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
than write a new one for the same subject; in a page, link a Markdown file of the working directory by
its absolute path (a path relative to the working directory would mean the pages folder there) and
never only as a `code` path, another page by its name. The folder exists from the moment the chat's agent starts.

## The chat page

- **A link** to a page (its absolute path, `[The plan](/…/chats/<chat>/pages/plan.md)`) opens it
  beside the chat as a link to a Markdown file of the chat's working directory does (DOCS.md §3): the
  chat knows its pages folder (`pages_dir` of the session). In the question dialog, which covers the
  chat, it opens in a new tab (DOCS.md §3, "A link in the question dialog").
- **A link in a page** to a file of the chat's working directory, by its absolute path, leads out of the
  pages folder: the viewer's kit says so (`outside`), and docside.js `outsideHref` and session.js
  `chatDocLink` take it where the same link in the chat would go (the file beside the chat, or one in
  another working tree). Only a link nothing can open gets the snackbar "That link leads out of this
  folder". A relative link stays in the pages folder: another page.
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
| `internal/web/static/js/session.js` | `chatDocLink`: a link into the pages folder, and a page's link out of it |
| `internal/web/static/js/docside.js` | `outsideHref`: a link out of the file shown, as an absolute path |

Tested by `go test ./internal/agent/... ./internal/server -run 'Docs|Pages'` and `node e2e/run.mjs chatpages ask-links`.

## Any file (2026-11-19)

Asked for by the owner on 2026-11-19: "every file a browser can open" - a video, a picture, a text file,
whatever - should be shown under the chat's pages and linkable in the chat so that it opens. Agents cut demo
videos (`out/guardch-chatgpt-demo.mp4`) and mixed voice-overs (`review/voice-a.mp3`) that the person could only
see by downloading them, and one chat built a frontend of its own on the tailnet to share MP3s. Decided the same
day: as broad as a browser goes, no setup for the agent.

| Topic | Decision |
|---|---|
| The pages folder | Holds any file now, not only Markdown: every regular file that is not hidden is listed (`docs.ScanAll`, each with its `kind`) and served (`docs.ResolveAll`), whatever its size, in ranges |
| Anywhere else | A file the agent links or names in its text, wherever it lies (its working directory, `/tmp`), opens beside the chat too, served by the chat's file route - only when a text of the agent's in this chat names that path as a whole: a link's target, a `code` path or a bare absolute path, as written, without `file://` or percent-decoded (`chatRefs`, kept while the chat does not change); never a part of a path it named |
| Listed | The menu, now **Pages and files**, lists the folder's files and the files the agent linked elsewhere (*Linked*), newest first. The badge still counts the folder's files only |
| Kinds | By name (`docs.FileKind`, mirrored in fileview.js `fileKind`), a file whose name says nothing by its first bytes (`docs.SniffKind`): Markdown, image, video, audio, PDF, HTML, text, and any other file |
| The viewer | fileview.js, in the column beside the chat (docside.js) or its full screen dialog: the browser's own player for a video and a recording (where it was, when the file changes), a picture (the lightbox on a press), the browser's PDF viewer, an HTML page in a sandboxed frame, text as a code block (JSON indented, the first 1 MB), a CSV or TSV as a table (500 rows), anything else a card to download it. A file the browser cannot play says so, with the download. The head has *Download* and *Open in a new tab* |
| Which links | A Markdown link to a file path (absolute, `file://`, relative to the working directory) that is not Markdown; and, as for Markdown, a `code` path, but only of a kind to look at or listen to (video, audio, picture, PDF, HTML): a coding chat names its source files as `code` all the time |
| Linked files | `GET /api/chats/<id>/files`: from the agent's texts, every link's target of any kind but Markdown, and `code` paths and bare absolute paths of a kind to look at; that are regular files now, outside the pages folder; at most 100, from the 400 newest paths; kept 10 s while the chat does not change |

### Served how

What a file may do when the browser shows it is held by its headers (`serveChatFile`), for both routes:

| Kind | Served as |
|---|---|
| Video, audio, picture | Its type, `Content-Security-Policy: sandbox`, ranges (`http.ServeContent`) |
| PDF | `application/pdf` with `frame-ancestors 'self'` only: the browser's PDF viewer does not run in a sandbox |
| HTML | `text/html` with `sandbox allow-scripts allow-popups allow-popups-to-escape-sandbox allow-modals allow-downloads allow-forms`: its scripts run, in an opaque origin, without the person's cookie; the frame has the same sandbox |
| Text, Markdown as a file | `text/plain; charset=utf-8`, sandbox: never a script or a page |
| Any other | Its registered type unless that is one a browser would render (HTML, XML, SVG, script, text), else `application/octet-stream`; always `Content-Disposition: attachment` |

Only a regular file is served, opened without blocking (a pipe put in the pages folder never holds a request).
Every answer has `nosniff`, `Referrer-Policy: no-referrer` and a `Content-Disposition` with the file's name
(`?download=1` makes it an attachment). The pages folder's files also have `Access-Control-Allow-Origin: *`, so
that an HTML report there can read its own data files (its origin is opaque).

An HTML page of the pages folder loads its pictures, styles and scripts from the folder by relative paths, as the
token in its address covers the folder. Its scripts could carry that address away (to a server of their own),
and with it the folder's files: what is in the folder is what the agent put there to show, and the page is the
agent's own or one it copied. An HTML file elsewhere is served by the chat's file route alone: it shows, its
scripts run, but its relative files do not load (the agent is told to copy the page and its files into the
pages folder).

### The agent

`agent.PagesGuidance` has a second paragraph, *Files*: Socrates shows any other file beside the chat and lists
what it links; to show the person a file, link it with its absolute path (or one relative to the working
directory), never only as a `code` path, and never through a web server, a tunnel or a share of its own; copy
what is worth keeping into the pages folder (an HTML page with the files it loads), where a page can play it
with `<video>` or `<audio>`; make what the person should watch or hear in a format every browser plays (MP4 with
H.264 and AAC, WebM, MP3, M4A, WAV).

### Code and tests

| Where | What |
|---|---|
| `internal/docs/files.go` | `FileKind`, `FileContentType`, `SniffKind`, `OpenFile` |
| `internal/docs/docs.go` | `ScanAll`, `ResolveAll` |
| `internal/server/docs.go` | The pages folder listed and served whole (`docsAllFiles`) |
| `internal/server/chatfile.go` | `/api/chats/{id}/file`, `/file/info`, `/files`; `serveChatFile` |
| `internal/agent/projection.go` | `AgentTexts`, what the linked files are found in |
| `internal/web/static/js/fileview.js` | The viewer and the kinds |
| `internal/web/static/js/docside.js` | `openFile`, `folderFile`, `chatFile`, `fileLinkRef` |
| `internal/web/static/js/session.js` | `chatDocTarget`: `{file}` for a file that is not Markdown |
| `internal/web/static/js/pages.js` | Every file of the folder and the linked ones in the menu |
| `internal/web/static/js/md.js` | `SHOWN_PATH`: a `code` path to a file to look at as a link |

Tested by `go test ./internal/docs ./internal/server -run 'Files|Docs|Pages|Linked'` and `node e2e/run.mjs chatpages`.
