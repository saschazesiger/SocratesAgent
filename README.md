# Socrates

**A web terminal for Claude Code, Codex, OpenCode and your shell.**
Every session is a real terminal in a real tmux pane and the browser is a window
onto it, so a session outlives the tab, the phone and the network.

<p align="center">
  <img src="docs/screenshot-session.png" alt="A Claude Code session in the browser, with a session per program in the sidebar" width="860">
</p>

## Install

Docker is the only supported way to run it.

```bash
git clone https://github.com/saschazesiger/SocratesAgent.git
cd SocratesAgent

# Build Image
docker build -t socrates .

# Start in Setup Mode
docker run --name socrates -p 8080:8080 -v socrates-data:/data socrates

# Start it temporarily
docker run --name socrates -v socrates-data:/data -v /root:/root socrates

# Start in Production Mode
docker run -d --name socrates --restart unless-stopped -v socrates-data:/data -v /root:/root socrates

# Start in Production Mode, with Shell sessions on the host
docker run -d --name socrates --restart unless-stopped --pid=host --privileged -v socrates-data:/data -v /root:/root socrates

# Stop and delete
docker rm -f socrates
```

Open <http://localhost:8080>, connect a bucket (below) and choose a password.
`-v socrates-data:/data` holds what stays on the machine — the workspaces, the
`HOME` the CLIs keep their credentials and transcripts in, tmux and a working
copy of the database. `-v /root:/root` is what makes the directories you work
in exist inside the container.

## Storage: an S3 bucket is required

Socrates keeps its own state in an S3 bucket, and it will not start without
one: until a bucket is connected it serves the connect page and nothing else
(`/api/health` still answers, so a container health check passes). The data
directory keeps a working copy, so a page never waits on the bucket; every
change goes up within about five seconds, and a new container on an empty
volume comes back from the bucket with everything below.

| in the bucket (encrypted) | stays in the data directory |
| --- | --- |
| settings, the password hash, sign-ins (login cookies), profiles and their order, push keys and subscriptions, unread state, every session row, every prepared session's prompt | the CLIs' own logins, config and transcripts (`HOME`), workspaces, tmux, journals, `plan.json`, VS Code, and the bootstrap file `s3.json` |

Any S3-compatible store works: Hetzner Object Storage, AWS S3, Cloudflare R2,
MinIO. Connect it on the setup page (step 1, before the password), or give the
container the configuration in its environment — then the environment wins
and the admin page shows it read-only:

```bash
docker run -d --name socrates --restart unless-stopped -v socrates-data:/data -v /root:/root \
  -e SOCRATES_S3_ENDPOINT=https://hel1.your-objectstorage.com \
  -e SOCRATES_S3_REGION=hel1 \
  -e SOCRATES_S3_BUCKET=my-bucket \
  -e SOCRATES_S3_PREFIX= \
  -e SOCRATES_S3_ACCESS_KEY_ID=… \
  -e SOCRATES_S3_SECRET_ACCESS_KEY=… \
  -e SOCRATES_S3_PATH_STYLE=true \
  -e SOCRATES_S3_PASSPHRASE='a long passphrase' \
  socrates
```

`SOCRATES_S3_BUCKET` switches the environment on; without it the others are
ignored. The region defaults to `us-east-1`, path-style addressing to on, and
the prefix to the bucket's root (a non-empty prefix gets a trailing `/`). The
endpoint is empty for AWS. A setup-page configuration is written to
`<data>/s3.json` (mode 0600). Admin → Storage tests the connection, syncs now,
and changes the endpoint, region, keys or addressing style for the same bucket
and prefix.

**Everything in the bucket is encrypted on this machine** (AES-256-GCM, with a
key derived from the passphrase by PBKDF2-SHA256, 600 000 rounds) before it is
sent; only `meta.json` (the salt and a check value) and `lease.json` (which
host holds the bucket) are plain. The passphrase never goes to the bucket.
**If you lose it, the bucket's contents cannot be read by anyone, you
included** — write it down somewhere other than the server. It is stored in
`s3.json` or the container's environment so the server can start by itself, so
treat the volume and the environment like the bucket keys they sit beside.

