import type { FluxInventoryEntry } from '@v8s/client';

/** ColTone values (docs/design-system.md "Status mapping"). */
export const TONE_NEUTRAL = 0;
export const TONE_SUCCESS = 1;
export const TONE_WARNING = 2;
export const TONE_DANGER = 3;
export const TONE_INFO = 4;
export const TONE_RELEASE = 5;

/** Severity for sorting and roll-up: higher is worse. Indexed by tone. */
const RANK = [0, 1, 4, 5, 3, 2];
const rankOf = (tone: number): number => RANK[tone] ?? 0;

export interface InventoryOpts {
    /** True when the entry's kind is watched by the agent. Default: the agent resolved a gvr. */
    isWatched?: (e: FluxInventoryEntry) => boolean;
}

export interface EntryHealth {
    tone: number;
    status: string;
}

export interface InventoryNs {
    key: string;
    label: string;
    count: number;
    tone: number;
    status: string;
    entries: FluxInventoryEntry[];
}

export interface InventoryGroup {
    key: string;
    label: string;
    count: number;
    tone: number;
    status: string;
    namespaces: InventoryNs[];
}

export interface InventorySummary {
    total: number;
    failing: number;
    warning: number;
    healthy: number;
    missing: number;
    notWatched: number;
    other: number;
}

export interface InventoryTree {
    groups: InventoryGroup[];
    summary: InventorySummary;
    /** Every group and namespace key (for "expand all"). */
    branchKeys: string[];
}

export interface FlatRow {
    key: string;
    depth: 0 | 1 | 2;
    kind: 'group' | 'ns' | 'leaf';
    label: string;
    count: number;
    tone: number;
    status: string;
    expanded?: boolean;
    entry?: FluxInventoryEntry;
}

export function entryHealth(e: FluxInventoryEntry, opts?: InventoryOpts): EntryHealth {
    if (e.live) return { tone: e.tone, status: e.status ?? '' };
    const watched = opts?.isWatched ? opts.isWatched(e) : !!e.gvr;
    return watched ? { tone: TONE_WARNING, status: 'Not found' } : { tone: TONE_NEUTRAL, status: 'Not watched' };
}

export function kindLabel(e: FluxInventoryEntry): string {
    return e.group ? `${e.group}/${e.kind}` : e.kind;
}

interface Acc {
    rank: number;
    tone: number;
    status: string;
    danger: number;
    warning: number;
}

const newAcc = (): Acc => ({ rank: -1, tone: 0, status: '', danger: 0, warning: 0 });

function add(a: Acc, h: EntryHealth): void {
    const r = rankOf(h.tone);
    if (r > a.rank) {
        a.rank = r;
        a.tone = h.tone;
        a.status = h.status;
    }
    if (h.tone === TONE_DANGER) a.danger++;
    else if (h.tone === TONE_WARNING) a.warning++;
}

/** Roll-up status word for a group/namespace: the failing count, else the warning count, else the worst entry's word. */
function rollStatus(a: Acc): string {
    if (a.danger) return `${a.danger} failing`;
    if (a.warning) return `${a.warning} warning`;
    return a.rank <= 1 ? '' : a.status;
}

