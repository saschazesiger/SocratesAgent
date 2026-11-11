# Chats in repositories: a GitHub App, a worktree and a branch per chat

Status: specification, 2026-11-11. Decided by the owner. Implementation follows in the same change set.

## 1. Why

Until now a chat worked in a folder on the server: a fresh empty one, a preset or a typed path. Several chats
of one project shared one checkout (`/root/SocratesAgent`), so every agent got a long paragraph on
coordination ("`git status` showing changes you did not make…", commit only your own hunks), and a branch switch,
stash or reset in one chat changed the files under another. The owner's observation that agents "work in
worktrees" came from Claude Code's *subagents* (`isolation: "worktree"`, checkouts under
`.claude/worktrees/agent-*`); the main agent always worked in the shared folder.

The way hosted coding agents work today (Claude Code on the web, Codex cloud, Copilot coding agent, Cursor
background agents) is the model this change takes over:

- **The repository is the unit**, not a folder. Socrates is connected to GitHub, and the person picks the
  repositories it works in.
- **Every chat gets its own isolated checkout** (a git worktree) on **its own branch**, made from a base branch
  the person picks. Nobody else works in that folder.
- **The branch is what lasts**, pushed to GitHub as the work goes on. The worktree is temporary.
- **How work comes back is the repository's rule**, written in its `AGENTS.md`: open a pull request or merge into
  the base branch. Where the file says nothing, the agent opens a pull request. Where it matters, GitHub's branch
  protection enforces the rule; `AGENTS.md` only asks.

The owner's decisions (2026-11-11): repositories only, no more folders on the server for new chats; connection
through a GitHub App of the owner's own; fallback rule "pull request"; a setup script per repository; build it
right away.

## 2. Terms

| Term | Meaning |
|---|---|
| **App** | The GitHub App this Socrates created for itself through GitHub's manifest flow (§3). One per Socrates. |
| **Installation** | The App installed on the owner's account or an organisation, with the repositories it may reach. Several installations are possible (personal account plus organisations). |
| **Repository** (`owner/name`) | A repository an installation grants. Exactly these are the repositories Socrates offers; the choice is made on GitHub's installation page, Socrates keeps no second list. |
| **Cache** | Socrates' own clone of a repository on the server, `<root>/.repos/<owner>/<name>.git` (bare). It holds the objects every worktree of that repository shares. |
| **Worktree** | A chat's checkout, `<root>/<name>-<id8>` (`git worktree add` from the cache). The agent runs there. |
| **Branch** | The chat's branch, `socrates/<slug>` by default. |
| **Base** | The branch the chat's branch was made from and where its work goes back to. |

`<root>` is the workspace root (`settings.workspace.root`, default `<data>/workspaces`, outside the backup).
`<id8>` is the first eight characters of the chat id.

## 3. Connecting GitHub (Admin → GitHub)

### 3.1 Creating the App: the manifest flow

Admin gets a card **GitHub**. Without an App it shows *Connect GitHub* and an optional field *Organisation*
(empty: the person's own account).

1. *Connect GitHub* posts a form from the browser to `https://github.com/settings/apps/new?state=<state>` (or
   `https://github.com/organizations/<org>/settings/apps/new?state=<state>`) with a `manifest` field:

   ```json
   {
     "name": "Socrates <host>",
     "url": "<origin>",
     "redirect_url": "<origin>/api/github/app/created",
     "setup_url": "<origin>/api/github/app/installed",
     "setup_on_update": true,
     "public": false,
     "hook_attributes": { "url": "<origin>/api/github/hook", "active": false },
     "default_permissions": {
       "contents": "write", "pull_requests": "write", "workflows": "write", "metadata": "read"
     }
   }
   ```

   `<origin>` is the origin the Admin page is open on. `<state>` is a random value the server keeps for ten
   minutes (`POST /api/github/app/start` returns it with the manifest and the target URL). `workflows: write` is
   needed because a push that changes `.github/workflows/` is refused without it. No webhook: Socrates asks
   GitHub when it needs to know.
2. The person confirms the name on GitHub. GitHub redirects the browser to `redirect_url?code=…&state=…`.
3. `GET /api/github/app/created` checks the state and exchanges the code (`POST
   https://api.github.com/app-manifests/{code}/conversions`, no authentication). It keeps the App's `id`,
   `slug`, `name`, `owner.login` and `html_url` in the settings document (`settings.github`), and seals `pem`
   (the private key), `client_secret` and `webhook_secret` with the data directory's key (`vault.Seal`, objects
   `github:pem`, `github:client_secret`, `github:webhook_secret`) into the kv table. Then it redirects the
   browser to `https://github.com/apps/<slug>/installations/new`.
