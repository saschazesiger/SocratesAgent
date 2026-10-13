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

Open <http://localhost:8080> and choose a password. `-v socrates-data:/data` is
everything Socrates remembers — the database, the workspaces and the `HOME` the
CLIs keep their credentials in. `-v /root:/root` is what makes the directories
you work in exist inside the container.

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
notes are in [docs/design](docs/design). `SOCRATES_ADDR`, `SOCRATES_DATA_DIR` and
`HOME` are the environment; `INSTALL_AGENTS`, `INSTALL_CLOUDFLARED`,
`INSTALL_VSCODE` and `VERSION` are the build arguments;
`scripts/migrate-to-docker.sh` seeds the volume from a local installation.

## License

MIT. See [LICENSE](LICENSE). tmux and the coding CLIs are separate programs
Socrates starts, each under its own terms, named in
[THIRD_PARTY_LICENSES.md](THIRD_PARTY_LICENSES.md).
