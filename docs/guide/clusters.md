# Clusters

v8s works on a selection of clusters, not on one at a time. Every list, search, and the Problems view span whatever you have selected, and v8s never connects to a cluster you have not selected.

## Where clusters come from

On the desktop and with `v8s serve`, clusters are the contexts in your kubeconfig. The agent uses the standard client-go loading rules, so `$KUBECONFIG` with several files is merged, and exec credential plugins work as they do for `kubectl`. Pass `--kubeconfig <path>` to `v8s serve` or `v8s agent` to use a specific file.

## Selecting clusters

Open the cluster selector in the top bar and tick the clusters you want. v8s remembers the selection for next time. Selecting a cluster starts its watches; deselecting stops them and frees the agent's memory for it.

## Groups

A group is a named set of clusters such as `production` or `staging`. Choosing a group selects all of its clusters in one step, and group chips are the fast path on a phone.

## Connection states

Each cluster shows its state next to its name: connecting, syncing, live, or an error such as no access or an expired login. A cluster that fails does not block the others; its rows simply leave the merged views until it recovers.

## Credentials

v8s sends your own credential to whatever endpoint the cluster is configured with, either the API server or an access gateway. See [Authentication](/auth) for the model, including linked read and write contexts.
