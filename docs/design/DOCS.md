# Docs per project folder (2026-11-10)

Asked for by the owner on 2026-11-10: per project folder a way to read its documentation, the Markdown
files of the repository with their diagrams ("graphs and dependencies"), opening on the folder's
README.md, with the folder structure beside it, "intuitive and beautiful". A link in a chat to such a
file opens it beside the chat. Decided with the owner the same day:

| Topic | Decision |
|---|---|
| Which files | Every Markdown file (`.md`, `.markdown`) of the repository, in its own folders. No `docs/` convention: Socrates takes the structure as it is |
| Agent files | `AGENTS.md`, `CLAUDE.md` and the like are files like any other; the usual names get a glyph of their own so that they are told apart at a glance |
| Where | The folder's ⋮ in the sidebar (beside its +) holds Notes, Docs and VS Code; Docs only when the folder has Markdown |
| Reading only | The files are changed through the chat; an editor in the browser would compete with the agents editing the same files |
| Agents | Every chat's agent is told to link a Markdown file of its folder as a relative Markdown link (`agent.DocsGuidance`) |
| Agents that do not (2026-11-11) | In the first day agents named such files 1513 times as a `code` path against 17 links, and a few times by their GitHub address. The instruction now says "every time" and names what not to write, and the chat opens a `code` path to a Markdown file, and a link to one on the folder's own forge repository, beside the chat as well |

## 1. The sidebar

- A project folder's head has its **+** and a **⋮** (a GitHub repository's group too, §7) (`.so-folder__more`, session.js `folderMoreHtml`).
  The ⋮ opens the row menu's element (`#sessionMenu`) drawn for the folder (`openFolderMenu`):
  **Notes** (the board, BOARDS.md), **Docs** (when the folder has Markdown), **Open VS Code** / **VS Code
  (open)** (where VS Code is available or the folder has one). A phone, or a menu too tall, gets the
  same items in an action sheet. VS Code opens inside the tap (a tab of its own).
- While the folder's VS Code runs, the ⋮ wears a dot in VS Code's colour (`.so-folder__running`) and
  says so in its name. This replaces the separate notes and VS Code marks of before.
- Whether a folder has Markdown is asked for every folder of the list in one request
  (`POST /api/docs/probe`, docview.js `probeDocs`), again after a minute; a menu opened before the
  answer waits for it.

## 2. The Docs page

- A page on the stage like Home and the notes (`#docs`, docs.js `mountDocs`); nothing is attached
  while it is shown. The address is `#docs=<folder>&page=<path>` (`&at=<heading>` for a heading), so
  Back, a reload and a link work. A file the frame moves to (a link in it) is put into the address in
  place (`history.replaceState`); the frame's own navigations are what Back steps through.
- **Head**: DS breadcrumbs (the folder, then each folder of the path, a folder with a README a link to
  it), "Docs", the folder's name; *Files* (narrow only), *Read the files again*, *Open in a new tab*.
- **Tree** (a column of `--so-docs-nav-width`, a side sheet under 900 px): a folder shows its README
  first, its other files by name, then its folders, each shut until opened; the folders of the file on
  screen open by themselves and the file is marked (`aria-current`). A filter finds files whose path
  has every word typed and lists them with their folder.
- **The file**, in a sandboxed frame (below), drawn by the artifact kit: GitHub Markdown, tables,
  pictures of the repository, Mermaid diagrams (flowcharts, sequence, class, ER, Gantt ...), GitHub's
  alerts (`> [!NOTE]`, `[!TIP]`, `[!IMPORTANT]`, `[!WARNING]`, `[!CAUTION]`) as callouts, an anchor on
  every heading.
- **On this page**: the page's headings of levels 2 and 3 in a column at 1400 px and wider, when there
  are at least two; a press scrolls the frame to the heading.
