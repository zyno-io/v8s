# Viewing and changing resources

Every resource type opens as a table, and every row opens a detail drawer. You can edit, create, and act on objects from there. Whatever you do runs with your own credential, and the API server has the final say. For the model behind this, see [Operations and security](/operations).

## View objects

Open a type from the sidebar or the [command bar](/guide/navigation#the-command-bar) (`:deploy`, `:svc`, `:pvc`). Built-in kinds have purpose-built columns and status logic: a Pod's status reason follows `kubectl get pods` (`CrashLoopBackOff`, `OOMKilled`, `Terminating`), a Deployment shows ready, up-to-date, and available counts, and Services show type, ports, and ready endpoints.

Custom resources and everything else get a generic view built from the type's printer columns, with a status tag when the type has a `Ready`, `Status`, or `Phase` column.

### Cluster-scoped and multi-cluster lists

With several clusters selected, a table merges them and adds a cluster column. Sorting, filtering, and counts cover the merged set.

### CPU and memory

Pod tables show CPU and memory as one dense block, in the style of k9s: right-aligned integers with the unit in the header, so a row stays readable at a glance. By default it holds **CPU** (millicores), **%R** and **%L** (usage as a percentage of the pod's request and limit), then **MEM** (MiB), **%R** and **%L**. Choose more from **Columns**: the requests and limits themselves, and usage as a percentage of the pod's node.

- `—` means there is no data: metrics-server does not report the pod (Pending, Completed, just started), or the numbers have not arrived yet. `n/a` means there is nothing to divide by: the pod has no request or no limit. A pod only has a limit when every container has one, as in `kubectl`.
- Percentages of a limit turn amber from 90 % and red from 100 %; percentages of a node turn amber from 90 %. Requests are never coloured: running above a request is normal for burstable pods.
- Sort by any of them from its header. A value sorts before `n/a`, which sorts before `—`, in both directions, so the busiest pods are on top whichever way you sort.
- Hover the block for the full picture: used, requested and limit with percentages, and the share of the node's allocatable, in cores or GiB from a core or a GiB up.
- Requests and limits come from the pod spec, as in `kubectl`, not from an in-place resize status.
- On a phone, each card gets a `cpu 16m · mem 163Mi` line.

**Nodes** use the same block: **CPU** (millicores), **%** (usage as a percentage of the node's allocatable) and **alloc** (the allocatable CPU), then the same for **MEM** (MiB). Allocatable is what pods can actually get (capacity minus reserved), so a node near 100 % is full for the scheduler. Percentages turn amber from 90 %. OS image, kernel, runtime and taints are hidden by default; turn them on from **Columns**. The allocatable columns are numbers now, so they sort numerically.

The **Overview** shows, per cluster, CPU and Memory: the big number is usage as a percentage of the allocatable of all nodes (amber from 90 %, red from 100 %), the bar is allocatable with usage filled in and a tick at the requests, then `requests 62% · limits 143%` and `41.2 of 112 cores`. Without metrics-server the big number is the requested share, labelled "requested", with a note that usage needs metrics-server. Limits above 100 % mean the cluster is overcommitted on limits. Hover for how many nodes report and how many pods have no limit.

Usage needs [metrics-server](https://github.com/kubernetes-sigs/metrics-server) in the cluster, and permission to read `metrics.k8s.io`. v8s asks for it only while a table shows usage (or sorts by it) or an Overview card is open, every 30 seconds, for the namespaces on screen (nodes and the Overview ask for one small node list); a tab hidden for 30 seconds stops asking. Without metrics-server the usage columns are not shown, and **Columns** says why; a cluster where it is missing or refusing shows a small "no metrics" tag next to the sync tag.

Columns you hide or show are remembered per resource type. Resizing a column no longer reveals the columns that are hidden by default (the IP column, the requests and limits).

## The detail drawer

Press `Enter` on a row, or click it, to open the drawer. The URL changes with it, so the drawer is linkable. On a phone it is a full page.

| Tab       | What it shows                                                                                         |
| --------- | ----------------------------------------------------------------------------------------------------- |
| Overview  | Status, conditions, key fields, owners, and, for managed objects, a link to the Flux or Helm source   |
| Logs      | For pods and workloads. See [Operations](/guide/operations#logs)                                      |
| Shell     | For pods. See [Operations](/guide/operations#pod-shell-and-debug-containers)                          |
| Inventory | For Flux Kustomizations: the objects they manage. See [Flux](/guide/flux)                             |
| Release   | For Flux HelmReleases: the Helm release underneath. See [Flux](/guide/flux)                           |
| Report    | For Trivy, Kyverno, and other scanner report objects: the report's rows (CVEs, checks, results)       |
| Security  | Kyverno, Trivy, and Kubescape findings for this workload. See [Security](/guide/security)             |
| Flows     | For pods, workloads, Services, and namespaces, when Hubble is detected. See [Network](/guide/network) |
| YAML      | The live object. Read, copy, or edit it                                                               |
| Events    | All events for this object, newest first                                                              |
| Related   | What owns this object and what it owns                                                                |

The Events tab shows every event for the object, Normal and Warning, newest first, with the component that reported each one. It loads when you open the tab and stays live while it is open. Events of cluster-scoped objects (nodes, volumes, namespaces) come from the `default` namespace. Kubernetes keeps events for about an hour, so older ones are gone, and an object recreated with the same name shows the earlier one's events until then. Event objects themselves have no Events tab.

Tabs appear in that order, and only those that apply to the type and, for Security and Flows, to the clusters you selected. Use the arrow keys, `Home`, and `End` to move between tabs. `l`, `s`, `e`, and `d` jump to Logs, Shell, YAML (in edit mode), and Overview from a table. See [Navigation](/guide/navigation#keyboard-shortcuts).

## Edit YAML

1. Open the YAML tab and choose **Edit**, or press `e` on a row.
2. Change the document.
3. Choose **Review changes**. v8s runs a server-side dry run against the live object (`dryRun=All`), so admission webhooks such as Kyverno or Gatekeeper reject a bad change before you commit it. You see a diff against the live object, plus any warnings or errors from the server.
4. Choose **Apply** once the dry run passes.

Applying is a normal update of the object you opened, like `kubectl edit`. Only fields you changed become yours, and deleting a field really deletes it.

::: tip Large documents
Documents over 1 MB open read-only, and so do objects deleted on the server.
:::

### Who else manages this field

During review, v8s compares the changed paths with the object's `managedFields` and names other managers:

> `spec.replicas` is managed by `kustomize-controller`. Flux will revert this change.

Read that before you apply. A change to a Flux- or Helm-managed field is overwritten at the next reconcile.

### GitOps banner

If the object carries Flux ownership labels (`kustomize.toolkit.fluxcd.io/name`, `helm.toolkit.fluxcd.io/name`) or Helm's `app.kubernetes.io/managed-by: Helm`, the review step shows a banner naming the owner ("may revert this change. Change it at the source, or suspend it first"). The Overview tab of a managed object has an **Open source** link to the Kustomization or HelmRelease, which is usually where the change belongs. See [Flux](/guide/flux) and [Helm](/guide/helm).

### Conflicts

Your edit carries the version you opened. If the object changed on the server in the meantime, v8s tells you: while you edit, you can **Reload (discard my edits)** or keep editing. If you apply and the server reports a conflict, v8s shows what changed on the server since you opened the object and your changes against the live object, and offers **Discard my edits and reload** or **Overwrite with mine**. Nothing is overwritten without that second choice.

### Server rejected it

A dry run that fails shows the server's message in place (invalid field, admission denial, quota, forbidden). Nothing was changed. Fix it and review again.

## Create objects

Choose **Create** on any list page.

- **From a template.** Pick Namespace, ConfigMap, Secret, Deployment, Service, Ingress, CronJob, or Job. v8s fills a minimal manifest for the current namespace.
- **From YAML.** Paste your own manifest in the editor. Multi-document YAML is supported.
- **Dry run first.** **Dry run** checks the manifest against the cluster without creating anything. **Create** applies it.

Creation uses server-side apply with `force=false`, so a conflict with another field manager is reported, not forced over.

## Actions

Actions appear on a row's action bar and in the bulk bar for a selection. They are disabled, with a reason, when your RBAC does not allow them. If your cluster has a [linked write context](/guide/clusters#linked-read-and-write-contexts), the tooltip says the action will run through it.

| Type                  | Actions                                                                        |
| --------------------- | ------------------------------------------------------------------------------ |
| Deployment            | Scale, Restart, Roll back                                                      |
| StatefulSet           | Scale, Restart                                                                 |
| DaemonSet             | Restart                                                                        |
| ReplicaSet            | Scale                                                                          |
| Node                  | Cordon, Uncordon, Drain (see [Operations](/guide/operations#cordon-and-drain)) |
| CronJob               | Suspend, Resume, Trigger now                                                   |
| Flux and Helm objects | See [Flux](/guide/flux) and [Helm](/guide/helm)                                |
| Any type              | Delete                                                                         |

- **Scale** asks for the replica count.
- **Restart** patches the pod template's `kubectl.kubernetes.io/restartedAt` annotation, the same thing `kubectl rollout restart` does.
- **Roll back** lists the Deployment's previous revisions and rolls back to the one you choose.
- **Trigger now** creates a Job from the CronJob's template.
- **Suspend** and **Resume** set the CronJob's `spec.suspend`.

These send small targeted patches that touch only their own field, so they do not overwrite concurrent changes.

### Confirmations

Every destructive action asks first. How hard it asks depends on the blast radius:

| Situation                                                                                             | Confirmation                                      |
| ----------------------------------------------------------------------------------------------------- | ------------------------------------------------- |
| One object in a cluster that is not protected                                                         | A confirm dialog                                  |
| One object, restart, cordon, suspend, resume, trigger, or Flux action, in a protected cluster         | A confirm dialog that names the protected cluster |
| More than one object, or objects in more than one cluster                                             | Type the count to confirm                         |
| One object, delete, scale, or roll back, in a [protected cluster](/guide/clusters#protected-clusters) | Type the object name to confirm                   |

Bulk dialogs list the exact objects and clusters. A selection always resolves to the objects you chose when you press confirm, even if the list moved underneath you. Retrying after a reconnect cannot apply an action twice.

On a phone, destructive actions use hold-to-confirm. See [On a phone](/guide/mobile).

## Secrets

Secret lists never download values: v8s watches Secrets as metadata only, so you see names, types, and ages. Opening a Secret does not fetch it either. The Overview tab says so, and the YAML tab loads the masked manifest on demand, tagged **values masked**.

Revealing is for the whole object, not per key. **Reveal all** fetches the Secret again with every value (base64-encoded, as stored) and tags the pane **values revealed**. It needs the `get` permission on that Secret. **Mask again** drops the values, and they are also dropped when you close the drawer, change tab, or open another object. You cannot reveal or mask while an edit is open.

Secret values are never written to logs or the recent activity list.

## Custom resources

CRDs are discovered per cluster. They appear under **Custom resources** in the sidebar, grouped by API group, and complete in the command bar (`:certificates`, `:ks`). A CRD's list uses its own printer columns. Flux, Kyverno, Trivy, Kubescape, and Cilium types have dedicated views instead; see their pages. The CustomResourceDefinition list shows group, versions, scope, and a jump to its instances.
