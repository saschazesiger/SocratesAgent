# The tools Socrates runs on, one function each. Sourced by scripts/update.sh
# after scripts/lib.sh; never run on its own.
#
# Every dep_* function takes a mode:
#   update  bring it to the newest version
#   repair  install it when it is missing or does not run, and otherwise
#           leave it at the version it is
# and reports what it found with `record`. A function that fails returns
# non-zero and the caller goes on with the next one: one tool that cannot be
# reached must not hold back the others.
#
# Where each one lives:
#   apt packages    git, ripgrep, jq, curl... from the distribution
#   tmux            the distribution's, held (see dep_tmux)
#   Go              /opt/socrates/go/<version>, to build Socrates
#   Node            /opt/socrates/node/<version>, linked into /usr/local/bin
#   Claude Code     the native install: ~/.local/bin/claude
#   Codex           the standalone install: ~/.local/bin/codex
#   OpenCode        /opt/socrates/opencode/<version>, linked as ~/.local/bin/opencode
#   code-server     /opt/socrates/code-server/<version>, linked into /usr/local/bin
#   cloudflared     <data>/bin/cloudflared, the copy Socrates downloaded itself

PACKAGES=(ca-certificates curl git ripgrep ncurses-term jq xz-utils tar gzip procps psmisc less openssh-client)
MIN_TMUX=3.3

# record NAME VERSION PREVIOUS STATUS ERROR [DETAIL]. STATUS is one of
# current, updated, installed, failed, skipped, held.
record() {
	# Fields are split on the unit separator, not a tab: read would fold two
	# tabs around an empty field into one.
	local error=${5:-} detail=${6:-}
	printf '%s\037%s\037%s\037%s\037%s\037%s\n' "$1" "${2:-}" "${3:-}" "$4" "${error//$'\n'/ }" "${detail//$'\n'/ }" >>"$RESULTS"
	case $4 in
	failed) warn "$1: $error" ;;
	updated) info "$1: ${3:-} -> ${2:-}" ;;
	*) info "$1: $4${2:+ ($2)}${detail:+ - $detail}" ;;
	esac
}

# outcome NAME BEFORE AFTER: the status for a tool that was at BEFORE and is
# now at AFTER.
outcome() {
	if [ -z "$2" ]; then echo installed; elif [ "$2" = "$3" ]; then echo current; else echo updated; fi
}

