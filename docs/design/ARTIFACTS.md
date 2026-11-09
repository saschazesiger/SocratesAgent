# Chat artifacts: pages an agent makes to show things (2026-11-08)

Status: asked for by the owner on 2026-11-08 ("like Socrates Tests: a new MCP server that lets an
agent make pages, HTML or similar, as simply as possible, to explain things graphically with
diagrams; for the person a side panel listing the pages; the agent creates, edits and deletes them;
per chat") and built the same day, with the details below decided by the agent.

## What it is

A chat whose built-in MCP server **Socrates Artifacts** (`socrates-artifacts`) is on can make
**artifacts**: pages that show something better than chat text can. A diagram of an architecture,
a flow chart, a chart of numbers, a comparison table, a timeline, a small interactive explainer.
The agent writes the page as an HTML fragment or as Markdown (with Mermaid diagrams), and the person
sees it beside the chat, live: a new version replaces what is on screen the moment it is saved.

The artifacts belong to the chat, like its tests: no catalog, no overview over chats. They are
removed with the chat and backed up with it.

## Decisions (agent, 2026-11-08)

| Topic | Decision |
|---|---|
| Formats | `html` (a fragment, wrapped in Socrates' page kit, or a whole document served as it is) and `markdown` (rendered by the kit, Mermaid code blocks drawn as diagrams). Nothing else: a chart is SVG or a chart library from a CDN, a picture an attached file |
| Diagrams | Mermaid, loaded by the kit from jsDelivr only when a page has a diagram; a page offline shows the diagram's source instead |
| Editing | A whole new content, or exact string edits on the latest version (`edits: [{old, new}]`), as an agent edits a file; every change of the content is a new version |
| Versions | The last 20 are kept, each with an optional note of what changed; the person can look at an older one |
| Files | Pictures and data a page uses (PNG, SVG, JSON, CSV ...), attached by path and named relative in the page (`<img src="chart.png">`); shared by every version |
| Isolation | A page runs in a sandboxed frame of its own opaque origin: it can run scripts and load from the internet, but never reach Socrates' API, cookies or pages |
| Person's actions | Look (any version), open in a new tab, delete. Changes are asked for in the chat |
| Switch | As every built-in MCP server: per chat (⋮ → *MCP servers…*, the new session sheet), Admin's *In every chat* |

## Storage

`<data>/chats/<chat>/artifacts/` (removed with the chat, backed up with it):

```
artifacts.json             {"version":1,"next":3,"rev":12,"artifacts":[...]}
pages/A1/3.html            the content of version 3 (.html or .md, after its format)
files/A1/chart.png         a file the page uses
```

`artifacts.json` is written whole, atomically (temp file and rename), under one lock per chat. An
artifact:

```json
{"id":"A1","title":"How a request flows","description":"From the browser to the database",
 "format":"html","key":"9f2c…(32 hex)","created_at":"…","updated_at":"…",
 "versions":[{"n":1,"at":"…","size":5120,"format":"html","note":"First draft"},{"n":2,…}],
 "files":[{"name":"chart.png","size":20480,"at":"…"}]}
```

`format` is the latest version's; every version keeps its own. `key` is random, made once, and is
what the page's address carries (below).

Limits (`internal/artifacts`, exported constants): 100 artifacts per chat, 2 MB of content per
version, the last 20 versions, 30 files per artifact, 20 MB per file, 200 MB per chat (content and
files), 200 characters of title, 500 of description, 300 of a version's note. A file name is one
plain name (letters, digits, `.`, `-`, `_`; no folder, not starting with `.` or `_`). A call that
attaches files keeps all of them or none; a file is copied, never moved, and must be a regular file.

## The tools (`internal/mcpserver/artifacts`, server `socrates-artifacts`)

Chats only. `artifact` is an artifact's id (`A3`) or its exact title.

| Tool | Does |
|---|---|
| `artifacts_list` | Every artifact: id, title, description, format, its latest version and when, its files |
| `artifact_create {title, content, format?, description?, files?}` | A new artifact (version 1); answers its id. `format` `html` (default) or `markdown`; `files` paths to attach (absolute or relative to the chat's folder) |
| `artifact_update {artifact, content?, edits?, format?, title?, description?, note?, files?, remove_files?}` | `content` replaces the page, `edits` `[{old, new, all?}]` change the latest content in place (each `old` must be found exactly once, or `all: true` replaces every one); either makes a new version, with `note` saying what changed. `files` attaches (a name there already is replaced), `remove_files` names files to take away |
| `artifact_delete {artifact}` | Deletes it with every version and file |
| `artifact_read {artifact, version?}` | Its metadata, the content of the latest version (or the one named), its versions and its files with absolute paths |

Answers are Markdown text. The server's instructions say when an artifact is worth making (a
picture explains it better than words; not for a few lines of text), how to write one (a fragment
using the kit's classes and colours, Markdown with Mermaid, SVG, Chart.js from the CDN), to edit
rather than make a new one for the same subject, and to mention the artifact in the chat by its
title.

## The page kit

An `html` content that is not a whole document (it has no `<html` or `<!doctype`) and every
`markdown` content are wrapped in the kit's document:

```html
<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" …>
<title>…</title><link rel="stylesheet" href="/artifact-kit/kit.css"><script type="module" src="/artifact-kit/kit.js"></script>
</head><body><main class="page">…the fragment…</main></body></html>
```

Markdown goes in as its text, escaped, in `<pre id="artifact-markdown">` inside `main` (so that the page
shows its Markdown rather than nothing when the kit cannot load), and `kit.js` renders it into `main` with marked (Socrates' vendored copy, served as `/artifact-kit/marked.esm.js`); raw
HTML in Markdown stays (the frame is sandboxed). `kit.js` then draws every Mermaid diagram
(`<pre class="mermaid">`, and Markdown's ```` ```mermaid ```` blocks) with Mermaid 11 from
`https://cdn.jsdelivr.net/npm/mermaid@11/dist/mermaid.esm.min.mjs`.

`kit.css` is a light, quiet stylesheet in Socrates' look: readable type and measure, headings,
tables, code, blockquotes, figures, SVG that fits, and a few classes an agent may use:

| Class | Is |
|---|---|
| `grid` | Cards or figures side by side, as many columns as fit (`grid-2`, `grid-3` fixed) |
| `card` | A bordered box; `card-title` its heading |
| `callout` (`info`, `ok`, `warn`, `error`) | A note with a coloured edge |
| `badge` (`info`, `ok`, `warn`, `error`) | A small label |
| `stat` (`stat-value`, `stat-label`) | A figure with its label |
| `muted`, `lead`, `small` | Quieter text, an introduction, small print |
| `steps` | An `<ol>` drawn as numbered steps |

Colours as custom properties for CSS and inline SVG: `--bg`, `--surface`, `--text`, `--muted`,
`--line`, `--accent`, `--ok`, `--warn`, `--error`, and the series `--c1` … `--c6`.

The kit's files are served at `/artifact-kit/{file}` (`kit.css`, `kit.js`, `marked.esm.js`), with
`Access-Control-Allow-Origin: *` (the page's origin is opaque, and a module script is fetched with
CORS), from the embedded `internal/web/static/artifact-kit/`.

## Serving a page

`GET /artifacts/{chat}/{artifact}/{key}/` is the page (the latest version; `?v=3` an older one), and
`GET /artifacts/{chat}/{artifact}/{key}/{file}` a file of it, so a relative name in the page finds
its file.

- **The page** needs the person's login (the cookie, which a frame of the app and a new tab send)
  and the key. It goes with `Content-Security-Policy: sandbox allow-scripts allow-popups
  allow-popups-to-escape-sandbox allow-forms allow-modals allow-downloads; frame-ancestors 'self'`,
  so that it has an opaque origin even when opened in a tab of its own: its scripts can never act
  as the person on Socrates (requests from it carry no cookie, and the API refuses its origin).
  `Referrer-Policy: no-referrer`, `Cache-Control: private, no-cache`, `nosniff`.
- **A file** needs the key only: the page's own requests carry no cookie. It goes as its type by
  name, with `Content-Security-Policy: sandbox` (an SVG or HTML file opened by itself runs
  nothing), `Access-Control-Allow-Origin: *` (the page may fetch its data), `nosniff`.
- The service worker leaves `/artifacts/` alone.
- A login proxy in front of Socrates (Cloudflare Access, an auth gateway) must let `/artifacts/`
  and `/artifact-kit/` through: the page's own requests (the kit, its files) carry no cookie, so a
  proxy that wants one answers them with its login page and the page stays empty. Socrates' login
  and the key still guard them.
- The app's own pages go with `Content-Security-Policy: frame-ancestors 'self'`, so a page cannot
  frame the app and dress it up.
- An address anyone may ask for (a file's) is checked for its form and for a chat that has
  artifacts before anything is looked up.

## API (behind `s.auth`, `internal/server/artifacts.go`)

| Route | Does |
|---|---|
| `GET /api/chats/{id}/artifacts` | `{artifacts: [...], rev, on}`: every artifact with its versions and files and `url` (the page's address, `/artifacts/{chat}/{id}/{key}/`); `on` whether the chat has the server on |
| `DELETE /api/chats/{id}/artifacts/{artifact}` | Deletes it |

An artifact in the list: `{id, title, description, format, version (the latest n), versions:
[{n, at, size, format, note}], files: [{name, size, at}], created_at, updated_at, url}`.

Every change (agent or person) publishes `{t: "artifacts", session, rev, artifact, what}` on
`/api/events` (`what`: `created`, `updated`, `deleted`); the chat page reads the artifacts again
when the frame names the chat it shows.

## The chat page

- **Artifacts button** in the top bar, beside the Tests button, shown for a chat that has the server
  on or has artifacts. Its badge: how many artifacts are new or changed since this device last
  showed them (remembered per device and chat).
- **The panel**, as the tests': a column on the chat's right over 1100 px (open or shut per device),
  a side sheet narrower. The Artifacts and the Tests column are never open together: opening one
  shuts the other. The list: each artifact's title, its description, "Version 3 · 2 min ago", a
  *New* or *Changed* tag while unseen. Empty: what artifacts are and how to ask for one; with the
  server off, how to switch it on.
- **The page view.** In the column, a press on an artifact turns the column into the page: it widens
  (half the stage), its head a Back button, the title, the version (a select when there are
  several, each with its note), *Open in a new tab* and *Delete*; below it the page in a sandboxed
  frame (`sandbox="allow-scripts allow-popups allow-popups-to-escape-sandbox allow-forms allow-modals
  allow-downloads"`). Narrower, the page opens in a full screen dialog with the same head. A new
  version of the artifact on screen reloads the frame (unless the person picked an older version).
- **Live.** When the agent makes a new artifact in the chat on screen, at a desk the column opens on
  it by itself, unless the person is looking at another artifact; narrower, the badge counts it and
  a snackbar offers *Open*.

## Code

| Where | What |
|---|---|
| `internal/artifacts` | The model, the store per chat (`artifacts.json`, pages, files), the edits, the Markdown answers, the page wrapping |
| `internal/mcpserver/artifacts` | The `socrates-artifacts` server: tools and instructions |
| `internal/server/artifacts.go` | The routes, the page and file serving, the events frame |
| `internal/web/static/artifact-kit/` | `kit.css`, `kit.js`: the page kit (not the app's UI: no DS, outside `make ds-check`) |
| `internal/web/static/js/artifacts.js`, `css/so-artifacts.css` | The button, the panel, the page view and dialog |
| `e2e/artifacts.mjs` | The browser scenario: a fake agent makes, edits and deletes artifacts through the server; the button, the column, the page view, versions, a phone |
