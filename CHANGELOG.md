# Changelog

All notable changes are recorded here. The release workflow uses the section matching the tag (for example `## 0.1.0`) as the
release notes when the tag has no annotation.

## Unreleased

### Fixed

- **"Disconnected from the agent" no longer needs a reload.** The page reconnects by itself after a server restart, a proxy idle timeout, sleep or a network change (backoff up to 15 s, immediately when the network returns or the tab is shown again), resubscribes every view and keeps showing the last rows meanwhile. A small "Reconnecting…" bar replaces the red banner; after a minute it offers **Retry now**. The agent now pings every socket every 25 s so idle connections survive proxies.

## 0.2.0 — 2026-11-18

### Added

- **Search by IP.** Type an address, a partial address (`10.42.`), a CIDR, an `a-b` range or an IPv6 address in the search palette to find Pods (including Multus secondary IPs), Services and their load-balancer IPs, EndpointSlices, Nodes and their pod CIDRs, Ingresses and Gateways. A full address, CIDR or range also searches IP-only CRDs the cluster has: Service CIDRs, MetalLB pools and BGP peers, Multus NetworkAttachmentDefinitions, Cilium pools and nodes, and Calico IP pools; those are watched only while a search needs them. Pods get an IP column, and list filters match IPs too. `:ip <address>` opens the search.
- **Events tab shows every event** of an object, Normal and Warning, newest first, loaded when you open the tab and live while it is open (the background watch still keeps Warning events only).
- **Protected clusters.** Click the lock at the end of a cluster's row in the selector. On a protected cluster, delete, scale, roll back, drain, Helm actions, Kyverno mode changes, Trivy rescans, Kubescape scans and the Problems quick actions ask you to type the name; a small lock marks the cluster everywhere it appears. This replaces the Production flag on groups: groups are now purely for organizing. Clusters in a group named `production` used to get typed confirmations by default and no longer do; protect them with the lock.
- **Hosted mode with a public OIDC client** (`publicClient: true`): sign in with an app that has no secret, such as the Entra app `kubelogin` uses (redirect URI under _Mobile and desktop applications_). The docs explain which Entra platform to pick.
- **Installable web app**: a manifest and icons, so Chrome offers "Install" for the hosted app.
- **Server-defined groups** (hosted `displayGroups`) appear in the selector as read-only groups.

### Changed

- **Linked contexts dialog**: linking a write context moved from a control revealed on row hover to a "Linked contexts…" dialog in the selector footer, with all suggested pairs and "Link all".
- **Labels and annotations** are expanded by default; labels are chips, annotations are blocks with long values collapsed behind "Show all".
- The search box is disabled until at least one cluster is connected. In the selector, a linked write context sits on its own line under the URL.
- A click anywhere on a table row opens it in the drawer (it used to need the name).
- Docs moved to **https://docs.v8s.io**.
- Quieter logs and fewer API calls for read-only users: client-go's log lines go to the agent's logger at debug level, and a background watch the user may not list is retried every 5 minutes instead of every ~30 seconds (it is still reported as unavailable in Problems).

### Fixed

- **Hosted mode**: the data connection failed in every real browser (WebSocket handshakes carry no `Sec-Fetch-Site`); the favicon was blocked by the content security policy; log downloads failed in Safari.
- **Linked write context on gateways**: when the read context cannot even run an access check (a gateway's read-only target refusing it), actions now use the linked write context, with a hint to activate its elevated session, instead of "unable to check". Reads and transient failures never switch.
- The log and flow viewers no longer paint blank rows while following a fast stream.

## 0.1.0`) as the

release notes when the tag has no annotation.

## Unreleased

- Quieter logs and fewer API calls for read-only users: client-go's klog lines (such as "Failed to watch ... forbidden") no longer flood stderr (they go to the agent's logger at debug level), and a baseline watch the user is not allowed to list is stopped and retried every 5 minutes (or when you open that kind) instead of being listed again every ~30 seconds. The kind is still reported as unavailable in Problems.

- Replaced the Production flag on cluster groups with a per-cluster Protected flag. Groups are now purely for organizing clusters. Click the lock at the end of a cluster's row in the selector to protect it. On a protected cluster, delete, scale, roll back, drain, Helm actions, Kyverno mode changes, Trivy rescans, Kubescape scans and the Problems quick actions ask you to type the name to confirm; restart, cordon, suspend, trigger and Flux actions on a single object show a confirm naming the cluster. A small lock marks the cluster in the selector, tables, the drawer, Problems and search. Clusters in a group named `production` used to get typed confirmations by default (that was the default production list) and no longer do: protect them with the lock. The old setting is ignored, with no migration.

- IP search also reads IP-only CRDs when you search for a full address, CIDR or range: Service CIDRs, MetalLB pools and BGP peers, Multus NetworkAttachmentDefinitions, Cilium pools and nodes, and Calico IP pools. They are watched only while a search needs them and then stop after a few minutes; a short prefix such as `10.` never starts them.

- The Events tab of a resource now shows all of its events, Normal and Warning, newest first, with the component that reported each one (the new Source column also appears in the global Events list). The events load when you open the tab and stay live while it is open; the always-on watch still keeps Warning events only. Events of cluster-scoped objects come from the `default` namespace, and Event objects themselves no longer offer an Events tab.
- Search by IP: type an address, a partial address (`10.42.`), a CIDR, a range or an IPv6 address in the search palette to find the Pods, Services, EndpointSlices, Nodes, Ingresses and Gateways that hold it, grouped by kind, with exact matches first. List filters take the same text (`:po 10.42.`), `:ip <address>` opens the palette, and Pods have a new IP column.

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
- Detectors for pods, workloads, jobs, nodes, storage, networking, Gateway API, cert-manager, API services, webhooks, Flux, Helm, Kyverno, Trivy, Kubescape, and Hubble. Defaults are listed in the [Problems guide](https://docs.v8s.io/guide/problems).

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
