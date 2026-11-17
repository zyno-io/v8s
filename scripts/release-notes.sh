#!/usr/bin/env bash
# Prints release notes for a tag: the annotated tag's message if it has one, else the matching CHANGELOG.md section.
#   scripts/release-notes.sh v1.2.3
set -euo pipefail

tag=${1:?usage: release-notes.sh <tag>}
version=${tag#v}
cd "$(dirname "$0")/.."

if [[ $(git cat-file -t "refs/tags/$tag" 2>/dev/null || true) == tag ]]; then
  # Annotated tag: contents is the message (and a signature block, if signed, which we strip).
  notes=$(git tag -l --format='%(contents)' "$tag" | sed '/^-----BEGIN PGP SIGNATURE-----$/,$d')
  if [[ -n "${notes//[[:space:]]/}" ]]; then
    printf '%s\n' "$notes"
    exit 0
  fi
fi

if [[ -f CHANGELOG.md ]]; then
  notes=$(awk -v v="$version" '
    /^## / {
      if (on) exit
      h = $0
      sub(/^## +\[?/, "", h)
      sub(/\]?( .*)?$/, "", h)
      on = (h == v)
      next
    }
    on { print }
  ' CHANGELOG.md)
  if [[ -n "${notes//[[:space:]]/}" ]]; then
    printf '%s\n' "$notes"
    exit 0
  fi
fi

echo "Release $tag. See [CHANGELOG.md](https://github.com/zyno-io/v8s/blob/$tag/CHANGELOG.md)."
