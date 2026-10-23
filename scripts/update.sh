#!/usr/bin/env bash
# Update Socrates and every tool it runs on, as root:
#
#   sudo scripts/update.sh              all of it, now
#   sudo scripts/update.sh --deps       only the tools: Go, Node, Claude Code,
#                                       Codex, OpenCode, code-server,
#                                       cloudflared, tmux and the packages
#   sudo scripts/update.sh --socrates   only Socrates
#   sudo scripts/update.sh --repair     install what is missing or broken and
#                                       update nothing
#   sudo scripts/update.sh --no-pull    build the source as it is in
#                                       /opt/socrates/src, without fetching
#   sudo scripts/update.sh --from=DIR   build Socrates from the working tree
#                                       in DIR as it is - a checkout being
#                                       worked on; the next nightly update
#                                       goes back to the repository's newest
#                                       commit unless SOCRATES_AUTO_UPDATE=deps
#
# socrates-update.timer runs it every night and after every boot with --auto,
# which does what SOCRATES_AUTO_UPDATE in /etc/socrates.env says: all (the
# default), deps (the tools, not Socrates) or off (repair only).
#
# The run happens in a systemd unit of its own and this terminal only shows
# its output, so it finishes even when the terminal goes away - which is what
# makes it safe to run from inside a Socrates session. The full output of the
# last run is in <data>/updates.log, a summary in <data>/updates.json, and the
# admin page shows both.
#
# Sessions keep running through an update: they live in socrates-tmux.scope,
# not in the service. A new Socrates is built from the newest commit, started
# once on a scratch directory, and only then swapped in; if it does not answer
# /api/health with its own version, the previous binary and database are put
# back, and that commit is not tried again.
set -euo pipefail

HERE=$(cd "$(dirname "$(readlink -f "$0")")" && pwd)
. "$HERE/lib.sh"
. "$HERE/deps.sh"

deps= socrates= mode=update pull=1 foreground= auto= from= trigger=${SOCRATES_UPDATE_TRIGGER:-manual}
args=()
for arg in "$@"; do
	args+=("$arg")
	case $arg in
	--from=*)
		from=$(readlink -f "${arg#--from=}") && [ -f "$from/go.mod" ] || die "${arg#--from=} is not a Socrates source tree"
		socrates=1
		# The unit the run moves into starts in /, where a relative path
		# means something else.
		args[${#args[@]} - 1]="--from=$from"
		;;
	--deps | --agents) deps=1 ;;
	--socrates) socrates=1 ;;
	--repair) mode=repair ;;
	--no-pull) pull= ;;
	--auto) auto=1 ;;
	--foreground) foreground=1 ;;
	--trigger=*) trigger=${arg#--trigger=} ;;
	-h | --help) awk 'NR > 1 && /^#/ {sub(/^# ?/, ""); print; next} NR > 1 {exit}' "$0"; exit 0 ;;
	*) die "$arg is not an option (see --help)" ;;
	esac
done
[ -n "$deps$socrates" ] || deps=1 socrates=1
[ "$(id -u)" = 0 ] || die "run it as root: sudo $0"
[ -n "$ARCH" ] || die "$(uname -m) is not a supported architecture"

if [ -n "$auto" ]; then
	case $(setting SOCRATES_AUTO_UPDATE all) in
	all) ;;
	deps) socrates= ;;
	off) mode=repair ;;
	*) warn "SOCRATES_AUTO_UPDATE is all, deps or off; doing all" ;;
	esac
fi

