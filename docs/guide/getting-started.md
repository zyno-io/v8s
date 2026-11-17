# Getting started

v8s reads your kubeconfig, watches the clusters you select, and shows them in one UI. This page takes you from nothing to a failing pod's logs in about five minutes.

## The five-minute path

1. **Install the desktop app.** Download the installer for your OS from the [releases page](https://github.com/zyno-io/v8s/releases) and run it. Details and checksums are in [Install](/guide/install).
2. **Open v8s.** It starts its own agent in the background. Nothing is connected yet, on purpose.
3. **Pick clusters.** Open the cluster selector in the top bar. v8s lists every context from your kubeconfig. Tick the ones you want, or click a group chip, then press **Connect**. See [Clusters](/guide/clusters).
4. **Find a problem.** Press `:` and type `pb`, then Enter. The [Problems view](/guide/problems) lists what is broken across every connected cluster, worst first.
5. **Open it.** Press Enter on a problem to expand its evidence and quick actions, then `o` (or the open button) to open the object in the detail drawer. The drawer shows status, events, and YAML. Use the Logs tab (or press `l` on the row) to read the pod's output, and the Shell tab (or `s`) to get a terminal in it. See [Operations](/guide/operations).

::: tip You are not locked into one cluster
Everything you just did spanned every cluster you selected. Add another with `:ctx +staging`, drop one with `:ctx -staging`. See [Navigation](/guide/navigation#the-command-bar).
:::

## Prerequisites

- A kubeconfig with at least one context. v8s uses the same loading rules as `kubectl`: `$KUBECONFIG` (several files are merged) or `~/.kube/config`.
- Whatever your contexts already need to authenticate: client certificates, tokens, or an exec plugin such as `kubelogin`, `aws`, or `gke-gcloud-auth-plugin`. If `kubectl --context <name> get ns` works in a terminal, it works in v8s.
- Kubernetes RBAC that lets you list and watch what you want to see. v8s disables actions you are not allowed to take and tells you why.

## Run it in a browser instead: `v8s serve`

If you prefer a browser tab, or you are on a Linux box without a desktop shell, use the CLI.

1. Download the CLI archive for your platform from the releases page and unpack it.
2. Run it:

```sh
v8s serve
```

v8s listens on `127.0.0.1:7931`, prints a URL, and opens your browser. The URL carries a one-time token in its fragment, so only that browser tab can talk to the agent. To pick a different port or skip opening the browser:

```sh
v8s serve --port 8080 --no-browser
```

To use a specific kubeconfig file, add `--kubeconfig /path/to/config`.

On a remote machine, bind stays on loopback. Forward the port over SSH and open the printed URL locally:

```sh
ssh -L 7931:127.0.0.1:7931 devbox
```

::: warning Not for sharing
`v8s serve` runs as you, with your credentials, and has no login of its own. Do not expose its port to other people. For a team, use [hosted mode](/guide/hosted).
:::

## Try it without a cluster: demo mode

The agent can serve synthetic clusters so you can explore the UI, or check how it feels at scale, without touching a real cluster. The fake clusters have names like `prod-us`, `prod-eu`, and `staging`, realistic pods, a stream of churn, and objects you can edit and act on.

```sh
v8s agent --fake clusters=3,pods=50000,churn=200 \
  --listen 127.0.0.1:7931 --dev-token demo
```

Then open:

```text
http://localhost:7931/#agent=ws://localhost:7931/ws&token=demo
```

| Option           | Meaning                                                              | Default |
| ---------------- | -------------------------------------------------------------------- | ------- |
| `clusters=N`     | Number of fake clusters                                              | 3       |
| `pods=N`         | Total pods across the clusters                                       | 50000   |
| `churn=N`        | Pod events per second across the clusters                            | 200     |
| `lograte=N`      | Synthetic log lines per second per pod                               | 20      |
| `rbac=1`         | Makes the second cluster read-only and adds a linked-able admin context | off     |

Fake mode keeps its settings in memory unless you pass `--settings <file>`. Use `rbac=1` to try [linked read and write contexts](/guide/clusters#linked-read-and-write-contexts).

## Next steps

- Learn the [command bar and shortcuts](/guide/navigation).
- Understand [clusters, groups, and linked contexts](/guide/clusters).
- Choose which [problem classes](/guide/problems#what-it-finds) are on.
- See everything the CLI can do in the [CLI reference](/guide/cli).
- If something does not connect, see [Troubleshooting](/guide/troubleshooting).
