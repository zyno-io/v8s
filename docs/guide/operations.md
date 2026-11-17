# Operations

v8s can change things as well as show them. Every write goes through the same safeguards, and writes use your own credential.

## Editing

Open an object's YAML to read or edit it. Edits are checked with a server-side dry run first, show a diff before applying, and warn when a field is owned by another manager (for example Flux). If the object changed underneath you, v8s shows the conflict as a diff instead of overwriting it.

## Everyday actions

Scale, restart, delete a Pod, trigger a CronJob, cordon and uncordon nodes. Destructive actions ask for confirmation, and on a phone they use hold-to-confirm.

## Logs

Follow a container's logs, search them, and download them. Tail mode on a phone shows the last 500 lines.

## Shells

Open a shell in a Pod. For distroless images v8s can attach a debug container. A node shell is available on desktop and may use a separate write context.

## Drain

Drain shows a pre-flight summary first: Pods blocked by disruption budgets and unmanaged Pods. The drain then runs as a resumable job.

## Port-forward

Port-forward a Pod or Service to your machine from the desktop app.

## Safety model

Operations carry your credential, never an identity of v8s's own, and the cluster's RBAC decides what is allowed. The UI hides actions you cannot perform. The full model is in [Operations and security](/operations).
