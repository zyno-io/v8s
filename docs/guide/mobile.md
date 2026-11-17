# On a phone

v8s works in a phone's browser, with no app to install. It is built for the jobs people do on a phone: is anything broken, what, since when, and can I do the one safe thing that fixes it. It is not a shrunken desktop, and it steers you to filtered views instead of giant tables.

A phone needs [hosted mode](/guide/hosted): the web app talks to a `v8s server` that does the watching. (`v8s serve` binds to loopback and is meant for the machine it runs on.)

## Open it

Browse to your v8s URL and sign in. v8s opens on **Problems**. The bottom bar has Problems, Clusters, Search, Starred, and More.

- **Pick clusters** with the group chips. One tap selects a group such as `production`.
- **Read Problems.** Each card shows severity, cluster, what is wrong, and for how long. Tap a card for the evidence and quick actions.
- **Acknowledge or snooze** a problem you have handled.
- **Search** for an object by name across the selected clusters.
- Tap an object for a full-screen detail page with status, conditions, events, and related objects. Tabs scroll sideways.

The server does the watching, so your phone's battery and data plan do not pay for it. Watches start when you open v8s and stop shortly after you close it, so a check-in starts cold. Budget for the first problems is about two seconds, and about six seconds for six clusters of 10k pods on 4G.

## What works on a phone

| Task                                            | On a phone                                                  |
| ----------------------------------------------- | ----------------------------------------------------------- |
| Problems across clusters                        | The main use. Grouped by workload, severity filter, acknowledge, snooze |
| Choose clusters and groups                      | Yes. Group chips are the fast path                          |
| Search objects by name                          | Yes                                                         |
| Object detail: status, conditions, events       | Yes, full screen                                            |
| Logs                                            | Yes, tail mode: last 500 lines, follow, search. Merged multi-pod logs are desktop-first |
| Flux status, suspend, resume, reconcile         | Yes. One-tap reconcile is the most common phone action      |
| Helm status, history, rollback                  | Yes. Rollback is hold-to-confirm                            |
| Scale, restart, delete a pod, trigger a CronJob | Yes, hold-to-confirm                                        |
| Cordon, uncordon                                | Yes                                                         |
| Drain                                           | Yes. The pre-flight summary is shown in full before you confirm |
| Security summaries                              | Yes, as summaries. Full CVE tables work but are desktop-first |
| Pod shell                                       | Works, with a simplified terminal and an accessory key row. Best on desktop |
| Edit YAML                                       | Reading is fine. Editing works but is not designed for a phone |
| Node shell                                      | Desktop only. Hidden on phones                              |
| Port-forward                                    | Desktop only                                                |
| Dense multi-cluster tables                      | Works as cards, but steers you to filtered views            |
| Hubble service map, Flux graphs                 | Desktop-first. The phone shows lists instead of graphs      |
| Bulk actions across clusters                    | Desktop only. Too easy to fat-finger                        |

## Layout differences

- Below 900 px the sidebar is a bottom sheet behind a **Resources** button.
- Below 640 px tables become fixed-height cards: name and status on the first line, cluster, namespace, and age under it, one key number on the right. Multi-select is long-press.
- Touch targets are at least 44 px.
- Destructive actions use hold-to-confirm or type-the-name, never a single tap.

## Keeping data use small

On a phone, v8s subscribes narrowly: a summary of problems (about 1 KB), a small window of rows (60 either side of the screen, fewer columns), and the object you have open. Updates arrive about once a second. With the Problems view open on a quiet cluster set it uses at most about 30 KB a minute, and nothing while the tab is hidden: after 30 seconds in the background it drops every subscription, and resuming costs one frame per view.

## Not yet

Installing v8s as an app (PWA) and push notifications are not part of the first release. v8s only evaluates problems while a client is open, so there is nothing to push from.
