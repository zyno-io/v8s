# Problems

The Problems view answers one question across every selected cluster: **what is broken right now?** It is the second most useful page on a desktop and the landing page on a phone. Open it with `:pb`, from the sidebar, or from the problems badge in the top bar.

## What you see

One list, sorted by severity and then by how recently it changed. Each row has a severity tag, the cluster, the problem class, the object (kind, namespace, name), a one-line title such as `CrashLoopBackOff: api (restarts 14)`, and how long it has been going on.

- A problem appears within about two seconds of the change that causes it, and disappears when it resolves.
- **Rollups.** When more than one pod of the same workload has the same problem, v8s shows one row: "Deployment `api`: 3/5 pods CrashLoopBackOff". Expand it for the pods. This is what keeps the view readable in an incident.
- A problem that clears within 30 seconds of being raised is held for that window instead of flapping.
- Time-based checks ("Pending for more than 5 minutes") carry their own thresholds, so a normal rollout does not raise noise.
- Problems exist only while v8s is open. There is no background alerting or push notification.

If you cannot list a kind that a check needs, the view says so ("Some checks are unavailable in `prod-eu`: no permission to list `ingresses`") instead of reporting zero problems.

## What it finds

Classes are grouped into families. Each can be switched on or off. Severity in brackets.

### Workloads and pods

| Class                    | Raised when                                                                              | Default |
| ------------------------ | ---------------------------------------------------------------------------------------- | ------- |
| `pod.crashloop`          | A container is in `CrashLoopBackOff` [error]                                             | On      |
| `pod.image-pull`         | `ImagePullBackOff`, `ErrImagePull`, or `InvalidImageName` [error]                        | On      |
| `pod.oomkilled`          | A container was `OOMKilled` within the last hour [warning]                               | On      |
| `pod.pending`            | Pending for more than 5 minutes, with the scheduling reason [warning]                    | On      |
| `pod.not-ready`          | Running but not Ready for more than 5 minutes [warning]                                  | On      |
| `pod.stuck-terminating`  | Deleting for more than 10 minutes [warning]                                              | On      |
| `pod.config-error`       | `CreateContainerConfigError`, such as a missing ConfigMap or Secret key [error]          | On      |
| `pod.evicted`            | Pod failed with reason `Evicted` [info]                                                  | Off     |
| `pod.restarting`         | Restarts increased in the last 10 minutes, not yet crash looping [warning]               | Off     |
| `workload.unavailable`   | Deployment, StatefulSet, or DaemonSet below desired beyond its deadline [error]; rolls up pods | On |
| `workload.rollout-stuck` | `ProgressDeadlineExceeded` [error]                                                       | On      |
| `job.failed`             | A Job failed (backoff limit or deadline) [error]                                         | On      |
| `cronjob.last-failed`    | The last scheduled Job failed [error]                                                    | On      |
| `cronjob.not-scheduling` | Not suspended, but no successful run in twice its interval [warning]                     | On      |
| `hpa.unable`             | HPA cannot scale, or has been at max replicas for 30 minutes [warning]                   | Off     |

### Nodes and storage

| Class          | Raised when                                                    | Default |
| -------------- | -------------------------------------------------------------- | ------- |
| `node.not-ready` | Node `Ready` is not True [critical]                          | On      |
| `node.pressure`  | Memory, disk, or PID pressure [error]                        | On      |
| `node.cordoned`  | Node is unschedulable [info]                                 | Off     |
| `pvc.pending`    | PVC Pending for more than 5 minutes [error]                  | On      |
| `pv.failed`      | PV `Failed`, or `Released` with Retain [warning]             | Off     |

### Networking

| Class                      | Raised when                                                                                    | Default                          |
| -------------------------- | ---------------------------------------------------------------------------------------------- | -------------------------------- |
| `ingress.no-backend`       | An Ingress points at a missing Service, or one with no ready endpoints [error]                 | On                               |
| `service.no-endpoints`     | A Service with a selector has had zero ready endpoints for 2 minutes [warning]                 | On                               |
| `gateway.route-unaccepted` | A Gateway API route is `Accepted=False` or `ResolvedRefs=False` [error]                        | On                               |
| `cert.expiring`            | A cert-manager Certificate is not Ready, or expires within 14 days [warning]                   | On when cert-manager is present  |

### Platform

| Class                  | Raised when                                                                                  | Default |
| ---------------------- | -------------------------------------------------------------------------------------------- | ------- |
| `apiservice.unavailable` | An APIService is `Available=False` [error]                                                 | On      |
| `webhook.failing`      | An admission webhook with `failurePolicy=Fail` has a Service with no ready endpoints [critical] | On  |
| `events.warning-burst` | Many Warning events on one object not explained by another class [warning]                   | Off     |
| `cluster.unreachable`  | v8s cannot reach the API server, or auth has expired [critical]                              | Always  |

### Flux and Helm

