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

msgpack: maps with the field names in the Go tags; `ids` is a msgpack `bin`. Go uses `github.com/vmihailenco/msgpack/v5`, the client `msgpackr` (with `useRecords: false`, `mapsAsObjects: true`).

## Views

1. `view.subscribe {qid, gen, query, window}` → `{columns}`. The agent then sends a full first patch (with `ids`) as soon as rows exist, and later patches as things change.
2. **Coalescing**: the first change after a quiet period flushes within 16 ms; after that at most one patch per subscription per tick. The tick is 100 ms for loopback clients and 250 ms otherwise (P0 only implements loopback). A paused subscription (P1: hidden tab) gets no ticks.
3. **Backpressure**: at most 3 unacknowledged patch frames or 512 KiB in flight per socket; beyond that the subscription stays dirty and the next tick sends the newest state. Never a backlog.
4. **Generations**: `view.update` with a new `query` must bump `gen`. Patches carry the `gen` they were computed for; the client drops patches older than its current `gen`.
5. **Windows**: the client asks for `limit` rows from `offset`. With `anchorId` set (the first visible row), `offset` is relative to the anchor's current index, so inserts above the viewport never move what the user is reading. The agent clamps the window to the view.
6. **Ids**: per subscription, monotonic, never reused. A row's id stays stable while the object exists in the view (including across re-sorts and query changes on the same subscription).
7. **Rows**: `[id, version, …cells]` in `columns` order; `version` increases when any cell changes. The client keeps rows by id and re-renders a row only when its version changes.

## Pods view (`gvr: "v1/pods"`)

Columns: see `PodColumns`. Sortable columns: `cluster`, `name`, `namespace`, `status`, `restarts`, `node`, `createdAt`. Filters: `statusTone in [...]` (tones as strings `"1"`…`"5"`), `status eq <reason>`, `node eq <name>`. `namespaces` restricts by namespace; `text` is a case-insensitive substring match on name. Default sort: `statusTone` severity (danger, warning, others), then `name`.

Status reason follows `kubectl get pods`: a container's waiting/terminated reason wins (`CrashLoopBackOff`, `ImagePullBackOff`, `OOMKilled`, `Error`, `ContainerCreating`), then `Terminating` if deleting, else the pod phase. Tone: danger for crash/pull/OOM/Error/Failed, warning for Pending/ContainerCreating/Terminating/not-ready Running, success for ready Running, info for Succeeded/Completed.
