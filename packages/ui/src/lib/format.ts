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

/** Text colour for a usage cell: `lim` warns from 90 % and alarms from 100 %; `node` warns from 90 %; requests are uncoloured. */
export function usageTone(rule: 'lim' | 'node' | null, v: unknown): 'warning' | 'danger' | '' {
    if (rule == null || typeof v !== 'number' || v < 0) return '';
    if (rule === 'lim' && v >= 1000) return 'danger';
    return v >= 900 ? 'warning' : '';
}
