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

## IP addresses

Kinds with IPs keep the addresses of each object next to its row (IP search, the IP-aware list filter, the Pods `ip` column). Fields:

| Kind | Fields |
| --- | --- |
| Pod | `status.podIPs` (falls back to `podIP`); Multus secondary networks from `k8s.v1.cni.cncf.io/network-status` (non-default entries; annotations over 16 KiB skipped, at most 32). `hostIP` is not indexed: the Node stands in for it |
| Service | `spec.clusterIPs` (`None` skipped), `externalIPs`, `loadBalancerIP`, the `metallb.universe.tf`, `metallb.io`, `kube-vip.io` and Cilium LB-IPAM annotations, `status.loadBalancer.ingress[].ip` |
| EndpointSlice | `endpoints[].addresses` (FQDN slices skipped) |
| Node | `InternalIP`, `ExternalIP`, `spec.podCIDRs` (falls back to `podCIDR`), CNI annotations (Calico, Flannel, Cilium, Kube-OVN) |
| Ingress | `status.loadBalancer.ingress[].ip` |
| Gateway | `status.addresses`, `spec.addresses` of type IPAddress |

At most 1024 tokens are kept per row.

On-demand sources (read only while an IP search needs them, see performance.md §4):

| Kind | Fields |
| --- | --- |
| ServiceCIDR (`networking.k8s.io` v1, v1beta1) | `spec.cidrs` |
| MetalLB IPAddressPool (`metallb.io/v1beta1`) | `spec.addresses` (CIDR or `a-b`, v4 and v6) |
| MetalLB BGPPeer (`metallb.io` v1beta2, v1beta1) | `peerAddress`; `sourceAddress` (ranks lower) |
| Multus NetworkAttachmentDefinition (`k8s.cni.cncf.io/v1`) | `spec.config` JSON (up to 64 KiB): host-local `subnet`, `rangeStart`/`rangeEnd`, `ranges[][]`, static `addresses[]`, whereabouts `range`, `range_start`/`range_end`, `ipRanges[]`, ovn-k8s `subnets`, one level of conflist `plugins[]`; gateways rank lower. An invalid config shows `invalid config` and holds no addresses |
| CiliumLoadBalancerIPPool (`cilium.io` v2, v2alpha1) | `spec.blocks[]` (`cidr` or `start`/`stop`) and the legacy `spec.cidrs[].cidr` |
| CiliumPodIPPool (`cilium.io` v2alpha1, v2) | `spec.ipv4.cidrs`, `spec.ipv6.cidrs` |
| CiliumNode (`cilium.io/v2`) | `CiliumInternalIP` addresses only (InternalIP and ExternalIP duplicate the Node), `spec.ipam.podCIDRs`, `spec.ipam.pools.allocated[].cidrs` |
| Calico IPPool (`crd.projectcalico.org/v1`) | `spec.cidr` |

Once these CRDs have curated modules, their list pages show the curated columns (including a range column) instead of printer columns. Not indexed on purpose: v1 Endpoints, `networking.k8s.io` IPAddress, CiliumEndpoint, Kube-OVN IP, Calico IPAMBlock and `projectcalico.org/v3` (they duplicate pod, Service or pool addresses).

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
| Event | Curated | P1 | type, reason, object, message, count, last seen, source; global stream is Warning-only; all events of one object on demand in its Events tab |
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

Pod CPU/memory usage comes from `metrics.k8s.io` (metrics-server) when present: LISTed every 30 s (it has no watch), only while a visible view shows usage columns or sorts by one, for the namespaces of the views that want it (a sort needs every row's value, so never just the visible window). The agent joins each poll to the rows when it sorts and encodes; rows themselves are not updated. Usage columns are dropped (and listed disabled in the Columns menu) when no selected cluster has metrics-server. Prometheus integration (history, sparklines) is a P5 option, not MVP.

Pod columns (all integers; CPU in millicores, memory in MiB, percentages in per-mille):

| Columns | Meaning |
| --- | --- |
| `cpuUse`, `memUse` | usage: CPU is `ceil(sum of container nanocores / 1e6)` as `kubectl top`; memory is floored to MiB |
| `cpuReq`, `cpuLim`, `memReq`, `memLim` | the pod's effective requests and limits, from the spec (not in-place resize status), hidden by default |
| `cpuPctReq`, `cpuPctLim`, `memPctReq`, `memPctLim` | usage over request / limit |
| `cpuPctNode`, `memPctNode` | usage over the allocatable of the pod's node (hidden by default; the node is always named in the tooltip) |

Requests and limits follow Kubernetes: regular containers are summed, init containers are the maximum of the sum so far (plus the restartable ones before them), restartable init containers (native sidecars) count as running, a pod-level `resources` block overrides, and `overhead` is added to requests and to non-zero limits. A limit exists only when every regular container and every sidecar has one (or the pod has a pod-level limit); otherwise the pod is unbounded and shows `n/a`. Node percentages use the node's **allocatable**, not capacity (it is what the scheduler packs against).

Node columns: `cpuUse` and `memUse` are the node's usage (NodeMetrics), `cpuPctAlloc` and `memPctAlloc` that usage over the node's own allocatable, and `cpu` and `memory` (the keys the old string columns had, so saved layouts still apply) are the allocatable itself, in millicores and MiB; they are numeric now and sort as such, and need no metrics-server. `os`, `kernel`, `runtime` and `taints` are hidden by default.

Overview capacity: the synthetic `v8s/v1/capacity` view has one row per cluster. The agent computes it with a throttled full scan of the pod and node stores (at most every 2 s, only while an Overview has it open, and only when a store's change counter or the node usage snapshot moved; about 0.75 ms at 50k pods and 5k nodes). The denominator is the allocatable of **all** nodes, control-plane and cordoned ones included (as `kubectl describe nodes`). Requests and limits are summed over pods that are bound to a node and have not finished (Succeeded and Failed pods and unscheduled pods are skipped); a pod without a limit adds nothing to the limits and is counted in `podsNoCPULim` / `podsNoMemLim`. Usage is the sum of the node metrics that report, and is -1 when none does.

Cells: `null` means no data (no metrics yet, a pod metrics-server does not report such as Pending or Completed, or a sample older than the pod), `-1` means n/a (no request, no limit or no node to divide by). Missing values sort last in both directions.

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
