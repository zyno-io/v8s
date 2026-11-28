/** Relative age such as `45s`, `12m`, `3h`, `11d` from a unix-ms timestamp. */
export function formatAge(createdAtMs: number, nowMs: number): string {
    if (!createdAtMs) return '—';
    const s = Math.max(0, Math.floor((nowMs - createdAtMs) / 1000));
    if (s < 60) return s + 's';
    const m = Math.floor(s / 60);
    if (m < 60) return m + 'm';
    const h = Math.floor(m / 60);
    if (h < 48) return h + 'h';
    return Math.floor(h / 24) + 'd';
}

export const OLD_AGE_MS = 3600 * 1000;

export function formatCount(n: number): string {
    return n.toLocaleString('en-US');
}

// ---- usage cells (docs/design-system.md "Usage block") ----

/**
 * A usage cell in k9s style: a bare integer, the unit lives in the header. `null` (no data) is `—`, `-1` (no request, limit or
 * node to divide by) is `n/a`; both render muted. CPU is always millicores and memory MiB; percentages are sent per-mille.
 * A percentage of a node below 10 % keeps one decimal, because node fractions are small.
 */
export function formatUsage(unit: string, pctNode: boolean, v: unknown): string {
    if (v == null) return '—';
    const n = v as number;
    if (n < 0) return 'n/a';
    if (unit === 'permille') return pctNode && n < 100 ? (n / 10).toFixed(1) : String(Math.round(n / 10));
    return String(n);
}

/** Text color for a usage cell: `lim` warns from 90 % and alarms from 100 %; `node` warns from 90 %; requests are uncolored. */
export function usageTone(rule: 'lim' | 'node' | null, v: unknown): 'warning' | 'danger' | '' {
    if (rule == null || typeof v !== 'number' || v < 0) return '';
    if (rule === 'lim' && v >= 1000) return 'danger';
    return v >= 900 ? 'warning' : '';
}

/**
 * The background bar of a percentage cell in the usage block: its tier (color) and its fill height in % of the row, capped at a
 * full row. `ok` below 70 %, `warn` from 70 %, `hot` from 90 %. A request percentage (`rule` null) gets a bar only when the row
 * has no limit for that resource (`lim`, the matching %L value, is n/a or absent): then the request is the only bound. Null (no
 * bar) for no data, n/a, and a request next to a limit.
 */
export function usageBar(rule: 'lim' | 'node' | null, v: unknown, lim?: unknown): { tier: 'ok' | 'warn' | 'hot'; fill: number } | null {
    if (typeof v !== 'number' || v < 0) return null;
    if (rule == null && typeof lim === 'number' && lim >= 0) return null;
    return { tier: v >= 900 ? 'hot' : v >= 700 ? 'warn' : 'ok', fill: Math.min(v, 1000) / 10 };
}

/**
 * Text color for a memory request percentage next to a limit (it has no bar then): `warning` above 100 %, `danger` above 200 %
 * of the request, on the shown (rounded) percentage. A pod over its memory request is among the first evicted when its node runs
 * short of memory. CPU requests stay uncolored: running over a CPU request is normal for bursty pods.
 */
export function usageReqTone(res: 'cpu' | 'mem', v: unknown, lim: unknown): 'warning' | 'danger' | '' {
    if (res !== 'mem' || typeof v !== 'number' || v < 0 || typeof lim !== 'number' || lim < 0) return '';
    const p = Math.round(v / 10);
    return p > 200 ? 'danger' : p > 100 ? 'warning' : '';
}
