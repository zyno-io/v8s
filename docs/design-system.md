# Design system

v8s defines its own design system. It draws on two separate Signal24 products for their UI elements, not for a shared system:

- **PixelCI** for the visual foundation: its `DESIGN.md` tokens (colour, type, radius, shadow) and rules, which v8s adopts as its starting point.
- **The CI Dashboard** for interaction patterns that fit v8s's shape: a dense, filterable, live-updating status table with tone filter chips, per-row dismiss, cards on narrow screens, and installability.

This chapter says what v8s takes from PixelCI, what it adds, and which rules it bends because a Kubernetes manager is denser than a review desk.

## Take from PixelCI

- **Tokens.** Every colour, radius, shadow, and type token, in light and dark, under the same custom-property names. Components never branch on theme. Theme follows the OS unless the user picks one.
- **Type.** Inter with `cv11`, `ss01`, `tnum`; the 11–20px ramp; weight before size.
- **The One Indigo Rule.** Indigo means action, selection, or focus. It never carries status.
- **The Tag Triplet Rule.** Status is a small tag: soft fill, matching border, tone text, and always a word.
- **The Machine Text Rule.** Machine-produced text a person may copy is monospace: UIDs, image refs and digests, IPs, resource versions, revisions, commit SHAs, YAML.
- **The Shadow Means Detached Rule** and **The Nested Radius Rule** (12 › 8 › 5).
- **Controls** share one 32px height so a filter row of search, selects, segmented controls, and buttons aligns.
- **Selects** are `SmartSelect`, never a native `<select>`: a 32px trigger (26px `sm`) that opens a floating list in the top layer (above dialogs, immune to `contain:`/`container-type:` ancestors), flipping above the trigger near the viewport bottom and becoming a bottom sheet under 640px. From 8 options it has a search field; keys are arrows, Home/End, PageUp/PageDown, type-ahead, Enter, Esc (focus returns to the trigger). Only the visible rows are in the DOM, rows are 44px on coarse pointers, and nothing is computed while closed. Option text is always rendered as text. Vendored from `@signal24/vue-foundation` (MIT) because the package bundles lodash, mark.js and a global stylesheet.
- **Danger buttons** are outlined with Destructive Red text and always lead to a confirm dialog.
- **Icons**: Font Awesome Free.

## Token delivery

Copy the token block from PixelCI into `packages/ui/src/styles/tokens.scss` in P0 and own it there; v8s's tokens evolve independently. `@zyno-io/vue-foundation` stays designless and supplies behaviour only.

## Status mapping

Kubernetes has many status vocabularies. v8s collapses them onto the six tones and keeps the source word in the tag, so colour is never the only signal and nothing is renamed.

| Tone | Meaning in v8s | Examples (tag text is the source word) |
| --- | --- | --- |
| `success` | Healthy, converged | `Running` + all ready, `Ready`, `Bound`, `Active`, `deployed`, Flux `Ready=True`, `Succeeded` Job, `Complete` |
| `warning` | Transitional, degraded but moving, or needs a look | `Pending`, `ContainerCreating`, `Progressing`, `Reconciling`, `pending-install`/`pending-upgrade`, partially ready (`2/3`), `SchedulingDisabled`, medium vulns |
| `danger` | Broken, failing, needs action | `CrashLoopBackOff`, `ImagePullBackOff`, `OOMKilled`, `Failed`, `Error`, `NotReady`, Helm `failed`, Flux `Ready=False`, Kyverno `fail`, critical/high vulns, Hubble `DROPPED` |
| `info` | Informational, finished, or neutral-positive | `Completed` pod, `Succeeded` pod, `superseded` Helm revision, `pass` policy result |
| `release` | Newness or a version event | a new revision since last visit, `pending-rollback`, image update available (Flux image automation) |
| `neutral` | Off, paused, unknown | `Suspended`, `Terminating`, `Unknown`, `uninstalled`, `skip`, cordoned-but-ready, low/negligible vulns, kind tags, namespace tags |