**One Socrates per bucket and prefix.** The running one holds a lease
(`lease.json`, renewed every 30 s, free after 90 s without a renewal and at
once on a clean stop). A second container on the same bucket shows who holds it
and offers **Take over** with the Socrates password; the first one then stops
uploading and says so in a banner. If the bucket cannot be reached at start,
a data directory that was synchronised with exactly that bucket before starts
from its copy and catches up later; any other one waits on the connect page and
retries. A configured server never lets the connect page point it at a
different bucket.

Upgrading an existing installation: start it with a bucket configured. A
bucket with no Socrates state in it yet takes this installation's database as
it is, password included. Give the configuration **before** the new image
starts — `/data/s3.json` written into the volume, or the `SOCRATES_S3_*`
variables (for `scripts/redeploy-container.sh`, through
`SOCRATES_RUN_ARGS`, e.g. `--env-file /root/socrates-s3.env`, a file kept
outside the repository). Without it the container comes up on the connect page
with the tunnel off; the redeploy script refuses the swap until one of the two
is there. Connecting an existing installation from the setup page asks for its
Socrates password, because connecting uploads its sign-ins and keys to the
bucket you name, or replaces its state with that bucket's.

The upgrade migrates the database, and a database an upgrade migrated is
refused by the build before it. The previous file is kept beside it as
`socrates.db.pre-v8.bak`; to go back, stop the container, copy that file over
`socrates.db` (removing `socrates.db-wal` and `socrates.db-shm`), and start the
old image:

```bash
docker stop socrates
docker run --rm -v socrates-data:/data alpine sh -c \
  'cd /data && rm -f socrates.db-wal socrates.db-shm && cp socrates.db.pre-v8.bak socrates.db'
```

`redeploy-container.sh` does this by itself when its rollback runs.

If `meta.json` disappears from a bucket that still holds Socrates objects, no
new installation is created over them (a new one would have a new salt, and
the objects could never be opened again): a machine that was synchronised with
that place puts its copy of `meta.json` back; any other refuses to start until
it is restored, or the prefix is emptied or deliberately overwritten.

## Sign the CLIs in

```bash
docker exec -it socrates claude      # then codex, then opencode
```

Answer the first-run questions too, Claude Code's text-style prompt included: a
session started before it is answered opens on that instead of on a prompt. They
stay signed in across `docker rm`, because `HOME` is in the volume.

### More than one account

A profile is a second set of sign-ins - a business Claude account beside a
private one. Add it under **Profiles** in the dashboard, then pick it in the
new-session sheet. Each profile gets its own `CLAUDE_CONFIG_DIR`, `CODEX_HOME`
and OpenCode data directory under `<data>/profiles/<id>`; settings,
instructions, skills, plugins and MCP servers are linked from the default ones,
while credentials and conversations stay apart. To sign in, start a Claude Code
session in the profile (it asks by itself), or a container Shell in it and run
`codex login --device-auth` or `opencode auth login`.

The sheet lists the profiles in the order of the dashboard's list (drag a row
by its handle to change it), shows each account's five-hour and weekly usage
with how long until each renews and, unless you pick another, chooses the one
with the most usage left for the time until it renews. A session keeps the
profile it was started in for its whole life.

A Claude Code session can also be started with **Ultracode** (dynamic
workflows on every task) from the new-session sheet. It is off unless switched
on there, and a session keeps the choice for its whole life.

## A shell on the host

Panes run inside the container, because the tmux server does. When creating a
Shell session, choose **Host** or **Container**. Host redirects to the host's
login shell and credentials; Container runs the shell and installed tools inside
Socrates, for example to update an agent CLI. Host is offered when Socrates was
started with `--pid=host` and the right to enter namespaces. It remains the
default when available, and the choice is kept when the session restarts.

