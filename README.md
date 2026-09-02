# Socrates

**A web harness for Claude Code, Codex and OpenCode.**
One Go binary with a ChatGPT style web interface and a hands free audio mode.
Every chat is bound to one of the three agents and one of its models, and what
you type goes straight into that program's own headless protocol. There is no
model in between: Socrates does not think, it carries.

<p align="center">
  <img src="docs/screenshot-chat.png" alt="A chat with a tool card, a reasoning step and the agent's answer" width="860">
</p>

---

## Why

Claude Code, Codex and OpenCode are excellent, and they live in a terminal. That
is fine at a desk and useless everywhere else — on a phone, on a train, in a car,
on the sofa. Every wrapper that fixes this seems to add a model of its own that
paraphrases the agent, guesses what it meant and gets in the way.

Socrates adds no model. It opens the agent the way its own maintainers intended —
`claude -p`, `codex app-server`, `opencode serve` — and renders what comes back:
the answer as chat text, every tool call as a card, questions as messages you
reply to. You pick the agent and the model when you start the chat, and from then
on you are talking to that agent, through a browser, with your voice if you want.

## What you get

- **A chat that feels familiar.** Sidebar with past conversations, streaming
  answers, markdown, mobile friendly. Light, quiet, minimal.
- **One agent per chat, chosen up front.** Agent, model and — where the agent has
  one — reasoning effort are picked in the new-chat sheet and shown in the chat
  header. The model can be changed between turns; the agent cannot, because a
  different agent is a different conversation.
- **Tool activity you can read.** Commands, file edits, searches, reasoning and
  subagents arrive as structured cards in the chat, in the order they happened,
  with their output and whether they succeeded. No screen scraping, no ANSI.
- **Turns that survive a restart.** Every chat's agent runs in its own detached
  host process. Restarting or upgrading Socrates does not interrupt a running
  turn or the subagents it started; the new process reattaches and replays
  everything that happened while it was away.
- **Built for a bad connection.** Losing signal is treated as normal, not as an
  error. A banner says the moment the live view stops being live and how old what
  you are looking at is — the chat and the hands free display both stop
  pretending. The stream reconnects itself and replays exactly what was missed,
  so nothing quietly goes stale. Anything you send while the connection is gone —
  a message, a new chat, a half typed draft — is kept and delivered when there is
  signal again, once and only once. The app even opens with no network at all,
  and picks up where it left off.
- **It asks you back.** When something is ambiguous the agent asks in its reply
  and stops, instead of guessing. You answer with the next message.
- **Voice in and out.** Record in the browser, transcribe through OpenRouter,
  have the answer read back by a voice that runs on the same machine as Socrates
  — no key, no account, no provider and nothing to choose. It installs itself on
  first start and is already in the Docker image.
- **Audio mode.** One big microphone button, a timer, and the answer shown as
  large as it fits and read out loud. When it ends on a question you hear it and
  simply speak your reply.
- **Archive instead of delete.** A conversation you are done with can be put away
  rather than thrown away: the transcript stays, its agent session is closed, and
  the sidebar hides it until you switch it from **Active** to **All**. Writing to
  an archived chat makes it active again by itself.
- **Reachable from anywhere, without opening a port.** A managed Cloudflare
  tunnel publishes the local server on the internet — a throwaway
  `trycloudflare.com` address in one click, or your own hostname with a tunnel
  token. `cloudflared` is downloaded automatically if you do not have it. Start,
  stop and watch it from the dashboard.
- **An admin dashboard for everything.** API key, a searchable picker over the
  live OpenRouter catalogue for the two models Socrates itself uses, which agents
  are switched on and where their binaries are, voice, remote access, password,
  and a setup check.
- **Single binary.** Go plus embedded HTML/CSS/JS, SQLite for state, no build
  step, no CDN, no telemetry.

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
  ┌─────────────────────────────────────────────────────────────┐
  │  socrates serve (single Go binary)                          │
  │                                                             │
  │    web UI · JSON API · SSE · SQLite state                   │
  │                        │                                    │
  │                   the engine: normalised events → messages, │
  │                   step cards, transcript, chat title        │
  │                        │                                    │
  └────────────────────────┼────────────────────────────────────┘
                           │
              OpenRouter   │  unix socket (one per chat)
      transcribe + titles  │
              only         ▼
                    socrates agent-host          detached: survives a
                           │                     restart of the server
                           ▼
                    the adapter for this chat's agent
                           │
        ┌──────────────────┼──────────────────────┐
        ▼                  ▼                      ▼
  claude -p            codex app-server      opencode serve
  stream-json          JSON-RPC 2.0          HTTP + SSE
  over stdio           over stdio            over 127.0.0.1
        │                  │                      │
        ▼                  ▼                      ▼
   Anthropic           OpenAI                your OpenCode provider
   (your login)        (your login)          (your login)
