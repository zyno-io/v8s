# Network: Hubble

If you run [Cilium](https://cilium.io) with Hubble, v8s shows live network flows, dropped traffic, and a service map next to your workloads. Hubble is read-only here: you see what the network is doing and jump to the policy responsible, and you change policy through the normal YAML editor.

## Requirements

- **Hubble Relay** running in the cluster. v8s detects a `hubble-relay` Service in any namespace (by default `kube-system`) the first time you look at a network view.
- **`create` on `pods/portforward`** in the relay's namespace. v8s reaches the relay through a Kubernetes port-forward carrying your own credential, so it works the same whether you reach the cluster directly or through a gateway such as Warpgate. If the permission is missing, v8s says so plainly.

There are no direct relay URLs. Hubble Relay has no authorization of its own, so v8s never connects to it except through a port-forward as you.

## Flows tab

On pods, workloads, Services, and namespaces, the **Flows** tab shows a live table of flows: time, source, destination, port and protocol, an L7 summary (`GET /api 503`, `DNS A example.com`), a verdict tag (`FORWARDED`, `DROPPED`), and the drop reason.

Filters map directly to Hubble's flow filters and are applied by the relay, not in your browser: verdict, drop reason, protocol, HTTP status, method and path, DNS query, direction, and labels.

You always need to scope a flow view to at least a namespace or a workload. v8s never opens an unfiltered stream of a whole cluster. A busy cluster can produce thousands of flows per second, so:

- The table shows at most about 200 flows per second. Above that, v8s samples and the toolbar says so ("showing 200/s of ~3,400/s, narrow the filter").
- The view keeps the last 10,000 flows on desktop (2,000 on a phone).
- A flow subscription nobody is looking at is cancelled after ten seconds.

## Service map

For a namespace or workload, the **service map** draws workloads as nodes (grouped by identity, not by pod) with edges for request rate, error rate, and drops over a recent window. Dropped edges are red and dashed, so colour is not the only signal. v8s builds the map from a filtered flow stream and sends the UI only a small graph. On a phone you get lists (drops and dependencies) instead of a graph.

## Drops view

The **Drops** view lists dropped flows across all selected clusters, grouped by source workload, destination workload, and drop reason, with counts and last seen. Each row links to the NetworkPolicy or CiliumNetworkPolicy most likely responsible, with an **Edit policy** shortcut that opens it in the YAML editor.

## Hubble status

Relay health, the nodes it is connected to, and each node's flow buffer capacity. Use it to answer "why are there no flows?".

## Network problem classes

| Class                  | Raised when                                                                              | Default |
| ---------------------- | ---------------------------------------------------------------------------------------- | ------- |
| `network.policy-drops` | Sustained policy-denied drops between two workloads (about 10 in 5 minutes)              | Off     |
| `network.l7-errors`    | HTTP 5xx rate for a service above a threshold                                            | Off     |
| `network.dns-errors`   | A spike in NXDOMAIN or SERVFAIL                                                          | Off     |
| `hubble.relay-down`    | Hubble Relay is unhealthy                                                                | On, where Hubble is detected |

The network classes are **off by default** and are enabled per cluster, because they need a continuous flow subscription: a port-forward that runs only while you have v8s open. v8s subscribes to a narrow server-side filter (only dropped flows, plus HTTP 5xx only if you enabled that class), counts per pair per minute, and keeps nothing else. Turn them on for the clusters where you want them in the [Problems view](/guide/problems).

## Cilium policies

Kubernetes NetworkPolicy, CiliumNetworkPolicy, and CiliumClusterwideNetworkPolicy open like any other resource. NetworkPolicy has curated columns (pod selector and rule summary). The Cilium kinds use the generic view.
