# Navigation

v8s is keyboard-first. Almost everything reachable with the mouse is also reachable from the command bar or a shortcut.

## Command bar

Type `:` anywhere to open the command bar, modelled on k9s. It completes as you type.

| Command               | Effect                                                |
| --------------------- | ----------------------------------------------------- |
| `:ctx`                | Choose clusters or a group                            |
| `:<type> [namespace]` | Open a resource type, for example `:pods kube-system` |
| `:ns`                 | Choose a namespace                                    |

The design is described in [Design system](/design-system#command-bar-k9s-style).

## Search

Press `/` or `Cmd+K` (`Ctrl+K` on Windows and Linux) to search objects by name across the selected clusters. The same palette runs actions.

## Lists

- Click a column header to sort, and type in the filter box to narrow rows instantly.
- Rows are virtualized, so scrolling stays smooth on tens of thousands of objects.
- Star resource types you use often to pin them to the sidebar, and bookmark individual objects.

## Details

Opening a row shows its status, conditions, events, and related objects in a detail view. On a phone this is a full-screen page.

## Themes

v8s follows your system theme and offers light and dark from the user menu.