```

An **adapter** is the only thing in Socrates that knows a native protocol. It
translates that protocol into one small set of events — text, reasoning, a tool
started, a tool finished, a subagent, a notice, the turn ended — and the engine
turns those into the assistant messages and step cards you see. Adding a fourth
agent means writing one adapter, not patching the app.

Each chat's adapter runs inside its own **agent host**: a detached
`socrates agent-host` process that owns the CLI, appends every event to a journal
on disk and hands it to whoever is connected. That is what makes a restart
harmless — the turn keeps running in the host, and the server reattaches to the
journal and replays it. It is also why the browser can drop off a network and
come back without losing anything.

**OpenRouter is used for two things only:** transcribing what you say, and
writing a chat's title. The coding agents never go through it — each one talks to
its own provider with its own credentials, the same ones it uses in your
terminal.

## Requirements

- **Go 1.25+** — only to build the binary.
- **A Unix-like system** — macOS or Linux (WSL counts). Socrates builds and runs
  on Windows and the UI, the dashboard, the tunnel and voice all work there, but
  **agent chats do not**: they need a unix socket and POSIX process detachment,
  and sending a message on Windows is refused with a plain sentence rather than
  half working. Use the Docker image or WSL.
- **At least one agent CLI** in your `PATH`, **signed in already**:
  [`claude`](https://claude.com/claude-code),
  [`codex`](https://github.com/openai/codex),
  [`opencode`](https://opencode.ai).
  Socrates does not log you in and holds no keys for them; run each one once in a
  terminal first.
- **An OpenRouter API key** — <https://openrouter.ai/keys>. Only speech to text
  and chat titles depend on it; without one, Socrates works and stays silent.
- **`piper`** — only on macOS, and only for the voice that reads answers out
  loud: it is the one thing Socrates cannot install for you there. One
  `brew install piper`. See [Voice](#voice).
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

Or with Docker (the image also brings the three agent CLIs, `cloudflared` and
the voice):

```bash
docker build -t socrates .
docker run -p 8080:8080 -v socrates-data:/data socrates
```

The CLIs still need their own credentials inside the container — mount your
`~/.claude`, `~/.codex` and `~/.config/opencode` into it, or sign in once in a
shell on the running container.

Each of the three extras is a build argument, so you can leave out whatever you
would rather mount yourself or simply not carry:

```bash
docker build -t socrates --build-arg INSTALL_AGENTS=0 --build-arg INSTALL_VOICE=0 .
```

`INSTALL_AGENTS`, `INSTALL_CLOUDFLARED` and `INSTALL_VOICE` all default to `1`,
and `VERSION` — what `socrates -version` prints — defaults to `docker`. Nothing
is lost by leaving one out: Socrates downloads `cloudflared` and the voice into
its data directory the first time it needs them, and an agent binary can be
mounted in and pointed at from the dashboard.

Then open <http://localhost:8080>.

## First run

1. `/setup` asks you for the password you will use from now on. You can paste
   your OpenRouter key right away, and decide whether the instance should be
   published through a Cloudflare tunnel — both can also be changed later.
2. You land in the admin dashboard. Press **Run checks**. It verifies your
   OpenRouter key, the workspace directory, that agent hosts can be started on
   this machine, **each enabled agent** — that its binary is on `PATH`, what
   version it reports and how many models it offers — remote access, and both
   halves of voice.
3. Go back to the chat, press **+**, pick an agent and a model, and ask for
   something.

<p align="center">
  <img src="docs/screenshot-admin.png" alt="The Agents card in the admin dashboard" width="860">
</p>

## Agents and models

A chat is bound to one agent at creation and keeps it for life. The new-chat
sheet lists the three agents with what Socrates found on this machine: whether
the binary is there, which version, which models it offers and any note that
applies to it. An agent you do not use can be switched off in the dashboard and
disappears from the picker.

| Agent | How Socrates talks to it | Model | Reasoning effort |
| --- | --- | --- | --- |
| **Claude Code** | `claude -p --output-format stream-json --input-format stream-json`, one long lived process per chat, newline JSON over stdio | `--model` | `--effort` |
| **Codex** | `codex app-server`, JSON-RPC 2.0 over stdio | on `thread/start` and on every turn | `model_reasoning_effort` |
| **OpenCode** | `opencode serve` on a loopback port it picks itself, HTTP for calls and SSE for events | per session, `provider/model` | as the model's *variant* |

**Where the models come from.** Codex and OpenCode are asked: Socrates reads
Codex's `model/list` and OpenCode's `GET /config/providers` - every provider with
working credentials and its whole model list, the same list OpenCode's own picker
shows - and offers what that installation actually has.
Claude Code has no such command, so Socrates ships a curated list of the
documented aliases — Opus, Sonnet, Haiku, Fable, Best, Opus plan and the 1M
variants — and the picker's field also accepts anything you type, so a new alias
works the day Anthropic ships it without waiting for a Socrates release. A model
id Claude does not know comes back as a normal failed turn with Claude's
own message in it.

**Effort levels are the agent's own.** Claude Code takes low, medium, high,
xhigh and max; Codex reports a list per model, xhigh included; OpenCode names
its "variants" per model. The effort row offers exactly what the chosen model
reports, and the server refuses a level the model does not name.

**Your own short list.** Four hundred OpenRouter models is a list to search,
not to choose from. The Agents card in the dashboard has a short list per agent:
pick a model from what the agent reports or type an id, give each one the effort
a new chat starts on, and save. The new-chat sheet then offers exactly that list,
starting on its first entry, and an id you typed is accepted as typed. An empty
list offers everything the agent reports.

**Changing the model.** Allowed between turns, never during one. Socrates closes
the chat's agent host and opens a new one on the new model; the agent's own
session is resumed, so the conversation continues rather than restarting.

**Sessions.** A session belongs to the chat, not to a message: what the agent
learned while answering one thing is still there for the next. Sessions are the
CLIs' own — Socrates stores their ids and resumes them — so they outlive both the
host process and Socrates itself. They end when the chat is archived or deleted.

**Where they run.** The dashboard has a workspace root (default
`~/.socrates/workspaces`); a chat gets its own directory below it — named after
the chat — so chats stay isolated. A chat can instead be pointed at an existing
project directory, which is what you want when the work is on a real repository.

**Known limitation — OpenCode and OpenRouter models.**

> OpenCode 1.17.x can only run models whose provider uses `@ai-sdk/openai`,
> `@ai-sdk/anthropic`, or `@ai-sdk/openai-compatible` with a `url`. OpenRouter
> models are listed by OpenCode but fail at the first turn with
> `UnsupportedApiError`. Socrates shows what OpenCode reports and does not change
> your OpenCode configuration. If you want OpenRouter models in OpenCode,
> override the provider yourself in `opencode.json`
> (`"npm": "@ai-sdk/openai-compatible"`, `"options": {"baseURL":
> "https://openrouter.ai/api/v1"}`) and export `OPENROUTER_API_KEY`; the built-in
> `opencode` (Zen) models work out of the box.

The failure is not loud: OpenCode sends nothing at all on that path, so the turn
ends with *"the agent produced no answer"* rather than with the real error. The
same warning is on the OpenCode entry in the picker, which is where you will see
it before spending a turn finding out.

## Choosing models

There are two kinds of model here and they are not interchangeable.

**The agent's models** are the ones above. They are that program's own names —
`sonnet`, `gpt-5.6-sol`, `opencode/big-pickle` — they are chosen per chat, and
they never touch OpenRouter.

**Socrates' own two models** are OpenRouter models and live in the dashboard:
the one that transcribes a recording, and the one that writes a chat's title.
Both are picked with a searchable dropdown over the live OpenRouter catalogue,
grouped by provider and annotated with context length and price. The list is
fetched when the dashboard opens; OpenRouter serves it without a key, so it works
before you have pasted one, and every field still accepts anything you type.

The voice that reads an answer out loud is on neither list. It is not a model you
pick — see **Voice** below.

## Voice

- **Microphones need a secure context.** Browsers only allow recording on
  `localhost` or over HTTPS. If you run Socrates on a server, put it behind a TLS
  reverse proxy or the Cloudflare tunnel, otherwise the microphone button will
  report that it is blocked.
- **Speech to text** goes through the transcription model chosen in the
  dashboard — an audio capable chat model such as `google/gemini-2.5-flash`, or a
  dedicated transcriber such as `openai/gpt-transcribe` or `deepgram/nova-3`.
  Socrates works out which of the two endpoints a model lives at and remembers
  it. The browser records raw PCM and sends a 16 kHz WAV, so no ffmpeg is
  involved.
- **Text to speech** is one voice, running on the server, and there is nothing to
  configure: no provider, no model, no voice name, no API key and no account.
  Socrates installs [Piper](https://github.com/rhasspy/piper) and both voices by
  itself the first time it starts — the Voice card in the dashboard shows the
  download while it runs — and the Docker image already has all of it baked in,
  so a container reads the first answer it is given. German is
  `de_DE-thorsten-medium`, English is `en_US-ljspeech-medium`, and the spoken
  language below is what picks between them. Both are installed whichever
  language you speak, on purpose: the language is a setting you flip, and a flip
  that starts a 60 MB download is a broken experience.
  - **macOS is the exception.** There Socrates installs nothing and says so: the
    published macOS builds of this Piper release ship without the libraries their
    own binary loads, so an installation would look finished and then abort
    inside the loader. Run `brew install piper` once and Socrates picks it up
    from your `PATH`; the two voices are still downloaded and managed for you.
    Linux (x86_64, aarch64, armv7l) and Windows x86_64 install themselves.
  - **It sounds synthetic**, clearly so, and it is completely intelligible. That
    is the trade: a small neural model on your own CPU instead of a voice that is
    indistinguishable from a person and belongs to somebody else.
  - **It is fast.** Roughly ten times faster than real time on an ordinary CPU —
    445 characters of German became 24 seconds of audio in 1.5 seconds — so the
    answer starts almost as soon as it is written.
  - **It costs nothing**, per character or otherwise, and it needs no connection
    to a third party at any point. What it needs is one download of about 150 MB
    — the engine and both voices — and only the first time; it unpacks to about
    180 MB under `<data>/voice`.
  - The bundled engine ships GPL-3.0 and MIT components; what they are and where
    their source lives is in
    [THIRD_PARTY_LICENSES.md](THIRD_PARTY_LICENSES.md).
- **Spoken language** is one setting in the admin dashboard, English or Deutsch,
  and it covers everything Socrates says: which language your recording is
  transcribed into, which installed voice reads the answer out loud, and which
  language the agent is asked to answer in. Getting this wrong is what makes a
  German answer come out with an English accent. English is the default; there is
  no detection and nothing to configure per chat.

## Audio mode

The top bar has one slider with two stops — **Chat** and **Audio** — and only one
of them at a time. The second turns the chat into a hands free surface: a large
microphone button with a recording timer, a short status line while the agent
works, and the finished answer shown as large as it fits and read out loud. If
the agent needs a decision it asks for it in that answer, which is read out with
the rest — you tap the microphone and say what you want.

<p align="center">
  <img src="docs/screenshot-auto.png" alt="Audio mode on a phone: one microphone button and the answer shown large" width="300">
</p>

One answer is spoken per turn, at the end, not every sentence as it appears. A
long turn is therefore quiet for a while; the status line is what tells you it is
still working. That is deliberate: narrating a twenty minute refactor into a car
is worse than waiting for the result.

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
Socrates downloads the official build for your platform from Cloudflare's release
page into `<data>/bin/cloudflared` the moment you start a tunnel, checks that it
runs, and uses it from then on. The dashboard shows the download progress and has
a **Download cloudflared** button if you want it ready beforehand. A
`cloudflared` that is already installed always wins, and an explicit path in the
settings is never overridden.

Pick one of two modes in **Admin → Remote access** (or right in the setup
wizard):

**Quick tunnel** — one click, no Cloudflare account. Cloudflare hands out a
random `https://….trycloudflare.com` address, which Socrates shows as soon as it
appears. The address changes on every restart, and anyone who has the link
reaches your login page, so treat it as a temporary demo door.