Problem severity (see [problems.md](problems.md)) maps `critical`/`error` → danger, `warning` → warning, `info` → info. Cordoned nodes wear a neutral `Cordoned` tag next to their health tag, not instead of it.

## Extensions v8s needs

PixelCI is a review desk: a 1120px column of cards and rows. v8s is closer in shape to the CI Dashboard: one big live table. These additions keep PixelCI's vocabulary and add density.

### App frame

- **Top bar** (46px, translucent, blurred), unchanged. It holds the brand mark, the **cluster selector**, global search (`/` or `⌘K`), the problems badge, and the theme/user menu.
- **Resource sidebar** (new; 232px, collapsible to 52px icons). Sections: *Starred* (user-pinned resource types, drag to reorder), *Overview*, *Problems*, *Workloads*, *Network*, *Config*, *Storage*, *Access*, *Flux*, *Helm*, *Security* (Kyverno, Trivy, Kubescape), *Network flows* (Hubble), *Custom resources* (discovered CRD groups, lazily expanded). Nav items use the existing `nav-link` treatment (Inset Grey hover, Screenshot Mat active). Integration sections appear only when the integration is detected in at least one selected cluster.
- **Full-width pages.** List pages break the 1120px container, as PixelCI's review screen does. The rule becomes **The Table Gets the Width Rule**: chrome never takes horizontal space from a resource table. Detail pages and forms keep the 1120px container.

### Cluster selector

A top-bar button showing the selected count ("3 clusters") or the single cluster name. It opens a floating menu:

- Group chips at the top (`production`, `staging`, user-defined). Clicking a chip selects exactly that group; shift-click adds it.
- A searchable checklist of clusters with a connection-state dot (connected, connecting, error, disconnected) and the group tags. A cluster with a linked write context shows a small `write` tag (warning tone) while that context is in use. While the cluster is elevated the tag reads `elevated` instead. A cluster that can be elevated carries a small chevron menu (10 px, 44 px hit area on touch) beside its `synced` status chip above a table and beside the phase tag on its Overview card; the menu has one item, Elevate or Revert, with a muted hint line. While elevated, the cluster's status chip itself switches to the warning tone and names the write context it runs as (no separate chip); the Overview card shows an `elevated` tag (warning tone).
- "Apply" commits the selection. Checking boxes connects nothing until Apply, so a fat-fingered "select all" on 40 clusters does not open 40 connections. On launch, the remembered selection appears pre-checked with a "Connect" button (or reconnects automatically if the user turned that on).

Each cluster gets a stable **cluster tag**: neutral tone, the cluster's short name, and an optional user-chosen accent dot (6 hues tuned to sit beside, not clash with, status tones). In multi-cluster tables the cluster tag is the first column.

### Dense data table

The core component. It is not built from PixelCI's `list-row` (14px × 18px padding, ~48px rows), which would show 15 rows on a laptop.

