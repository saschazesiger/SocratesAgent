# 🛠️ Operations

Everything you need to run Socrates on a server. For a first look, see the [README](../README.md).

- [📦 What the installer sets up](#-what-the-installer-sets-up)
- [🔄 Updates](#-updates)
- [🚢 Deploy on every push](#-deploy-on-every-push)
- [⚙️ Configuration](#️-configuration)
- [🗺️ What is where](#️-what-is-where)
- [💾 Backup & restore](#-backup--restore)
- [🧹 Uninstall](#-uninstall)

---

## 📦 What the installer sets up

`scripts/install.sh` builds Socrates from the repository's newest commit and installs:

| Tool | Source | Location |
| --- | --- | --- |
| git, ripgrep, jq, curl, ncurses-term… | distribution | apt |
| tmux ≥ 3.3 | distribution, **held** | apt |
| Go (to build Socrates) | go.dev, checksum verified | `/opt/socrates/go/<version>` |
| Node | nodejs.org, checksum verified | `/opt/socrates/node/<version>` → `/usr/local/bin` |
| Claude Code | native installer | `~/.local/bin/claude` |
| Codex | standalone installer | `~/.local/bin/codex` |
| OpenCode | release binary | `/opt/socrates/opencode/<version>` → `~/.local/bin/opencode` |
| code-server | release tarball | `/opt/socrates/code-server/<version>` → `/usr/local/bin` |

Then it starts `socrates.service` and switches on `socrates-update.timer`.
✅ You can run it again at any time: it repairs what is missing and updates the rest.

> 💡 A server that ran an earlier Socrates keeps its sign-ins: the first start turns them into accounts.

---

## 🔄 Updates

**Everything updates itself.** The timer runs every night between 3 and 5, and 10 minutes after every boot.
It updates Socrates, Go, Node, Claude Code, Codex, OpenCode, code-server, cloudflared and the packages.

👉 **Admin → Updates** shows the last run, every version and the log, and has **Update now**.

### By hand

```bash
sudo /usr/local/lib/socrates/update.sh              # everything
sudo /usr/local/lib/socrates/update.sh --deps       # only the tools
sudo /usr/local/lib/socrates/update.sh --socrates   # only Socrates
sudo /usr/local/lib/socrates/update.sh --repair     # restore what's missing, update nothing
```

### Why updates are safe

| | |
| --- | --- |
| 🟢 **Sessions keep running** | They live in their own systemd scopes. Only the web server restarts, and the page reconnects. |
| 📂 **No tool changes under a running session** | Each version gets its own directory, and a link is switched. Old versions are deleted only once nothing uses them. |
| 🧪 **Tested before it goes live** | A new build is started once on a scratch directory first. |
| ↩️ **Automatic rollback** | If it doesn't answer `/api/health` with its own version, the old binary and database come back, and that commit is skipped. |
| 🐕 **Watchdog** | A Socrates that hangs is restarted like one that crashed. |
| 📴 **Offline-safe** | Without network, nothing is changed. A tool that fails to download doesn't block the others. |

> ⚠️ A **reboot** ends the terminals. A Shell restarts in its folder when you open it, and chats continue normally.

### tmux is held

A tmux upgraded under a running server can no longer talk to it, so tmux is held with `apt-mark hold`.
The updater installs a newer tmux on its own once no tmux server is running, and Admin says when one is waiting.
To install it right away:

```bash
systemctl stop "socrates-tmux*.scope"   # ends every Shell; they resume when opened
sudo /usr/local/lib/socrates/update.sh
```

---

## 🚢 Deploy on every push

```bash
sudo scripts/deploy-runner.sh
```

This registers a GitHub Actions runner on the server. It only connects out, so it works behind NAT.

```
push to main ──▶ GitHub builds + attests ──▶ runner ──▶ deploy.sh verifies attestation ──▶ smoke test · health check · rollback
```

The runner's user may run `deploy.sh` as root, and nothing else.

---

## ⚙️ Configuration

The settings live in `/etc/socrates.env`:

| Variable | Default | Meaning |
| --- | --- | --- |
| `SOCRATES_ADDR` | `127.0.0.1:8080` | Listen address |
| `SOCRATES_DATA_DIR` | `/var/lib/socrates` | Data directory |
| `SOCRATES_AUTO_UPDATE` | `all` | `all` · `deps` (tools only) · `off` (repairs only) |
| `SOCRATES_NODE_MAJOR` | `lts` | Pin Node to a major version, e.g. `22` |
| `SOCRATES_CODE_SERVER_VERSION` | `latest` | Pin code-server to a version |
| `SOCRATES_CLAUDE_CHANNEL` | `latest` | Claude Code's release channel |
| `SOCRATES_REPO` / `SOCRATES_BRANCH` | this repo / `main` | Where Socrates is built from |
| `SOCRATES_GITHUB_API` / `SOCRATES_GITHUB_WEB` / `SOCRATES_GITHUB_GIT` | `https://api.github.com` / `https://github.com` / `https://github.com` | Where GitHub is (only the tests change these) |
| `SOCRATES_FOLDER_CHATS` | unset | `1` lets `POST /api/chats` still start a chat in a preset or typed folder (the test suites); the sheet never offers it |

---

## 🐙 GitHub & repositories

New chats work in GitHub repositories: each chat gets its own worktree on its own branch, and the branch is pushed to GitHub ([📐 spec](design/REPOS.md)).

| | |
| --- | --- |
| 🔗 **Connect** | **Admin → GitHub → Connect GitHub** (optionally for an organisation). GitHub creates a public App of this Socrates' own; confirm its name, then pick the repositories on GitHub's install page. **Choose repositories** changes the pick later |
| 🏢 **Organisations** | **Add an organisation** opens the App's install page, where GitHub asks for the account. An App created before Socrates made them public is private and can only be installed on its owner: make it public once on GitHub, App settings → *Advanced* → *Make public* (the card links there) |
| ✅ **Accepting** | Anyone can install a public App, so Socrates uses only the installations you accepted (`settings.github.accepted_installations`). Coming back signed in from GitHub's install page accepts that installation; one installed otherwise waits on the card (*installed on … — accept only if that is yours*) with **Accept** and **Remove**. **Remove** uninstalls the App from that account on GitHub. The installation on the App's owner is accepted on its own while nothing else is |
| 🔑 **Rights** | The App may read and write contents, pull requests and workflows of the chosen repositories, nothing else. Every token Socrates hands out reaches one repository and lasts an hour; the App's private key is sealed with the data directory's key (`secrets.key`) |
| 🌿 **A chat** | The sheet asks for the repository, the base branch and the branch (`socrates/…`; an existing branch is continued). *No repository* is an empty folder of its own |
| 🧰 **Setup** | Admin → GitHub → a repository's *Setup* (a dialog): a shell script run in every new worktree before the agent starts (`npm ci`, copying an `.env` …). Without it an executable `.socrates/setup` in the repository runs. Log: `<data>/chats/<chat>/setup.log` |
| 📤 **Push** | At the end of every turn Socrates pushes the branch if it has new commits. How work comes back (pull request or merge into the base) is the repository's `AGENTS.md`; without a rule the agent opens a pull request |
| 📋 **The list** | One line per repository, those with a setup script first; a filter once there are more than six, and the first eight until *Show all* |
| 📂 **Where** | `<workspace root>/.repos/<owner>/<name>.git` is one shared clone per repository, `<workspace root>/<name>-<chat id[:8]>` a chat's worktree. Both are outside the backup: **the branch on GitHub is what survives a lost server** |
| 🧹 **Cleaning up** | **Done**: a clean, pushed worktree is removed (made again if the chat is reopened). **Delete**: uncommitted changes are committed as `WIP: …` and pushed first. Branches on GitHub are never deleted by Socrates (GitHub's *Automatically delete head branches* does it for merged ones) |
| 🔌 **Disconnect** | Admin → GitHub → *Disconnect* forgets the App's keys here; delete the App itself on GitHub (the card links to it) |

Chats from before this change keep their folder.

---

## 🗺️ What is where

| | |
| --- | --- |
| `socrates.service` | The web server: `systemctl status socrates`, `journalctl -u socrates -f` |
| `socrates-agents-<chat>.scope` | One chat's agent, which outlives the service |
| `socrates-tmux.scope` | The Shell terminals |
| `socrates-update.timer` | Nightly update: `journalctl -u socrates-update` |
| `/etc/socrates.env` | Configuration (see above) |
| `/var/lib/socrates` | **All data**: database, chats (with their tests), accounts, transcripts, terminals, backup settings |
| `/usr/local/bin/socrates` | The binary |
| `/usr/local/lib/socrates` | The updater |
| `/opt/socrates` | Source, Go, Node, OpenCode, code-server |
| `/root` | `HOME`: the agents' folders link into the accounts |

🗑️ A session filed under **Done** stops its program at once and keeps only its files (for a chat, `<data>/chats/<chat>/`). It is deleted 14 days later; Admin → Workspace → *Keep done sessions for* changes that (`workspace.done_days`, 1 to 365), for the done sessions already there too. A folder chat's working directory is never deleted; a repository chat's worktree goes as described under [GitHub & repositories](#-github--repositories).

---

## 🧪 Chat tests

| | |
| --- | --- |
| 📂 **Where** | `<data>/chats/<chat>/tests/`: `tests.json` (every test of the chat with its runs) and `files/<test>/r<run>/` (the screenshots, logs and output that are their evidence) |
| 🧹 **Cleaning up** | They go with their chat; a test keeps its last 20 runs. Limits: 200 tests per chat, 20 MB per file, 500 MB per chat (`chats.test_evidence_mb` in the settings document, `PUT /api/settings`) |
| 🔌 **Switch** | Socrates Tests is a built-in MCP server: off in every chat until Admin (*In every chat*) or the chat switches it on |
| 🗑️ **Test mode** | The per-repository test catalog it replaced is gone: the first start of this build deletes `<data>/tests/`, `<data>/run/tests/`, `<data>/cache/tests/` and the sessions of its runs. A repository's `.socrates/tests/` is no longer read |

---

## 📄 Chat pages

| | |
| --- | --- |
| 📂 **Where** | `<data>/chats/<chat>/pages/`: the Markdown files (and their pictures) a chat's agent writes to explain things. Made when the chat's agent starts, removed with the chat, in the backup |
| 🌐 **Addresses** | Served like a project's docs, at `/docs/<token>/<path>` (below) |
| 🗑️ **Artifacts** | Socrates Artifacts, which they replaced, is gone: the first start of this build deletes every `<data>/chats/<chat>/artifacts/` |

---

## 📚 Docs per project

| | |
| --- | --- |
| 📂 **What** | A project folder's Markdown files, read where they are (nothing is copied or stored). Hidden folders, `node_modules`, `vendor`, `dist`, `build` and the like are skipped; at most 2000 files are listed |
| 🌐 **Addresses** | A file is served at `/docs/<token>/<path>`: the token is made from a key kept in the database and is only handed to a signed-in browser. Only Markdown and pictures inside the folder are served, never code, `.env` or a hidden folder |
| 🌐 **Mermaid** | Diagrams load Mermaid from `cdn.jsdelivr.net` in the person's browser; the page kit is served at `/artifact-kit/` |
| 🚪 **Login proxy** | Behind Cloudflare Access or another login gateway, let `/docs/` and `/artifact-kit/` through (Access: an app of their own for these paths with a *Bypass* policy). A page runs in a sandboxed frame and its requests carry no cookie: blocked, the kit gets the proxy's login page and a page stays white. The token (only a signed-in browser gets it) still guards the files |

---

## 🗂️ Project notes

| | |
| --- | --- |
| 💾 **Where** | Every project folder's board (columns, cards, named colours) is one row of the database's `kv` table, `board:<folder path>`, so it is in the backup and comes back with a restore |
| 🔗 **Address** | `/#board=<folder path, URI-encoded>` opens a folder's notes; `GET`/`PUT /api/boards` read and save them (docs/design/BOARDS.md) |

---

## 🔌 MCP servers

| | |
| --- | --- |
| 🧩 **Built in** | `socrates mcp-server <name>` is what an agent starts for a built-in server (BrowserStack, Socrates Tests); it talks to the server over `<data>/run/` sockets. Off in every chat until Admin or the chat switches it on |
| 🔑 **Accounts and sign-ins** | Built-in servers' accounts and OAuth sign-ins are in the database's `kv` table, sealed with `secrets.key`, so they are in the backup |
| 📱 **BrowserStack** | A chat's screenshots in `<data>/mcp/browserstack/chats/<chat>/` (removed with the chat), the evidence a chat's tests take theirs from. Sessions open when the server stopped are listed in `<data>/run/mcp/browserstack/sessions.json` and closed at the next start. A repository describes its site in `.socrates/mcp.json`, `browserstack` (docs/design/MCP.md) |
| 🔐 **OAuth** | The authorization server sends the browser back to `<the address Admin is open at>/api/mcp/oauth/callback`; agents reach such a server through `/mcp-proxy/<name>` on Socrates' local address |

---

## 💾 Backup & restore

Set it up under **Admin → Backup**. In short:

- 📁 **What:** the whole data directory, i.e. database and key, `chats/`, `agents/`, `accounts/`, `terminals/`, `sessions/`, `vscode/`
- 🚫 **Not included:** working directories (they live in git), caches, sockets, logs, and `backup.json` itself
- 🧩 **How:** files are cut into 8 MiB chunks, and a chunk the bucket already has is never uploaded again. Databases are copied consistently while in use.
- 🔐 **Encryption:** AES-256-GCM, with keys derived from your passphrase (PBKDF2-SHA256, 600 000 rounds). Even the chunk names reveal nothing. Only `meta.json` (salt and check value) is plain text.
- 🗝️ The passphrase and the bucket keys are kept in `<data>/backup.json` (mode 0600), which is never uploaded.
- 🗑️ Chunks no snapshot needs any more are deleted a day later.

### Restore over SSH

Copy the old `backup.json` into the new data directory, then:

```bash
systemctl stop socrates
socrates restore -list
socrates restore -snapshot latest
systemctl start socrates
```

- A restore only goes into an **empty** data directory.
- The restored server keeps backing up to the same bucket as a machine of its own, so snapshots stay separate per machine.

<details>
<summary>📜 Upgrading from the old S3 mirror</summary>

An installation with `SOCRATES_S3_*` (or `s3.json`) is converted on its first start: the backup goes into the same bucket under `backup/v1/`.
Once a snapshot exists, the Backup card offers to delete the old objects. From then on the card owns the configuration,
and **Turn off backups** removes `backup.json` (the old variables don't bring it back).

</details>

---

## 🧹 Uninstall

```bash
systemctl disable --now socrates socrates-update.timer && systemctl stop "socrates-tmux*.scope"
rm -rf /etc/systemd/system/socrates.service /etc/systemd/system/socrates-update.* /usr/local/bin/socrates* \
  /usr/local/lib/socrates /opt/socrates /var/cache/socrates /etc/socrates.env /var/lib/socrates
find /usr/local/bin -lname '/opt/socrates/*' -delete && apt-mark unhold tmux
```

> ⚠️ This also deletes `/var/lib/socrates`, i.e. **all chats and settings**. Make a backup first.
