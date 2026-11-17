# Problems engine

One view answers "what is broken, anywhere I'm looking?" across every selected cluster: failing pods, failed Jobs, failed Helm releases, unhealthy Flux objects, Ingresses that route nowhere, NotReady nodes, policy failures, critical vulnerabilities, policy drops. It is the default landing page on a phone and the second-most-used page on desktop.

## Requirements

- **Cross-cluster.** One list over all selected clusters, sorted by severity then recency.
- **Live.** A problem appears within ~2 s of the triggering watch event and disappears when resolved.
- **Cheap.** Detecting problems must not require watching everything in full. The always-on cost per cluster is bounded (see the baseline below).
- **Stable identity.** A problem has an ID that survives agent restarts and re-evaluation, so acknowledgements stick.
- **Toggleable classes.** Each class can be switched off; the choice is remembered per user (and per cluster group, optionally).
- **Explainable.** Every problem says why, links the object, and links the evidence (events, conditions, related objects).

## Where it runs

In the **agent**, next to the watch caches. Detectors read the agent's indexed, projected store directly; they never cross a process boundary per object. The UI subscribes to the problems query like any other view and receives windowed rows plus counts per class.

Hosted mode runs detectors **per user** over that user's own watches (the default; see [performance.md §10](performance.md#10-hosted-mode-fan-out-and-sharing)), so problems contain only what the user's own token can read, and they are evaluated only while the user has v8s open. On impersonation or kiosk clusters with the optional shared cache, detectors run **once per cluster**, and each problem is filtered per user by its subject's authorization scope; a problem about an object a user cannot see is never shown to them.

Problems are a **synthetic resource type** (`v8s/problems`) in the agent's view engine, so cross-cluster merge, sort, grouping, windowing, and authorization filtering are the same code that serves resource lists.

## Model

```ts
interface Problem {
  id: string;            // hash(clusterId, class, group/kind, namespace, name, discriminator)
  cluster: string;
  class: ProblemClass;   // e.g. 'pod.crashloop'
  severity: 'critical' | 'error' | 'warning' | 'info';
  subject: { group: string; kind: string; namespace?: string; name: string; uid?: string };
  title: string;         // "CrashLoopBackOff: api (restarts 14)"
  message?: string;      // first line of the underlying reason/message, truncated
  evidence: EvidenceRef[];  // conditions, events, related objects (refs, not copies)
  since: number;         // derived from the evidence: condition lastTransitionTime, container startedAt, Job failure time
  lastChanged: number;   // last time severity, count, or message changed
  count?: number;        // aggregated problems (vulns per image, drops per pair)
  source?: string;       // 'trivy' | 'kubescape' | 'kyverno' | 'hubble' | ...
}
```

**Stable IDs.** IDs hash the subject's **name**, not its uid, so a Deployment deleted and recreated by GitOps keeps its problem and its acknowledgement. The discriminator is class-specific: container name for crash loops, none for policy failures (one problem per resource; Kyverno names reports by the resource's uid, so the report name must not be used), image `repository:tag` for vulnerability problems, `(src, dst, reason)` for drops. Pod-level problems that roll up to a workload use the workload's identity, so pod churn does not create new problems.