**Named tunnel** — your own hostname, your own Cloudflare account:

1. Zero Trust → Networks → Tunnels → **Create a tunnel** → *Cloudflared*, name it
   and save it.
2. Cloudflare then shows **Install and run connector**. Copy the token out of the
   install command on that screen and paste it into Socrates.
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
| `-version` | | | print the version and exit |
| | `OPENROUTER_API_KEY` | | seeds the key on first start |
| | `SOCRATES_WORKSPACE_ROOT` | `<data>/workspaces` | default workspace root |
| | `SOCRATES_PIPER_DIR` | | a Piper installation to use instead of the managed one; the Docker image sets it |
| | `XDG_RUNTIME_DIR` | `$TMPDIR` | where the agent host sockets live; a unix socket path has a hard length limit, and Socrates says so by name if yours is too long |

There are two subcommands. `socrates serve` is the same thing as plain `socrates`
and takes the same flags, for anyone who prefers to say it out loud.
`socrates agent-host --dir <dir>` is internal: it hosts one agent session and
Socrates starts it itself — that is the box in the diagram above, and not
something to run by hand.

Everything else lives in the admin dashboard and is stored in
`<data>/socrates.db` — a single SQLite file that holds settings, chats, messages,
every step and your password hash.

## Security

Socrates is built for a single trusted operator, and it runs the coding agents
**fully unattended**. That is the point of it — nobody can tap "allow" from a car
— and it is the thing to understand before you publish it.

