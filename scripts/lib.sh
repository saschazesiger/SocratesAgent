# Shared by scripts/install.sh, scripts/update.sh and scripts/deps.sh. It is
# sourced, never run. It holds no state of its own beyond the variables below,
# which /etc/socrates.env may override.
#
# Everything here is written to survive being interrupted at any point: files
# are downloaded to a temporary name and renamed, tools are unpacked into a
# directory of their own version and switched with a symlink that is replaced
# in one rename, and nothing a running session uses is ever deleted under it.

ENV_FILE=${SOCRATES_ENV_FILE:-/etc/socrates.env}

# env_value NAME prints NAME's value from /etc/socrates.env, the way systemd
# reads it: the last assignment wins, an `export ` in front and one pair of
# quotes around the value are allowed.
env_value() {
	[ -r "$ENV_FILE" ] || return 0
	sed -n -E "s/^[[:space:]]*(export[[:space:]]+)?$1=//p" "$ENV_FILE" | tail -1 |
		sed -E 's/^"(.*)"$/\1/; s/^'"'"'(.*)'"'"'$/\1/'
}

# setting NAME DEFAULT: the environment first (so a one-off run can override
# it), then /etc/socrates.env, then the default.
setting() {
	local v=${!1:-}
	[ -n "$v" ] || v=$(env_value "$1")
	printf '%s' "${v:-$2}"
}

SOCRATES_HOME=$(setting SOCRATES_HOME /root)
DATA=$(setting SOCRATES_DATA_DIR /var/lib/socrates)
OPT=/opt/socrates
SRC=$OPT/src
LIBEXEC=/usr/local/lib/socrates
BIN=/usr/local/bin/socrates
CACHE=/var/cache/socrates
LOCK=/run/lock/socrates-update.lock
STATE=$DATA/update-state
UNIT_DIR=/etc/systemd/system
LOCAL_BIN=$SOCRATES_HOME/.local/bin
DEFAULT_REPO=https://github.com/saschazesiger/SocratesAgent.git

# Every program the service, its sessions and these scripts look for, in the
# order the service's PATH has them. Kept equal to deploy/socrates.service.
SOCRATES_PATH=$LOCAL_BIN:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/snap/bin
export PATH=$SOCRATES_PATH HOME=$SOCRATES_HOME LANG=C.UTF-8 LC_ALL=C.UTF-8
# Output that is parsed (apt-cache's "Candidate:", git's) has to be English,
# whatever locale the terminal that ran this has.
unset LANGUAGE
# Everything here is installed for root's own HOME. Vendor installers that
# see sudo refuse to run (Claude Code's), or install for the user behind it.
unset SUDO_USER SUDO_UID SUDO_GID SUDO_COMMAND

say() { printf '\n== %s\n' "$*"; }
info() { printf '   %s\n' "$*"; }
warn() { printf 'warning: %s\n' "$*" >&2; }
die() { printf '%s: %s\n' "${0##*/}" "$*" >&2; exit 1; }

case $(uname -m) in
x86_64 | amd64) ARCH=amd64 NODE_ARCH=x64 ;;
aarch64 | arm64) ARCH=arm64 NODE_ARCH=arm64 ;;
*) ARCH= NODE_ARCH= ;;
esac

# retry N CMD...: runs CMD up to N times, waiting 5, 10, 20... seconds between
# tries. Networks fail, mirrors hiccup and package locks get held; one bad
# minute should not cost a day's update.
retry() {
	local n=$1 i=1 wait=5
	shift
	while :; do
		"$@" && return 0
		[ "$i" -ge "$n" ] && return 1
		warn "attempt $i of $n failed: $* - trying again in ${wait}s"
		sleep "$wait"
		i=$((i + 1)) wait=$((wait * 2))
	done
}

# fetch URL FILE downloads to FILE.part and renames it into place only when it
# is whole. curl retries by itself on any error, and every attempt is bounded,
# so a stalled mirror cannot hang an unattended update.
# A transfer slower than 10 kB/s for a minute is a dead one and is retried.
fetch() {
	curl -fsSL --proto '=https' --tlsv1.2 --connect-timeout 20 --max-time 1200 --speed-limit 10240 --speed-time 60 \
		--retry 4 --retry-delay 5 --retry-all-errors -o "$2.part" "$1" &&
		mv -f "$2.part" "$2"
}

# fetch_text URL prints a small document (a version list, a JSON pointer).
fetch_text() {
	curl -fsSL --proto '=https' --tlsv1.2 --connect-timeout 20 --max-time 60 \
		--retry 4 --retry-delay 5 --retry-all-errors "$1"
}

# latest_github_tag OWNER/REPO prints the tag GitHub's "latest release"
# redirect points at, without the API and its 60-requests-an-hour limit.
latest_github_tag() {
	local url
	url=$(curl -fsSLI --proto '=https' --connect-timeout 20 --max-time 60 --retry 4 --retry-all-errors \
		-o /dev/null -w '%{url_effective}' "https://github.com/$1/releases/latest") || return 1
	case $url in */tag/*) printf '%s\n' "${url##*/tag/}" ;; *) return 1 ;; esac
}

sha256_of() { sha256sum "$1" | cut -d' ' -f1; }