| Class                    | Raised when                                                                      | Default |
| ------------------------ | -------------------------------------------------------------------------------- | ------- |
| `flux.not-ready`         | `Ready=False`, excluding suspended and progressing within grace                  | On      |
| `flux.stalled`           | `Stalled=True`                                                                   | On      |
| `flux.stale`             | No successful reconcile in 3 times the interval                                  | On      |
| `flux.source-failed`     | A source (Git, OCI, Helm repository) failed                                      | On      |
| `flux.controller-down`   | A Flux controller deployment is down                                             | On      |
| `flux.dependency-blocked`| `dependsOn` chains collapsed to their root cause                                 | On      |
| `flux.suspended`         | An object is suspended [info]                                                    | Off     |
| `helm.failed`            | The latest release revision is `failed`                                          | On      |
| `helm.stuck-pending`     | A release has been `pending-*` for more than 15 minutes                          | On      |

### Policy and security

| Class                    | Raised when                                                                                | Default |
| ------------------------ | ------------------------------------------------------------------------------------------ | ------- |
| `policy.fail`            | A resource has Kyverno policy failures (one problem per resource) [warning]                | On      |
| `policy.error`           | A resource has Kyverno policy errors                                                       | On      |
| `policy.not-ready`       | A Kyverno policy is not Ready                                                              | On      |
| `policy.admission-denied`| A Kyverno denial in the last hour, from Events                                             | On      |
| `vuln.critical`          | An image has critical vulnerabilities (one problem per image, not per CVE)                 | On      |
| `vuln.high`              | An image has high vulnerabilities                                                          | Off     |
| `vuln.critical-fixable`  | Critical vulnerabilities with a fix available                                              | Off     |
| `misconfig.critical`     | Critical configuration audit failures                                                      | On      |
| `misconfig.high`         | High configuration audit failures                                                          | Off     |
| `secret.exposed`         | Trivy found an exposed secret in an image                                                  | On      |
| `rbac.high`              | High RBAC assessment findings                                                              | Off     |

These come from Kyverno, Trivy Operator, and Kubescape. See [Security](/guide/security). The same classes apply regardless of which scanner reported them; a `source` tag tells you which.

### Network flows

| Class                    | Raised when                                                                          | Default |
| ------------------------ | ------------------------------------------------------------------------------------ | ------- |
| `network.policy-drops`   | Sustained policy-denied drops between two workloads (about 10 in 5 minutes)          | Off     |
| `network.l7-errors`      | A service's HTTP 5xx rate is above a threshold                                       | Off     |
| `network.dns-errors`     | A spike of NXDOMAIN or SERVFAIL responses                                            | Off     |
| `hubble.relay-down`      | Hubble Relay is unhealthy                                                            | On, where Hubble is detected |

These need Hubble and are enabled per cluster. See [Network](/guide/network).

## Filter and group

- **Class chips.** One chip per family (Pods, Workloads, Jobs, Nodes, Network, Storage, Flux, Helm, Policy, Vulnerabilities, Network flows, Platform), each with a live count. Expand a chip to toggle individual classes. Disabled classes are filtered in the agent, so a phone never receives problems it would hide.
- **Severity.** All, Error and above, or Critical only.
- **Cluster and namespace.** Narrow to a cluster or namespace.
- **Search.** Matches the object name and the title.
- **Group by.** None, class, cluster, namespace, or cluster group. Groups collapse.
- **Sort.** Severity then most recently changed (the default), or by how long the problem has existed.
- **Collapse across clusters.** Optionally merge problems with the same class, kind, namespace, and name across clusters into one row with cluster tags, so "the `ingress-nginx` webhook is failing in 4 clusters" is one row.

## Look into one

Expand a row to see the evidence inline: the condition or state that triggered it, the last five related Warning events, and quick actions that fit the problem, such as **Logs**, **Describe**, **Restart**, **Reconcile**, or **Roll back**. Click the object to open it in the drawer.

Vulnerability problems are per image: "nginx:1.25 · 4 critical, 31 high · 12 workloads". Expanding shows the workloads and links to the full report.

## Acknowledge and snooze

- **Acknowledge** hides a problem for you until it materially changes: severity rises, its count grows past a threshold, or it resolves and comes back.
- **Snooze** hides it for an hour, a day, or a week.
- **Show acknowledged** brings them back, faded, with a **Restore** button.

Acknowledgements belong to you and persist across restarts (stored locally on desktop, on the server in hosted mode). A problem keeps its identity when GitOps deletes and recreates its object, so an acknowledgement survives that too.

## Presets per group

You can save which classes are on for each [cluster group](/guide/clusters#groups). For example, `production` shows everything, while `staging` hides `pod.pending` and the vulnerability classes. Choosing a group applies its preset.

## On a phone

Problems is the landing tab on a phone. It loads a tiny summary first (counts per cluster and class), then a small window of rows, and fetches detail only when you open an entry. Acknowledge and snooze work from the phone. See [On a phone](/guide/mobile).

## How it stays cheap

When a cluster is selected, v8s keeps a fixed set of cheap watches that feed the default-on checks: projected pods and nodes, workload and Job status, Services, EndpointSlices, Ingresses, PVCs, Warning events only, and so on. Security and policy report counts come from server-side tables that carry only the counts. Nothing is watched in full that does not need to be. Detectors run in the agent on only what changed. The design is in [Problems engine](/problems).
