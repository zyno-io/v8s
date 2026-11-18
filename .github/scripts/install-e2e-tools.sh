#!/usr/bin/env bash
# Installs the pinned kind and flux CLIs for .github/workflows/e2e.yml, verifying their published checksums.
# helm and kubectl come with the ubuntu runner image.
set -euo pipefail

: "${KIND_VERSION:?}" "${FLUX_VERSION:?}"
tmp=$(mktemp -d)
cd "$tmp"

base=https://github.com/kubernetes-sigs/kind/releases/download/$KIND_VERSION
curl -fsSLo kind "$base/kind-linux-amd64"
curl -fsSL "$base/kind-linux-amd64.sha256sum" | awk '{print $1"  kind"}' | sha256sum -c -
sudo install kind /usr/local/bin/kind

base=https://github.com/fluxcd/flux2/releases/download/v$FLUX_VERSION
tarball=flux_${FLUX_VERSION}_linux_amd64.tar.gz
curl -fsSLO "$base/$tarball"
curl -fsSL "$base/flux_${FLUX_VERSION}_checksums.txt" | grep " $tarball\$" | sha256sum -c -
tar xzf "$tarball" flux
sudo install flux /usr/local/bin/flux

kind version
flux --version
helm version --short
