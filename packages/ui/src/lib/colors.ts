const HUES = ['var(--info)', 'var(--release)', 'var(--warning)', 'var(--success)', 'var(--danger)', 'var(--neutral)'];
const cache = new Map<string, string>();

/** Stable accent for a cluster tag dot. */
export function clusterColor(name: string): string {
    let c = cache.get(name);
    if (!c) {
        let h = 0;
        for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) >>> 0;
        c = HUES[h % HUES.length];
        cache.set(name, c);
    }
    return c;
}

export function phaseColor(phase: string): string {
    switch (phase) {
        case 'ready':
            return 'var(--success)';
        case 'unreachable':
        case 'auth-required':
            return 'var(--danger)';
        case 'connecting':
        case 'syncing':
        case 'degraded':
        case 'reconnecting':
            return 'var(--warning)';
        default:
            return 'var(--text3)';
    }
}
