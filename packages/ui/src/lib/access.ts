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
    const fresh = new Map<string, AccessResult>();
    if (missing.length) {
        const gen = generation;
        const res = await getAgent().call<AccessCheckResult>('access.check', { checks: missing });
        missing.forEach((c, i) => {
            const r = res.results?.[i];
            if (!r) return;
            fresh.set(keyOf(c), r);
            // An answer computed before a clear (e.g. the cluster was elevated meanwhile) is used once, never cached.
            if (gen === generation) cache.set(keyOf(c), { at: now, r });
        });
    }
    return checks.map(c => fresh.get(keyOf(c)) ?? cache.get(keyOf(c))?.r ?? { allowed: true, viaWrite: false, reason: '' });
}

let generation = 0;

export function clearAccessCache(): void {
    generation++;
    cache.clear();
}
