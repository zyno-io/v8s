# Spike: decode, retention, and transfer costs

Throwaway benchmark backing the performance decisions in [docs/performance.md](../../docs/performance.md). Not product code.

It answers four questions:

1. What does it cost to decode a large pod list, in V8 and in Go, as JSON and as protobuf?
2. How much memory does a cache of full objects hold, versus stripped objects, metadata only, and projected rows?
3. What does it cost to move rows across a thread boundary (agent → UI worker → main thread)?
4. Is client-side sort and filter over projected rows cheap enough to skip a server round trip?

## Run

```sh
node gen.mjs 10000 out/pods-10k.json
node gen.mjs 25000 out/pods-25k.json
node --expose-gc node-bench.mjs out/pods-10k.json
(cd go && go run . ../out/pods-10k.json)
```

`gen.mjs` writes a synthetic `PodList` shaped like a Deployment-managed pod as the API server returns it: one to three containers, 12 env vars each, probes, tolerations, a projected token volume, two `managedFields` entries, and the `kubectl.kubernetes.io/last-applied-configuration` annotation. The median pod is 11.7 KB of JSON (p90 16 KB), of which 5.5 KB is `managedFields` plus last-applied. That is on the heavy side of real clusters, so treat the numbers as an upper bound.

A 50k-pod list could not be generated as one JSON string: at ~580 MB it exceeds V8's maximum string length. That is a finding in itself. An unpaginated list of a big kind cannot be parsed in a JS process at all.

## Results

Apple M1 Max, Node 24.11, Go 1.27, `k8s.io/api` v0.34. Medians. Timings are from a quiet machine; a later re-run under heavy load (load average > 400) was 2–5× slower in absolute terms with the same JSON-vs-protobuf ordering, and identical heap figures (heap is deterministic).

| Measurement | 10k pods | 25k pods |
| --- | --- | --- |
| JSON size (full) | 111 MB | 278 MB |
| JSON size (stripped of managedFields + last-applied) | 54 MB | 136 MB |
| Protobuf size (full) | 80 MB | 201 MB |
| Protobuf size (stripped) | 29 MB | 73 MB |
| **V8** `JSON.parse` full list | 473 ms | 1,156 ms |
| **Go** `encoding/json` → `corev1.PodList` | 614 ms | 1,540 ms |
| **Go** protobuf → `corev1.PodList` | 101 ms | 274 ms |
| V8 retained heap, full objects | 181 MB | 453 MB |
| V8 retained heap, stripped objects | 77 MB | 192 MB |
| Go retained heap, full typed objects | 180 MB | 450 MB |
| Go retained heap, stripped typed objects | 124 MB | 310 MB |
| Go retained heap, `PartialObjectMetadata` | 13 MB | 32 MB |
| **Go retained heap, projected pod rows** (identity, labels, owner, node, phase, conditions, container states; strings interned with `unique`) | **14 MB** | **36 MB** |
| V8 retained heap, 9-column row tuples | 2.3 MB | 5.7 MB |
| Project stripped objects → row tuples | 3.6 ms | 10.7 ms |
| Encode rows → columnar (string table + typed arrays) | 2.6 ms | 10.6 ms |
| `postMessage` structured clone, stripped full objects | 487 ms | 1,228 ms |
| `postMessage` structured clone, row tuples | 3.7 ms | 9.0 ms |
| `postMessage` structured clone, stripped JSON string | 13 ms | 26 ms |
| `postMessage` **transfer**, columnar typed arrays | 0.8 ms | 1.8 ms |
| Sort rows by name (`<` compare) | 1.8 ms | 5.3 ms |
| Sort rows by name (`localeCompare`) | 2.2 ms | 6.4 ms |
| Sort rows by name (`Intl.Collator`) | 5.6 ms | 15.9 ms |
| Substring filter on name | 0.4 ms | 0.6 ms |
| Apply 100 deltas through a uid → index map | < 0.1 ms | < 0.1 ms |

## What it means

- **Never decode a list on the UI main thread.** 473 ms for 10k pods is 28 dropped frames. Even in a worker, a full parse is half a second of CPU.
- **Protobuf is 6× cheaper to decode than JSON in Go** for core types. Ask for `application/vnd.kubernetes.protobuf` wherever the API server offers it (built-in types; not CRDs).
- **Strip `managedFields` and last-applied at ingest.** They are half the bytes. Stripping cuts V8 heap 2.4× and Go heap 1.5×.
- **Typed Go objects are not small.** Stripped typed pods hold 124 MB per 10k, more than V8 holds for the same stripped JSON (77 MB), because `PodSpec` and `Container` carry many zero-valued fields and Go strings each carry a header. A long-lived cache of fully decoded typed objects for every watched kind is the wrong default. Hold what views and detectors need. A projection carrying everything the pod list and the pod detectors use costs **14 MB per 10k pods**, 9× less than stripped typed objects and 13× less than full ones. Full objects for detail views are fetched on demand.
- **Metadata-only watches are ~10× smaller** than stripped typed objects (14× smaller than full objects). Kinds that only need names, labels, owners, and timestamps should be watched as `PartialObjectMetadata`.
- **Never send full objects across a thread or wire boundary for a list.** Cloning 10k objects costs 487 ms; 10k row tuples cost 3.7 ms; transferred columnar buffers cost 0.8 ms. Lists carry projected rows; the full object is fetched when a detail view opens.
- **Sort and filter on projected rows are cheap.** 25k rows sort in ~5 ms and filter in under 1 ms in V8 (Go is faster). Cheap enough to run on every query change wherever it runs. The plan runs it in the agent and sends the UI only a window, so UI memory scales with the viewport and phones use the same path. `Intl.Collator` is 3× slower than a plain compare; use a byte-order compare for names (Kubernetes names are ASCII DNS labels).
- **Columnar transfer is fastest but unnecessary at window scale.** With windows of ≤ 2k rows, plain row arrays clone in under 1 ms.
- **Indexed delta apply is free.** The cost of an update is in re-rendering, not in finding the row.

## Caveats

- Synthetic data. Real pod size varies a lot by cluster (env vars, sidecars, injected annotations).
- Node `worker_threads` `postMessage` uses the same V8 serializer as browser `postMessage`, but browsers add IPC hops (and Tauri/WebKit has a different engine). The ratios hold; the absolute numbers will differ.
- Rust (`kube-rs` with `k8s-openapi` types) was not measured. Its typed structs are `Option`-heavy and would land closer to Go typed than to metadata-only; the conclusion (do not cache full typed objects) does not depend on the language.
