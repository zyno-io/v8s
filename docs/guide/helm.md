# Helm

v8s shows every Helm release in your selected clusters, lets you inspect history and diff revisions, and can roll back, uninstall, or retry a release. It does this with the Helm SDK as you, so hooks, waits, and history pruning behave like the `helm` CLI. You do not need `helm` installed. Helm needs no server components, so the Helm section is always present.

## Releases table

Releases from all selected clusters are in one table: name, namespace, revision, status, updated, and **managed by** (a link to the Flux HelmRelease when Flux manages it).

| Status                                                   | Tone    |
| -------------------------------------------------------- | ------- |
| `deployed`                                               | success |
| `failed`                                                 | danger  |
| `pending-install`, `pending-upgrade`, `pending-rollback` | warning |
| `superseded`                                             | info    |
| `uninstalling`                                           | neutral |

Helm keeps each revision in a Secret, and v8s lists releases from the Secret metadata only, so a cluster with hundreds of releases and thousands of revisions loads fast and never downloads the large chart payloads just to show a list. The **chart**, **chart version**, and **app version** columns fill in for the rows you can see, because they live inside the payload. Sorting or filtering by chart decodes the rest in the background, with a progress indicator.

You can see releases if you can `list` Secrets in the namespace; `get` is needed only to open one.

## Release detail

The release drawer has six tabs:

- **Overview.** Description, last updated, chart and app version, and the release's hooks (name, kind, events, weight).
- **History.** Every revision and its status. Pick one revision to roll back to, or tick two to compare.
- **Values.** Switch between the values you supplied and the computed values.
- **Manifest.** The rendered manifest.
- **Notes.** The chart's notes.
- **Diff.** Pick any two revisions and see the difference in values and in the manifest. The diff is computed in the agent.

## Actions

| Action    | What it does                                                                                                                                               |
| --------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Roll back | Roll back to a revision you choose. Options: wait, timeout, clean up on failure, and force (behind a confirm)                                              |
| Uninstall | Remove the release. Options: keep history, wait, timeout, cascade policy. Type to confirm in production groups                                             |
| Retry     | For a `failed` or stuck `pending-*` release, re-run the last upgrade with the chart and values stored in the release. No chart repository access is needed |

For a stuck `pending-*` release, **Retry** offers **Mark failed** (on by default), which is what people do by hand today so the release can be retried.

Each action is a [job](/guide/operations#the-jobs-tray), so a reload or a dropped connection does not lose track of it, and it keeps running for a few minutes if you close the window.

## Flux-managed releases

If a release carries Flux's labels (`helm.toolkit.fluxcd.io/name` and `namespace`), Flux owns it, and v8s guards you:

- **Roll back** and **Retry** become HelmRelease operations: reconcile, force, or reset failures.
- **Uninstall** warns that Flux will install the release again unless you suspend or delete the HelmRelease first.
- The direct Helm actions are still there, behind an "I know, do it anyway" confirmation.

See [Flux](/guide/flux).

## Problems from Helm

| Class                | Meaning                                                 | Default |
| -------------------- | ------------------------------------------------------- | ------- |
| `helm.failed`        | The latest revision is `failed`                         | On      |
| `helm.stuck-pending` | A release has been `pending-*` for more than 15 minutes | On      |

See [Problems](/guide/problems).

## Not included

Upgrading a release with edited values is not part of the first release. Use your normal deployment path (Flux, CI, or `helm upgrade`).
