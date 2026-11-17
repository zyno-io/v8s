# v8s architecture and product plan

**Status:** proposal for review. Nothing here is built yet beyond a [decode spike](../spikes/decode-bench/README.md).

v8s is an open-source, multi-cluster Kubernetes manager built around one constraint: **it must stay fast on large clusters, many clusters at once, on a laptop and on a phone.** It ships as a desktop app and as a hosted web app (installable as a PWA), with first-class Flux, Helm, Kyverno, Trivy, Kubescape, and Hubble support and a cross-cluster Problems view.

## How to read this plan

| Chapter | What it covers |
| --- | --- |
| **This document** | Vision, non-goals, competitive landscape, system architecture, key decisions, roadmap, open questions, repo layout |
| [performance.md](performance.md) | **The most important chapter.** Ingest and watch strategy, agent store and view engine, wire protocol, UI rendering, streaming, hosted fan-out, budgets, what not to do, CI benchmarks |
| [data-model.md](data-model.md) | Object identity, resource coverage matrix (MVP vs later), generic CRD support, stars and bookmarks, persisted state |
| [problems.md](problems.md) | Problems engine: detectors, incremental evaluation, problem classes, cross-cluster merge, filter UX |
| [integrations.md](integrations.md) | Flux, Helm, Kyverno, Trivy Operator, Kubescape, Hubble: data sources, surfaces, actions, problems, performance |
| [operations.md](operations.md) | Editing, pod shell, node shell, cordon/drain, logs, port-forward, idempotency, audit, hosted security |
| [auth.md](auth.md) | Kubeconfig and exec plugins on desktop; OIDC pass-through, cloud token exchange, and impersonation for hosted |
| [packaging.md](packaging.md) | Electron desktop, `v8s serve`, hosted server, PWA and what works on a phone |
| [design-system.md](design-system.md) | Mapping onto the PixelCI / CI Dashboard design system, plus the dense-table and mobile extensions |

## Vision

v8s is the Kubernetes manager you keep open all day because it is **instant**, it shows **every selected cluster at once**, and it tells you **what is broken** before you go looking. It treats the tools a modern platform team actually runs (Flux, Helm, Kyverno, Trivy, Kubescape, Cilium/Hubble) as part of the product, not as CRDs to squint at.

Three bets:

1. **Performance is the feature.** Opening a 10k-pod namespace, switching clusters, filtering, and scrolling must feel like a native app on a laptop and stay smooth on a phone. Every architectural decision in this plan is justified against a latency, memory, CPU, or frame budget (see [performance.md](performance.md)).
2. **Multi-cluster by default.** You pick a set of clusters (or a group like `production`), and every list, search, and the Problems view spans that set. v8s never connects to a cluster you have not selected, and never selects all by default.
3. **Problems, not dashboards.** One cross-cluster, filterable list of things that are wrong, including failed Flux reconciliations, failed Helm releases, policy failures, critical vulnerabilities, and dropped flows, with the evidence and the fix one click away.

It ships as a **desktop app** (macOS, Windows, Linux) and as a **hosted web app** that installs as a **PWA** on phones, from one Vue codebase and one agent.

## Non-goals

- **Not a cluster provisioner.** No creating clusters, node pools, or cloud resources (Rancher, cloud consoles, and Terraform own that).
- **Not a CD system.** v8s operates Flux and Helm; it does not replace them or invent its own deploy pipeline. Edits to GitOps-managed objects are discouraged in the UI, not blocked.
- **Not an observability stack.** No long-term metrics, log storage, or tracing. v8s streams live logs and Hubble flows and shows current metrics-server data; history belongs in Prometheus/Loki/Tempo, which v8s may link to later.
- **Not a policy or security scanner.** v8s reads Kyverno, Trivy, and Kubescape results; it never scans images or evaluates policies itself.
- **Not an RBAC administration product.** It shows effective permissions and respects them; managing IdPs and org-wide RBAC is out of scope.
- **No plugin marketplace in v1.** Integrations are in-tree modules with a stable internal interface; third-party plugins come later if the interface proves itself.
- **No Windows node shell, no multi-tenant SaaS billing, no AI assistant** in the MVP.