# Out of this terminal and into a unit of its own. The unit is what keeps the
# update alive when this shell is a Socrates session whose service is about
# to restart, or an SSH connection that drops.
if [ -z "${SOCRATES_UPDATE_DETACHED:-}" ] && [ -z "$foreground" ] && [ -d /run/systemd/system ]; then
	unit=socrates-update-manual
	# A oneshot is "activating" for the whole of its run, which is-active
	# does not count as active.
	for u in socrates-update.service "$unit.service"; do
		case $(systemctl show -p ActiveState --value "$u" 2>/dev/null) in
		active | activating | deactivating | reloading)
			echo "An update is already running. Its output: journalctl -fu socrates-update -u $unit"
			exit 75
			;;
		esac
	done
	systemctl reset-failed "$unit.service" >/dev/null 2>&1 || true
	journalctl -fqn0 -o cat -u "$unit.service" &
	follow=$!
	set +e
	systemd-run --unit="$unit" --collect --quiet --wait --service-type=oneshot \
		-p TimeoutStartSec=3h -p Nice=5 \
		--setenv=SOCRATES_UPDATE_DETACHED=1 --setenv=SOCRATES_UPDATE_TRIGGER="$trigger" \
		"$HERE/update.sh" "${args[@]}"
	status=$?
	set -e
	sleep 1
	kill "$follow" 2>/dev/null || true
	wait "$follow" 2>/dev/null || true
	exit $status
fi

# One update at a time: the timer, the admin page's button and a terminal can
# all start one. A second waits for nothing - it says so and leaves.
mkdir -p /run/lock
exec 9>"$LOCK"
if ! flock -n 9; then
	# The admin page's label was for a run that is not going to happen; the
	# one going on is the run that was asked for.
	rm -f "$STATE/next-trigger"
	echo "Another update is running; this one is not needed."
	exit 0
fi

mkdir -p "$DATA" "$STATE" "$CACHE"
chmod 700 "$DATA"
# The admin page names who asked for the run when it was the one that did.
if [ -f "$STATE/next-trigger" ]; then
	trigger=$(head -c 32 "$STATE/next-trigger" | tr -cd 'a-z')
	rm -f "$STATE/next-trigger"
fi
# The timer also fires ten minutes after a boot, and at once when it is first
# started on a machine that has been up longer than that - right after
# install.sh has done a whole run. A run that completed within the hour is
# enough.
if [ "$trigger" = timer ] && [ -n "$(find "$STATE/last-complete" -mmin -60 2>/dev/null)" ]; then
	echo "Socrates and its tools were brought up to date less than an hour ago."
	exit 0
fi
RESULTS=$(mktemp)
STARTED=$(date -u +%Y-%m-%dT%H:%M:%SZ)
LOG=$DATA/updates.log
: >"$LOG"
chmod 644 "$LOG"
# tee without the lock's descriptor, and waited for at the end so the last
# lines reach the log before systemd ends the unit.
exec > >(exec tee -a "$LOG" 9>&-) 2>&1
TEE_PID=$!

# write_status STATE writes <data>/updates.json, the summary the admin page
# reads. Its shape is documented in internal/server/updates.go.
write_status() {
	local state=$1 finished= ok=true first=1 name version previous status error detail
	[ "$state" = running ] || finished=$(date -u +%Y-%m-%dT%H:%M:%SZ)
	grep -q $'\037failed\037' "$RESULTS" 2>/dev/null && ok=false
	[ "$state" = failed ] && ok=false
	{
		printf '{"schema":1,"state":%s,"trigger":%s,"started_at":%s,"finished_at":%s,"ok":%s,"components":[' \
			"$(json_str "$state")" "$(json_str "$trigger")" "$(json_str "$STARTED")" "$(json_str "$finished")" "$ok"
		while IFS=$'\037' read -r name version previous status error detail; do
			[ -n "$first" ] || printf ','
			first=
			printf '{"name":%s,"version":%s,"previous":%s,"status":%s,"error":%s,"detail":%s}' \
				"$(json_str "$name")" "$(json_str "$version")" "$(json_str "$previous")" \
				"$(json_str "$status")" "$(json_str "$error")" "$(json_str "$detail")"
		done <"$RESULTS"
		printf ']}\n'
	} | write_atomic "$DATA/updates.json"
}

finished=
on_exit() {
	local rc=$?
	# A run that stopped in the middle of swapping Socrates must not leave
	# the service down.
	if ! systemctl is-active -q socrates 2>/dev/null && [ -f "$UNIT_DIR/socrates.service" ] && [ -x "$BIN" ]; then
		systemctl reset-failed socrates >/dev/null 2>&1 || true
		systemctl start socrates || true
	fi
	[ -n "$finished" ] || write_status failed
	rm -f "$RESULTS"
	exec >&- 2>&-
	wait "$TEE_PID" 2>/dev/null || true
	exit $rc
}
trap on_exit EXIT
write_status running

