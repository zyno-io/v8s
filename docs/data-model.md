# Data model and resource coverage

## Object identity

Every object in v8s is addressed by an **ObjectKey**:

```
cluster / group / version-independent kind (GVR resource) / namespace / name      → stable address (URLs, bookmarks)
uid                                                                                → identity (problems, caches)
resourceVersion                                                                    → freshness (edits, cache validity)
```

- URLs use the address: `/c/prod-eu/apps/deployments/payments/api`. Bookmarks survive object recreation.
- Inside the agent, rows are addressed by dense `u32` **handles** per `(cluster, GVR)`, mapped from `uid`. Handles never leave the agent. The wire carries **per-subscription ids** that are never reused ([performance.md §9](performance.md#9-wire-protocol-and-sync)), and actions resolve them back to `(cluster, uid)` in the agent.
- A **cluster** has a stable local id assigned when it is first added, and a user-assigned name. A cluster can have a linked write context alongside its read context ([auth.md](auth.md#linked-contexts-read-and-write)); otherwise each context is its own cluster.

## Resource tiers

Each resource type has a **view spec** (columns, projection, sort keys, actions, detail tabs). Types without one get the generic CRD treatment from `additionalPrinterColumns`.

| Tier | Meaning |
| --- | --- |
| **Curated** | Hand-written view spec: purpose-built columns, status logic, actions, detail tabs, create form or template. |
| **Generic+** | Generic CRD view spec plus a status-condition reader and known actions. |
| **Generic** | Columns from `additionalPrinterColumns` (or name/namespace/age), YAML view/edit, events, delete. |

## Coverage matrix

`P1`–`P5` are the roadmap phases in [architecture.md §Roadmap](architecture.md#roadmap).

### Core

| Resource | Tier | Phase | Notable columns / actions |
| --- | --- | --- | --- |
| Namespace | Curated | P1 | status, labels; create, delete (type-to-confirm) |
| Node | Curated | P1 | status, roles, version, CPU/mem capacity and allocatable, pods, taints, age; cordon/uncordon (P2), drain (P2), node shell (P2) |
| Event | Curated | P1 | type, reason, object, message, count, last seen; global stream with Warning filter |
| Pod | Curated | P1 | ready, status reason, restarts, node, IP, age, QoS; logs, exec (P2), delete, evict, debug container (P2) |
| Deployment | Curated | P1 | ready/up-to-date/available, images, age; scale, restart, rollback (P2), pause/resume |
| ReplicaSet | Curated | P1 | desired/current/ready, owner; revision history for rollback |
| StatefulSet | Curated | P1 | ready, update strategy/partition; scale, restart |
| DaemonSet | Curated | P1 | desired/current/ready/up-to-date/available; restart |
| Job | Curated | P1 | completions, duration, status; delete with pods, logs |
| CronJob | Curated | P1 | schedule (human-readable), suspend, last schedule/success; trigger now, suspend/resume |
| Service | Curated | P1 | type, cluster IP, external IP, ports, ready endpoints, selector; port-forward (P2) |
| EndpointSlice | Curated | P1 | service, ready/total endpoints |
| Ingress | Curated | P1 | class, hosts, paths → backends with health, TLS; open URL |
| IngressClass | Generic+ | P1 | |
| NetworkPolicy | Curated | P2 | pod selector, ingress/egress rule summary; "pods affected" |
| ConfigMap | Curated | P1 | keys, size; edit |
| Secret | Curated | P1 | type, keys (values masked, reveal per key), age; metadata-only list |
| PersistentVolumeClaim | Curated | P1 | status, volume, capacity, access modes, storage class, used by |
| PersistentVolume | Curated | P1 | status, claim, reclaim policy |
| StorageClass | Generic+ | P1 | provisioner, default, binding mode |
| ServiceAccount | Curated | P2 | secrets/tokens, used by |
| Role, ClusterRole, RoleBinding, ClusterRoleBinding | Curated | P2 | rule summary; "who can" view (P4) |
| HorizontalPodAutoscaler | Curated | P2 | target, min/max/current, metrics |
| PodDisruptionBudget | Curated | P2 | min/max, allowed disruptions, matched pods |
| ResourceQuota, LimitRange | Generic+ | P2 | usage vs hard |
| PriorityClass, RuntimeClass | Generic | P2 | |
| CustomResourceDefinition | Curated | P1 | group, versions, scope, instance count; jump to instances |
| APIService | Generic+ | P2 | available condition |
| Validating/MutatingWebhookConfiguration, ValidatingAdmissionPolicy (+Binding) | Generic+ | P3 | failure policy, endpoint health |
| Lease | Generic | P4 | holder (leader election debugging) |
| CertificateSigningRequest | Generic+ | P4 | approve/deny |
| VolumeAttachment, CSIDriver, CSINode | Generic | P4 | |

### Gateway API and common add-ons

| Resource | Tier | Phase |
| --- | --- | --- |
| Gateway, GatewayClass, HTTPRoute, GRPCRoute (`gateway.networking.k8s.io/v1`) | Curated | P3 |
| cert-manager Certificate, Issuer, ClusterIssuer, CertificateRequest | Generic+ | P3 |
| VerticalPodAutoscaler, KEDA ScaledObject | Generic+ | P5 |
| Argo CD Application | Generic+ (curated later via module) | P5 |
| Cilium CiliumNetworkPolicy, CiliumClusterwideNetworkPolicy | Generic+ | P5 (with Hubble) |

### First-class integrations

See [integrations.md](integrations.md). Flux and Helm in P2; Kyverno and Trivy in P3 (they feed problems); Kubescape and Hubble in P5.

## Metrics

Node and pod CPU/memory usage come from `metrics.k8s.io` (metrics-server) when present: polled every 30 s, only while a visible view shows usage columns, and only for the visible window; never watched (it has no watch). Usage columns appear only when metrics are available. Prometheus integration (history, sparklines) is a P5 option, not MVP.

## Generic CRD support

1. **Discovery** via aggregated discovery (`/apis` with `application/json;g=apidiscovery.k8s.io;v=v2;as=APIGroupDiscoveryList`): one request for all groups, versions, resources, verbs, scope. Cached per cluster in the agent with ETag, refreshed on CRD/APIService watch events and every 5 minutes.
2. **Schema** from the CRD's `openAPIV3Schema` (or `/openapi/v3/apis/<group>/<version>` for aggregated APIs), fetched on demand when a create/edit form or YAML completion needs it.
3. **Columns** from `additionalPrinterColumns`, computed by the API server: generic CRDs are watched in **server-side Table mode** (`Accept: application/json;as=Table;v=v1;g=meta.k8s.io` with `?includeObject=Metadata`, [performance.md §4](performance.md#ingest-modes)), so the agent never decodes unknown CRDs into maps. Without printer columns: name, namespace, age.
4. **Status**: Table mode does not carry `status.conditions`. When the user enables the generic `crd.not-ready` problem class for a CRD group (off by default), the agent promotes that GVR to a typed-partial-JSON projection of `metadata` + `status.conditions` (mode B), which gives the generic view a `Ready`/`Available` tag.

## Starred resource types and bookmarks

- **Starred types** (requirement): any resource type, curated or CRD, can be starred from its list header or the sidebar. Starred types appear at the top of the sidebar in user order, with live counts (from the agent's per-namespace counts, summed over namespaces the user may see; only for already-watched types; others show no count rather than starting a watch).
- **Bookmarks**: any object or filtered list URL can be bookmarked with a name. Bookmarks are cross-cluster aware ("this Deployment in whichever selected cluster has it").
- Desktop stores both locally; hosted stores them per user server-side. Both export/import as JSON.

## Persisted user state

| State | Desktop | Hosted |
| --- | --- | --- |
| Last cluster selection, cluster groups | local settings file | user record |
| Starred types, bookmarks, column layouts, problem class toggles | local settings file | user record |
| Problem acknowledgements | local | user record |
| Saved port-forwards | local | n/a |
| Recent activity | in memory, cleared on quit | in memory, cleared at logout |
| Kubeconfig contexts | the user's kubeconfig files (read, never rewritten) plus an optional v8s-managed kubeconfig for imported clusters | cluster records configured by admins |
