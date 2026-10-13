#!/bin/sh
# The three coding CLIs keep their credentials and their transcripts in $HOME,
# and each one works that directory out from the environment: Claude Code from
# $HOME (or $CLAUDE_CONFIG_DIR), Codex from $HOME/.codex, OpenCode from
# $HOME/.local/share. So does Socrates, when it looks a session's CLI id up in
# the agent's own store to resume it.
#
# In the image that directory would be /root, which lives in the container
# layer: `docker rm` would sign all three out and take every transcript with
# them, and the resume path a restart depends on - the one the Dockerfile
# promises - would have nothing to resume from. The image therefore puts $HOME
# inside the data volume, and this script is here for the only thing that
# cannot be done in a Dockerfile: on a volume that already existed before the
# image did, nothing has created that directory yet.
set -e
[ -n "$HOME" ] && mkdir -p "$HOME"
exec "$@"