say "Socrates update ($trigger, $mode) on $(hostname) at $STARTED"

# run_dep NAME runs dep_NAME in a subshell of its own, so a tool that fails -
# however it fails - is recorded and the next one still runs.
run_dep() {
	local name=$1 rc=0 lines
	say "$name"
	lines=$(wc -l <"$RESULTS")
	# Without the lock's descriptor: a process an installer leaves behind
	# must not hold the lock, or every later run would think one is going.
	# Run as a plain command, not in an if or ||: there bash ignores set -e
	# inside the subshell too, and a step that failed would go on.
	set +e
	(set -euo pipefail; "dep_$name" "$mode") 9>&-
	rc=$?
	set -e
	if [ "$(wc -l <"$RESULTS")" = "$lines" ]; then
		record "${name//_/-}" "" "" failed "stopped with exit status $rc"
	fi
	write_status running
	return 0
}

if [ -n "$deps" ]; then
	if [ "$(free_mb "$OPT" 2>/dev/null || free_mb /)" -lt 1024 ] 2>/dev/null; then
		mode=repair
		warn "less than 1 GB free: only repairing, nothing new is downloaded"
	fi
	mkdir -p "$OPT"
	for dep in "${DEPS[@]}"; do run_dep "$dep"; done
fi

# --- Socrates itself -------------------------------------------------------

GO=$OPT/go/current/bin/go
export CGO_ENABLED=0 GOTOOLCHAIN=local GOFLAGS=-mod=readonly \
	GOCACHE=$CACHE/go-build GOMODCACHE=$CACHE/gomod GOPATH=$CACHE/gopath
# git's background gc would outlive the run holding its files open.
git_src() { git -c safe.directory="$SRC" -c gc.autoDetach=false -c maintenance.autoDetach=false -C "$SRC" "$@"; }

installed_version() { timeout 30 "$BIN" -version 2>/dev/null || true; }

# wait_healthy VERSION [SECONDS]: does the service answer /api/health with
# VERSION? The listener opens only once the database is migrated and the
# sessions reconciled; a service that keeps restarting is given up on early.
wait_healthy() {
	local want=$1 limit=${2:-420} start restarts body
	start=$(date +%s)
	restarts=$(systemctl show -p NRestarts --value socrates 2>/dev/null || echo 0)
	while [ $(($(date +%s) - start)) -lt "$limit" ]; do
		body=$(health_json || true)
		if [ -n "$body" ] && [ "$(json_field version <<<"$body")" = "$want" ]; then
			return 0
		fi
		if [ $(($(systemctl show -p NRestarts --value socrates 2>/dev/null || echo 0) - restarts)) -ge 3 ]; then
			warn "socrates.service restarted three times while starting"
			return 1
		fi
		sleep 2
	done
	return 1
}

# smoke BINARY VERSION starts BINARY on a scratch data directory and a port
# of its own, and waits for it to answer with VERSION. A build that cannot
# get as far as serving never reaches the service.
smoke() {
	local bin=$1 want=$2 dir port pid body ok=1 i
	dir=$(mktemp -d)
	for port in $(shuf -i 20000-29999 -n 20); do
		ss -Htln "sport = :$port" 2>/dev/null | grep -q . || break
	done
	env -i PATH="$PATH" HOME="$dir" LANG=C.UTF-8 SOCRATES_DATA_DIR="$dir/data" SOCRATES_ADDR="127.0.0.1:$port" \
		ionice -c2 nice -n0 "$bin" >"$dir/out.log" 2>&1 9>&- &
	pid=$!
	for i in $(seq 90); do
		body=$(curl -fsS --max-time 3 "http://127.0.0.1:$port/api/health" 2>/dev/null || true)
		if [ "$(json_field version <<<"$body")" = "$want" ]; then ok=0; break; fi
		kill -0 "$pid" 2>/dev/null || break
		sleep 1
	done
	# A build that does not stop when asked is not one to swap in either.
	kill "$pid" 2>/dev/null || true
	for i in $(seq 10); do kill -0 "$pid" 2>/dev/null || break; sleep 1; done
	if kill -0 "$pid" 2>/dev/null; then
		kill -9 "$pid" 2>/dev/null || true
		warn "the new build did not stop on SIGTERM"
		ok=1
	fi
	wait "$pid" 2>/dev/null || true
	if [ "$ok" != 0 ]; then
		warn "the new build did not answer on a scratch directory. Its output:"
		tail -20 "$dir/out.log" >&2 || true
	fi
	rm -rf "$dir"
	return $ok
}

