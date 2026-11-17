# Flux, Helm, and integrations

v8s has dedicated views for the tools clusters are actually run with, instead of showing them only as generic custom resources.

## Flux

All Flux kinds in one place: sources, Kustomizations, HelmReleases, image automation, and notifications. You get an overview of what is ready, an inventory tree for each Kustomization, and the dependency graph. Suspend, resume, and reconcile (including forced reconcile) are one action each, and work in bulk.

## Helm

Releases are listed from their metadata, so the list renders quickly even on clusters with hundreds of releases. Open a release for its history, values, manifest, and a diff between revisions. Rollback, uninstall, and retry are available, and releases managed by Flux are routed to their HelmRelease.

## Policy and security

- **Kyverno**: policy reports and failing resources.
- **Trivy**: vulnerabilities per image and workload.
- **Kubescape**: compliance findings.
- **Hubble**: network flows, including dropped traffic.

Findings also feed the [Problems](/guide/problems) view.

## Other platform views

Gateway API and cert-manager have their own views. Any other custom resource opens in a generic table driven by the cluster's discovery data.

The coverage matrix and per-integration design notes are in [Integrations](/integrations).
