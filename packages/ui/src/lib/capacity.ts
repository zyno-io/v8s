import type { Column, MetricsAvail, Row } from '@v8s/client';

/** The capacity row of one cluster by column key: millicores for CPU, MiB for memory; `cpuUse`/`memUse` are -1 when unknown. */
export type CapacityRow = Record<string, number>;

/** Reads the one capacity row (`v8s/v1/capacity`) into a key → number record, or null while there is none. */
export function capacityRow(columns: readonly Column[] | null, row: Row | undefined): CapacityRow | null {
    if (!columns?.length || !row) return null;
    const out: CapacityRow = {};
    columns.forEach((c, i) => {
        const v = row[i + 2];
        if (c.type === 'int' && typeof v === 'number') out[c.key] = v;
    });
    return 'cpuAlloc' in out ? out : null;
}

export type CapacityKind = 'cpu' | 'mem';

export interface CapacityView {
    /** Allocatable is known and non-zero; without it nothing else is meaningful. */
    ok: boolean;
    /** The big number is usage (metrics-server answered); otherwise it is the requested share. */
    usage: boolean;
    /** The big number: usage (or requests) as a percentage of allocatable, rounded for display. */
    pct: string;
    tone: '' | 'warning' | 'danger';
    /** Bar fill and request tick as 0–100 widths. */
    fill: number;
    tick: number;
    /** `requests 62% · limits 143%` */
    share: string;
    /** `41.2 of 112 cores`, or `28 of 112 cores requested` without usage. */
    amount: string;
    /** Why there is no usage figure (metrics-server missing, forbidden, …), or ''. */
    note: string;
    title: string;
}

const pctText = (p: number): string => (p < 10 ? p.toFixed(1) : String(Math.round(p)));
const cores = (m: number): string => {
    const v = m / 1000;
    return String(v >= 100 ? Math.round(v) : +v.toFixed(1));
};
const gib = (mi: number): string => {
    const v = mi / 1024;
    return String(v >= 100 ? Math.round(v) : +v.toFixed(1));
};

/** Why usage is missing for a cluster in a given metrics state. */
export function usageNote(metrics: MetricsAvail | undefined, reporting: number): string {
    if (metrics === 'absent') return 'usage: metrics-server not installed';
    if (metrics === 'forbidden') return 'usage: metrics not allowed';
    if (metrics === 'unavailable') return 'usage: metrics-server not answering';
    if (metrics === 'ok' && !reporting) return 'usage: no node reports metrics';
    return '';
}

/** The Overview card's CPU or Memory cell from a capacity row. Pure: everything the template shows is decided here. */
export function capacityView(kind: CapacityKind, c: CapacityRow | null, metrics: MetricsAvail | undefined): CapacityView {
    const cpu = kind === 'cpu';
    const alloc = c?.[cpu ? 'cpuAlloc' : 'memAlloc'] ?? 0;
    const none: CapacityView = { ok: false, usage: false, pct: '—', tone: '', fill: 0, tick: 0, share: '', amount: '', note: '', title: '' };
    if (!c || alloc <= 0) return none;
    const req = c[cpu ? 'cpuReq' : 'memReq'] ?? 0;
    const lim = c[cpu ? 'cpuLim' : 'memLim'] ?? 0;
    const use = c[cpu ? 'cpuUse' : 'memUse'] ?? -1;
    const usage = use >= 0;
    const reqPct = (req / alloc) * 100;
    const limPct = (lim / alloc) * 100;
    const main = usage ? (use / alloc) * 100 : reqPct;
    const fmt = cpu ? cores : gib;
    const unit = cpu ? 'cores' : 'GiB';
    const clamp = (p: number) => Math.max(0, Math.min(100, p));
    const nodes = c.nodes ?? 0;
    const reporting = c.nodesReporting ?? 0;
    const noLimit = c[cpu ? 'podsNoCPULim' : 'podsNoMemLim'] ?? 0;
    const note = usage ? '' : usageNote(metrics, reporting);
    const title = [
        usage ? `${reporting} of ${nodes} nodes reporting` : '',
        `${(c.podsCounted ?? 0).toLocaleString('en-US')} pods counted, ${noLimit.toLocaleString('en-US')} without a ${cpu ? 'CPU' : 'memory'} limit`,
        'Percentages are of the allocatable of every node'
    ]
        .filter(Boolean)
        .join('\n');
    return {
        ok: true,
        usage,
        pct: pctText(main),
        tone: main >= 100 ? 'danger' : main >= 90 ? 'warning' : '',
        fill: clamp(main),
        tick: clamp(reqPct),
        share: `requests ${pctText(reqPct)}% · limits ${pctText(limPct)}%`,
        amount: usage ? `${fmt(use)} of ${fmt(alloc)} ${unit}` : `${fmt(req)} of ${fmt(alloc)} ${unit} requested`,
        note,
        title
    };
}
