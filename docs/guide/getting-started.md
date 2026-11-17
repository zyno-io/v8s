# Getting started

v8s is a fast, multi-cluster Kubernetes manager. It reads your kubeconfig, watches the clusters you select, and shows them in one UI that stays responsive on very large clusters. This page gets you from nothing to a running v8s in a few minutes.

::: info Early development
v8s is under active development. The first builds browse Pods across clusters; the rest of the feature set follows the [roadmap](/architecture#roadmap).
:::

## Pick how to run it

| Option      | Best for                                           | Command or download                                                         |
| ----------- | -------------------------------------------------- | --------------------------------------------------------------------------- |
| Desktop app | Daily use on macOS, Windows, and Linux             | Installer from the [releases page](https://github.com/zyno-io/v8s/releases) |
| `v8s serve` | A browser tab, remote dev boxes over an SSH tunnel | `v8s serve`                                                                 |
| Container   | Trying the agent in a container                    | `ghcr.io/zyno-io/v8s` (see [Install](/guide/install))                       |

## Run `v8s serve`

1. Download the CLI archive for your platform from the releases page and unpack it.
2. Run `v8s serve`.
3. v8s prints a URL and opens it in your browser. The URL carries a one-time token in its fragment, so only this browser tab can talk to the agent.

```sh
v8s serve --port 7931 --no-browser
```

## Select clusters

v8s lists the contexts from your kubeconfig (`$KUBECONFIG` is merged, like `kubectl`). Open the cluster selector, tick the clusters you want, and the tables fill in. See [Clusters](/guide/clusters).

## Next steps

- [Install](/guide/install) the desktop app, the CLI, or the container.
- Learn the keyboard-first [navigation](/guide/navigation).
- Check what is broken across everything with [Problems](/guide/problems).
