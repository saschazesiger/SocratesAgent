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
#
# Upgrading to a build that keeps its state in an S3 bucket: the new container
# needs the bucket's configuration before it starts, or it comes up serving
# only the connect page - with the tunnel off, so nobody outside can reach it,
# and /api/health still 200, so nothing here would notice. Either write
# /data/s3.json into the volume (see README, "Upgrading an existing
# installation") or pass the SOCRATES_S3_* variables through
# SOCRATES_RUN_ARGS, e.g. --env-file /root/socrates-s3.env (a file kept
# outside the repository). The swap is refused while neither is there.
#
# The database is copied before the new image can migrate it, and a rollback
# puts that copy back: the old image refuses a database a newer one migrated.
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

# s3_configured reports whether the new container will find a bucket: the
# variables in its arguments, or s3.json in the volume it shares with the
# running container.
s3_configured() {
	case " $RUN_ARGS " in
	*SOCRATES_S3_BUCKET* | *--env-file*) return 0 ;;
	esac
	docker exec "$NAME" test -f /data/s3.json 2>/dev/null
}
if ! s3_configured; then
	msg="no S3 bucket is configured for $NAME: write /data/s3.json into the volume or pass SOCRATES_S3_* through SOCRATES_RUN_ARGS (e.g. --env-file /root/socrates-s3.env, kept outside the repo); see README 'Upgrading an existing installation'"
	if [ -n "$check_only" ]; then
		say "WARNING: $msg. A swap would be refused."
	else
		say "$msg - refusing to replace the running container"
		exit 1
	fi
fi

if [ -n "$check_only" ]; then
	say "check only: done, nothing replaced"
	exit 0
fi

old_image=$(docker inspect -f '{{.Image}}' "$NAME")
docker tag "$old_image" "$IMAGE:previous"
docker tag "$IMAGE:candidate" "$IMAGE:latest"
say "old image $old_image tagged $IMAGE:previous"

# The database files, copied (backup_db) and put back (restore_db) with the
# old image, in a throwaway container that shares the old container's volumes.
DB_FILES="socrates.db socrates.db-wal socrates.db-shm"
backup_db() {
	docker run --rm --volumes-from "$OLD" --entrypoint sh "$IMAGE:previous" -c \
		"cd /data && for f in $DB_FILES; do rm -f \"\$f.pre-redeploy\"; if [ -f \"\$f\" ]; then cp -p \"\$f\" \"\$f.pre-redeploy\" || exit 1; fi; done"
}
restore_db() {
	docker run --rm --volumes-from "$OLD" --entrypoint sh "$IMAGE:previous" -c \
		"cd /data && [ -f socrates.db.pre-redeploy ] && for f in $DB_FILES; do rm -f \"\$f\"; if [ -f \"\$f.pre-redeploy\" ]; then cp -p \"\$f.pre-redeploy\" \"\$f\" || exit 1; fi; done"
}

rollback() {
	say "ROLLBACK: $*"
	docker logs --tail 60 "$NAME" 2>/dev/null || true
	docker rm -f "$NAME" >/dev/null 2>&1 || true
	# The new image may have migrated the database already; the old one
	# refuses a newer schema. What the new container wrote meanwhile is in
	# its bucket, not lost, but this volume goes back to how it was.
	if [ -n "${db_saved-}" ]; then
		if restore_db; then
			say "the database is back as it was before the redeploy"
		else
			say "could not put the database back - the copy is /data/socrates.db.pre-redeploy"
		fi
	fi
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
db_saved=
if backup_db; then
	db_saved=1
	say "database copied to /data/socrates.db.pre-redeploy"
else
	say "could not copy the database - a rollback could not undo a migration"
	docker rename "$OLD" "$NAME"
	docker start "$NAME" >/dev/null
	say "the old container was started again; nothing replaced"
	exit 1
fi

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
