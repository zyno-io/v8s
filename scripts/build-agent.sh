#!/usr/bin/env bash
# Builds the v8s agent as static binaries, optionally with the UI embedded.
#
#   scripts/build-agent.sh [--host] [--no-ui|--ui-dist DIR] [--layout cli|desktop] [--out DIR] [TARGET...]
#
# TARGET is GOOS/GOARCH. Default targets: linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64.
#   --host       build only for the machine running the script
#   --ui-dist D  embed this prebuilt UI (a packages/ui/dist copy) instead of building it
#   --no-ui      skip the UI build and embed nothing (the desktop app ships the UI separately)
#   --layout     cli (default): <out>/<goos>_<goarch>/v8s[.exe]   (default out: dist)
#                desktop:      <out>/<mac|linux|win>-<x64|arm64>/v8s[.exe]   (default out: agent/bin;
#                              matches the electron-builder ${os}-${arch} macros in packages/desktop)
# Version: $VERSION, else `git describe`, else 0.0.0-dev (a leading "v" is stripped).
set -euo pipefail

cd "$(dirname "$0")/.."
root=$PWD

with_ui=1
layout=cli
out=""
ui_dist=""
targets=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --host) targets=("$(go env GOOS)/$(go env GOARCH)") ;;
    --no-ui) with_ui=0 ;;
    --ui-dist) ui_dist=$2; shift ;;
    --layout) layout=$2; shift ;;
    --out) out=$2; shift ;;
    -h | --help) sed -n '2,13p' "$0"; exit 0 ;;
    -*) echo "unknown flag: $1" >&2; exit 2 ;;
    *) targets+=("$1") ;;
  esac
  shift
done
if [[ ${#targets[@]} -eq 0 ]]; then
  targets=(linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64)
fi
if [[ -z "$out" ]]; then
  [[ $layout == desktop ]] && out=agent/bin || out=dist
fi
[[ $layout == cli || $layout == desktop ]] || { echo "unknown layout: $layout" >&2; exit 2; }

version=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)}
version=${version#v}

webui=agent/internal/webui/dist
find "$webui" -mindepth 1 ! -name .keep -delete
if [[ $with_ui == 1 ]]; then
  if [[ -z "$ui_dist" ]]; then
    echo "==> building UI"
    yarn workspace @v8s/ui build-only
    ui_dist=packages/ui/dist
  fi
  [[ -f "$ui_dist/index.html" ]] || { echo "no UI build at $ui_dist" >&2; exit 1; }
  cp -R "$ui_dist"/. "$webui"/
fi

for t in "${targets[@]}"; do
  goos=${t%/*}
  goarch=${t#*/}
  exe=v8s
  [[ $goos == windows ]] && exe=v8s.exe
  if [[ $layout == desktop ]]; then
    case $goos in darwin) eos=mac ;; windows) eos=win ;; *) eos=$goos ;; esac
    case $goarch in amd64) earch=x64 ;; *) earch=$goarch ;; esac
    dest=$out/$eos-$earch
  else
    dest=$out/${goos}_${goarch}
  fi
  # --out may be absolute or relative to the repo root.
  [[ $dest == /* ]] || dest=$root/$dest
  mkdir -p "$dest"
  echo "==> go build $goos/$goarch (v$version) -> $dest/$exe"
  (cd agent && CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go build -trimpath \
    -ldflags "-s -w -X github.com/zyno-io/v8s/agent/internal/version.Version=$version" \
    -o "$dest/$exe" ./cmd/v8s)
done