The working directory carries over when it is a bind mount of the same name on
both sides, and falls back to `/root` when it is not. Shell only — the three
agents are handed generated files under `/data`, which is the volume and not a
path the host has. A binary path in Admin → Harnesses → Shell is used for
Container sessions; an explicit Host choice uses the image's host wrapper.

## Agents on the host

The agents stay in the container, and reach the host one command at a time
with `on-host`:

```bash
on-host kubectl get pods -A                  # a command on the host, as root
on-host -c 'journalctl -u k3s | tail -50'    # a script for the host's shell
```

It is the same jump as the host shell, under the same condition — `--pid=host`
and the right to enter namespaces — and when that holds, every Claude Code,
Codex and OpenCode session is told about it in its instructions. That is how an
agent can drive the host's Kubernetes, its services and its files, with no
approvals in between. Without the flags nothing is said and nothing changes.

## Keys

Both optional, both in Admin: **OpenRouter** for dictation, **Google Cloud** for
reading answers out loud.

## Notifications

The bell in the top bar turns them on for this device. Over HTTPS (the
Cloudflare tunnel, or `localhost`) they are Web Push: Socrates sends one when a
session finishes or needs an answer, and it arrives with the page closed and
the phone locked. On an iPhone, add Socrates to the Home Screen and turn the
bell on from there; Safari allows push only for an installed web app. Over
plain http they appear only while the page is open.

## Prepared sessions (preStart)

Switch **preStart** on in the new-session sheet (Claude Code, Codex and
OpenCode) and **Prepare** stores the session without starting anything. It
opens as a panel instead of a terminal: the same settings as the sheet, all
still changeable, and a large field for the first prompt, which is saved as you
type (on this device at once, on the server after a pause, then in the bucket —
the line under the field says which). **Start** (or Ctrl/⌘+Enter) launches it
exactly as a new session with those settings, and the prompt goes in as the
program's first message. **Start automatically** takes a date and time instead:
the server starts it then by itself, even with no browser open, and sends a
notification either way. A server that was down at that moment starts it as
soon as it is back; a start that failed is not retried, and the panel says
why. Prepared sessions are listed in their own group at the top of the sidebar.

## Files

The paperclip in the top bar uploads files into the session's working
directory, under `.socrates/uploads/` (ignored by git), and puts their paths on
the prompt. Claude Code attaches a pasted image path as an image. Pasting or
dropping an image onto the terminal does the same. The two buttons beside it
copy (the selection, or the terminal's text in a sheet you can select in) and
paste.

## Security

One password in front of coding CLIs that run **unattended**, with their
approvals and sandboxes bypassed and no way to turn that off — access to the web
interface is access to everything you mounted. `-p 127.0.0.1:8080:8080` keeps it
off the network; publish it through the Cloudflare tunnel in Admin, with
Cloudflare Access in front of the hostname. With `--pid=host --privileged` there
is no container boundary left at all: that one is root on the machine, and not
something to publish — and every agent is told how to use it.

## Development

```bash
make check   # gofmt, go vet, go mod tidy, go test -race, go build
make e2e     # the browser suite: node, a Chromium and tmux >= 3.3
```

The front end has no build step — edit `internal/web/static` and rebuild. Design
notes are in [docs/design](docs/design). `SOCRATES_ADDR`, `SOCRATES_DATA_DIR`,
`HOME` and the `SOCRATES_S3_*` variables are the environment; `INSTALL_AGENTS`, `INSTALL_CLOUDFLARED`,
`INSTALL_VSCODE` and `VERSION` are the build arguments;
`scripts/migrate-to-docker.sh` seeds the volume from a local installation.

## License

MIT. See [LICENSE](LICENSE). tmux and the coding CLIs are separate programs
Socrates starts, each under its own terms, named in
[THIRD_PARTY_LICENSES.md](THIRD_PARTY_LICENSES.md).