## Competitive landscape

| Product | Model | Strengths | Where it falls short for v8s's goals |
| --- | --- | --- | --- |
| **Lens / OpenLens / Freelens** | Electron; renderer holds a MobX store of full objects per watched kind | Mature, broad resource coverage, extensions, Helm, terminal | Heavy memory on big clusters (full objects in reactive renderer state), one cluster per view, Problems limited to events, Flux only via extensions, Lens itself went commercial/closed; forks lag |
| **k9s** | Go TUI | Extremely fast, keyboard-driven, loved by power users, tiny footprint | Terminal only, one cluster at a time, no cross-cluster view, no mobile, limited Flux/Helm actions via plugins, steep learning curve |
| **Headlamp** (Kubernetes SIG UI) | React; browser talks to a Go backend proxy; desktop via Electron | Web + desktop, plugin system, OIDC in-cluster deploy, multi-cluster | React re-renders full lists, limited virtualization historically, problems view is thin, Flux via plugin |
| **Octant** (VMware) | Go backend + Angular | Was a good resource graph explorer | Archived (2022) |
| **Kubernetes Dashboard** | In-cluster web UI | Was the official UI | Archived in January 2026; SIG UI points users to Headlamp |
| **Rancher** | Full management platform | Multi-cluster, provisioning, RBAC, apps, fleet GitOps | Heavy server install, its own agents in clusters, UI is slow on large clusters, Flux not first-class (uses Fleet), overkill for "show me what's broken" |
| **Weave GitOps / Capacitor / Flux UI** | Flux-specific UIs | Good Flux views | Flux only; no general resource management; Weave GitOps lost its company |
| **Argo CD UI** | Argo-specific | Excellent app tree | Argo only |
| **Komodor, Groundcover, other SaaS** | Commercial SaaS with in-cluster agents | Change tracking, problem detection, history | Closed source, agent per cluster, data leaves your network, priced per node |
| **Aptakube, Seabird, Kubenav, others** | Small desktop/mobile apps | Fast native feel (some Tauri), mobile (Kubenav) | Narrow feature sets, small teams, little Flux/Helm/security integration |

### Why v8s wins

