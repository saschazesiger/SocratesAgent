#!/usr/bin/env bash
# The one command the deploy key may run on the server (its authorized_keys
# line forces it; scripts/deploy-access.sh writes that line). The deploy job of
# .github/workflows/deploy.yml, on a GitHub-hosted runner in the tailnet,
# sends the commit as the command and the binary on stdin:
#
#   ssh socrates-deploy@SERVER COMMIT < socrates
#
# Nothing it is sent is taken on trust: deploy.sh installs the binary only if
# GitHub attests that deploy.yml built it from COMMIT of the branch on a
# GitHub-hosted runner, and update.sh then smoke tests, health checks and
# rolls back as with any update.
set -euo pipefail

# A Socrates binary is about 40 MB; anything far bigger is not one.
max=$((300 * 1024 * 1024))

commit=${SSH_ORIGINAL_COMMAND:-}
if ! [[ $commit =~ ^[0-9a-f]{40}$ ]]; then
	echo "usage: ssh socrates-deploy@SERVER COMMIT < BINARY (COMMIT a full commit hash)" >&2
	exit 2
fi

dir=$(mktemp -d "${HOME:-/tmp}/incoming.XXXXXX")
trap 'rm -rf "$dir"' EXIT
head -c $((max + 1)) >"$dir/socrates"
size=$(stat -c %s "$dir/socrates")
if [ "$size" -eq 0 ] || [ "$size" -gt "$max" ]; then
	echo "the binary on stdin is empty or bigger than $max bytes" >&2
	exit 2
fi

sudo -n /usr/local/lib/socrates/deploy.sh "$commit" "$dir/socrates"
