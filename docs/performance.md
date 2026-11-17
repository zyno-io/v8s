# Performance architecture

Performance is v8s's first design constraint. This chapter is the contract: the mechanisms, the budgets, how they are measured, and the mistakes the design rules out. Other chapters defer to it.

## 1. Principles

1. **UI cost scales with the viewport, not the cluster.** The browser never holds a dataset; it holds a window of projected rows. Opening a 50k-pod view costs the UI the same as opening a 50-pod view.
2. **Decode once, near the source, in the cheapest format.** The Go agent decodes protobuf where the API server offers it, projects to the fields views and detectors need, and discards the rest at ingest.
3. **Watch, don't poll; stream, don't relist.** Every list is backed by a long-lived watch; opening a view never triggers a relist.
4. **Send state, not logs.** The agent sends the difference between what a client has and what it should have, coalesced on a tick. Slow clients get fewer, fresher frames, never a backlog.
5. **Nothing big crosses a boundary.** Full objects cross the wire only one at a time for detail views. Lists cross as rows; counts cross as counts.
6. **Nothing big is reactive.** Vue reactivity covers UI state and per-view version counters, never payloads.
7. **Every stream is bounded.** Logs, flows, events, and terminal output have ring buffers and rate limits on both sides.
8. **Budgets are tested.** Every number in §11 has a benchmark in CI (§13). A regression fails the build.

## 2. Measured costs

The [decode spike](../spikes/decode-bench/README.md) measured synthetic, heavy pods (median 11.7 KB JSON with `managedFields` and last-applied) on an M1 Max:

| Cost (10k pods) | Value | Consequence |
| --- | --- | --- |
| `JSON.parse` in V8 | 473 ms | Never on a UI thread; never in the agent's hot path either |
| Go `encoding/json` → typed | 614 ms | JSON decode of core types is the expensive path |
| Go protobuf → typed | 101 ms | **6× cheaper**: request protobuf for built-in types |
| Retained: full objects (V8 / Go) | 181 / 180 MB | Never cache full objects for lists |
| Retained: stripped of managedFields + last-applied (V8 / Go typed) | 77 / 124 MB | Strip at ingest; still too big to cache everything typed |
| Retained: `PartialObjectMetadata` (Go) | 13 MB | Metadata-only watches for kinds that need no spec/status |
| Retained: projected pod rows with interned strings (Go) | 14 MB | What the agent holds per 10k pods (list columns + detector inputs) |
| Retained: 9-column row tuples (V8) | 2.3 MB | What a list window actually needs in the UI |
| `postMessage` clone: full objects vs rows | 487 ms vs 3.7 ms | Lists cross boundaries as rows, never objects |
| Sort 25k rows by name / substring filter | 5.3 ms / 0.6 ms | Sorting and filtering are cheap wherever they run |
| 50k-pod list as one JSON string | does not fit (V8 max string length) | Unpaginated lists of big kinds are impossible in JS, and wasteful anywhere |