UNITS=(socrates.service socrates-update.service socrates-update.timer)

# install_units copies the unit files of the source into systemd, when they
# differ, and makes systemd read them again. The ones they replace are kept
# in $STATE/units.previous for a rollback. A timer somebody switched off
# stays off.
install_units() {
	local from=$1 f changed='' timer_on=''
	systemctl is-enabled -q socrates-update.timer 2>/dev/null && timer_on=1
	rm -rf "$STATE/units.previous" && mkdir -p "$STATE/units.previous"
	for f in "${UNITS[@]}"; do
		[ -f "$UNIT_DIR/$f" ] && cp -p "$UNIT_DIR/$f" "$STATE/units.previous/"
		[ -f "$from/deploy/$f" ] || continue
		cmp -s "$from/deploy/$f" "$UNIT_DIR/$f" && continue
		install -m 644 "$from/deploy/$f" "$UNIT_DIR/.$f.new"
		mv -f "$UNIT_DIR/.$f.new" "$UNIT_DIR/$f"
		changed=1
	done
	if [ -n "$changed" ]; then
		systemctl daemon-reload
		systemctl enable socrates.service >/dev/null 2>&1 || true
		if [ -n "$timer_on" ]; then
			systemctl enable socrates-update.timer >/dev/null 2>&1 || true
			systemctl restart socrates-update.timer >/dev/null 2>&1 || true
		fi
	fi
	return 0
}

# restore_units puts back what install_units replaced.
restore_units() {
	local f changed=''
	for f in "${UNITS[@]}"; do
		[ -f "$STATE/units.previous/$f" ] || continue
		cmp -s "$STATE/units.previous/$f" "$UNIT_DIR/$f" && continue
		cp -p "$STATE/units.previous/$f" "$UNIT_DIR/$f"
		changed=1
	done
	[ -z "$changed" ] || systemctl daemon-reload
}

# install_scripts puts the source's scripts where the timer runs them from. A
# broken updater would be the one thing no later update could fix, so the new
# set is staged beside the old, has to parse and to answer --help - which
# sources all of it - and only then replaces it, file by file by rename. The
# set it replaces is kept in previous/.
install_scripts() {
	local from=$1 f stage=$LIBEXEC.new
	rm -rf "$stage" && mkdir -p "$stage" || return 1
	for f in lib.sh deps.sh update.sh install.sh; do
		install -m 755 "$from/scripts/$f" "$stage/$f" && bash -n "$stage/$f" ||
			{ warn "scripts/$f does not parse; the installed updater stays"; rm -rf "$stage"; return 1; }
	done
	if ! SOCRATES_UPDATE_DETACHED=1 bash "$stage/update.sh" --help >/dev/null 2>&1 9>&-; then
		warn "the new update.sh does not run; the installed updater stays"
		rm -rf "$stage"
		return 1
	fi
	mkdir -p "$LIBEXEC/previous"
	for f in lib.sh deps.sh update.sh install.sh; do
		[ -f "$LIBEXEC/$f" ] && cp -p "$LIBEXEC/$f" "$LIBEXEC/previous/$f"
		mv -f "$stage/$f" "$LIBEXEC/$f"
	done
	rm -rf "$stage"
}

# mark_bad COMMIT: that commit is not built again. A working tree (--from)
# is never marked: the next try is of whatever it holds then.
mark_bad() {
	[ -n "$from" ] || echo "$1" >>"$STATE/bad-commits"
}

