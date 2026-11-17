import type { AccessCheck, AccessCheckResult, AccessResult } from '@v8s/client';

import { getAgent } from './agent.ts';

const TTL_MS = 30_000;
const cache = new Map<string, { at: number; r: AccessResult }>();

const keyOf = (c: AccessCheck) => `${c.cluster}|${c.verb}|${c.gvr}|${c.namespace ?? ''}|${c.name ?? ''}`;

/** `access.check` with a small client-side cache (the agent also caches for 60 s). Results are in input order. */
export async function checkAccess(checks: readonly AccessCheck[]): Promise<AccessResult[]> {
    const now = Date.now();
    const missing: AccessCheck[] = [];
    const seen = new Set<string>();
    for (const c of checks) {
        const k = keyOf(c);
        const hit = cache.get(k);
        if (hit && now - hit.at < TTL_MS) continue;
        if (!seen.has(k)) {
            seen.add(k);
            missing.push(c);
        }
    }
    if (missing.length) {
        const res = await getAgent().call<AccessCheckResult>('access.check', { checks: missing });
        missing.forEach((c, i) => {
            const r = res.results?.[i];
            if (r) cache.set(keyOf(c), { at: now, r });
        });
    }
    return checks.map(c => cache.get(keyOf(c))?.r ?? { allowed: true, viaWrite: false, reason: '' });
}

export function clearAccessCache(): void {
    cache.clear();
}
