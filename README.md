# Socrates

**A web terminal for Claude Code, Codex, OpenCode and your shell.**
Every session is a real terminal in a real tmux pane and the browser is a window
onto it, so a session outlives the tab, the phone, the network and an update.

<p align="center">
  <img src="docs/screenshot-session.png" alt="A Claude Code session in the browser, with a session per program in the sidebar" width="860">
</p>

## Install

On Debian or Ubuntu with systemd (x86_64 or arm64), as root:

```bash
git clone https://github.com/saschazesiger/SocratesAgent.git
cd SocratesAgent
sudo scripts/install.sh
```

or, without a checkout,
`curl -fsSL https://raw.githubusercontent.com/saschazesiger/SocratesAgent/main/scripts/install.sh | sudo bash`.

The script installs everything Socrates runs on and builds Socrates from the
repository's newest commit:

| | from | where |
| --- | --- | --- |
| git, ripgrep, jq, curl, ncurses-term... | the distribution | apt |
| tmux (3.3 or newer) | the distribution, held | apt |
| Go, to build Socrates | go.dev, checksum verified | `/opt/socrates/go/<version>` |
| Node, for the sessions | nodejs.org, checksum verified | `/opt/socrates/node/<version>`, linked into `/usr/local/bin` |
| Claude Code | its native installer | `~/.local/bin/claude` |
| Codex | its standalone installer | `~/.local/bin/codex` |
| OpenCode | its release binary | `/opt/socrates/opencode/<version>`, linked as `~/.local/bin/opencode` |
| code-server | its release tarball | `/opt/socrates/code-server/<version>`, linked into `/usr/local/bin` |

Then it starts Socrates as a systemd service and switches on
`socrates-update.timer`, which keeps all of it up to date (below). Running the
script again is safe: it repairs what is missing and updates the rest. Then:

1. **Open it.** Socrates listens on `127.0.0.1:8080`. From your computer:
   `ssh -L 8080:localhost:8080 root@your-server`, then <http://localhost:8080>.
   Choose a password - or restore a backup of another machine (below).
2. **Sign the agents in**, once, in a terminal on the machine or in a Shell
   session: `claude`, `codex login --device-auth`, `opencode auth login`.
   Answer Claude Code's first-run questions too, or a new session opens on them.
3. **Reach it from anywhere**: Admin → Cloudflare tunnel, with Cloudflare Access
   in front of the hostname.

## Updates

**Everything updates itself.** `socrates-update.timer` runs the updater every
night between 3 and 5 and ten minutes after every boot: Socrates from the
newest commit, Go, Node, Claude Code, Codex, OpenCode, code-server,
cloudflared and the packages above. The boot run is also what puts back a tool
that went missing. Admin → Updates shows the last run, every tool's version,
the log, and has **Update now**; the setup check warns when the last run
failed or is more than two days old.

By hand:

```bash
sudo /usr/local/lib/socrates/update.sh              # everything, now
sudo /usr/local/lib/socrates/update.sh --deps       # only the tools
sudo /usr/local/lib/socrates/update.sh --socrates   # only Socrates
sudo /usr/local/lib/socrates/update.sh --repair     # put back what is missing, update nothing
```

`SOCRATES_AUTO_UPDATE` in `/etc/socrates.env` says what the timer does: `all`
(the default), `deps` (the tools but not Socrates) or `off` (only repairs).
`SOCRATES_NODE_MAJOR`, `SOCRATES_CODE_SERVER_VERSION` and
`SOCRATES_CLAUDE_CHANNEL` pin those; `SOCRATES_REPO` and `SOCRATES_BRANCH` are
where Socrates is built from.

**Sessions keep running through an update.** They live in their own systemd
scope, not in the service, so a new Socrates takes only the web server down for
a moment and the page reconnects by itself. A tool is never changed under a
running session: every version is unpacked into a directory of its own and a
link is switched, and an old version is only deleted once nothing runs from it.
The update itself runs as a systemd unit of its own, so it finishes even when
it was started from inside a Socrates session that loses its terminal.

**An update that does not work is undone.** A new Socrates is built from the
source in `/opt/socrates/src`, started once on a scratch directory, and only
then swapped in. If it does not answer `/api/health` with its own version - or
keeps restarting - the previous binary and database are put back, and that
commit is not tried again until a newer one arrives. A commit that does not
build never touches the service. One tool that cannot be downloaded is
reported and the others go on; nothing is changed without network.

**A hung Socrates is restarted.** The service has a systemd watchdog: it is fed
only while Socrates answers its own health check, so a process that hangs is
restarted like one that crashed, and a crash loop never parks the unit in
`failed`.

