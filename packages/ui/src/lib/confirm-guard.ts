import type { ObjectRef } from '@v8s/client';

import { confirmPhrase, needsTypeToConfirm } from '@/lib/actions.ts';
import { useClustersStore } from '@/stores/clusters.ts';

/**
 * The protected / bulk guard every mutating action shares (ActionBar uses the same rules): the phrase the user must type and the
 * note naming the protected clusters. Actions started from places other than the ActionBar (the Problems quick actions) use it so
 * a protected cluster is never one click away.
 */
export function confirmGuard(targets: readonly Pick<ObjectRef, 'cluster' | 'name'>[]): { phrase?: string; note?: string } {
    const clusters = useClustersStore();
    const ids = [...new Set(targets.map(t => t.cluster))];
    const prod = ids.filter(c => clusters.isProtected(c)).map(c => clusters.nameById.get(c) ?? c);
    const typed = needsTypeToConfirm({
        count: targets.length,
        clusters: ids,
        protectedClusters: ids.filter(c => clusters.isProtected(c))
    });
    return {
        phrase: typed ? confirmPhrase(targets.length, targets[0]?.name ?? '') : undefined,
        note: prod.length ? `Protected cluster: ${prod.join(', ')}.` : undefined
    };
}
