# Security: Kyverno, Trivy, Kubescape

v8s has built-in views for policy and scanner results from [Kyverno](https://kyverno.io), [Trivy Operator](https://aquasecurity.github.io/trivy-operator/), and [Kubescape](https://kubescape.io). The **Security** sidebar section appears when at least one selected cluster has them installed. v8s detects each from the cluster's API discovery, so nothing needs configuring.

v8s does not install or scan anything itself. It shows what these tools report, and gives you the few actions they support.

## Kyverno

### Which policies

Kyverno 1.17 deprecated classic `ClusterPolicy` and `Policy`, and from **Kyverno 1.20 onward only CEL policies are executed** (`policies.kyverno.io`: ValidatingPolicy, ImageValidatingPolicy, MutatingPolicy, GeneratingPolicy, DeletingPolicy, their namespaced variants, and PolicyException). v8s shows classic policies read-only, with a hint to migrate to CEL, and offers Audit and Enforce switches for CEL validating policies.

### Policies table

| Column     | Meaning                                                                   |
| ---------- | ------------------------------------------------------------------------- |
| Mode       | `Audit`, `Enforce`, or `Mixed`, worked out per rule                       |
| Read-only  | Yes for classic policies, which Kyverno 1.20 and later no longer executes |
| Ready      | The policy's Ready condition                                              |
| Rules      | Number of rules                                                           |
| Background | Whether background scanning is on                                         |
| Severity   | The policy's severity annotation                                          |
| Message    | The policy's message                                                      |

Open a policy to see its summary (mode, readiness, rules, background scanning, severity, message), a **violations table** with the resource, rule, and message of each failing result, and the policy's YAML (read-only). The Policies page has one tab per policy kind the clusters serve, and a filter box.

### Reports

v8s reads Kyverno reports from both `wgpolicyk8s.io` (PolicyReport, ClusterPolicyReport) and `openreports.io`, and shows them together. Kyverno writes one report per resource, so a big cluster can have tens of thousands. v8s watches only the counts, and fetches a report's detail (which policy, which rule, which message) when you expand a problem or open a resource's **Security** tab. The violations-by-policy view is built as a background job and is best on a desktop.

### Switch Audit and Enforce

Open a policy from **Security, Policies** and use the **Audit** and **Enforce** buttons. The switch applies to the whole policy, and only for CEL `ValidatingPolicy` and `NamespacedValidatingPolicy`. Other kinds have no mode, and classic policies are read-only. The confirm dialog shows the current fail count, so you know what Enforce would start blocking before you commit. It changes `validationActions` (`Deny` for Enforce, `Audit` for Audit) and keeps a `Warn` the policy already had. It needs `patch` on the policy and runs through your linked write context if you have one.

::: warning Enforce blocks admission
Switching to Enforce makes the API server reject non-compliant objects. Check the fail count in the dialog and the violations table first.
:::

### Create an exception

From a violation in the policy's violations table, or from a failing row in a Kyverno report on a workload's Security tab, choose **Exception**. v8s opens the Create dialog with a PolicyException pre-filled with the policy, rule, and resource. Nothing is applied until you review the YAML and create it, because an exception is a security decision.

### "Why was my deploy denied?"

Admission denials from Kyverno (the `PolicyViolation` event reason) are shown from Events. The `policy.admission-denied` problem lists the ones where an enforced policy blocked the request, for the last hour.

### Re-scan

Kyverno has no re-scan API, so v8s does not fake a button.

## Trivy Operator

### Views

- **Vulnerabilities** (Security, Vulnerabilities). Images ranked by severity counts, de-duplicated by `repository:tag` across workloads and clusters, with the workloads, reports, and clusters each image appears in. The same image in 40 pods is one row: "40 workloads". Filter by name, source (Trivy or Kubescape), or critical only, and sort by any severity column.

  **Exposed via** shows how an image's workloads can be reached: **Ingress** (an Ingress, or a Gateway API HTTPRoute or GRPCRoute), **LB** (a LoadBalancer Service), **NodePort**, or **Service**. It covers every workload running the image, in every selected cluster. v8s works this out from what it already watches: the Services whose endpoints point at the workload's pods, their type, and the Ingresses and routes that send traffic to them. A LoadBalancer or Ingress that has no address yet still counts, since it is set up to be exposed. The filter icon in the column header chooses what counts as exposed. By default that is Ingress, LoadBalancer and NodePort; **Service** is off because a plain Service is only reachable inside the cluster, so those images show `-`. Your choice is remembered in this browser. The **Exposed via** filter shows only images with any of the checked kinds, or the ones that are **Not exposed**. While a cluster's Services, endpoints or pods are still loading, or cannot be read with your permissions, its images show `?` (**Unknown**) rather than `-`, and the list refreshes until they resolve. On a phone the card lists the kinds, and **Exposure definition** in the filter bar sets what counts.
- **Open an image** to see the reports for it, one per workload and container. Open a report for its full CVE table: CVE, severity, package, installed version, fixed version, title. It is virtualized, so thousands of rows are fine.
- **Report types.** Config audit, exposed secrets, RBAC assessment, infra assessment, and their cluster-scoped versions, and ClusterComplianceReport, are listed under Security in the sidebar. They open as ordinary tables, and the Report tab of one shows its rows: checks with severity, check ID, title, and resource.
- **Security tab** on a pod or workload (Deployment, StatefulSet, DaemonSet, ReplicaSet, Job, CronJob): its container images with their scan results, then its vulnerability, config audit, exposed secret, and policy reports from every scanner, each with an **Open** button for the details.

Vulnerability reports are the largest objects v8s handles, so it never loads them in full to count: counts come from a lightweight table, and the full report is fetched only when you open it. SBOM reports are not shown in v0.1.0.

### Rescan

The rescan button on a report row deletes the report so Trivy Operator recreates it on its next reconcile. Its tooltip says exactly that ("Delete report to trigger a rescan"). It works on report kinds the operator regenerates, and needs permission to delete reports.

CSV and JSON export of the vulnerability view is not in v0.1.0.

## Kubescape

Kubescape's operator stores results through an aggregated API server (`spdx.softwarecomposition.kubescape.io`), not ordinary CRDs. v8s reads the small summary objects (`VulnerabilityManifestSummary` and `WorkloadConfigurationScanSummary`) and fetches full manifests only on demand. If the aggregated API does not support watch, v8s falls back to re-listing every five minutes.

- **Vulnerabilities and configuration scan** appear in the same Security views as Trivy, with a **source** column, so the two scanners are never silently merged. Configuration scan details show controls with their frameworks (for example NSA, MITRE, CIS).
- **Scan now** (a button in Security, Vulnerabilities when Kubescape is detected) creates a Job from each of the operator's scheduled-scan CronJobs (`kubescape-scheduler`, `kubevuln-scheduler`), the same as `kubectl create job --from=cronjob/...`. It needs `create` on Jobs and reports an error if the operator's CronJobs are not found.

ApplicationProfile and NetworkNeighborhood viewers are not in v0.1.0.

Kubescape's runtime threat-detection alerts are not stored in the Kubernetes API, so v8s does not show them.

## Problem classes these feed

| Class                     | Source           | Default |
| ------------------------- | ---------------- | ------- |
| `policy.fail`             | Kyverno          | On      |
| `policy.error`            | Kyverno          | On      |
| `policy.not-ready`        | Kyverno          | On      |
| `policy.admission-denied` | Kyverno          | On      |
| `vuln.critical`           | Trivy, Kubescape | On      |
| `vuln.high`               | Trivy, Kubescape | Off     |
| `misconfig.critical`      | Trivy, Kubescape | On      |
| `misconfig.high`          | Trivy, Kubescape | Off     |
| `secret.exposed`          | Trivy            | On      |
| `rbac.high`               | Trivy            | Off     |

Policy failures are one problem per resource. Vulnerability problems are one per image, not per CVE, so 300 CVEs in one image is one row. See [Problems](/guide/problems) to turn classes on or off per group. For example, hide the vulnerability classes in `staging`.

## What needs which permission

Everything is read-only unless you use an action. To see a view you need to `list` and `watch` the underlying kind; if you cannot, the view and its problems are skipped and the Problems page says so. Rescan needs `delete` on reports. Scan now needs `create` on Jobs. Creating an exception needs `create` on PolicyException. Switching Audit and Enforce needs `patch` on the policy.
