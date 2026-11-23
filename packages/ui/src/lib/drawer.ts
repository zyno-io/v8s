import type { ObjectRef } from '@v8s/client';
import type { Router } from 'vue-router';

import { shallowRef } from 'vue';

import type { DrawerTab } from './route.ts';

import { carriedTab, drawerQuery, gvrFromRoute, gvrPath } from './route.ts';

/** Set by the `e` shortcut: the YAML pane starts editing as soon as it shows (and clears it). */
export const editRequest = shallowRef(false);

/**
 * Opens an object in the drawer; the URL carries it (`?c=…&ns=…&n=…`). Pushes when opening, replaces when only the tab changes.
 * Without a `tab`, an open drawer keeps its current tab when the new object has it (clicking from pod to pod stays on Logs).
 */
export function openObject(router: Router, ref: ObjectRef, tab?: DrawerTab, replace = false): Promise<unknown> {
    const cur = router.currentRoute.value;
    const here = gvrFromRoute(cur.path, cur.params);
    const loc = { path: here === ref.gvr ? cur.path : gvrPath(ref.gvr), query: drawerQuery(ref, tab ?? carriedTab(cur.query, ref.gvr)) };
    return replace ? router.replace(loc) : router.push(loc);
}

export function closeDrawer(router: Router): Promise<unknown> | void {
    const cur = router.currentRoute.value;
    if (!cur.query.c && !cur.query.n) return;
    const { c: _c, n: _n, ns: _ns, tab: _tab, ...rest } = cur.query;
    return router.replace({ path: cur.path, query: rest });
}

export function isDrawerOpen(router: Router): boolean {
    const q = router.currentRoute.value.query;
    return !!(q.c && q.n);
}
