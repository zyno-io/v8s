import type { Kind } from '@v8s/client';

export type SectionId = 'cluster' | 'workloads' | 'network' | 'config' | 'storage' | 'access' | 'more';

export interface SidebarItem {
    gvr: string;
    label: string;
}

export interface SidebarSection {
    id: SectionId;
    title: string;
    items: SidebarItem[];
}

export interface CrdGroup {
    group: string;
    items: SidebarItem[];
}

export interface SidebarModel {
    starred: SidebarItem[];
    sections: SidebarSection[];
    crds: CrdGroup[];
}

const CLUSTER = ['v1/nodes', 'v1/namespaces', 'v1/events', 'events.k8s.io/v1/events'];
const WORKLOADS = [
    'v1/pods',
    'apps/v1/deployments',
    'apps/v1/statefulsets',
    'apps/v1/daemonsets',
    'apps/v1/replicasets',
    'batch/v1/jobs',
    'batch/v1/cronjobs'
];
const NETWORK = [
    'v1/services',
    'networking.k8s.io/v1/ingresses',
    'discovery.k8s.io/v1/endpointslices',
    'v1/endpoints',
    'networking.k8s.io/v1/networkpolicies',
    'networking.k8s.io/v1/ingressclasses'
];
const CONFIG = [
    'v1/configmaps',
    'v1/secrets',
    'v1/resourcequotas',
    'v1/limitranges',
    'autoscaling/v2/horizontalpodautoscalers',
    'policy/v1/poddisruptionbudgets',
    'scheduling.k8s.io/v1/priorityclasses'
];
const STORAGE = [
    'v1/persistentvolumeclaims',
    'v1/persistentvolumes',
    'storage.k8s.io/v1/storageclasses',
    'storage.k8s.io/v1/volumeattachments',
    'storage.k8s.io/v1/csidrivers',
    'storage.k8s.io/v1/csinodes'
];
const ACCESS_EXTRA = ['v1/serviceaccounts'];

/** API groups whose kinds are built in; everything else is a custom resource. */
const BUILTIN_GROUPS = new Set([
    '',
    'apps',
    'batch',
    'autoscaling',
    'policy',
    'networking.k8s.io',
    'rbac.authorization.k8s.io',
    'storage.k8s.io',
    'discovery.k8s.io',
    'events.k8s.io',
    'coordination.k8s.io',
    'scheduling.k8s.io',
    'node.k8s.io',
    'admissionregistration.k8s.io',
    'apiextensions.k8s.io',
    'apiregistration.k8s.io',
    'certificates.k8s.io',
    'flowcontrol.apiserver.k8s.io',
    'metrics.k8s.io',
    'authentication.k8s.io',
    'authorization.k8s.io',
    'resource.k8s.io',
    'internal.apiserver.k8s.io'
]);

export function sectionFor(k: Pick<Kind, 'gvr' | 'group'>): SectionId | 'crd' {
    if (CLUSTER.includes(k.gvr)) return 'cluster';
    if (WORKLOADS.includes(k.gvr)) return 'workloads';
    if (NETWORK.includes(k.gvr)) return 'network';
    if (CONFIG.includes(k.gvr)) return 'config';
    if (STORAGE.includes(k.gvr)) return 'storage';
    if (k.group === 'rbac.authorization.k8s.io' || ACCESS_EXTRA.includes(k.gvr)) return 'access';
    return BUILTIN_GROUPS.has(k.group) ? 'more' : 'crd';
}

const TITLES: Record<SectionId, string> = {
    cluster: 'Cluster',
    workloads: 'Workloads',
    network: 'Network',
    config: 'Config',
    storage: 'Storage',
    access: 'Access',
    more: 'More'
};

const ORDER: Record<SectionId, string[]> = {
    cluster: CLUSTER,
    workloads: WORKLOADS,
    network: NETWORK,
    config: CONFIG,
    storage: STORAGE,
    access: ACCESS_EXTRA,
    more: []
};

/** `deployments` → `Deployments`; the sidebar and page titles use the plural. */
export function kindLabel(k: Pick<Kind, 'plural' | 'kind'>): string {
    const p = k.plural || k.kind;
    return p.charAt(0).toUpperCase() + p.slice(1);
}

/** Best-effort label for a gvr whose kind is not (yet) in the list. */
export function labelFromGvr(gvr: string): string {
    const plural = gvr.slice(gvr.lastIndexOf('/') + 1);
    return plural.charAt(0).toUpperCase() + plural.slice(1);
}

function byOrder(order: string[]) {
    return (a: Kind, b: Kind) => {
        const ia = order.indexOf(a.gvr);
        const ib = order.indexOf(b.gvr);
        if (ia !== ib) return (ia < 0 ? 999 : ia) - (ib < 0 ? 999 : ib);
        return a.plural.localeCompare(b.plural);
    };
}

/**
 * Sidebar model from `kinds.list`: Starred first, then fixed sections, then custom resources by API group. Only kinds the
 * selected clusters can list are shown; a starred gvr that is not available is still listed so it can be unstarred.
 */
export function buildSidebar(kinds: readonly Kind[], stars: readonly string[]): SidebarModel {
    const avail = kinds.filter(k => k.available);
    const byGvr = new Map(kinds.map(k => [k.gvr, k]));
    const starred = stars.map(gvr => {
        const k = byGvr.get(gvr);
        return { gvr, label: k ? kindLabel(k) : labelFromGvr(gvr) };
    });

    const buckets = new Map<SectionId, Kind[]>();
    const crdBuckets = new Map<string, Kind[]>();
    for (const k of avail) {
        const s = sectionFor(k);
        if (s === 'crd') {
            const g = crdBuckets.get(k.group) ?? [];
            g.push(k);
            crdBuckets.set(k.group, g);
        } else {
            const g = buckets.get(s) ?? [];
            g.push(k);
            buckets.set(s, g);
        }
    }
    const sections: SidebarSection[] = [];
    for (const id of Object.keys(TITLES) as SectionId[]) {
        const list = buckets.get(id);
        if (!list?.length) continue;
        sections.push({ id, title: TITLES[id], items: [...list].sort(byOrder(ORDER[id])).map(k => ({ gvr: k.gvr, label: kindLabel(k) })) });
    }
    const crds: CrdGroup[] = [...crdBuckets.entries()]
        .sort((a, b) => a[0].localeCompare(b[0]))
        .map(([group, list]) => ({
            group,
            items: [...list].sort((a, b) => a.plural.localeCompare(b.plural)).map(k => ({ gvr: k.gvr, label: kindLabel(k) }))
        }));
    return { starred, sections, crds };
}
