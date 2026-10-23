#!/usr/bin/env bash
# Install Socrates on this machine, as root. From a checkout:
#
#   sudo scripts/install.sh
#
# or without one:
#
#   curl -fsSL https://raw.githubusercontent.com/saschazesiger/SocratesAgent/main/scripts/install.sh | sudo bash
#
# It installs everything Socrates runs on - the packages, tmux, Go to build
# it, Node for the sessions, Claude Code, Codex, OpenCode and code-server -
# builds Socrates from the repository's newest commit and starts it as a
# systemd service, with socrates-update.timer to keep all of it up to date
# from then on. Running it again is safe: it repairs what is missing and
# brings everything up to date; /etc/socrates.env is kept as it is.
#
# Where it installs from: SOCRATES_REPO and SOCRATES_BRANCH, in the
# environment or /etc/socrates.env; otherwise the checkout's origin and
# branch; otherwise github.com/saschazesiger/SocratesAgent, main.
#
# Debian or Ubuntu with systemd, on x86_64 or arm64.
set -euo pipefail

HERE=$(cd "$(dirname "$(readlink -f "${BASH_SOURCE[0]:-$0}")")" 2>/dev/null && pwd || pwd)

[ "$(id -u)" = 0 ] || { echo "install.sh: run it as root: sudo $0" >&2; exit 1; }
command -v apt-get >/dev/null || { echo "install.sh: this needs Debian or Ubuntu (apt-get)" >&2; exit 1; }
[ -d /run/systemd/system ] || { echo "install.sh: this needs systemd" >&2; exit 1; }

