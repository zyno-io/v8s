# Product

<!-- impeccable:product-schema 1 -->

> Facts below come from the v8s repository (the repo root, `../..`: README.md, CHANGELOG.md through 0.3.0, docs/, docs/guide/)
> and the owner's brief ("a really nice standalone landing page for v8s.io, same setup as siple.app;
> footer: need help with your Kubernetes…? Zyno Consulting can help, link https://zyno.consulting").
> Items marked *(inferred)* were not confirmed by the owner.

## Platform

web

## Stack

Astro (static output, npm), pinned by the owner: the same setup as `siple-web` (siple.app):
`src/{components,data,layouts,lib,pages,scripts,styles}`, Playwright VRT, `scripts/og.mjs`, GitHub Actions
(`.github/workflows/www.yml` in the v8s repo) publishing `dist/` to ZynoHosting (`www-staging.v8s.io` automatically from `main`,
`v8s.io` dispatched by release.yml once a release is published). Domain: `v8s.io`.

## Users

- **Platform, SRE and DevOps engineers** who run several Kubernetes clusters (production, staging, per-region)
  and today juggle `kubectl`, k9s, Lens/Headlamp-style UIs and a pile of dashboards. They want one fast view
  across clusters, a list of what is broken, and safe day-to-day operations. *(inferred from README and docs)*
- **Teams running GitOps and policy tooling**: Flux, Helm, Kyverno, Trivy Operator, Kubescape, Cilium Hubble.
- **On-call engineers on a phone**, through a team's self-hosted v8s with OIDC sign-in.

## Product Purpose

v8s (short for **Visual k8s**, confirmed by the owner) is a fast, multi-cluster Kubernetes manager for desktop, browser and phone. A Go agent keeps compact
projections of your clusters and sends the UI only the rows on screen, so 10k+ objects per cluster, and
several clusters at once, stay smooth. The site's job: explain what v8s is and why it is different in
seconds, get a visitor to download the desktop app (or run `v8s serve`, or deploy hosted mode), and send
them to the docs. Success = a download or a docs visit.

## Positioning

Built around a performance budget, multi-cluster by default, and your own credential all the way through.
Design budgets on a laptop: 60 fps scrolling over 50k pods, 20 ms sort and filter, under 50 MB of agent
memory per 10k pods. A Problems view (51 classes in 12 families) spans every selected cluster. No v8s cloud,
no telemetry; the agent runs on your machine or in your own infrastructure. One UI and one Go binary give
the desktop app, `v8s serve`, and a self-hosted multi-user server.

## Operating Context

- Released: **v0.3.0 (2026-11-18)** (first release v0.1.0, 2026-11-17), published on GitHub (`zyno-io/v8s`). Assets:
  `v8s-<ver>-mac-arm64.dmg`, `v8s-<ver>-mac-x64.dmg` (signed and notarized), `v8s-<ver>-win-x64.exe`
  (signed installer), `v8s-<ver>-linux-x86_64.AppImage`, `v8s-<ver>-linux-amd64.deb`,
  `v8s-<ver>-linux-x86_64.rpm`; CLI archives `v8s_<ver>_{darwin,linux}_{amd64,arm64}.tar.gz`,
  `v8s_<ver>_windows_amd64.zip`; `checksums.txt`; build provenance attestations.
- Container image `ghcr.io/zyno-io/v8s` (linux/amd64, arm64); Helm chart `oci://ghcr.io/zyno-io/charts/v8s`.
- Docs: https://docs.v8s.io/ (VitePress; moved there in 0.2.0). Repo: https://github.com/zyno-io/v8s.
- Demo mode: `v8s agent --fake clusters=3,pods=50000` serves synthetic clusters.

## Capabilities and Constraints

Confirmed (README, CHANGELOG 0.1.0 to 0.3.0, docs): multi-cluster selection and groups; a per-cluster Protected flag (lock) for typed confirmations;
clusters connect only when you press Connect; kubeconfig loads like `kubectl` incl. exec plugins; linked
read and write contexts for access gateways such as Warpgate; RBAC-aware actions; k9s-style command bar
(`:ctx`, `:ns`, `:po`, `:pb`), search palette, shortcuts (`l` logs, `s` shell, `e` edit YAML, `d` describe);
virtualized tables; detail drawer; YAML edit with server-side dry run and diff; scale, restart, rollback,
cordon, drain, CronJob trigger; secrets never watched in full, values only on Reveal all; logs, pod and node
shell, port-forward; Problems view with acknowledge and snooze; Flux (reconcile, suspend, inventory,
dependency graph); Helm (history, revision diff, rollback, Flux-managed guard); Kyverno (Audit/Enforce);
Trivy Operator and Kubescape findings by image; Hubble flows, drops, service map; hosted mode with OIDC,
passthrough, impersonation and kiosk strategies; Problems-first phone layout.

