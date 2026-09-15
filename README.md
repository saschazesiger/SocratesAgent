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

## A shell on the host

Panes run inside the container, because the tmux server does. Started with
`--pid=host` and the right to enter namespaces, a Shell session runs on the host
machine instead — as the default, with nothing to set: the image ships the
wrapper, and Socrates names it only when the jump actually works, so a container
started without those flags keeps the shell it had.

The working directory carries over when it is a bind mount of the same name on
both sides, and falls back to `/root` when it is not. Shell only — the three
agents are handed generated files under `/data`, which is the volume and not a
path the host has. A binary path in Admin → Harnesses → Shell overrides all of
it.

## Keys

Both optional, both in Admin: **OpenRouter** for dictation, **Google Cloud** for
reading answers out loud.

## Security

One password in front of coding CLIs that run **unattended**, with their
approvals and sandboxes bypassed and no way to turn that off — access to the web
interface is access to everything you mounted. `-p 127.0.0.1:8080:8080` keeps it
off the network; publish it through the Cloudflare tunnel in Admin, with
Cloudflare Access in front of the hostname. With `--pid=host --privileged` there
is no container boundary left at all: that one is root on the machine, and not
something to publish.

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