- **Row height 32px fixed** (body-sm 13px text, 6px × 12px cell padding). A "comfortable" density at 40px (the CI Dashboard's table row height) is a user setting. Fixed heights are a performance requirement, not taste: see [performance.md §UI rendering](performance.md#7-ui-rendering).
- **Header** 32px, label-caps style (11px, 600, 0.045em, uppercase, Meta Grey), sticky. Sort indicator on the active column; click sorts, shift-click adds a secondary key.
- **Columns** are resizable and hideable per resource type, remembered per user. Name column first after cluster; name is `card-title` weight (600) at 13px. Age and timestamps are relative with `tnum`.
- **Usage block** (pods and nodes): the visible usage columns (CPU, MEM, their percentages, requests and limits) draw as one block in the k9s manner: right-aligned, unit-less integers at 13px with `tnum`, sub-columns of 44 px (millicores), 48 px (MiB) and 36 px (a percentage), a 6 px gap inside CPU and inside MEM and 14 px between them (274 px for the default block). The unit is a muted, 10px, non-uppercase suffix in the header (`CPU m`, `MEM Mi`) and the sort arrow replaces it, so nothing reflows. A header says `%R`/`%L` when the block shows that resource's raw column and `%CPU/L` otherwise. No icons. Each percentage sub-cell carries a background bar from the bottom of the row, its height the percentage (capped at a full row): a flat translucent fill (no top edge, which would strike through the digits), with 3px of padding so the digits keep off its edge, so hover and selection show through, and the number on it in `--text2` (`--text` when hot). Limit and node percentages are green below 70 %, amber from 70 % and red from 90 % (`--bar-ok`/`--bar-warn`/`--bar-hot`). A request percentage has no bar when the row has a limit for that resource (the limit is the bound that matters); without a limit the request is the only bound and gets the same coloured bar. Next to a limit, a memory request percentage is tinted instead: `--warning` above 100 %, `--danger` above 200 % (on the shown, rounded value), because pods over their memory request are evicted first under node memory pressure; CPU request percentages stay `--text2`. Each percentage header's `title` adds a line on what its colours mean, and the row's `title` adds "over request: evicted first if the node runs short of memory" when memory is tinted. The bar is one CSS background sized by an inline `--p`, set only when the row re-renders: no pseudo-element, no layout, no transition. Raw columns stay plain text; no data (`—`) and n/a are `--text3` with no bar. The block is not resizable, and one `title` on it carries the details. On nodes the block is CPU, `%`, `alloc`, MEM, `%`, `alloc` (310 px: alloc 52 and 56 px), and the percentages are of the node's allocatable. The Overview capacity cell has the other bar: a 6 px `--border` track for the allocatable, a `--text2` fill for usage (`--warning` from 90 %, `--danger` from 100 %), and a 2 px `--text` tick at the requests share.
- **Row hover** Row Hover fill; **selected** rows (checkbox column) Accent Soft fill; **focused** row the inset 2px focus ring (The Inset Ring Rule).
- **Bulk action bar** appears at the bottom of the table, in the decision-bar style (Desk Grey strip, hairline top), when rows are selected: "3 selected · Delete · Restart · Suspend". One primary action at most.
- **Keyboard**: `j`/`k` move focus, `Enter` opens detail, `x` toggles selection, `/` focuses filter, `e` edit YAML, `l` logs, `s` shell, `d` describe. Shown in a legend like PixelCI's shortcut legend.
- **Empty, loading, partial** states: an empty filtered result says what filter hid what. While a cluster is still syncing, its rows stream in and the toolbar shows "syncing prod-eu · 4,210 rows" (desktop) or "syncing prod-eu · 35%" (hosted, where totals are never unfiltered). Never a full-table spinner.

### Command bar (k9s-style)

Typing `:` anywhere opens a one-line command bar at the top of the content (Floating shadow, mono input, completion list below). It is the keyboard-first way around v8s, modelled on k9s:

| Command | Effect |
| --- | --- |
| `:ctx` | open the cluster selector |
| `:ctx <name> [<name>…]` | select exactly these clusters (names or groups; completes from contexts and groups) |
| `:ctx +<name>` / `:ctx -<name>` | add or remove one cluster from the selection |
| `:<type>` | open a resource type by plural, kind, or short name (`:pods`, `:po`, `:deploy`, `:svc`, `:hr`, `:ks`); CRDs complete from discovery |
| `:<type> <ns>` | open the type filtered to a namespace (`:po payments`) |
| `:<type> <ns>/<name>` | open one object's detail |
| `:ip <address\|cidr\|range>` | search objects by IP in the palette (`:ip 10.0.0.5`, `:ip 10.0.0.0/16`) |
| `:<type> <ip\|cidr>` | open the type filtered by address (`:po 10.42.`, `:svc 10.96.0.0/12`); kinds with IPs only |
| `:ns <ns>` / `:ns all` | set or clear the namespace filter on the current view |
| `:problems` (`:pb`) | open Problems |
| `:star` / `:unstar` | star or unstar the current type |
| `:q` / `Esc` | close the bar |

Also, outside the bar: `/` focuses the filter, `?` shows every shortcut, `Esc` clears filter then selection then closes the drawer, and `Ctrl-a`-style shortcuts are avoided so browser and OS shortcuts keep working. Completion is prefix-then-fuzzy over commands, kinds, namespaces (from the agent's per-cluster namespace index), clusters, and groups. Unknown commands say so inline; they never navigate somewhere unexpected.

### Filter bar

Lifted from the CI Dashboard's filter bar: a search field (max 360px) with a `/` key cap, then **tone filter chips**: a checkbox styled as a 28px chip with an icon and a live count, tooltip carrying the name and description, accessible label carrying both. The Problems view uses exactly this pattern for problem classes. Namespace filter is a multi-select with search.

### Detail drawer and page

Selecting a row opens a **detail drawer** (right side, 560px, resizable, Floating shadow) on wide screens, a full page on narrow ones. Tabs: Overview, YAML, Events, Logs (pods/workloads), Shell (pods), Flows (Hubble), Security (Trivy/Kyverno/Kubescape findings for this workload), Related (owners, owned, selected-by). The URL always reflects the open object, so a drawer is linkable.

### YAML editor and diff

CodeMirror 6, not Monaco: ~10× smaller bundle, no web-worker-per-language boot, and it virtualizes long documents natively. Mono 12px, theme from tokens. Edit flow: edit → "Review changes" shows a side-by-side diff against the live object (server-side dry run, see [operations.md](operations.md#editing)) → Apply (primary). Conflicts (resourceVersion changed) show a three-way notice, not a silent overwrite.

### Terminal and log viewer

- **Terminal**: xterm.js with the WebGL renderer on desktop, canvas/DOM fallback on mobile. Background is a fixed near-black in both themes (`#0c0d10`, the dark `bg`) with dark-theme text tokens; a terminal that flips to white in light mode reads as broken.
- **Log viewer**: a virtualized monospace list, not a terminal. Toolbar: container select, follow toggle, timestamps toggle, wrap toggle, search (highlights and jumps), previous-container toggle, download. Level-coloured gutters only when lines parse as structured logs (`level=error`, JSON `"level":"error"`) in danger/warning text tones; the line itself stays Ink.

### Problems view

A table like any other (cluster, severity, class, object, message, age, first seen), plus the CI Dashboard's per-row **dismiss** (acknowledge) control and "Show dismissed" toggle. Grouping by class or by cluster uses collapsible group header rows (32px, Inset Grey) inside the same virtualized list. See [problems.md](problems.md).

### Graphs

Two graph surfaces: Flux dependency/inventory trees and the Hubble service map. Both are drawn on `<canvas>` (or a single SVG for under ~200 nodes) with tokens read from computed styles, never as one Vue component per node. Edges are Control Outline; node boxes are Card White with a status tag. Dropped-flow edges are danger-toned and dashed so colour is not the only signal.

## Mobile layouts

Like the CI Dashboard's project table, v8s switches tables to cards on narrow screens. Breakpoints follow PixelCI (1120, 900, 640, 360). The first round is the phone's browser; installability comes later ([packaging.md](packaging.md#mobile-web)).

- **Below 900px** the sidebar becomes a bottom sheet behind a "Resources" button; the top bar keeps cluster selector, problems badge, and search.
- **Below 640px** tables become **fixed-height cards** (72px: name + status tag on line one, cluster/namespace/age caption on line two, a single key metric right-aligned). Still virtualized; still fixed height. Multi-select moves to long-press.
- **Touch targets** are at least 44px; the 32px controls get 44px hit areas via padding on coarse pointers (`@media (pointer: coarse)`).
- **Bottom navigation** on phones: Problems, Clusters, Search, Starred, More. Problems is the default landing tab on a phone.
- **Detail** is a full-screen page with a back button; tabs become a horizontal scroll strip.
- **Destructive actions** on a phone use a hold-to-confirm or type-the-name confirm, never a single tap.

## Brand

Name **v8s**, always lower case. Until there is a mark, use a small glyph in an inverted rounded square beside the text wordmark. No illustration; the cluster is the content.
