#!/usr/bin/env bash
# Make this machine install every push to the branch it follows, as root and
# once:
#
#   sudo scripts/deploy-runner.sh
#
# It registers a GitHub Actions runner for the repository with the label
# socrates-deploy, which .github/workflows/deploy.yml runs its last step on.
# The runner only connects out to GitHub, so this works behind NAT and needs
# no open port. It runs as its own user, socrates-deploy, who may run
# /usr/local/lib/socrates/deploy.sh as root and nothing else; that script
# installs only a binary GitHub attests deploy.yml built from the branch.
# The repository is also set to ask before running a workflow from any
# outside contributor's pull request, since such a job could reach the runner.
#
# It needs gh logged in as somebody who administers the repository. Running it
# again re-registers the runner and changes nothing else.
#
#   sudo scripts/deploy-runner.sh --remove   unregister the runner and remove it
set -euo pipefail

HERE=$(cd "$(dirname "$(readlink -f "$0")")" && pwd)
. "$HERE/lib.sh"

[ "$(id -u)" = 0 ] || die "run it as root: sudo $0"
command -v gh >/dev/null || die "gh is needed: https://cli.github.com"
gh auth status >/dev/null 2>&1 || die "gh is not logged in: gh auth login"
[ -x "$LIBEXEC/deploy.sh" ] || die "$LIBEXEC/deploy.sh is missing: run install.sh or update.sh first"

USER_NAME=socrates-deploy
RUNNER_HOME=/var/lib/socrates-deploy
RUNNER_DIR=$RUNNER_HOME/runner
repo=$(setting SOCRATES_REPO "$DEFAULT_REPO")
slug=${repo#https://github.com/}
slug=${slug%.git}
[[ $slug =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || die "$repo is not a GitHub repository"
name=$(hostname -s)-socrates

as_runner() { (cd "$RUNNER_DIR" && runuser -u "$USER_NAME" -- "$@"); }

if [ "${1:-}" = --remove ]; then
	if [ -f "$RUNNER_DIR/.runner" ]; then
		(cd "$RUNNER_DIR" && ./svc.sh stop && ./svc.sh uninstall) || true
		token=$(gh api -X POST "repos/$slug/actions/runners/remove-token" --jq .token)
		as_runner ./config.sh remove --token "$token"
	fi
	rm -f /etc/sudoers.d/socrates-deploy
	say "The runner is gone; pushes are installed by the nightly update again"
	exit 0
fi

say "User $USER_NAME"
id -u "$USER_NAME" >/dev/null 2>&1 ||
	useradd --system --home-dir "$RUNNER_HOME" --create-home --shell /usr/sbin/nologin "$USER_NAME"
mkdir -p "$RUNNER_DIR"
chown "$USER_NAME:" "$RUNNER_HOME" "$RUNNER_DIR"
chmod 750 "$RUNNER_HOME"

say "sudo for $LIBEXEC/deploy.sh"
rule=$(mktemp)
echo "$USER_NAME ALL=(root) NOPASSWD: $LIBEXEC/deploy.sh" >"$rule"
visudo -cqf "$rule" || die "the sudoers rule does not parse"
install -m 440 "$rule" /etc/sudoers.d/socrates-deploy
rm -f "$rule"

say "The runner"
if ! [ -x "$RUNNER_DIR/config.sh" ]; then
	tmp=$(mktemp -d)
	gh release download -R actions/runner -p "actions-runner-linux-$NODE_ARCH-*.tar.gz" -D "$tmp"
	tar -xzf "$tmp"/actions-runner-linux-*.tar.gz -C "$RUNNER_DIR"
	rm -rf "$tmp"
	chown -R "$USER_NAME:" "$RUNNER_DIR"
	"$RUNNER_DIR/bin/installdependencies.sh" >/dev/null
fi
if [ -f "$RUNNER_DIR/.runner" ]; then
	(cd "$RUNNER_DIR" && ./svc.sh stop >/dev/null 2>&1 && ./svc.sh uninstall >/dev/null 2>&1) || true
fi
token=$(gh api -X POST "repos/$slug/actions/runners/registration-token" --jq .token)
as_runner ./config.sh --unattended --replace --url "https://github.com/$slug" --token "$token" \
	--name "$name" --labels socrates-deploy --work _work
(cd "$RUNNER_DIR" && ./svc.sh install "$USER_NAME" && ./svc.sh start)

say "Approval for workflows from outside contributors' pull requests"
gh api -X PUT "repos/$slug/actions/permissions/fork-pr-contributor-approval" \
	-f approval_policy=all_external_contributors

say "Done: every push to $(setting SOCRATES_BRANCH main) of $slug is installed here (Actions → Deploy)"
