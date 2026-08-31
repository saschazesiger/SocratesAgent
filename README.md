# Socrates

**A top level agent for Claude Code, Codex and OpenCode.**
One Go binary with a ChatGPT style web interface, a live view of what the coding
agents are doing, and a hands free voice mode. It runs entirely on
[OpenRouter](https://openrouter.ai) and does the actual work at a real terminal
on your machine — which is also how it drives the agent CLIs you already have.

<p align="center">
  <img src="docs/screenshot-chat.png" alt="Socrates chat with the live process view" width="900">
</p>

---

## Why

Claude Code, Codex and OpenCode are excellent at doing the work. They are less
good at deciding *which* of them should do it, and they live in a terminal.

Socrates sits one level above them: you talk to it, it plans, and then it sits
down at a terminal and works. It starts the right agent the way you would, types
the brief into it, reads the screen, answers what the agent asks, and waits for
it to finish. In between it runs ordinary commands — `git`, a build, a test
suite — because it has a shell, not a fixed list of things it can do. It asks you
when a decision is genuinely yours, and it answers you in one place: by text or
by voice.

## What you get

- **A chat that feels familiar.** Sidebar with past conversations, streaming
  answers, markdown, mobile friendly. Light, quiet, minimal.
- **A real terminal, not a wrapper.** Socrates opens interactive sessions and
  drives them like a person: typing, reading the screen, pressing keys. That is
  how it runs Claude Code, and equally how it runs anything else.
- **A live process view you can take over.** Every session is streamed to the
  browser as the screen it really is — and there is an input box, so you can
  type into the same program Socrates is talking to, at any moment.
- **Sessions that survive a restart.** Each one runs in its own small host
  process, so restarting Socrates does not interrupt an agent mid task; it
  reconnects to what was already running.
- **It asks you back.** When something is ambiguous the agent offers two to four
  options as buttons instead of guessing.
- **Voice in and out.** Record in the browser, transcribe through OpenRouter,
  have the answer read back to you.
- **Auto mode.** One big microphone button, a timer, and the answer shown as
  large as it fits and read out loud. Options are spoken and can be answered by
  voice or by tapping.
- **Reachable from anywhere, without opening a port.** A managed Cloudflare
  tunnel publishes the local server on the internet — a throwaway
  `trycloudflare.com` address in one click, or your own hostname with a tunnel
  token. `cloudflared` is downloaded automatically if you do not have it. Start,
  stop and watch it from the dashboard.
- **An admin dashboard for everything.** API key, a searchable picker over the
  live OpenRouter catalogue, the programs Socrates may run and how to drive
  them, prompts, voice, remote access, password, and a setup check.
- **Single binary.** Go plus embedded HTML/CSS/JS, SQLite for state, no build
  step, no CDN, no telemetry.

<p align="center">
  <img src="docs/screenshot-auto.png" alt="Auto mode" width="440">
  <img src="docs/screenshot-question.png" alt="The agent asking a question in auto mode" width="440">
</p>

## How it works

```
   browser on this machine          browser anywhere
        │                                │
        │ http://localhost:8080          │ https://your-hostname
        │                                ▼
        │                         Cloudflare edge
        │                                │
        │                                ▼
        │                      cloudflared (child process)
        ▼                                │
  ┌─────────────────────────────────────────────────────┐
  │  socrates (single Go binary)                        │
  │                                                     │
  │    web UI  ·  JSON API  ·  SSE  ·  SQLite state     │
  │                        │                            │
  │                orchestration loop                   │
  │                 │              │                    │
  └─────────────────┼──────────────┼────────────────────┘
                    │              │
          OpenRouter│              │unix socket
       plan, answer,│              │
         transcribe ▼              ▼
                            socrates term-host   (one per session,
                                    │             detached, survives
                                    ▼             a restart)
                            pseudo terminal
                                    │
                                    ▼
                            claude · codex · opencode · bash · anything
```

The orchestrator has one capability: a terminal. `shell_run` for a command that
just needs running, and `terminal_open` / `terminal_send` / `terminal_wait` /
`terminal_read` / `terminal_close` for anything it has to hold a conversation
with. Plus `ask_user`, for when the decision is yours.

Claude Code, Codex and OpenCode are not special cases in the code. They are
entries in a list, each one saying which command to start and — in plain
English — how to drive it. Adding a fourth is configuration, not a patch.

Every session runs behind a real pseudo terminal, so the agent CLIs show their
full interactive interface instead of dropping into a headless mode, and
Socrates reads the rendered screen exactly as a person would see it.

## Requirements

- **Go 1.24+** — only to build the binary.
- **An OpenRouter API key** — <https://openrouter.ai/keys>.
- **A Unix-like system** for the full experience — macOS, Linux, WSL. Socrates
  builds and runs on Windows, but without a pseudo terminal there, full screen
  CLIs fall back to their non interactive behaviour.
- **At least one agent CLI** in your `PATH`, signed in:
  [`claude`](https://claude.com/claude-code),
  [`codex`](https://github.com/openai/codex),
  [`opencode`](https://opencode.ai).
- **`cloudflared`** — not required: if you turn on remote access and it is
  missing, Socrates downloads it for you. See [Remote access](#remote-access).

## Install

```bash
git clone https://github.com/saschazesiger/SocratesAgent.git
cd SocratesAgent
make build
./socrates
```

Or without cloning:

```bash
go install github.com/saschazesiger/SocratesAgent@latest
SocratesAgent            # the binary takes the name of the repository
```

Or with Docker (the image also installs the three agent CLIs):

```bash
docker build -t socrates .
docker run -p 8080:8080 -v socrates-data:/data socrates
```

Then open <http://localhost:8080>.

## First run

1. `/setup` asks you for the password you will use from now on. You can paste
   your OpenRouter key right away, and decide whether the instance should be
   published through a Cloudflare tunnel — both can also be changed later.
2. You land in the admin dashboard. Check the tools, press **Run checks** — it
   verifies your key, the workspace directory, the terminal and every enabled
   tool.
3. Go back to the chat and ask for something.

<p align="center">
  <img src="docs/screenshot-admin.png" alt="Admin dashboard" width="900">
</p>

## Configuring the tools

Each entry in the dashboard answers two questions: *when should Socrates use
you?* and *how do I drive you?* Both texts go into the model's instructions
verbatim, so write them the way you would brief a colleague on their first day.

| Tool | Ships enabled | Good at |
| --- | --- | --- |
| Claude Code | yes | writing, refactoring and debugging code, careful multi step edits |
| Codex | yes | research, investigation, comparing options, writing up findings |
| OpenCode | no | an open source alternative implementer |

A tool is: a command, its arguments, an optional model, a permission switch, and
the "how to drive it" text. That is the whole extension mechanism — point one at
`aider`, at a REPL, at a deploy script, at anything with a prompt, and describe
how it behaves. No `kind` field, no code.

Socrates can also just run commands. `git status`, `npm test`, `rg TODO` need no
configuration at all; the tool list is only for programs it has to hold a
conversation with.

**Permissions.** Every shipped tool starts in its own unattended mode by
default — `--dangerously-skip-permissions` for Claude Code,
`--dangerously-bypass-approvals-and-sandbox` for Codex, `--auto` for OpenCode —
which is what makes long tasks work without babysitting. Turn the switch off and
the tool is started with its normal approval flags instead; its questions then
appear on screen, and Socrates answers them the way you would, after reading
what is being asked. Both sets of arguments are editable per tool.

**Where they run.** The admin dashboard has a workspace root (default
`~/.socrates/workspaces`); every chat gets its own directory below it, so chats
stay isolated. A chat can also be pinned to an existing project directory
through `PATCH /api/chats/{id}` with a `workspace` field.

**Sessions.** A session belongs to its chat, not to a single message: an agent
you started while asking one thing is still there for the next thing, and a long
build keeps running while you talk. Sessions live in their own host processes,
so they survive a restart of Socrates and are reconnected on the way back up.
They end when you close them, when the program exits, or when the chat is
deleted.

**Taking over.** Every session shown in the chat has an input box and a row of
key buttons. Whatever you type goes to the same program Socrates is driving, so
you can answer a prompt yourself, correct a wrong turn, or just watch.

## Choosing models

The model fields are searchable dropdowns over the live OpenRouter catalogue,
grouped by provider and annotated with context length and price. The list is
fetched when the dashboard opens — OpenRouter serves it without a key, so it
works before you have pasted one — and every field still accepts anything you
type, which is what you need for a tool's own model names such as `sonnet` or
`gpt-5-codex`.

## Voice

- **Microphones need a secure context.** Browsers only allow recording on
  `localhost` or over HTTPS. If you run Socrates on a server, put it behind a
  TLS reverse proxy, otherwise the microphone button will report that it is
  blocked.
- **Speech to text** goes through an audio capable OpenRouter chat model
  (`google/gemini-2.5-flash` by default). The browser records raw PCM and sends
  a 16 kHz WAV, so no ffmpeg is involved. You can also point Socrates at any
  OpenAI compatible `/audio/transcriptions` endpoint.
- **Text to speech** uses the browser's own speech synthesis by default, which
  needs no key and no network. For a better voice, configure any OpenAI
  compatible `/audio/speech` endpoint in the admin dashboard.

## Auto mode

The toggle in the top right turns the chat into a hands free surface: a large
microphone button with a recording timer, a short status line while the agents
work, and the finished answer shown as large as it fits and read out loud. If
the agent needs a decision, the question and its options fill the screen and are
spoken — you can tap an option or simply say "the second one".

## Remote access

Socrates always serves on its local address — that never changes, and it is the
address you point Cloudflare at. On top of that it can run a
[Cloudflare tunnel](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/)
as a supervised child process, so the instance is reachable from the internet
without opening a port, forwarding anything on your router, or owning a static
IP.

<p align="center">
  <img src="docs/screenshot-tunnel.png" alt="The remote access card in the admin dashboard" width="760">
</p>

There is nothing to install by hand. If `cloudflared` is not on your `PATH`,
Socrates downloads the official build for your platform from Cloudflare's
release page into `<data>/bin/cloudflared` the moment you start a tunnel, checks
that it runs, and uses it from then on. The dashboard shows the download
progress and has a **Download cloudflared** button if you want it ready
beforehand. A `cloudflared` that is already installed always wins, and an
explicit path in the settings is never overridden.

Pick one of two modes in **Admin → Remote access** (or right in the setup
wizard):

**Quick tunnel** — one click, no Cloudflare account. Cloudflare hands out a
random `https://….trycloudflare.com` address, which Socrates shows as soon as it
appears. The address changes on every restart, and anyone who has the link
reaches your login page, so treat it as a temporary demo door.

**Named tunnel** — your own hostname, your own Cloudflare account:

1. Zero Trust → Networks → Tunnels → **Create a tunnel** → *Cloudflared*.
2. Copy the token out of the install command it shows you and paste it into
   Socrates.
3. Add a public hostname for the tunnel and point it at the local address that
   the admin dashboard displays (`http://localhost:8080` by default). This is
   exactly why Socrates keeps serving locally.
4. Enter the same hostname in Socrates so it can link you to it, then press
   **Start tunnel**.

The tunnel is supervised: it restarts with backoff if `cloudflared` dies, it
comes back automatically when Socrates restarts, and it is shut down cleanly on
exit. The token is passed through the environment, so it never shows up in the
process list, and it is redacted from the log tail in the dashboard.

## Configuration

| Flag | Environment | Default | Meaning |
| --- | --- | --- | --- |
| `-addr` | `SOCRATES_ADDR` | `:8080` | listen address; use `127.0.0.1:8080` to accept local connections only |
| `-data` | `SOCRATES_DATA_DIR` | `~/.socrates` | database and workspaces |
| `-version` | | | print the version |
| | `SOCRATES_SHELL` | `$SHELL` | the shell a bare terminal session starts |
| | `OPENROUTER_API_KEY` | | seeds the key on first start |
| | `SOCRATES_WORKSPACE_ROOT` | `<data>/workspaces` | default workspace root |

Everything else lives in the admin dashboard and is stored in
`<data>/socrates.db` — a single SQLite file that holds settings, chats,
messages, every process step and your password hash.

## Security

Socrates is built for a single trusted operator.

- One password, hashed with PBKDF2-HMAC-SHA256 (210k rounds), a session cookie
  that is `HttpOnly` and `SameSite=Lax`, and rate limited logins.
- Socrates has a shell and runs **as the user that runs Socrates**, with the
  coding agents unattended by default. Access to the web interface is access to
  that shell — treat the password accordingly, and put Cloudflare Access in
  front of the hostname if you publish it.
- Socrates listens on every interface by default, so it works out of the box on
  a server, in Docker and behind a tunnel. Pass `-addr 127.0.0.1:8080` (or set
  `SOCRATES_ADDR`) to accept local connections only and publish it exclusively
  through the Cloudflare tunnel.
- Requests through a tunnel are rate limited per `CF-Connecting-IP`, and the
  session cookie is marked `Secure` as soon as the request arrives over HTTPS.
- Terminal sessions are reachable only through the authenticated API, are scoped
  to the chat that opened them, and talk to their host process over a unix
  socket inside the data directory.

## Development

```bash
make check       # gofmt, go vet, go test, go build
go test ./...    # unit tests, a scripted interactive CLI driven through a real
                 # pseudo terminal, and an end to end agent loop against a mock
```

Layout:

```
main.go                  flags, startup, graceful shutdown
internal/config          settings document and defaults
internal/store           SQLite persistence (chats, runs, steps, questions)
internal/openrouter      streaming chat completions, models, audio
internal/term            pseudo terminals, screen rendering, session hosts
internal/agent           the orchestration loop, tools, event bus
internal/server          HTTP API, auth, SSE, admin, voice, terminals
internal/tunnel          supervised Cloudflare tunnel and its installer
internal/proc            process group helpers
internal/web/static      the whole front end: plain HTML, CSS and JS
```

The front end has no build step. Edit the files under `internal/web/static` and
rebuild the binary — that is all.

## License

MIT. See [LICENSE](LICENSE).
