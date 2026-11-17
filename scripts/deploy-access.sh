#!/usr/bin/env bash
# Make this machine install every push to the branch it follows, as root and
# once:
#
#   sudo TS_AUTHKEY=tskey-auth-… scripts/deploy-access.sh
#
# .github/workflows/deploy.yml builds and attests the binary on a GitHub-hosted
# runner; its deploy job, also GitHub-hosted, joins the tailnet as an
# ephemeral device (TS_AUTHKEY, a reusable auth key) and sends the binary over
# SSH to this machine's tailnet address. The key it signs in with may run one
# command, deploy-ssh.sh, and only from the tailnet; its user, socrates-deploy,
# may run /usr/local/lib/socrates/deploy.sh as root and nothing else, and that
# installs only a binary GitHub attests deploy.yml built from the branch.
#
# It writes the repository's secrets DEPLOY_SSH_KEY and TS_AUTHKEY and its
# variables DEPLOY_HOST and DEPLOY_KNOWN_HOSTS, so it needs gh logged in as
# somebody who administers the repository, and tailscale up on this machine.
# Without TS_AUTHKEY the secret the repository has is kept. Running it again
# makes a new key and changes nothing else. A self-hosted deploy runner of an
# earlier setup is unregistered and removed.
#
#   sudo scripts/deploy-access.sh --remove   take the access away again
set -euo pipefail

HERE=$(cd "$(dirname "$(readlink -f "$0")")" && pwd)
. "$HERE/lib.sh"

[ "$(id -u)" = 0 ] || die "run it as root: sudo $0"
command -v gh >/dev/null || die "gh is needed: https://cli.github.com"
gh auth status >/dev/null 2>&1 || die "gh is not logged in: gh auth login"

USER_NAME=socrates-deploy
HOME_DIR=/var/lib/socrates-deploy
RUNNER_DIR=$HOME_DIR/runner
repo=$(setting SOCRATES_REPO "$DEFAULT_REPO")
slug=${repo#https://github.com/}
slug=${slug%.git}
[[ $slug =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || die "$repo is not a GitHub repository"

# remove_runner takes away the self-hosted runner an earlier setup registered.
remove_runner() {
	[ -d "$RUNNER_DIR" ] || return 0
	say "The self-hosted runner of the earlier setup"
	if [ -f "$RUNNER_DIR/.runner" ]; then
		(cd "$RUNNER_DIR" && ./svc.sh stop && ./svc.sh uninstall) || true
		token=$(gh api -X POST "repos/$slug/actions/runners/remove-token" --jq .token)
		(cd "$RUNNER_DIR" && runuser -u "$USER_NAME" -- ./config.sh remove --token "$token") || true
	fi
	rm -rf "$RUNNER_DIR"
}

if [ "${1:-}" = --remove ]; then
	remove_runner
	rm -f "$HOME_DIR/.ssh/authorized_keys" /etc/sudoers.d/socrates-deploy
	for s in DEPLOY_SSH_KEY TS_AUTHKEY; do gh secret delete "$s" -R "$slug" 2>/dev/null || true; done
	for v in DEPLOY_HOST DEPLOY_KNOWN_HOSTS; do gh variable delete "$v" -R "$slug" 2>/dev/null || true; done
	say "The deploy access is gone; pushes are installed by the nightly update again"
	exit 0
fi

command -v tailscale >/dev/null || die "tailscale is needed: https://tailscale.com/download"
host=$(tailscale ip -4 2>/dev/null | head -n1)
[ -n "$host" ] || die "tailscale is not up on this machine: tailscale up"
[ -x "$LIBEXEC/deploy.sh" ] || die "$LIBEXEC/deploy.sh is missing: run install.sh or update.sh first"
[ -f /etc/ssh/ssh_host_ed25519_key.pub ] || die "this machine has no SSH host key /etc/ssh/ssh_host_ed25519_key.pub"
if [ -z "${TS_AUTHKEY:-}" ] && ! gh secret list -R "$slug" | grep -q '^TS_AUTHKEY\b'; then
	die "the repository has no secret TS_AUTHKEY yet: run it with TS_AUTHKEY=tskey-auth-… (a reusable auth key)"
fi

remove_runner

say "User $USER_NAME"
id -u "$USER_NAME" >/dev/null 2>&1 ||
	useradd --system --home-dir "$HOME_DIR" --create-home "$USER_NAME"
# sshd runs the forced command with the user's shell; the user has no
# password, so the key is the only way in.
usermod -s /bin/bash "$USER_NAME"
passwd -l "$USER_NAME" >/dev/null
chown "$USER_NAME:" "$HOME_DIR"
chmod 750 "$HOME_DIR"
rm -rf "$HOME_DIR"/incoming.*

say "deploy-ssh.sh, and sudo for $LIBEXEC/deploy.sh"
install -m 755 "$HERE/deploy-ssh.sh" "$LIBEXEC/.deploy-ssh.sh.new"
mv -f "$LIBEXEC/.deploy-ssh.sh.new" "$LIBEXEC/deploy-ssh.sh"
rule=$(mktemp)
echo "$USER_NAME ALL=(root) NOPASSWD: $LIBEXEC/deploy.sh" >"$rule"
visudo -cqf "$rule" || die "the sudoers rule does not parse"
install -m 440 "$rule" /etc/sudoers.d/socrates-deploy
rm -f "$rule"

say "The deploy key"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
ssh-keygen -q -t ed25519 -N '' -C "deploy@$slug" -f "$tmp/key"
install -d -m 700 -o "$USER_NAME" -g "$USER_NAME" "$HOME_DIR/.ssh"
# Only from the tailnet (Tailscale's IPv4 and IPv6 ranges), only deploy-ssh.sh.
printf 'restrict,from="100.64.0.0/10,fd7a:115c:a1e0::/48",command="%s" %s\n' \
	"$LIBEXEC/deploy-ssh.sh" "$(cat "$tmp/key.pub")" >"$HOME_DIR/.ssh/authorized_keys"
chown "$USER_NAME:" "$HOME_DIR/.ssh/authorized_keys"
chmod 600 "$HOME_DIR/.ssh/authorized_keys"

say "The repository's secrets and variables"
gh secret set DEPLOY_SSH_KEY -R "$slug" <"$tmp/key"
if [ -n "${TS_AUTHKEY:-}" ]; then
	printf '%s' "$TS_AUTHKEY" | gh secret set TS_AUTHKEY -R "$slug"
fi
gh variable set DEPLOY_HOST -R "$slug" --body "$host"
gh variable set DEPLOY_KNOWN_HOSTS -R "$slug" --body "$host $(cut -d' ' -f1,2 /etc/ssh/ssh_host_ed25519_key.pub)"

say "Done: every push to $(setting SOCRATES_BRANCH main) of $slug is installed here over the tailnet ($host; Actions → Deploy)"
