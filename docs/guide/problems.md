# Problems

The Problems view answers one question across every selected cluster: **what is broken right now?** It is the second most useful page on a desktop and the landing page on a phone. Open it with `:pb`, from the sidebar, or from the problems badge in the top bar.

## What you see

One list, sorted by severity and then by how recently it changed. Each row has a severity tag, the cluster, the problem class, the object (kind, namespace, name), a one-line title such as `CrashLoopBackOff: api (restarts 14)`, and how long it has been going on.

- A problem appears within about two seconds of the change that causes it, and disappears when it resolves.
- **Rollups.** When more than one pod of the same workload has the same problem, v8s shows one row: "Deployment `api`: 3/5 pods CrashLoopBackOff". Expand it for the pods. This is what keeps the view readable in an incident.
- A problem that clears within 30 seconds of being raised is held for that window instead of flapping.
- Time-based checks ("Pending for more than 5 minutes") carry their own thresholds, so a normal rollout does not raise noise.
- Problems exist only while v8s is open. There is no background alerting or push notification.
- Press `:pb` or choose **Problems** in the sidebar. On a desktop the app opens on Pods; on a phone it opens on Problems.

If you cannot list a kind that a check needs, the view says so ("Some checks are unavailable in `prod-eu`: no permission to list `ingresses`") instead of reporting zero problems.

## What it finds

There are 51 problem classes in 12 families. Each can be switched on or off from the Problems page. Severity is in brackets. "Default" is what a fresh install does; classes whose data source is missing (no Flux, no Kyverno, no Hubble) simply raise nothing.

### Pods, workloads, and jobs

| Class                    | Raised when                                                                                    | Default |
| ------------------------ | ---------------------------------------------------------------------------------------------- | ------- |
| `pod.crashloop`          | A container is in `CrashLoopBackOff` [error]                                                   | On      |
| `pod.image-pull`         | `ImagePullBackOff`, `ErrImagePull`, or `InvalidImageName` [error]                              | On      |
| `pod.oomkilled`          | A container was `OOMKilled` within the last hour [warning]                                     | On      |
| `pod.pending`            | Pending for more than 5 minutes, with the scheduling reason [warning]                          | On      |
| `pod.not-ready`          | Running but not Ready for more than 5 minutes [warning]                                        | On      |
| `pod.stuck-terminating`  | Deleting for more than 10 minutes [warning]                                                    | On      |
| `pod.config-error`       | `CreateContainerConfigError`, such as a missing ConfigMap or Secret key [error]                | On      |
| `pod.evicted`            | Pod failed with reason `Evicted` [info]                                                        | Off     |
| `pod.restarting`         | Restarts increased in the last 10 minutes, not yet crash looping [warning]                     | Off     |
| `workload.unavailable`   | Deployment, StatefulSet, or DaemonSet below desired beyond its deadline [error]; rolls up pods | On      |
| `workload.rollout-stuck` | `ProgressDeadlineExceeded` [error]                                                             | On      |
| `hpa.unable`             | HPA cannot scale, or has been at max replicas for 30 minutes [warning]                         | Off     |
| `job.failed`             | A Job failed (backoff limit or deadline) [error]                                               | On      |
| `cronjob.last-failed`    | The last scheduled Job failed [error]                                                          | On      |
| `cronjob.not-scheduling` | Not suspended, but no successful run in twice its interval [warning]                           | On      |

### Nodes and storage

| Class            | Raised when                                      | Default |
| ---------------- | ------------------------------------------------ | ------- |
| `node.not-ready` | Node `Ready` is not True [critical]              | On      |
| `node.pressure`  | Memory, disk, or PID pressure [error]            | On      |
| `node.cordoned`  | Node is unschedulable [info]                     | Off     |
| `pvc.pending`    | PVC Pending for more than 5 minutes [error]      | On      |
| `pv.failed`      | PV `Failed`, or `Released` with Retain [warning] | Off     |

### Networking

