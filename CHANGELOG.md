# Changelog

All notable changes are recorded here. The release workflow uses the section matching the tag (for example `## 0.1.0`) as the
release notes when the tag has no annotation.

## Unreleased

## 0.1.0 — 2026-11-17

First release. v8s is a multi-cluster Kubernetes manager for desktop, browser, and phone, built around a Go agent that keeps compact projections of your clusters and sends the UI only the rows on screen.

### Install and run

- Desktop app for macOS (Apple silicon and Intel, signed and notarized), Windows (x64, signed installer), and Linux (x64 AppImage, deb, rpm). The app bundles and supervises its own agent.
- Static `v8s` CLI archives for Linux and macOS (amd64, arm64) and Windows (amd64) with the UI embedded. Commands: `v8s serve` (agent on loopback, serves the UI, opens your browser), `v8s agent`, `v8s server`, `v8s version`, and `v8s help`. A demo mode (`v8s agent --fake`) serves synthetic clusters.
- Container image `ghcr.io/zyno-io/v8s` for `linux/amd64` and `linux/arm64`; its default command is `server --config /etc/v8s/config.yaml`.
- Helm chart `charts/v8s` for hosted mode.
- `checksums.txt` and GitHub build provenance attestations on release files and the image.

### Clusters and navigation

- Multi-cluster selection, groups (with a production flag), remembered selection, and connection states. Clusters connect only when you press Connect. Kubeconfig contexts load like `kubectl`, including exec credential plugins with your login-shell environment.
- Linked read and write contexts with automatic or confirmed switching for writes, built for access gateways such as Warpgate.
- RBAC-aware actions that are disabled with a reason, and a namespace-restricted fallback.
- k9s-style command bar (`:ctx`, `:ns`, `:po`, `:pb`, `:star`), search palette, starred types, bookmarks, deep links, and light and dark themes.
- Keyboard shortcuts: `j`/`k`, `Enter`, `x`, `/`, `:`, `Esc`, `?`, plus `l` (logs), `s` (shell), `e` (edit YAML), and `d` (describe).
- Cluster Overview page with one card per cluster.

### Viewing and changing resources

- Virtualized tables over tens of thousands of rows with sort, filter, tone chips, column layouts, and bulk selection.
- Detail drawer with Overview, Logs, Shell, Inventory, Release, Report, Security, Flows, YAML, Events, and Related tabs.
- YAML edit with server-side dry run, diff, `managedFields` ownership warnings, GitOps banner, and conflict handling. Create from templates or YAML with server-side apply.
- Actions: scale, restart, roll back, cordon, uncordon, drain (pre-flight, resumable job), CronJob suspend, resume, and trigger, delete, with typed confirmations in production groups.
- Secrets are never watched in full. Values load only on **Reveal all** for the whole object.
- Logs (single, merged workload, previous instance, search, download), pod shell with ephemeral debug containers, node shell, and port-forward on desktop and `v8s serve`.
- Jobs tray and a short in-memory recent-activity list.

### Problems

- Problems view across all selected clusters with 51 classes in 12 families, rollups by workload, severity and family filters, grouping, cross-cluster aggregation, and acknowledge and snooze. Acknowledgements survive object recreation and are collected after 7 days unseen.
- Detectors for pods, workloads, jobs, nodes, storage, networking, Gateway API, cert-manager, API services, webhooks, Flux, Helm, Kyverno, Trivy, Kubescape, and Hubble. Defaults are listed in the [Problems guide](https://zyno-io.github.io/v8s/guide/problems).

### Integrations

- **Flux:** all source, kustomize, helm, notification, and image kinds, overview, Kustomization inventory, dependency graph, suspend, resume, reconcile, reconcile with source, force, and reset failures.
- **Helm:** metadata-only release list, history, values, manifest, notes, hooks, revision diff, roll back, uninstall, and retry, with a guard for Flux-managed releases.
- **Kyverno:** policies and reports (`wgpolicyk8s.io` and `openreports.io`), Audit and Enforce switch for CEL validating policies, exception templates, classic policies read-only.
- **Trivy Operator and Kubescape:** vulnerabilities by image across clusters, CVE tables, report views, a per-workload Security tab, Trivy rescan (report deletion), and Kubescape scan now.
- **Hubble:** flows tab with relay-side filters, drops view, service map, Hubble status, and network problem classes, through a port-forward carrying your own credential.

### Hosted mode

- `v8s server`: self-hosted multi-user mode with OIDC login (`oidc`), a cluster app registration whose ID token clusters receive (`clusterApp`, reusing the login tokens when it has the same `clientId`), `oidc-passthrough`, `impersonation`, and read-only `kiosk` strategies, IdP-group cluster visibility, per-user engines that stop 30 seconds after the last client closes, rate limits, and a `__Host-` session cookie. Per-user settings are JSON files under `dataDir`.
- Sign-in banner for lapsed cluster tokens, and logout to `/?signedout=1`.
- Helm chart with values for the whole server configuration, secrets by environment variable or file, persistence, and ingress.

### Phone

- Problems-first layout with a bottom bar, cards instead of tables, narrow subscriptions, and hold-to-confirm for destructive actions, served by hosted mode.

### Not in this release

Installable PWA, offline mode, and web push; a hosted shared cache; cloud token exchange and per-cluster audiences; hosted HTTP preview (port-forward is refused in hosted mode); CEL user-defined detectors; Prometheus integration; SQLite or Postgres storage; desktop auto-update; CSV and JSON export of vulnerabilities; Kubescape ApplicationProfile and NetworkNeighborhood viewers.
