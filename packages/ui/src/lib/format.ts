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
