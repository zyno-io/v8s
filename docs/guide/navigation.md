# Navigation

v8s is keyboard-first. Almost everything you can reach with the mouse is also reachable from the command bar, the search palette, or a shortcut. Press `?` at any time to see the list of shortcuts.

## Layout

- **Top bar.** The cluster selector, search, the problems badge, and the theme and user menu.
- **Sidebar.** Resource types grouped as Starred, Overview, Problems, Workloads, Network, Config, Storage, Access, then integration sections (Flux, Helm, Security, Network flows) and Custom resources. Integration sections appear only when the integration is detected in at least one selected cluster. Custom resource groups are discovered from each cluster's CRDs and expand on demand.
- **Table.** The current resource type across all selected clusters.
- **Detail drawer.** Opens on the right when you select a row. See [Viewing and changing resources](/guide/resources).

Below 900 px wide the sidebar becomes a bottom sheet, and below 640 px tables become cards. See [On a phone](/guide/mobile).

## Tables

Every resource list is the same component, so the controls work everywhere.

- **Sort.** Click a column header. Shift-click to add a secondary sort key.
- **Filter.** Type in the filter box (or press `/`). It matches names and answers instantly, even over tens of thousands of rows.
- **Tone chips.** Where a type has a status, chips such as success, warning, and danger appear with live counts. Toggle them to show only broken or only healthy rows. Status is always a word, never colour alone: `CrashLoopBackOff`, `Running`, `Suspended`.
- **Columns.** Use **Columns** to hide or show columns, and drag column edges to resize. Your layout is remembered per resource type.
- **Cluster column.** The cluster column (a short tag with the cluster name) appears only when more than one cluster is selected.
- **Namespace filter.** Set it from the namespace menu or with `:ns`.
- **Selection.** Select rows with `x` or the checkboxes. A bulk action bar appears at the bottom with the actions that apply to every selected row, such as Restart or Delete.
- **Syncing.** While a cluster is still loading, its rows stream in and the toolbar shows a progress tag such as `syncing prod-eu · 4,210`. There is no full-table spinner.

## Keyboard shortcuts

| Key                | Action                                                      |
| ------------------ | ----------------------------------------------------------- |
| `:`                | Open the command bar                                        |
| `/`                | Focus the filter                                            |
| `j` / `k`          | Move the focus down / up (arrow keys also work)             |
| `Enter`            | Open the focused row in the drawer                          |
| `x`                | Select or deselect the focused row                          |
| `Esc`              | Clear the filter, then the selection, then close the drawer |
| `Ctrl+K` / `Cmd+K` | Open the search palette                                     |
| `?`                | Show the shortcut list                                      |

`Esc` works in order, so repeated presses unwind your view one layer at a time. Inside the YAML editor, `Esc` only leaves the editor, so it can never close the drawer over unsaved edits. v8s avoids `Ctrl+letter` shortcuts, so your browser and OS keep theirs.

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

| Command                 | Effect                                                         |
| ----------------------- | -------------------------------------------------------------- |
| `:po`                   | Open pods (all namespaces)                                     |
| `:po payments`          | Open pods in the `payments` namespace                          |
| `:deploy payments/api`  | Open the Deployment `api` in `payments`, in the drawer        |
| `:no worker-1`          | For cluster-scoped types, the argument is the object name      |
| `:hr`                   | Open Flux HelmReleases                                         |

### Other commands

| Command                    | Effect                                                    |
| -------------------------- | --------------------------------------------------------- |
| `:ns payments`             | Filter the current view to a namespace                    |
| `:ns all`                  | Clear the namespace filter                                |
| `:pb` or `:problems`       | Open the [Problems view](/guide/problems)                 |
| `:star` / `:unstar`        | Star or unstar the current resource type                  |
| `:q`                       | Close the bar (`Esc` does the same)                       |

Aliases: `:context` for `:ctx`, `:namespace` for `:ns`.

## Search palette

Press `Ctrl+K` (`Cmd+K` on macOS) to search across everything. It finds objects by name across the selected clusters (searched in the agent, so it is fast on big clusters), resource types, and commands. Pick a result to open it.

## Starred types and bookmarks

- **Star a resource type** from its page header, the sidebar, or with `:star`. Starred types appear at the top of the sidebar, in your order, with live counts for the ones already being watched. Any type can be starred, including CRDs.
- **Bookmark a view.** Use the bookmark button next to **Bookmarks** in the sidebar to save the current page, including a filter or an open object, under a name. Click a bookmark to return to it.

Stars, bookmarks, groups, and column layouts are saved in your settings, so they survive restarts. In hosted mode they are saved per user on the server, so they follow you between devices.

## Deep links

The address always reflects what you are looking at. An open object adds the cluster, namespace, name, and tab to the URL, so a drawer is linkable:

```text
/r/apps/v1/deployments?c=prod-eu&ns=payments&n=api&tab=yaml
```

Copy the URL from the browser, or use the one in your bookmark. In hosted mode, anyone who can sign in and has access to that cluster can open it. The desktop app also registers `v8s://` links.

## Themes

v8s follows your system theme and lets you pick light or dark from the user menu.