4. The person picks the account and the repositories on GitHub and installs. GitHub redirects to
   `setup_url?installation_id=…&setup_action=install`; `GET /api/github/app/installed` redirects to
   `/admin#github`. The installation id itself is not trusted from the URL: Socrates lists installations with the
   App's JWT (§3.2).

Admin then shows the App (name, owner, link), every installation (account, *Choose repositories* →
`https://github.com/settings/installations/<id>` or the organisation's equivalent) and the repositories with
their settings (§3.4), and *Disconnect* (forgets the App's keys here; the App on GitHub is deleted by the person,
Admin links to its settings page).

Creating the App needs the person's GitHub login in the browser, which only they can do. A Socrates without an
App offers new chats only without a repository (§4.1) and says in the new-chat sheet where to connect GitHub.

### 3.2 Tokens

`internal/github`:

- **App JWT**: RS256 over `{"iat": now-60, "exp": now+540, "iss": <app id>}`, signed with the private key
  (standard library only: `crypto/rsa`, `crypto/sha256`, base64url). Used for `/app/installations` and to mint
  installation tokens.
- **Installation token**: `POST /app/installations/{id}/access_tokens` with `{"repositories": ["<name>"]}`, so a
  token reaches **only the one repository** it is asked for. Valid one hour; cached per repository until five
  minutes before `expires_at`.
- **Which installation reaches a repository**: from `GET /installation/repositories` per installation (paged),
  cached for a minute and refreshed on demand (Admin, the new-chat sheet, a token for an unknown repository).

The API and web bases come from `SOCRATES_GITHUB_API` (default `https://api.github.com`) and `SOCRATES_GITHUB_WEB`
(default `https://github.com`), so the tests run against a fake. `SOCRATES_GITHUB_GIT` (default
`https://github.com`) is the base of the clone URL `<git>/<owner>/<name>.git`; the tests point it at a folder of
bare repositories (`file://`).

### 3.3 API

| Method and path | What |
|---|---|
| `GET /api/github` | `{connected, app: {name, slug, owner, html_url}, installations: [{id, account, settings_url}], repos: [{full_name, default_branch, private, installation}], problem}` |
| `POST /api/github/app/start` | `{org}` → `{action, manifest, state}`; the page builds and submits the form. |
| `GET /api/github/app/created` | GitHub's redirect (§3.1). |
| `GET /api/github/app/installed` | GitHub's redirect (§3.1). |
| `DELETE /api/github/app` | Disconnect. |
| `GET /api/github/repos/{owner}/{name}/branches` | Fetches the cache (creating it the first time) and lists `refs/remotes/origin/*`: `{default, branches: [{name, sha, date}]}`, newest first. |
| `PUT /api/github/repos/{owner}/{name}/settings` | `{setup}` (§3.4). |

All of them are Admin-only except `GET /api/github` and the branch list, which every signed-in person may read.

### 3.4 Settings per repository

`settings.github.repos["owner/name"] = {setup: "<shell script>"}`. Admin shows a text area *Setup* per
repository. Only the setup script is kept per repository; everything else comes from the repository itself.

## 4. A chat in a repository

### 4.1 The new-chat sheet

The folder controls (dynamic, preset, typed path) give way to:

- **Repository**: a `dropdown()` of the repositories (`GET /api/github`), the last one used first, and a last
  entry *No repository* (an empty folder of its own, as the old "dynamic" mode: for questions, research or work
  on the server that is no project). Opened from a repository group's "+" in the sidebar, it is preset.
- **Base branch** (only with a repository): a `dropdown()` from the branch list, the repository's default branch
  preselected.
- **Branch**: a text field, prefilled `socrates/<slug>`, where `<slug>` is the first message's first words
  (lower case, `a-z0-9-`, at most 40 characters) or `<id8>` without a message. If the name is an existing branch
  on GitHub, the hint says *continues the existing branch* and the base field is disabled: the chat checks that
  branch out instead of making one (picking up a pull request's review, say). The server checks the name with
  `git check-ref-format --branch`.

Presets and typed paths go from Admin and the sheet. Existing chats keep their folder, their group in the sidebar
and their guidance (§6) until they are deleted. `PUT /api/sessions/{id}/workdir` ("Point to folder") stays for
those chats and is refused for a chat in a repository.

### 4.2 Creating the chat

`POST /api/chats` takes `{repo, base, branch}` instead of `{workdir_mode, workdir}` (both are still accepted for
`workdir_mode: "dynamic"` = *No repository*; preset and custom answer 400 with a sentence that says presets are
gone). With a repository the handler:

1. Checks that the App reaches the repository.
2. **Fetches the cache** (`git fetch --prune origin`), creating it the first time: `git init --bare`, `git remote
   add origin <url>`, `remote.origin.fetch = +refs/heads/*:refs/remotes/origin/*` (so a fetch never moves a
   chat's local branch), `git fetch`. The repository's git operations run under one lock per repository.
3. **Makes the worktree**: `git worktree add -b <branch> <root>/<name>-<id8> origin/<base>`, without an upstream (the branch is not on GitHub yet; the first push sets it); for an
   existing branch `git worktree add -b <branch> <path> origin/<branch>` with upstream `origin/<branch>`. A branch already
   checked out in another chat's worktree answers 409 naming that chat.
4. Writes the worktree's own git configuration (`extensions.worktreeConfig`, `git config --worktree`):
   `remote.origin.url` stays the plain HTTPS URL; the credentials come from the helper (§5.2).
   *As built:* nothing is written per worktree. The cache's configuration (shared by its worktrees) has the
   plain URL and `push.autoSetupRemote = true`, so a bare `git push` of a new branch works as well.
5. Stores the row with `workdir = <worktree>`, `repo`, `base`, `branch` (§8) and starts the chat as before.
6. Runs the **setup** (§4.3) before the agent's first turn, in the chat host's boot, so the HTTP request does
   not wait for it. *As built:* it runs in the server's pump just before the host is launched
   (`chatPeer.launch` → `prepareRepo`), which is where the worktree is made again too: the request has
   answered, the first message waits in the outbox, and the agent's instructions can name a failed setup.
   `options.setup.state` is `pending` → `running` → `done`, `failed` or `none`.

### 4.3 Setup

If the repository's *Setup* (§3.4) is set, it runs with `sh -e`; otherwise an executable
`.socrates/setup` in the worktree runs, if there is one. Working directory: the worktree. Environment: the
server's, plus `SOCRATES_REPO=owner/name`, `SOCRATES_BRANCH`, `SOCRATES_BASE`, `SOCRATES_WORKTREE`, and the
credential helper (§5.2), so `npm ci` against private packages on GitHub works. Timeout ten minutes. Output goes to
`<data>/chats/<id>/setup.log`. The chat shows *Setting up the repository…* while it runs and a notice when it
failed (with the log's last lines); the agent's instructions name the log and the failure, and the agent starts
either way. A typical script: `npm ci`, `cp /etc/<project>/.env .env`.

### 4.4 The chat's lifetime

| When | What Socrates does |
|---|---|
| Every **turn end** (`TypeTurnEnded`, not during a rebuild) | If the branch has commits GitHub does not have, `git push --force-with-lease origin <branch>` (the branch is the chat's own; a rebase onto the base rewrites it). Uncommitted changes are left alone: committing is the agent's work. A failed push is a notice in the chat. |
| **Done** | The turn-end push, then: worktree clean and everything pushed → `git worktree remove`, local branch deleted from the cache (GitHub has it). Otherwise the worktree stays, and the chat says why. |
| **Reopened**, or started after its worktree was removed | Before the host starts, the worktree is made again from `origin/<branch>` (§4.2 step 3, existing branch). |
| **Deleted** (by hand or by the retention sweep) | Uncommitted changes are committed to the branch as `WIP: uncommitted changes of chat "<title>"` and pushed, then the worktree and the local branch go. Nothing is lost silently. The branch on GitHub stays: GitHub's *Automatically delete head branches* removes merged ones. |
| **Every hour** | `git worktree prune` and `git gc --auto` in each cache. |

### 4.5 Pull requests and the base

- Chat tool **`open_pull_request`** `{title, body, draft?}`: the server opens the pull request (`POST
  /repos/{owner}/{name}/pulls`, head = branch, base = base) with an installation token, after pushing. It
  answers with the URL, or with the existing pull request of the branch. The chat keeps the URL and number
  (`options.pr`) and shows them.
- Merging into the base is plain git: `git fetch origin && git rebase origin/<base>`, tests, `git push origin
  HEAD:<base>` (fast-forward only). The credential helper has the rights.

## 5. The agent in a worktree

### 5.1 Instructions

A chat in a repository gets, instead of the shared-tree paragraph of `chattools.Guidance`:

> You work in your own git worktree of `<owner>/<name>` at `<worktree>`, on branch `<branch>`, made from
> `<base>`. Nobody else works in this folder. Commit as you go and push (`git push -u origin <branch>`); the
> remote and the credentials are set up, and Socrates pushes the branch at the end of every turn too.
> How finished work goes back is the repository's rule: follow its `AGENTS.md` (or `CLAUDE.md`). If it says
> nothing about it, open a pull request into `<base>` with `open_pull_request`. Before you merge or open the pull
> request, bring the branch up to date (`git fetch origin && git rebase origin/<base>`), run the tests again and
> push with `--force-with-lease`. To merge into `<base>` yourself: `git push origin HEAD:<base>` after that
> rebase; never force-push `<base>`, never switch this worktree to another branch.
> Other chats work on their own branches in their own worktrees. Use the chat tools when your work affects
> theirs: the same files on `<base>`, a shared service, a database, the build on the server.

followed by the rest of the chat-tools guidance (when to ask, what not to send). The setup's outcome (§4.3) is
appended when it failed. Chats without a repository and old folder chats keep today's text.

The instructions are passed like today's guidance: Claude Code's `--append-system-prompt` (rebuilt at every
launch), Codex's `developerInstructions` (only for a new thread; a resumed thread keeps them, which is right since
a chat never changes repository or branch), OpenCode's MCP instructions.

### 5.2 Credentials

The agent never sees a long-lived secret. Its environment gets git configuration through
`GIT_CONFIG_COUNT`/`GIT_CONFIG_KEY_n`/`GIT_CONFIG_VALUE_n`:

```
credential.https://github.com.helper = ""                      (drops root's gh helper for this process)
credential.https://github.com.helper = !<socrates> git-credential
credential.https://github.com.useHttpPath = true
```

`socrates git-credential get` reads git's request (`protocol`, `host`, `path`), and asks the server over the
chat's tool socket with the chat's token (both already in the host's environment for the chat tools) for a
token for that repository. The server answers only for **the chat's own repository**; anything else gets no
credentials and git fails with its own message. The answer is `username=x-access-token`, `password=<installation
token>`. Server-side git (cache, push at turn end) passes the same token with `-c
http.extraHeader=Authorization: Basic …` and never writes it to disk.

*As built:* the configuration is a file, `<data>/chats/<id>/gitconfig`, named by `GIT_CONFIG_GLOBAL` in the
agent's environment (written by the host at every start and account switch). It includes the person's own
global configuration first, then the three lines above for the scheme and host of the clone URL, the helper
being `<socrates> git-credential --data <data> --chat <id>`, which reads the chat's tool endpoint from
`host.json`. Not `GIT_CONFIG_COUNT/KEY/VALUE` and not the token variable: Codex keeps variables whose names
hold `KEY`, `SECRET` or `TOKEN` out of the commands its agent runs. The setup (§4.3), a child of the server,
gets the helper through `GIT_CONFIG_COUNT`. The server's own git passes the header through `GIT_CONFIG_*`
too, so the token is in no process listing.

`GH_TOKEN` is not given: the installation token expires within the hour, and `open_pull_request` covers what
`gh` would be used for. The agent still runs as root, so root's own `gh` login stays reachable: the App limits
what Socrates hands out, it is not a sandbox.

### 5.3 What else follows the repository

| Feature | Before | Now for a chat in a repository |
|---|---|---|
| Sidebar group | the folder | the repository (`owner/name`, shown as `name`); "+" starts a chat in it |
| `list_chats`, peers note | same git working tree (`chatScope`) | same repository; each entry names its branch and whether it is on the base |
| Notes board | `board:<folder>` | `board:github:<owner>/<name>` |
| Docs of a group | Markdown files of the folder | Markdown files on the default branch, read from the cache (`git ls-tree`, `git show`); a chat's links resolve in its own worktree |
| Folder agent config and MCP of the folder | read from the folder | read from the worktree (unchanged code: the worktree is a folder) |
| Info dialog | Folder | Repository, Branch, Base, Pull request, Worktree |
| Header | — | a chip with the branch, linking to the pull request or the branch on GitHub |

## 6. What stays as it was

- Chats made before this change keep their folder, their sidebar group, "Point to folder" and the shared-tree
  guidance. Nothing moves them.
- *No repository* chats are the old dynamic folders.
- Subagents of Claude Code may still use `isolation: "worktree"`; inside a chat's worktree their checkouts land
  under its `.claude/worktrees/`, and they go with the worktree.

## 7. Claude Code's memory

Claude Code keeps a project's auto memory under `<config>/projects/<escaped path>/memory`. Whether that path is a
worktree's own or the repository's shared one decides whether chats of one repository share their memory. The
implementation checks this against the installed binary and, if the path is per worktree, links each worktree's
memory folder to one per repository (`<config>/projects/-socrates-repo-<owner>-<name>/memory`), so memory is
shared per repository as it was per folder. The memories of `/root/SocratesAgent` are copied into the
repository's folder once, the first time a chat of `saschazesiger/SocratesAgent` starts.

*As built (checked against Claude Code 2.1.322):* the default memory path is keyed by the project's
canonical git root, and for a linked worktree of a bare repository that is the common git directory: a
chat in `<root>/app-1234abcd` writes to `<config>/projects/<root with every non-alphanumeric a dash>--repos-
<owner>-<name>-git/memory`. Every worktree of one cache shares it already, so nothing is linked. At the first
start of a Claude chat of a repository on an account, the memory of an older folder chat whose checkout's
`origin` is the same GitHub repository is copied into that folder (`seedClaudeMemory`), once.

## 8. Data

- `sessions` gets `repo TEXT NOT NULL DEFAULT ''`, `base TEXT NOT NULL DEFAULT ''`, `branch TEXT NOT NULL
  DEFAULT ''` (schema version 13, `addSessionColumns`). `repo` empty = a chat without a repository or an old
  folder chat.
- `options.pr = {number, url}` once a pull request is open; `options.setup = {state, at}`.
- kv: the sealed App keys (§3.1).
- settings: `github = {app_id, slug, name, owner, html_url, repos: {"owner/name": {setup}}}`.
- `workspace.presets` and `workspace.allow_custom` are no longer read for new chats and leave Admin; they stay in
  the document so a downgrade still finds them.

## 9. Tests

- `internal/github`: JWT (signature checked with the public key), token cache and scoping, manifest conversion,
  installation and repository listing, pull request creation, all against an `httptest` server.
- Repository workspaces: cache creation and fetch, worktree for a new and an existing branch, the 409 for a branch
  in use, turn-end push only when ahead, done/reopen/delete with clean, dirty and unpushed worktrees, the WIP
  commit, setup success, failure and timeout. All against bare repositories in a temp folder (`file://`).
- The credential helper end to end: a `git push` through it reaches the fake with the token, a different
  repository gets nothing.
- Guidance: the repository text for a repository chat, the old text for the others.
- e2e: Admin's GitHub card against a fake GitHub (connect, repositories, setup field); the sheet's repository,
  base and branch fields; a chat in a repository grouped under it with its branch chip; the fake CLI committing
  and the push at turn end arriving in the bare remote.

## 10. Operations

`docs/OPERATIONS.md` gets a section *GitHub*: connecting, choosing repositories, the setup script, where caches
and worktrees live (`<root>/.repos`, `<root>/<name>-<id8>`, outside the backup), and that a chat's branch on
GitHub is what survives a lost server. README gets a short row.
