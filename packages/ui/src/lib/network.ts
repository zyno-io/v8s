import type { DropGroup, DropPolicy, HubbleStatus, Kind, ObjectRef, ViewHandle } from '@v8s/client';

// Pure helpers behind the Drops view and the Hubble status panel.

// ---- windows ----

export interface WindowOption {
    sec: number;
    label: string;
}
/** The agent caps every window at 3600 s, so 6 h is not offered. */
export const DROP_WINDOWS: WindowOption[] = [
    { sec: 300, label: '5 minutes' },
    { sec: 3600, label: '1 hour' }
];
export const DEFAULT_DROP_WINDOW = 3600;
export const MAP_WINDOWS: WindowOption[] = [
    { sec: 300, label: '5 minutes' },
    { sec: 900, label: '15 minutes' },
    { sec: 3600, label: '1 hour' }
];
export const DEFAULT_MAP_WINDOW = 300;
export const DROPS_REFRESH_MS = 30_000;

// ---- drops ----

export type DropSort = 'count' | 'lastSeen';

export const workloadText = (w: { namespace?: string; workload: string }): string => (w.namespace ? `${w.namespace}/${w.workload}` : w.workload);

/** Stable identity of a group (selection survives a refresh). */
export const dropKey = (g: DropGroup): string => `${g.cluster}|${workloadText(g.src)}|${workloadText(g.dst)}|${g.reason}`;

export function filterDrops(groups: readonly DropGroup[], text: string): DropGroup[] {
    const q = text.trim().toLowerCase();
    if (!q) return [...groups];
    const terms = q.split(/\s+/);
    return groups.filter(g => {
        const hay = [g.cluster, workloadText(g.src), workloadText(g.dst), g.reason, ...g.policies.map(p => p.name)].join(' ').toLowerCase();
        return terms.every(t => hay.includes(t));
    });
}

/** Sorted copy: count desc (ties: newest first) or newest first (ties: count desc); the key breaks remaining ties. */
export function sortDrops(groups: readonly DropGroup[], by: DropSort): DropGroup[] {
    return [...groups].sort((a, b) => {
        const d = by === 'count' ? b.count - a.count || b.lastSeen - a.lastSeen : b.lastSeen - a.lastSeen || b.count - a.count;
        return d || dropKey(a).localeCompare(dropKey(b));
    });
}

export const totalDrops = (groups: readonly DropGroup[]): number => groups.reduce((n, g) => n + g.count, 0);

// ---- policies ----

export const POLICY_GVRS: Record<string, string> = {
    NetworkPolicy: 'networking.k8s.io/v1/networkpolicies',
    CiliumNetworkPolicy: 'cilium.io/v2/ciliumnetworkpolicies',
    CiliumClusterwideNetworkPolicy: 'cilium.io/v2/ciliumclusterwidenetworkpolicies'
};

/** The object ref of a policy, or null when its kind is unknown or the cluster does not serve it. */
export function policyRef(cluster: string, p: DropPolicy, kinds: readonly Pick<Kind, 'gvr' | 'clusters'>[]): ObjectRef | null {
    const gvr = POLICY_GVRS[p.kind];
    if (!gvr) return null;
    const k = kinds.find(x => x.gvr === gvr);
    if (!k || (k.clusters.length && !k.clusters.includes(cluster))) return null;
    return { cluster, gvr, namespace: p.namespace || undefined, name: p.name };
}

export const WORKLOAD_GVR = 'apps/v1/deployments';

/** The source (or any) workload as a deployment ref; null when the workload is a reserved identity with no namespace. */
export function workloadRef(cluster: string, w: { namespace?: string; workload: string }): ObjectRef | null {
    if (!w.namespace || !w.workload) return null;
    return { cluster, gvr: WORKLOAD_GVR, namespace: w.namespace, name: w.workload };
}

export const dropNote = (e: { cluster: string; error: string }, name: string): string => `No reachable relay in ${name}: ${e.error}`;

// ---- namespaces of a cluster ----

/** Sorted namespace names of one cluster from the `v1/namespaces` view (reads its columns, not another dataset). */
export function clusterNamespaces(handle: ViewHandle | null, cluster: string): string[] {
    const cols = handle?.columns.value;
    if (!handle || !cols) return [];
    const ni = cols.findIndex(c => c.key === 'name');
    const ci = cols.findIndex(c => c.key === 'cluster');
    if (ni < 0) return [];
    const set = new Set<string>();
    for (const row of handle.rows.values()) {
        if (ci >= 0 && row[ci + 2] !== cluster) continue;
        const v = row[ni + 2];
        if (typeof v === 'string' && v) set.add(v);
    }
    return [...set].sort();
}

// ---- status ----

export const fillPct = (s: Pick<HubbleStatus, 'numFlows' | 'maxFlows'>): number =>
    s.maxFlows > 0 ? Math.min(100, Math.round((s.numFlows / s.maxFlows) * 100)) : 0;

/** Flows the relay can hold per node below which a busy cluster loses history quickly. */
export const SMALL_BUFFER_PER_NODE = 4096;

/** "Why are there no flows?" hints for an available relay. */
export function statusHints(s: HubbleStatus): string[] {
    const out: string[] = [];
    if (s.unavailableNodes > 0)
        out.push(
            `${s.unavailableNodes} node${s.unavailableNodes === 1 ? ' is' : 's are'} unavailable: flows from those nodes are missing. Check the Cilium agent pods on them.`
        );
    const nodes = s.connectedNodes + s.unavailableNodes;
    if (s.maxFlows > 0 && nodes > 0 && s.maxFlows / nodes < SMALL_BUFFER_PER_NODE)
        out.push(
            `The flow buffer is small (${Math.round(s.maxFlows / nodes)} flows per node). Raise hubble.eventBufferCapacity in the Cilium config to keep more history.`
        );
    if (s.connectedNodes > 0 && s.seenFlows === 0)
        out.push('The relay has seen no flows yet. Check that Hubble is enabled in the Cilium agents and traffic is flowing.');
    return out;
}

export function formatUptime(sec: number): string {
    if (!sec || sec < 0) return '0s';
    const d = Math.floor(sec / 86400);
    const h = Math.floor((sec % 86400) / 3600);
    const m = Math.floor((sec % 3600) / 60);
    if (d) return `${d}d ${h}h`;
    if (h) return `${h}h ${m}m`;
    if (m) return `${m}m ${Math.floor(sec % 60)}s`;
    return `${Math.floor(sec)}s`;
}

export function nodeTone(state: string): 'success' | 'danger' | 'warning' {
    return state === 'connected' ? 'success' : state === 'unavailable' ? 'danger' : 'warning';
}