# install_versioned NAME VERSION URL SHA256 STRIP SMOKE... unpacks the tarball
# at URL into /opt/socrates/NAME/VERSION and points NAME/current at it. SMOKE
# is a command, relative to the unpacked directory, that has to succeed
# before the new version is used; an empty SHA256 skips the checksum. A
# version already unpacked and working is reused. Older versions are kept
# while anything runs from them, and the newest two otherwise.
install_versioned() {
	local name=$1 version=$2 url=$3 sum=$4 strip=$5
	shift 5
	local dir=$OPT/$name final=$OPT/$name/$version file tmp
	mkdir -p "$dir" "$CACHE/downloads"
	if ! { [ -d "$final" ] && (cd "$final" && timeout 120 "$@") >/dev/null 2>&1 </dev/null; }; then
		file=$CACHE/downloads/$name-$version-${url##*/}
		fetch "$url" "$file" || { rm -f "$file.part"; echo "could not download $url" >&2; return 1; }
		if [ -n "$sum" ] && [ "$(sha256_of "$file")" != "$sum" ]; then
			rm -f "$file"
			echo "$url does not match its published checksum" >&2
			return 1
		fi
		tmp=$dir/.$version.tmp.$$
		rm -rf "$tmp" && mkdir -p "$tmp"
		if ! tar -xf "$file" -C "$tmp" --strip-components="$strip" --no-same-owner; then
			rm -rf "$tmp" "$file"
			echo "could not unpack $file" >&2
			return 1
		fi
		rm -f "$file"
		if ! (cd "$tmp" && timeout 120 "$@") >/dev/null 2>&1 </dev/null; then
			rm -rf "$tmp"
			echo "$name $version does not run on this machine ($*)" >&2
			return 1
		fi
		# A broken copy of the same version is moved aside rather than deleted,
		# in case something still runs from it.
		if [ -e "$final" ] && ! mv -f "$final" "$dir/.$version.broken.$$"; then
			rm -rf "$tmp"
			return 1
		fi
		mv -f "$tmp" "$final" || { rm -rf "$tmp"; return 1; }
	fi
	swap_link "$version" "$dir/current" || return 1
	prune "$dir" 2 || true
}

dep_packages() {
	local mode=$1 p missing=() version
	for p in "${PACKAGES[@]}"; do installed_pkg "$p" || missing+=("$p"); done
	if [ ${#missing[@]} -gt 0 ] || [ "$mode" = update ]; then
		# A third-party list that fails (an expired key on somebody's repo) is
		# not a reason to stop: the distribution's own lists are what is used.
		apt_get update -qq || warn "apt-get update reported errors; going on with the package lists there are"
	fi
	if [ ${#missing[@]} -gt 0 ]; then
		if ! retry 3 apt_get install -y --no-install-recommends "${missing[@]}"; then
			record apt "" "" failed "could not install ${missing[*]}"
			return 1
		fi
	fi
	if [ "$mode" = update ]; then
		# Only these packages, and only to what the configured repositories
		# hold: the rest of the machine is left to its own upgrade policy.
		retry 2 apt_get install -y --only-upgrade --no-install-recommends "${PACKAGES[@]}" ||
			warn "could not upgrade the packages; they stay as they are"
	fi
	version=$(dpkg-query -W -f='${Version}' git 2>/dev/null || true)
	if [ ${#missing[@]} -gt 0 ]; then
		record apt "${#PACKAGES[@]} packages" "" installed "" "installed ${missing[*]}"
	else
		record apt "${#PACKAGES[@]} packages" "" current "" "git $version"
	fi
}

# tmux comes from the distribution and is held there (apt-mark hold): a tmux
# upgraded under a running server can no longer talk to it, which would be
# every session lost to an unattended upgrade. A newer tmux is installed here
# instead, at a moment no tmux server runs at all, so nothing is cut off.
dep_tmux() {
	local mode=$1 before after candidate installed
	if ! command -v tmux >/dev/null; then
		retry 3 apt_get install -y --no-install-recommends tmux || {
			record tmux "" "" failed "could not install tmux"
			return 1
		}
	fi
	before=$(first_line tmux -V | awk '{print $2}' || true)
	apt-mark hold tmux >/dev/null 2>&1 || true
	installed=$(dpkg-query -W -f='${Version}' tmux 2>/dev/null || true)
	candidate=$(apt-cache policy tmux 2>/dev/null | awk '/Candidate:/ {print $2}')
	if [ -n "$installed" ] && [ -n "$candidate" ] && [ "$candidate" != "(none)" ] && [ "$candidate" != "$installed" ]; then
		# Any tmux at all, not only Socrates': a running server names itself
		# "tmux: server", so this matches on the name's start, not all of it.
		if [ "$mode" = update ] && ! pgrep '^tmux' >/dev/null; then
			apt-mark unhold tmux >/dev/null
			retry 2 apt_get install -y --only-upgrade --no-install-recommends tmux || warn "could not upgrade tmux"
			apt-mark hold tmux >/dev/null
		fi
	fi
	after=$(first_line tmux -V | awk '{print $2}' || true)
	if ! version_ge "${after%%[a-z]*}" "$MIN_TMUX"; then
		record tmux "$after" "$before" failed "Socrates needs tmux $MIN_TMUX or newer; this distribution has $after"
		return 1
	fi
	installed=$(dpkg-query -W -f='${Version}' tmux 2>/dev/null || true)
	if [ -n "$candidate" ] && [ "$candidate" != "(none)" ] && [ -n "$installed" ] && [ "$candidate" != "$installed" ]; then
		record tmux "$after" "" held "" "tmux $candidate is installed once no tmux server runs: systemctl stop 'socrates-tmux*.scope' ends the sessions"
	else
		record tmux "$after" "$before" "$(outcome tmux "$before" "$after")" ""
	fi
}

# go_required prints the Go version Socrates' go.mod asks for.
go_required() {
	local mod
	for mod in "$SRC/go.mod" "${REPO_DIR:-/nonexistent}/go.mod"; do
		[ -f "$mod" ] && { awk '$1 == "go" {print $2; exit}' "$mod"; return; }
	done
	echo 1.25.0
}

# Go builds Socrates, nothing else. It follows the newest stable release, so
# the binary gets the standard library's security fixes. GOTOOLCHAIN=local in
# update.sh keeps it from fetching some other toolchain on its own.
dep_go() {
	local mode=$1 need before latest json sum
	need=$(go_required)
	before=$("$OPT/go/current/bin/go" env GOVERSION 2>/dev/null || true)
	if [ "$mode" = repair ] && [ -n "$before" ] && version_ge "$before" "$need"; then
		record go "$before" "" current ""
		return
	fi
	if ! json=$(fetch_text 'https://go.dev/dl/?mode=json'); then
		if [ -n "$before" ] && version_ge "$before" "$need"; then
			record go "$before" "" current "" "go.dev could not be reached; keeping $before"
			return
		fi
		record go "$before" "" failed "go.dev could not be reached and no usable Go is installed"
		return 1
	fi
	# A page that is not the JSON it was (an error page, a changed format)
	# reads as no version, which keeps the Go there is.
	latest=$(jq -r '[.[] | select(.stable)][0].version // empty' <<<"$json" 2>/dev/null || true)
	sum=$(jq -r --arg f "$latest.linux-$ARCH.tar.gz" '[.[].files[] | select(.filename == $f) | .sha256][0] // empty' <<<"$json" 2>/dev/null || true)
	if [ "$before" != "$latest" ]; then
		# The toolchain that builds a program running as root is not taken
		# without its checksum.
		if [ -z "$latest" ] || [ -z "$sum" ]; then
			if [ -n "$before" ] && version_ge "$before" "$need"; then
				record go "$before" "" current "" "go.dev's list could not be read; keeping $before"
				return
			fi
			record go "$before" "" failed "go.dev's list names no checksum for $latest.linux-$ARCH.tar.gz"
			return 1
		fi
		if ! install_versioned go "$latest" "https://go.dev/dl/$latest.linux-$ARCH.tar.gz" "$sum" 1 ./bin/go version; then
			record go "$before" "" failed "could not install $latest"
			return 1
		fi
	fi
	link_bin "$OPT/go/current/bin/go" go
	link_bin "$OPT/go/current/bin/gofmt" gofmt
	record go "$latest" "$before" "$(outcome go "$before" "$latest")" ""
}

# Node is for the sessions - npx-launched MCP servers, the projects in the
# workspaces; the agents themselves are native binaries. It follows the newest
# LTS line (SOCRATES_NODE_MAJOR=22 holds it on one), and npm installs global
# packages into /usr/local, so they outlive a Node upgrade.
dep_node() {
	local mode=$1 major before latest index sums sum dir
	major=$(setting SOCRATES_NODE_MAJOR lts)
	before=$("$OPT/node/current/bin/node" --version 2>/dev/null || true)
	if [ "$mode" = repair ] && [ -n "$before" ]; then
		link_node
		record node "$before" "" current ""
		return
	fi
	if ! index=$(fetch_text https://nodejs.org/dist/index.json); then
		if [ -n "$before" ]; then
			link_node
			record node "$before" "" current "" "nodejs.org could not be reached; keeping $before"
			return
		fi
		record node "" "" failed "nodejs.org could not be reached"
		return 1
	fi
	if [ "$major" = lts ]; then
		latest=$(jq -r '[.[] | select(.lts != false)][0].version // empty' <<<"$index" 2>/dev/null || true)
	else
		latest=$(jq -r --arg m "v$major." '[.[] | select(.version | startswith($m))][0].version // empty' <<<"$index" 2>/dev/null || true)
	fi
	[ -n "$latest" ] && [ "$latest" != null ] || {
		record node "$before" "" failed "no Node release matches SOCRATES_NODE_MAJOR=$major"
		return 1
	}
	if [ "$before" != "$latest" ]; then
		sums=$(fetch_text "https://nodejs.org/dist/$latest/SHASUMS256.txt") || {
			record node "$before" "" failed "could not read the checksums of Node $latest"
			return 1
		}
		sum=$(awk -v f="node-$latest-linux-$NODE_ARCH.tar.xz" '$2 == f {print $1}' <<<"$sums")
		[ -n "$sum" ] || {
			record node "$before" "" failed "Node $latest has no build for linux-$NODE_ARCH"
			return 1
		}
		if ! install_versioned node "$latest" "https://nodejs.org/dist/$latest/node-$latest-linux-$NODE_ARCH.tar.xz" "$sum" 1 ./bin/node --version; then
			record node "$before" "" failed "could not install $latest"
			return 1
		fi
	fi
	dir=$OPT/node/$latest
	mkdir -p "$dir/etc"
	[ -f "$dir/etc/npmrc" ] || printf 'prefix=/usr/local\n' >"$dir/etc/npmrc"
	link_node
	record node "$latest" "$before" "$(outcome node "$before" "$latest")" ""
}

link_node() {
	local b
	for b in node npm npx corepack; do
		if [ -e "$OPT/node/current/bin/$b" ]; then
			link_bin "$OPT/node/current/bin/$b" "$b"
		elif ours "/usr/local/bin/$b" && [ -L "/usr/local/bin/$b" ]; then
			# Node 25 and later ship no corepack: a link to it would dangle.
			rm -f "/usr/local/bin/$b"
		fi
	done
	return 0
}

# remove_npm_copy PACKAGE takes out a copy of an agent that an older install
# put in with npm, once the native one runs: two copies on PATH is a session
# starting the one that is not updated. Deleting a file a session still runs
# from is safe - the session keeps it open; only overwriting it is not.
remove_npm_copy() {
	[ -d "/usr/local/lib/node_modules/$1" ] || return 0
	# The npm Claude Code runs its bundled ripgrep from that directory on every
	# search, so it stays while a session runs from it; the next run tries again.
	if grep -qsF "/usr/local/lib/node_modules/$1" /proc/[0-9]*/cmdline 2>/dev/null; then
		info "the npm copy of $1 is still in use; it is removed by a later run"
		return 0
	fi
	if command -v npm >/dev/null; then
		npm rm -g --prefix /usr/local "$1" >/dev/null 2>&1 && info "removed the npm copy of $1" && return 0
	fi
	warn "could not remove the npm copy of $1 from /usr/local"
}

# Claude Code: the native install, which keeps every version in a directory
# of its own under ~/.local/share/claude/versions and switches a link, so the
# sessions running the old one are not disturbed. Its own auto-updater stays
# on; this is the backstop for a machine whose sessions are all idle.
dep_claude() {
	local mode=$1 bin=$LOCAL_BIN/claude before after channel script native='' update_failed=''
	channel=$(setting SOCRATES_CLAUDE_CHANNEL latest)
	before=$(first_line "$bin" --version | awk '{print $1}' || true)
	case $(readlink -f "$bin" 2>/dev/null) in "$SOCRATES_HOME"/.local/share/claude/versions/*) native=1 ;; esac
	if [ -n "$native" ] && [ -n "$before" ]; then
		if [ "$mode" = update ]; then
			retry 3 timeout 600 "$bin" update </dev/null || update_failed=1
		fi
	fi
	after=$(first_line "$bin" --version | awk '{print $1}' || true)
	if [ -z "$after" ] || { [ "$mode" = update ] && [ "$after" = "$before" ] && [ -z "$native" ]; }; then
		script=$(mktemp)
		if ! fetch https://claude.ai/install.sh "$script"; then
			rm -f "$script"
			record claude "$before" "" failed "could not download the Claude Code installer"
			return 1
		fi
		retry 3 timeout 900 bash "$script" "$channel" </dev/null || warn "the Claude Code installer failed"
		rm -f "$script"
		after=$(first_line "$bin" --version | awk '{print $1}' || true)
	fi
	if [ -z "$after" ]; then
		record claude "$before" "" failed "Claude Code does not run after installing it"
		return 1
	fi
	remove_npm_copy @anthropic-ai/claude-code
	if [ -n "$update_failed" ] && [ "$after" = "$before" ]; then
		record claude "$after" "" failed "claude update failed; Claude Code stays at $before"
		return 1
	fi
	record claude "$after" "$before" "$(outcome claude "$before" "$after")" ""
}

# Codex: the standalone install, which unpacks each release into a directory
# of its own under ~/.codex/packages and switches a link. It is only run when
# a newer release is published: the installer reinstalls even the version
# that is already there.
dep_codex() {
	local mode=$1 bin=$LOCAL_BIN/codex before after latest script
	before=$(first_line "$bin" --version | awk '{print $NF}' || true)
	if [ "$mode" = repair ] && [ -n "$before" ]; then
		record codex "$before" "" current ""
		return
	fi
	latest=$(fetch_text https://releases.openai.com/codex/channels/latest | jq -r '.tag_name // empty' 2>/dev/null || true)
	latest=${latest#rust-v}
	if [ -n "$before" ] && { [ -z "$latest" ] || [ "$before" = "$latest" ]; }; then
		if [ -n "$latest" ]; then
			record codex "$before" "" current ""
		else
			record codex "$before" "" current "" "the release channel could not be read; keeping $before"
		fi
		return
	fi
	script=$(mktemp)
	if ! fetch https://chatgpt.com/codex/install.sh "$script"; then
		rm -f "$script"
		record codex "$before" "" failed "could not download the Codex installer"
		return 1
	fi
	mkdir -p "$LOCAL_BIN"
	# The installer reads the same channel itself; naming the release would
	# also switch off the standalone install's own update path.
	retry 3 env CODEX_NON_INTERACTIVE=1 CODEX_INSTALL_DIR="$LOCAL_BIN" \
		timeout 900 sh "$script" </dev/null || warn "the Codex installer failed"
	rm -f "$script"
	after=$(first_line "$bin" --version | awk '{print $NF}' || true)
	if [ -z "$after" ]; then
		record codex "$before" "" failed "Codex does not run after installing it"
		return 1
	fi
	remove_npm_copy @openai/codex
	record codex "$after" "$before" "$(outcome codex "$before" "$after")" ""
}

# OpenCode: its release binary, unpacked here rather than with its installer,
# which overwrites the one copy it has and checks nothing. It stays on major
# version 1 (SOCRATES_OPENCODE_MAJOR): OpenCode 2 is a different program
# under the same name. Its own updater is off in every session Socrates
# starts.
dep_opencode() {
	local mode=$1 before after latest major asset
	major=$(setting SOCRATES_OPENCODE_MAJOR 1)
	before=$(first_line "$LOCAL_BIN/opencode" --version || true)
	if [ "$mode" = repair ] && [ -n "$before" ]; then
		record opencode "$before" "" current ""
		return
	fi
	# The newest release of this major, not npm's "latest", which moves to the
	# next major the day it is out.
	latest=$(curl -fsSL --proto '=https' --connect-timeout 20 --max-time 120 --retry 4 --retry-all-errors \
		-H 'Accept: application/vnd.npm.install-v1+json' https://registry.npmjs.org/opencode-ai |
		jq -r --arg m "$major." '[.versions | keys[] | select(startswith($m) and (contains("-") | not))]
			| sort_by(split(".") | map(tonumber? // 0)) | last // empty' 2>/dev/null || true)
	case $latest in "$major".*) ;; *)
		if [ -n "$before" ]; then
			record opencode "$before" "" current "" "no OpenCode $major.x release could be read; keeping $before"
			return
		fi
		record opencode "" "" failed "no OpenCode $major.x release could be read from the npm registry"
		return 1
		;;
	esac
	if [ "$before" != "$latest" ] || ! [ -x "$OPT/opencode/current/opencode" ]; then
		case $ARCH in
		amd64) asset=linux-x64 && grep -qw avx2 /proc/cpuinfo || asset=linux-x64-baseline ;;
		arm64) asset=linux-arm64 ;;
		esac
		ldd --version 2>&1 | grep -qi musl && asset=$asset-musl
		if ! install_versioned opencode "$latest" \
			"https://github.com/anomalyco/opencode/releases/download/v$latest/opencode-$asset.tar.gz" "" 0 ./opencode --version; then
			record opencode "$before" "" failed "could not install $latest"
			return 1
		fi
	fi
	mkdir -p "$LOCAL_BIN"
	if ours "$LOCAL_BIN/opencode" || [ "$(readlink "$LOCAL_BIN/opencode")" = "$SOCRATES_HOME/.opencode/bin/opencode" ]; then
		swap_link "$OPT/opencode/current/opencode" "$LOCAL_BIN/opencode"
	fi
	after=$(first_line "$LOCAL_BIN/opencode" --version || true)
	if [ -z "$after" ]; then
		record opencode "$before" "" failed "OpenCode does not run after installing it"
		return 1
	fi
	remove_npm_copy opencode-ai
	record opencode "$after" "$before" "$(outcome opencode "$before" "$after")" ""
}

# code-server, for the VS Code sessions: its release tarball, which carries
# its own Node. SOCRATES_CODE_SERVER_VERSION pins it; the default follows the
# newest release.
dep_code_server() {
	local mode=$1 before want tag
	before=$(first_line code-server --version | awk '{print $1}' || true)
	if [ "$mode" = repair ] && [ -n "$before" ]; then
		record code-server "$before" "" current ""
		return
	fi
	want=$(setting SOCRATES_CODE_SERVER_VERSION latest)
	if [ "$want" = latest ]; then
		tag=$(latest_github_tag coder/code-server || true)
		want=${tag#v}
		if [ -z "$want" ]; then
			[ -n "$before" ] && { record code-server "$before" "" current "" "GitHub could not be reached; keeping $before"; return; }
			record code-server "" "" failed "could not find code-server's newest release"
			return 1
		fi
	fi
	if [ "$before" != "$want" ] || [ "$(readlink -f /usr/local/bin/code-server)" != "$(readlink -f "$OPT/code-server/current/bin/code-server")" ]; then
		if ! install_versioned code-server "$want" \
			"https://github.com/coder/code-server/releases/download/v$want/code-server-$want-linux-$ARCH.tar.gz" "" 1 ./bin/code-server --version; then
			record code-server "$before" "" failed "could not install $want"
			return 1
		fi
		link_bin "$OPT/code-server/current/bin/code-server" code-server
	fi
	local after
	after=$(first_line code-server --version | awk '{print $1}' || true)
	record code-server "$after" "$before" "$(outcome code-server "$before" "$after")" ""
}

# cloudflared is downloaded by Socrates itself the first time the tunnel is
# switched on, into the data directory, and runs with --no-autoupdate. It is
# brought up to date here; the running tunnel moves to the new binary when
# Socrates restarts, which update.sh then does.
dep_cloudflared() {
	local mode=$1 bin=$DATA/bin/cloudflared before after tag
	if ! [ -x "$bin" ]; then
		record cloudflared "" "" skipped "" "Socrates downloads it when the tunnel is switched on"
		return
	fi
	before=$(first_line "$bin" --version | awk '{print $3}' || true)
	if [ "$mode" = repair ] && [ -n "$before" ]; then
		record cloudflared "$before" "" current ""
		return
	fi
	tag=$(latest_github_tag cloudflare/cloudflared || true)
	if [ -z "$tag" ] || [ "$tag" = "$before" ]; then
		if [ -n "$tag" ]; then
			record cloudflared "$before" "" current ""
		else
			record cloudflared "$before" "" current "" "GitHub could not be reached; keeping $before"
		fi
		return
	fi
	if ! fetch "https://github.com/cloudflare/cloudflared/releases/download/$tag/cloudflared-linux-$ARCH" "$bin.new"; then
		rm -f "$bin.new.part"
		record cloudflared "$before" "" failed "could not download cloudflared $tag"
		return 1
	fi
	chmod 755 "$bin.new"
	if ! timeout 60 "$bin.new" --version >/dev/null 2>&1 </dev/null; then
		rm -f "$bin.new"
		record cloudflared "$before" "" failed "cloudflared $tag does not run on this machine"
		return 1
	fi
	mv -f "$bin.new" "$bin"
	after=$(first_line "$bin" --version | awk '{print $3}' || true)
	touch "$STATE/restart-needed"
	record cloudflared "$after" "$before" updated ""
}

DEPS=(packages tmux go node claude codex opencode code_server cloudflared)