/** O(n log n) build (one pass plus sorts); the only per-node objects are the group and namespace nodes. */
export function buildInventoryTree(entries: FluxInventoryEntry[], opts?: InventoryOpts): InventoryTree {
    const summary: InventorySummary = { total: entries.length, failing: 0, warning: 0, healthy: 0, missing: 0, notWatched: 0, other: 0 };
    const groups = new Map<string, { g: InventoryGroup; acc: Acc; ns: Map<string, { n: InventoryNs; acc: Acc }> }>();
    const rank = new Map<FluxInventoryEntry, number>();
    for (const e of entries) {
        const h = entryHealth(e, opts);
        rank.set(e, rankOf(h.tone));
        if (!e.live) {
            if (h.tone === TONE_WARNING) summary.missing++;
            else summary.notWatched++;
        } else if (h.tone === TONE_DANGER) summary.failing++;
        else if (h.tone === TONE_WARNING) summary.warning++;
        else if (h.tone === TONE_SUCCESS) summary.healthy++;
        else summary.other++;

        const label = kindLabel(e);
        let gs = groups.get(label);
        if (!gs) {
            gs = { g: { key: 'g:' + label, label, count: 0, tone: 0, status: '', namespaces: [] }, acc: newAcc(), ns: new Map() };
            groups.set(label, gs);
        }
        const nsName = e.namespace ?? '';
        let ns = gs.ns.get(nsName);
        if (!ns) {
            ns = { n: { key: `n:${label}|${nsName}`, label: nsName || '(cluster)', count: 0, tone: 0, status: '', entries: [] }, acc: newAcc() };
            gs.ns.set(nsName, ns);
            gs.g.namespaces.push(ns.n);
        }
        ns.n.entries.push(e);
        add(ns.acc, h);
        add(gs.acc, h);
    }
    const out: InventoryGroup[] = [];
    const branchKeys: string[] = [];
    const byRank = (a: { rank: number; label: string }, b: { rank: number; label: string }) =>
        b.rank - a.rank || (a.label < b.label ? -1 : a.label > b.label ? 1 : 0);
    const gl: { rank: number; label: string; g: InventoryGroup }[] = [];
    for (const gs of groups.values()) {
        const nl: { rank: number; label: string; n: InventoryNs }[] = [];
        for (const ns of gs.ns.values()) {
            ns.n.entries.sort((a, b) => (rank.get(b) ?? 0) - (rank.get(a) ?? 0) || (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
            ns.n.count = ns.n.entries.length;
            ns.n.tone = ns.acc.tone;
            ns.n.status = rollStatus(ns.acc);
            nl.push({ rank: ns.acc.rank, label: ns.n.label, n: ns.n });
        }
        nl.sort(byRank);
        gs.g.namespaces = nl.map(x => x.n);
        gs.g.count = gs.g.namespaces.reduce((s, n) => s + n.count, 0);
        gs.g.tone = gs.acc.tone;
        gs.g.status = rollStatus(gs.acc);
        gl.push({ rank: gs.acc.rank, label: gs.g.label, g: gs.g });
    }
    gl.sort(byRank);
    for (const x of gl) {
        out.push(x.g);
        branchKeys.push(x.g.key);
        for (const n of x.g.namespaces) branchKeys.push(n.key);
    }
    return { groups: out, summary, branchKeys };
}

/** Flat, fixed-height rows for the virtualized list. A non-empty filter keeps matching leaves and force-expands their parents. */
export function flattenTree(tree: InventoryTree, expanded: Set<string>, filter: string, opts?: InventoryOpts): FlatRow[] {
    const q = filter.trim().toLowerCase();
    const rows: FlatRow[] = [];
    for (const g of tree.groups) {
        const gStart = rows.length;
        rows.push({ key: g.key, depth: 0, kind: 'group', label: g.label, count: g.count, tone: g.tone, status: g.status });
        const gOpen = !!q || expanded.has(g.key);
        let gCount = 0;
        for (const n of g.namespaces) {
            const nStart = rows.length;
            const nOpen = !!q || expanded.has(n.key);
            if (gOpen) rows.push({ key: n.key, depth: 1, kind: 'ns', label: n.label, count: n.count, tone: n.tone, status: n.status });
            let nCount = 0;
            for (const e of n.entries) {
                const h = entryHealth(e, opts);
                if (q && !`${kindLabel(e)} ${e.namespace ?? ''} ${e.name} ${h.status}`.toLowerCase().includes(q)) continue;
                nCount++;
                if (gOpen && nOpen)
                    rows.push({ key: 'l:' + e.id, depth: 2, kind: 'leaf', label: e.name, count: 1, tone: h.tone, status: h.status, entry: e });
            }
            gCount += nCount;
            if (q && !nCount && gOpen) rows.length = nStart;
            else if (gOpen) {
                const r = rows[nStart];
                if (r) {
                    r.count = q ? nCount : r.count;
                    r.expanded = nOpen;
                }
            }
        }
        if (q && !gCount) {
            rows.length = gStart;
            continue;
        }
        const gr = rows[gStart]!;
        gr.expanded = gOpen;
        if (q) gr.count = gCount;
    }
    return rows;
}
