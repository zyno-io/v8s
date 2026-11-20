# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

v8s ("Visual k8s") is a multi-cluster Kubernetes manager: a Go agent plus a Vue UI, shipped as an Electron desktop app,
`v8s serve` (localhost browser UI) and `v8s server` (multi-user hosted mode with OIDC). Toolchain: Go 1.27, Node 26,
Yarn 4 via Corepack.

## Performance is the #1 priority

Staying fast on large clusters (10k–50k+ objects each, several clusters at once, on a laptop and on a phone) is the
product's reason to exist and outranks feature convenience. Weigh every change by its cost at scale, not on a small
cluster.

- Budgets are in `docs/performance.md` §11, e.g. 60 fps scroll over 50k rows (≤ 4 ms scripting/frame), sort/filter over
  50k rows ≤ 20 ms in the agent, ≤ 2 ms main-thread per patch, agent ≤ 50 MB per 10k pods. A change that misses one is a bug.
- Before designing a feature, read `docs/performance.md` §1 (principles) and §12 (what not to do). If a feature can only be
  built by breaking a principle, raise it instead of shipping it.
- Hot paths in the agent (ingest projection, store, view, problems, wire) have Go benchmarks; run the affected package's
  `go test -bench` before and after and report the delta. Watch allocations as well as time.
- For UI-visible changes, check at scale with the fake agent (`pods=50000,churn=200`) and `yarn test:perf`, not just
  against a small cluster.

## Commands

```sh
yarn install
scripts/build-agent.sh --host          # UI build + agent with UI embedded → dist/<os>_<arch>/v8s
scripts/build-agent.sh --host --no-ui  # agent only

# Go (run from agent/)
go vet ./...
go test -race ./...
go test -run TestName ./internal/view/            # single test
go test -run '^$' -bench . -benchtime 1x ./...    # benchmark smoke (CI runs this)
go test -tags e2e -count=1 ./test/e2e/            # needs a kind cluster: test/e2e/cluster.sh up, V8S_E2E_KUBECONFIG=test/e2e/out/kubeconfig

# JS (repo root)
yarn lint                       # oxlint
yarn oxfmt --check .            # format check (yarn format writes)
yarn typecheck                  # all workspaces (vue-tsc / tsgo)
yarn workspace @v8s/ui test     # vitest; single file: yarn workspace @v8s/ui vitest run test/keys.test.ts
yarn workspace @v8s/client test
yarn workspace @v8s/ui test:e2e # Playwright smoke against the fake agent (builds the UI)
yarn test:perf                  # Playwright perf budgets (needs chromium: yarn workspace @v8s/ui playwright install chromium)
yarn protocol:gen               # regenerate packages/client/src/protocol.gen.ts from agent/pkg/protocol (tygo)
yarn docs:dev                   # VitePress docs site
```

UI development without a cluster: run the agent in fake mode and point the UI at it with a URL fragment.

```sh
go run ./cmd/v8s agent --fake clusters=3,pods=50000,churn=200 --listen 127.0.0.1:7931 --dev-token demo   # from agent/
yarn dev:ui   # then open http://localhost:5173/#agent=ws://localhost:7931/ws&token=demo
```

Fake options (`rbac=1`, `lograte=`, `metrics=0`, …) and what the fake world contains are documented in
`docs/protocol.md` § Fake mode. Fake mode serves every integration (Flux, Helm, Kyverno, Trivy, Kubescape, Hubble), so most
UI work and the UI e2e/perf suites need no real cluster.

CI (`.github/workflows/ci.yml`) runs exactly: go vet, go test -race, bench smoke, lint, oxfmt check, typecheck, client and
UI vitest, UI build, docs build. Lefthook pre-commit runs oxlint --fix and oxfmt on staged files.

## Architecture

The big-picture design is in `docs/architecture.md` and `docs/performance.md` (the most important chapter). The repo-layout
tree in architecture.md is aspirational: integrations live directly under `agent/internal/<name>/`, not `modules/`.

**Only the Go agent talks to Kubernetes; the UI renders windows of views the agent computes.** Sorting, filtering,
windowing and problem detection all happen in the agent so UI cost scales with the viewport, not the cluster. This is the
core constraint: don't move full lists into the UI, don't add deep Vue reactivity over row data (`markRaw` + `shallowRef`
version counters), don't parse large payloads on the main thread. See `docs/performance.md` §12 "What not to do".

Agent data path (`agent/internal/`):

- `ingest` — projecting ListerWatchers: objects are projected to compact rows per page/event *before* caching (protobuf for
  built-ins, Table mode for generic CRDs). Resource kinds register as `ingest.Module`s (`ingest.Register`).
- `store` → `view` — handle-addressed row stores with indexes; materialized sorted/filtered views and windows.
- `problems` — detector engine; detectors register via `problems.Register`. Problems are a synthetic resource type in the
  view engine, so merge/sort/window come for free.
- `wire` — data WebSocket: binary frames, msgpack, dictionary-coded rows, state-diff patches with acks. Spec in
  `docs/protocol.md`.
- `engine` — builds one complete engine (cluster manager, views, ops, streams, jobs, integrations, wire) from a cluster
  `Source`. Desktop, serve, fake and every hosted user's engine are all built here.
- `cluster`, `kube` — per-cluster lifecycle, clients, kubeconfig/exec plugins, passthrough.
- `hosted` — `v8s server`: OIDC login, sessions, per-user engines (each user's own credential; no shared cache by default).
- Integrations (`flux`, `helm`, `kyverno`, `trivy`, `kubescape`, `hubble`, `netkinds`, `platform`, `security`) wire
  themselves up from `init()` (kinds, problem classes, RPC handlers) and are pulled in by blank imports in
  `engine/engine.go`. Adding one means registering in `init()` and importing it there.
- `fake` — the synthetic multi-cluster world for `--fake`; new features should get fake fixtures so the UI can exercise them.
- `webui` — `embed.FS` of the UI build (`dist/` is gitignored except `.keep`; `build-agent.sh` fills it).

Protocol: `agent/pkg/protocol/protocol.go` is the source of truth for wire types. After changing it, run
`yarn protocol:gen`; never edit `packages/client/src/protocol.gen.ts` by hand.

JS workspaces (`packages/`):

- `@v8s/client` — protocol types, msgpack codec, and the Data Worker (`src/worker/`) that owns the socket, decodes, merges
  and posts at most one update per frame per view.
- `@v8s/ui` — Vue 3 + Vite + Pinia app. Logic lives in plain TS under `src/lib/` (unit-tested in `test/`); components in
  `src/components/`, routes in `src/pages/`. Pinia holds UI state only, never cluster data. Visual conventions:
  `docs/design-system.md`.
- `@v8s/desktop` — Electron main/preload; spawns and supervises the agent (`V8S_DEV_URL` points it at the Vite dev server).

## Conventions

- User-facing changes get an entry under `## Unreleased` in `CHANGELOG.md` (the release workflow uses the matching version
  section as release notes) and an update to the relevant page in `docs/guide/`.
- Releases are cut by tagging `vX.Y.Z`; every push to `main` also builds unsigned artifacts and `ghcr.io/zyno-io/v8s:main`.
- oxlint forbids `any` (`typescript/no-explicit-any`) and `@ts-` comments outside spec files.
