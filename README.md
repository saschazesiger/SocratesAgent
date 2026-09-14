# Socrates

**A web terminal for Claude Code, Codex, OpenCode and your shell.**
Every session is a real terminal in a real tmux pane and the browser is a window
onto it, so a session outlives the tab, the phone and the network.

<p align="center">
  <img src="docs/screenshot-session.png" alt="A Claude Code session in the browser, with a session per program in the sidebar" width="860">
</p>

Socrates runs in Docker. That is the only supported way to run it.

## Install

```bash
git clone https://github.com/saschazesiger/SocratesAgent.git
cd SocratesAgent
docker build -t socrates .
```

Then start it. This is the command:

```bash
docker run -d --name socrates --restart unless-stopped \
  -p 8080:8080 -v socrates-data:/data -v /root:/root socrates
```

Open <http://localhost:8080> and choose a password.

- `-d --restart unless-stopped` keeps it in the background and brings it back
  after a crash and after a reboot — provided the Docker daemon itself starts at
  boot, which is `systemctl enable docker`. After `docker stop socrates` it
  stays down until you `docker start socrates`; that is the difference from
  `--restart always`.
- `-v socrates-data:/data` is everything Socrates remembers: the database, the
  workspaces, and the `HOME` the three CLIs keep their credentials in.
- `-v /root:/root` is what makes the directories you work in exist inside the
  container. It is the wide answer — every agent here runs without permission
  prompts, so it can write anywhere in that mount. One `-v` per project is the
  narrow one.

`deploy/docker-compose.yml` is the same container written down rather than
remembered: `docker compose -f deploy/docker-compose.yml up -d`.

## Sign the CLIs in

Socrates starts `claude`, `codex` and `opencode`; it does not log them in. Once,
in a shell on the running container:

```bash
docker exec -it socrates claude      # then codex, then opencode
```

Answer the first-run questions too — Claude Code's text-style choice is a
full-screen prompt, and a session started before it is answered opens on that
instead of on a prompt. They stay signed in across `docker rm`, because `HOME`
is inside the volume.

### Coming from a local installation

Seed the volume from it once — password, keys, tunnel, presets, model lists and
the CLI credentials:

```bash
scripts/migrate-to-docker.sh
```

Presets pointing at host directories need a bind mount to work; the script names
them. The tunnel comes over enabled, so stop the old instance first, or turn the
tunnel off in Admin → Remote access before you start the container.

## Everyday commands

```bash
docker logs -f socrates              # the log
docker exec -it socrates bash        # a shell inside it
docker restart socrates              # panes die, sessions resume
docker stop socrates                 # and docker start socrates
```

After pulling changes, rebuild and recreate — a new image does not reach a
running container:

```bash
docker build -t socrates .
docker rm -f socrates
docker run -d --name socrates --restart unless-stopped \
  -p 8080:8080 -v socrates-data:/data -v /root:/root socrates
```

A container restart is a reboot for the terminal: the tmux server dies with the
container, the panes are gone, and every session comes back through the resume
path with its conversation and its working directory intact.

## Keys

Both are optional, both go in Admin, and neither belongs in the image:

- **OpenRouter** — <https://openrouter.ai/keys>. Only dictation needs it.
- **Google Cloud** — only for reading answers out loud. Standard voices are free
  for the first four million characters a month.

## Build arguments

```bash
docker build -t socrates --build-arg INSTALL_AGENTS=0 .
```

`INSTALL_AGENTS`, `INSTALL_CLOUDFLARED` and `INSTALL_VSCODE` default to `1`, and
`VERSION` — what `socrates -version` prints — to `docker`. tmux is not a build
argument: without it the image would have nothing to run.

## Configuration

| Environment | In the image | Meaning |
| --- | --- | --- |
| `SOCRATES_ADDR` | `:8080` | listen address inside the container |
| `SOCRATES_DATA_DIR` | `/data` | database, tmux socket, journals, workspaces |
| `HOME` | `/data/home` | where the CLIs keep credentials and transcripts |
| `OPENROUTER_API_KEY` | | seeds the key on first start |
| `GOOGLE_TTS_API_KEY` | | seeds the voice key on first start |

Everything else lives in the dashboard.

## Security

Socrates is built for a single trusted operator and it runs the coding CLIs
**unattended**. That is the point of it — nobody can tap "allow" from a car —
and it is the thing to understand before you publish it.

- All three CLIs start with their approvals and sandboxes bypassed, and that is
  not a setting you can turn off. Nothing stands between the program and
  whatever you mounted into the container. The exact flags are in
  [docs/design/HARNESS-POLICY.md](docs/design/HARNESS-POLICY.md).
- Access to the web interface is access to all of that. One password, hashed
  with PBKDF2-HMAC-SHA256 (210 000 rounds), an `HttpOnly`/`SameSite=Lax` cookie
  that becomes `Secure` over HTTPS, and logins rate-limited to a ten-minute
  delay after five failures.
- The port is published on every interface. `-p 127.0.0.1:8080:8080` keeps it on
  the machine; publish it through the Cloudflare tunnel in Admin instead, with
  Cloudflare Access in front of the hostname.

## Development

```bash
make check   # what CI runs: gofmt, go vet, go mod tidy, go test -race, go build
make e2e     # the browser suite: node, a Chromium and tmux >= 3.3
```

The Go tests use real tmux on a private socket of their own and never touch
yours. The front end has no build step — edit `internal/web/static` and rebuild.

```
main.go                       flags, startup, shutdown, export/import, internal subcommands
internal/termux               the tmux substrate: sessions, viewers, sizing, journal, adoption
internal/harnesses            the five programs: command lines, config files, id discovery
internal/catalog              which programs are installed and which models they offer
internal/store                SQLite persistence (sessions, settings, logins)
internal/config               the settings document and its defaults
internal/server               HTTP API, auth, the WebSocket, admin, voice, tunnel
internal/openrouter           transcription and the model catalogue
internal/googletts            a small client for Google Cloud Text-to-Speech
internal/tunnel               supervised Cloudflare tunnel and its installer
internal/web/static           the whole front end: plain HTML, CSS and JS
e2e                           Playwright scenarios against a real server and real tmux
deploy/docker-compose.yml     the container as a service
deploy/docker-entrypoint.sh   the image entrypoint: creates the HOME kept in the volume
scripts/migrate-to-docker.sh  seed the volume from a local installation
```

Design notes are in [docs/design](docs/design).

## License

MIT. See [LICENSE](LICENSE).

tmux and the coding CLIs are separate programs Socrates starts, each under its
own terms. They are named in
[THIRD_PARTY_LICENSES.md](THIRD_PARTY_LICENSES.md), which matters to anyone
publishing the Docker image.
