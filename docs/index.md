---
layout: home

hero:
    name: v8s
    text: a fast, multi-cluster Kubernetes manager
    tagline: Smooth on 10k+ objects per cluster, several clusters at a time, on desktop, in the browser, and on your phone.
    image:
        src: /logo.svg
        alt: v8s
    actions:
        - theme: brand
          text: Get started
          link: /guide/getting-started
        - theme: alt
          text: Read the design
          link: /architecture
        - theme: alt
          text: GitHub
          link: https://github.com/zyno-io/v8s

features:
    - title: Fast on big clusters
      details: A Go agent projects only the fields the UI needs and sends windowed patches, so 50k-pod tables scroll at 60 fps and filters answer instantly.
      link: /performance
      linkText: Performance design
    - title: Multi-cluster
      details: Pick clusters or a group such as production or staging. Every list, search, and the Problems view span the selection, and v8s only connects to clusters you chose.
      link: /guide/clusters
      linkText: Clusters
    - title: Problems
      details: One view of crash loops, failed Jobs, NotReady nodes, unhealthy Flux objects, failed Helm releases, policy failures, and more, across every selected cluster.
      link: /guide/problems
      linkText: Problems
    - title: Flux & Helm
      details: First-class Flux and Helm views, plus Kyverno, Trivy, Kubescape, and Hubble, instead of generic CRD browsing.
      link: /guide/integrations
      linkText: Integrations
    - title: Security & network
      details: Your own credential goes all the way through, to the API server or an access gateway such as Warpgate. Writes can switch to a linked write context.
      link: /auth
      linkText: Authentication
    - title: Desktop, web & phone
      details: One Vue bundle and one Go binary produce the Electron app, the self-hosted web app, and a phone-friendly Problems-first layout.
      link: /packaging
      linkText: Packaging
---
