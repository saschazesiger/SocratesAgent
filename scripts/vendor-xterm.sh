#!/usr/bin/env bash
# Re-download the pinned xterm.js bundle set into internal/web/static/vendor.
#
# The files it writes are committed, so this script is for an upgrade and never
# for a build: CI, the Dockerfile and `make check` never run it. It reads
# vendor/VERSIONS - the pin, one `package@version <file-in-tarball>
# <vendored-as>` per line - fetches each tarball from the npm registry once,
# and extracts only the files we ship. The `.js.map` files are deliberately not
# among them: 1.9 MB for xterm alone, and everything shipped is precached by
# the service worker.
#
# vendor/SHA256SUMS, if it exists, is checked after the extraction. A set that
# does not match is a set nobody meant to change, so the run fails and the
# working tree is left as `git checkout` can undo it.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
vendor="$here/internal/web/static/vendor"
versions="$vendor/VERSIONS"
[ -f "$versions" ] || { echo "no $versions to read" >&2; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

fetch() { # <package> <version> -> extracted tarball root in $work/<pkg>-<ver>
  local pkg="$1" ver="$2" name root
  name="${pkg#@xterm/}"
  root="$work/$name-$ver"
  if [ ! -d "$root" ]; then
    mkdir -p "$root"
    curl -fsSL "https://registry.npmjs.org/$pkg/-/$name-$ver.tgz" \
      | tar -xzf - -C "$root" --strip-components=1
  fi
  printf '%s' "$root"
}

while read -r spec from to; do
  case "$spec" in ''|\#*) continue ;; esac
  pkg="${spec%@*}"; ver="${spec##*@}"
  root="$(fetch "$pkg" "$ver")"
  [ -f "$root/$from" ] || { echo "$pkg@$ver has no $from" >&2; exit 1; }
  cp "$root/$from" "$vendor/$to"
  echo "vendored $to from $pkg@$ver"
done < "$versions"

if [ -f "$vendor/SHA256SUMS" ]; then
  ( cd "$vendor" && sha256sum -c SHA256SUMS ) || {
    echo "the downloaded set does not match vendor/SHA256SUMS; nothing was meant to change" >&2
    exit 1
  }
else
  ( cd "$vendor" && sha256sum xterm.js xterm.css addon-*.js LICENSE-xterm > SHA256SUMS )
  echo "wrote vendor/SHA256SUMS"
fi
