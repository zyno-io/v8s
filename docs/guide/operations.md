# Operations

This page covers the things you do to a running cluster: read logs, open shells, forward ports, and cordon or drain nodes. All of them run as you, through your credential or your linked [write context](/guide/clusters#linked-read-and-write-contexts). v8s adds guardrails; it never grants anything your credential does not.

## Logs

Open a pod's detail drawer and choose the **Logs** tab. The same tab exists on Deployments, StatefulSets, DaemonSets, Jobs, and CronJobs.

| Control    | What it does                                                     |
| ---------- | ---------------------------------------------------------------- |
| Container  | Choose a container, or all pods and containers merged            |
| Follow     | Stream new lines as they arrive. Scrolling up pauses following   |
| Timestamps | Show the line timestamps                                         |
| Wrap       | Wrap long lines                                                  |
| Previous   | Show the previous container instance (useful after a crash loop) |
| Search     | Highlight matches and jump between them, with match case         |
| Download   | Save the full log to a file                                      |

- **Start.** v8s loads the last 1,000 lines by default, then follows. Choose 1,000, 5,000, or 10,000 in **Tail lines**.
- **Merged workload logs.** Open Logs on a Deployment (or another workload, or a selector) to see every pod's output merged by timestamp. Merging happens in the agent, so it stays smooth. Merged logs are best on a desktop screen.
- **Bounded memory.** The view keeps a ring buffer (about 50,000 lines on desktop, 5,000 on a phone). When older lines fall off, the view says "N earlier lines dropped, download full log".
- **Download** streams straight from the cluster to a file; it does not pass through the UI, so a huge log does not slow the app.
- **Flow control.** A very chatty container cannot flood your screen. The agent stops reading from the API server when the UI is behind.
- Lines that parse as structured logs (`level=error`, or JSON with `"level":"error"`) get a coloured gutter.

::: info No long-term logs
v8s reads logs live from the pod. It stores nothing: when a pod is gone, so is its log. For history, use your log pipeline.
:::

## Pod shell and debug containers

Open a pod and choose the **Shell** tab, or press `s` on the pod's row. A shell opens only when you press **Connect**, and **Disconnect** ends it.

- v8s tries `/bin/bash`, then `/bin/sh`, then `/busybox/sh` in the container you selected.
- The terminal is a full terminal: resize, UTF-8, copy on select.
- For **distroless** or shell-less images, v8s offers an **ephemeral debug container**, like `kubectl debug`. Pick an image (for example `busybox` or `nicolaka/netshoot`) and v8s adds it to the pod, targeting the process namespace of the container you are debugging. This needs the `pods/ephemeralcontainers` permission.
- When the pod is deleted or the container restarts, the session ends and says why.

**Limits.** Sessions through a write context have a 15-minute idle timeout and a 60-minute cap, because they outlive a gateway's per-request check once established. Hosted mode also has an idle timeout (30 minutes by default) and a limit on concurrent sessions per user. On a phone the terminal is simplified, with an accessory key row for Esc, Ctrl, Tab, and arrows. It works, but a desktop is better.

Shell needs the `create` permission on `pods/exec`. Your gateway, if you use one, records the session. v8s records nothing.

## Node shell

Kubernetes has no node exec API, so v8s does what `kubectl debug node/...` does: it starts a short-lived privileged pod on the node and attaches to it.

**What it does.**

1. Creates a pod named `v8s-node-shell-<random>` in the `v8s-system` namespace, pinned to the node with `nodeName`. It uses `hostPID`, `hostNetwork`, `hostIPC`, tolerates every taint, runs `privileged`, and has `activeDeadlineSeconds: 3600`.
2. The container's command is `nsenter --target 1 --mount --uts --ipc --net --pid -- sh -l`, so you land in the host's namespaces. v8s attaches to its terminal.
3. When you close the shell, v8s deletes the pod.

**Namespace.** `v8s-system` is the default and is created on first use with the label `pod-security.kubernetes.io/enforce: privileged`. A cluster that forbids privileged pods everywhere cannot use node shell, and v8s says so; it does not try to get around Pod Security.

**Image.** A minimal image pinned by digest, configurable per cluster for air-gapped registries. There is no `:latest`.

**Cleanup, three layers.** v8s deletes the pod when the session ends. If v8s dies or your gateway session ends first, `activeDeadlineSeconds` kills the pod an hour after it started. And the next time you use that context, v8s deletes any expired `v8s-node-shell-*` pods it finds.

### Permissions

Node shell needs to create pods and attach to them in `v8s-system`. Grant that to the people who should have it, and nobody else:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
    name: v8s-node-shell
    namespace: v8s-system
rules:
    - apiGroups: ['']
      resources: ['pods']
      verbs: ['create', 'get', 'list', 'watch', 'delete']
    - apiGroups: ['']
      resources: ['pods/attach', 'pods/exec']
      verbs: ['create']
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
    name: v8s-node-shell
    namespace: v8s-system
subjects:
    - kind: Group
      name: platform-admins
      apiGroup: rbac.authorization.k8s.io
roleRef:
    kind: Role
    name: v8s-node-shell
    apiGroup: rbac.authorization.k8s.io
```

`pods/exec` is only needed for a second concurrent session on the same node. Create `v8s-system` yourself first if you do not want the person using node shell to need `create` on namespaces:

```sh
kubectl create namespace v8s-system
kubectl label namespace v8s-system pod-security.kubernetes.io/enforce=privileged
```

If your read context cannot delete pods, node-shell pods are cleaned up through the write context. You can instead grant the read context `delete` on pods in `v8s-system`, a dedicated namespace, so cleanup works without elevation.

::: warning Privileged by design
A node shell is root on the node. Treat the RBAC above as a break-glass permission. Node shell is hidden on phones, and Windows nodes are not supported.
:::

## Port-forward

Port-forward is available in the desktop app and `v8s serve`. It is not offered in [hosted mode](/guide/hosted), where a local port would be meaningless.

1. Open a Pod or Service and choose **Port forward**.
2. Choose the remote port and a local port (or let v8s pick one).
3. v8s binds `127.0.0.1:<port>`. For HTTP ports, **Open in browser** opens `http://127.0.0.1:<port>`.

A Service target resolves to a ready backing pod and re-resolves if that pod dies. Forwards are saved per cluster, so you can restore them with one click.

Port-forward needs `create` on `pods/portforward`, and, through a write context, follows the same idle and duration limits as shells.

## Cordon and drain

On a Node, choose **Cordon** or **Uncordon** (a patch to `spec.unschedulable`). You can select several nodes, across clusters, and the bulk dialog asks you to type the count.

**Drain** follows `kubectl drain` semantics: cordon, then evict pods with the Eviction API, so PodDisruptionBudgets are respected.

1. Choose **Drain** on a node.
2. Review the **pre-flight**. It shows, before anything is evicted:
    - pods that will be evicted;
    - pods blocked by a PodDisruptionBudget, with the PDB and its allowed disruptions;
    - unmanaged pods that will be lost;
    - pods using `emptyDir`;
    - DaemonSet pods that will be skipped.
3. Set options if needed: ignore DaemonSets (on by default), delete `emptyDir` data, force (for pods without a controller), grace period, timeout, and, behind a second confirm, disable eviction so pods are deleted and PodDisruptionBudgets are not honoured.
4. Confirm.

Progress streams per pod (evicting, waiting on PDB, terminated). A drain is an agent-side **job**, so it survives a reload or a brief disconnect: reopen the node and its state is there. **Cancel** stops further evictions and leaves the node cordoned. If v8s stops mid-drain, the node stays cordoned (the safe state) and the node offers **Resume drain**. Running a drain again is safe.

Draining several nodes runs them one at a time by default and stops at the first PDB timeout.

## The jobs tray

Drains, Helm actions, node-shell sessions, and bulk operations are tracked as jobs. The jobs tray lists them with their state and progress, and reattaches if you reload or reconnect.

A job you started keeps going for up to 15 minutes after your last v8s window closes, so a Helm rollback is not abandoned halfway; then it stops. A stopped drain leaves the node cordoned, and node-shell pods are deleted or expire. Job state is held in memory only, so it is gone after a restart.

## Recent activity

v8s keeps a short list of your own recent actions (the last 200: time, cluster, verb, object, result) so you can see what you just did and reopen it. It lives in memory and is cleared when you quit (desktop) or sign out (hosted). It never contains Secret values, terminal content, or tokens.

For an audit trail, use your gateway's records and the Kubernetes audit log. v8s sends a correlation id in its `User-Agent` (`v8s/<version> (corr <id>)`) to help you match them up.
