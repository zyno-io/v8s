# Operations and their security model

Everything that changes a cluster or opens an interactive channel into it. All of it runs in the **agent** (see [architecture.md](architecture.md#process-model)), with the user's own Kubernetes identity, so the API server's RBAC and admission are always the final authority. v8s adds guardrails on top; it never grants anything the user's credentials do not.

## Principles

1. **The user's identity, always.** For gateway-fronted clusters, every action carries the user's own gateway token to the target, on desktop and hosted (see [auth.md](auth.md#gateway-fronted-clusters-warpgate)). Otherwise: desktop uses the kubeconfig credential, hosted the forwarded identity. The agent never uses a shared privileged credential to perform a user action.
1. **Elevation is explicit.** Actions the default access profile cannot perform (node shell, drain, cluster-scoped edits on an operator role) offer "Run as `<elevated profile>`" in the confirm dialog. That request goes to the JIT target, which the gateway authorizes against the user's active ticket. Without a ticket, v8s links to the gateway's ticket page.
2. **RBAC-aware UI, not RBAC-enforcing UI.** The agent asks `SelfSubjectRulesReview` (per namespace, cached) and `SelfSubjectAccessReview` (for specific verbs on specific objects) so buttons the user cannot use are disabled with a reason ("You can't `delete` pods in `payments`"). The server still decides; a stale cache produces a clean 403 toast, not a broken state.
3. **Every mutation is previewable.** Edits and creates go through server-side dry run (`dryRun=All`) first, so admission webhooks (Kyverno, Gatekeeper) reject before the user commits.
4. **Destructive actions confirm proportionally.** Single object: confirm dialog. Bulk, cross-cluster, or production-group: type-to-confirm with the count and cluster names spelled out.
5. **Everything mutating is audited** by v8s, in addition to the Kubernetes audit log.

## Editing

- **YAML edit is an Update** (PUT of the edited object with the `resourceVersion` the user opened, field manager `v8s`): `kubectl edit` semantics. Only fields the user actually changed become owned by `v8s`, and deleting a field really deletes it. Applying the whole edited object with server-side apply would make `v8s` co-owner of every field, so removals of fields owned by `helm` or `kustomize-controller` would be silently kept, and later prunes by Flux or Helm 4 (both SSA) would be blocked by `v8s` ownership.
- **Ownership warnings come from `managedFields`.** During review the agent compares the changed paths with `managedFields` and names other managers: "`spec.replicas` is managed by `kustomize-controller`. Flux will revert this change. Suspend the Kustomization first?"
- **Server-side apply** (field manager `v8s`, `force=false`) is used for **create from YAML** and for multi-document applies, where SSA's create-or-update and conflict reporting are the right semantics. Conflicts are surfaced, and force is a second explicit step.
- **Targeted actions** (scale, suspend, cordon, restart) send minimal merge or strategic-merge patches that touch only their field.
- **GitOps awareness.** If the object carries Flux ownership labels (`kustomize.toolkit.fluxcd.io/name`, `helm.toolkit.fluxcd.io/name`) or Helm's `app.kubernetes.io/managed-by: Helm`, the editor shows a banner naming the owner and offers "Open source" (the Kustomization or HelmRelease) as the better place to change it.
- **Optimistic concurrency.** Edits carry the `resourceVersion` the user opened. On conflict the editor shows a three-way diff (opened, live, yours).
- **Create** from YAML (paste or file), from a small form for common kinds (Namespace, ConfigMap, Secret, Deployment, Service, Ingress, CronJob, Job from CronJob), or by duplicating an existing object (server-managed fields stripped).
- **Secrets** are masked by default; reveal is per key, audited, and requires `get` on the Secret (lists only need `list`, and the agent watches Secrets **metadata-only**; values are fetched on reveal).
- **Common one-click actions**: scale, restart (patch `kubectl.kubernetes.io/restartedAt`), rollback a Deployment to a previous ReplicaSet, trigger a Job from a CronJob, suspend/resume a CronJob, delete (with propagation policy choice for owners).

## Pod shell (exec)

- **Transport**: the agent uses the Kubernetes exec subresource over **WebSocket** (`v5.channel.k8s.io`), falling back to SPDY on old API servers. The browser never talks to the API server; the agent relays raw bytes over a dedicated WebSocket per session (see [performance.md §8](performance.md#8-logs-shell-and-flow-streaming)).
- **Shell discovery**: try `/bin/bash`, `/bin/sh`, `/busybox/sh`; for distroless containers, offer **ephemeral debug container** (`kubectl debug`-style, `pods/ephemeralcontainers` subresource) with a chosen image targeting the container's process namespace.
- **Terminal**: xterm.js, resize events forwarded, UTF-8 safe, copy-on-select.
- **Session limits**: idle timeout (default 30 min hosted, none desktop), max concurrent sessions per user (hosted), and an explicit "Session ended: pod deleted / container restarted" state.
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
  labels: { app.kubernetes.io/managed-by: v8s, v8s.dev/session: <session-id> }
  annotations: { v8s.dev/user: <user>, v8s.dev/expires-at: <ts> }
spec:
  nodeName: <node>
  hostPID: true
  hostNetwork: true
  hostIPC: true
  restartPolicy: Never
  activeDeadlineSeconds: 14400    # hard ceiling (4 h, configurable) even if v8s dies
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
- **Cleanup in three layers.** The agent deletes the pod when the session ends; `activeDeadlineSeconds` kills it if the agent dies; on connect the agent garbage-collects any `v8s-node-shell-*` pod whose `expires-at` has passed.
- **Image.** Pinned by digest and configurable per cluster (air-gapped registries). No `:latest`.
- **Audit.** Session start/end, node, user, and (opt-in, hosted) a typescript-style recording of the session.
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
- **Hosted**: a local port is meaningless. Hosted offers **HTTP-only proxied preview** on a **separate registrable domain** (e.g. app on `v8s.example.com`, previews on `<random-label>.v8s-preview.example.net`). A subdomain of the app's domain would be same-site: SameSite cookies would not protect the app from in-cluster content, and that content could set cookies on the parent domain. Each preview gets a random hostname label (never the session id, which would leak via DNS, SNI, and Referer) and a short-lived per-preview token held in a `__Host-` cookie, so one preview cannot plant or overwrite another's token. For the hosted SaaS the preview domain is registered on the Public Suffix List, making previews cross-site to each other as well. Opt-in per org and off by default, because it exposes in-cluster services through v8s. Raw TCP forwards in hosted mode are a non-goal for MVP.

## Idempotency and long-running jobs

- Every action carries a client-generated UUID; the agent dedupes for 10 minutes, so a retry after a reconnect cannot double-apply (two deletes, two rollbacks).
- Drains, Helm actions, node-shell sessions, and multi-object bulk operations are **agent-owned jobs** with IDs. The UI can close, reconnect, or move to another device (hosted) and reattach to the job's progress.
- Targeted actions (scale, suspend, cordon, restart) use merge or strategic-merge patches, not full PUTs, so they never clobber concurrent changes.

## Audit trail

The agent records every mutating request it makes and every interactive session:

```
{ ts, user, authMode, cluster, verb, gvr, namespace, name, dryRun, result, httpStatus, durationMs,
  sessionId?, bulkOperationId?, reason? }
```

- **Desktop**: local append-only JSONL with rotation, viewable in-app ("Activity").
- **Hosted**: database table, viewable by org admins, exportable, and optionally shipped to a webhook/syslog.
- **Never** recorded: request bodies of Secrets, terminal keystrokes (unless the hosted session-recording opt-in is on), tokens.
- An optional **"reason" prompt** for production-group clusters ("Why are you deleting this?") is a per-group setting.

## Hosted-mode security summary

| Risk | Control |
| --- | --- |
| v8s becomes a privilege escalation path | User actions always run as the user (OIDC pass-through or impersonation with the user's own identity); no shared admin credential for actions. |
| Impersonation credential stolen | Impersonation SA is scoped (`resourceNames` on `impersonate`, see [auth.md](auth.md)), short-lived bound tokens, stored encrypted, never sent to the browser. |
| Cross-user cache leakage | The shared cache (tier-0 kinds, no Secrets) is filtered per user at `(cluster, GVR, namespace)` granularity from SubjectAccessReview results, fail-closed; Secrets, full objects, logs, exec, and writes always go to the API server as the user. See [performance.md §10](performance.md#10-hosted-mode-fan-out-and-sharing). |
| Shell or port-forward abused as a tunnel | Per-org switches; session limits; audit; node shell and port-forward off by default for viewer roles. |
| CSRF / session theft | `__Host-` cookies (HttpOnly, Secure, SameSite=strict, host-only); `Origin`/`Sec-Fetch-Site` checks on **every** HTTP and WebSocket endpoint; proxied pod content only on a separate registrable domain; absolute session lifetime; re-auth (step-up) for destructive actions in production groups. |
| Token leakage to browser | Kubernetes credentials and IdP refresh tokens stay server-side; the browser holds only a v8s session cookie. |
