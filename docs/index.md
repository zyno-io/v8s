---
layout: home

hero:
    name: v8s
    text: A fast, multi-cluster Kubernetes manager
    tagline: Smooth on 10k+ objects per cluster, several clusters at a time, on the desktop, in a browser, and on your phone.
    image:
        src: /logo.svg
        alt: v8s
    actions:
        - theme: brand
          text: Get started
          link: /guide/getting-started
        - theme: alt
          text: Download
          link: /guide/install
        - theme: alt
          text: GitHub
          link: https://github.com/zyno-io/v8s

features:
    - title: Fast on big clusters
      details: A Go agent keeps compact projections of your clusters and sends the UI only the rows on screen. Design budget on a laptop is 60 fps scrolling over 50k pods, filter and sort changes in 20 ms, and under 50 MB of agent memory per 10k pods.
      link: /guide/performance
      linkText: Why it is fast
    - title: Multi-cluster, by design
      details: Select clusters or a group such as production. Every list, search, and the Problems view span the selection, and v8s never connects to a cluster you did not pick.
      link: /guide/clusters
      linkText: Clusters and groups
    - title: Problems view
      details: Crash loops, failed Jobs, NotReady nodes, stuck rollouts, unhealthy Flux objects, failed Helm releases, and policy failures in one list, rolled up by workload and sorted by severity.
      link: /guide/problems
      linkText: Problems
    - title: Flux and Helm
      details: Reconcile, suspend, and inspect Flux objects with inventory trees and dependency graphs. See Helm history, diff any two revisions, and roll back, with a guard for Flux-managed releases.
      link: /guide/flux
      linkText: Flux and Helm
    - title: Security and network
      details: Kyverno policies and reports, Trivy Operator and Kubescape findings, and Hubble flows, drops, and service maps as built-in views instead of raw CRDs.
      link: /guide/security
      linkText: Security and network
    - title: Desktop, web, and phone
      details: One UI and one Go binary give you the Electron app, the self-hosted web app, and a Problems-first layout that works on a phone.
      link: /guide/mobile
      linkText: On a phone
    - title: Your credential, all the way through
      details: Requests carry your own credential to the API server or to an access gateway such as Warpgate. A linked write context takes over for writes. Secrets stay masked until you reveal a key.
      link: /guide/clusters#linked-read-and-write-contexts
      linkText: Linked contexts
    - title: Keyboard first
      details: A k9s-style command bar (`:ctx prod-us staging`, `:po payments`, `:deploy payments/api`), a search palette, and shortcuts for everything you do all day.
      link: /guide/navigation
      linkText: Navigation
---