**No persistence needed for "since".** `since` comes from the evidence itself (the condition's `lastTransitionTime`, the container's `startedAt`, the Job's failure time), so it survives agent restarts without a database. Only acknowledgements are persisted.

**Grouping.** Pod-level problems roll up to their controller when more than one pod of the same workload has the same class: "Deployment `api`: 3/5 pods CrashLoopBackOff" is one row with an expander, not five rows. This is the single most important thing for keeping the view readable during an incident.

## Detectors

A detector is a pure function over a small set of watched inputs, registered by a module:

```ts
interface Detector {
  class: ProblemClass;
  inputs: GVRSpec[];                // what it needs watched, and at what fidelity (row projection / metadata / full)
  defaultEnabled: boolean;
  evaluate(key: ObjectKey, store: StoreView): ProblemDraft[];   // re-run on change of key
  dependents?(key: ObjectKey, store: StoreView): ObjectKey[];   // keys whose problems may change when key changes (joins)
}
```

**Incremental evaluation.** A watch event marks its object dirty, plus its `dependents` (EndpointSlice → its Service → Ingresses routing to it; Pod → its owner chain; Node → pods on it). One evaluator pass per cluster runs over the dirty set every **250 ms** on desktop and every **1 s** hosted, diffs each key's problem set against the previous one, and emits added/updated/removed problems. A 5,000-pod rollout therefore costs bounded work per pass.

**Joins use EndpointSlices, never selector matching.** The API server already joined Services to Pods; "Service has 0 ready endpoints" and "Ingress backend Service is missing or empty" read EndpointSlice ready counts through the `endpointslice by service` and `ingress by backend service` indexes.

**Time-based conditions** ("Pending > 5 min", "NotReady > 60 s", "restarts increased within 10 min", "CronJob missed its schedule", "Flux Ready=False > 5 min") are driven by a min-heap of deadlines, not by polling. The thresholds double as hysteresis, so normal rollouts do not generate noise. A problem that resolves within 30 s of being raised is held for that window instead of flapping.

## Problem classes

`MVP` classes ship in P3; the rest arrive with their integration. Default severity in brackets.

### Workloads and pods

| Class | Detection | Default |
| --- | --- | --- |
| `pod.crashloop` | container `waiting.reason=CrashLoopBackOff` [error] | on |
| `pod.image-pull` | `ImagePullBackOff`/`ErrImagePull`/`InvalidImageName` [error] | on |
| `pod.oomkilled` | `lastState.terminated.reason=OOMKilled` within 1 h [warning] | on |
| `pod.pending` | `Pending` > 5 min; reason from `PodScheduled` condition (Unschedulable, insufficient cpu/memory, taints) [warning] | on |
| `pod.not-ready` | Running but not Ready > 5 min (failing readiness) [warning] | on |
| `pod.evicted` | `Failed` with reason `Evicted` [info] | off |
| `pod.stuck-terminating` | deletionTimestamp > 10 min ago [warning] | on |
| `pod.config-error` | `CreateContainerConfigError` (missing ConfigMap/Secret key) [error] | on |
| `workload.unavailable` | Deployment/StatefulSet/DaemonSet available < desired beyond progress deadline [error]; rolls up pod problems | on |
| `workload.rollout-stuck` | `Progressing=False, reason=ProgressDeadlineExceeded` [error] | on |
| `job.failed` | Job `Failed=True` (backoff limit, deadline) [error] | on |
| `cronjob.last-failed` | last scheduled Job failed [error] | on |
| `cronjob.not-scheduling` | suspended=false and no successful run in 2× its schedule interval [warning] | on |
| `pod.restarting` | restart count increased within 10 min while not (yet) in CrashLoopBackOff [warning] | off |
| `hpa.unable` | HPA `ScalingActive=False` or at max replicas for > 30 min [warning] | off |

### Nodes and capacity

| Class | Detection | Default |
| --- | --- | --- |
| `node.not-ready` | `Ready` ≠ True [critical] | on |
| `node.pressure` | Memory/Disk/PID pressure [error] | on |
| `node.cordoned` | unschedulable [info] | off |
| `pvc.pending` | PVC `Pending` > 5 min [error] | on |
| `pv.failed` | PV `Failed`/`Released` with Retain [warning] | off |

### Networking

| Class | Detection | Default |
| --- | --- | --- |
| `ingress.no-backend` | Ingress rule → Service missing, or Service has no ready endpoints (EndpointSlice join) [error] | on |
| `service.no-endpoints` | Service with selector and zero ready endpoints > 2 min (excluding ExternalName/headless-by-design) [warning] | on |
| `gateway.route-unaccepted` | Gateway API HTTPRoute `Accepted=False` or `ResolvedRefs=False` [error] | on |
| `cert.expiring` | cert-manager Certificate not Ready or `status.notAfter` < 14 d (requires cert-manager; plain TLS Secrets are not read, because Secrets are only ever fetched on demand) [warning] | on when cert-manager is present |

### Platform and events

| Class | Detection | Default |
| --- | --- | --- |
| `events.warning-burst` | ≥ N `Warning` events for one object in 10 min not already explained by another class [warning] | off |
| `apiservice.unavailable` | `APIService` `Available=False` (breaks discovery, kubectl, and Kubescape storage) [error] | on |
| `webhook.failing` | validating or mutating admission webhook whose Service has no ready endpoints and `failurePolicy=Fail` [critical] | on |
| `cluster.unreachable` | the agent cannot reach the API server / auth expired [critical] | always |

### Integrations

From [integrations.md](integrations.md): `helm.failed`, `helm.stuck-pending`, `flux.not-ready`, `flux.stalled`, `flux.stale`, `flux.source-failed`, `flux.controller-down`, `flux.dependency-blocked`, `flux.suspended` (off), `policy.fail`, `policy.error`, `policy.not-ready`, `policy.admission-denied`, `vuln.critical`, `vuln.high` (off), `misconfig.critical`, `misconfig.high` (off), `secret.exposed`, `rbac.high` (off), `network.policy-drops` (off), `network.l7-errors` (off), `network.dns-errors` (off), `hubble.relay-down`.

## The problems baseline (always-on watches)

When a cluster is selected, the agent starts a fixed **baseline** set of watches that feed the default-on detectors, whether or not the Problems view is open. The baseline is designed to be cheap:

| Kind | Fidelity | Why |
| --- | --- | --- |
| Pods | row projection (phase, conditions, container states/reasons/restarts, owner, node) | most classes |
| Nodes | row projection | node classes |
| Deployments, StatefulSets, DaemonSets, ReplicaSets | status + owner projection | rollups, unavailable |
| Jobs, CronJobs | status projection | job classes |
| Services, EndpointSlices, Ingresses | projection (selectors, ports, backend refs, ready counts) | network joins |
| PVCs | status projection | storage |
| Events | `type=Warning` field selector only, 1 h retention | evidence and `events.*` |
| APIServices, Validating/MutatingWebhookConfigurations | full (few, small) | platform |
| CustomResourceDefinitions | metadata only | discovery invalidation |
| Flux sources, Kustomizations, HelmReleases | full, stripped | Flux classes |
| Helm release Secrets | metadata only, `owner=helm`; per user, like every hosted watch | Helm classes |
| Kyverno policies (ClusterPolicy, Policy, ValidatingPolicy, NamespacedValidatingPolicy) | status projection | `policy.not-ready` |
| Gateway API Gateways/HTTPRoutes/GRPCRoutes, cert-manager Certificates | status projection (when installed) | `gateway.*`, `cert.*` |
| Kyverno / OpenReports policy reports | server-side Table (Pass/Fail/Warn/Error/Skip counts) + metadata | policy classes |
| Trivy reports, Kubescape summaries | server-side Table (severity counts) + metadata | vuln/misconfig classes |

Kinds a detector needs but the user cannot `list`/`watch` are skipped; the Problems view shows "Some checks are unavailable in `prod-eu` (no permission to list `ingresses`)" instead of silently reporting zero problems.

On a phone the baseline is the same (it runs in the agent, not the phone); only the UI subscription is narrower. See [packaging.md §Mobile](packaging.md#mobile-web).

## Cross-cluster merge

Each cluster's engine emits problem deltas independently. The agent's query layer merges them into the user's problems view:

- **Sort**: severity, then `lastChanged` descending (or `since`, user choice).
- **Group by**: none, class, cluster, namespace, or cluster group.
- **Cross-cluster aggregation** (optional toggle): problems with the same `(class, subject kind, namespace, name)` across clusters collapse into one row with cluster tags (in hosted mode, collapsing happens **after** per-user filtering, so the cluster list and counts only reflect what the user may see) ("`ingress-nginx` webhook failing in 4 clusters"), which is how fleet-wide incidents usually look.
- Counts per class and per cluster are maintained incrementally, so the filter chips show live counts without scanning.
- Disabled classes are filtered **in the agent**, so a client (especially a phone) never receives problems it would hide.

## UX

- **Filter bar**: the CI Dashboard filter pattern. One chip per problem class family (Pods, Workloads, Jobs, Nodes, Network, Storage, Flux, Helm, Policy, Vulnerabilities, Network flows, Platform), each with an icon and live count; expanding a chip toggles individual classes. Severity segmented control (All / Error+ / Critical). Cluster and namespace filters. Search.
- **Rows**: severity tag, cluster tag, class, subject (kind/namespace/name), title, since. Expanding a row shows the evidence inline: the triggering condition/state, the last 5 related Warning events, and quick actions (logs, describe, restart, reconcile, rollback; whatever fits the class).
- **Acknowledge** (the CI Dashboard's dismiss): hides a problem for this user until it materially changes (severity rises, count grows past a threshold, or it resolves and recurs). Optional snooze (1 h / 1 d / 1 w). "Show acknowledged" brings them back faded with Restore. Acknowledgements are stored per user (desktop: local; hosted: server) keyed by problem ID.
- **Problem class presets** saved per cluster group, e.g. production shows everything, staging hides `pod.pending` and vulnerability classes.
- **Notifications** (later): desktop notifications for new critical problems in chosen groups while the app is open. Off by default. There is no background or push alerting: v8s only evaluates problems while a client is open.

## Performance notes

- Detectors run in the agent on the dirty set (250 ms desktop, 1 s hosted), never in a UI thread.
- Evaluation cost is proportional to changed keys, not cluster size. A full re-evaluation happens only on cluster (re)connect.
- Problem objects are small (~300 bytes each); even 10k problems is a few MB in the agent and one windowed query for the UI.
- Problems carry evidence **refs**, never copies of events or objects.
