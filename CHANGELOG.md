# Changelog

All notable changes are recorded here. The release workflow uses the section matching the tag (for example `## 0.1.0`) as the
release notes when the tag has no annotation.

## Unreleased

### Fixed

- A list that could not load now loads on its own when that can change: Secrets your read context may not list appear as soon as you elevate the cluster, and a type none of the selected clusters served appears once you select one that does (or its CRD is installed), instead of staying empty until you leave the page. A list you are not allowed to read now says so instead of "None of the selected clusters serves …".
- Elevating or reverting a cluster shows on its status chip at once (a spinner and `elevating…` / `reverting…`) until the cluster switches, and its open lists start reloading at once instead of after that cluster's API discovery (half a minute or more when discovery had to be retried).
- A list that is still loading on a cluster now says so (`Loading secrets from prod-eu…`, and `loading secrets…` on that cluster's chip) instead of `No secrets.`; the chip used to say `synced` as soon as the cluster's pods had loaded. This shows when you first open a large list such as Secrets, and right after elevating.

## 0.8.0 — 2026-11-22

### Added

- `:ns +<namespace>` and `:ns -<namespace>` add a namespace to, or remove one from, the current view's namespace filter instead of replacing it. Several can be combined (`:ns +shop -payments`), and `-` completes from the namespaces in the filter.

### Changed

- The Namespace menu and the cluster selector show what you have picked under **Selected**, below the list, so the picks stay in view while you search or scroll. Click one there to remove it.

## 0.7.0 — 2026-11-22

### Added

- **Elevate a linked cluster.** For a cluster with a linked write context, click its status chip above a table (or the button on its Overview card): **Elevate to `<write context>`** runs that cluster's reads, watches, Problems and writes as the write context without touching the other clusters; **Revert** goes back. The cluster's data reloads each time, and while elevated its chip and tags turn amber and name the write context. Elevation lasts until you revert, deselect the cluster, or restart v8s.

### Fixed

- Namespace lists (the table's Namespace menu, the command bar, the Create dialog) update while open as clusters connect, instead of needing a close and reopen.
- New custom resource types appear without a reconnect. The agent now tells the UI when a cluster's discovered types change (a CRD installed or removed), re-checks a few seconds after CRD activity to cover discovery lag, and polls every minute instead of every five when your context cannot list CRDs.

## 0.6.1 — 2026-11-20

### Changed

- **Can't reach the agent** is now a red alert banner instead of an amber warning: the app cannot load or change anything until the connection is back.
- Tables: faster fast scrolling on large lists. A row that scrolls out of view now lends its element to the one scrolling in, instead of one row being removed and another created: about a quarter less script work and half the garbage collection per frame while flicking through 50k pods, and far fewer dropped frames on slower machines.

### Fixed

- v8s no longer floods the API server or a gateway in front of it when a proxy truncates watch-list streams or closes watches early. A truncated watch-list makes that resource fall back to LIST + watch (logged once as a warning), and repeated early closes back off from 1 s up to 60 s instead of re-watching immediately.

## 0.6.0 — 2026-11-20

### Added

- **Vertical Pod Autoscaler.** Pods whose requests VPA set (the admission controller's `vpaUpdates` annotation) show a **VPA** tag just before the usage block. The usage tooltip names the VPA, so high usage against the request is clearly VPA's to handle. The tag is coloured by the VPA's status: green **Provided**, amber **LowConfidence** or **NoPodsMatched**, red **ConfigUnsupported**, grey while pending. The column can be sorted. It appears only when a selected cluster has a pod that VPA manages, so clusters without VPA, or with VPA installed but unused, keep the full width. The pod's Overview shows the VPA's recommendation for each container (target, lower, upper, uncapped) next to the current request, with an **Open VPA** button. VerticalPodAutoscalers get a curated list: target, update mode, status, and the target, lower and upper CPU and memory as a usage block. Their detail view shows each container's recommendation. VPA objects are watched only while a VPA view is open; the pod tag comes from the pod itself, with no extra reads.
- Vulnerabilities: **Unknown** in the Exposed via column. While a cluster's Services, endpoints, pods or Ingresses are still loading, or cannot be read with your permissions, its images show `?` instead of `-`; they never count as **Not exposed**, the filter offers **Unknown** to find them, and the list re-reads every 10 s a few times until they resolve.

### Fixed

- Vulnerabilities and the CVE table: rows no longer overlap after the window is resized between phone and desktop widths.

## 0.5.0 — 2026-11-19

### Added

- **Version and update check.** Hover the logo to see the running version (also in the hosted account menu and the phone More sheet). The agent checks GitHub once a day for a newer stable release; when there is one, an **Update** pill (hosted: **Server update**) appears next to the theme button with a link to the release notes and a per-version **Dismiss**. Development builds never check, fake mode makes no request, and nothing is sent but a plain `GET`. Turn it off with `--no-update-check`, `V8S_UPDATE_CHECK=off`, or `updateCheck: false` in the hosted config.
- **k9s-style row keys** on resource tables: `Ctrl+D` delete, `Shift+J` jump to the owner (pod → ReplicaSet → Deployment, Job → CronJob), `r` restart, `Shift+F` port-forward, `o` open the pod's node, `y` YAML, `c` / `n` copy the name / namespace. They act on the focused row (or the selection, or the object open in the drawer) through the same actions, permission checks and confirmations as the buttons; a key always asks first, with **Cancel** focused. `:ns` with no namespace opens the Namespaces list.
- **Remembered per browser:** the clusters you last connected (as the group when they are exactly one), the last page (opening `/` returns to it), and the namespace filters of each cluster set. When only one cluster is configured, v8s connects to it without asking.

### Changed

- Dropdowns: every native select is now a **smart select**: a button that opens a list with a search field when there are eight or more options, arrow, Home/End and type-ahead keys, and highlighted matches. On a phone it opens as a bottom sheet with 44 px rows. It draws above dialogs and drawers and renders only the rows in view.
- Tables: a focused row no longer also draws a focus ring around the whole table.
- Releases: every push to `main` builds the CLI, the desktop installers and the container image (`ghcr.io/zyno-io/v8s:main` and `:<short sha>`) unsigned, so a release can be tried before it is tagged; signing, notarization, attestations, the Helm chart and the GitHub release stay tag-only.

### Fixed

- Problems: the acknowledge / snooze menu opens at its button instead of far away, and no longer scrolls the list.

## 0.4.0 — 2026-11-19

### Added

- Vulnerabilities: an **Exposed via** column shows how each image's workloads can be reached: Ingress (including Gateway API HTTPRoute and GRPCRoute), LoadBalancer, NodePort or Service. It is worked out from the Services, endpoints, Ingresses and routes v8s already watches, with no extra API reads. The column header chooses what counts as exposed (Ingress, LoadBalancer and NodePort by default; a plain Service is cluster-internal, so it is off and those images show `-`). The filter bar's **Exposed via** checkboxes filter by kind or **Not exposed**. Sortable; on a phone the card lists the kinds.

### Changed

- Pods and Nodes: each **CPU and memory percentage cell carries a bar** that fills the row from the bottom by its value: green below 70 %, amber from 70 %, red from 90 %. A request percentage gets a bar only when the pod has no limit for that resource. The number on it is a shade softer and no longer coloured.
- Pods: next to a limit, the **memory request percentage turns amber above 100 % and red above 200 %**, since pods over their memory request are evicted first when a node runs short of memory. CPU request percentages stay plain.
- Pods and Nodes: **hover a percentage header** for what its colours mean.

## 0.3.2 — 2026-11-18

### Changed

- Keyboard: **Enter in the filter moves focus to the results** (Resources and Problems), so `j`/`k` and the arrow keys move the row focus straight away, and **Esc from the list clears the filter** and keeps focus on the list; a second Esc continues with the selection, then the drawer. The arrow keys now move the row focus whenever the list has focus.

## 0.3.1 — 2026-11-18

### Fixed

- Fake mode (`--fake`, the demo and screenshots): nodes are **sized from the pods**, so the Overview capacity card shows a plausible cluster (about 30–70 % CPU used) instead of over 200 % with the default 50,000 pods.

### Changed

- Problems: the family chips are **one line that scrolls sideways** (like the Policies tabs) and share the row with the search box when there is room, instead of wrapping onto several lines.

## 0.3.0 — 2026-11-18

### Added

- **CPU and memory in the Pods table**, from metrics-server: usage, and usage as a percentage of the pod's request and limit, in one dense block (CPU in millicores, memory in MiB, units in the header). Requests, limits and the percentage of the node are in the Columns menu. Hover the block for used, requested, limit and node share; amber from 90 % and red from 100 % of a limit; sort by any of them (missing values always last). On a phone each card shows `cpu 16m · mem 163Mi`. Usage is read only while a table shows it, every 30 seconds, for the namespaces on screen, and a cluster without metrics-server shows no usage columns and says why.
- **CPU and memory in the Nodes table**: usage, usage as a percentage of the node's allocatable, and the allocatable itself, as the same dense block (metrics-server needed for usage; allocatable always shows).
- **CPU and Memory on the Overview**, per cluster: usage as a percentage of the allocatable of all nodes (amber from 90 %, red from 100 %), a bar with a tick at the requests share, `requests 62% · limits 143%` and `41.2 of 112 cores`. Without metrics-server it shows the requested share and says usage needs metrics-server.

### Changed

- Nodes: the **OS image, kernel, runtime and taints** columns are hidden by default (Columns brings them back), and **CPU / Memory allocatable are numeric**, so they sort numerically instead of as text.
- The pods **IP** column is hidden by default (turn it on from Columns; IP search and filtering are unaffected), and the Namespace, Status, Restarts, Node and Age columns are narrower, so the usage block fits a 1440 px screen with the sidebar pinned.

### Fixed

- Columns that are hidden by default no longer **reappear after you resize a column**.
  Saved layouts keep their hidden list but no longer reveal default-hidden columns: Flux columns you had shown (`requestedAt`, `lastHandled`, `dependsOn`) may need to be turned on again from Columns.

## 0.2.2 — 2026-11-18

### Fixed

- **Hubble status** no longer says "Why are there no flows?" while flows are arriving: that heading now appears only when the relay has seen none, otherwise "Some flows may be missing". Cilium's default flow buffer (4,095 per node) is no longer reported as small; the per-node figure divides by connected nodes only.

### Internal

- The agent's real-cluster e2e suite runs in CI (nightly, on demand, and on PRs that change it) against a seeded kind cluster and a Cilium cluster.

## 0.2.1 — 2026-11-18

### Added

- **Collapsible, resizable sidebar.** Unpin it (or press `[`) to give the content the full width; hover the left edge or click the toggle by the logo to open it over the content. Drag its edge to resize (keyboard too); width and pin state are saved in your settings.

### Changed

- Problems: **Merge across clusters** moved into the grouping menu, and **Acknowledged** is a toggle next to the search box, so the checkboxes no longer take their own line.
- Counts read "721 in 2 clusters" instead of "across".
- In the sidebar, Nodes, Namespaces and Events sit under **Cluster** (they looked like part of Network flows).

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
