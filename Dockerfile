# ---- build ----------------------------------------------------------------
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=docker
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/socrates .

# ---- runtime --------------------------------------------------------------
# The delegate agents (claude, codex, opencode) are Node CLIs. They are
# installed here so a container can actually delegate work; set INSTALL_AGENTS=0
# to build a slim image and mount your own binaries instead.
FROM node:22-bookworm-slim
ARG INSTALL_AGENTS=1
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates git ripgrep \
 && rm -rf /var/lib/apt/lists/* \
 && if [ "$INSTALL_AGENTS" = "1" ]; then \
      npm install -g @anthropic-ai/claude-code @openai/codex opencode-ai || \
      echo "warning: could not install every agent CLI, configure paths in /admin"; \
    fi

COPY --from=build /out/socrates /usr/local/bin/socrates

ENV SOCRATES_DATA_DIR=/data \
    SOCRATES_ADDR=:8080
VOLUME ["/data"]
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s \
  CMD ["/usr/local/bin/socrates", "-version"]

ENTRYPOINT ["/usr/local/bin/socrates"]
