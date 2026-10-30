#!/usr/bin/env bash
# qa-instance.sh - the test instance of Socrates that the device tests in
# .socrates/tests run against (.socrates/tests/README.md).
#
# It is a second Socrates on this machine, apart from the real one in every
# way that matters: its own system user without sudo (its Shell is reachable
# from the internet through its tunnel, guarded by its password only), its own
# data directory, its own port on loopback, its own systemd unit and its own
# copies of the agent CLIs. It is built from a clean export of a git ref, never
# from the working tree, so other people's unfinished changes are not tested.
#
#   qa-instance.sh install --hostname H --tunnel-token-file F [--ref REF]
#   qa-instance.sh update [--ref REF]      rebuild, copy the CLIs again, restart
#   qa-instance.sh reset                   delete every session, seed again, and
#                                          make the demo shop's four test runs again
#   qa-instance.sh status                  the service, the URL, the accounts
#   qa-instance.sh password                print the password (to sign in by hand)
#   qa-instance.sh uninstall               stop it and remove user, unit and data
#
# Secrets live in /etc/socrates-qa (root only), never where the instance's own
# user could read them: secrets.env is what the BrowserStack tool's secrets
# command prints (BROWSERSTACK_ACCOUNTS, QA_SOCRATES_PASSWORD). install takes
# BROWSERSTACK_ACCOUNTS (user:key,user:key,...) and, optionally,
# OPENROUTER_API_KEY from the environment.
#
# The demo shop (scripts/qa-demo-tests) is a test repository of the instance
# with a history of four test runs, worked by the Scripted agent for free, so
# that the device tests find realistic content on the Tests page.
set -euo pipefail

QA_USER=socrates-qa
QA_HOME=/var/lib/socrates-qa
QA_DATA=$QA_HOME/data
QA_ETC=/etc/socrates-qa
QA_PORT=8095
QA_ADDR=127.0.0.1:$QA_PORT
QA_LOCAL=http://$QA_ADDR
QA_UNIT=socrates-qa
UNIT_FILE=/etc/systemd/system/$QA_UNIT.service
REPO=$(cd "$(dirname "$(realpath "$0")")/.." && pwd)
BUILD_TMP=${TMPDIR_QA:-/root/.cache/socrates-qa-build}
DEMO_SHOP=$QA_HOME/work/demo-shop

