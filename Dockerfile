# ---- build ----------------------------------------------------------------
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=docker
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/socrates .

# ---- runtime --------------------------------------------------------------
# tmux is not optional. Every session Socrates runs is a tmux pane, so an image
# without it can serve the login page and nothing else; it is installed here so
# that the in-app installer never has to run inside a container. ncurses-term
# comes with it for the terminfo entries the CLIs ask for.
#
# The three CLIs (claude, codex, opencode) are Node programs, and a session is
# bound to one of them, so a container without them can only run Shell
# sessions. They are installed here; set INSTALL_AGENTS=0 to build a slim image
# and mount your own binaries instead. Their credentials are not in the image
# and never should be: sign each CLI in once, or mount the config directory it
# uses.
#
# The voice that reads answers out loud is Google Cloud Text-to-Speech, which
# is an API key and not a program: there is nothing to bake into the image and
# nothing to download. Add the key in Admin → Voice on first run.
FROM node:22-bookworm-slim
ARG INSTALL_AGENTS=1
ARG INSTALL_CLOUDFLARED=1
ARG INSTALL_VSCODE=1
ARG CODE_SERVER_VERSION=4.137.0
ARG TARGETARCH
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates curl git ripgrep tmux ncurses-term tini \
 && rm -rf /var/lib/apt/lists/* \
 && if [ "$INSTALL_AGENTS" = "1" ]; then \
      npm install -g @anthropic-ai/claude-code @openai/codex opencode-ai || \
      echo "warning: could not install every agent CLI, configure paths in /admin"; \
    fi \
 && if [ "$INSTALL_CLOUDFLARED" = "1" ]; then \
      curl -fsSL "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-${TARGETARCH:-amd64}" \
        -o /usr/local/bin/cloudflared && chmod +x /usr/local/bin/cloudflared || \
      echo "warning: could not install cloudflared, remote access stays off"; \
    fi

# code-server supplies the browser IDE, including its extension host and terminal.
RUN if [ "$INSTALL_VSCODE" = "1" ]; then \
      curl -fsSL "https://github.com/coder/code-server/releases/download/v${CODE_SERVER_VERSION}/code-server-${CODE_SERVER_VERSION}-linux-${TARGETARCH:-amd64}.tar.gz" -o /tmp/code-server.tar.gz \
      && mkdir -p /opt/code-server \
      && tar -xzf /tmp/code-server.tar.gz -C /opt/code-server --strip-components=1 \
      && ln -s /opt/code-server/bin/code-server /usr/local/bin/code-server \
      && rm /tmp/code-server.tar.gz; \
    fi

COPY --from=build /out/socrates /usr/local/bin/socrates
COPY deploy/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh

# HOME is inside the volume on purpose. Every credential the three CLIs hold,
# and every transcript Socrates resumes a session from, is written under $HOME
# by the CLI itself - ~/.claude and ~/.claude.json, ~/.codex,
# ~/.local/share/opencode - and with HOME at /root all of it would sit in the
# container layer, where `docker rm` takes it. In the volume it survives the
# container, which is what makes signing in once, or importing a signed-in
# machine's credentials, a thing you do once rather than every time.
#
# code-server's own state (~/.local/share/code-server, its extensions) follows
# the same variable, so the browser IDE keeps its extensions too.
ENV SOCRATES_DATA_DIR=/data \
    SOCRATES_ADDR=:8080 \
    HOME=/data/home
RUN mkdir -p /data/home
VOLUME ["/data"]
EXPOSE 8080

# GET /api/health is the only check that says anything: `socrates -version`
# prints a string and exits without ever touching the server, so a wedged HTTP
# server would report healthy. curl is already in the image, and the port comes
# out of SOCRATES_ADDR so that overriding it does not leave the check behind.
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s \
  CMD ADDR="${SOCRATES_ADDR:-:8080}"; \
      curl -fsS -o /dev/null "http://127.0.0.1:${ADDR##*:}/api/health" || exit 1

# tini is the entrypoint because the tmux server daemonizes out of Socrates'
# process tree and is reparented to PID 1. With Socrates as PID 1 that reparent
# ends in a zombie nobody reaps; tini reaps it. Socrates deliberately installs
# no SIGCHLD reaper of its own - one at PID 1 would race os/exec's Wait on
# Socrates' own children (every viewer's `tmux attach`, the installer, every
# discovery subprocess) and turn their exits into ECHILD. `docker run --init`
# does the same thing from the outside and is equally fine.
#
# A container restart is a reboot: the tmux server dies with the container, so
# sessions come back through the resume path rather than surviving - see the
# README. Bringing the container itself back is a restart policy on the run,
# not anything Socrates does.
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/docker-entrypoint.sh", "/usr/local/bin/socrates"]