# version_ge A B: is version A at least B? Leading v and go are ignored.
version_ge() {
	local a=${1#v} b=${2#v}
	a=${a#go} b=${b#go}
	[ "$(printf '%s\n%s\n' "$b" "$a" | sort -V | head -1)" = "$b" ]
}

# first_line CMD... prints the first line CMD writes, without the SIGPIPE a
# `| head -1` can turn into a failure under pipefail. A program asked for its
# version gets a minute to say it: one that hangs must not hang the update.
first_line() {
	local out
	out=$(timeout 60 "$@" 2>/dev/null </dev/null) || return 1
	printf '%s\n' "${out%%$'\n'*}"
}

# swap_link TARGET LINK points LINK at TARGET in one rename, so there is no
# moment at which LINK is missing or half-written.
swap_link() {
	local tmp
	tmp="$(dirname "$2")/.$(basename "$2").$$"
	ln -sfn "$1" "$tmp" && mv -Tf "$tmp" "$2"
}

# ours LINK: is LINK free to be set by these scripts - missing, or a symlink
# into /opt/socrates (or the old /opt/code-server)? A program somebody put
# there themselves is left alone.
ours() {
	[ -e "$1" ] || [ -L "$1" ] || return 0
	[ -L "$1" ] || return 1
	case $(readlink "$1") in "$OPT"/* | /opt/code-server/*) return 0 ;; esac
	return 1
}

# link_bin TARGET NAME makes /usr/local/bin/NAME point at TARGET, if it is ours
# to set.
link_bin() {
	if ours "/usr/local/bin/$2"; then
		swap_link "$1" "/usr/local/bin/$2"
	elif [ "$(readlink -f "/usr/local/bin/$2")" != "$(readlink -f "$1")" ]; then
		warn "/usr/local/bin/$2 is not managed by Socrates, so it was left as it is"
	fi
}

# in_use DIR: is a running process executing a program from under DIR, or
# working in it? Such a directory is never deleted: the process would lose
# the files it still has to load.
in_use() {
	local dir p target
	dir=$(readlink -f "$1") || return 1
	for p in /proc/[0-9]*; do
		for target in "$(readlink "$p/exe" 2>/dev/null)" "$(readlink "$p/cwd" 2>/dev/null)"; do
			case $target in "$dir" | "$dir"/*) return 0 ;; esac
		done
	done
	return 1
}

# prune DIR KEEP: removes the versions in DIR (one directory each, beside the
# `current` link) beyond the KEEP newest, except the current one and any a
# process still runs from.
prune() {
	local dir=$1 keep=$2 current v n=0
	[ -d "$dir" ] || return 0
	# By name: `current` holds the bare version (swap_link), and a path
	# resolved through a symlinked /opt would never equal "$dir/$v".
	current=$(readlink "$dir/current" 2>/dev/null || true)
	current=${current##*/}
	# Unfinished unpacks of an interrupted run, and broken copies that were
	# moved aside once nothing runs from them any more.
	find "$dir" -mindepth 1 -maxdepth 1 -name '.*.tmp*' -mmin +60 -exec rm -rf {} + 2>/dev/null || true
	for v in "$dir"/.*.broken.*; do
		[ -d "$v" ] && ! in_use "$v" && rm -rf "$v"
	done
	for v in $(find "$dir" -mindepth 1 -maxdepth 1 -type d ! -name '.*' -printf '%f\n' | sort -rV); do
		n=$((n + 1))
		[ "$n" -le "$keep" ] && continue
		[ "$v" = "$current" ] && continue
		in_use "$dir/$v" && continue
		rm -rf "${dir:?}/$v"
	done
}

# free_mb PATH: megabytes free on PATH's file system.
free_mb() { df -Pm "$1" 2>/dev/null | awk 'NR==2 {print $4}'; }

# json_str VALUE prints VALUE as a JSON string.
json_str() {
	local s=$1
	s=${s//\\/\\\\}
	s=${s//\"/\\\"}
	s=${s//$'\n'/\\n}
	s=${s//$'\r'/\\r}
	s=${s//$'\t'/\\t}
	s=$(printf '%s' "$s" | tr -d '\000-\010\013\014\016-\037')
	printf '"%s"' "$s"
}

# write_atomic FILE writes stdin to FILE through a temporary file, so a reader
# never sees half of it.
write_atomic() {
	local tmp="$1.tmp.$$"
	cat >"$tmp" && chmod 644 "$tmp" && mv -f "$tmp" "$1"
}

# apt_get runs apt-get without questions, waiting out a lock that
# unattended-upgrades holds after a boot instead of failing on it.
apt_get() {
	DEBIAN_FRONTEND=noninteractive apt-get -q -o DPkg::Lock::Timeout=900 -o Acquire::Retries=3 \
		-o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold "$@"
}

# installed_pkg: is the package installed - held or not.
installed_pkg() { [ "$(dpkg-query -W -f='${db:Status-Status}' "$1" 2>/dev/null)" = installed ]; }

# health_json prints what /api/health answers, or fails.
health_url() {
	local addr host port
	addr=$(setting SOCRATES_ADDR 127.0.0.1:8080)
	host=${addr%:*} port=${addr##*:}
	case $host in "" | 0.0.0.0 | "[::]" | "::") host=127.0.0.1 ;; esac
	printf 'http://%s:%s/api/health' "$host" "$port"
}
health_json() { curl -fsS --max-time 5 "$(health_url)" 2>/dev/null; }
json_field() { sed -n -E "s/.*\"$1\"[[:space:]]*:[[:space:]]*\"([^\"]*)\".*/\1/p" | head -1; }
