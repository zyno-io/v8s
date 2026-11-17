# Problems

The Problems view answers one question across every selected cluster: what is broken right now?

## What it covers

Problems are grouped into classes, and you can switch classes on or off.

| Area         | Examples                                                                                |
| ------------ | --------------------------------------------------------------------------------------- |
| Workloads    | Crash loops, image pull failures, pending Pods, failed Jobs                             |
| Nodes        | NotReady nodes, pressure conditions, capacity                                           |
| Networking   | Ingresses with no endpoints, dropped network flows                                      |
| Platform     | Warning events, unhealthy certificates                                                  |
| Integrations | Failed Helm releases, unhealthy Flux objects, policy failures, critical vulnerabilities |

The full list and the detection rules are in the [problems engine design](/problems).

## Reading the view

Problems are merged across clusters and grouped by workload, so a Deployment with ten crashing Pods is one entry with a count. Each entry shows its severity, the cluster, and how long it has been failing.

## Triage

Acknowledge a problem to mark it as seen, or snooze it for a while. Filter by severity or class, and use a group preset such as `production` to focus on what matters.

## On a phone

Problems is the landing tab on a phone. It subscribes to a small summary first and only fetches detail when you open an entry, so a check-in stays cheap on battery and data.