Two conclusions shape everything below: **project at ingest** (the agent's live memory per 10k pods is ~14 MB of projections, measured, not 124–180 MB of objects), and **window at the edge** (the UI never needs more than a few thousand rows).

## 3. Data path overview

```
 kube-apiserver ──protobuf/JSON watch──▶ ┌──────────────── Go agent ─────────────────┐
   (per cluster)                         │ ListerWatcher (project per page/event)     │
                                         │   └▶ reflector (RV, 410, bookmarks)        │
                                         │       └▶ store: rows + u32 handles + indexes│
                                         │           ├▶ detectors (dirty set, timers) │
                                         │           └▶ view engine (filter/sort/window,
                                         │                materialized per query)      │
                                         └──────────────┬─────────────────────────────┘
                                   binary WebSocket (msgpack, state patches, acks)
                                                        ▼
                     ┌──── Data Worker (one per tab) ────┐     ┌──── main thread (Vue) ────┐
                     │ owns socket, decodes msgpack,     │ ──▶ │ Map<id,Row> (markRaw)      │
                     │ resolves dictionaries, merges,    │ rAF │ shallowRef version counter │
                     │ ≤1 post per frame per view        │     │ virtual list, fixed rows   │
                     └───────────────────────────────────┘     └────────────────────────────┘
```

Streams (exec, logs, port-forward, Hubble flows) use their own WebSocket each (§8).

## 4. Ingest and watch strategy

### Ingest modes

Every `(cluster, GVR)` has exactly one ingest source. If two consumers need different fidelity, the highest wins and both read from it.

| Mode | Used for | What is stored |
| --- | --- | --- |
| **A. Protobuf → typed projection** | Pod, Node, Namespace, Deployment, ReplicaSet, StatefulSet, DaemonSet, Job, CronJob, Service, EndpointSlice, Ingress, PVC, PV, HPA, PDB, Event, NetworkPolicy | Per-kind projection struct (pod ≈ 1–2 KB), not the typed object |
| **B. Typed partial JSON** | Curated CRDs: Flux kinds, Gateway API, cert-manager | Projection (decode into a struct with only needed fields; `encoding/json` skips the rest cheaply) |
| **C. Server-side Table + metadata** (`Accept: application/json;as=Table;v=v1;g=meta.k8s.io`, query `includeObject=Metadata`) | Generic CRDs; Trivy reports; Kyverno/OpenReports policy reports; Kubescape summaries | Metadata + printer-column cells computed by the API server |
| **D. `PartialObjectMetadata`** | Secrets (Helm release list, Secret lists), ConfigMap lists, ownership graph for arbitrary kinds, counts | Metadata only |

CRDs are never decoded as `unstructured` maps for lists: that is the most expensive representation in Go (every field a boxed `interface{}`). Protobuf is not available for CRDs; CBOR serving (KEP-4222, beta in 1.37) is a future option for mode B/C and will be adopted behind a capability check once GA.

**Mode C is the security-report trick.** Trivy's CRDs declare printer columns for Critical/High/Medium/Low/Unknown; PolicyReports declare Pass/Fail/Warn/Error/Skip. A Table watch gives v8s every count without transferring a single vulnerability or result array. Bodies are fetched only when a user opens one (see [integrations.md](integrations.md)).

Promotion: if a detector later needs a fidelity a mode-C source cannot give, the agent starts a mode A/B informer, swaps the store once synced, then stops the Table watch. No gap, no double memory beyond the swap.

### Projection happens before the cache

The agent uses client-go's **Reflector** (for resourceVersion tracking, 410 Gone handling, bookmarks, and backoff) with **its own ListerWatcher that projects each list page and each watch event before the reflector sees it**. client-go's `SetTransform` would run after full decode and full allocation; projecting inside the ListerWatcher keeps peak memory bounded by one page, not one list.

Every projection drops `managedFields` and `kubectl.kubernetes.io/last-applied-configuration` unconditionally (half the bytes, per the spike).

### How lists are fetched

1. **WatchList / streaming list** (`sendInitialEvents=true`), enabled via client-go's `WatchListClient` feature. Beta and on by default on API servers since 1.34 (GA targeted for 1.38). The API server streams initial state from its watch cache with no large allocation on either side. The agent forwards projected initial events into a provisional index so the first rows appear progressively, flagged `syncing`. Progress is shown as the count of rows the user can see so far (desktop), or a percentage and phase (hosted), never as a cluster-wide total from the shared cache.
2. **Fallback: paginated LIST** with `limit=500`, progressive rows per page.
3. **`resourceVersion=0` unpaginated LIST** only for kinds known to be small in both count and size (Nodes, Namespaces, APIServices; < ~2k objects). It is served from the watch cache and is the cheapest request for the API server.

**CustomResourceDefinitions are watched metadata-only** (mode D) for discovery invalidation and counts. CRDs embed their full OpenAPI schemas, and clusters with Crossplane or many operators carry 1,000+ of them; schemas are fetched per CRD on demand (create forms, YAML completion).

### Watch tiers

- **Tier 0 (problems baseline).** Starts when a cluster is selected, runs until it is deselected. The exact list is in [problems.md](problems.md#the-problems-baseline-always-on-watches). Its size depends on what is installed:

  | Part | Watches |
  | --- | --- |
  | Core (Nodes, Namespaces, Pods, Deployments, ReplicaSets, StatefulSets, DaemonSets, Jobs, CronJobs, Services, EndpointSlices, Ingresses, PVCs, HPAs, Warning Events, APIServices, Validating/MutatingWebhookConfigurations, CRDs metadata-only) | 19 |
  | Helm release Secrets (metadata-only; per user in hosted) | 1 |
  | Flux (GitRepository, OCIRepository, HelmRepository, HelmChart, Bucket, Kustomization, HelmRelease); notification and image-automation kinds are on demand | ≤ 7 |
  | Kyverno (ClusterPolicy, Policy, ValidatingPolicy, NamespacedValidatingPolicy, PolicyReport, ClusterPolicyReport; OpenReports instead of wgpolicyk8s when enabled) | ≤ 6 |
  | Trivy (VulnerabilityReport, ConfigAuditReport, ExposedSecretReport; others when their class is enabled) | ≤ 3 (+4) |
  | Kubescape summaries | ≤ 2 |
  | Gateway API (Gateway, HTTPRoute, GRPCRoute), cert-manager Certificate | ≤ 4 |
  | Hosted only: Roles, ClusterRoles, RoleBindings, ClusterRoleBindings (authz cache invalidation) | 4 |
  | **Total** | **~20 bare cluster; ≤ 50 with everything installed** |
- **On demand.** Any other resource type a view opens. Reference-counted by subscriptions; **lingers** after the last subscriber leaves (desktop 5 min, hosted 2 min) so flicking between views is instant. The lingering set is LRU-evicted to stay inside the watch budget (§5).
- **Never watched**: Hubble flows (streamed on demand, §8), SbomReports, full vulnerability reports, Kubescape manifests, `metrics.k8s.io` (polled, §5).

### Cluster deselect and GC

Deselecting a cluster starts a 10 s grace (accidental toggles are free), then stops all its watches, drops its stores, row handles, string dictionaries, and problem state, and, on desktop, calls `debug.FreeOSMemory()` when more than 100 MB was released so the RSS actually falls. Re-selecting is a cold start (WatchList makes that cheap for the API server).

### Restricted users

If a cluster-wide list returns 403, the agent falls back to per-namespace informers. `SelfSubjectRulesReview` needs a namespace as input, so it cannot discover namespaces; candidates come from the kubeconfig context's namespace, a per-cluster namespace list in settings, and (hosted) the hub cache's namespace list confirmed per namespace by SubjectAccessReview (§10). Per-namespace watches count against the watch budget, so this mode watches only Pods, Deployments, StatefulSets, DaemonSets, Jobs, and Warning Events per namespace (6 × namespaces), plus open views, across at most **5 namespaces** by default (≈ 30 watches inside the 96 budget). Other kinds in those namespaces are listed on demand when a view opens. The UI says which namespaces it is showing.

### Memory practice in the agent

- `GOMEMLIMIT`: desktop 70% of a configurable cap (default 1.5 GB); hosted container limit minus 10%.
- String interning with Go's `unique.Make` for namespaces, node names, images, label keys/values, owner names, reasons. These repeat thousands of times.
- Inside the agent, rows are addressed by dense `u32` handles per `(cluster, GVR)` with free-list reuse; indexes are maps of handle slices. **Handles never leave the agent** (see §9 for wire ids).

## 5. API-server guardrails

v8s must never be the reason an API server is slow.

| Guardrail | Value |
| --- | --- |
| Read QPS per cluster (agent-wide) | 50, burst 100 |
| Writes per user per cluster | 10/s |
| Watches per (cluster, identity) from the agent | ≤ 96: tier 0 (≤ 50) first, on-demand LRU gets the remainder (≥ 32), restricted-user per-namespace watches and per-user hosted watches count against the same budget |
| Lazy body fetches (reports, Helm payloads) | ≤ 4 concurrent per cluster, queued |
| Discovery | aggregated discovery (one request); desktop caches on disk; invalidated by CRD/APIService events |
| Hosted authorization reviews | ≤ 20 SubjectAccessReviews/s per cluster across all users, ≤ 5 concurrent per user; computed lazily for the namespaces a user's open views and problems actually touch, never as a full namespace × GVR sweep |
| OpenAPI v3 | per group-version, hash-addressed URLs, cached indefinitely |
| `metrics.k8s.io` | polled every 30 s, only while a visible view shows usage columns |
| Relists | jittered exponential backoff; never triggered by opening a view |
| API Priority and Fairness | honour `429` + `Retry-After`; the agent sets a distinct User-Agent (`v8s/<version>`) so cluster operators can write a FlowSchema for it |
| Events | Warning-only field selector in tier 0; full event stream only for an open object's Events tab (field selector on `involvedObject.uid`) |

## 6. Agent store, indexes, and view engine

### Store

A custom `cache.Store` implementation per `(cluster, GVR)`:

- Rows are projection structs addressed by `u32` handles; `uid → handle` map.
- **`Replace` diffs instead of clearing and refilling**, so a relist after a 410 produces a handful of updates, not a full-view flash and a full detector re-run.
- Secondary indexes, maintained incrementally: namespace; node (pods); owner (any owned kind); selected label `k=v` pairs (configurable set: `app`, `app.kubernetes.io/*`, plus any label currently used in an open filter); EndpointSlice by Service; Ingress by backend Service; HelmRelease/Kustomization inventory reverse index.
- **Per-namespace** counts (per GVR, per problem class), from which every displayed count is derived. A count shown to a user is always the sum over namespaces that user may see (§10); there is no cluster-wide count that bypasses the authorization filter.

### View engine

A **view** is a materialized, sorted, filtered row-handle array for a normalized query:

```
ViewQuery { clusters[], gvr | "v8s/problems", namespaces[] | "*", labelSelector,
            filters[{col, op, value}], text, sort[{col, dir}], columns[], groupBy? }
```

- Building a view over 50k rows takes ~5–10 ms (filter + sort of handles with a byte-order compare on interned strings).
- **Incremental maintenance**: on an update the engine re-tests the row against the filter and moves it by binary search plus a `memmove` of the handle array (≈ 20 µs at 50k); inserts and deletes likewise. Per-tick cost is proportional to changed rows.
- **Multi-cluster** views merge per-cluster sorted runs; the cluster is just another column.
- **Problems** are a synthetic resource type (`v8s/problems`) in the same engine, so cross-cluster merge, sort, grouping, windowing, and authorization filtering are shared code.
- **groupBy** (owner, namespace, cluster, image) produces group header rows with counts inside the same windowed array, so "50 crash-looping pods" can render as one expandable row.
- Views are reference-counted by subscriptions and dropped 60 s after the last one leaves (rebuilding is ~10 ms).
- Hosted: views are keyed by `(normalized query, authorization scope)` and **shared across sessions** with the same scope (§10).

### Full objects

- `get(ref)` fetches through the passthrough with the user's identity, strips `managedFields` by default (toggle in the YAML view), and is the only way a full object reaches the UI.
- `watchObject(ref)` keeps an open detail live: if the GVR is cached in mode A/B, the agent keeps the full object for that single uid from the event stream; otherwise it opens a single-object watch (`fieldSelector=metadata.name=<name>`).
- YAML serialization, diffs, and syntax work happen in a UI worker; the editor is CodeMirror 6.

### Everything else is passthrough

The agent fully models read paths (ingest, views, problems, related-object queries). Everything else (GET/PUT/PATCH/DELETE of arbitrary objects, subresources, discovery, OpenAPI) is an **authenticated passthrough** that adds identity, rate limits, audit, and dry-run, without modelling the kind.

## 7. UI rendering

### Data Worker

One Web Worker per tab owns the data socket:

- Decodes msgpack (`msgpackr`), resolves dictionary references, merges patches into per-view row maps.
- Posts to the main thread **at most once per animation frame per view**, chunked to ≤ 500 rows so a single structured clone stays under ~2 ms; row-id arrays go as transferable `Uint32Array`s.
- Drops patches whose `gen` is older than the view's current query generation (so typing a filter never shows stale results out of order).
- Owns per-view search highlighting and log-buffer search (§8).

No `SharedArrayBuffer`: it needs COOP/COEP, which breaks popup-based OIDC flows and some embedding, and the windowed design does not need it.

### Main-thread store and Vue

- Each open view keeps a plain `Map<id, Row>` and an `ids` array, both `markRaw`. The only reactive state per view is a `shallowRef` **version counter**, `total`, and per-cluster sync state.
- Pinia holds UI state only: selection (by id), cluster selection and groups, column layouts, toggles, view specs. **Never datasets.**
- A lint rule bans `ref()`/`reactive()`/`computed()` over agent payload types (enforced by a type brand on payload types).

### Virtualized tables

- `@tanstack/vue-virtual` (already used in PixelCI) with **fixed row height**: constant `estimateSize`, **no `measureElement`**. Variable-height measurement is right for PixelCI's galleries and wrong for tables: it forces layout reads during scroll. Mobile cards are fixed-height too (72px).
- One `v-for` over the visible slice; **no per-row or per-cell components**; plain elements with `v-memo="[row.ver, selected, colsVer]"` so unchanged rows are skipped entirely on a version bump.
- Column widths are CSS custom properties on the table container (a resize changes one variable, not N inline styles). The scroller has `contain: strict`; rows have `contain: layout paint` and are absolutely positioned with `transform: translateY`.
- **Relative ages** are computed at render from timestamps; a shared 1 s tick (30 s when every visible age is older than an hour) bumps only the visible rows' memo key.
- **Window paging**: the client keeps a window of rows around the viewport (desktop local ± 1000, hosted ± 300, mobile ± 60) and asks for a new window when the viewport comes within a third of an edge. Dragging the scrollbar far past the window shows fixed-height skeleton rows for one frame (local) or one round trip (hosted).
- **Stable scroll**: the client sends the first visible row as `anchorId`; the agent returns the anchor's new offset, so inserts above the viewport never shift what the user is reading. Selection is by id, never index. PixelCI's `rangeWithPinned` keeps a focused or selected row rendered when it scrolls out.
- **Bulk actions target the query** ("delete all 1,204 matching pods"), not a list of ids the UI would have to hold. The agent enumerates server-side and confirms the count.

### Other heavy surfaces

- **Graphs** (Flux dependencies, Hubble service map): `<canvas>` with a layout computed in a worker (`elkjs` or `d3-force` off-thread). One Vue component for the canvas, none per node.
- **YAML editor**: CodeMirror 6, lazy chunk. Large documents (> 1 MB) open read-only with a warning.
- **Route-level code splitting**: terminal (xterm.js), editor, graphs, and integration views are separate chunks. The shell + Problems + a list view is the initial bundle.

## 8. Logs, shell, and flow streaming

Each stream gets **its own WebSocket** from the UI to the agent, so a chatty log never delays view patches or keystrokes (no head-of-line blocking), and TCP backpressure works per stream.

### Logs

- **Agent**: follows `pods/log` (one upstream stream per container), splits lines, and batches them: ≤ 100 ms or ≤ 64 KB per frame. Multi-pod logs merge by timestamp in the agent.
- **Credit-based flow control**: the client grants credit in bytes; the agent stops reading from the API server stream when credit runs out (TCP backpressure to kubelet). A log that produces 50 MB/s cannot flood the browser.
- **UI**: a Data-Worker-side **ring buffer** of lines (desktop 50k lines, mobile 5k) stored as one growable string arena plus offsets, not one JS string object per line. The main thread renders the visible window from the worker's buffer with the same fixed-row virtualizer as tables (no xterm for logs: it re-parses ANSI and holds its own buffer). ANSI colours are parsed in the worker into span runs.
- **Follow mode** paints at most once per frame; when the user scrolls up, follow pauses and new lines only bump a "N new lines" pill.
- **Search** runs in the worker over the ring buffer; results are offsets.
- **Download** streams from the agent straight to a file (desktop: native save; web: a streamed download URL) without passing through the UI.
- **Budget**: 5k lines/s sustained at ≤ 30% main-thread time on desktop; 1k lines/s on mobile.

### Shell (exec)

- Agent ↔ API server: WebSocket exec (`v5.channel.k8s.io`), SPDY fallback via client-go's fallback executor.
- UI ↔ agent: a dedicated binary WebSocket carrying raw stdin/stdout bytes and resize frames, no msgpack, no batching for stdin (keystroke latency budget ≤ 5 ms added locally, ≤ 15 ms hosted).
- Output is batched per animation frame into xterm.js `write()`; xterm.js WebGL renderer on desktop (falls back to canvas/DOM where WebGL is unavailable, e.g. some mobile browsers).
- Scrollback capped at 10k lines desktop, 2k mobile.

### Hubble flows

- gRPC `GetFlows` with **server-side filters always** (at least a namespace or workload scope; the UI cannot request an unfiltered follow).
- Agent-side rate cap to the UI: ~200 flows/s, batched every 100 ms; above that it samples and reports the true rate.
- Aggregations (service map edges, drop counters) are computed in the agent; the UI receives counters, not flows.
- Ring buffer of 10k flows desktop, 2k mobile.

## 9. Wire protocol and sync

### Framing

- **Data socket**: one per tab. Binary frames `[u8 kind][u32 channel][u32 seq][msgpack payload]`. Maximum frame 128 KB; larger payloads (a big YAML) are chunked so they never queue view patches behind them. `permessage-deflate` on for remote connections, off for loopback (compression costs more CPU than it saves bytes locally).
- **Wire ids are per subscription, monotonic, and never reused.** The agent maps `(cluster, GVR, handle, handle generation)` to a fresh `u32` the first time a row enters a subscription and never hands that id to another object for the subscription's lifetime. Ids from different clusters and GVRs can therefore share one merged view (multi-cluster lists, Problems) without colliding, and a deleted-then-replaced object can never inherit a selection. **Actions resolve selected ids to `(cluster, uid)` in the agent at confirm time**, and the confirm dialog lists the resolved objects, so a selection can never hit a different object than the one the user chose.
- **Rows are positional arrays** against a per-subscription column schema sent once. Repeated strings (cluster, namespace, node, reason, image, owner kind) are **dictionary references**; each dictionary entry is sent once per connection.
- Wire types are defined in Go and generated to TypeScript with `tygo`.

### Subscriptions and patches

```
Subscribe   { qid, gen, query: ViewQuery, window: { anchorId?, offset, limit } }
ViewPatch   { qid, gen, seq, total,
              clusterState[{ cluster, phase: syncing(pct) | synced | stale | forbidden | error }],
              offset,
              ids?:  Uint32Array,                 // only when membership or order changed
              rows:  [[id, rowVer, ...cells]] }   // only rows new to the client or changed
Ack         { seq }
```

- **State-based sync.** The agent remembers, per subscription, the ids and row versions it last sent. Each tick it diffs the current window against that and sends only the difference. Reconnecting means resending the window; no resourceVersion ever leaves the agent, and per-client memory is bounded by window size.
- **Coalescing tick**: the first change after a quiet period flushes within 16 ms; after that, at most one patch per subscription per tick:

  | Client | Tick |
  | --- | --- |
  | Desktop, local agent | 100 ms |
  | Hosted, desktop browser | 250 ms |
  | Mobile | 1 s (2 s on `saveData` or 2g/3g) |
  | Hidden tab | paused |

- **Backpressure**: the client acks each `seq`. The agent allows at most 3 unacked frames or 512 KB in flight per socket. Beyond that, subscriptions stay dirty and the next tick sends the newest state.
- **Generations**: `gen` increments on every query change; patches for older generations are dropped by the worker.
- **Idempotent actions**: every action carries a client UUID; the agent dedupes for 10 minutes, so a retry after reconnect cannot double-delete.

### Why not the alternatives

| Alternative | Rejected because |
| --- | --- |
| Ship all rows to the UI, sort/filter in a worker | Works on loopback, fails the mobile data and memory budgets, and UI heap grows with the cluster. One code path for all clients wins. |
| Op-log deltas (resourceVersion-ordered event replay) | Needs per-client unbounded queues or complex compaction; reconnect needs a replay window. State diffs are bounded by construction. |
| Protobuf to the UI | No benefit at window scale; msgpack is schemaless and has a fast JS decoder. |
| WebTransport | Not available in Safari (iOS). |
| gRPC-web | Needs a proxy layer; streams are one-directional; no win over WebSocket here. |
| Columnar typed arrays to the UI | The spike shows they transfer fastest (0.8 ms per 10k rows), but with windows of ≤ 2k rows, plain arrays cost < 1 ms. Revisit if a feature needs > 5k client-side rows (charts). |

## 10. Hosted mode: fan-out and sharing

Hosted v8s is self-hosted for a team. How it fans out depends on the cluster's identity strategy ([auth.md](auth.md)).

### Per-user watches (default; required for gateway-fronted clusters)

Clusters behind an access gateway such as Warpgate must see **every request carry the user's own token** ([auth.md §Gateway-fronted clusters](auth.md#gateway-fronted-clusters-warpgate)). A shared cache would read cluster data outside the gateway's per-request authorization and session recording, so hosted v8s runs **per-user watches** for these clusters:

- Each `(user, cluster)` has its own tier-0 and on-demand watches, store, views, and problems, all on the user's default access profile, exactly like a desktop agent.
- **Lifecycle follows the user, not the server.** A user's watches for a cluster start when one of their sessions selects it, and stop 2 minutes after their last tab closes or deselects it (problems are therefore evaluated while a user is connected; offline alerting is out of scope).
- **No authorization filter is needed.** The gateway and API server authorize every watch and request directly, so the SubjectAccessReview machinery below does not run for these clusters.
- **Cost model**: per `(user, cluster)`, ~20–50 watches and ~14 MB agent memory per 10k pods (measured projection). A team of 10 people with 6 clusters of 10k pods each selected: ≈ 0.85 GB agent RSS and ≈ 1,200–3,000 long-lived watches through the gateway. That is comfortable for one server process; the gateway's capacity for that many watch streams (and their session recordings) is the number to verify in P0.
- **Budget**: hosted agent RSS ≤ 50 MB per 10k pods per active `(user, cluster)`; watches per `(user, cluster)` within the §5 budget.
- Wire, view, and UI behaviour is identical to the shared mode; only where the store lives changes.

### Optional shared cache (clusters reached directly)

For clusters reached without a gateway, an admin can enable a shared cache, because per-user watches multiply API-server watch load and agent memory by the number of users (50 users × 10 clusters × 10k pods ≈ 7 GB of per-user caches, and 50× the watches):

- **One shared tier-0 cache per cluster**, read through a hub ServiceAccount with `get/list/watch` on tier-0 kinds and **no access to Secrets**.
- **Per-user authorization filter** at `(cluster, GVR, namespace)` granularity:
  1. Resolve the user's identity as the API server sees it with `SelfSubjectReview` under the user's credentials (captures username/group prefixes and claim mappings exactly).
  2. `SubjectAccessReview`: can the user `list <gvr>` cluster-wide? If yes, unrestricted for that GVR (one call covers most operators).
  3. Otherwise `SelfSubjectRulesReview` per namespace to find candidate namespaces, confirmed per `(namespace, GVR)` with `SubjectAccessReview`. If rules review reports `incomplete` (webhook authorizers), SAR per namespace that actually has objects.
  4. Results cached **60 s** (matching the UI's permission cache), **fail closed**. RBAC changes invalidate **only the affected users**: a changed RoleBinding/ClusterRoleBinding invalidates the users and groups among its old and new subjects; a changed Role/ClusterRole invalidates users whose cached decisions came from bindings to it (the agent indexes bindings by roleRef). Unrelated sessions keep their cache, so a busy cluster that creates RoleBindings constantly never blanks everyone's views. During re-check a user's existing rows stay visible (marked revalidating) for at most 5 s before failing closed. Revocations that do not touch RBAC objects (IdP group removal, EKS access entries, cloud IAM) therefore take effect within 60 s. Reviews are rate-limited (§5) and computed lazily per namespace actually viewed; on clusters with webhook authorizers (EKS, GKE, AKS with Azure RBAC), where rules reviews are `incomplete`, that laziness is what keeps the SAR volume bounded. The same rule applies to deciding whether a cluster appears in a user's selector at all: only cheap signals are used (see [auth.md §RBAC awareness](auth.md#rbac-awareness)), never a namespace sweep.
  5. Filtering is a namespace-bitset lookup per row on interned namespace ids, so it is effectively free.
- **Never from the shared cache**: Secrets (including Helm release metadata, which is a per-user metadata watch), full-object GETs, logs, exec, port-forward, interactive Hubble flows, and every write. All of these go to the API server **as the user**, so the API server remains the authority for anything sensitive or mutating.
- **Counts and aggregates are masked, not just rows.** Every count (sidebar, starred types, filter chips, the mobile problems summary, a Node's pod count, "12 workloads" on a vulnerability problem) is the sum of per-namespace counts over the user's visible-namespace bitset. Aggregated problems (per image, per drop pair, cross-cluster collapses) are stored as per-namespace parts and re-aggregated per authorization scope; cross-cluster collapse happens **after** filtering. A problem with several subjects (a drop between two workloads) is shown only if the user can see every subject's namespace.
- **Problems** from shared-cache inputs are computed once per cluster and filtered as above. Detectors whose inputs are user-only run per user: Helm classes use the user's own Helm release watch (or a Flux HelmRelease's status, which is in the shared cache). Hubble network classes use the cluster's **direct relay URL** (an Ingress or internal Service the operator exposes for v8s) rather than a port-forward, so the hub needs no `pods/portforward` at all; `pods/portforward` in `kube-system` would let the hub forward to every pod there, since relay pod names are random and `resourceNames` cannot narrow it. Without a direct relay URL, hosted network classes are unavailable for that cluster. Results are filtered per user like any multi-subject problem.
- **Hubble in hosted mode**: the relay has no authorization of its own. Interactive flow subscriptions carry a server-injected `FlowFilter` restricting results to flows with at least one end in a namespace the user can see, and the server **redacts the other end** when its namespace is not visible: pod name, labels, workload, and IP are replaced by the namespace name, or by `world`/the Cilium reserved identity for non-pod endpoints. Users see that their pod talked to *something in* `payments`, never what. A direct relay URL is used in hosted mode only with this filtering and redaction.
- **Views are shared** across sessions with the same normalized query and authorization scope; each session holds only its window and last-sent state (≤ 1 MB per session).
- **Opt-in only**: the shared cache is enabled per cluster by an admin (it needs a hub ServiceAccount), and never for gateway-fronted clusters.

## 11. Performance budgets

Reference hardware: desktop = 2021 M1 MacBook Pro / equivalent x86 laptop; mobile = Pixel 6a-class Android with 4× CPU throttling and 4G network profile in CI.

| Metric | Desktop (Electron, local agent) | Hosted (desktop browser) | Mobile web |
| --- | --- | --- | --- |
| Cold start to interactive | ≤ 1.5 s from launch | ≤ 1.5 s | ≤ 3.5 s |
| Warm start to interactive | ≤ 0.8 s | ≤ 0.8 s (HTTP cache) | ≤ 1.5 s (HTTP cache) |
| Initial JS (shell + Problems + one list) | n/a | ≤ 250 KB gz | ≤ 150 KB gz |
| Cluster select → first rows (10k pods) | ≤ 1.0 s p95 | ≤ 1.0 s + RTT (≤ 300 ms when hub cache is warm) | as hosted |
| Full sync, 50k pods, LAN | ≤ 8 s | ≤ 8 s | n/a |
| View switch, informer warm | ≤ 100 ms | ≤ 100 ms + RTT | ≤ 300 ms + RTT |
| Sort/filter change over 50k rows | ≤ 20 ms agent, ≤ 50 ms to screen | ≤ 50 ms + RTT | ≤ 150 ms + RTT |
| Scroll | 60 fps, scripting ≤ 4 ms/frame p95, no long tasks | same | ≤ 8 ms/frame p95 |
| API event → row visible, p95 | ≤ 150 ms | ≤ 400 ms | ≤ 1.5 s |
| Main-thread cost per patch | ≤ 2 ms p95 | ≤ 2 ms | ≤ 4 ms |
| 1,000 events/s sustained churn | main thread ≤ 10% busy; agent ≤ 1 core | same | n/a |
| UI heap | ≤ 80 MB; growing the cluster set 10k → 200k objects adds ≤ 10 MB | same | ≤ 50 MB |
| Agent RSS | base ≤ 50 MB; ≤ 50 MB per 10k pods; ≤ 600 MB at 200k objects | ≤ 50 MB per 10k pods per active (user, cluster) (per-user mode); per cluster + ≤ 1 MB per session (shared mode) | n/a |
| Electron total (main + renderer + GPU), idle, 1 cluster | ≤ 350 MB | n/a | n/a |
| Log streaming | 5k lines/s at ≤ 30% main thread; ≤ 60 paints/s | same | 1k lines/s |
| Added exec keystroke latency | ≤ 5 ms | ≤ 15 ms | ≤ 15 ms |
| Idle data on Problems view | n/a | ≤ 30 KB/min | ≤ 30 KB/min; 0 when hidden |

### Mobile specifics

- Phones subscribe narrowly: the problems summary (cluster × class counts, ~1 KB), the problems window (± 60 rows), the current list window (± 60 rows, fewer columns), the open object. Sidebar counts are fetched once, not kept live.
- Hidden page: ticks pause immediately; after 30 s all subscriptions are dropped. Resume is one frame per view, because sync is state-based.
- The agent's cluster-side work is identical for phone and desktop clients. The phone's battery never pays for watching clusters.

## 12. What not to do

The failure modes of existing tools, ruled out by design:

1. **Mirroring the cluster into renderer state** (the Lens pattern: a store of full objects per watched kind in the renderer). UI memory then scales with the cluster and every update walks a reactive graph.
2. **Deep reactivity over payloads**: `ref(rows)`, `reactive(object)`, per-row or per-cell components, measured row heights in tables.
3. **Main-thread parsing**: `JSON.parse` or YAML stringify of large payloads, or structured-cloning thousands of rows to the main thread at once.
4. **Unpaginated `resourceVersion=0` lists of big kinds decoded into full objects and transformed afterwards**, `unstructured` for CRD lists, keeping `managedFields` and last-applied.
5. **Relisting when a view opens**, per-user duplicate informers in hosted mode, or starting informers for every discovered resource type.
6. **Downloading VulnerabilityReport or PolicyReport arrays to compute counts.**
7. **Op-log deltas with unbounded per-client queues**; unbounded log, flow, or terminal buffers; streams without acks or credit.
8. **Index-based selection or scroll anchors**, and `Replace` implemented as clear-and-refill.
9. **Sharing cached data across users without a per-user authorization filter**, including counts and aggregates computed over the unfiltered cache; treating `SelfSubjectRulesReview` as authoritative; Secrets in a shared cache; serving proxied pod content on the app's site.
10. **Auto-connecting every context or running every exec plugin at startup**; polling metrics for all pods; watching all Events cluster-wide without a filter.

## 13. Benchmarking and CI

- **Agent micro-benchmarks** (`go test -bench`, every merge request): recorded watch streams (captured once from kwok, stored as fixtures) replayed through a fake ListerWatcher. Tracked: ns/event, allocs/event, retained bytes per 10k objects (heap after GC), view rebuild time at 50k, incremental update time. Compared to `main` with `benchstat` in the same job; > 10% regression fails.
- **Nightly scale test**: `kwokctl` with 500 fake nodes and 50k pods, Stage rules producing crash loops, pending pods, and churn at configurable rates; synthetic CRDs (5k VulnerabilityReports at ~300 KB, 20k PolicyReports, a few hundred Flux objects with status). Measured: connect → first rows, full sync time, event → row latency (an annotation timestamp on the pod compared with a UI "row visible" test hook), agent RSS, API-server request counts (to catch accidental relists).
- **UI performance tests** (Playwright Chromium with CDP tracing) against the real agent with a replay backend (`v8s agent --fake-cluster=<fixture> --rate=1000ev/s`): wheel-scroll through 50k rows, type a filter, sustained churn, log flood, terminal flood. Collected: rAF frame times, Long Tasks, heap (`Performance.getMetrics`). A mobile project runs with 4× CPU throttling; a WebKit project runs coarse checks (iOS Safari proxy and the future Tauri question).
- **Noise control**: fixed self-hosted runner for absolute budgets, plus same-job comparison against `main` for regressions.
- **Field telemetry** (opt-in, desktop and self-hosted): frame-time and long-task histograms per view, so budgets are checked on real clusters, not only kwok.