say() { printf '\n== %s\n' "$*"; }
info() { printf '   %s\n' "$*"; }
die() { printf 'qa-instance: %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" = 0 ] || die "run it as root"

# instance_value NAME: a value install remembered in $QA_ETC/instance.env.
instance_value() {
	[ -r "$QA_ETC/instance.env" ] || return 0
	sed -n "s/^$1=//p" "$QA_ETC/instance.env" | tail -1
}

# secret NAME: a value of $QA_ETC/secrets.env.
secret() {
	[ -r "$QA_ETC/secrets.env" ] || return 0
	sed -n "s/^$1=//p" "$QA_ETC/secrets.env" | tail -1
}

# ------------------------------------------------------------------ build

# build REF compiles Socrates from a clean export of REF into the instance.
build() {
	local ref=$1 src version
	say "Building Socrates from $ref"
	git -C "$REPO" fetch --quiet origin 2>/dev/null || info "git fetch failed; building from what is here"
	version=$(git -C "$REPO" rev-parse --short=12 "$ref")
	rm -rf "$BUILD_TMP"
	mkdir -p "$BUILD_TMP/src"
	git -C "$REPO" archive "$ref" | tar -x -C "$BUILD_TMP/src"
	(cd "$BUILD_TMP/src" && TMPDIR=$BUILD_TMP go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$BUILD_TMP/socrates" .)
	install -d -o "$QA_USER" -g "$QA_USER" "$QA_HOME/bin"
	install -m 0755 -o "$QA_USER" -g "$QA_USER" "$BUILD_TMP/socrates" "$QA_HOME/bin/socrates.new"
	mv -f "$QA_HOME/bin/socrates.new" "$QA_HOME/bin/socrates"
	rm -rf "$BUILD_TMP"
	info "socrates $version"
}

# copy_clis puts copies of this machine's claude, codex, opencode and
# cloudflared into the instance user's ~/.local/bin: root's own installs sit
# under /root and the real Socrates' data directory, which no other user can
# read.
copy_clis() {
	say "Copying the agent CLIs"
	install -d -o "$QA_USER" -g "$QA_USER" "$QA_HOME/.local" "$QA_HOME/.local/bin"
	local name path
	for name in claude codex opencode cloudflared; do
		path=$(PATH=/root/.local/bin:/var/lib/socrates/bin:$PATH command -v "$name" || true)
		if [ -z "$path" ]; then
			info "$name: not installed here, skipped"
			continue
		fi
		install -m 0755 -o "$QA_USER" -g "$QA_USER" "$(readlink -f "$path")" "$QA_HOME/.local/bin/$name.new"
		mv -f "$QA_HOME/.local/bin/$name.new" "$QA_HOME/.local/bin/$name"
		info "$name: $("$QA_HOME/.local/bin/$name" --version 2>/dev/null | head -1)"
	done
	# Codex runs its tools through helpers it finds beside its own binary
	# (codex-code-mode-host, codex-exec-server): without them every command
	# and MCP call of a Codex chat fails to start.
	path=$(PATH=/root/.local/bin:/var/lib/socrates/bin:$PATH command -v codex || true)
	if [ -n "$path" ]; then
		local helper
		for helper in "$(dirname "$(readlink -f "$path")")"/codex-*; do
			[ -f "$helper" ] || continue
			install -m 0755 -o "$QA_USER" -g "$QA_USER" "$helper" "$QA_HOME/.local/bin/$(basename "$helper")"
		done
	fi
}

write_unit() {
	cat >"$UNIT_FILE" <<EOF
# The test instance of Socrates (scripts/qa-instance.sh in the Socrates
# repository). Not the real one: that is socrates.service.
[Unit]
Description=Socrates test instance
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=0

[Service]
User=$QA_USER
Group=$QA_USER
# Without INVOCATION_ID Socrates does not take itself for the service, and
# starts tmux and the agent hosts as its own children instead of in scopes of
# their own: systemd-run --scope needs root, which this user does not have. A
# restart of this unit therefore ends the instance's sessions too.
ExecStart=/usr/bin/env -u INVOCATION_ID $QA_HOME/bin/socrates -addr $QA_ADDR -data $QA_DATA
WorkingDirectory=$QA_HOME
Environment=HOME=$QA_HOME
Environment=LANG=C.UTF-8
Environment=TMPDIR=$QA_HOME/tmp
Environment=PATH=$QA_HOME/.local/bin:/usr/local/bin:/usr/bin:/bin
Environment=SOCRATES_WORKSPACE_ROOT=$QA_HOME/work
# The scripted agent: free and repeatable answers for the flows that need one
# (a plan, a limit, a crash, five questions at once).
Environment=SOCRATES_FAKE_AGENT=1
Restart=always
RestartSec=3
WatchdogSec=120
NotifyAccess=main
LimitNOFILE=65536:524288
OOMPolicy=continue
TimeoutStopSec=60

[Install]
WantedBy=multi-user.target
EOF
	systemctl daemon-reload
}

wait_healthy() {
	local i
	for i in $(seq 1 60); do
		if curl -fsS "$QA_LOCAL/api/health" >/dev/null 2>&1; then return 0; fi
		sleep 1
	done
	journalctl -u "$QA_UNIT" -n 30 --no-pager >&2 || true
	die "the instance did not answer on $QA_LOCAL"
}

# ------------------------------------------------------------------ the API

JAR=""
api_login() {
	JAR=$(mktemp -p "$QA_ETC" jar.XXXXXX)
	trap 'api_logout' EXIT
	local pw
	pw=$(secret QA_SOCRATES_PASSWORD)
	api POST /api/login "$(jq -n --arg p "$pw" '{password: $p}')" >/dev/null
}

api_logout() {
	[ -n "$JAR" ] || return 0
	curl -sS -o /dev/null -b "$JAR" -H "Origin: $QA_LOCAL" -X POST "$QA_LOCAL/api/logout" || true
	rm -f "$JAR"
	JAR=""
}

# api METHOD PATH [JSON]: one call to the instance, the body on stdout; a
# status of 400 or more stops the script with the answer.
api() {
	local method=$1 path=$2 body=${3:-} out code
	out=$(mktemp -p "$QA_ETC" out.XXXXXX)
	code=$(curl -sS -o "$out" -w '%{http_code}' -b "$JAR" -c "$JAR" -H "Origin: $QA_LOCAL" \
		-H 'Content-Type: application/json' -X "$method" ${body:+--data "$body"} "$QA_LOCAL$path")
	if [ "$code" -ge 400 ]; then
		cat "$out" >&2
		rm -f "$out"
		die "$method $path answered $code"
	fi
	cat "$out"
	rm -f "$out"
}

# ------------------------------------------------------------------ setup

# first_setup does what the setup page does: the password, the OpenRouter key
# (titles, status, dictation) and the named tunnel to the public hostname.
first_setup() {
	local host=$1 body
	say "Setting the instance up for https://$host"
	JAR=$(mktemp -p "$QA_ETC" jar.XXXXXX)
	trap 'api_logout' EXIT
	body=$(jq -n --arg p "$(secret QA_SOCRATES_PASSWORD)" --arg o "$(cat "$QA_ETC/openrouter.key" 2>/dev/null || true)" \
		--arg t "$(cat "$QA_ETC/tunnel.token")" --arg h "$host" \
		'{password: $p, openrouter_key: $o, tunnel: {enabled: true, mode: "token", token: $t, hostname: $h}}')
	api POST /api/setup "$body" | jq -r '"   tunnel: " + (if .tunnel_error == "" then "started" else .tunnel_error end)'
}

# configure gives the instance one account per agent, the cheapest model of
# each as its default, and the demo project as a preset directory.
configure() {
	say "Accounts, models and the demo project"
	local have
	have=$(api GET /api/accounts | jq -r '.accounts[].agent')
	grep -qx claude <<<"$have" || api POST /api/accounts '{"agent":"claude","kind":"subscription","label":"Claude QA"}' >/dev/null
	grep -qx codex <<<"$have" || api POST /api/accounts '{"agent":"codex","kind":"subscription","label":"Codex QA"}' >/dev/null
	grep -qx fake <<<"$have" || api POST /api/accounts '{"agent":"fake","kind":"subscription","label":"Scripted QA"}' >/dev/null
	if ! grep -qx opencode <<<"$have" && [ -s "$QA_ETC/openrouter.key" ]; then
		api POST /api/accounts "$(jq -n --arg k "$(cat "$QA_ETC/openrouter.key")" \
			'{agent: "opencode", kind: "api_key", label: "OpenRouter QA", key: $k, provider: "openrouter"}')" >/dev/null
	fi
	api PUT /api/settings "$(api GET /api/settings | jq --arg demo "$QA_HOME/work/demo" '{settings: (.settings
		| .harnesses.claude.default_model = "haiku" | .harnesses.claude.default_effort = "low"
		| .harnesses.codex.default_model = "gpt-6-luna" | .harnesses.codex.default_effort = "low"
		| .harnesses.opencode.default_model = "openrouter/deepseek/deepseek-v4-flash"
		| .workspace.presets = [{label: "demo", path: $demo}])}')" >/dev/null
	api GET /api/accounts | jq -r '.accounts[] | "   \(.label) (\(.agent)): \(.state)"'
}