A reboot does end the sessions; each comes back on its own conversation when
you open it.

## What is where

| | |
| --- | --- |
| `socrates.service` | the web server: `systemctl status socrates`, `journalctl -u socrates -f` |
| `socrates-tmux.scope` | the sessions. `systemctl stop "socrates-tmux*.scope"` ends every one of them |
| `socrates-update.timer` | the nightly update: `systemctl list-timers socrates-update.timer`, `journalctl -u socrates-update` |
| `/etc/socrates.env` | the listen address and what is updated |
| `/var/lib/socrates` | the data directory: the database, profiles, journals, `backup.json`, `updates.json` and `updates.log` of the last update |
| `/root` | `HOME`: the agents' own logins, settings and transcripts, Claude Code and Codex |
| `/usr/local/bin/socrates` | the binary |
| `/usr/local/lib/socrates` | the updater the timer runs |
| `/opt/socrates` | the source Socrates is built from, and Go, Node, OpenCode and code-server |

tmux is held at its installed version (`apt-mark hold tmux`), because a tmux
upgraded under a running server can no longer talk to it. The updater installs
a newer tmux by itself at a moment no tmux server runs; the admin page says
when one is waiting. To have it now: `systemctl stop "socrates-tmux*.scope"`
and run the updater - the sessions resume when opened.

To remove Socrates:

```bash
systemctl disable --now socrates socrates-update.timer && systemctl stop "socrates-tmux*.scope"
rm -rf /etc/systemd/system/socrates.service /etc/systemd/system/socrates-update.* /usr/local/bin/socrates* \
  /usr/local/lib/socrates /opt/socrates /var/cache/socrates /etc/socrates.env /var/lib/socrates
find /usr/local/bin -lname '/opt/socrates/*' -delete && apt-mark unhold tmux
```

## Storage: one folder, backed up to S3

Everything Socrates keeps is in its data directory, `/var/lib/socrates`
(`SOCRATES_DATA_DIR`): the database with the settings, the password, sign-ins,
sessions and prepared prompts, and beside it the sessions' files, profiles and
VS Code's state. It needs nothing else to run, and it never waits on a network.

**Admin → Backup** backs that folder up to an S3 bucket - Hetzner Object
Storage, AWS S3, Cloudflare R2, MinIO, anything S3-compatible. Every 10
minutes (5 at the least) while anything changed, once more when Socrates
stops, and on **Back up now**. Each backup is a snapshot of the whole folder,
but only what changed is uploaded: files are cut into 8 MiB chunks, and a
chunk the bucket already holds is never sent again, so a journal that grew
costs its last chunk. Every SQLite database in the folder - Socrates' own and
the agents' - is copied consistently while it is in use, never read half
written. Snapshots are kept for 48 hours, then one a day for 30 days and one
a week for 26 weeks; chunks no snapshot needs any more are deleted a day later.
The card says when the last backup ran, how large it was and what failed; a
day without a successful one is said on the session page too.

| backed up | left out |
| --- | --- |
| the database, `sessions/`, `profiles/`, `vscode/`, `vscode-extensions/`, and whatever later versions keep there (chats, agents, accounts) | working directories (the workspace root and the presets, which live in git), `run/`, `cache/`, `bin/`, sockets, the terminals' byte journals, the updater's files, and `backup.json` itself |

**Everything in the bucket is encrypted on this machine** (AES-256-GCM, with
keys derived from your passphrase by PBKDF2-SHA256, 600 000 rounds) before it
is sent; even the chunks' names say nothing about their content. Only
`meta.json` (the salt and a check value) is plain. The passphrase never goes
to the bucket. **If you lose it, nobody can read the backup, you included** -
write it down somewhere other than the server. It is in `<data>/backup.json`
(mode 0600) with the bucket's keys, which is why that file is not backed up.

**Moving to a new machine:** install Socrates there and open its setup page,
choose **Restore from a backup**, enter the bucket and the passphrase, pick a
snapshot, and sign in with the old password once it is back. Over SSH instead:
put the old `backup.json` into the new data directory, then

```bash
systemctl stop socrates
socrates restore -list
socrates restore -snapshot latest
systemctl start socrates
```

A restore only goes into a data directory that holds no installation yet.
The restored machine keeps backing up into the same bucket, as a machine of
its own: snapshots are kept apart by machine, and the card says when another
machine wrote one in the last day.

An installation that had the bucket mirror of earlier versions (`s3.json`, or
`SOCRATES_S3_*` in `/etc/socrates.env`) is carried over by itself: the backup
goes into the same bucket under `backup/v1/` inside the old prefix, and the
mirror's objects are left as they were.

## More than one account

