import type { Row } from '@v8s/client';

import type { UsageIdx } from './columns.ts';

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
    return parts.join(' · ');
}

/** The usage block's tooltip: both resources against request, limit and node. Built only when a row re-renders. */
export function usageTitle(u: UsageIdx, row: Row): string {
    const node = u.node >= 0 && typeof row[u.node] === 'string' ? (row[u.node] as string) : '';
    const n = (i: number) => num(row, i);
    return [
        line('CPU', cpuText, n(u.cpuUse), n(u.cpuReq), n(u.cpuLim), n(u.cpuPctReq), n(u.cpuPctLim), n(u.cpuPctNode), node),
        line('MEM', memText, n(u.memUse), n(u.memReq), n(u.memLim), n(u.memPctReq), n(u.memPctLim), n(u.memPctNode), node)
    ].join('\n');
}

/** The phone card's "cpu 16m · mem 163Mi" line and its tone (the worse of the two limit percentages), or null without data. */
export function usageCardLine(u: UsageIdx, row: Row): { text: string; tone: 'warning' | 'danger' | '' } | null {
    const cpu = num(row, u.cpuUse);
    const mem = num(row, u.memUse);
    if (cpu == null || mem == null) return null;
    const tones = [usageTone('lim', num(row, u.cpuPctLim)), usageTone('lim', num(row, u.memPctLim))];
    return { text: `cpu ${cpu}m · mem ${mem}Mi`, tone: tones.includes('danger') ? 'danger' : tones.includes('warning') ? 'warning' : '' };
}
