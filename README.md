# v8s

**Visual k8s**: a fast, multi-cluster Kubernetes manager for desktop, web, and phone.

[![CI](https://github.com/zyno-io/v8s/actions/workflows/ci.yml/badge.svg)](https://github.com/zyno-io/v8s/actions/workflows/ci.yml)
[![Docs](https://github.com/zyno-io/v8s/actions/workflows/pages.yml/badge.svg)](https://docs.v8s.io/)
[![Release](https://img.shields.io/github/v/release/zyno-io/v8s?include_prereleases)](https://github.com/zyno-io/v8s/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**Documentation: <https://docs.v8s.io/>**

v8s keeps compact projections of your clusters in a Go agent and sends the UI only the rows on screen, so 10k+ objects per cluster, and several clusters at once, stay smooth. Every request carries your own credential to the API server, or to an access gateway such as [Warpgate](https://github.com/warp-tech/warpgate).

## Features

- **Fast on big clusters.** Design budgets of 60 fps scrolling over 50k pods and 20 ms sort and filter, with the agent doing the heavy work.
- **Multi-cluster by default.** Select clusters or a group such as `production`. Every list, search, and the Problems view span the selection. v8s connects only to clusters you pick.
- **Problems view.** 51 problem classes across pods, workloads, jobs, nodes, storage, networking, Flux, Helm, policy, vulnerabilities, and network flows, rolled up by workload, with acknowledge and snooze.
- **Flux and Helm.** Reconcile, suspend, inventory trees, dependency graph, Helm history, revision diff, rollback, and a guard for Flux-managed releases.
- **Security and network.** Kyverno policies with Audit and Enforce switches, Trivy Operator and Kubescape findings by image, and Hubble flows, drops, and service map.
- **Day-to-day operations.** YAML edit with server-side dry run and diff, create, scale, restart, pod and node shell, logs, cordon and drain, port-forward.
- **Linked read and write contexts** that switch automatically for writes.
- **Keyboard first.** A k9s-style command bar, a search palette, and shortcuts such as `l` logs, `s` shell, `e` edit YAML, `d` describe.
- **Desktop, browser, and phone.** One UI and one Go binary: the desktop app, `v8s serve`, and a self-hosted multi-user server with OIDC sign-in and a Problems-first phone layout.

## Install

**Desktop app.** Download the installer for macOS (`.dmg`), Windows (`.exe`), or Linux (`.AppImage`, `.deb`, `.rpm`) from the [releases page](https://github.com/zyno-io/v8s/releases).

**In a browser.** Download the CLI archive for your platform, then:

```sh
v8s serve
```

It listens on `127.0.0.1:7931` and opens your browser. `v8s serve` has no login of its own. Do not expose its port.

**Container (hosted mode).** The image's default command is `server --config /etc/v8s/config.yaml`:

```sh
docker run --rm -p 7931:7931 \
  -v $PWD/config.yaml:/etc/v8s/config.yaml:ro \
  ghcr.io/zyno-io/v8s:0.1.0
```

**Helm chart (hosted mode):**

```sh
helm install v8s oci://ghcr.io/zyno-io/charts/v8s --version 0.1.0 \
  --namespace v8s --create-namespace --values my-values.yaml
```

See [Install](https://docs.v8s.io/guide/install), [Hosted mode](https://docs.v8s.io/guide/hosted), and the [CLI reference](https://docs.v8s.io/guide/cli).

## Build from source

You need Go 1.27 and Node 26 with Yarn 4 (through Corepack).

```sh
yarn install
scripts/build-agent.sh --host   # UI + agent with the UI embedded, in dist/<os>_<arch>/v8s
dist/*/v8s serve                # open the UI in your browser
yarn docs:dev                   # docs site at http://localhost:5173/v8s/
```

Releases are cut by tagging `vX.Y.Z` (`.github/workflows/release.yml`). See [CHANGELOG.md](CHANGELOG.md). Every push to `main`
runs the same workflow unsigned: the CLI archives and desktop installers as run artifacts (7 days), and the container image
as `ghcr.io/zyno-io/v8s:main` (and `:<short sha>`), so a release can be tried live before it is tagged. Signing,
notarization, attestations, the Helm chart and the GitHub release happen on tags only.

## Design docs

The design behind v8s is in [docs/](docs/architecture.md): [architecture](docs/architecture.md), [performance](docs/performance.md), [data model](docs/data-model.md), [problems engine](docs/problems.md), [integrations](docs/integrations.md), [operations and security](docs/operations.md), [authentication](docs/auth.md), [packaging](docs/packaging.md), and the [design system](docs/design-system.md). The [decode spike](spikes/decode-bench/README.md) backs the main performance decisions.

## Stack

Go agent (client-go, Helm SDK) · Vue 3 + Vite + Pinia · Electron for desktop · msgpack over WebSocket.

## License

[MIT](LICENSE)
