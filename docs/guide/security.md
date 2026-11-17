# Security: Kyverno, Trivy, Kubescape

v8s has built-in views for policy and scanner results from [Kyverno](https://kyverno.io), [Trivy Operator](https://aquasecurity.github.io/trivy-operator/), and [Kubescape](https://kubescape.io). The **Security** sidebar section appears when at least one selected cluster has them installed. v8s detects each from the cluster's API discovery, so nothing needs configuring.

v8s does not install or scan anything itself. It shows what these tools report, and gives you the few actions they support.

## Kyverno

### Which policies

Kyverno 1.17 deprecated classic `ClusterPolicy` and `Policy`, and from **Kyverno 1.20 onward only CEL policies are executed** (`policies.kyverno.io`: ValidatingPolicy, ImageValidatingPolicy, MutatingPolicy, GeneratingPolicy, DeletingPolicy, their namespaced variants, and PolicyException). v8s shows classic policies read-only, with a hint to migrate to CEL, and offers editing and mode switches for CEL policies.

### Policies table

| Column         | Meaning                                                                          |
| -------------- | -------------------------------------------------------------------------------- |
| Kind, scope    | Policy kind and whether it is cluster-wide or namespaced                         |
| Mode           | `Audit`, `Enforce`, or `Mixed`, worked out per rule                              |
| Background     | Whether background scanning is on                                                |
| Ready          | The policy's Ready condition                                                     |
| Rules          | Number of rules                                                                  |
| Results        | Live pass, fail, warn, error, and skip counts from reports                       |

Open a policy to see its rules, the kinds it matches, the exceptions that apply, and a **violations table** across all selected clusters: resource, cluster, namespace, rule, message, and age.

### Reports

v8s reads Kyverno reports from both `wgpolicyk8s.io` (PolicyReport, ClusterPolicyReport) and `openreports.io`, and shows them together. Kyverno writes one report per resource, so a big cluster can have tens of thousands. v8s watches only the counts, and fetches a report's detail (which policy, which rule, which message) when you expand a problem or open a resource's **Security** tab. The violations-by-policy view is built as a background job and is best on a desktop.

### Switch Audit and Enforce

On a policy, choose **Switch to Enforce** (or **Switch to Audit**). It applies to the whole policy or to a single rule. The confirm dialog shows the current fail count, so you know what Enforce would start blocking before you commit. For CEL policies this changes `validationActions`.

::: warning Enforce blocks admission
Switching to Enforce makes the API server reject non-compliant objects. Check the fail count in the dialog and the violations table first.
:::

### Create an exception

From a violation, choose **Create exception**. v8s opens a PolicyException in the YAML editor, pre-filled with the policy, rule, and resource. It opens for review rather than applying, because an exception is a security decision. It uses `policies.kyverno.io/v1` for CEL policies.

### "Why was my deploy denied?"

Admission denials from Kyverno (the `PolicyViolation` event reason) are shown from Events, and the `policy.admission-denied` problem lists them for the last hour.

### Re-scan

Kyverno has no re-scan API, so v8s does not fake a button. It shows the report age and the background-scan interval instead.

## Trivy Operator

### Views

- **Vulnerabilities.** Images ranked by critical and high counts, de-duplicated by `repository:tag` across workloads and clusters. The same image in 40 pods is one row: "40 workloads".
- **Open an image** to see its full CVE table: CVE, severity, package, installed version, fixed version, title. It is virtualized, so thousands of rows are fine.
- **Config audit**, **exposed secrets**, **RBAC assessment**, and **infra assessment.** Tables of failing checks with severity, check ID, title, and resource.
- **Compliance.** ClusterComplianceReport summaries, pass and fail per control.
- **Security tab** on a workload: its image vulnerabilities, config audit failures, and exposed secrets.

Vulnerability reports are the largest objects v8s handles, so it never loads them in full to count: counts come from a lightweight table, and the full report is fetched only when you open an image. SBOM reports are fetched on demand only, never watched.

### Rescan

**Rescan** deletes the report so Trivy Operator recreates it on its next reconcile. The button says exactly that ("Delete report to trigger a rescan"). It needs permission to delete reports.

### Export

Export the filtered vulnerability view as CSV or JSON. It streams straight from the agent.

## Kubescape

Kubescape's operator stores results through an aggregated API server (`spdx.softwarecomposition.kubescape.io`), not ordinary CRDs. v8s reads the small summary objects and fetches full manifests only on demand. If the aggregated API does not support watch, v8s falls back to polling every five minutes.

- **Vulnerabilities and configuration scan** appear in the same Security views as Trivy, with a **source** column, so the two scanners are never silently merged. Configuration scans show controls with their framework mapping (NSA, MITRE, CIS).
- **ApplicationProfile** and **NetworkNeighborhood** viewers show what a workload actually executed and connected to. With Hubble, the NetworkNeighborhood view links to flows.
- **Run scan now** creates a Job from Kubescape's scheduled-scan CronJob, the same as triggering any CronJob.

Kubescape's runtime threat-detection alerts are not stored in the Kubernetes API, so v8s does not show them.

## Problem classes these feed

| Class                    | Source                          | Default |
| ------------------------ | ------------------------------- | ------- |
| `policy.fail`            | Kyverno                         | On      |
| `policy.error`           | Kyverno                         | On      |
| `policy.not-ready`       | Kyverno                         | On      |
| `policy.admission-denied`| Kyverno                         | On      |
| `vuln.critical`          | Trivy, Kubescape                | On      |
| `vuln.high`              | Trivy, Kubescape                | Off     |
| `vuln.critical-fixable`  | Trivy (needs the CVE index job) | Off     |
| `misconfig.critical`     | Trivy, Kubescape                | On      |
| `misconfig.high`         | Trivy, Kubescape                | Off     |
| `secret.exposed`         | Trivy                           | On      |
| `rbac.high`              | Trivy                           | Off     |

Policy failures are one problem per resource. Vulnerability problems are one per image, not per CVE, so 300 CVEs in one image is one row. See [Problems](/guide/problems) to turn classes on or off per group. For example, hide the vulnerability classes in `staging`.

## What needs which permission

Everything is read-only unless you use an action. To see a view you need to `list` and `watch` the underlying kind; if you cannot, the view and its problems are skipped and the Problems page says so. Rescan needs `delete` on reports. Creating an exception needs `create` on PolicyException. Switching Audit and Enforce needs `patch` on the policy.
