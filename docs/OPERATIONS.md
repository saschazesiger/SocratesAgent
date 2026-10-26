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

---

## 🗺️ What is where

| | |
| --- | --- |
| `socrates.service` | The web server: `systemctl status socrates`, `journalctl -u socrates -f` |
| `socrates-agents-<chat>.scope` | One chat's agent, which outlives the service |
| `socrates-tmux.scope` | The Shell terminals |
| `socrates-update.timer` | Nightly update: `journalctl -u socrates-update` |
| `/etc/socrates.env` | Configuration (see above) |
| `/var/lib/socrates` | **All data**: database, chats, accounts, transcripts, terminals, backup settings |
| `/usr/local/bin/socrates` | The binary |
| `/usr/local/lib/socrates` | The updater |
| `/opt/socrates` | Source, Go, Node, OpenCode, code-server |
| `/root` | `HOME`: the agents' folders link into the accounts |

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