# demo_project (re)makes the small git project the tests work in.
demo_project() {
	local dir=$QA_HOME/work/demo
	rm -rf "$dir"
	install -d -o "$QA_USER" -g "$QA_USER" "$dir" "$dir/notes"
	(
		cd / && sudo -u "$QA_USER" -H bash -euc '
			git config --global user.name "Socrates QA"
			git config --global user.email "qa@socrates.invalid"
			git config --global init.defaultBranch main
			cd "$1"
			git init -q
			printf "# Demo project\n\nA small project the device tests of Socrates work in. Agents may read and\nchange it; scripts/qa-instance.sh reset puts it back.\n" >README.md
			printf "def greet(name):\n    return f\"Hello, {name}!\"\n\n\nif __name__ == \"__main__\":\n    print(greet(\"Socrates\"))\n" >hello.py
			printf "# Notes\n\n- buy milk\n- answer mail\n" >notes/todo.md
			git add -A
			git commit -qm "Demo project"
		' _ "$dir"
	)
}

# seed starts the sessions every test may look at but never changes: their
# titles start with "Seed:" (.socrates/tests/TESTS.md). They run on the
# scripted agent and the Shell, so they cost nothing.
seed() {
	say "Seed sessions"
	local demo=$QA_HOME/work/demo id
	seed_chat() {
		id=$(api POST /api/chats "$(jq -n --arg t "$1" --arg w "$demo" --arg c "seed-$2" \
			'{client_id: $c, harness: "fake", model: "fake-small", workdir_mode: "preset", workdir: $w, title: $t}')" | jq -r '.session.id')
		api POST "/api/chats/$id/messages" "$(jq -n --arg c "seed-$2-m1" --arg m "$3" '{client_id: $c, text: $m}')" >/dev/null
		info "$1"
	}
	seed_chat "Seed: Markdown answer" md "/md"
	seed_chat "Seed: tools and a diff" tools "/tools"
	seed_chat "Seed: a plan waits" plan "/plan"
	seed_chat "Seed: task list" todo "/todo"
	api POST /api/sessions "$(jq -n --arg w "$demo" '{client_id: "seed-shell", harness: "shell", workdir_mode: "preset", workdir: $w, title: "Seed: Shell"}')" >/dev/null
	info "Seed: Shell"
}