# rollback VERSION: the previous binary, database and unit files back, and
# the service started on them.
rollback() {
	systemctl stop socrates 2>/dev/null || true
	restore_units
	if [ -x "$BIN.previous" ]; then
		cp -p "$BIN.previous" "$BIN.rollback" && mv -f "$BIN.rollback" "$BIN"
	fi
	# The log of the new build's writes goes only with a whole database to put
	# back in its place.
	if [ -f "$STATE/backup/socrates.db" ]; then
		rm -f "$DATA/socrates.db-wal" "$DATA/socrates.db-shm"
		cp -p "$STATE/backup"/* "$DATA/"
	fi
	systemctl reset-failed socrates >/dev/null 2>&1 || true
	systemctl start socrates || true
}

# recover_swap finishes what an interrupted run left: the machine went down,
# or the run was stopped, between swapping a new Socrates in and seeing it
# healthy. The binary in place is then unproven and the previous one and its
# database are the ones known to work, so they are not overwritten by this
# run's swap; the unproven one is kept if it is healthy, and rolled back
# otherwise.
recover_swap() {
	local pending version
	pending=$(cat "$STATE/swap-pending" 2>/dev/null || true)
	[ -n "$pending" ] || return 0
	version=$(installed_version)
	say "The last update stopped part-way: checking Socrates $version"
	if [ -n "$version" ] && wait_healthy "$version" 300; then
		echo "$pending" >"$STATE/socrates.commit"
		info "it answers; keeping it"
	else
		warn "it does not answer; going back to the previous build"
		rollback
		mark_bad "$pending"
	fi
	rm -f "$STATE/swap-pending"
}

# clone_src makes /opt/socrates/src afresh. A clone that failed half-way
# leaves a directory the next try would refuse, so each try starts clean.
clone_src() {
	rm -rf "$SRC.tmp"
	git clone --quiet --branch "$1" "$2" "$SRC.tmp" && mv "$SRC.tmp" "$SRC"
}

# fetch_src brings refs/remotes/origin/BRANCH up to date, and says why not
# when it cannot: 0 fetched, 2 the branch is not there, 3 the repository
# cannot be reached, 4 the local clone is broken and could not be remade.
fetch_src() {
	local repo=$1 branch=$2 rc
	retry 3 git_src fetch --quiet --prune --tags --force origin "+refs/heads/$branch:refs/remotes/origin/$branch" && return 0
	rc=0
	timeout 120 git ls-remote --exit-code "$repo" "refs/heads/$branch" >/dev/null 2>&1 || rc=$?
	case $rc in
	0)
		# The repository answers and has the branch: it is this clone.
		warn "the clone in $SRC is broken; cloning it again"
		mv "$SRC" "$SRC.broken.$(date +%s)" && clone_src "$branch" "$repo" &&
			git_src fetch --quiet --tags --force origin "+refs/heads/$branch:refs/remotes/origin/$branch" && return 0
		return 4
		;;
	2) return 2 ;;
	*) return 3 ;;
	esac
}

update_socrates() {
	local repo branch target installed_commit before version backup build=$SRC rc out keep_previous=''
	repo=$(setting SOCRATES_REPO "$DEFAULT_REPO")
	branch=$(setting SOCRATES_BRANCH main)

	if ! [ -x "$GO" ]; then
		record socrates "$(installed_version)" "" failed "there is no Go to build it with"
		return 1
	fi
	# Only this run uses the clone - the lock says so - so a lock file in it
	# is one a killed run left, and would fail every git command from now on.
	[ -d "$SRC/.git" ] && find "$SRC/.git" -name '*.lock' -type f -delete 2>/dev/null
	recover_swap
	before=$(installed_version)
	installed_commit=$(cat "$STATE/socrates.commit" 2>/dev/null || true)

	if [ -n "$from" ]; then
		build=$from
		version=$(git -c safe.directory="$from" -C "$from" describe --tags --always --dirty 2>/dev/null || echo local)
		target=local:$version
	else
		if ! [ -d "$SRC/.git" ] && ! retry 3 clone_src "$branch" "$repo"; then
			rm -rf "$SRC.tmp"
			record socrates "$before" "" failed "could not clone $repo"
			return 1
		fi
		if [ -n "$pull" ]; then
			[ "$(git_src remote get-url origin)" = "$repo" ] || git_src remote set-url origin "$repo"
			rc=0
			fetch_src "$repo" "$branch" || rc=$?
			case $rc in
			0) ;;
			2)
				record socrates "$before" "" failed "$repo has no branch $branch (SOCRATES_BRANCH)"
				return 1
				;;
			4)
				record socrates "$before" "" failed "the source clone in $SRC is broken and could not be made again"
				return 1
				;;
			*)
				if [ -n "$before" ]; then
					record socrates "$before" "" current "" "$repo could not be reached; keeping $before"
					return 0
				fi
				record socrates "" "" failed "could not fetch $repo"
				return 1
				;;
			esac
			target=$(git_src rev-parse "refs/remotes/origin/$branch")
		else
			target=$(git_src rev-parse HEAD)
		fi
	fi

	if [ -z "$from" ] && [ "$target" = "$installed_commit" ] && [ -n "$before" ]; then
		record socrates "$before" "" current ""
		return 0
	fi
	if [ "$mode" = repair ] && [ -n "$before" ]; then
		record socrates "$before" "" current "" "a newer commit is there; it is installed by the next update"
		return 0
	fi
	if [ -z "$from" ] && grep -qx "$target" "$STATE/bad-commits" 2>/dev/null; then
		record socrates "$before" "" skipped "" "commit ${target:0:12} failed before (it did not build or start); waiting for a newer one"
		return 0
	fi

	if [ -z "$from" ]; then
		if [ -n "$pull" ]; then
			git_src checkout --quiet --force --detach "$target"
			git_src clean -qfdx
		fi
		# What is built has to be what is recorded as installed afterwards.
		if [ "$(git_src rev-parse HEAD)" != "$target" ]; then
			record socrates "$before" "" failed "could not check out ${target:0:12}"
			return 1
		fi
		version=$(git_src describe --tags --always 2>/dev/null || echo "${target:0:12}")
	fi
	say "Build Socrates $version"
	if ! (cd "$build" && retry 3 "$GO" mod download); then
		record socrates "$before" "" failed "could not download the Go modules"
		return 1
	fi
	rc=0
	out=$(cd "$build" && "$GO" build -trimpath -buildvcs=false -ldflags "-s -w -X main.version=$version" -o "$BIN.new" . 2>&1) || rc=$?
	if [ "$rc" != 0 ]; then
		printf '%s\n' "$out"
		rm -f "$BIN.new"
		# Only a commit that cannot be built is marked, not a machine that
		# could not build it this time: a full disk, the OOM killer, a Go
		# older than go.mod asks for.
		if [ "$rc" -gt 128 ] || [ "$(free_mb "$CACHE")" -lt 1024 ] 2>/dev/null ||
			grep -qiE 'no space left|requires go >=|signal: killed|cannot allocate memory|out of memory' <<<"$out"; then
			record socrates "$before" "" failed "$version could not be built on this machine this time"
		else
			mark_bad "$target"
			record socrates "$before" "" failed "commit ${target:0:12} does not build"
		fi
		return 1
	fi
	# Twice before it counts against the commit: a loaded machine can be slow
	# to start anything.
	if ! smoke "$BIN.new" "$version" && ! smoke "$BIN.new" "$version"; then
		rm -f "$BIN.new"
		mark_bad "$target"
		record socrates "$before" "" failed "$version does not start"
		return 1
	fi

	say "Swap in Socrates $version"
	install_units "$build"
	# From here until it is healthy, the binary in place is unproven; a run
	# that stops in between leaves this for the next one (recover_swap).
	echo "$target" >"$STATE/swap-pending"
	# Stopped while the database is copied, so the copy is whole. The
	# sessions are not in the service and do not notice.
	systemctl stop socrates 2>/dev/null || true
	backup=$STATE/backup
	rm -rf "$backup.new" && mkdir -p "$backup.new"
	for f in socrates.db socrates.db-wal socrates.db-shm; do
		[ -f "$DATA/$f" ] || continue
		if ! cp -p "$DATA/$f" "$backup.new/"; then
			rm -rf "$backup.new" "$BIN.new" "$STATE/swap-pending"
			restore_units
			systemctl start socrates || true
			record socrates "$before" "" failed "could not back up the database; nothing was changed"
			return 1
		fi
	done
	rm -rf "$backup" && mv "$backup.new" "$backup"
	# Copies go to a temporary name and are renamed into place: the sessions'
	# journal sinks and hooks run this binary, and writing into a file that
	# is being executed fails ("Text file busy").
	if [ -x "$BIN" ]; then
		cp -p "$BIN" "$BIN.previous.tmp" && mv -f "$BIN.previous.tmp" "$BIN.previous"
	fi
	mv -f "$BIN.new" "$BIN"
	systemctl reset-failed socrates >/dev/null 2>&1 || true
	systemctl start socrates || true

	if wait_healthy "$version"; then
		# Twenty seconds more, against a build that answers once and then
		# falls over.
		sleep 20
		if wait_healthy "$version" 30; then
			echo "$target" >"$STATE/socrates.commit"
			rm -f "$STATE/swap-pending"
			install_scripts "$build" || true
			record socrates "$version" "$before" "$(outcome socrates "$before" "$version")" ""
			return 0
		fi
	fi

	say "Socrates $version is not healthy: back to ${before:-nothing}"
	journalctl -u socrates -n 30 --no-pager || true
	if ! [ -x "$BIN.previous" ] || [ -z "$before" ]; then
		rm -f "$STATE/swap-pending"
		record socrates "$version" "$before" failed "$version is not healthy and there is no previous build to go back to"
		return 1
	fi
	rollback
	rm -f "$STATE/swap-pending"
	if wait_healthy "$before"; then
		# The previous build works on the same machine, as it did
		# before, so it is this commit, and it is not tried again until
		# there is a newer one.
		mark_bad "$target"
		record socrates "$before" "$version" failed "$version did not answer its health check; rolled back to $before"
	else
		record socrates "$before" "$version" failed "neither $version nor $before answers as it did; see journalctl -u socrates"
	fi
	return 1
}

if [ -n "$socrates" ]; then
	say "socrates"
	set +e
	(set -euo pipefail; update_socrates) 9>&-
	rc=$?
	set -e
	if [ "$rc" != 0 ]; then
		grep -q $'^socrates\037' "$RESULTS" || record socrates "$(installed_version)" "" failed "the update stopped part-way"
	fi
	# A swap restarted the service already; a restart asked for by a tool
	# is then done.
	if awk -F'\037' '$1 == "socrates" && ($4 == "updated" || $4 == "installed") {f=1} END {exit !f}' "$RESULTS"; then
		rm -f "$STATE/restart-needed"
	fi
	write_status running
fi

# --- The service ----------------------------------------------------------

if [ -f "$UNIT_DIR/socrates.service" ] && [ -x "$BIN" ]; then
	if [ -f "$STATE/restart-needed" ]; then
		say "Restart Socrates for the updated tools"
		systemctl restart socrates 9>&- || true
		rm -f "$STATE/restart-needed"
	fi
	if ! systemctl is-active -q socrates; then
		say "Socrates was not running: starting it"
		systemctl reset-failed socrates >/dev/null 2>&1 || true
		systemctl start socrates 9>&- || true
	fi
	if ! wait_healthy "$(installed_version)" 420; then
		record service "$(installed_version)" "" failed "socrates.service does not answer $(health_url)"
	else
		info "Socrates $(installed_version) answers"
	fi
fi

finished=1
if grep -q $'\037failed\037' "$RESULTS"; then
	write_status 'done'
	say "Done, with failures: $(awk -F'\037' '$4 == "failed" {printf "%s%s", sep, $1; sep=", "}' "$RESULTS")"
	exit 1
fi
if [ -n "$deps" ] && [ -n "$socrates" ]; then touch "$STATE/last-complete"; fi
write_status 'done'
say "Done"
