# Clusters

v8s works on a selection of clusters, not one at a time. Every list, search, and the [Problems view](/guide/problems) span whatever you have selected. v8s never connects to a cluster you have not selected.

## Where clusters come from

On the desktop and with `v8s serve`, clusters are the contexts in your kubeconfig. v8s loads them the way `kubectl` does:

- `$KUBECONFIG` (several paths are merged), otherwise `~/.kube/config`.
- `--kubeconfig <path>` on `v8s serve` or `v8s agent` to use one specific file.
- New contexts show up without a restart.

In [hosted mode](/guide/hosted), clusters come from the server's configuration, filtered to the ones your identity-provider groups allow.

### Credentials and exec plugins

Each context authenticates the way its kubeconfig entry says: client certificates, bearer tokens, token files, or an exec credential plugin. Plugins that work with `kubectl` work here, including `kubelogin` (OIDC), `aws eks get-token`, `gke-gcloud-auth-plugin`, Azure `kubelogin`, Teleport `tsh`, and `pinniped`.

- **Environment.** Plugins run with the environment from your login shell, so `aws`, `gke-gcloud-auth-plugin`, and `kubelogin` are found even when you launch the app from the macOS Dock or Finder. See [Troubleshooting](/guide/troubleshooting#exec-plugin-not-found) if a plugin is still not found.
- **Browser sign-in.** A plugin that opens a browser (for example `kubelogin` with OIDC) shows "waiting for sign-in" on that cluster only. Other clusters keep working. When many contexts share one identity provider client, v8s runs the first sign-in once and the others reuse it, so you get one browser tab, not forty.
- **Plugins that prompt in the terminal** (typing an MFA code) cannot be answered from v8s. Run the plugin once in a terminal to warm its cache, then connect.
- **Proxies.** `proxy-url` in a kubeconfig, and `HTTPS_PROXY` / `NO_PROXY`, are honored.

## Choose clusters

Open the cluster selector in the top bar (or type `:ctx`).

1. Tick the clusters you want, or click a group chip to select that group. Shift-click a chip to add it to the current ticks.
2. Press **Connect**.

The ticked clusters are listed under **Selected** below the list, whatever you have searched for or scrolled to. Click one there to untick it.

Ticking boxes connects nothing until you press Connect. A mistaken "select all" on 40 clusters costs nothing. On launch, v8s shows the selection you last connected to pre-ticked with a **Connect** button; it does not reconnect until you press it. Large selections connect in batches, so a stampede of exec plugins and API requests does not happen.

Deselecting a cluster stops its watches after a short grace period (ten seconds, so an accidental click is free) and frees the memory the agent held for it. Selecting it again is a cold start, which is cheap.

You can also change the selection from the keyboard. See [the command bar](/guide/navigation#the-command-bar):

```text
:ctx prod-us staging     connect exactly these two
:ctx +edge               add one
:ctx -staging            remove one
:ctx production          a group name selects all its clusters
```

### What the browser remembers

The last selection is remembered per browser (in its local storage), so it is pre-ticked even after the agent restarts. When the selection is exactly one group, v8s remembers the group rather than its clusters: if that group gains or loses members later, the new membership is pre-ticked. Clusters that no longer exist are dropped. If the browser has nothing saved, v8s falls back to the selection the agent reports.

**One cluster configured?** If only one cluster is listed (contexts hidden as the write side of a [linked pair](#linked-read-and-write-contexts) don't count), v8s connects to it at startup and does not show the selector. With any other number of clusters, nothing connects until you press Connect.

## Groups

A group is a named set of clusters, such as `production`, `staging`, or `eu`.

- Create one from the selector: tick clusters, choose **+ New group**, and name it.
- Click a group chip to select exactly that group. Shift-click to add it.
- **Manage** lets you rename a group, change its members to the current ticks, or delete it.
- In [hosted mode](/guide/hosted), the server can put clusters in groups too (`displayGroups`). Those chips have a dashed border: they select clusters like any group, but you can't rename, change, or delete them.

On a phone, group chips are the fast way to pick clusters. See [On a phone](/guide/mobile).

## Protected clusters

Click the lock at the end of a cluster's row in the selector to **protect** it. A protected cluster shows a small lock beside its name in the selector, tables, the detail drawer, Problems and search.

On a protected cluster, these actions require typing to confirm, even for a single object (the object name, or the count for several): delete, scale, roll back, drain (the node name), Helm release actions (the release name, plus the namespace for uninstall), Kyverno policy mode changes (the policy name), Trivy report rescans (the report name), Kubescape scans (the cluster name), and the Problems quick actions. Restart, cordon, uncordon, suspend, resume, trigger, and the Flux actions on a single object show a confirm dialog that names the protected cluster, without typing. Bulk and cross-cluster actions always type the count, protected or not.

Click the lock again to unprotect the cluster. The flag belongs to the cluster, not to a group, and is saved with your settings. For a [linked read/write pair](#linked-read-and-write-contexts), protect the read cluster (the one the selector lists); actions that run through its write context are protected too.

## Connection states

Each cluster shows its state next to its name in the selector and in the toolbar of multi-cluster views.

| State            | What it means                                                                                                                         | What to do                                                                                 |
| ---------------- | ------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------ |
| not connected    | Not selected, or selected but not yet connected.                                                                                      | Press Connect.                                                                             |
| connecting       | v8s is loading the context and running its credential plugin.                                                                         | Wait. A browser sign-in may be open.                                                       |
| sign-in required | The credential is missing or expired.                                                                                                 | Sign in again (run the plugin or `kubectl` once), then reconnect.                          |
| syncing          | Connected; the first rows are arriving. The count shows rows loaded so far. Rows appear as they load.                                 | Nothing. You can use the view while it syncs.                                              |
| ready            | Live. Updates arrive within a fraction of a second.                                                                                   |                                                                                            |
| degraded         | Connected, but some resource types are forbidden or failing. The message says which, for example "no permission to list `ingresses`". | Ask for the RBAC, or ignore the affected checks.                                           |
| reconnecting     | The connection dropped. Last-known rows stay visible but greyed, marked "stale since" a time.                                         | Wait. v8s retries with backoff.                                                            |
| unreachable      | The API server cannot be reached.                                                                                                     | Check VPN, network, and the server address. See [Troubleshooting](/guide/troubleshooting). |

A cluster that fails does not block the others. Its rows leave the merged views until it recovers, and an unreachable or expired cluster is itself reported as a critical problem in the [Problems view](/guide/problems).

## Linked read and write contexts

Many setups give you two contexts per cluster: one that can only read, and one that can also write. With [Warpgate](https://github.com/warp-tech/warpgate) these are two targets, for example `prod-eu-operator` (read-only) and `prod-eu-cluster-admin` (writes, during a time-boxed elevated session). v8s can **link** them so they behave as one cluster:

- Reads, watches, and the Problems view use the read context.
- When an action needs more than the read context can do, v8s runs it through the write context. The confirm dialog names the context it will use, and the cluster wears a small `write` tag while it is in use.
- Elevation, time limits, and approvals belong to your gateway. v8s adds none.

### Link two contexts

1. Open the cluster selector and choose **Linked contexts…** in its footer. The button shows how many links you have.
2. Under **Link a pair…**, pick the **Read context** and the **Write context**, then press **Link**.
3. The write context no longer appears as its own cluster.

If context names differ only by a role suffix (`-operator` and `-cluster-admin`), the dialog lists them under **Suggestions**, and the selector shows a note with a **Review** button. Press **Link** on a suggestion, **Link all** to accept every suggestion at once, or **Dismiss** to hide one. v8s never links by itself.

Use **Unlink** next to a pair in the same dialog to undo it. Unlinked contexts are ordinary, separate clusters.

### Automatic switch

By default, writes switch to the linked write context automatically. If you untick **Switch to the linked write context automatically when the read context is denied** in the **Linked contexts** dialog, v8s asks first each time ("This will run as the write context `prod-eu-cluster-admin`") and proceeds only if you confirm.

Before the first write through a link, v8s checks that both contexts point at the same cluster (it compares the `kube-system` namespace UID). If they do not match, nothing is written and v8s tells you the link looks wrong.

### Elevate a cluster

To run a whole cluster as its write context, not just single actions, click its `synced` chip above a table and pick **Elevate to `<write context>`**, or press **Elevate to `<write context>`** on its card in the Overview. Until you **Revert**, that one cluster's reads, watches, discovery, Problems and usage all run as the write context, so you see what that identity sees (for example Secrets the read context cannot list). Other selected clusters are not touched. While a cluster is elevated, its chip turns amber, everything loaded from it (table rows, the object drawer, Problems, dialogs) is labelled with the write context's name, the Overview card shows an `elevated` tag, and its actions run as the write context without asking for confirmation ("Runs as `<write context>` (elevated)").

- **Progress.** While the switch runs, the cluster's chip shows a spinner and `elevating…` or `reverting…`. The first elevation in a session first checks that the write context reaches the same cluster, which can take a second or two through a gateway. Lists you have open reload as the new identity (each shows `loading …` on the cluster's chip until its rows arrive), including one the read context was not allowed to read (Secrets, for example).
- **Cost.** Elevating and reverting each restart that cluster's data cold, like reselecting it after the grace period. Expect the cluster to show `syncing` again.
- **Lifetime.** Elevation is kept in memory only. It ends when you revert, when the cluster stays deselected past the grace period, when you unlink the write context, or when v8s restarts.
- **Streams.** Exec, shell and port-forward streams opened while elevated keep the write-context limits (closed after 15 minutes idle, and after 60 minutes at most). A stream that is already open keeps the context it started with; reopen it to switch.
- **Gateways.** If the write context's session is not active, the cluster shows `sign-in required ... (is its elevated session active?)`: renew the session or Revert.

### "Write context rejected"

This means the cluster's write context refused the request. With a gateway, it almost always means **your elevated session is not active**: start or renew it in the gateway (Warpgate), then retry. Reads keep working in the meantime. Other rejections through the write context (admission webhooks, quota, validation) keep their own message, so read it. See [Troubleshooting](/guide/troubleshooting#write-context-rejected).

## RBAC-aware actions

v8s asks the API server what you are allowed to do, as the endpoint sees you, and caches the answer for a minute. Buttons you cannot use are disabled with the reason ("You can't `delete` pods in `payments`"). If the cache is stale, you get a clean error toast, not a broken state. The API server remains the authority on every request.

If you can only access some namespaces, v8s falls back to watching those namespaces: the kubeconfig context's namespace plus a per-cluster namespace list you can extend as you navigate. In that mode it watches the core workload kinds in at most five namespaces by default, and says which namespaces it is showing.

## Credentials, in one paragraph

Every request carries your own credential to whatever endpoint the context points at: the API server, or a gateway that authorizes and records you and forwards on. v8s has no gateway-specific mode; a gateway is just a Kubernetes endpoint with OIDC. Details are in [Authentication](/auth).