| Class                      | Raised when                                                                    | Default |
| -------------------------- | ------------------------------------------------------------------------------ | ------- |
| `ingress.no-backend`       | An Ingress points at a missing Service, or one with no ready endpoints [error] | On      |
| `service.no-endpoints`     | A Service with a selector has had zero ready endpoints for 2 minutes [warning] | On      |
| `gateway.route-unaccepted` | A Gateway API route is `Accepted=False` or `ResolvedRefs=False` [error]        | On      |
| `cert.expiring`            | A cert-manager Certificate is not Ready, or expires within 14 days [warning]   | On      |

`cert.expiring` is on by default but only raises anything where cert-manager is installed.

### Platform

| Class                    | Raised when                                                                                     | Default |
| ------------------------ | ----------------------------------------------------------------------------------------------- | ------- |
| `apiservice.unavailable` | An APIService is `Available=False` [error]                                                      | On      |
| `webhook.failing`        | An admission webhook with `failurePolicy=Fail` has a Service with no ready endpoints [critical] | On      |
| `cluster.unreachable`    | v8s cannot reach the API server, or auth has expired [critical]                                 | On      |
| `events.warning-burst`   | Many Warning events on one object in 10 minutes, not explained by another class [warning]       | Off     |

### Flux and Helm

| Class                     | Raised when                                                             | Default |
| ------------------------- | ----------------------------------------------------------------------- | ------- |
| `flux.not-ready`          | `Ready=False`, excluding suspended and progressing within grace [error] | On      |
| `flux.stalled`            | `Stalled=True` [error]                                                  | On      |
| `flux.stale`              | No successful reconcile in 3 times the interval [warning]               | On      |
| `flux.source-failed`      | A source (Git, OCI, Helm repository) failed [error]                     | On      |
| `flux.controller-down`    | A Flux controller deployment is down [critical]                         | On      |
| `flux.dependency-blocked` | `dependsOn` chains collapsed to their root cause [warning]              | On      |
| `flux.suspended`          | An object is suspended [info]                                           | Off     |
| `helm.failed`             | The latest release revision is `failed` [error]                         | On      |
| `helm.stuck-pending`      | A release has been `pending-*` for more than 15 minutes [warning]       | On      |

### Policy and security

| Class                     | Raised when                                                                          | Default |
| ------------------------- | ------------------------------------------------------------------------------------ | ------- |
| `policy.fail`             | A resource has Kyverno policy failures (one problem per resource) [warning]          | On      |
| `policy.error`            | A resource has Kyverno policy errors [error]                                         | On      |
| `policy.not-ready`        | A Kyverno policy is not Ready [warning]                                              | On      |
| `policy.admission-denied` | An enforced Kyverno policy blocked a request in the last hour, from Events [warning] | On      |
| `vuln.critical`           | An image has critical vulnerabilities, one problem per image, not per CVE [error]    | On      |
| `vuln.high`               | An image has high vulnerabilities [warning]                                          | Off     |
| `misconfig.critical`      | Critical configuration audit failures [error]                                        | On      |
| `misconfig.high`          | High configuration audit failures [warning]                                          | Off     |
| `secret.exposed`          | An exposed secret was found in an image [error]                                      | On      |
| `rbac.high`               | Critical or high RBAC assessment findings [warning]                                  | Off     |

These come from Kyverno, Trivy Operator, and Kubescape. See [Security](/guide/security). The vulnerability classes apply regardless of which scanner reported them; a `source` tag tells you which. Misconfiguration classes come from both scanners, `secret.exposed` and `rbac.high` from Trivy only.

### Network flows

| Class                  | Raised when                                                                           | Default |
| ---------------------- | ------------------------------------------------------------------------------------- | ------- |
| `network.policy-drops` | Sustained policy-denied drops between two workloads (about 10 in 5 minutes) [warning] | Off     |
| `network.l7-errors`    | A service's HTTP 5xx count over 5 minutes is above a threshold (about 20) [warning]   | Off     |
| `network.dns-errors`   | A spike of NXDOMAIN or SERVFAIL responses (about 20 in 5 minutes) [warning]           | Off     |
| `hubble.relay-down`    | Hubble Relay is unreachable [warning]                                                 | On      |

