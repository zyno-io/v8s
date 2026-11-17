# Network: Hubble

If you run [Cilium](https://cilium.io) with Hubble, v8s shows live network flows, dropped traffic, and a service map next to your workloads. Hubble is read-only here: you see what the network is doing and jump to the policy responsible, and you change policy through the normal YAML editor.

## Requirements

- **Hubble Relay** running in the cluster. v8s looks for a `hubble-relay` Service in any namespace (`kube-system` is preferred) and a ready relay pod behind it. The Network flows section of the sidebar appears when a relay is found in a selected cluster.
- **`create` on `pods/portforward`** in the relay's namespace. v8s reaches the relay through a Kubernetes port-forward carrying your own credential, so it works the same whether you reach the cluster directly or through a gateway such as Warpgate. If the permission is missing, v8s says so plainly.

There are no direct relay URLs. Hubble Relay has no authorization of its own, so v8s never connects to it except through a port-forward as you.

## Flows tab

On pods, Deployments, StatefulSets, DaemonSets, ReplicaSets, Services, and namespaces, the **Flows** tab shows a live table of flows: time, source, destination, port and protocol, an L7 summary or drop reason, and a verdict tag (`FORWARDED`, `DROPPED`, `ERROR`, `AUDIT`). **Pause** and **Follow** stop and resume the table, and **Clear** empties it.

Filters are applied by the relay, not in your browser: verdict, direction, protocol (TCP, UDP, ICMP, HTTP, DNS), drop reason, HTTP status (for HTTP), and DNS query (for DNS).

A flow view is always scoped to the object whose tab you opened, a namespace or a workload. v8s never opens an unfiltered stream of a whole cluster. A busy cluster can produce thousands of flows per second, so:

- The table shows at most about 200 flows per second. Above that, v8s samples and the toolbar says so ("showing 200/s of ~3,400/s, narrow the filter").
- The view keeps the last 10,000 flows on desktop (2,000 on a phone).
- A paused flow stream is cancelled after ten seconds. The shared connection to the relay closes 30 seconds after its last user.

## Service map

Pick a cluster, a namespace, and a window (5 minutes, 15 minutes, or 1 hour), and the **service map** draws workloads as nodes (grouped by identity, not by pod) with edges for requests, errors, and drops. Dropped edges are red and dashed, so colour is not the only signal. Select a node for its edges and an **Open flows** shortcut. An edges table below lists every edge with requests, errors, drops, and a status word, sorted by drops, errors, then requests. v8s builds the map from a filtered flow stream and sends the UI only a small graph. The map is not offered on a phone, which has the Drops and Hubble status views.

## Drops view

The **Drops** view lists dropped flows across all selected clusters for the last hour (or 5 minutes), grouped by source workload, destination workload, and drop reason, with counts and last seen. Sort by most drops or most recent, and filter by text. Each row names the NetworkPolicy or CiliumNetworkPolicy most likely responsible, and its details open the policy in the drawer. "No matching policy found" usually means default deny or an unmanaged rule.

## Hubble status

Per cluster: whether the relay is available, its version and uptime, flows seen and per second, the flow buffer fill, the connected and unavailable nodes with each node's state, and a "Why are there no flows?" checklist. Use it to answer that question.

## Network problem classes

| Class                  | Raised when                                                                              | Default |
| ---------------------- | ---------------------------------------------------------------------------------------- | ------- |
| `network.policy-drops` | Sustained policy-denied drops between two workloads (about 10 in 5 minutes)              | Off     |
| `network.l7-errors`    | HTTP 5xx count for a service above a threshold (about 20 in 5 minutes)                   | Off     |
| `network.dns-errors`   | A spike in NXDOMAIN or SERVFAIL (about 20 in 5 minutes)                                  | Off     |
| `hubble.relay-down`    | Hubble Relay is unreachable                                                              | On      |

The three flow classes are **off by default** because they need a continuous flow subscription: a port-forward that runs only while you have v8s open. v8s subscribes to a narrow server-side filter (only dropped flows, plus HTTP 5xx only if you enabled that class), counts per pair per minute, and keeps nothing else. Turn them on in the [Problems view](/guide/problems). `hubble.relay-down` is on by default and raises only where a relay exists.

## Cilium policies

Kubernetes NetworkPolicy, CiliumNetworkPolicy, and CiliumClusterwideNetworkPolicy open like any other resource. NetworkPolicy has curated columns (pod selector and rule summary). The Cilium kinds use the generic view.