# as_qa runs a command as the instance's user.
as_qa() { (cd / && sudo -u "$QA_USER" -H "$@"); }

# qa_git runs git in the demo shop as the instance's user.
qa_git() { as_qa git -C "$DEMO_SHOP" -c user.name="Socrates QA" -c user.email=qa@socrates.invalid "$@"; }

# demo_key is the key of the demo shop's test repository, empty when it is
# not on the instance's list.
demo_key() {
	api GET /api/tests/repos | jq -r --arg p "$DEMO_SHOP" '[.repos[] | select(.path == $p) | .key][0] // ""'
}

# demo_dir is the demo shop's folder in the instance's data: its key as
# Socrates makes it (testmode.RepoKey), whether it is listed or not.
demo_dir() {
	echo "$QA_DATA/tests/$(basename "$DEMO_SHOP")-$(printf '%s' "$DEMO_SHOP" | sha256sum | cut -c1-8)"
}

# demo_purge deletes the demo shop's test runs and then, with the instance
# stopped for a moment, its bug list: the runs demo_tests makes are runs 1 to
# 4 again (the tests' fake parameters count on the run numbers) and their
# bugs are new.
demo_purge() {
	local key id n=0 dir
	dir=$(demo_dir)
	[ -e "$dir" ] || return 0
	say "Deleting the demo shop's test runs"
	key=$(demo_key)
	if [ -z "$key" ] && [ -f "$DEMO_SHOP/.socrates/tests/TESTS.md" ]; then
		# Removed from the list on the Tests page: adding it again lists
		# its runs, which go with their sessions.
		key=$(api POST /api/tests/repos "$(jq -n --arg p "$DEMO_SHOP" '{path: $p}')" | jq -r '.key')
	fi
	if [ -n "$key" ]; then
		for id in $(api GET "/api/tests/repos/$key/runs" | jq -r '.runs[].id'); do
			api DELETE "/api/tests/runs/$id" >/dev/null
			n=$((n + 1))
		done
	fi
	info "$n deleted"
	# Their sessions are removed a moment later; every other session is
	# gone already (delete_sessions), so the chats' folders empty out.
	for _ in $(seq 1 60); do
		[ -n "$(ls -A "$QA_DATA/chats" 2>/dev/null)" ] || break
		sleep 0.5
	done
	api_logout
	systemctl stop "$QA_UNIT"
	rm -rf "${dir:?}"
	systemctl start "$QA_UNIT"
	wait_healthy
	api_login
	info "its bugs and run numbers are gone (the instance was restarted)"
}

# demo_shop (re)makes the demo shop's repository from scripts/qa-demo-tests
# of the commit the instance was built from (of this checkout when that is
# not known here), owned by the instance's user, with one commit.
demo_shop() {
	local version
	version=$(curl -fsS "$QA_LOCAL/api/health" | jq -r '.version // ""')
	rm -rf "$DEMO_SHOP"
	install -d -o "$QA_USER" -g "$QA_USER" "$DEMO_SHOP"
	if [ -n "$version" ] && git -C "$REPO" cat-file -e "$version:scripts/qa-demo-tests" 2>/dev/null; then
		git -C "$REPO" archive "$version:scripts/qa-demo-tests" | tar -x -C "$DEMO_SHOP"
	else
		cp -a "$REPO/scripts/qa-demo-tests/." "$DEMO_SHOP/"
	fi
	chown -R "$QA_USER:$QA_USER" "$DEMO_SHOP"
	qa_git init -q -b main
	qa_git add -A
	qa_git commit -qm "The demo shop and its test catalog"
}

