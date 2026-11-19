# FAQ

## How is v8s different from other Kubernetes UIs?

It is built around a performance budget. A Go agent keeps compact projections of your clusters and sends the UI only the rows it is showing, so large clusters, and several at once, stay smooth. It is multi-cluster by default and has a [Problems view](/guide/problems) across all of them. See [Why it is fast](/guide/performance).

## Does v8s send my cluster data anywhere?

No. The agent runs on your machine, or in your own infrastructure for [hosted mode](/guide/hosted), and talks to your clusters directly. There is no v8s cloud service and no telemetry. The one outside request is a once-a-day check of GitHub for a newer release: it carries no cluster data and no version or identifiers, and you can [turn it off](/guide/install#updates).

## Which credentials does v8s use?

Yours. Every request carries your own credential to the API server or access gateway you configured. v8s never reads cluster data under an identity of its own, unless a hosted-mode admin configures an impersonation or kiosk credential for a specific cluster. See [Authentication](/auth).

## Does it connect to all my kubeconfig contexts?

No. v8s lists them but connects to none until you select clusters and press **Connect**. It never connects to a cluster you did not select. See [Clusters](/guide/clusters#choose-clusters).

## Does it work with exec credential plugins?

Yes, on the desktop and with `v8s serve`. The agent loads your kubeconfig the way `kubectl` does, including `kubelogin`, `aws`, and `gke-gcloud-auth-plugin`. See [Troubleshooting](/guide/troubleshooting#exec-plugin-not-found) if one is not found.

## Does it work with Warpgate or another access gateway?

Yes. To v8s a gateway is a Kubernetes endpoint with OIDC, configured like any other cluster. Link the read-only and writable targets so writes switch automatically. Elevation belongs to the gateway. See [Linked contexts](/guide/clusters#linked-read-and-write-contexts) and [Hosted mode](/guide/hosted#warpgate).

## Are Secrets safe to browse?

Secret lists show names and metadata only: values are never downloaded for a list. Values stay hidden until you choose **Reveal all** on the Secret, which fetches the whole object and needs `get` on that Secret. Secret values never go into the recent activity list. See [Secrets](/guide/resources#secrets).

## Can v8s keep my logs?

No. v8s reads logs live from pods and keeps no long-term logs, and no record of your activity beyond a short in-memory list. Use your log pipeline for history, and your gateway and the Kubernetes audit log for audit. See [Operations](/guide/operations#logs).

## Can I run it on a server for my team?

Yes: [hosted mode](/guide/hosted) (`v8s server`). It is self-hosted only, with OIDC sign-in and each person's own token passed through to the cluster. Do not expose `v8s serve` or the single-user agent to other people.

## Which platforms are supported?

The desktop app runs on macOS (Apple silicon and Intel), Windows (x64), and Linux (x64: AppImage, deb, rpm). The CLI is available for Linux and macOS on amd64 and arm64, and for Windows on amd64. The container image is `linux/amd64` and `linux/arm64`. See [Install](/guide/install).

## Does it work on a phone?

Yes, through hosted mode in a phone browser, with Problems as the landing page. See [On a phone](/guide/mobile).

## Which Kyverno versions are supported?

Kyverno's CEL policies (`policies.kyverno.io`) are supported. From Kyverno 1.20, only CEL policies run, so classic `ClusterPolicy` and `Policy` are shown read-only with a hint to migrate. See [Security](/guide/security#kyverno).

## Why does a view show "syncing"?

The first rows arrive as soon as they are loaded, and the count updates until the cluster has been read in full. You can work in the view meanwhile. See [Connection states](/guide/clusters#connection-states).

## Can I use v8s without a cluster?

Yes, in demo mode: `v8s agent --fake clusters=3,pods=50000`. See [Getting started](/guide/getting-started#try-it-without-a-cluster-demo-mode).

## Does it support Argo CD, cert-manager, Istio, or KEDA?

Flux, Helm, Kyverno, Trivy Operator, Kubescape, and Hubble have built-in views. Other custom resources, including Argo CD and cert-manager, open as generic resources with their own printer columns, and cert-manager certificates feed a [Problems](/guide/problems) class. Built-in views for more integrations are planned.

## How do I report a bug or request a feature?

Open an issue on [GitHub](https://github.com/zyno-io/v8s/issues). See [Troubleshooting](/guide/troubleshooting#how-to-report-a-bug) for what to include.
