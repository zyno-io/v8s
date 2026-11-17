# Flux

v8s has built-in views for [Flux](https://fluxcd.io), so you do not have to read raw `.status.conditions` to know whether GitOps is healthy. The Flux section appears in the sidebar when a selected cluster has Flux installed (v8s detects the `*.toolkit.fluxcd.io` APIs on connect, and re-checks every five minutes or when a CRD changes).

## Supported kinds

| API group                        | Kinds                                                                             |
| -------------------------------- | --------------------------------------------------------------------------------- |
| `source.toolkit.fluxcd.io`       | GitRepository, OCIRepository, HelmRepository, HelmChart, Bucket, ExternalArtifact |
| `kustomize.toolkit.fluxcd.io`    | Kustomization                                                                     |
| `helm.toolkit.fluxcd.io`         | HelmRelease                                                                       |
| `notification.toolkit.fluxcd.io` | Provider, Alert, Receiver                                                         |
| `image.toolkit.fluxcd.io`        | ImageRepository, ImagePolicy, ImageUpdateAutomation                               |

v8s reads whichever API version your cluster serves, so it works across Flux upgrades. Flux Operator kinds (FluxInstance, ResourceSet) show up as generic custom resources.

## Overview

The Flux overview spans every selected cluster:

- Readiness by kind: total, ready, not ready, reconciling or unknown, and suspended, with the failing objects listed first.
- The health of Flux's own controllers (the Deployments in `flux-system`).
- Objects that have not reconciled in three times their interval.

Flux objects are few and small, so v8s keeps them live as part of the [Problems](/guide/problems) baseline. The overview is instant.

## Tables

Each Flux kind has its own columns:

| Column         | Meaning                                                                     |
| -------------- | --------------------------------------------------------------------------- |
| Ready          | The `Ready` condition with its reason, for example `False · ArtifactFailed` |
| Suspended      | Whether `spec.suspend` is set                                               |
| Revision       | Short commit SHA or chart version                                           |
| Source         | The `sourceRef`                                                             |
| Interval       | `spec.interval`                                                             |
| Last reconcile | How long ago it last reconciled                                             |
| Message        | Truncated here; the full message is in the tooltip                          |

## Actions

Select one or more objects, in one cluster or several, and use the action bar.

| Action                        | What it does                                                                               |
| ----------------------------- | ------------------------------------------------------------------------------------------ |
| Suspend / Resume              | Sets `spec.suspend`                                                                        |
| Reconcile                     | Requests a reconcile by setting `reconcile.fluxcd.io/requestedAt`                          |
| Reconcile with source         | Reconciles the referenced source first, waits for it to finish, then reconciles the object |
| Force reconcile (HelmRelease) | Forces a fresh install or upgrade run                                                      |
| Reset failures (HelmRelease)  | Clears the failure counters so remediation starts over                                     |

### Reconcile feedback

After you request a reconcile, the row shows `Reconciling…` until Flux reports it handled your request, then it resolves to the new Ready state. You get honest feedback without v8s polling the cluster.

## Kustomization detail

- **Inventory** (a tab on the Kustomization). The objects it manages, each joined to the live object and its health. "This Kustomization is Ready but two of its Deployments are crash looping" is visible at a glance.
- **Dependency graph** (Flux, Dependency graph in the sidebar). The `dependsOn` graph across sources, Kustomizations, and HelmReleases. If a chain is blocked, the root cause is the one marked.
- **Events** for the object.

Inventories can hold thousands of entries. v8s keeps them compact and joins them to live objects only when you open the detail.

## HelmRelease detail

A HelmRelease and the Helm release underneath it are the same thing seen from two sides, so the detail shows both: Flux status, remediation counters (`installFailures`, `upgradeFailures`), and the Helm release history, values, and manifest diff between revisions. See [Helm](/guide/helm).

## Sources

A source's detail shows its status, artifact revision, and conditions on the Overview tab. The `sourceRef` of each Kustomization and HelmRelease is a column in their tables, so you can sort and filter to find what uses a source.

## Editing Flux-managed objects

If you edit an object that Flux manages, the review step shows a banner naming the Kustomization or HelmRelease, and the object's Overview tab offers **Open source**. Flux will revert a direct change at the next reconcile, so the change usually belongs in Git, or you can suspend the owner first. See [Viewing and changing resources](/guide/resources#gitops-banner).

## Problems from Flux

| Class                     | Meaning                                              |
| ------------------------- | ---------------------------------------------------- |
| `flux.not-ready`          | `Ready=False`                                        |
| `flux.stalled`            | `Stalled=True`                                       |
| `flux.stale`              | No successful reconcile in three intervals           |
| `flux.source-failed`      | A source could not fetch                             |
| `flux.controller-down`    | A Flux controller is not running                     |
| `flux.dependency-blocked` | Blocked by a dependency, collapsed to the root cause |
| `flux.suspended`          | Suspended (info, off by default)                     |

See [Problems](/guide/problems#flux-and-helm) for the full list.
