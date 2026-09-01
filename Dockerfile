# ---- build ----------------------------------------------------------------
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=docker
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/socrates .

# ---- runtime --------------------------------------------------------------
# The three agents (claude, codex, opencode) are Node CLIs, and a chat is bound
# to one of them, so a container without them can serve the UI and nothing else.
# They are installed here; set INSTALL_AGENTS=0 to build a slim image and mount
# your own binaries instead. Their credentials are not in the image and never
# should be: sign each CLI in once, or mount the config directory it uses.
#
# The voice that reads answers out loud is Piper, running on this machine: the
# engine and the two voices Socrates speaks are baked in, so a container needs
# no download and no configuration on its first run. That is the 150 MB the app
# would otherwise fetch itself, and about 180 MB unpacked in the image. Set
# INSTALL_VOICE=0 to leave it out - Socrates then downloads the same files into
# its data directory the first time an answer has to be read out loud.
# See THIRD_PARTY_LICENSES.md for what is bundled and under which licence.
FROM node:22-bookworm-slim
ARG INSTALL_AGENTS=1
ARG INSTALL_CLOUDFLARED=1
ARG INSTALL_VOICE=1
ARG TARGETARCH
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl git ripgrep \
 && rm -rf /var/lib/apt/lists/* \
 && if [ "$INSTALL_AGENTS" = "1" ]; then \
      npm install -g @anthropic-ai/claude-code @openai/codex opencode-ai || \
      echo "warning: could not install every agent CLI, configure paths in /admin"; \
    fi \
 && if [ "$INSTALL_CLOUDFLARED" = "1" ]; then \
      curl -fsSL "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-${TARGETARCH:-amd64}" \
        -o /usr/local/bin/cloudflared && chmod +x /usr/local/bin/cloudflared || \
      echo "warning: could not install cloudflared, remote access stays off"; \
    fi \
 && if [ "$INSTALL_VOICE" = "1" ]; then \
      case "${TARGETARCH:-amd64}" in arm64) PIPER_ARCH=aarch64 ;; *) PIPER_ARCH=x86_64 ;; esac \
      && VOICES="https://huggingface.co/rhasspy/piper-voices/resolve/main" \
      && mkdir -p /opt/piper/voices \
      && curl -fsSL "https://github.com/rhasspy/piper/releases/download/2023.11.14-2/piper_linux_${PIPER_ARCH}.tar.gz" \
           -o /tmp/piper.tar.gz \
      && tar -xzf /tmp/piper.tar.gz -C /opt/piper \
      && rm /tmp/piper.tar.gz \
      && curl -fsSL "${VOICES}/de/de_DE/thorsten/medium/de_DE-thorsten-medium.onnx" \
           -o /opt/piper/voices/de_DE-thorsten-medium.onnx \
      && curl -fsSL "${VOICES}/de/de_DE/thorsten/medium/de_DE-thorsten-medium.onnx.json" \
           -o /opt/piper/voices/de_DE-thorsten-medium.onnx.json \
      && curl -fsSL "${VOICES}/en/en_US/ljspeech/medium/en_US-ljspeech-medium.onnx" \
           -o /opt/piper/voices/en_US-ljspeech-medium.onnx \
      && curl -fsSL "${VOICES}/en/en_US/ljspeech/medium/en_US-ljspeech-medium.onnx.json" \
           -o /opt/piper/voices/en_US-ljspeech-medium.onnx.json \
      || { echo; \
           echo "################################################################"; \
           echo "## WARNING: the local voice is NOT in this image.             ##"; \
           echo "## /opt/piper is missing or incomplete, so the image built    ##"; \
           echo "## as if INSTALL_VOICE=0 had been passed. Nothing is broken:  ##"; \
           echo "## Socrates reads an incomplete directory as \"not installed\"  ##"; \
           echo "## and downloads engine and voices into /data on first use.   ##"; \
           echo "################################################################"; \
           echo; }; \
    fi

COPY --from=build /out/socrates /usr/local/bin/socrates

# SOCRATES_PIPER_DIR is what makes the app use the copy above instead of
# downloading its own. A slim build leaves the directory empty, which reads as
# "not installed" and puts the download back.
ENV SOCRATES_DATA_DIR=/data \
    SOCRATES_ADDR=:8080 \
    SOCRATES_PIPER_DIR=/opt/piper
VOLUME ["/data"]
EXPOSE 8080

# GET /api/health is the only check that says anything: `socrates -version`
# prints a string and exits without ever touching the server, so a wedged HTTP
# server would report healthy. curl is already in the image, and the port comes
# out of SOCRATES_ADDR so that overriding it does not leave the check behind.
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s \
  CMD ADDR="${SOCRATES_ADDR:-:8080}"; \
      curl -fsS -o /dev/null "http://127.0.0.1:${ADDR##*:}/api/health" || exit 1

ENTRYPOINT ["/usr/local/bin/socrates"]
