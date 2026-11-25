import { describe, expect, it } from 'vitest';

import { listsPods, podsFilter, podsViaReplicaSets } from '@/lib/related.ts';

describe('related pods', () => {
    it('lists pods for workloads only', () => {
        for (const g of ['apps/v1/deployments', 'apps/v1/statefulsets', 'apps/v1/daemonsets', 'apps/v1/replicasets', 'batch/v1/jobs'])
            expect(listsPods(g)).toBe(true);
        for (const g of ['v1/pods', 'batch/v1/cronjobs', 'v1/services']) expect(listsPods(g)).toBe(false);
    });

    it('filters pods on the workload as owner', () => {
        expect(podsFilter('apps/v1/statefulsets', 'db')).toEqual({ col: 'owner', op: 'eq', value: 'db' });
        expect(podsFilter('batch/v1/jobs', 'migrate')).toEqual({ col: 'owner', op: 'eq', value: 'migrate' });
        expect(podsFilter('v1/services', 'web')).toBeNull();
        expect(podsFilter('apps/v1/statefulsets', '')).toBeNull();
    });

    it('finds a deployment’s pods through its ReplicaSets', () => {
        expect(podsViaReplicaSets('apps/v1/deployments')).toBe(true);
        expect(podsFilter('apps/v1/deployments', 'web')).toBeNull();
        expect(podsFilter('apps/v1/deployments', 'web', ['web-b', 'web-a'])).toEqual({ col: 'owner', op: 'in', value: ['web-a', 'web-b'] });
    });
});
