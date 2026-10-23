#!/usr/bin/env bash
# Install the Socrates that .github/workflows/deploy.yml built from a push to
# the branch this machine follows. The workflow's last step runs it on the
# self-hosted runner, whose user may run this as root and nothing else
# (scripts/deploy-runner.sh):
#
#   sudo /usr/local/lib/socrates/deploy.sh COMMIT BINARY
#
# A job from somebody's pull request can end up on that runner too, so neither
# argument is taken on trust. BINARY has to carry GitHub's attestation that
# deploy.yml built it from COMMIT of the branch on a GitHub-hosted runner, and
# update.sh then installs it only if COMMIT is on the branch, with the smoke
# test, health check and rollback of any update. An update that is already
# running is waited for.
set -euo pipefail

HERE=$(cd "$(dirname "$(readlink -f "$0")")" && pwd)
. "$HERE/lib.sh"

[ $# = 2 ] || die "usage: $0 COMMIT BINARY"
commit=$1
[[ $commit =~ ^[0-9a-f]{40}$ ]] || die "COMMIT is a full commit hash"
[ -f "$2" ] || die "$2 is not a file"
[ "$(id -u)" = 0 ] || die "run it as root"
command -v gh >/dev/null || die "gh is needed to check the binary's attestation"

repo=$(setting SOCRATES_REPO "$DEFAULT_REPO")
branch=$(setting SOCRATES_BRANCH main)
slug=${repo#https://github.com/}
slug=${slug%.git}
[[ $slug =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || die "$repo is not a GitHub repository; there is no attestation to check"

# A copy only root can write: what is checked is then what is installed.
mkdir -p "$CACHE/deploy"
chmod 700 "$CACHE/deploy"
copy=$CACHE/deploy/socrates-$commit
trap 'rm -f "$copy" "$copy.tmp"' EXIT
install -m 755 "$2" "$copy.tmp"
mv -f "$copy.tmp" "$copy"

say "Check that $slug's deploy workflow built it from ${commit:0:12} of $branch"
retry 3 gh attestation verify "$copy" --repo "$slug" \
	--signer-workflow "$slug/.github/workflows/deploy.yml" \
	--source-ref "refs/heads/$branch" --source-digest "$commit" \
	--deny-self-hosted-runners

# update.sh answers 75 while another update runs: the nightly one, or the
# button on the admin page.
for _ in $(seq 240); do
	rc=0
	"$HERE/update.sh" --socrates --commit="$commit" --binary="$copy" --trigger=push || rc=$?
	[ "$rc" = 75 ] || break
	sleep 30
done
[ "$rc" = 0 ] || exit "$rc"

# update.sh also ends well when it decided not to install: a commit that
# failed here before, or a second run that found one going.
installed=$(cat "$STATE/socrates.commit" 2>/dev/null || true)
if [ "$installed" = "$commit" ]; then
	say "Socrates $("$BIN" -version) from ${commit:0:12} is running"
elif [ -n "$installed" ] && git -c safe.directory="$SRC" -C "$SRC" merge-base --is-ancestor "$commit" "$installed" 2>/dev/null; then
	say "A newer commit, ${installed:0:12}, is already running"
else
	die "${commit:0:12} was not installed; ${installed:0:12} is running (see the update above, or Admin)"
fi
