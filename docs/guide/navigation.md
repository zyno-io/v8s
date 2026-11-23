# Navigation

v8s is keyboard-first. Almost everything you can reach with the mouse is also reachable from the command bar, the search palette, or a shortcut. Press `?` at any time to see the list of shortcuts.

## Layout

- **Top bar.** The cluster selector, search, the problems badge, and the theme and user menu.
- **Sidebar.** Starred types first, then a Cluster section with **Overview**, **Problems**, and the cluster-level types, then Security (Vulnerabilities, Policies, and Trivy report types) and Network flows (Drops, Service map, Hubble status), then Workloads, Network, Config, Storage, Access, Flux, Helm, and More, and last Custom resources. Security, Network flows, and Flux appear only when the integration is detected in at least one selected cluster. Custom resource groups are discovered from each cluster's CRDs and expand on demand. On a desktop v8s opens on Pods, on a phone on Problems.
- **Overview.** One card per selected cluster: connection state and version, node and pod counts (running, failing, pending, completed), problem counts by severity, GitOps health for Flux and Helm, and the integrations detected there. Open it from the sidebar.
- **Table.** The current resource type across all selected clusters.
- **Detail drawer.** Opens on the right when you select a row. See [Viewing and changing resources](/guide/resources).

The sidebar can be **pinned** (part of the layout, the default) or **unpinned**. Click the pin button at the top of the sidebar, or press `[`, to unpin it: the sidebar hides and the content takes the full width. Hover the left edge of the window, or click the sidebar button next to the top bar's left edge, to open it as an overlay. It closes when the pointer leaves it, when you click outside, press `Esc`, or follow a link in it. While it is open, the pin button pins it back. Drag the sidebar's right edge to resize it (180 to 420 px), or focus the edge and use the arrow keys (16 px per press); double-click the edge to reset the width. Both settings are saved with your other settings.

Below 900 px wide the sidebar becomes a bottom sheet, and below 640 px tables become cards. See [On a phone](/guide/mobile).

## Tables

Every resource list is the same component, so the controls work everywhere.

- **Sort.** Click a column header. Shift-click to add a secondary sort key.
- **Filter.** Type in the filter box (or press `/`). It matches names and answers instantly, even over tens of thousands of rows.
- **Tone chips.** Where a type has a status, chips such as success, warning, and danger appear with live counts. Toggle them to show only broken or only healthy rows. Status is always a word, never colour alone: `CrashLoopBackOff`, `Running`, `Suspended`.
- **Columns.** Use **Columns** to hide or show columns, and drag column edges to resize. Your layout is remembered per resource type.
- **Cluster column.** The cluster column (a short tag with the cluster name) appears only when more than one cluster is selected.
- **Namespace filter.** Set it from the namespace menu or with `:ns`. The menu lists the namespaces you picked under **Selected**, below the scrolling list, so they stay in view while you search; click one there to remove it.
- **Selection.** Select rows with `x` or the checkboxes. A bulk action bar appears at the bottom with the actions that apply to every selected row, such as Restart or Delete.
- **Syncing.** While a cluster is still loading, its rows stream in and the toolbar shows a progress tag such as `syncing prod-eu · 4,210`. There is no full-table spinner.

## Keyboard shortcuts

Press `?` for the same list in the app.

| Key                   | Action                                                                         |
| --------------------- | ------------------------------------------------------------------------------ |
| `:`                   | Open the command bar                                                           |
| `/`                   | Focus the filter                                                               |
| `j` / `k`             | Move the focus down / up (arrow keys also work)                                |
| `Enter`               | Open the focused row in the drawer                                             |
| `x`                   | Select or deselect the focused row                                             |
| `l`                   | Open the Logs tab of the focused pod or workload                               |
| `s`                   | Open the Shell tab of the focused pod                                          |
| `e`                   | Edit the YAML of the focused row, or of the object open in the drawer          |
| `d`                   | Describe: open the focused row, or the open object, on the Overview tab        |
| `y`                   | Open the focused row, or the open object, on the YAML tab                      |
| `c`                   | Copy the focused row's name                                                    |
| `n`                   | Copy the focused row's namespace (namespaced types)                            |
| `Shift+J`             | Jump to the owner: Pod to ReplicaSet, ReplicaSet to Deployment, Job to CronJob |
| `o`                   | On pods, open the node the pod runs on                                         |
| `Shift+F`             | Port-forward the focused pod or service                                        |
| `r`                   | Restart the focused workload, or the selected workloads (asks first)           |
| `Ctrl+D`              | Delete the focused row, or the selected rows                                   |
| `Enter` in the filter | Move focus to the results (first row), then `j` / `k` or the arrow keys        |
| `Esc`                 | Clear the filter, then the selection, then close the drawer                    |
| `Ctrl+K` / `Cmd+K`    | Open the search palette                                                        |
| `[`                   | Pin or unpin the sidebar                                                       |
| `?`                   | Show the shortcut list                                                         |

