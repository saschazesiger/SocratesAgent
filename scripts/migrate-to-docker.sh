#!/usr/bin/env bash
# Move a local Socrates into the Docker volume, so that
#
#   docker run -p 8080:8080 -v socrates-data:/data socrates
#
# comes up as the installation you already have: same password, same OpenRouter
# and voice keys, same tunnel, same presets, same terminal and the same per
# agent model lists - and with the three CLIs already signed in.
#
# Two things travel, and they travel differently. The settings document and the
# password go through `socrates export | socrates import`, which is the only
# path that also moves the directories named in them from ~/.socrates into
# /data. The CLI credentials are files that belong to the CLIs, not to
# Socrates, so they are copied as files, into the $HOME the image keeps inside
# the volume.
#
# It is safe to run more than once: the import touches the settings and the
# password and nothing else, and the copy overwrites the credential files it
# knows about and leaves everything else in the volume alone.
set -euo pipefail

VOLUME=${VOLUME:-socrates-data}
IMAGE=${IMAGE:-socrates}
DATA_DIR=${SOCRATES_DATA_DIR:-$HOME/.socrates}
COPY_CREDENTIALS=1
EXPORT_ARGS=()
IMPORT_ARGS=()

usage() {
  cat >&2 <<USAGE
Usage: scripts/migrate-to-docker.sh [flags]

  -v, --volume NAME     the Docker volume to seed        (default: $VOLUME)
  -i, --image NAME      the Socrates image to run        (default: $IMAGE)
  -d, --data DIR        the local data directory to read (default: $DATA_DIR)
      --no-credentials  do not copy the CLI credentials; sign in inside the
                        container instead
      --no-password     do not carry the password over; the container asks for
                        a new one at /setup
      --keep-paths      write the directories exactly as they are here, without
                        moving the ones under $DATA_DIR into /data
  -h, --help            this
USAGE
}

while [ $# -gt 0 ]; do
  case "$1" in
    -v|--volume) VOLUME=$2; shift 2 ;;
    -i|--image) IMAGE=$2; shift 2 ;;
    -d|--data) DATA_DIR=$2; shift 2 ;;
    --no-credentials) COPY_CREDENTIALS=0; shift ;;
    --no-password) EXPORT_ARGS+=(-no-password); shift ;;
    --keep-paths) IMPORT_ARGS+=(-keep-paths); shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "migrate-to-docker: $1 is not a flag" >&2; usage; exit 2 ;;
  esac
done

command -v docker >/dev/null || { echo "migrate-to-docker: docker is not on PATH" >&2; exit 1; }
[ -f "$DATA_DIR/socrates.db" ] || {
  echo "migrate-to-docker: no database at $DATA_DIR/socrates.db - point -d at the local data directory" >&2
  exit 1
}
docker image inspect "$IMAGE" >/dev/null 2>&1 || {
  echo "migrate-to-docker: no image called $IMAGE - build it first with: docker build -t socrates ." >&2
  exit 1
}

# A container that is already serving this volume holds the settings in memory
# and writes them back when the dashboard saves, so an import underneath it is
# either invisible or overwritten. Stop it, migrate, start it again.
running=$(docker ps -q --filter "volume=$VOLUME")
if [ -n "$running" ]; then
  echo "migrate-to-docker: these containers are using the volume $VOLUME:" >&2
  docker ps --filter "volume=$VOLUME" --format '  {{.ID}}  {{.Image}}  {{.Names}}' >&2
  echo "Stop them first - a running Socrates would write its own settings back over the import." >&2
  exit 1
fi

echo "==> exporting the settings from $DATA_DIR"
export_file=$(mktemp); chmod 600 "$export_file"
trap 'rm -f "$export_file"' EXIT
# The export runs in the image rather than against a binary on this machine, so
# that both halves of the migration are the same build and a stale ./socrates
# can never write a document the container cannot read.
#
# It is mounted at its own path and not at some /src, because the document
# records the directory it was exported from and the import moves the paths
# that sat inside it: mounted anywhere else, ~/.socrates/workspaces would look
# like a directory on another disk and be left behind.
docker run --rm -v "$DATA_DIR:$DATA_DIR" "$IMAGE" export -data "$DATA_DIR" \
  ${EXPORT_ARGS[@]+"${EXPORT_ARGS[@]}"} > "$export_file"

echo "==> importing into the volume $VOLUME"
docker run --rm -i -v "$VOLUME:/data" "$IMAGE" import \
  ${IMPORT_ARGS[@]+"${IMPORT_ARGS[@]}"} < "$export_file"

if [ "$COPY_CREDENTIALS" = "1" ]; then
  # Credentials and configuration only. The transcripts, the caches and the
  # agents' own databases are deliberately left behind: OpenCode's alone is
  # several gigabytes here, and none of it is a setting.
  candidates=(
    .claude.json
    .claude/.credentials.json
    .claude/settings.json
    .claude/CLAUDE.md
    .claude/agents
    .claude/commands
    .claude/skills
    .claude/plugins
    .codex/auth.json
    .codex/config.toml
    .codex/AGENTS.md
    .config/opencode/opencode.json
    .config/opencode/opencode.jsonc
    .config/opencode/AGENTS.md
    .local/share/opencode/auth.json
  )
  present=()
  for path in "${candidates[@]}"; do
    [ -e "$HOME/$path" ] && present+=("$path")
  done
  if [ ${#present[@]} -eq 0 ]; then
    echo "==> no CLI credentials found under $HOME - sign the CLIs in inside the container"
  else
    echo "==> copying the CLI credentials into $VOLUME:/data/home"
    printf '      %s\n' "${present[@]}"
    tar -c -C "$HOME" "${present[@]}" \
      | docker run --rm -i -v "$VOLUME:/data" --entrypoint sh "$IMAGE" \
          -c 'mkdir -p /data/home && tar -x -C /data/home'
  fi
fi

cat <<DONE

Done. Start it with:

  docker run -p 8080:8080 -v $VOLUME:/data $IMAGE

Any note above about a preset directory is a path on this machine that the
container cannot see; add a bind mount for it, for example

  docker run -p 8080:8080 -v $VOLUME:/data -v /some/project:/some/project $IMAGE
DONE