- **Opening**: the file asked for, else the README at the top (the server's `readme`), else the first
  file. A folder without Markdown says so.
- **Live**: while the page is on screen, the file shown is checked every 4 s (`GET /api/docs/stat`);
  a change on disk draws it again at the same scroll position (the kit reports it, `?y=` brings it
  back). The tree is read again every 30 s and when the tab comes back; a file that went is said in
  a snackbar and the folder's first file is shown.
- Glyphs by name (docview.js `glyphFor`, DS glyphs only): README `info`; AGENTS, CLAUDE, GEMINI,
  COPILOT-INSTRUCTIONS, SKILL, CONVENTIONS `sparkles`; CHANGELOG, CHANGES, HISTORY, NEWS, RELEASES
  `history`; CONTRIBUTING `userPlus`; CODE_OF_CONDUCT, CODEOWNERS, AUTHORS, MAINTAINERS `users`; LICENSE,
  COPYING, NOTICE `key`; SECURITY `shield`; TODO, ROADMAP, PLAN `flag`; FAQ, HELP, SUPPORT,
  TROUBLESHOOTING `helpCircle`; INSTALL, SETUP, GETTING_STARTED, QUICKSTART, USAGE `download`; DEPLOY,
  OPERATIONS, RUNBOOK `server`; ARCHITECTURE, DESIGN, DECISIONS, OVERVIEW, ADR `box`; API, REFERENCE,
  SPEC `fileCode`; TESTING `checkCircle`; STYLE, STYLEGUIDE `pencil`; anything else `fileText`; a
  folder `folder`.

## 3. Links in a chat

- A link in a chat's conversation whose target is a Markdown file inside the chat's working
  directory opens the file beside the chat (docside.js `docLinkTarget`, chatview.js `ctx.docLink`):
  a path relative to the folder (`docs/ARCH.md`, `./README.md#setup`), an absolute path inside it, or
  a `file://` address of one. A web address, a file outside the folder or one that is not Markdown is
  left to the browser. A modified or middle click is the browser's too.
- **A `code` path** to a Markdown file (`` `docs/ARCH.md` ``, `` `./README.md#setup` ``, an absolute path;
  nothing else in the span) is drawn as a link to it (md.js `codespan`, not inside a link's text), and
  opens the same way. A path the folder's listing does not have opens the one file whose path ends in
  it (`` `ARCH.md` `` is `docs/ARCH.md` when only one file is called so); else a snackbar says it is
  not a Markdown file of the folder, and a path link never goes to the browser (it would show
  Socrates' own page).
- **A forge address** of the folder's own repository opens beside the chat too
  (docside.js `forgeLinkTarget`): `https://<host>/<owner>/<name>/blob/<ref>/<path>` (GitLab's
  `/-/blob/`), when `<host>/<owner>/<name>` is the folder's remote as the probe said it (below) and
  the path lies in the folder. The branch is not looked at: the file shown is the one on disk.
  Another repository's address stays the browser's.
- **Another working tree** of the folder's git repository: an agent that works in a `git worktree`
  outside the chat's folder (a folder chat whose agent made one for its branch) names the files it
  wrote there by paths that mean that tree. A path the folder does not have (docside.js `open`), and
  a path out of the folder, absolute or with `..` (`openOutside`), is looked for with `GET
  /api/docs/locate`; the file found opens beside the chat with its tree as the folder. Only a path no
  tree has gets the snackbar.
- At a desk (over 1100 px) the file is a column on the chat's right (`#docsidePanel`, half the
  stage), never open with the tests' column: opening one shuts the other. Its head:
  the page's title, *Open in Docs* (the Docs page on the same file, with the tree), *Open in a new tab*,
  *Close*. Narrower it is a full screen dialog with the same head. A link in it to another Markdown
  file stays in it; the file is drawn again when it changes on disk. Another chat on the stage closes
  it.
- Chat agents are told (`agent.DocsGuidance`, appended to Claude Code's system prompt, Codex's
  developer instructions and the instructions of the ask server other agents get): every time a
  Markdown file of the working directory is mentioned (in a list, a table, the closing summary), as a
  Markdown link with its path relative to the working directory, optionally with a `#heading`; never
  only as a `code` path or a bare name, never by a GitHub, web or `file://` address. A file it wrote in
  another working tree (a `git worktree` outside the working directory) by its absolute path.

## 4. Server

`internal/docs` scans and resolves, `internal/server/docs.go` serves.

- **A folder** is the sidebar's folder key (the working directory without trailing slashes). It is
  served only when it is an absolute, existing directory and either some session's working directory
  or inside the workspace root, or that folder as it stands in another working tree of its git
  repository (`docs.Worktrees`).
- **Working trees** (`docs.Worktrees`): the folder in every working tree of its repository, read from
  the repository's directory: the main tree (whose `.git` it is) and each entry of `.git/worktrees/`
  (its `gitdir` file names the tree's `.git`, absolute or relative to the entry). Only what the
  repository names counts: the folder's own tree must be one of them (a `.git` that merely points at a
  repository makes no tree), each tree's `.git` must lead back to the same repository, and the folder
  must be a directory inside each tree with its symbolic links followed. Whether a folder is shown as
  such a tree is kept 20 s: every page and picture asks.
- **Scan**: every regular `.md`/`.markdown` file under the folder, without following symbolic links,
  skipping folders whose name starts with `.` and `node_modules`, `vendor`, `third_party`,
  `bower_components`, `dist`, `build`, `target`, `out`, `__pycache__`, `venv`, `site-packages`,
  `coverage`. At most 2000 files, 16 folders deep, 100000 entries looked at (then `truncated`).
- **Probe**: any Markdown at the top, else a walk with the same rules that stops at the first file or
  after 5000 entries; answers kept 20 s.
- **Repository** (`docs.RepoOf`, for the probe): the nearest `.git` at or above the folder (a
  directory, or a worktree's file `gitdir:` with its `commondir`), its config's `origin` url (else the
  first remote's) as `host/owner/name` in lower case (https, ssh and `git@host:` forms; a local path
  is none), and the folder's path inside the repository as `prefix`. Read from the files: no git
  is run.

| Route | Does |
|---|---|
| `POST /api/docs/probe` `{folders: [...]}` (at most 300) | `{docs: {"<folder>": true\|false}, repos: {"<folder>": {remote, prefix}}}`: `repos` for a folder with docs in a git repository with a remote (`docs.RepoOf`) |
| `GET /api/docs?folder=<path>` | `{folder, files: [{path, size, mtime}], truncated, base, readme}`: `base` is `/docs/<token>/`, `readme` the README.md, README.markdown or index.md at the top (or the first file, or "") |
| `GET /api/docs/stat?folder=<path>&path=<rel>` | `{path, size, mtime, kind}` of one file (a folder's README for a folder), 404 when it is gone |
| `GET /api/docs/locate?folder=<path>&path=<rel or abs>` | `{folder, path}` of a Markdown file in a working tree of the folder's repository: a relative path in the tree that has it (the one changed last when several do), an absolute one in the tree it lies in; 404 when none has it |
| `GET /docs/{token}/{path...}` | The file (below) |

- **The token** is the capability for a folder's files: `base64url(folder) + "." +
  base64url(HMAC-SHA256(key, "docs\0" + folder))` (32 characters), the key 32 random bytes made once and
  kept in the database (`kv` `docs.key`, so in the backup). Only a signed-in caller gets it (`GET
  /api/docs`). The frame's own requests carry no cookie (its origin is opaque), so `/docs/` asks for the
  token and not the login.
- **What is served**: a Markdown file or a picture (`png jpg jpeg gif svg webp avif ico bmp`) inside
  the folder, by a path with no `..`, no part starting with `.` and none of the skipped folders, that
  resolves (symbolic links followed) inside the folder; Markdown up to 5 MB, a picture up to 20 MB. A
  folder is redirected to its README.md (or index.md). Everything else is a 404: source code, `.env`,
  `.git`, a path out of the folder.
- **A Markdown file** goes in the kit's document (`docs.Page`, `<body data-docs="<path>">`) with `Content-Security-Policy: sandbox allow-scripts allow-popups
  allow-popups-to-escape-sandbox allow-modals allow-downloads; frame-ancestors 'self'; script-src 'self'
  <host>/artifact-kit/ https://cdn.jsdelivr.net/npm/mermaid@11/; connect-src 'none'; object-src 'none';
  base-uri 'none'; form-action 'none'`, `Referrer-Policy: no-referrer`, `nosniff`, `Cache-Control:
  private, no-cache`. A doc is a file of someone's repository (or a page a chat's agent wrote, PAGES.md): its raw HTML stays (marked), but no script of its own runs (no inline script or
  handler) and it makes no request of its own that could carry the page's address - the token - away.
  `?raw=1` is its text (a sandbox, no CORS).
- **A picture** goes as its type with `Content-Security-Policy: sandbox`, `nosniff`,
  `Access-Control-Allow-Origin: *`.
- The service worker leaves `/docs/` alone. A login proxy in front of Socrates must let `/docs/`
  through as it does `/artifacts/` and `/artifact-kit/`.

## 5. The kit in a docs frame

`body[data-docs]` turns on (artifact-kit `kit.js` `docsMode`): the frame tells the app (postMessage,
`{socratesDocs: 1, t, ...}`) the page it shows (`page`: path, title, headings), where it is scrolled
(`scroll`), and a link it did not follow (`file`: a file of the folder that is not Markdown,
`outside`: a link out of the folder); a link to a Markdown file or a folder of the folder is followed
in the frame, a picture opens in a tab. The app sends `scroll` (a heading's id). The anchors, the
alerts and the scroll back to `#heading` are the kit's own. The kit (`/artifact-kit/`) keeps the name
of Socrates Artifacts, which it was made for (removed 2026-11-11, PAGES.md).

## 6. Code and tests

| Where | What |
|---|---|
| `internal/docs` | Scan, Has, Resolve, Page, RepoOf, Listed |
| `internal/server/docs.go` | The routes, the token, the allowed folders |
| `internal/server/docsrepo.go` | A repository's group: its docs folder, who may see it (§7) |
| `internal/repos/docs.go` | A repository's docs written out per commit (§7) |
| `internal/agent/docsguide.go` | `DocsGuidance` |
| `internal/web/static/js/docview.js` | Listing, probe, addresses, glyphs, frame messages |
| `internal/web/static/js/docs.js` | The Docs page |
| `internal/web/static/js/docside.js` | A file beside a chat; which link means which file (`docLinkTarget`, `forgeLinkTarget`, `fileIn`) |
| `internal/web/static/js/md.js` | A `code` path to a Markdown file as a link |
| `internal/web/static/css/so-docs.css` | Both |
| `internal/web/static/artifact-kit/kit.js` | Docs mode, anchors, alerts |

Tested by `go test ./internal/docs ./internal/repos ./internal/server -run 'Docs'` and `node e2e/run.mjs docs` (the
boards and VS Code scenarios open their pages through the folder's ⋮; `repoflow` opens a repository's Docs and
fetches a commit pushed meanwhile).

## 7. A GitHub repository's group (2026-11-12)

Since chats work in GitHub repositories (REPOS.md), a project's chats are grouped by their repository
(`github:<owner>/<name>`), each in a worktree of its own, and the group had no Docs. Asked for by the owner on
2026-11-12, "best directly via GitHub": the group's Docs are the repository's **default branch as GitHub has
it**, not any chat's worktree.

- **Where**: the group's ⋮ has Notes and Docs (no VS Code: each chat has a worktree of its own). The probe
  answers for the key without asking GitHub: true for a repository a chat works in while GitHub is
  connected, or, once its docs were written, whether they have Markdown.
- **From GitHub**: the listing (`GET /api/docs?folder=github:<owner>/<name>`) fetches the repository's cache
  (`<root>/.repos/<owner>/<name>.git`, the one the chats share) when it last fetched longer than a minute ago,
  and *Read the files again* asks with `&fetch=1`, which fetches now. A fetch that fails shows what was
  fetched last, and is tried again only after the same minute (a GitHub that hangs must not hold the
  repository's lock for every listing); a repository the App no longer reaches is not shown at all, what was
  fetched included. Not the REST API: one `git fetch` brings every file and picture, and the cache is there
  anyway.
- **Written out per commit** (`repos.Manager.Docs`): the Markdown files and pictures of the branch's newest
  commit that the Docs view would list (`docs.Listed`: no hidden or skipped folder, within the size bounds;
  no symlink or submodule) go into `<root>/.repos/<owner>/<name>.docs/<commit>/`, at most 5000 files and
  256 MB, read with `git cat-file --batch`. A file whose blob the folder before had is linked from it, so it
  keeps its time and the page on screen is not drawn again; a changed file gets the commit's time. The last
  three commits are kept. Every route of the view then reads that folder as it reads a project folder.
- **The token** signs the key (`github:<owner>/<name>`), not the commit's folder, so a page's address
  outlives the next commit. A page and a stat read the folder written last (without the repository's lock,
  which a chat's push may hold for a while); only the listing goes to GitHub.
- **Who**: GitHub connected and some chat working in the repository (whatever the case of its name).
  Without GitHub the pages are 404.
- **The page's head** names the branch (`· main`) beside the repository and, where the clone URL is a web
  address, has a *GitHub* button (Z-Dash has no GitHub glyph: the external-link one and the word) for the file on screen; a link in a page to a file that is not Markdown
  offers the same in its snackbar.
- `locate` and the links in a chat are unchanged: a chat's links resolve in its own worktree, which is a
  folder like any other.

| Route | For a repository's group |
|---|---|
| `GET /api/docs?folder=github:<owner>/<name>[&fetch=1]` | As for a folder, plus `repo: {name, branch, web}` (`web` the branch's files on GitHub, `https://github.com/<owner>/<name>/blob/<branch>/`) |
| `GET /api/docs/stat`, `GET /docs/{token}/{path}` | The folder written last |
| `POST /api/docs/probe` | As above, no `repos` entry |