- **Unattended means unattended.** Each agent is started in its own bypass mode:
  `--permission-mode bypassPermissions` for Claude Code (plus `IS_SANDBOX=1`,
  because it refuses to skip permissions as root — the normal case in a container
  — unless it is told it is already confined); `approvalPolicy: "never"` and
  `sandbox: "danger-full-access"` for Codex; `OPENCODE_PERMISSION="allow"` for
  OpenCode. There are no approval cards in the UI, by design. Anything the agent
  decides to run, it runs.
- **Remote control is off for every chat.** Claude Code and Codex can both hand a
  running session to their vendor's own phone and web apps — Anthropic's Remote
  Control, OpenAI's remote control — where a second surface steers the very turn
  Socrates is driving and the transcript is kept on their servers for as long as
  it is connected. Socrates is the surface, so every session it starts turns that
  off explicitly rather than inheriting whatever the machine or the account
  defaults to: `--settings '{"disableRemoteControl":true,"remoteControlAtStartup":false}'`
  for Claude Code, `-c features.remote_control=false` for Codex. OpenCode has no
  such feature. `extra_args` in the admin dashboard is appended after these, so
  it is also the way to undo them — note that a second `--settings` replaces this
  one outright rather than merging with it.
- **The agents run as the user that runs Socrates**, with that user's files,
  credentials and network. Access to the web interface is therefore access to
  that account — treat the password accordingly, and put Cloudflare Access in
  front of the hostname if you publish it.
