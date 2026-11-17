# Operations and their security model

Everything that changes a cluster or opens an interactive channel into it. All of it runs in the **agent** (see [architecture.md](architecture.md#process-model)), with the user's own Kubernetes identity, so the API server's RBAC and admission are always the final authority. v8s adds guardrails on top; it never grants anything the user's credentials do not.

## Principles

1. **The user's identity, always.** Every action carries the user's own credential all the way through: the kubeconfig credential on desktop, the user's own IdP token in hosted mode, to the API server or to a gateway such as Warpgate ([auth.md](auth.md)). The agent never uses a shared privileged credential to perform a user action.
1. **Elevation is explicit.** Actions the default access profile cannot perform (node shell, drain, cluster-scoped edits on an operator role) offer "Run as `<elevated profile>`" in the confirm dialog, which shows that profile's endpoint. Time-boxing and approval are the gateway's (Warpgate tickets); without an active gateway session, v8s says so and links to the gateway. Elevated writes carry the uid and resourceVersion the user saw ([auth.md §Access profiles](auth.md#access-profiles-one-cluster-several-contexts)).
2. **RBAC-aware UI, not RBAC-enforcing UI.** The agent asks `SelfSubjectRulesReview` (per namespace, cached) and `SelfSubjectAccessReview` (for specific verbs on specific objects) so buttons the user cannot use are disabled with a reason ("You can't `delete` pods in `payments`"). The server still decides; a stale cache produces a clean 403 toast, not a broken state.
3. **Every mutation is previewable.** Edits and creates go through server-side dry run (`dryRun=All`) first, so admission webhooks (Kyverno, Gatekeeper) reject before the user commits.
4. **Destructive actions confirm proportionally.** Single object: confirm dialog. Bulk, cross-cluster, or production-group: type-to-confirm with the count and cluster names spelled out.
5. **v8s is a GUI, not a system of record.** It keeps no long-term logs. The authoritative audit trail is the gateway's (where one is used; Warpgate records the person and the session) and the Kubernetes audit log; v8s shows a short-lived [recent activity](#recent-activity) list for the user's own convenience.

## Editing

- **YAML edit is an Update** (PUT of the edited object with the `resourceVersion` the user opened, field manager `v8s`): `kubectl edit` semantics. Only fields the user actually changed become owned by `v8s`, and deleting a field really deletes it. Applying the whole edited object with server-side apply would make `v8s` co-owner of every field, so removals of fields owned by `helm` or `kustomize-controller` would be silently kept, and later prunes by Flux or Helm 4 (both SSA) would be blocked by `v8s` ownership.
- **Ownership warnings come from `managedFields`.** During review the agent compares the changed paths with `managedFields` and names other managers: "`spec.replicas` is managed by `kustomize-controller`. Flux will revert this change. Suspend the Kustomization first?"
- **Server-side apply** (field manager `v8s`, `force=false`) is used for **create from YAML** and for multi-document applies, where SSA's create-or-update and conflict reporting are the right semantics. Conflicts are surfaced, and force is a second explicit step.
- **Targeted actions** (scale, suspend, cordon, restart) send minimal merge or strategic-merge patches that touch only their field.
- **GitOps awareness.** If the object carries Flux ownership labels (`kustomize.toolkit.fluxcd.io/name`, `helm.toolkit.fluxcd.io/name`) or Helm's `app.kubernetes.io/managed-by: Helm`, the editor shows a banner naming the owner and offers "Open source" (the Kustomization or HelmRelease) as the better place to change it.
- **Optimistic concurrency.** Edits carry the `resourceVersion` the user opened. On conflict the editor shows a three-way diff (opened, live, yours).
- **Create** from YAML (paste or file), from a small form for common kinds (Namespace, ConfigMap, Secret, Deployment, Service, Ingress, CronJob, Job from CronJob), or by duplicating an existing object (server-managed fields stripped).
- **Secrets** are masked by default; reveal is per key and requires `get` on the Secret (lists only need `list`, and the agent watches Secrets **metadata-only**; values are fetched on reveal).
- **Common one-click actions**: scale, restart (patch `kubectl.kubernetes.io/restartedAt`), rollback a Deployment to a previous ReplicaSet, trigger a Job from a CronJob, suspend/resume a CronJob, delete (with propagation policy choice for owners).

## Pod shell (exec)

- **Transport**: the agent uses the Kubernetes exec subresource over **WebSocket** (`v5.channel.k8s.io`), falling back to SPDY on old API servers. The browser never talks to the API server; the agent relays raw bytes over a dedicated WebSocket per session (see [performance.md §8](performance.md#8-logs-shell-and-flow-streaming)).
- **Shell discovery**: try `/bin/bash`, `/bin/sh`, `/busybox/sh`; for distroless containers, offer **ephemeral debug container** (`kubectl debug`-style, `pods/ephemeralcontainers` subresource) with a chosen image targeting the container's process namespace.
- **Terminal**: xterm.js, resize events forwarded, UTF-8 safe, copy-on-select.
- **Session limits**: idle timeout (default 30 min hosted, none desktop), max concurrent sessions per user (hosted), and an explicit "Session ended: pod deleted / container restarted" state. **Streams opened through an elevated profile** (exec, attach, port-forward, log follow) have a 15-minute idle timeout and a 60-minute cap on desktop and hosted alike, because they outlive the gateway's per-request check once established.
- **Mobile**: available but secondary. A phone gets a simplified terminal (DOM renderer, an accessory key row for Esc/Ctrl/Tab/arrows) and a warning that it is best on desktop. See [packaging.md §Mobile](packaging.md#mobile-web).

## Node shell

Kubernetes has no node exec API. v8s does what `kubectl debug node/...` and Lens's node shell do, with tighter controls.

**Mechanism.** Create a short-lived pod pinned to the node:

```yaml
apiVersion: v1
kind: Pod
metadata:
  generateName: v8s-node-shell-
  namespace: v8s-system           # configurable per cluster
  labels: { app.kubernetes.io/managed-by: v8s, v8s.io/correlation: <correlation id> }
  annotations: { v8s.io/user: <user>, v8s.io/expires-at: <ts> }
spec:
  nodeName: <node>
  hostPID: true
  hostNetwork: true
  hostIPC: true
  restartPolicy: Never
  activeDeadlineSeconds: 3600     # hard ceiling even if v8s dies; matches the elevated-stream cap
  terminationGracePeriodSeconds: 0
  tolerations: [{ operator: Exists }]
  priorityClassName: system-node-critical   # optional, configurable
  containers:
    - name: shell
      image: <pinned digest, configurable; default a minimal busybox/alpine>
      command: ["nsenter", "--target", "1", "--mount", "--uts", "--ipc", "--net", "--pid", "--", "sh", "-l"]
      stdin: true
      tty: true
      securityContext: { privileged: true }
```

The container's own command already entered the host namespaces, so the agent **attaches** to its TTY (`pods/attach`). An exec into this container would start a process in the container's namespaces, not the host's; additional concurrent sessions on the same node therefore exec `nsenter --target 1 --mount --uts --ipc --net --pid -- sh -l` explicitly. Node shell needs `create` on `pods` and `pods/attach` (plus `pods/exec` for additional sessions).

**Controls.**

- **Namespace.** `v8s-system` by default, created on first use with `pod-security.kubernetes.io/enforce: privileged`. Clusters that forbid privileged pods anywhere simply cannot use node shell; v8s says so instead of trying to bypass it.
- **Who can.** Node shell requires `create` on `pods` and `pods/attach` in that namespace. Cluster operators gate it with RBAC (a `v8s-node-shell` Role, documented). Hosted mode adds an org-level policy switch, off by default for viewer roles.
- **Cleanup in three layers**, all through the profile that created the pod (usually elevated): the agent deletes the pod when the session ends; `activeDeadlineSeconds` (1 h) kills it if the agent dies or the gateway session expires first; and the next time the user uses that profile on the cluster, the agent deletes any `v8s-node-shell-*` pod whose `expires-at` has passed. Clusters can instead grant the default profile `delete pods` in `v8s-system` (a dedicated namespace, so the grant is narrow), letting cleanup run without elevation.
- **Image.** Pinned by digest and configurable per cluster (air-gapped registries). No `:latest`.
- **Recording.** v8s records nothing; the gateway records the session. Session start/end appear in recent activity.
- **Windows nodes** are out of scope for MVP (no nsenter equivalent).
- **Desktop-primary.** Node shell is hidden in the phone layout. It remains reachable on tablets.

## Cordon, uncordon, drain

- **Cordon/uncordon** patch `spec.unschedulable`. Bulk over a selection, cross-cluster allowed with type-to-confirm.
- **Drain** follows `kubectl drain` semantics exactly (the agent embeds the same logic rather than reimplementing it): cordon, then evict via the **Eviction API** so PodDisruptionBudgets are respected; options for ignore-DaemonSets (default on), delete-emptyDir-data, grace period, timeout, pod selector, and disable-eviction (delete instead, behind a second confirm).
- **Pre-flight** shows what will happen before anything is evicted: pods to evict, pods blocked by PDBs (with the PDB and its `disruptionsAllowed`), unmanaged pods that will be lost, emptyDir pods, DaemonSet pods skipped.
- **Progress** streams per pod (evicting, waiting on PDB, terminated) and survives the UI closing: drain is an agent-side job with an ID, and reopening the node shows its state. Cancel stops further evictions and leaves the node cordoned.
- **Crash-safe**: if the agent dies mid-drain, the node stays cordoned (the safe state) and the UI offers "Resume drain". Drain is idempotent to re-run.
- **Multi-node drain** runs nodes sequentially by default, configurable concurrency, stops on first PDB timeout.

## Logs

- **Source**: `pods/log` with `follow`, `timestamps=true`, `sinceSeconds`/`tailLines`, `previous`, per container. Multi-pod logs (all pods of a Deployment/selector) merge by timestamp in the agent.
- **Limits**: initial `tailLines` 1,000 (configurable). The agent forwards lines in batches; the UI holds a bounded ring buffer (default 50k lines desktop, 5k mobile). Older lines fall off with a "N earlier lines dropped, download full log" marker. Download streams straight from the agent to a file without passing through the UI's memory.
- Search runs in a worker over the buffer.
- Details in [performance.md §Streaming](performance.md#8-logs-shell-and-flow-streaming).

## Port-forward

- **Desktop**: binds `127.0.0.1:<port>` locally through the agent (WebSocket port-forward to the pod; Service targets resolve to a ready backing pod and re-resolve if it dies). Saved forwards per cluster, restorable with one click. "Open in browser" for HTTP ports.
- **Desktop "Open in browser"** opens `http://127.0.0.1:<port>`. Neither Electron nor `v8s serve` authenticates with cookies (both present a token in `Sec-WebSocket-Protocol`; see [packaging.md](packaging.md#v8s-serve)), so a forwarded in-cluster page on another loopback port has no ambient credential to ride. The agent also checks `Origin`/`Sec-Fetch-Site` on every HTTP endpoint, not only WebSocket upgrades.
- **Hosted**: a local port is meaningless. Hosted offers **HTTP-only proxied preview** on a **separate registrable domain** (e.g. app on `v8s.example.com`, previews on `<random-label>.v8s-preview.example.net`). A subdomain of the app's domain would be same-site: SameSite cookies would not protect the app from in-cluster content, and that content could set cookies on the parent domain. Each preview gets a random hostname label (never the session id, which would leak via DNS, SNI, and Referer) and a short-lived per-preview token held in a `__Host-` cookie, so one preview cannot plant or overwrite another's token. Installs that expose previews to several users should register the preview domain on the Public Suffix List, making previews cross-site to each other as well. Opt-in per org and off by default, because it exposes in-cluster services through v8s. Raw TCP forwards in hosted mode are a non-goal for MVP.

## Idempotency and long-running jobs

- Every action carries a client-generated UUID; the agent dedupes for 10 minutes, so a retry after a reconnect cannot double-apply (two deletes, two rollbacks).
- Drains, Helm actions, node-shell sessions, and multi-object bulk operations are **agent-owned jobs** with IDs. The UI can close, reconnect, or move to another device (hosted) and reattach to the job's progress.
- Targeted actions (scale, suspend, cordon, restart) use merge or strategic-merge patches, not full PUTs, so they never clobber concurrent changes.

## Recent activity

v8s keeps a short, bounded list of the user's own recent actions (last 200 entries: time, cluster, profile, verb, object, result) so a user can see what they just did and reopen it.

- **Desktop**: in memory for the app session, cleared on quit.
- **Hosted**: in memory per user session, cleared at logout or expiry. No database table, export, or log shipping.
- **Never** included: Secret values, terminal content, tokens.
- The authoritative trail is the gateway's records and the Kubernetes audit log. v8s helps correlate with them by sending a correlation id in its `User-Agent` ([auth.md §Correlation](auth.md#correlation)).

## Hosted-mode security summary

| Risk | Control |
| --- | --- |
| v8s becomes a privilege escalation path | User actions always run as the user (their own token through to the API server or gateway; impersonation only where an admin configures it); no shared admin credential for actions. |
| Impersonation credential stolen | Impersonation SA is scoped (`resourceNames` on `impersonate`, see [auth.md](auth.md)), short-lived bound tokens, stored encrypted, never sent to the browser. |
| Cross-user cache leakage | By default there is no shared cache: every user's data comes from watches carrying their own token. The optional shared cache (impersonation and kiosk clusters only) is filtered per user, fail-closed. See [performance.md §10](performance.md#10-hosted-mode-fan-out-and-sharing). |
| Shell or port-forward abused as a tunnel | Per-org switches; session limits and elevated-stream caps; the gateway's session recording where a gateway is used; node shell and port-forward off by default for viewer roles. |
| CSRF / session theft | `__Host-` cookies (HttpOnly, Secure, SameSite=strict, host-only); `Origin`/`Sec-Fetch-Site` checks on **every** HTTP and WebSocket endpoint; proxied pod content only on a separate registrable domain; absolute session lifetime; re-auth (step-up) for destructive actions in production groups. |
| Token leakage to browser or logs | IdP tokens stay server-side for the session's lifetime only; the browser holds only a v8s session cookie; the `User-Agent` carries an HMAC correlation id, never the session id. |
| Server compromise | Live sessions' tokens are exposed, including any elevated gateway access those users currently hold; mitigated by session-lifetime-only storage, KMS encryption, short sessions, and optional step-up re-auth before elevated actions ([auth.md](auth.md#1-oidc-pass-through-default)). |
