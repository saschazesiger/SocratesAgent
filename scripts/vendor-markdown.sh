#!/usr/bin/env bash
# Re-download the pinned Markdown renderer (marked) and sanitizer (DOMPurify)
# into internal/web/static/vendor/markdown.
#
# The files it writes are committed, so this script is for an upgrade and never
# for a build. It reads vendor/markdown/VERSIONS - one `package@version
# <file-in-tarball> <vendored-as>` per line - fetches each tarball from the npm
# registry once, and copies only the files the chat view loads. DOMPurify's
# ES module is published as .mjs; it is vendored as .js so that the server
# stamps its import like every other module's.
#
# vendor/markdown/SHA256SUMS, if it exists, is checked after the copy. A set
# that does not match is a set nobody meant to change, so the run fails and
# the working tree is left as `git checkout` can undo it.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
vendor="$here/internal/web/static/vendor/markdown"
versions="$vendor/VERSIONS"
[ -f "$versions" ] || { echo "no $versions to read" >&2; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

fetch() { # <package> <version> -> extracted tarball root in $work/<pkg>-<ver>
  local pkg="$1" ver="$2" root
  root="$work/$pkg-$ver"
  if [ ! -d "$root" ]; then
    mkdir -p "$root"
    curl -fsSL "https://registry.npmjs.org/$pkg/-/$pkg-$ver.tgz" \
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
    echo "the downloaded set does not match vendor/markdown/SHA256SUMS; nothing was meant to change" >&2
    exit 1
  }
else
  ( cd "$vendor" && sha256sum marked.esm.js purify.es.js LICENSE-marked LICENSE-dompurify > SHA256SUMS )
  echo "wrote vendor/markdown/SHA256SUMS"
fi
