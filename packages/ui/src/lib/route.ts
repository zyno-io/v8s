import type { ObjectRef } from '@v8s/client';
import type { LocationQuery, RouteLocationRaw } from 'vue-router';

export const PODS_GVR = 'v1/pods';

/** `/pods` for pods, `/r/<gvr>` for everything else. */
export function gvrPath(gvr: string): string {
    return gvr === PODS_GVR ? '/pods' : '/r/' + gvr;
}

/** Inverse of `gvrPath` for a route's path and params. */
export function gvrFromRoute(path: string, params: Record<string, unknown>): string | null {
    if (path === '/pods') return PODS_GVR;
    const g = params.gvr;
    if (typeof g === 'string' && g) return g;
    if (Array.isArray(g) && g.length) return g.join('/');
    return null;
}

export type DrawerTab = 'overview' | 'logs' | 'shell' | 'inventory' | 'release' | 'yaml' | 'events' | 'related';
export const DRAWER_TABS: DrawerTab[] = ['overview', 'logs', 'shell', 'inventory', 'release', 'yaml', 'events', 'related'];

const LOG_GVRS = new Set([
    'v1/pods',
    'apps/v1/deployments',
    'apps/v1/statefulsets',
    'apps/v1/daemonsets',
    'apps/v1/replicasets',
    'batch/v1/jobs',
    'v1/services'
]);

export const HELM_RELEASES_GVR = 'helm.sh/v1/releases';

/** The tabs a kind's drawer offers, in order: Logs and Shell for pods, Logs for workloads, Inventory for Kustomizations, Release for Flux HelmReleases. */
export function tabsFor(gvr: string): DrawerTab[] {
    const out: DrawerTab[] = ['overview'];
    if (LOG_GVRS.has(gvr)) out.push('logs');
    if (gvr === PODS_GVR) out.push('shell');
    if (/^kustomize\.toolkit\.fluxcd\.io\/[^/]+\/kustomizations$/.test(gvr)) out.push('inventory');
    if (/^helm\.toolkit\.fluxcd\.io\/[^/]+\/helmreleases$/.test(gvr)) out.push('release');
    out.push('yaml', 'events', 'related');
    return out;
}

export interface DrawerTarget {
    ref: ObjectRef;
    tab: DrawerTab;
}

/** The URL reflects the open object: `?c=<cluster>&ns=<namespace>&n=<name>&tab=yaml`. */
export function drawerQuery(ref: Pick<ObjectRef, 'cluster' | 'namespace' | 'name'>, tab?: DrawerTab): Record<string, string> {
    const q: Record<string, string> = { c: ref.cluster, n: ref.name ?? '' };
    if (ref.namespace) q.ns = ref.namespace;
    if (tab && tab !== 'overview') q.tab = tab;
    return q;
}

const first = (v: LocationQuery[string]): string => (Array.isArray(v) ? (v[0] ?? '') : (v ?? '')) || '';

export function parseDrawer(gvr: string | null, query: LocationQuery): DrawerTarget | null {
    if (!gvr) return null;
    const cluster = first(query.c);
    const name = first(query.n);
    if (!cluster || !name) return null;
    const tab = first(query.tab) as DrawerTab;
    return {
        ref: { cluster, gvr, namespace: first(query.ns) || undefined, name },
        tab: DRAWER_TABS.includes(tab) && tabsFor(gvr).includes(tab) ? tab : 'overview'
    };
}

export function objectLocation(ref: ObjectRef, tab?: DrawerTab): RouteLocationRaw {
    return { path: gvrPath(ref.gvr), query: drawerQuery(ref, tab) };
}