# demo_commit is the commit made before demo run $1, if any
# (e2e/testsfake.mjs makes the same).
demo_commit() {
	case $1 in
	3) echo "Cart: recompute the total when the quantity changes" ;;
	4) echo "Search: rank by sales; profile: wrap long streets" ;;
	esac
}

# demo_wait waits for test run $1 to end and prints how it ended.
demo_wait() {
	local state=""
	for _ in $(seq 1 240); do
		state=$(api GET "/api/tests/runs/$1" | jq -r '.state')
		case $state in done | failed | stopped) break ;; esac
		sleep 0.5
	done
	echo "$state"
}

# demo_tests adds the demo shop as a test repository with every role on the
# Scripted agent and runs its catalog four times, one run after another: a
# history with an issue that stays, a cell fixed in run 3, a flaky one, a
# regression and a fix in run 4, a correcting review and bugs over the runs
# (scripts/qa-demo-tests/.socrates/tests/README.md).
demo_tests() {
	say "The demo shop's test runs (Scripted agent)"
	local key settings n id state msg began=$SECONDS
	demo_shop
	key=$(api POST /api/tests/repos "$(jq -n --arg p "$DEMO_SHOP" '{path: $p}')" | jq -r '.key')
	n=$(api GET "/api/tests/repos/$key/catalog" | jq '.errors | length')
	[ "$n" = 0 ] || die "the demo shop's catalog has $n error(s)"
	settings=$(api GET "/api/tests/repos/$key/settings" | jq '.settings
		| .cell = {harness: "fake", model: "fake-small", effort: "low"} | .reviewer = .cell | .analyst = .cell
		| .parallel = 8 | .review = true | .analysis = true')
	api PUT "/api/tests/repos/$key/settings" "$settings" >/dev/null
	for n in 1 2 3 4; do
		msg=$(demo_commit "$n")
		if [ -n "$msg" ]; then
			printf -- '- %s\n' "$msg" | as_qa tee -a "$DEMO_SHOP/CHANGELOG.md" >/dev/null
			qa_git commit -qam "$msg"
		fi
		id=$(api POST "/api/tests/repos/$key/runs" "$(jq -n --arg n "Demo run $n" '{name: $n}')" | jq -r '.id')
		state=$(demo_wait "$id")
		[ "$state" = "done" ] || die "demo run $n ($id) ended ${state:-in no state}"
		api GET "/api/tests/runs/$id" | jq -r '"   run \(.number): \(.counts.cells) cells, " +
			([.counts.verdicts | to_entries[] | "\(.value) \(.key)"] | join(", "))'
	done
	info "$key, $((SECONDS - began)) s"
}

# delete_sessions removes every session and chat of the instance.
delete_sessions() {
	say "Deleting every session"
	local id n=0
	for id in $(api GET /api/sessions | jq -r '.sessions[].id'); do
		api DELETE "/api/sessions/$id" >/dev/null
		n=$((n + 1))
	done
	info "$n deleted"
}

# ------------------------------------------------------------------ commands

cmd_install() {
	local host="" token_file="" ref=origin/main pw
	while [ $# -gt 0 ]; do
		case $1 in
		--hostname) host=$2; shift 2 ;;
		--tunnel-token-file) token_file=$2; shift 2 ;;
		--ref) ref=$2; shift 2 ;;
		*) die "install: unknown argument $1" ;;
		esac
	done
	[ -n "$host" ] && [ -s "$token_file" ] || die "install needs --hostname and --tunnel-token-file"
	[ -n "${BROWSERSTACK_ACCOUNTS:-}" ] || [ -n "$(secret BROWSERSTACK_ACCOUNTS)" ] ||
		die "set BROWSERSTACK_ACCOUNTS (user:key,user:key,...) for the first install"
	command -v jq >/dev/null || die "jq is needed"

	say "User, folders and secrets"
	id "$QA_USER" >/dev/null 2>&1 || useradd --system --home-dir "$QA_HOME" --create-home --shell /bin/bash --user-group "$QA_USER"
	chmod 0750 "$QA_HOME"
	install -d -o "$QA_USER" -g "$QA_USER" "$QA_HOME/tmp" "$QA_HOME/work" "$QA_DATA"
	install -d -m 0700 "$QA_ETC"
	(
		umask 077
		[ "$token_file" -ef "$QA_ETC/tunnel.token" ] || cp "$token_file" "$QA_ETC/tunnel.token"
		[ -z "${OPENROUTER_API_KEY:-}" ] || printf '%s' "$OPENROUTER_API_KEY" >"$QA_ETC/openrouter.key"
		pw=$(secret QA_SOCRATES_PASSWORD)
		[ -n "$pw" ] || pw=$(openssl rand -base64 30 | tr -d '/+=\n' | cut -c1-24)
		printf 'BROWSERSTACK_ACCOUNTS=%s\nQA_SOCRATES_PASSWORD=%s\n' "${BROWSERSTACK_ACCOUNTS:-$(secret BROWSERSTACK_ACCOUNTS)}" "$pw" >"$QA_ETC/secrets.env"
		printf 'HOSTNAME=%s\n' "$host" >"$QA_ETC/instance.env"
	)
	build "$ref"
	copy_clis
	write_unit
	demo_project
	systemctl enable --now "$QA_UNIT"
	wait_healthy
	if curl -fsS "$QA_LOCAL/api/state" | jq -e '.setup_required == false' >/dev/null 2>&1; then
		api_login
	else
		first_setup "$host"
	fi
	configure
	demo_purge
	seed
	demo_tests
	cmd_status_body
	cat <<EOT