`y`, `c`, `n`, `Shift+J`, `o`, `Shift+F`, `r` and `Ctrl+D` are k9s-style. They use the same actions as the action bar and the drawer, with the same permission checks and confirmations: `Ctrl+D` and `r` ask before they run, bulk or protected-cluster changes still need the typed confirmation, and a key does nothing (or shows the reason) where the action is not offered. `Shift+J` reads the object's owner references and shows "No owner" when there is none. `r` applies to Deployments, StatefulSets and DaemonSets; `Shift+F` to pods and services (and the port-forward dialog); `o` to pods. With no row focused (for example after a `Shift+J` jump) the keys act on the object open in the drawer. Confirmations from `Ctrl+D` and `r` start with Cancel focused, so a stray `Enter` never runs them. Helm releases use their own Uninstall and Upgrade actions instead of `Ctrl+D` and `r`.

`l` and `s` act only where the type has that tab: `l` on pods and workloads, `s` on pods. The [Problems view](/guide/problems#keyboard) has its own keys (`j`, `k`, `Enter`, `a`, `o`).

In a filter, `Enter` hands focus to the list, with the first row focused. `Esc` from the list clears the filter and keeps focus on the list; `Esc` again continues the order below. `Esc` works in order, so repeated presses unwind your view one layer at a time. Inside the YAML editor, `Esc` only leaves the editor, so it can never close the drawer over unsaved edits. Apart from `Ctrl+D` and `Ctrl+K`, v8s avoids `Ctrl+letter` shortcuts, so your browser and OS keep theirs (`Cmd+D` still bookmarks on macOS). Shortcuts are off while you type in a text field.

## The command bar

Press `:` anywhere to open a one-line command bar at the top, like k9s. Completion is as you type: prefix matches first, then fuzzy, over commands, resource types, namespaces, clusters, and groups. `Tab` accepts a completion. Unknown commands say so inline; they never navigate somewhere unexpected.

### Choose clusters

| Command                | Effect                                                                    |
| ---------------------- | ------------------------------------------------------------------------- |
| `:ctx`                 | Open the cluster selector                                                 |
| `:ctx prod-us staging` | Connect exactly these clusters. Names or group names, separated by spaces |
| `:ctx +edge`           | Add `edge` to the current selection                                       |
| `:ctx -staging`        | Remove `staging` from the current selection                               |

You cannot mix plain names with `+name` and `-name` in one command. A group name selects every cluster in the group, so `:ctx production` connects the whole group.

### Open resources

Type a resource type by its plural, kind, or short name. `po`, `pods`, `pod`, `deploy`, `svc`, `no`, `cj`, `hr`, `ks` all work, and CRDs complete from discovery. If a prefix matches several types, the bar lists the candidates.

| Command                | Effect                                                    |
| ---------------------- | --------------------------------------------------------- |
| `:po`                  | Open pods (all namespaces)                                |
| `:po payments`         | Open pods in the `payments` namespace                     |
| `:deploy payments/api` | Open the Deployment `api` in `payments`, in the drawer    |
| `:no worker-1`         | For cluster-scoped types, the argument is the object name |
| `:hr`                  | Open Flux HelmReleases                                    |

### Other commands

| Command              | Effect                                                 |
| -------------------- | ------------------------------------------------------ |
| `:ns`                | Open the Namespaces list (same as `:namespaces`)       |
| `:ns payments`       | Filter the current view to a namespace                 |
| `:ns shop,pay*`      | Filter to several namespaces; `*` matches any text     |
| `:ns +shop`          | Add `shop` to the namespace filter                     |
| `:ns -shop`          | Remove `shop` from the namespace filter                |
| `:ns -kube-*`        | Remove every namespace in the filter matching `kube-*` |
| `:ns all`            | Clear the namespace filter                             |
| `:pb` or `:problems` | Open the [Problems view](/guide/problems)              |
| `:star` / `:unstar`  | Star or unstar the current resource type               |
| `:q`                 | Close the bar (`Esc` does the same)                    |

Aliases: `:context` for `:ctx`, `:namespace` for `:ns`.

`:ns` takes comma-separated lists (`:ns a,b,c`) and `*` patterns anywhere in a name (`:ns pay*`, `:ns *-api`, `:ns a,b*,c`), also after `+` and `-`. A pattern is expanded once, against the namespaces v8s knows when you run the command, so a namespace created later is not added; a pattern that matches nothing is an error. `+` patterns match all known namespaces and `-` patterns the ones in the filter. Completion works on the part after the last comma, and a pattern shows how many namespaces it matches.

## Search palette

Press `Ctrl+K` (`Cmd+K` on macOS) to search across everything. It finds objects by name across the selected clusters (searched in the agent, so it is fast on big clusters), resource types, and commands. Pick a result to open it.

### Search by IP

Type an address to find what holds it. The palette understands:

- a full address (`10.42.3.7`, `fd00::5`), with an optional port (`10.42.3.7:8080`, `[fd00::5]:443`) or zone (`fe80::1%eth0`)
- a partial address as you type (`10.42.`, `10.4`, `fd00:10:`)
- a CIDR (`10.42.0.0/16`) or a range (`10.0.0.1-10.0.0.50`)

Results come in one section per kind, `IP · Pods (3)`, with the matched value in monospace, the field it came from (`Pod IP`, `Cluster IP`, `LB ingress IP`, `Pod CIDR`), and the cluster when you have more than one connected. Exact matches come first, then objects whose CIDR or range contains the address (`contains`), objects inside your CIDR or range (`in range`), partial overlaps (`overlaps`) and prefix matches. A section shows a few hits; **Show all N** opens the full list filtered by that address. Name matches for the same text are listed after the IP hits.

What is searched. The first group of kinds is always watched already. The pools, ranges and CRD kinds from ServiceCIDRs down are read only while you search: a full address, CIDR or range (or a prefix with two complete parts such as `10.42.`) starts watching the ones your clusters have and keeps them for a few minutes. Typing a short prefix such as `10.` never starts anything. Kinds a cluster does not have are skipped.

| Kind                                          | Addresses                                                                                                                                             |
| --------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------- |
| Pods                                          | pod IPs; secondary network IPs from Multus (`k8s.v1.cni.cncf.io/network-status`)                                                                      |
| Services                                      | cluster IPs, external IPs, requested load balancer IPs (`loadBalancerIP` and the MetalLB, kube-vip and Cilium annotations), load balancer ingress IPs |
| EndpointSlices                                | endpoint addresses (the hit shows the Service)                                                                                                        |
| Nodes                                         | internal and external IPs, pod CIDRs, CNI node addresses (Calico, Flannel, Cilium, Kube-OVN annotations)                                              |
| Ingresses                                     | load balancer ingress IPs                                                                                                                             |
| Gateways                                      | `status.addresses` and `spec.addresses` that are IP addresses                                                                                         |
| ServiceCIDRs                                  | `spec.cidrs`                                                                                                                                          |
| MetalLB IPAddressPools, BGPPeers              | pool addresses (CIDRs and ranges, IPv4 and IPv6); the peer address and source address                                                                 |
| Multus NetworkAttachmentDefinitions           | the IPAM ranges, static addresses and gateways of `spec.config` (host-local, static, whereabouts, conflists)                                          |
| Cilium LoadBalancerIPPools, PodIPPools, Nodes | pool blocks and CIDRs; a node's Cilium internal IP, pod CIDRs and allocated pool CIDRs                                                                |
| Calico IPPools                                | `spec.cidr`                                                                                                                                           |

A node's own address also finds its pods only when they run on the host network; the node row stands in for the host IP.

The same text works in list filters. On the Pods, Services, EndpointSlices, Nodes and Ingresses pages, "Filter by name or IP" matches names or addresses, and `:po 10.42.` opens Pods filtered that way. `:ip 10.0.0.5` opens the palette with the search filled in. Pods also have an **IP** column that sorts numerically. The first strong search on a cluster may say it is still loading some sources and refresh itself within a few seconds; if the cluster is at its watch limit it says which sources it could not read.

## Starred types and bookmarks

- **Star a resource type** from its page header, the sidebar, or with `:star`. Starred types appear at the top of the sidebar, in your order, with live counts for the ones already being watched. Any type can be starred, including CRDs.
- **Bookmark a view.** Use the bookmark button next to **Bookmarks** in the sidebar to save the current page, including a filter or an open object, under a name. Click a bookmark to return to it.

Stars, bookmarks, groups, and column layouts are saved in your settings, so they survive restarts. In hosted mode they are saved per user on the server, so they follow you between devices.

## What the browser remembers

Three things are remembered per browser, in its local storage, with nothing to set up:

- **The cluster selection.** See [Choose clusters](/guide/clusters#choose-clusters). It is pre-ticked on the next launch, not connected.
- **The last page.** Opening the app at its root address (`/`) takes you to the page you were last on, including its query (a filter in the URL, for instance). The open object in the drawer is not restored. Only the first load does this: going to `/` from inside the app still lands on the default page (Pods, or Problems on a phone), and Overview is always one click away in the sidebar.
- **Namespace filters.** Each set of connected clusters has its own namespace filters, for every resource type. Switch to another set and its filters load (none if you haven't used it); switch back and yours return. A set that is exactly a group is remembered as that group, so it keeps its filters when the group's membership changes. The 50 most recently used sets are kept.

If the browser blocks local storage, none of this is saved and v8s works as it did before.

## Deep links

The address always reflects what you are looking at. An open object adds the cluster, namespace, name, and tab to the URL, so a drawer is linkable:

```text
/r/apps/v1/deployments?c=prod-eu&ns=payments&n=api&tab=yaml
```

Copy the URL from the browser, or use the one in your bookmark. In hosted mode, anyone who can sign in and has access to that cluster can open it.

## Themes

v8s follows your system theme and lets you pick light or dark from the user menu.