These need Hubble. `hubble.relay-down` is on by default but only raises where a relay exists. See [Network](/guide/network).

## Filter and group

- **Family chips.** One chip per family (Pods, Workloads, Jobs, Nodes, Network, Storage, Flux, Helm, Policy, Vulnerabilities, Network flows, Platform), each with a live count. Click a chip to show only that family. The caret on a chip opens switches for each class in the family (long-press on a phone). A chip whose classes are all off shows `off`. Class switches take effect in the agent, so a phone never receives problems it would hide, and they are saved in your settings. A class that is overridden for some clusters in the settings file is marked "overridden in N clusters".
- **Severity.** All, Error and up, or Critical.
- **Search.** Matches the object name. Press `/` to focus it.
- **Group by.** No grouping, class, cluster, or namespace. Namespace grouping is unavailable above 100 namespaces.
- **Order.** Newest change first (the default), or oldest first.
- **Across clusters.** Merges problems with the same class, kind, namespace, and name across clusters into one row with cluster tags, so "the `ingress-nginx` webhook is failing in 4 clusters" is one row. Needs at least two clusters and at most 2,000 problems; narrow the filters if the box is disabled.
- **Show acknowledged.** Brings acknowledged and snoozed problems back, faded.

### Keyboard

| Key       | Action                                      |
| --------- | ------------------------------------------- |
| `j` / `k` | Move the focus down / up                    |
| `Enter`   | Expand or collapse the focused row or group |
| `a`       | Acknowledge, or restore if acknowledged     |
| `o`       | Open the problem's object in the drawer     |

## Look into one

Expand a row to see the evidence inline: the condition or state that triggered it, container states, the Warning events related to it, related objects, and quick actions that fit the problem. The actions v8s can offer are **Logs**, **Previous logs**, **Describe**, **Restart**, **Roll back**, **Reconcile**, **Suspend**, **Open release**, **Open policy**, and **Open image vulns**. Only those that apply to the problem and that you are allowed to run are shown. Open the object itself in the drawer with the row's open button or `o`.

Vulnerability problems are per image: "nginx:1.25 · 4 critical, 31 high · 12 workloads". Expanding shows the workloads, and **Open image vulns** goes to the full report.

## Acknowledge and snooze

- **Acknowledge** hides a problem for you until it materially changes: its severity rises, or its count grows by 50% (at least one more). A problem that resolves and comes back is a new problem.
- **Snooze** hides it for 1 hour, 1 day, or 1 week.
- **Show acknowledged** brings them back, faded, with a **Restore** button.

Acknowledgements belong to you and persist across restarts (stored in your local settings file on desktop, in your per-user settings file on the server in hosted mode). A problem keeps its identity when GitOps deletes and recreates its object, and an acknowledgement is kept while the problem is not live (an object being recreated, a restart, or a class switched off for a while). It is collected only after the problem has gone unseen for 7 days.

## On a phone

Problems is the landing tab on a phone. It loads a tiny summary first (counts per cluster and class), then a small window of rows, and fetches detail only when you open an entry. Acknowledge and snooze work from the phone. See [On a phone](/guide/mobile).

## How it stays cheap

When a cluster is selected, v8s keeps a fixed set of cheap watches that feed the default-on checks: projected pods and nodes, workload and Job status, Services, EndpointSlices, Ingresses, PVCs, Warning events only, and so on. Security and policy report counts come from server-side tables that carry only the counts. Nothing is watched in full that does not need to be. Detectors run in the agent on only what changed. The design is in [Problems engine](/problems).

## Presets

Save the current set of enabled classes as a preset for a cluster group (the **Presets** menu on the Problems page), apply it with one click, or delete it. Presets are stored in your settings, so production can show everything while staging hides noisier classes.
