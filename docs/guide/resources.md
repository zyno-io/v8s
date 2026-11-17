# Viewing and changing resources

Every resource type opens as a table, and every row opens a detail drawer. You can edit, create, and act on objects from there. Whatever you do runs with your own credential, and the API server has the final say. For the model behind this, see [Operations and security](/operations).

## View objects

Open a type from the sidebar or the [command bar](/guide/navigation#the-command-bar) (`:deploy`, `:svc`, `:pvc`). Built-in kinds have purpose-built columns and status logic: a Pod's status reason follows `kubectl get pods` (`CrashLoopBackOff`, `OOMKilled`, `Terminating`), a Deployment shows ready, up-to-date, and available counts, and Services show type, ports, and ready endpoints.

Custom resources and everything else get a generic view built from the type's printer columns, with a status tag when the type has a `Ready`, `Status`, or `Phase` column.

### Cluster-scoped and multi-cluster lists

With several clusters selected, a table merges them and adds a cluster column. Sorting, filtering, and counts cover the merged set.

## The detail drawer

Press `Enter` on a row, or click it, to open the drawer. The URL changes with it, so the drawer is linkable. On a phone it is a full page.

| Tab      | What it shows                                                                                         |
| -------- | ----------------------------------------------------------------------------------------------------- |
| Overview | Status, conditions, key fields, owners, and, for managed objects, a link to the Flux or Helm source   |
| YAML     | The live object. Read, copy, or edit it                                                               |
| Events   | Events for this object, newest first                                                                  |
| Related  | Owners, owned objects, and what selects or is selected by this object                                 |
| Logs     | For pods and workloads. See [Operations](/guide/operations#logs)                                      |
| Shell    | For pods. See [Operations](/guide/operations#pod-shell-and-debug-containers)                          |
| Flows    | For pods, workloads, Services, and namespaces with Hubble. See [Network](/guide/network)              |
| Security | Kyverno, Trivy, and Kubescape findings for this workload. See [Security](/guide/security)             |

Tabs that do not apply to a type, or to a cluster without the integration, are not shown. Use the arrow keys to move between tabs.

## Edit YAML

1. Open the YAML tab and choose **Edit YAML**.
2. Change the document.
3. Choose **Review changes**. v8s runs a server-side dry run against the live object (`dryRun=All`), so admission webhooks such as Kyverno or Gatekeeper reject a bad change before you commit it. You see a diff against the live object, plus any warnings or errors from the server.
4. Choose **Apply** once the dry run passes.

Applying is a normal update of the object you opened, like `kubectl edit`. Only fields you changed become yours, and deleting a field really deletes it.

::: tip Large documents
Documents over 1 MB open read-only.
:::

### Who else manages this field

During review, v8s compares the changed paths with the object's `managedFields` and names other managers:

> `spec.replicas` is managed by `kustomize-controller`. Flux will revert this change.

Read that before you apply. A change to a Flux- or Helm-managed field is overwritten at the next reconcile.

### GitOps banner

If the object carries Flux ownership labels (`kustomize.toolkit.fluxcd.io/name`, `helm.toolkit.fluxcd.io/name`) or Helm's `app.kubernetes.io/managed-by: Helm`, the editor shows a banner naming the owner and offers **Open source** to jump to the Kustomization or HelmRelease, which is usually where the change belongs. See [Flux](/guide/flux) and [Helm](/guide/helm).

### Conflicts

Your edit carries the version you opened. If the object changed on the server in the meantime, v8s tells you before you apply: you can reload the live object (discarding your edits) or keep editing and apply, and a conflict is reported rather than silently overwritten. If another process is editing the same fields, you see which.

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

| Type                         | Actions                                                                     |
| ---------------------------- | --------------------------------------------------------------------------- |
| Deployment                   | Scale, Restart, Roll back                                                   |
| StatefulSet                  | Scale, Restart                                                              |
| DaemonSet                    | Restart                                                                     |
| ReplicaSet                   | Scale                                                                       |
| Node                         | Cordon, Uncordon, Drain (see [Operations](/guide/operations#cordon-and-drain)) |
| CronJob                      | Suspend, Resume, Trigger now                                                |
| Flux and Helm objects        | See [Flux](/guide/flux) and [Helm](/guide/helm)                             |
| Any type                     | Delete                                                                      |

- **Scale** asks for the replica count.
- **Restart** patches the pod template's `kubectl.kubernetes.io/restartedAt` annotation, the same thing `kubectl rollout restart` does.
- **Roll back** lists the Deployment's previous revisions and rolls back to the one you choose.
- **Trigger now** creates a Job from the CronJob's template.
- **Suspend** and **Resume** set the CronJob's `spec.suspend`.

These send small targeted patches that touch only their own field, so they do not overwrite concurrent changes.

### Confirmations

Every destructive action asks first. How hard it asks depends on the blast radius:

| Situation                                                   | Confirmation                                      |
| ----------------------------------------------------------- | ------------------------------------------------- |
| One object in a non-production cluster                      | A confirm dialog                                  |
| More than one object, or objects in more than one cluster   | Type the count to confirm                         |
| Any object in a cluster that belongs to a production group  | Type the object name (or the count) to confirm    |

Bulk dialogs list the exact objects and clusters. A selection always resolves to the objects you chose when you press confirm, even if the list moved underneath you. Retrying after a reconnect cannot apply an action twice.

On a phone, destructive actions use hold-to-confirm. See [On a phone](/guide/mobile).

## Secrets

Secret lists never download values: v8s watches Secrets as metadata only, so you see names, types, and ages. Opening a Secret shows its keys with values hidden. **Reveal values** loads the full object, including the base64-encoded values, and requires the `get` permission on that Secret. Values go back to hidden when you leave the object.

Secret values are never written to logs or the recent activity list.

## Custom resources

CRDs are discovered per cluster. They appear under **Custom resources** in the sidebar, grouped by API group, and complete in the command bar (`:certificates`, `:ks`). A CRD's list uses its own printer columns. Flux, Kyverno, Trivy, Kubescape, and Cilium types have dedicated views instead; see their pages. The CustomResourceDefinition list shows group, versions, scope, and a jump to its instances.
