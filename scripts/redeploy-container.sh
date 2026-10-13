#!/bin/sh
# Rebuild the socrates image from this checkout and replace the running
# container with it. If the new container does not come up healthy, the old
# one is started again, exactly as it was.
#
#   scripts/redeploy-container.sh           build, smoke test, swap
#   scripts/redeploy-container.sh --check   build and smoke test only; the
#                                           running container is not touched
#
# Run it on the host. An agent inside the Socrates container dies with the
# container, so from there it is started as a unit of the host's systemd,
# which nothing in the container can take down:
#
#   on-host systemd-run --unit=socrates-redeploy --collect \
#     /root/SocratesAgent/scripts/redeploy-container.sh
#
# systemd-run does not pass the caller's environment on: the variables below
# reach the script only as `--setenv=NAME=value` options to systemd-run.
#
# Everything it does goes to $LOG (default /root/socrates-redeploy.log).
set -eu

NAME=${SOCRATES_NAME:-socrates}
IMAGE=${SOCRATES_IMAGE:-socrates}
LOG=${LOG:-/root/socrates-redeploy.log}
# The production run from the README, with shell sessions on the host.
RUN_ARGS=${SOCRATES_RUN_ARGS:---restart unless-stopped --pid=host --privileged -v socrates-data:/data -v /root:/root}
OLD="$NAME-previous"
SMOKE="$NAME-smoke"
SMOKE_VOL="$NAME-smoke-data"

check_only=
[ "${1-}" = --check ] && check_only=1

exec >>"$LOG" 2>&1
say() { echo "[$(date '+%F %T')] $*"; }

# healthy waits up to $2 seconds for the container to answer its own health
# check from the inside, the same request the image's HEALTHCHECK makes.
healthy() {
	i=0
	while [ "$i" -lt "$2" ]; do
		if [ "$(docker inspect -f '{{.State.Running}}' "$1" 2>/dev/null)" = true ] &&
			docker exec "$1" curl -fsS -o /dev/null http://127.0.0.1:8080/api/health 2>/dev/null; then
			return 0
		fi
		sleep 2
		i=$((i + 2))
	done
	return 1
}

say "=== redeploy ${check_only:+(check only) }from $(cd "$(dirname "$0")/.." && pwd)"
cd "$(dirname "$0")/.."
say "checkout: $(git rev-parse --abbrev-ref HEAD 2>/dev/null) $(git rev-parse --short HEAD 2>/dev/null), $(git status --porcelain 2>/dev/null | wc -l) uncommitted change(s)"

if [ -z "$check_only" ]; then
	docker inspect "$NAME" >/dev/null 2>&1 || { say "no container named $NAME - refusing"; exit 1; }
	if docker inspect "$OLD" >/dev/null 2>&1; then
		say "a container named $OLD is left from an earlier run - look at it and remove it first"
		exit 1
	fi
fi

say "building $IMAGE:candidate"
docker build -t "$IMAGE:candidate" .

# The new image has to start and answer before the old container is touched:
# a fresh volume, nothing mounted, nothing published.
say "smoke test"
docker rm -f "$SMOKE" >/dev/null 2>&1 || true
docker volume rm "$SMOKE_VOL" >/dev/null 2>&1 || true
docker run -d --name "$SMOKE" -v "$SMOKE_VOL:/data" "$IMAGE:candidate" >/dev/null
if healthy "$SMOKE" 60; then
	smoke_ok=1
else
	smoke_ok=
	docker logs --tail 40 "$SMOKE" || true
fi
docker rm -f "$SMOKE" >/dev/null 2>&1 || true
docker volume rm "$SMOKE_VOL" >/dev/null 2>&1 || true
[ -n "$smoke_ok" ] || { say "smoke test FAILED - the running container was not touched"; exit 1; }
say "smoke test passed"

if [ -n "$check_only" ]; then
	say "check only: done, nothing replaced"
	exit 0
fi

old_image=$(docker inspect -f '{{.Image}}' "$NAME")
docker tag "$old_image" "$IMAGE:previous"
docker tag "$IMAGE:candidate" "$IMAGE:latest"
say "old image $old_image tagged $IMAGE:previous"

rollback() {
	say "ROLLBACK: $*"
	docker logs --tail 60 "$NAME" 2>/dev/null || true
	docker rm -f "$NAME" >/dev/null 2>&1 || true
	docker rename "$OLD" "$NAME"
	docker start "$NAME" >/dev/null
	if healthy "$NAME" 120; then
		say "rolled back: the old container is running and healthy"
	else
		say "rolled back, but the old container is NOT healthy - check 'docker logs $NAME'"
	fi
	exit 1
}

# The old container is stopped and kept under another name, so a rollback is
# the very same container, not a re-creation of it.
say "stopping $NAME (everything inside it, including the agent that started this, stops now)"
docker stop -t 30 "$NAME" >/dev/null
docker rename "$NAME" "$OLD"

say "starting $NAME from $IMAGE:latest"
# shellcheck disable=SC2086 # RUN_ARGS is a list of arguments.
docker run -d --name "$NAME" $RUN_ARGS "$IMAGE:latest" >/dev/null || rollback "docker run failed"

healthy "$NAME" 120 || rollback "the new container did not become healthy within 120s"
# Healthy once is not enough if it crash-loops straight after.
sleep 10
healthy "$NAME" 30 || rollback "the new container stopped answering after it started"

docker rm "$OLD" >/dev/null
docker rmi "$IMAGE:candidate" >/dev/null 2>&1 || true
say "done: $NAME runs $(docker inspect -f '{{.Image}}' "$NAME"); the old image is kept as $IMAGE:previous"
