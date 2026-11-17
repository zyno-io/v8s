# First-class integrations

Flux, Helm, Kyverno, Trivy Operator, Kubescape, and Hubble are **first-class**: each has purpose-built list columns, status logic, actions, detail tabs, problem detectors, and a cross-cluster view. Generic CRD browsing still works for all of them (and for everything else), but nobody should have to read raw `.status.conditions` to know whether Flux is healthy.

API facts below were checked against upstream in November 2026 (Kubernetes 1.37, Helm 4.3, Kyverno 1.17+). Items marked **verify** need a check against a live install in P0.

## Detection and lifecycle

An integration is **detected** per cluster from discovery, never assumed:

| Integration | Detected when discovery contains |
| --- | --- |
| Flux | `kustomize.toolkit.fluxcd.io` or `helm.toolkit.fluxcd.io` or `source.toolkit.fluxcd.io` |
| Helm | always (Helm needs no server components); release Secrets found by label `owner=helm` |
| Kyverno | `kyverno.io` or `policies.kyverno.io`; reports from `wgpolicyk8s.io` and/or `openreports.io` |
| Trivy Operator | `aquasecurity.github.io` |
| Kubescape | `spdx.softwarecomposition.kubescape.io` (aggregated APIService) or `kubescape.io` |
| Hubble | a `hubble-relay` Service in any namespace (default `kube-system`), probed lazily |

Discovery runs on connect and on a 5-minute timer (plus immediately when a `CustomResourceDefinition` or `APIService` watch event arrives, if the user can watch them). An integration's sidebar section, problem classes, and watches exist only while it is detected in at least one selected cluster.

Each integration is a **module** in the agent (watch specs, row projections, detectors, actions) and in the UI (columns, detail tabs, views). Modules register with the core; the core has no integration-specific code. This is also the extension point for third-party integrations later (Argo CD, cert-manager, Gatekeeper, KEDA, Istio).

---

## Flux

### Kinds

| Group/version | Kinds |
| --- | --- |
| `source.toolkit.fluxcd.io/v1` | GitRepository, OCIRepository, HelmRepository, HelmChart, Bucket, ExternalArtifact |
| `kustomize.toolkit.fluxcd.io/v1` | Kustomization |
| `helm.toolkit.fluxcd.io/v2` | HelmRelease |
| `notification.toolkit.fluxcd.io/v1` (Receiver also `v1`, Alert/Provider `v1beta3` on older installs) | Provider, Alert, Receiver |
| `image.toolkit.fluxcd.io/v1` | ImageRepository, ImagePolicy, ImageUpdateAutomation |
| `fluxcd.controlplane.io/v1` (Flux Operator, optional, **verify**) | FluxInstance, ResourceSet, ResourceSetInputProvider |

The agent negotiates the served version from discovery and reads through version-tolerant projections (it only needs `metadata`, `spec.suspend`, `spec.interval`, `spec.sourceRef`, `spec.dependsOn`, `status.conditions`, `status.lastAppliedRevision`/`lastAttemptedRevision`, `status.artifact.revision`, `status.inventory`, `status.history`).

### Surfaces

- **Flux overview** (per cluster and across selected clusters): counts by kind and readiness; failing first; suspended count; controllers' own health (the `flux-system` Deployments); last reconcile age outliers ("not reconciled in 3× its interval").
- **Per-kind tables** with Flux columns: Ready (tag from the `Ready` condition with its reason, e.g. `False · ArtifactFailed`), Suspended, Revision (mono, short SHA or chart version), Source, Interval, Last reconcile, Message (truncated, full in tooltip).
- **Kustomization detail**: inventory tree from `status.inventory.entries` (`id` is `<namespace>_<name>_<group>_<kind>`, **verify**), each entry joined to the live object and its health, so "this Kustomization is Ready but two of its Deployments are crash looping" is visible. Dependency graph (`dependsOn`) for the cluster, on canvas.
- **HelmRelease detail**: Flux status plus the underlying Helm release (history, values, manifest diff between revisions), since they are the same release seen from two sides. Remediation state (`status.failures`, `installFailures`, `upgradeFailures`).
- **Source detail**: artifact revision, size, last fetch, and "used by" (reverse index of `sourceRef`).
- **Events** filtered to the object and its controller.