# Piped in from curl: there is nothing beside this script yet. Clone the
# source and run the install.sh in it.
if ! [ -f "$HERE/lib.sh" ] || ! [ -f "$HERE/../go.mod" ]; then
	repo=${SOCRATES_REPO:-https://github.com/saschazesiger/SocratesAgent.git}
	branch=${SOCRATES_BRANCH:-main}
	export DEBIAN_FRONTEND=noninteractive
	command -v git >/dev/null && command -v curl >/dev/null ||
		{ apt-get -q -o DPkg::Lock::Timeout=900 update -qq || true; apt-get -q -o DPkg::Lock::Timeout=900 install -y --no-install-recommends git curl ca-certificates; }
	if ! [ -d /opt/socrates/src/.git ]; then
		mkdir -p /opt/socrates
		rm -rf /opt/socrates/src.tmp
		git clone --quiet --branch "$branch" "$repo" /opt/socrates/src.tmp
		mv /opt/socrates/src.tmp /opt/socrates/src
	fi
	SOCRATES_REPO=$repo SOCRATES_BRANCH=$branch exec bash /opt/socrates/src/scripts/install.sh "$@"
fi

export REPO_DIR=$(cd "$HERE/.." && pwd)
. "$HERE/lib.sh"
[ -n "$ARCH" ] || die "$(uname -m) is not a supported architecture"

repo=$(setting SOCRATES_REPO "$(git -c safe.directory="$REPO_DIR" -C "$REPO_DIR" remote get-url origin 2>/dev/null || true)")
repo=${repo:-$DEFAULT_REPO}
branch=$(setting SOCRATES_BRANCH "$(git -c safe.directory="$REPO_DIR" -C "$REPO_DIR" symbolic-ref --short -q HEAD 2>/dev/null || true)")
branch=${branch:-main}

say "Configuration: $ENV_FILE"
if ! [ -f "$ENV_FILE" ]; then
	(umask 077 && cat >"$ENV_FILE") <<EOF
# Read by socrates.service at start (systemctl restart socrates after a
# change) and by the updater at every run.

# Where the web interface listens. 127.0.0.1 keeps it on this machine; publish
# it with the Cloudflare tunnel in Admin. :8080 opens it to the network.
SOCRATES_ADDR=127.0.0.1:8080

# What socrates-update.timer does every night and after a boot: all (Socrates
# and the tools it runs), deps (only the tools), off (only put back what is
# missing or broken).
SOCRATES_AUTO_UPDATE=all

# Where Socrates is built from.
SOCRATES_REPO=$repo
SOCRATES_BRANCH=$branch

# Optional pins. Node follows the newest LTS line unless a major is named
# here; code-server its newest release unless a version is; Claude Code the
# latest channel unless this says stable.
#SOCRATES_NODE_MAJOR=24
#SOCRATES_CODE_SERVER_VERSION=4.141.0
#SOCRATES_CLAUDE_CHANNEL=stable
EOF
	info "wrote $ENV_FILE"
else
	# An older file lacks the updater's settings; they are added, never
	# changed - after a newline, should its last line have none, or the
	# first would be glued onto that line's value.
	[ -s "$ENV_FILE" ] && [ -n "$(tail -c1 "$ENV_FILE")" ] && echo >>"$ENV_FILE"
	for kv in "SOCRATES_AUTO_UPDATE=all" "SOCRATES_REPO=$repo" "SOCRATES_BRANCH=$branch"; do
		grep -Eq "^[[:space:]]*(export[[:space:]]+)?${kv%%=*}=" "$ENV_FILE" || printf '%s\n' "$kv" >>"$ENV_FILE"
	done
fi
# It may hold keys.
chmod 600 "$ENV_FILE"

# Somebody else on the port is a restart loop later; better said now.
addr=$(setting SOCRATES_ADDR 127.0.0.1:8080)
port=${addr##*:}
host=${addr%:*}
# Only a listener on the same address, or on every address, is in the way:
# a k3s node has node-cache on 169.254.20.10:8080, which 127.0.0.1:8080 is
# not.
taken=$(ss -Htlnp "sport = :$port" 2>/dev/null | grep -v '"socrates"' |
	awk -v h="$host" '{a=$4; sub(/:[0-9]+$/, "", a); sub(/%.*/, "", a)
		if (h == "" || h == "0.0.0.0" || h == "[::]" || a == h || a == "0.0.0.0" || a == "*" || a == "[::]") print}' || true)
if [ -n "$taken" ]; then
	printf '%s\n' "$taken" >&2
	die "port $port on ${host:-every address} is taken by another program; set SOCRATES_ADDR in $ENV_FILE to a free one and run this again"
fi

say "Updater and services"
mkdir -p "$LIBEXEC" "$OPT" "$DATA" "$CACHE"
for f in lib.sh deps.sh update.sh install.sh deploy.sh; do
	install -m 755 "$HERE/$f" "$LIBEXEC/.$f.new"
	mv -f "$LIBEXEC/.$f.new" "$LIBEXEC/$f"
done
for f in socrates.service socrates-update.service socrates-update.timer; do
	install -m 644 "$REPO_DIR/deploy/$f" "$UNIT_DIR/$f"
done
systemctl daemon-reload
systemctl enable socrates.service socrates-update.timer >/dev/null

# The source the updater builds from. A checkout pointing somewhere else is
# pointed at the configured repository.
if [ -d "$SRC/.git" ]; then
	git -c safe.directory="$SRC" -C "$SRC" remote set-url origin "$repo"
fi

# Everything, now, in this terminal: the packages and tools, then Socrates.
# A tool that cannot be installed is reported and the rest goes on; Socrates
# itself has to work.
status=0
SOCRATES_UPDATE_TRIGGER=install "$LIBEXEC/update.sh" --foreground || status=$?
systemctl start socrates-update.timer

[ -x "$BIN" ] && systemctl is-active -q socrates ||
	die "Socrates is not running - see the output above and journalctl -u socrates"

say "Done"
[ "$status" = 0 ] || echo "Some tools could not be installed (above); the nightly update tries them again, or run: sudo $LIBEXEC/update.sh"
echo "Socrates is running: systemctl status socrates"
echo "It updates itself and its tools every night: systemctl list-timers socrates-update.timer"
# The first steps only for a first install: one that is already connected
# and signed in has nothing left to do.
if [ "$(health_json | json_field mode)" = connect ]; then
	cat <<EOF

1. Open it. From your own computer:
     ssh -L $port:localhost:$port root@$(hostname -f 2>/dev/null || hostname)
   then http://localhost:$port - connect the S3 bucket and choose a password.
2. Sign the agents in, once, in a terminal here or in a Shell session:
     claude      codex login --device-auth      opencode auth login
3. Remote access: Admin -> Cloudflare tunnel.
EOF
fi
