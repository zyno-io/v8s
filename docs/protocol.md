# Agent ↔ UI protocol (v1)

The source of truth is [`agent/pkg/protocol/protocol.go`](../agent/pkg/protocol/protocol.go); TypeScript types are generated from it (`yarn protocol:gen` → `packages/client/src/protocol.gen.ts`). This page explains the behaviour the types cannot. Background: [performance.md §9](performance.md#9-wire-protocol-and-sync).

## Connection

- One **data socket** per browser tab: `GET /ws`, WebSocket, binary frames only.
- **Desktop / `v8s serve`**: the client offers `Sec-WebSocket-Protocol: v8s.v1, token.<hex>`. The agent rejects a missing or wrong token, and an `Origin` that is not the agent's own origin (or the Electron app origin), with 403 before upgrading. It echoes `v8s.v1`.
- **Fake mode** (`v8s agent --fake …`, used by tests and UI development) still requires the token, but it is printed to stdout like in desktop mode.
- On upgrade the agent sends `Hello`. The client checks `version == 1` and closes on mismatch.

## Frames

`[u8 kind][u32 channel BE][u32 seq BE][msgpack payload]`. `seq` increments by one for every frame the agent sends. The client sends `seq = 0` except on acks.

| Kind | Direction | channel | Payload |
| --- | --- | --- | --- |
| 1 Hello | agent → client | 0 | `Hello` |
| 2 Request | client → agent | request id | `Request` |
| 3 Response | agent → client | request id | `Response` |
| 4 Patch | agent → client | qid | `ViewPatch` |
| 5 Ack | client → agent | 0 | none (`seq` = acked patch frame) |
| 6 Dict | agent → client | 0 | `DictAdd` |
| 7 ClusterState | agent → client | 0 | `ClusterStates` |
| 8 Object | agent → client | oid | `ObjectFrame` (not acknowledged) |

msgpack: maps with the field names in the Go tags; `ids` is a msgpack `bin`. Go uses `github.com/vmihailenco/msgpack/v5`, the client `msgpackr` (with `useRecords: false`, `mapsAsObjects: true`).

## Views

1. `view.subscribe {qid, gen, query, window}` → `{columns}`. The agent then sends a full first patch (with `ids`) as soon as rows exist, and later patches as things change.
2. **Coalescing**: the first change after a quiet period flushes within 16 ms; after that at most one patch per subscription per tick. The tick is 100 ms for loopback clients and 250 ms otherwise (P0 only implements loopback). A paused subscription (P1: hidden tab) gets no ticks.
3. **Backpressure**: at most 3 unacknowledged patch frames or 512 KiB in flight per socket; beyond that the subscription stays dirty and the next tick sends the newest state. Never a backlog.
4. **Generations**: `view.update` with a new `query` must bump `gen`. Patches carry the `gen` they were computed for; the client drops patches older than its current `gen`.
5. **Windows**: the client asks for `limit` rows from `offset`. With `anchorId` set (the first visible row), `offset` is relative to the anchor's current index, so inserts above the viewport never move what the user is reading. The agent clamps the window to the view. If the anchor row is gone, or it changed and re-sorted outside the window the client holds, the window keeps its previous position instead of chasing it.
6. **Ids**: per subscription, monotonic, never reused. A row's id stays stable while the object exists in the view (including across re-sorts and query changes on the same subscription).
7. **Rows**: `[id, version, …cells]` in `columns` order; `version` increases when any cell changes. The client keeps rows by id and re-renders a row only when its version changes.

## Requests

Every request is a `Request{method, params}` on a fresh channel id answered by one `Response{result | error}`. Errors are `{code, message}`; codes: `bad_request`, `not_found`, `forbidden`, `unavailable`, `internal`, `invalid` (the API server rejected the object), `conflict`, `write_unavailable`. Slow requests (object operations, a generic kind's first view) are answered asynchronously, so responses may arrive out of order with patches; match them by channel id.

| Method | Params → result |
| --- | --- |
| `clusters.list` | → `{clusters, selected}`. `selected` is the live selection, else the remembered one from settings. Each cluster has `groups` and `writeContext?` from settings; contexts that are linked write contexts are not listed. |
| `clusters.select {ids}` | → `ClusterStates`. Starts the tier-0 watches of new clusters; deselected clusters stop after a 10 s grace. The selection is stored in settings. |
| `kinds.list` | → `{kinds}`: the union over the **selected** clusters' discovery. `Kind{gvr, group, version, kind, plural, shortNames, namespaced, available, curated, clusters}`; `available` is true for every listable kind (list + watch), `clusters` are the selected clusters that serve it. Until a cluster's discovery has loaded, its curated kinds are listed. |
| `view.subscribe` / `view.update` / `view.unsubscribe` | as in [Views](#views). `view.subscribe` returns the kind's `columns`; for a generic kind it waits (up to 20 s) for the first `columnDefinitions`, and answers `not_found` when no selected cluster serves the kind. `view.update` with a changed `gvr` or `clusters` re-scopes the watches and, if the `gvr` changed, returns the new `{columns}`. |
| `object.get {cluster, gvr, namespace, name, keepManagedFields?}` | → `{yaml, uid, resourceVersion, managedBy: [{manager, operation, fields}], owners: [{kind, name}], gitops?: {kind, name, namespace}}`. `gitops` comes from Flux (`kustomize.toolkit.fluxcd.io/name`+`/namespace` → `Kustomization`, `helm.toolkit.fluxcd.io/...` → `HelmRelease`) and Helm (`app.kubernetes.io/managed-by: Helm` + `meta.helm.sh/release-name` → `Helm`) metadata. `managedFields` is stripped from `yaml` unless requested; `managedBy` always lists the field paths per manager. |
| `object.subscribe {oid, ref, keepManagedFields?}` / `object.unsubscribe {oid}` | Empty result. The agent then sends `KindObject` frames on channel `oid`: the same payload as `object.get` (plus `deleted?`, `error?`) for the initial state and after every change, from a single-object watch (`fieldSelector=metadata.name=`) on the read context. The agent's caches hold projections, not objects, so a curated kind's cache is not used. |
| `object.dryRun {ref, yaml, mode}` | → `{ok, warnings, error?, conflicts: [{path, manager}]}`. Server-side `dryRun=All` (update: PUT against the live resourceVersion; create: server-side apply). For updates, `conflicts` lists the changed paths that another field manager (not `v8s`) owns according to the live `managedFields`. A server rejection is `ok: false` with `error`, not a failed request. |
| `object.apply {requestId, ref, yaml, mode, resourceVersion}` | → `{resourceVersion, usedContext, applied?}`. `mode: "update"` is a PUT with the opened `resourceVersion` and field manager `v8s` (kubectl edit semantics). `mode: "create"` is server-side apply (field manager `v8s`, `force=false`) of every document of a multi-document YAML. A 409 is `{error: {code: "conflict", live}}` where `live` is the current object payload. |
| `object.delete {requestId, ref, propagation?}` | → `{usedContext}`. `propagation`: `Background` (default), `Foreground`, `Orphan`. |
| `object.action {requestId, ref, action, params}` | → `{usedContext, resourceVersion?, name?}`. Actions: `scale {replicas}` (deployments, statefulsets, replicasets), `restart` (patches `kubectl.kubernetes.io/restartedAt`; deployments, statefulsets, daemonsets), `cordon` / `uncordon` (nodes), `suspend` / `resume` (cronjobs), `trigger` (creates a Job from a CronJob; `name` is the Job), `rollback {revision}` (deployments; restores that ReplicaSet's pod template). All are minimal merge/JSON patches. |
| `access.check {checks: [{cluster, verb, gvr, namespace?, name?}]}` | → `{results: [{allowed, viaWrite, reason}]}` in input order. `SelfSubjectAccessReview` on the read context, then on the linked write context when denied; cached 60 s. |
| `settings.get` | → the settings document. |
| `settings.set {patch}` | → the merged document. `patch` is an RFC 7386 JSON merge patch (`null` deletes a key, arrays replace). |
| `search {text, limit}` | → `{results: [{cluster, gvr, kind, namespace, name}]}` over every kind currently watched on the selected clusters: case-insensitive name contains, prefix matches first, at most `limit` (default 50, max 200). |

`ref` is `{cluster, gvr, namespace?, name?}`; `gvr` is `v1/pods` or `apps/v1/deployments`. In `create` mode `ref.name` may be empty.

### Mutations: contexts and idempotency

- **Every mutating request carries a client `requestId`** (`object.apply`, `object.delete`, `object.action`). The agent remembers the outcome (result or error) for 10 minutes; a repeat, even while the first is still running, returns that outcome without acting twice. Dry runs are not deduplicated.
- **Reads and watches always use the read context.** For a mutation the agent asks `SelfSubjectAccessReview` on the read context (cached 60 s). If allowed, it runs there. If denied and the cluster has a linked write context (`settings.links`) and `autoSwitchWrite` is on (default), it runs through the write context. Otherwise: `forbidden`. If the write context answers 401/403, the error is `write_unavailable` with the server's message. If the review itself fails, the read context is tried and the server decides. Results carry `usedContext`.
- Linked contexts are listed in `clusters.list` as `writeContext` of the read cluster, and are not listed as clusters of their own.

### Settings document

`{groups: {name: [clusterId]}, links: {clusterId: writeContextId}, autoSwitchWrite: bool (default true), stars: [gvr], bookmarks: [{name, url}], selection: [clusterId], columnLayouts: {gvr: {...}}, problemPrefs: {}, acks: {}}`. Desktop persists it atomically (temp file + rename, mode 0600) to `os.UserConfigDir()/v8s/settings.json`; fake mode keeps it in memory unless `--settings` is given.

## Resource kinds

Every kind goes through the same engine; a **module** per kind supplies GVR, columns, projection, sort/filter accessors and tone. View queries use the module's column keys for `sort` and `filters` (`eq`/`in` on any string, dictionary or integer column, `contains` on any string column; tone columns filter by tone number). A `Column` has `key`, `type`, `label`, `width` (px hint) and `align`. The default sort is `statusTone` severity then `name`, except events (`lastSeen` descending).

**Curated (mode A: protobuf, typed, projected before the cache).** Columns after `cluster`, `name` and `namespace` (cluster-scoped kinds have no namespace column); all end with `statusTone` where meaningful and `createdAt` (age):

| GVR | Columns |
| --- | --- |
| `v1/pods` | ready, containers, status, restarts, node |
| `apps/v1/deployments` | ready, upToDate, available, images |
| `apps/v1/statefulsets` | ready, images |
| `apps/v1/daemonsets` | desired, current, ready, upToDate, available, images |
| `apps/v1/replicasets` | desired, current, ready, owner |
| `batch/v1/jobs` | completions, duration, status, owner |
| `batch/v1/cronjobs` | schedule, suspend, active, lastSchedule |
| `v1/services` | type, clusterIP, externalIP, ports, selector |
| `networking.k8s.io/v1/ingresses` | class, hosts, address, ports |
| `v1/nodes` | status, roles, version, internalIP, os, kernel, runtime, cpu, memory, pods, taints |
| `v1/namespaces` | status |
| `v1/persistentvolumeclaims` | status, volume, capacity, accessModes, storageClass |
| `v1/persistentvolumes` | status, claim, capacity, accessModes, reclaimPolicy, storageClass |
| `v1/events` | type, reason, object, message, count, lastSeen. Warning events only (`fieldSelector=type=Warning`). |
| `autoscaling/v2/horizontalpodautoscalers` | reference, targets, minPods, maxPods, replicas |
| `policy/v1/poddisruptionbudgets` | minAvailable, maxUnavailable, allowed, healthy, expected |
| `networking.k8s.io/v1/networkpolicies` | podSelector, policyTypes, ingress, egress |
| `v1/serviceaccounts` | secrets |
| `discovery.k8s.io/v1/endpointslices` | service, addressType, ready, total |

**Generic (mode C: server-side Table).** Every other listable kind, including CRDs, ConfigMaps, Secrets and RBAC kinds, is watched with `Accept: application/json;as=Table;v=v1;g=meta.k8s.io` and `includeObject=Metadata`, so no object body (and no Secret value) reaches the agent. Columns: `cluster`, `name`, `namespace` (if namespaced), the printer columns with priority 0 (typed from `columnDefinitions`: `integer`/`number` → int, `date` → time, `boolean` → dict, everything else → str), `statusTone` when a Ready/Status/Synced/Healthy/Available/State/Phase column exists (`True`, `Running`, `2/2` → success; `False`, `Failed` → danger; `Unknown`, `Pending`, `1/2` → warning), and `createdAt`. Column keys are the lower-cased name without punctuation (`Created At` → `createdat`; reserved keys get a `_` suffix). `date` printer columns arrive from the API server as human durations (`5d3h`); the agent converts them to a timestamp rounded to the minute. A kind's schema is fixed by the first cluster that reports it; other clusters' tables are mapped by column name.

## Watch lifecycle

- **Tier 0** starts when a cluster is selected and runs until it is deselected: pods, nodes, namespaces, deployments, statefulsets, daemonsets, replicasets, jobs, cronjobs, services, endpointslices, ingresses, persistentvolumeclaims, Warning events, plus metadata-only watches of CustomResourceDefinitions and APIServices that invalidate discovery.
- **Any other kind** is watched on demand: view subscriptions hold references, one watch per (cluster, kind) however many views or clusters-in-scope use it. When the last reference goes, the watch **lingers 5 minutes**; each cluster has a budget of **96 watches**, and a new watch beyond it evicts the least recently used lingering one (tier-0 and referenced watches are never evicted; if nothing can be evicted the kind simply shows no rows from that cluster).
- **Discovery** (aggregated discovery with legacy fallback) is cached per cluster, refreshed every 5 minutes and when a CRD or APIService changes.
- Rows appear **progressively** while a kind syncs: projected list pages and initial watch events are shown provisionally and reconciled by the reflector's final `Replace`. Fallback LISTs are paged (500).

## Fake mode

`--fake clusters=N,pods=P,churn=R[,rbac=1]` serves, per cluster: P/N pods owned by one ReplicaSet per app (400 apps, each with a Deployment, a current and a previous ReplicaSet for rollback, a Service and an EndpointSlice), StatefulSets, DaemonSets, Ingresses, 150 nodes, 37 namespaces, Warning events for failing pods, Jobs and CronJobs, ConfigMaps and Secrets (Table mode), `storageclasses`, two CRDs (`kustomizations.kustomize.toolkit.fluxcd.io`, `helmreleases.helm.toolkit.fluxcd.io`) with Ready/Status columns, and the matching Flux objects. Object get, subscribe, dry run, apply, delete and actions work against an in-memory store that feeds the views (a successful apply bumps `resourceVersion`). With `rbac=1`, the second cluster is read-only for mutations and a context `<name>-admin` can be linked to it (`settings.links`) to exercise write-context switching.

## Pods view (`gvr: "v1/pods"`)

Columns: see `PodColumns`. Sortable columns: every column of the schema. Filters: `statusTone in [...]` (tones as strings `"1"`…`"5"`), `status eq <reason>`, `node eq <name>`. `namespaces` restricts by namespace; `text` is a case-insensitive substring match on name. Default sort: `statusTone` severity (danger, warning, others), then `name`.

Status reason follows `kubectl get pods` (including native sidecars: restartable init containers count in `ready`/`containers`, and `SchedulingGated`): a container's waiting/terminated reason wins (`CrashLoopBackOff`, `ImagePullBackOff`, `OOMKilled`, `Error`, `ContainerCreating`), then `Terminating` if deleting, else the pod phase. Tone: danger for crash/pull/OOM/Error/Failed, warning for Pending/ContainerCreating/Terminating/not-ready Running, success for ready Running, info for Succeeded/Completed.