### Actions

| Action | Mechanism |
| --- | --- |
| Suspend / resume | merge-patch `spec.suspend: true/false` |
| Reconcile | annotate `reconcile.fluxcd.io/requestedAt=<RFC3339 now>` |
| Reconcile with source | reconcile the referenced source first, wait for its `lastHandledReconcileAt`, then the object |
| Force (HelmRelease) | set `reconcile.fluxcd.io/forceAt` and `requestedAt` to the same token |
| Reset failures (HelmRelease) | set `reconcile.fluxcd.io/resetAt` and `requestedAt` to the same token |
| Bulk | suspend/resume/reconcile across a selection, across clusters |
| Open in Git | link from GitRepository URL + revision when it is a known forge |

After a reconcile request the row shows `Reconciling…` until `status.lastHandledReconcileAt` equals the token, then resolves to the new Ready state. That gives honest feedback without polling.

Flux-managed objects edited directly get a GitOps banner (see [operations.md §Editing](operations.md#editing)).

### Problems

`flux.not-ready` (Ready=False, excluding suspended and progressing within grace), `flux.stalled` (Stalled=True), `flux.stale` (no successful reconcile in 3× interval), `flux.suspended` (info, off by default), `flux.source-failed`, `flux.controller-down`, `flux.dependency-blocked` (DependencyNotReady chains collapsed to the root cause).

### Performance

Flux objects are few (hundreds per cluster) and small. They are always watched in full (stripped of `managedFields`) as part of the **problems baseline**, so the Flux overview is instant. Kustomization inventories can be large (thousands of entries); the inventory is kept as compact `id` strings and joined to live objects lazily when the detail opens.

---

## Helm

### Data source

Helm stores each release revision in a Secret (or ConfigMap, if the driver is `configmap`) of type `helm.sh/release.v1` named `sh.helm.release.v1.<name>.v<revision>`, payload gzip + base64 JSON. Helm 4 kept the format. Release Secrets carry labels `name`, `owner=helm`, `status`, `version`, `createdAt`, `modifiedAt`.

**The release list needs no decoding.** The agent watches Secrets with label selector `owner=helm` **metadata-only** (`PartialObjectMetadata`; in hosted mode this watch runs **per user**, because Secrets are never in the shared cache), so a cluster with 300 releases × 10 revisions costs 3,000 small metadata records, not 3,000 gzipped payloads (which can each be megabytes: the chart and its templates are inside). Name, namespace, revision, status, and timestamps come from labels. The latest revision per release is computed in the agent.

The payload is fetched and decoded **only** when the user opens a release (values, manifest, notes, chart metadata, hooks) or a detector needs it (it rarely does; status is in the label). Decoded payloads are cached in an LRU (by `uid` + `resourceVersion`) with a byte budget.

Users without `get secrets` in a namespace can still see releases if they can `list` Secrets (metadata only), which covers more RBAC setups.

### Surfaces

- **Releases table** across clusters: name, namespace, revision, status (`deployed` success, `failed` danger, `pending-*` warning, `superseded` info, `uninstalling` neutral), updated, managed by (Flux HelmRelease link when labelled). These come from labels. **Chart, chart version, and app version are not in the labels**: they fill in lazily for **visible rows only**, from the HelmRelease's `status.history` when Flux manages the release, otherwise by decoding the latest revision's payload through the bounded fetch queue (cached by `resourceVersion`). Sorting or filtering by chart triggers a background job that decodes the rest, with a progress indicator.
- **Release detail**: revision history with status per revision; values (user-supplied vs computed, toggle); rendered manifest; **diff between any two revisions** (values and manifest), computed in the agent; notes; resources from the manifest joined to live objects with health; hooks.

### Actions

Performed by the agent with the Helm 4 SDK (`action.Rollback`, `action.Uninstall`, `action.Upgrade`) as the user, so hooks, waits, and history pruning behave exactly like the CLI.

| Action | Behaviour |
| --- | --- |
| Rollback | to a chosen revision; options wait, timeout, cleanup-on-fail, force (behind confirm) |
| Uninstall | options keep-history, wait, timeout, cascade policy; type-to-confirm in production groups |
| Retry | for a `failed` or stuck `pending-*` release: re-run the last upgrade with the stored chart and values (no chart repo access needed; it is in the release). For a stuck `pending-*`, offer "mark failed" first, which is what people do by hand today |
| Upgrade with edited values | edit values YAML, preview the manifest diff via dry run, apply. P3, not MVP |

**Flux-managed releases** (labels `helm.toolkit.fluxcd.io/name`/`namespace`) route actions to Flux: Rollback and Retry become HelmRelease reconcile / force / reset, and Uninstall warns that Flux will reinstall unless the HelmRelease is suspended or deleted. Direct Helm actions stay available behind an "I know, do it anyway" confirm.

### Problems

`helm.failed` (latest revision `failed`), `helm.stuck-pending` (`pending-*` older than 15 min), `helm.resources-unhealthy` (deployed, but manifest objects are not healthy; uses the problems join, P3).

---

## Kyverno

### Kinds

| Group/version | Kinds | Notes |
| --- | --- | --- |
| `kyverno.io/v1` | ClusterPolicy, Policy | Classic policies; deprecated since 1.17, still widely deployed |
| `policies.kyverno.io/v1` | ValidatingPolicy, ImageValidatingPolicy, MutatingPolicy, GeneratingPolicy, DeletingPolicy, their `Namespaced*` variants, PolicyException | CEL-based, stable since 1.17 |
| `kyverno.io/v2` | PolicyException, CleanupPolicy, ClusterCleanupPolicy | Legacy exceptions for ClusterPolicy |
| `wgpolicyk8s.io/v1alpha2` | PolicyReport, ClusterPolicyReport | Default report API |
| `openreports.io/v1alpha1` | Report, ClusterReport | Opt-in since 1.15; the future direction |
| `reports.kyverno.io/v1` | EphemeralReport, ClusterEphemeralReport | Intermediate; **not watched** (high churn, aggregated into the above) |

The agent reads both report APIs and normalizes them into one `PolicyResult` shape.

### Surfaces

- **Policies table**: kind, name, scope, mode (`Audit`/`Enforce`/`Mixed`, computed per rule from `rules[].validate.failureAction`, falling back to the deprecated spec-level `validationFailureAction`; `validationActions` for CEL policies), background scan on/off, Ready, rule count, and live result counts (pass/fail/warn/error/skip) aggregated from reports.
- **Policy detail**: rules, matched kinds, exceptions that apply, and a **violations table** (resource, cluster, namespace, rule, message, age) across all selected clusters.
- **Per-resource Security tab**: every Kyverno result for that object, failing first.
- **Admission denials** surfaced from Events (`PolicyViolation` reason) so "why did my deploy fail?" has an answer.

### Actions

- **Switch Audit ⇄ Enforce** for a whole policy or one rule: patches each rule's `validate.failureAction` (Kyverno ≥ 1.13; the per-rule field overrides the spec-level one, so patching only `spec.validationFailureAction` can show "Enforce" while rules still audit), or `validationActions` for CEL policies. Confirm shows the current fail count, so the user knows what Enforce would block.
- **Create PolicyException** from a violation, prefilled with the policy, rule, and resource (`policies.kyverno.io/v1` for CEL policies, `kyverno.io/v2` for ClusterPolicy); opens in the YAML editor for review, since exceptions are security decisions.
- **Re-scan** is not an API in Kyverno; the UI shows report age and the background-scan interval instead of a fake button.

### Problems

`policy.fail` (one problem **per resource**, from the report's Fail count; severity warning by default; the per-policy/per-rule breakdown and the policy's `policies.kyverno.io/severity` are shown when the row is expanded, from a lazily fetched report body), `policy.error` (per resource, from the Error count), `policy.not-ready` (policy `Ready` condition; policies are in the baseline), `policy.admission-denied` (from Events, last hour).

### Performance

PolicyReports are **one report per resource** in Kyverno ≥1.10, so a 10k-pod cluster can have tens of thousands of reports, each listing results for every applicable rule. The agent:

- Watches reports in **server-side Table mode** ([performance.md §4](performance.md#ingest-modes)): the report CRDs' printer columns carry Pass/Fail/Warn/Error/Skip counts, so the always-on cost is metadata plus five integers per report. No result arrays are transferred to know which resources are failing.
- Per-resource problems ("Deployment `api`: 3 policy failures") come straight from those counts.
- **Result details** (which policy, which rule, the message) are fetched lazily, one report at a time, when the user expands a problem or opens a resource's Security tab, and cached by `resourceVersion`.
- The **violations-by-policy** view needs every failing result across the cluster. It runs as an explicit agent job (paged list of reports, results projected to `{resourceRef, policy, rule, result, severity, message}` with interned strings, `pass` results counted not stored), cached by report `resourceVersion` and kept fresh from the Table watch's change notifications. Desktop-primary.
- Nothing is ever sent to the UI except windowed rows.

---

## Trivy Operator

### Kinds

All in `aquasecurity.github.io/v1alpha1`: VulnerabilityReport, ClusterVulnerabilityReport, ConfigAuditReport, ClusterConfigAuditReport, ExposedSecretReport, RbacAssessmentReport, ClusterRbacAssessmentReport, InfraAssessmentReport, ClusterInfraAssessmentReport, ClusterComplianceReport, SbomReport, ClusterSbomReport.

Vulnerability, ExposedSecret, and ConfigAudit reports carry `.report.summary` with `criticalCount`, `highCount`, `mediumCount`, `lowCount` (vulnerability reports add `unknownCount`).

### The size problem

VulnerabilityReports are the biggest objects v8s will meet: one per container image per workload, each listing every CVE with title, description, links, and package data. A single report can be hundreds of KB; a cluster can hold thousands. Watching them in full would cost more memory than every other kind combined. SbomReports are worse.

The strategy:

1. **Server-side Table watch.** Trivy's CRDs declare printer columns for repository, tag, scanner, and Critical/High/Medium/Low/Unknown counts. The agent watches reports in server-side Table mode (`Accept: application/json;as=Table;v=v1;g=meta.k8s.io`, `?includeObject=Metadata`) ([performance.md §4](performance.md#ingest-modes)), so the always-on cost is metadata (labels carry the workload: `trivy-operator.resource.kind/name/namespace`, `trivy-operator.container.name`) plus a few cells. **No vulnerability arrays are transferred to know the counts.** Default-on detectors use only these counts, keyed by image `repository:tag`; whether a fix is available needs per-CVE `fixedVersion`, which only bodies carry, so fixability lives in the opt-in CVE index (item 3).
2. **Full report on demand.** The vulnerability detail view fetches one report (JSON; protobuf is not served for CRDs; CBOR once available) through a per-cluster bounded queue (4 concurrent, [performance.md §5](performance.md#5-api-server-guardrails)) and streams it to the UI as a windowed, sortable table (CVE, severity, package, installed, fixed, title). Thousands of rows are fine; it is a virtualized table like any other.
3. **Cross-cluster CVE index (P5).** "Where is CVE-2026-1234 running?" needs CVE ids, which only bodies have. It runs as an explicit agent job: paged list (`limit=20`) with streaming decode, keeping only `{vulnerabilityID, severity, fixedVersion, image}` for critical and high, cached by report `resourceVersion`, refreshed incrementally from the Table watch. Desktop-primary.
4. **SbomReports are never watched**, only fetched on demand from a workload's Security tab.

### Surfaces

- **Vulnerabilities overview**: images ranked by critical/high counts, deduplicated by `repository:tag` across workloads and clusters (the same image in 40 pods is one row with "40 workloads").
- **Config audit**, **exposed secrets**, **RBAC assessment**, **infra assessment**: tables of failing checks with severity, ID, title, and the resource.
- **Compliance**: ClusterComplianceReport summary (pass/fail per control), linked to failing checks.
- **Per-resource Security tab**: the workload's image vulnerabilities, config audit failures, exposed secrets.

### Actions

- **Rescan**: delete the report; the operator recreates it on its next reconcile. The button says exactly that ("Delete report to trigger a rescan").
- Export CSV/JSON of a filtered vulnerability view (streamed from the agent).

### Problems

`vuln.critical` (critical count > 0, on by default), `vuln.high` (off by default), `vuln.critical-fixable` (critical with a fix available; needs the CVE index job, off by default, desktop-primary), `misconfig.critical`/`misconfig.high`, `secret.exposed`, `rbac.high`. Vulnerability problems are **per image, not per CVE**, so 300 CVEs in one image are one problem row ("nginx:1.25 · 4 critical, 31 high · 12 workloads").

---

## Kubescape

### Data source

Kubescape's operator stores results through its **storage component, an aggregated API server** (`spdx.softwarecomposition.kubescape.io/v1beta1`), not CRDs. That matters: the objects are served by an extension API server, which may be slower, may not support watch the same way, and is unavailable if that pod is down.

Kinds include VulnerabilityManifest, VulnerabilityManifestSummary (namespaced; the listing kind), WorkloadConfigurationScan and its summary, SBOMSyft, ApplicationProfile, NetworkNeighborhood, and a newer ContainerProfile. The exact set varies by version, so the agent builds its watch list from discovery and maps known kinds; unknown kinds fall back to generic browsing. **Verify** the kind list and scopes against a live install in P0.

Runtime threat-detection alerts from Kubescape's node-agent are **not** stored in the Kubernetes API; they go to exporters (Alertmanager, HTTP, syslog, stdout). v8s does not ingest them in MVP. A later option is an HTTP exporter target on the agent for hosted installs.

### Strategy

- Watch **summary kinds only** (VulnerabilityManifestSummary, WorkloadConfigurationScanSummary), in Table mode where the aggregated API serves useful Table columns, otherwise as typed partial JSON projections (summaries are small). Full manifests, SBOMs, ApplicationProfiles, and NetworkNeighborhoods are fetched on demand.
- If watch on the aggregated API fails or is unsupported, fall back to a **paged list every 5 minutes** with `resourceVersion` comparison.
- Normalize into the same shapes as Trivy (`ImageVulnSummary`, `ConfigCheckResult`) so the Security views and problem classes are scanner-agnostic. When both scanners run, the UI shows a source column rather than merging their verdicts.

### Surfaces and actions

- Shares the Security section with Trivy: vulnerabilities, configuration scan (controls with framework mapping such as NSA, MITRE, CIS), plus Kubescape-only **ApplicationProfile** and **NetworkNeighborhood** viewers (what a workload actually executed and connected to). The NetworkNeighborhood view cross-links to Hubble flows when both exist.
- **Trigger scan**: the documented mechanisms are the operator's scheduled-scan CronJobs and the `operatorcommands.kubescape.io/v1alpha1` CRD. v8s offers "Run scan now" by creating a Job from the scan CronJob (the same as triggering any CronJob). Creating `OperatorCommand` objects directly is deferred until the API is confirmed stable (**verify**).

### Problems

The same classes as Trivy (`vuln.*`, `misconfig.*`), tagged with source `kubescape`.

---

## Hubble (Cilium)

### Data source

Hubble Relay exposes a **gRPC** `observer.Observer` service (GetFlows server-streaming, GetNodes, GetNamespaces, ServerStatus, GetAgentEvents, GetDebugEvents). The relay listens on 4245; its Service is port 80 (443 with TLS). Relay server TLS is off by default. There is no HTTP/JSON API.

**Access path.** The agent opens a Kubernetes **port-forward** (through the API server, as the user) to a ready `hubble-relay` pod and speaks gRPC over it. This needs `create pods/portforward` in the relay's namespace, which v8s states plainly when it is missing. API-server service proxy is not used: it does not reliably carry HTTP/2 streaming. Directly reached clusters whose operators expose relay through an Ingress can configure a direct relay URL (and TLS) instead, on desktop. The relay has **no authorization of its own** (anyone who reaches it sees every flow), so a direct relay URL bypasses both RBAC and any gateway: it is **not allowed for clusters reached through a gateway**, and in hosted mode it is allowed only on shared-cache clusters, with server-injected per-user `FlowFilter`s and redaction of the far end ([performance.md §10](performance.md#10-hosted-mode-fan-out-and-sharing)). Everywhere else, flows come through a port-forward carrying the user's own token.

This is one of the reasons the agent is written in Go: the Cilium Observer client and flow types are Go, and gRPC streaming over a port-forward is straightforward there.

### Surfaces

- **Flows tab** on pods, workloads, Services, and namespaces: a live, virtualized flow table (time, source, destination, port/protocol, L7 summary such as `GET /api 503` or `DNS A example.com`, verdict tag, drop reason). Filters map one-to-one to Hubble `FlowFilter` (verdict, drop reason, protocol, HTTP status/method/path, DNS query, direction, labels) and are **applied server-side** by the relay.
- **Service map** for a namespace or workload: nodes are workloads (identity-aggregated, not pods), edges carry request rate, error rate, and drops over the last N minutes. Built in the agent from a filtered flow stream, sent to the UI as a small graph, drawn on canvas.
- **Drops view**: dropped flows across selected clusters grouped by `(source workload, destination workload, drop reason)` (in hosted mode, only pairs where the user can see both namespaces), with counts and last-seen, linked to the NetworkPolicy/CiliumNetworkPolicy most likely responsible.
- **Hubble status**: relay health, connected nodes, flow buffer capacity per node (from ServerStatus), so "why are there no flows?" is answerable.

### Actions

Hubble is read-only. Cross-links: from a drop to the matching policies (Kubernetes NetworkPolicy, `cilium.io/v2` CiliumNetworkPolicy and CiliumClusterwideNetworkPolicy) with an "Edit policy" shortcut.

### Problems

`network.policy-drops` (sustained `POLICY_DENIED`/`POLICY_DENY` drops between a pair of workloads above a threshold in a window, e.g. ≥10 in 5 min), `network.l7-errors` (HTTP 5xx rate above threshold for a service, off by default), `network.dns-errors` (NXDOMAIN/SERVFAIL spikes, off by default), `hubble.relay-down`.

These need a **continuous** flow subscription, which is expensive. It runs per `(user, cluster)` through a port-forward carrying the user's token (so through the gateway where there is one), only while that user has v8s open, and counts toward the per-user cost model ([performance.md §10](performance.md#10-hosted-mode-fan-out-and-sharing)). The problems engine therefore subscribes to a narrow server-side filter (`verdict=DROPPED`, plus `http_status_code=5xx` only when that class is enabled), aggregates into counters per pair per minute, and keeps nothing else. Network problem classes are **off by default** and enabled per cluster.

### Performance

Flows are the highest-rate stream in v8s (thousands per second on a busy cluster). Rules:

- **Always filter server-side.** Never request an unfiltered `GetFlows` follow stream. The UI cannot ask for one: the agent requires at least a namespace or workload scope.
- **Aggregate in the agent.** The service map and drops view receive counters, not flows.
- **Rate-limit to the UI.** The live flow table gets at most ~200 flows/s, batched every 100 ms; beyond that the agent samples and the toolbar says "showing 200/s of ~3,400/s, narrow the filter". The UI keeps a ring buffer (10k desktop, 2k mobile).
- **Pause on hidden.** A flow subscription with no visible consumer is cancelled after 10 s.
