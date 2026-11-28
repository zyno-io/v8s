import type { Row } from '@v8s/client';

import type { UsageIdx, UsageSub } from './columns.ts';

import { usageTone } from './format.ts';

const num = (row: Row, i: number): number | null => {
    const v = i >= 0 ? row[i] : null;
    return typeof v === 'number' ? v : null;
};

/** `1250m (1.25 cores)` once it is a core or more, else `16m`. */
export const cpuText = (m: number): string => (m >= 1000 ? `${m}m (${+(m / 1000).toFixed(2)} cores)` : `${m}m`);
/** `1536Mi (1.5Gi)` from a GiB up, else `163Mi`. */
export const memText = (mi: number): string => (mi >= 1024 ? `${mi}Mi (${+(mi / 1024).toFixed(2)}Gi)` : `${mi}Mi`);

const pct = (permille: number): string => (permille < 100 ? (permille / 10).toFixed(1) : String(Math.round(permille / 10)));
const pctText = (permille: number | null): string => (permille == null || permille < 0 ? '' : ` (${pct(permille)}%)`);

function line(
    name: 'CPU' | 'MEM',
    fmt: (n: number) => string,
    used: number | null,
    req: number | null,
    lim: number | null,
    pReq: number | null,
    pLim: number | null,
    pNode: number | null,
    node: string
): string {
    const parts = [used == null ? `${name} no data` : `${name} ${fmt(used)} used`];
    parts.push(req == null ? 'no request' : `${fmt(req)} requested${pctText(pReq)}`);
    parts.push(lim == null ? 'no limit' : `${fmt(lim)} limit${pctText(pLim)}`);
    if (pNode != null && pNode >= 0 && node) parts.push(`${pct(pNode)}% of ${node} allocatable`);
    if (name === 'MEM' && lim != null && pReq != null && Math.round(pReq / 10) > 100)
        parts.push('over request: evicted first if the node runs short of memory');
    return parts.join(' · ');
}

/** A node row's line: usage against the node's own allocatable. */
function nodeLine(name: 'CPU' | 'MEM', fmt: (n: number) => string, used: number | null, alloc: number | null, pAlloc: number | null): string {
    const parts = [used == null ? `${name} no data` : `${name} ${fmt(used)} used`];
    if (alloc == null) parts.push('no allocatable');
    else parts.push(`${pAlloc != null && pAlloc >= 0 ? `${pct(pAlloc)}% of ` : ''}${fmt(alloc)} allocatable`);
    return parts.join(' · ');
}

/** The usage block's tooltip: both resources against request, limit and node (nodes: against their allocatable). Built only when a row re-renders. */
export function usageTitle(u: UsageIdx, row: Row): string {
    if (u.cpuPctAlloc >= 0) {
        const a = (i: number) => num(row, i);
        return [
            nodeLine('CPU', cpuText, a(u.cpuUse), a(u.cpuAlloc), a(u.cpuPctAlloc)),
            nodeLine('MEM', memText, a(u.memUse), a(u.memAlloc), a(u.memPctAlloc))
        ].join('\n');
    }
    const node = u.node >= 0 && typeof row[u.node] === 'string' ? (row[u.node] as string) : '';
    const n = (i: number) => num(row, i);
    const vpa = u.vpa >= 0 && typeof row[u.vpa] === 'string' ? (row[u.vpa] as string) : '';
    return [
        line('CPU', cpuText, n(u.cpuUse), n(u.cpuReq), n(u.cpuLim), n(u.cpuPctReq), n(u.cpuPctLim), n(u.cpuPctNode), node),
        line('MEM', memText, n(u.memUse), n(u.memReq), n(u.memLim), n(u.memPctReq), n(u.memPctLim), n(u.memPctNode), node),
        ...(vpa ? [`Requests managed by VPA ${vpa}`] : [])
    ].join('\n');
}

/** The phone card's "cpu 16m · mem 163Mi" line and its tone (the worse of the two limit percentages), or null without data. */
export function usageCardLine(u: UsageIdx, row: Row): { text: string; tone: 'warning' | 'danger' | '' } | null {
    const cpu = num(row, u.cpuUse);
    const mem = num(row, u.memUse);
    if (cpu == null || mem == null) return null;
    const tones =
        u.cpuPctAlloc >= 0
            ? [usageTone('node', num(row, u.cpuPctAlloc)), usageTone('node', num(row, u.memPctAlloc))]
            : [usageTone('lim', num(row, u.cpuPctLim)), usageTone('lim', num(row, u.memPctLim))];
    const vpa = u.vpa >= 0 && row[u.vpa] ? ' · VPA' : '';
    return { text: `cpu ${cpu}m · mem ${mem}Mi${vpa}`, tone: tones.includes('danger') ? 'danger' : tones.includes('warning') ? 'warning' : '' };
}

const BARS = 'green below 70 %, amber from 70 %, red from 90 %';

/** What a usage header's colors mean, for its tooltip under the sort hint ('' for raw columns). */
export function usageHint(u: UsageSub): string {
    if (u.unit !== 'permille') return '';
    if (u.tone === 'lim') return `Bar: share of the limit, ${BARS}.`;
    if (u.tone === 'node') return `Bar: share of the node's allocatable, ${BARS}.`;
    const without = `Without a limit: a bar, ${BARS}.`;
    return u.res === 'mem'
        ? `With a limit: no bar; amber above 100 % of the request, red above 200 % (pods over their memory request are evicted first when the node runs short). ${without}`
        : `With a limit: no bar (running over a CPU request is normal). ${without}`;
}
