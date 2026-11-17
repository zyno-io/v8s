# v8s

A fast, multi-cluster Kubernetes manager for desktop, web, and phone.

[![CI](https://github.com/zyno-io/v8s/actions/workflows/ci.yml/badge.svg)](https://github.com/zyno-io/v8s/actions/workflows/ci.yml)
[![Docs](https://github.com/zyno-io/v8s/actions/workflows/pages.yml/badge.svg)](https://zyno-io.github.io/v8s/)
[![Release](https://img.shields.io/github/v/release/zyno-io/v8s?include_prereleases)](https://github.com/zyno-io/v8s/releases)

**Documentation: <https://zyno-io.github.io/v8s/>**

> **Status: design phase.** This repository currently holds the architecture and product plan and one performance spike. There is no runnable application yet.

## What it is meant to be

- **Fast on big clusters.** 10k+ objects per cluster, several clusters at once, smooth scrolling and instant filtering. Every architectural choice is justified against a latency, memory, or frame budget.
- **Multi-cluster by default.** Pick clusters or a group (`production`, `staging`); every list, search, and the Problems view span the selection. v8s only ever connects to clusters you selected.
- **A Problems view** across all selected clusters: crash loops, failed Jobs, NotReady nodes, Ingresses with no endpoints, failed Helm releases, unhealthy Flux objects, policy failures, critical vulnerabilities, dropped network flows. Filterable by class.
- **First-class Flux, Helm, Kyverno, Trivy, Kubescape, and Hubble**, not just generic CRD browsing.
- **Day-to-day operations**: edit with dry run and diff, pod shell, node shell, cordon/drain, logs, port-forward.
- **Desktop app** (macOS, Windows, Linux) and a **self-hosted web app** that works in desktop and phone browsers.
- **Your credential, all the way through**: every request carries the user's own credential, to the API server directly or through an access gateway such as [Warpgate](https://github.com/warp-tech/warpgate), with optional linked read/write contexts that switch automatically for writes.

## Build

```sh
yarn install
scripts/build-agent.sh --host   # UI + agent with the UI embedded, in dist/<os>_<arch>/v8s
dist/*/v8s serve                # open the UI in your browser
yarn docs:dev                   # docs site at http://localhost:5173/v8s/
```

See the [documentation site](https://zyno-io.github.io/v8s/) for guides; releases are cut by tagging `vX.Y.Z` (`.github/workflows/release.yml`).

## The plan

Start with [docs/architecture.md](docs/architecture.md). The performance chapter, [docs/performance.md](docs/performance.md), is the core of the design.

| Document                                  | Topic                                                                                                 |
| ----------------------------------------- | ----------------------------------------------------------------------------------------------------- |
| [architecture.md](docs/architecture.md)   | Vision, landscape, architecture, decisions, roadmap, decisions log, repo layout                       |
| [performance.md](docs/performance.md)     | Watch strategy, agent store and views, wire protocol, rendering, streaming, budgets, CI               |
| [data-model.md](docs/data-model.md)       | Identity, resource coverage matrix, CRDs, stars and bookmarks                                         |
| [problems.md](docs/problems.md)           | Problems engine and view                                                                              |
| [integrations.md](docs/integrations.md)   | Flux, Helm, Kyverno, Trivy, Kubescape, Hubble                                                         |
| [operations.md](docs/operations.md)       | Editing, shells, drain, logs, port-forward, security                                                  |
| [auth.md](docs/auth.md)                   | Credential pass-through on desktop and hosted, access gateways (Warpgate), linked read/write contexts |
| [packaging.md](docs/packaging.md)         | Electron desktop, hosted server, mobile web                                                           |
| [design-system.md](docs/design-system.md) | Design system                                                                                         |

## Spikes

- [spikes/decode-bench](spikes/decode-bench/README.md): what decoding, caching, and moving 10k–25k pods actually costs in V8 and Go. Its numbers back the main performance decisions.

## Stack (planned)

Go agent (client-go, Helm SDK) · Vue 3 + Vite + Pinia · Electron for desktop · msgpack over WebSocket.

## License

[MIT](LICENSE)