- One password, hashed with PBKDF2-HMAC-SHA256 (210k rounds), a session cookie
  that is `HttpOnly` and `SameSite=Lax`, and rate limited logins.
- Socrates listens on every interface by default, so it works out of the box on a
  server, in Docker and behind a tunnel. Pass `-addr 127.0.0.1:8080` (or set
  `SOCRATES_ADDR`) to accept local connections only and publish it exclusively
  through the Cloudflare tunnel.
- Requests through a tunnel are rate limited per `CF-Connecting-IP`, and the
  session cookie is marked `Secure` as soon as the request arrives over HTTPS.
- Agent hosts are reachable only through the authenticated API. Each one has its
  own unix socket in a `0700` directory under the user's runtime directory, and
  the OpenCode server one of them starts is bound to loopback behind a random
  password generated per process and never written to disk.

## Development

```bash
make check       # exactly what CI runs: gofmt, go vet, go mod tidy, go test -race, go build
make fmt         # the one target that rewrites your files
make e2e         # the browser end to end suite (needs node and a Chromium)
go test ./...    # unit tests, the adapters against fake CLIs that speak the real
                 # protocols, and hosts started as real detached processes
```

Layout:

```
main.go                     flags, startup, graceful shutdown, the agent-host subcommand
internal/harness            the adapter contract: Spec, the normalised Event, the registry
internal/harness/claude     Claude Code: stream-json over stdio
internal/harness/codex      Codex: app-server JSON-RPC over stdio
internal/harness/opencode   OpenCode: serve, HTTP + SSE on loopback
internal/harness/fakes      fake claude/codex/opencode binaries for hermetic tests
internal/agenthost          the detached host process, its socket protocol and its journal
internal/engine             turns, the event pump, step cards, the SSE bus, chat titles
internal/catalog            which agents are installed and which models they offer
internal/store              SQLite persistence (chats, runs, steps, messages)
internal/config             settings document and defaults
internal/openrouter         transcription, the model catalogue, chat completions for titles
internal/piper              the local voice: its installer and the renderer
internal/server             HTTP API, auth, SSE, admin, agents, voice, tunnel
internal/tunnel             supervised Cloudflare tunnel and its installer
internal/proc               process group helpers
internal/web/static         the whole front end: plain HTML, CSS and JS
e2e                         Playwright specs driven against a real server
```

The front end has no build step. Edit the files under `internal/web/static` and
rebuild the binary — that is all.

## License

MIT. See [LICENSE](LICENSE).

The local voice is not part of that: Piper, the libraries it ships with and the
two voice models carry their own licences, one of them GPL-3.0. They are named in
[THIRD_PARTY_LICENSES.md](THIRD_PARTY_LICENSES.md), which matters to anyone
publishing the Docker image.