- **Perf by construction**, not by tuning later: the UI never holds full objects for lists, decoding happens once in the agent (protobuf where possible), and only the visible window is reactive. Lens-class memory blowups are structurally impossible (see [performance.md §What not to do](performance.md#12-what-not-to-do)).
- **True multi-cluster**: one table, one search, one Problems view over a chosen set, with groups for preselection. Most tools are one-cluster-at-a-time.
- **First-class Flux and Helm** with the actions people actually use (reconcile with source, force, reset, suspend/resume, rollback, retry stuck releases) and correct GitOps awareness when editing.
- **Security and network in the same place**: Kyverno, Trivy, Kubescape results and Hubble drops feed the same Problems view as crash loops.
- **Real identity in hosted mode**: OIDC pass-through or scoped impersonation, so the API server audit log shows the person.
- **One codebase, three surfaces**: desktop, hosted web, and phone (PWA).
- **Open source** (license: [open question Q2](#open-questions)).

## Architecture

### Process model

**One Go agent in every mode. Only the agent talks to Kubernetes.** The UI is a renderer of views the agent computes.

```
 Desktop                                         Hosted
 ┌──────────── Electron ─────────────┐           ┌──────── browser / installed PWA ────────┐
 │ main: spawn+supervise agent,      │           │ same Vue bundle                          │
 │       updates, menus, keychain    │           │ Data Worker ── WebSocket (session cookie)│
 │ renderer: Vue bundle              │           └───────────────────┬──────────────────────┘
 │   Data Worker ── WebSocket(token) │                               │ HTTPS / WSS
 └───────────────┬───────────────────┘                               ▼
                 │ loopback                     ┌──────────── v8s server (Go) ─────────────┐
                 ▼                              │ OIDC login · cluster registry · identity │
 ┌──────── v8s agent (Go) ─────────┐            │ strategies · shared caches + authz filter│
 │ kubeconfig + exec plugins       │            │ views · problems · passthrough · jobs ·  │
 │ ingest · store · views ·        │            │ audit · SQLite/Postgres                  │
 │ problems · passthrough · jobs · │            └───────┬───────────────────────┬──────────┘
 │ streams · local audit           │                    │ user identity         │ (P5) reverse tunnel
 └───────┬─────────────────────────┘                    ▼                       ▼
         │ user's credentials                    kube-apiservers         v8s connector in a
         ▼                                                               private cluster
   kube-apiservers (N selected clusters)
```

Why one smart agent everywhere:

- **A browser cannot talk to most API servers directly** (no CORS, client certificates, exec plugins, private networks), so a proxy exists in every mode regardless.
- A **dumb proxy** (the Lens and Headlamp pattern) pushes full lists into the renderer. That is exactly how those tools fail at 10k+ objects.
- With decoding, indexing, sorting, windowing, and problem detection in the agent, **UI cost scales with the viewport**. Mobile becomes a window-size setting, not a separate architecture. Desktop, hosted, and phone share one code path.
- **Smart on reads, thin on the long tail.** The agent fully models the read paths (ingest, views, problems, related-object queries). Arbitrary GET/PUT/PATCH/DELETE, subresources, discovery, and OpenAPI go through an authenticated passthrough that adds identity, rate limits, dry run, and audit without modelling every kind.

### Binary modes

| Command | Purpose |
| --- | --- |
| `v8s agent --desktop` | Single user, kubeconfig credentials, loopback only, launched by Electron |
| `v8s serve` | The same, plus serves the embedded UI on localhost and opens the browser |
| `v8s server` | Multi-tenant hosted server |
| `v8s connector` | (P5) In-cluster reverse tunnel to a hosted server for private clusters |
| `v8s auth token --cluster X` | Exec-credential plugin for kubeconfigs that v8s writes (built-in OIDC) |

### Agent components

| Component | Responsibility | Chapter |
| --- | --- | --- |
| `kube` | Clients per cluster and identity, kubeconfig loading, exec plugins, OIDC, rate limits, passthrough | [auth.md](auth.md) |
| `ingest` | Discovery, ListerWatchers that project per page/event, reflectors, watch tiers, linger, GC | [performance.md §4](performance.md#4-ingest-and-watch-strategy) |
| `store` | Handle-addressed projection stores, secondary indexes, diffing `Replace`, counts | [performance.md §6](performance.md#6-agent-store-indexes-and-view-engine) |
| `view` | Normalized queries, materialized sorted views, windows, group-by, sharing | [performance.md §6](performance.md#view-engine) |
| `problems` | Detectors, dirty-set evaluation, deadline heap, problem views | [problems.md](problems.md) |
| `modules/*` | Core kinds, Flux, Helm, Kyverno, Trivy, Kubescape, Hubble: projections, columns, detectors, actions | [integrations.md](integrations.md) |
| `ops` | Exec, logs, port-forward, drain, node shell, Helm actions, bulk actions as resumable jobs | [operations.md](operations.md) |
| `wire` | Data socket, subscriptions, state diffs, coalescing, acks, stream sockets | [performance.md §9](performance.md#9-wire-protocol-and-sync) |
| `hosted` | OIDC login, sessions, identity strategies, authz filter, database, audit | [auth.md](auth.md), [performance.md §10](performance.md#10-hosted-mode-fan-out-and-sharing) |
| `desktop` | Loopback handshake, settings, keychain, local audit | [packaging.md](packaging.md) |

### UI components

- **Vue 3.5 + Vite + Pinia + vue-router + `@zyno-io/vue-foundation`**, matching PixelCI and the CI Dashboard. Pinia holds UI state only.
- **Data Worker** per tab: owns the data socket, decodes, merges, posts at most one update per frame per view.
- **View stores**: `markRaw` row maps plus a `shallowRef` version counter per view.
- **Virtualized fixed-height tables** (`@tanstack/vue-virtual`), CodeMirror 6, xterm.js, canvas graphs, all route-split.
- **Module UIs** (columns, detail tabs, actions per integration) registered against the same view-spec interface as core kinds.

### Multi-cluster connection lifecycle

v8s **never connects to clusters the user has not selected**, and nothing is selected by default on first run. The user checks clusters (or picks a group) and presses Apply. v8s remembers the last applied selection. On next launch the default is to **restore it as a pending selection**: the clusters are pre-checked and a single "Connect" restores them. A setting ("Reconnect last selection on launch") makes that automatic for people who always work on the same set. Either way, only clusters the user chose are ever connected.

```
             select+Apply                 credentials ok           discovery ok
  idle ─────────────────────▶ authenticating ─────────▶ discovering ───────────▶ syncing(n/N) ──▶ ready
   ▲                              │  │                                              │            │
   │                  auth needed │  │ unreachable                         watch    │            │ 410/disconnect
   │                              ▼  ▼                                     error    ▼            ▼
   │                      auth-required  unreachable ◀──────── backoff ─────── degraded ◀──── reconnecting
   │                                                                     (some kinds forbidden    (rows kept, greyed,
   │                                                                      or failing; says which)  "stale since T")
   │ deselect (10 s grace) → stop watches, drop stores, free memory
   └──────────────────────────────────────────────────────────────────────────────────────────────────
```

- Each cluster progresses independently; a slow or broken cluster never blocks others. Its state shows in the cluster selector and in every multi-cluster view's toolbar (`syncing prod-eu 4,210/~12,000`).
- `authenticating` serializes identical exec-plugin commands so one IdP login serves several contexts.
- `degraded` lists what is missing ("no permission to list `ingresses`; Ingress checks disabled").
- `reconnecting` keeps last-known rows visible but greyed with "stale since T"; the outage is itself a `cluster.unreachable` problem.
- Selecting 40 clusters at once is allowed but staged: connections start in batches of 8 to avoid an exec-plugin and API-server stampede.

### Cluster groups

- Groups are named sets of clusters (`production`, `staging`, `eu`), with an optional colour dot and an optional "production" flag that turns on stricter confirms (type-to-confirm, optional reason prompt, optional step-up auth in hosted).
- Desktop: user-defined; v8s **suggests** groups from context-name patterns (`*-prod*`, `*-stg*`) on first run without applying them. Hosted: admin-defined groups plus personal groups.
- Group chips in the cluster selector: click selects exactly that group, shift-click adds it.
- Problem-class presets and default namespaces can be set per group.

## Key decisions

| # | Decision | Chosen | Main alternative | Why (short) |
| --- | --- | --- | --- | --- |
| D1 | Where cluster data lives | Go agent; UI holds only a window | Renderer-side store (Lens) | UI memory and frame time independent of cluster size |
| D2 | Agent language | **Go** | Rust (kube-rs), Node | client-go reflector, protobuf (6× faster decode, measured), exec plugins, WebSocket exec, Helm SDK, kubectl drain, Flux and Cilium Go types; goroutine fan-out for hosted |
| D3 | Desktop shell | **Electron** + agent child process | Tauri, Wails | One pinned Chromium on Linux/macOS/Windows meets frame budgets; WebKitGTK risk on Linux; Tauri's Rust core unused |
| D4 | Wire format | WebSocket, binary frames, msgpack, dictionary-coded positional rows | JSON, protobuf, gRPC-web, WebTransport | Fast JS decoder, compact, Safari-compatible |
| D5 | Sync model | State-based window diffs with acks and coalescing ticks | Op-log of watch events | Bounded per-client memory; reconnect = resend window |
| D6 | Sort/filter location | Agent | UI worker over full dataset | Same path for phone and desktop; UI heap scales with viewport |
| D7 | Ingest | Projection inside ListerWatcher; protobuf for built-ins, Table mode for generic CRDs and reports, metadata-only where enough | client-go informers of full objects + transform | 124–180 MB per 10k pods as typed objects vs 14 MB projected (both measured in the spike) |
| D8 | List fetch | WatchList, else paged LIST (500), RV=0 only for small kinds | Unpaged LIST | Bounded memory on both sides |
| D9 | Hosted caching | Shared per-cluster cache + per-user authz filter; sensitive paths as the user | Per-user watches | 50× less memory and watch load; API server stays the authority for sensitive reads and all writes |
| D10 | Hosted identity | OIDC pass-through preferred; cloud token exchange; scoped impersonation; shared read-only kiosk | Single service account | API server audit shows the person; RBAC is the cluster's |
| D11 | Vue reactivity | `markRaw` data + `shallowRef` version counters + `v-memo` rows | Reactive datasets | Reactive proxies over thousands of objects are the main jank source |
| D12 | Tables | Fixed row height, no row components | Measured rows, component per row | No layout reads during scroll; predictable windows |
| D13 | Editor | CodeMirror 6 | Monaco | ~10× smaller, mobile-capable, virtualizes long docs |
| D14 | Problems | Synthetic resource type in the view engine | Separate subsystem | Merge, sort, window, authz for free |

None of these needed a call from Sean: each has a perf or ecosystem rationale strong enough to decide. Each lists what would reverse it in its chapter.

## Roadmap

Each phase has exit criteria, including performance budgets that must pass in CI. Phases are sequential in emphasis, not strictly gated.

### P0 · Foundations

- Monorepo, CI, lint/format (oxlint/oxfmt as in PixelCI), lefthook.
- Go agent skeleton: kubeconfig loading, one cluster, Pods via projected ListerWatcher + reflector + store; view engine with sort/filter/window; wire protocol with acks.
- Vue shell with design tokens, Data Worker, one virtualized Pods table.
- Electron shell with the loopback handshake.
- Perf harness from day one: kwok 50k-pod fixture, recorded watch streams, `go test -bench` with benchstat, Playwright scroll test.
- Verify the items marked **verify** in [integrations.md](integrations.md) against live installs.

**Exit:** 50k fake pods scroll at 60 fps with ≤ 4 ms scripting per frame; agent ≤ 50 MB per 10k pods; event → row ≤ 150 ms p95.

### P1 · Desktop MVP: browse and edit

- Multi-cluster selection, groups, remember last selection, connection lifecycle states.
- Desktop auth: kubeconfig merge, exec plugins, login-shell environment, keychain.
- Tier-0 and on-demand watches; discovery; generic CRDs in Table mode.
- Curated views for P1 kinds in the [coverage matrix](data-model.md#coverage-matrix); detail drawer; events; related objects.
- YAML view/edit (Update with resourceVersion, `kubectl edit` semantics) with server-side dry run, ownership warnings from `managedFields`, and conflict diff; create from YAML (server-side apply); delete; scale; restart.
- Logs (single container, follow, search, download).
- Starred resource types, bookmarks, global search, keyboard navigation, light/dark.
- RBAC-aware UI (`SelfSubjectRulesReview`/`SelfSubjectAccessReview`).
- `v8s serve`. Signed builds for macOS, Windows, Linux; auto-update.

**Exit:** all P0 budgets with 3 clusters selected; cold start ≤ 1.5 s; Electron idle ≤ 350 MB.

### P2 · Operate, Flux, Helm

- Pod shell (WebSocket exec, debug containers for distroless), node shell, cordon/uncordon, drain with pre-flight and resumable jobs, port-forward (desktop), local audit log.
- Flux module: all kinds, overview, inventory tree, dependency graph, suspend/resume/reconcile/force/reset, bulk.
- Helm module: metadata-only release list, detail with history/values/manifest/diff, rollback, uninstall, retry; Flux-aware routing.
- Multi-pod logs.

**Exit:** on a 300-release, 30-Kustomization cluster, the Helm list (name, namespace, revision, status, updated, from Secret labels) and the Flux overview render ≤ 300 ms after connect; chart and app-version columns (from decoded payloads, visible rows only) fill within 1 s; exec keystroke latency ≤ 5 ms added.

### P3 · Problems, Kyverno, Trivy

- Problems engine (dirty-set evaluation, deadline heap, rollups), Problems view with class filters, acknowledge/snooze, cross-cluster aggregation, presets per group.
- Kyverno and Trivy modules with Table-mode ingest and lazy bodies; Security tab per workload.
- Gateway API and cert-manager views; networking problem classes.

**Exit:** 5k VulnerabilityReports and 20k PolicyReports add ≤ 30 MB agent RSS with every default-on class enabled (all of which are computed from Table-mode counts; no report bodies fetched); a crash loop appears in Problems ≤ 2 s after the container state changes.

### P4 · Hosted, OAuth identity, PWA

- `v8s server`: OIDC login, cluster registry, groups, per-user state, Postgres/SQLite, Helm chart.
- Identity strategies: OIDC pass-through (with token exchange for audiences) and scoped impersonation (constrained impersonation where available); shared read-only kiosk.
- Shared caches with per-user authorization filter; per-user paths for sensitive reads and all writes; hosted audit.
- PWA: manifest, service worker app shell, offline snapshot, mobile layouts (cards, bottom nav), reduced-fan-out subscriptions, hold-to-confirm.
- Hosted HTTP preview on a separate registrable domain (opt-in).

**Exit:** mobile budgets on a throttled mid-range Android profile; 50 simulated users × 10 clusters on one replica within ≤ 1 MB per session over the shared cache; a security review of the identity strategies and the authz filter.

### P5 · Network, depth, scale

- Hubble module (flows tab, drops view, service map, network problem classes).
- Kubescape module (summary ingest, ApplicationProfile/NetworkNeighborhood viewers).
- Cross-cluster CVE index; violations-by-policy job.
- Cloud token exchange (EKS, AKS OBO, GKE workforce), cluster connector for private clusters, hosted replica sharding.
- Web push for critical problems; desktop notifications.
- CEL user-defined detectors; module API documentation for third-party integrations (Argo CD, KEDA, Gatekeeper).
- Prometheus links/sparklines (optional).

## High-value extras (and where they land)

| Extra | Phase | Note |
| --- | --- | --- |
| RBAC-aware UI | P1 | Disabled actions say why |
| Global search across selected clusters (name, label, image, IP) | P1 | Agent-side over indexes; `⌘K` command palette also runs actions |
| Events stream with Warning filter | P1 | |
| YAML diff editor with dry run | P1 | |
| Bookmarks and starred types | P1 | Requirement 13 |
| Offline kubeconfig import | P1 | Copied into a v8s-managed file |
| Port-forward (saved, restorable) | P2 | Desktop |
| Audit of destructive actions | P2 (local), P4 (hosted) | |
| "Explain this resource" (owners, selectors, what routes to it) | P2 | Related tab |
| Resource diff between clusters ("staging vs prod Deployment") | P3 | Cheap given multi-cluster and the YAML diff |
| Rollout history and one-click Deployment rollback | P2 | |
| "Who can do X" (RBAC query by subject or verb) | P4 | `SubjectAccessReview` sweeps |
| Shareable deep links | P1 | URL reflects cluster/kind/namespace/name/filter |
| CSV/JSON export of any filtered table | P2 | Streamed from the agent |
| Deliberately **not** in MVP | n/a | AI assistant, cost analysis, cluster provisioning, plugin marketplace, Windows node shell |

## Open questions

Decisions for Sean. Each has a recommendation; none blocks P0.

| # | Question | Recommendation |
| --- | --- | --- |
| Q1 | Project domain and reverse-DNS prefix for labels/annotations (`v8s.dev/...` is used as a placeholder) | Register `v8s.dev` if available |
| Q2 | License | **Apache-2.0** (Kubernetes ecosystem norm, patent grant). PixelCI's source-available license would deter contributors and packagers |
| Q3 | Where the canonical repo lives: GitHub `zyno-io/v8s` (like vue-foundation) or `git.s24.dev` with a GitHub mirror | GitHub `zyno-io/v8s`; OSS discovery and contributions happen there |
| Q4 | Is a Signal24-run hosted SaaS planned, or self-hosted only? | Design for both (already done); build self-hosted first. SaaS raises the priority of the connector (P5) and a separate billing control plane |
| Q5 | Is a cluster-wide read-only hub ServiceAccount acceptable as the hosted default? | Yes, default shared cache with per-cluster opt-out to per-user watches |
| Q6 | Electron's ~150–250 MB baseline: acceptable for the desktop app? | Yes; frame-time consistency on Linux matters more. Keep the WebKit perf project in CI to revisit Tauri |
| Q7 | Extract the CI Dashboard / PixelCI tokens into a shared package (e.g. in `@zyno-io/vue-foundation`) now that three products use them? | Yes, after v8s P0 proves the dense-table extensions |
| Q8 | Opt-in telemetry (frame-time histograms, crash reports) in desktop and self-hosted builds? | Yes, opt-in, documented, no cluster data |
| Q9 | Hosted node-shell session recording (keystroke/typescript capture) | Offer as an org opt-in in P4; off by default |
| Q10 | Kubescape runtime alerts (only available via exporters) worth ingesting via an HTTP exporter endpoint on `v8s server`? | Later (post-P5), only if users ask |
| Q11 | Platform priority for the first desktop alpha | macOS + Linux first, Windows in the same release if signing is ready |
| Q12 | Name the pod/flow/log defaults (ring sizes, linger times) as user settings or keep them internal? | Internal defaults with an "Advanced" settings page |

Engineering questions to settle by benchmark or a quick check (not for Sean): whether pods should use Table-mode ingest above ~30k pods per cluster; whether Helm 4's SDK rolls back releases created by Helm 3 cleanly (else vendor the v3 SDK for rollback); Kubescape storage watch support and kind list; Trivy report digest location.

## Repository layout

A Yarn 4 workspace (Signal24 convention) plus a Go module:

```
v8s/
├── README.md
├── LICENSE                         # Q2
├── package.json                    # yarn workspaces: packages/*
├── go.work                         # agent + spikes
├── agent/                          # Go module: github.com/zyno-io/v8s/agent
│   ├── cmd/v8s/                    # one binary: agent | serve | server | connector | auth
│   ├── internal/
│   │   ├── kube/                   # clients, kubeconfig, exec plugins, OIDC, passthrough, rate limits
│   │   ├── ingest/                 # discovery, projecting ListerWatchers, tiers, linger, GC
│   │   ├── store/                  # handle stores, indexes, diffing Replace, interning
│   │   ├── view/                   # queries, materialized views, windows, group-by
│   │   ├── problems/               # engine, deadline heap, core detectors
│   │   ├── modules/                # core/, flux/, helm/, kyverno/, trivy/, kubescape/, hubble/
│   │   ├── ops/                    # exec, logs, portforward, drain, nodeshell, jobs
│   │   ├── wire/                   # protocol, sessions, diffs, acks, stream sockets
│   │   ├── hosted/                 # login, identity strategies, authz filter, db, audit
│   │   └── desktop/                # handshake, settings, keychain, local audit
│   ├── pkg/protocol/               # wire types (source of truth; tygo → TS)
│   └── bench/                      # recorded watch-stream fixtures + benchmarks
├── packages/
│   ├── ui/                         # @v8s/ui: Vue 3 app (Vite), routes, views, module UIs
│   ├── client/                     # @v8s/client: generated protocol types + Data Worker + view stores
│   └── desktop/                    # @v8s/desktop: Electron main + preload, electron-builder config
├── charts/v8s/                     # Helm chart for v8s server
├── test/
│   ├── perf/                       # kwok scenarios, Playwright perf suites, budgets.yaml
│   └── e2e/                        # Playwright functional tests (kind cluster)
├── docs/                           # this plan; later user docs (VitePress, as PixelCI)
└── spikes/                         # throwaway experiments with their results
```

The UI build is embedded into the Go binary (`embed.FS`) for `v8s serve` and `v8s server`, and loaded from disk by Electron.