Added in 0.2.0 to 0.3.0 (CHANGELOG, docs/guide):

- **CPU and memory (0.3.0, docs/guide/resources.md "CPU and memory")**: the Pods table shows CPU (millicores) and MEM
  (MiB), each with %R and %L (percent of the pod's request and limit), as one dense k9s-style block of right-aligned
  integers with units in the header; requests, limits and % of the node are in Columns. `—` = no data, `n/a` = nothing
  to divide by; amber from 90 % of a limit, red from 100 % (node share amber from 90 %; requests never coloured);
  sortable, missing values always last; hover for used/requested/limit/node share; phone cards show
  `cpu 16m · mem 163Mi`. Nodes: usage, % of allocatable, and the allocatable. Overview, per cluster: usage as a % of
  all nodes' allocatable, a bar with a tick at requests, `requests 62% · limits 143%`, `41.2 of 112 cores`. Needs
  metrics-server and `metrics.k8s.io` access; read every 30 s only while shown, for the namespaces on screen; without it
  usage columns are hidden and Columns says why.
- **Search by IP (0.2.0, docs/guide/navigation.md)**: address, partial address, CIDR, range, IPv6 in the search palette;
  pods (incl. Multus secondary IPs), Services and LB IPs, EndpointSlices, Nodes and pod CIDRs, Ingresses, Gateways, and
  (full address, CIDR or range) ServiceCIDRs, MetalLB pools and BGP peers, Multus NetworkAttachmentDefinitions, Cilium
  pools and nodes, Calico IP pools. `:ip <address>`. The pods IP column is hidden by default since 0.3.0.
- **Events tab (0.2.0)** shows every event of the object, Normal and Warning, newest first, live while open.
- **Protected clusters (0.2.0, docs/guide/clusters.md)**: the lock in the selector; delete, scale, roll back, drain, Helm
  actions, Kyverno mode changes, Trivy rescans, Kubescape scans and Problems quick actions ask you to type the name;
  replaces the Production flag on groups (groups are only for organizing).
- **Hosted public OIDC client (0.2.0)**: `publicClient: true`, no secret (the Entra app `kubelogin` uses; redirect URI
  under Mobile and desktop applications). Server-defined `displayGroups` appear as read-only groups.
- **Installable web app (0.2.0)**: manifest and icons, Chrome offers Install for the hosted app.
- **Auto-reconnect (0.2.1)** of the UI to the agent (backoff up to 15 s, "Reconnecting…" bar, resubscribes views);
  **collapsible, resizable sidebar** (unpin or `[`; hover the left edge to open it over the content).

Still not shipped (never claim): offline mode, web push, desktop auto-update, Prometheus, Argo CD,
CSV/JSON export, hosted port-forward (the web app is installable, nothing more).

Pre-1.0. Never claim: user counts, customer logos, testimonials, ratings, prices, benchmarks beyond the
stated design budgets (they are budgets, not measured results).

## Brand Commitments

- Name **v8s**, always lower case.
- Mark: `docs/public/logo.svg`: a white "v" stroke with a dot on an indigo (`#4f46e5`) rounded square.
- The app's own visual system is the **PixelCI design system** (cool grey desk, white hairline cards, one
  indigo accent, status as tag triplets, Inter, Font Awesome Free) with v8s's indigo (`#4f46e5` light,
  `#5b52f0` dark), from `packages/ui/src/styles/tokens.scss`.
- Licence: **MIT**. Open source.
- Maker: Signal24 LLC dba Zyno Consulting. The footer carries a Zyno Consulting pitch for Kubernetes help,
  linking to https://zyno.consulting (owner's brief). v8s never requires Zyno Consulting.

## Evidence on Hand

- Real UI, captured from demo mode (`v8s agent --fake`, synthetic clusters), light and dark, into
  `public/shots/` (owner approved). No real customer cluster data.
- Real commands, config, command-bar syntax and problem classes from the v8s docs.
- No testimonials, customer logos, usage numbers, press or measured benchmarks exist; do not fabricate them.

## Product Principles

1. Show the real app; no mockups that drift from it.
2. Speed is the product: say how (agent projections, rows on screen) rather than just "fast".
3. Your clusters, your credentials: nothing leaves your infrastructure.
4. Honest scope: say what this release does and what it doesn't.

## Accessibility & Inclusion

WCAG 2.2 AA: contrast in both themes, visible focus, alt text on every screenshot, reduced motion respected,
usable at 320–390px. *(inferred)*