A profile is a second set of sign-ins - a business Claude account beside a
private one. Add it under **Profiles** in the dashboard, then pick it in the
new-session sheet. Each profile gets its own `CLAUDE_CONFIG_DIR`, `CODEX_HOME`
and OpenCode data directory under `<data>/profiles/<id>`; settings,
instructions, skills, plugins and MCP servers are linked from the default ones,
while credentials and conversations stay apart. To sign in, start a Claude Code
session in the profile (it asks by itself), or a Shell session in it and run
`codex login --device-auth` or `opencode auth login`.

The sheet lists the profiles in the order of the dashboard's list (drag a row
by its handle to change it), shows each account's five-hour and weekly usage
with how long until each renews and, unless you pick another, chooses the one
with the most usage left for the time until it renews. A session keeps the
profile it was started in for its whole life.

A Claude Code session can also be started with **Ultracode** (dynamic
workflows on every task) from the new-session sheet. It is off unless switched
on there, and a session keeps the choice for its whole life.

## Keys

Both optional, both in Admin: **OpenRouter** for dictation, **Google Cloud** for
reading answers out loud.

With an OpenRouter key, **Check each turn's outcome** (Admin → Status & naming,
off by default) has every finished turn judged as complete, partly done (with
what is still open), waiting on you, or blocked. The session shows an hourglass
while that is read, and its notification waits up to 45 seconds to say it. For
Claude Code the read is also given the last instruction, the last answer and
the task list from its transcript.

## Notifications

**Notifications** in the options menu (the `⋮` at the right of the top bar)
turns them on for this device. Over HTTPS (the
Cloudflare tunnel, or `localhost`) they are Web Push: Socrates sends one when a
session finishes or needs an answer, and it arrives with the page closed and
the phone locked. On an iPhone, add Socrates to the Home Screen and turn
them on from there; Safari allows push only for an installed web app. Over
plain http they appear only while the page is open.

A question notification shows the question itself when it can be read in
time, and tapping it opens Home with that question ready to answer. The app
icon's badge is the number of sessions waiting for an answer (questions and
permission prompts), and a notification for a question that has been answered
elsewhere is removed. On Android, a long press on the installed app's icon
offers **Questions** and **Home**.

## Prepared sessions (PreStart)

Switch **PreStart** on in the new-session sheet (Claude Code, Codex and
OpenCode) and **Prepare** stores the session without starting anything. It
opens as a panel instead of a terminal: the same settings as the sheet, all
still changeable, and a large field for the first prompt, which is saved as you
type (on this device at once, on the server after a pause - the line under
the field says which). **Start** (or Ctrl/⌘+Enter) launches it
exactly as a new session with those settings, and the prompt goes in as the
program's first message. **Start automatically** takes a date and time instead:
the server starts it then by itself, even with no browser open, and sends a
notification either way. A server that was down at that moment starts it as
soon as it is back; a start that failed is not retried, and the panel says
why. A prepared session is listed in its folder, under **PreStart** above the
sessions that are running there. The first time you leave the prompt field
after writing in it, the session is named after the prompt.

## Files

The paperclip in the top bar uploads files into the session's working
directory, under `.socrates/uploads/` (ignored by git), and puts their paths on
the prompt. Claude Code attaches a pasted image path as an image. Pasting or
dropping an image onto the terminal does the same. The two buttons beside it
copy (the selection, or the terminal's text in a sheet you can select in) and
paste.

## Security

One password in front of coding CLIs that run **unattended**, as root on this
machine, with their approvals and sandboxes bypassed and no way to turn that
off. Access to the web interface is root on the machine. It therefore listens
on `127.0.0.1` only; publish it through the Cloudflare tunnel with Cloudflare
Access in front of the hostname, never by setting `SOCRATES_ADDR=:8080` on a
network you do not trust.

## Development

```bash
make check   # gofmt, go vet, go mod tidy, go test -race, go build
make e2e     # the browser suite: node, a Chromium and tmux >= 3.3
```

The front end has no build step — edit `internal/web/static`, then
`sudo scripts/update.sh --from=.` to run the working tree as it is (with the
same smoke test and rollback as an update; set `SOCRATES_AUTO_UPDATE=deps` to
keep the nightly update from replacing it with the repository's newest commit). Design notes are
in [docs/design](docs/design). The environment is `SOCRATES_ADDR` (default
`127.0.0.1:8080`), `SOCRATES_DATA_DIR` (default `/var/lib/socrates`) and
`HOME`.

## License

MIT. See [LICENSE](LICENSE). tmux and the coding CLIs are separate programs
Socrates starts, each under its own terms, named in
[THIRD_PARTY_LICENSES.md](THIRD_PARTY_LICENSES.md).