Next: open https://$host/admin (password: $0 password), Accounts, and press
Sign in on "Claude QA" and "Codex QA" once. The tests use those logins.
EOT
}

cmd_update() {
	local ref=origin/main
	[ "${1:-}" = --ref ] && ref=$2
	build "$ref"
	copy_clis
	write_unit
	systemctl restart "$QA_UNIT"
	wait_healthy
	info "restarted"
}

cmd_reset() {
	wait_healthy
	api_login
	delete_sessions
	demo_purge
	demo_project
	seed
	demo_tests
}

cmd_status_body() {
	say "Status"
	info "service: $(systemctl is-active "$QA_UNIT" 2>/dev/null || true), version $(curl -fsS "$QA_LOCAL/api/health" 2>/dev/null | jq -r '.version // "-"')"
	info "public:  https://$(instance_value HOSTNAME) -> $(curl -s -o /dev/null -w '%{http_code}' "https://$(instance_value HOSTNAME)/login" || true)"
	[ -n "$JAR" ] || api_login
	api GET /api/accounts | jq -r '.accounts[] | "   \(.label) (\(.agent)): \(.state)"'
	info "sessions: $(api GET /api/sessions | jq '.sessions | length')"
	local key
	key=$(demo_key)
	if [ -z "$key" ]; then
		info "demo shop: not set up (reset makes it)"
		return 0
	fi
	local runs bugs
	runs=$(api GET "/api/tests/repos/$key/runs" | jq -r '.runs | "\(length) test run(s)" +
		(if length > 0 then ", the latest run \(max_by(.number).number) \(max_by(.number).state)" else "" end)')
	bugs=$(api GET "/api/tests/repos/$key/bugs" | jq '[.bugs[] | select(.status == "open")] | length')
	info "demo shop: $runs, $bugs open bug(s) ($DEMO_SHOP)"
}

cmd_uninstall() {
	systemctl disable --now "$QA_UNIT" 2>/dev/null || true
	rm -f "$UNIT_FILE"
	systemctl daemon-reload
	pkill -u "$QA_USER" 2>/dev/null || true
	userdel "$QA_USER" 2>/dev/null || true
	rm -rf "$QA_HOME" "$QA_ETC"
	info "removed; the Cloudflare tunnel and its DNS record are left as they are"
}

case ${1:-} in
install) shift; cmd_install "$@" ;;
update) shift; cmd_update "$@" ;;
reset) cmd_reset ;;
status) wait_healthy; cmd_status_body ;;
password) secret QA_SOCRATES_PASSWORD; echo ;;
uninstall) cmd_uninstall ;;
*) sed -n '2,29p' "$0" | sed 's/^# \{0,1\}//'; exit 2 ;;
esac
