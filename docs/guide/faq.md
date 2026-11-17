# FAQ

## How is v8s different from other Kubernetes UIs?

It is built around a performance budget. A Go agent keeps compact projections of your clusters and sends the UI only the rows it is showing, so large clusters and several clusters at once stay smooth. It is also multi-cluster by default and has a Problems view across all of them.

## Does v8s send my cluster data anywhere?

No. The agent runs on your machine (or in your own infrastructure for hosted mode) and talks to your clusters directly. There is no v8s cloud service.

## Which credentials does v8s use?

Yours. Requests carry your own credential to the API server or access gateway you configured. See [Authentication](/auth).

## Does it work with exec credential plugins?

Yes on desktop and with `v8s serve`: the agent loads your kubeconfig the same way `kubectl` does, including exec plugins.

## Can I run it on a server for my team?

Not yet. Multi-user [hosted mode](/guide/hosted) is planned. Do not expose the single-user agent or the container image to other people.

## Which platforms are supported?

The desktop app runs on macOS, Windows, and Linux. The CLI is available for Linux and macOS on amd64 and arm64, and for Windows on amd64.

## How do I report a bug or request a feature?

Open an issue on [GitHub](https://github.com/zyno-io/v8s/issues).
