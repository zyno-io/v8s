import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const elevateCluster = vi.fn();
type St = { id: string; phase: string; synced: number; elevated?: boolean };
const clusterStates = await vi.hoisted(async () => (await import('vue')).shallowRef<St[]>([]));
vi.mock('../src/lib/agent.ts', () => ({
    getAgent: () => ({ elevateCluster, clusterStates }),
    refreshKinds: () => {}
}));
const clearAccessCache = vi.fn();
vi.mock('../src/lib/access.ts', () => ({ clearAccessCache: () => clearAccessCache() }));

import { elevateItem } from '../src/lib/elevate.ts';
import { useClustersStore } from '../src/stores/clusters.ts';
import { useUiStore } from '../src/stores/ui.ts';

beforeEach(() => {
    setActivePinia(createPinia());
    elevateCluster.mockReset();
    clearAccessCache.mockReset();
    clusterStates.value = [];
    const c = useClustersStore();
    c.loaded = true;
    c.list = [{ id: 'prod', name: 'Prod', server: '', groups: [], writeContext: 'prod-admin' }];
});

describe('elevateItem', () => {
    it('offers Elevate for a linked cluster', () => {
        const it = elevateItem('Prod', 'prod-admin', false);
        expect(it?.elevate).toBe(true);
        expect(it?.label).toBe('Elevate to prod-admin');
        expect(it?.hint).toContain('prod-admin');
    });
    it('offers Revert while elevated', () => {
        expect(elevateItem('Prod', 'prod-admin', true)).toMatchObject({ label: 'Revert to Prod', elevate: false });
        // even if the link went away meanwhile
        expect(elevateItem('Prod', '', true)?.elevate).toBe(false);
    });
    it('offers nothing without a link', () => {
        expect(elevateItem('Prod', '', false)).toBeNull();
    });
});

describe('clusters store elevation', () => {
    it('isElevated and elevatedKey follow the cluster states', () => {
        const c = useClustersStore();
        expect(c.isElevated('prod')).toBe(false);
        clusterStates.value = [{ id: 'prod', phase: 'ready', synced: 0, elevated: true }];
        expect(c.isElevated('prod')).toBe(true);
        expect(c.elevatedKey).toBe('prod');
        expect(c.elevatedCount).toBe(1);
    });

    it('setElevated calls the agent, clears the access cache and toggles elevating', async () => {
        const c = useClustersStore();
        let release!: () => void;
        elevateCluster.mockReturnValue(new Promise<void>(r => (release = r)));
        const p = c.setElevated('prod', true);
        expect(c.elevating.has('prod')).toBe(true);
        void c.setElevated('prod', true); // guarded while in flight
        expect(elevateCluster).toHaveBeenCalledTimes(1);
        expect(elevateCluster).toHaveBeenCalledWith('prod', true);
        release();
        await p;
        expect(c.elevating.has('prod')).toBe(false);
        expect(clearAccessCache).toHaveBeenCalled();
    });

    it('toasts the failure', async () => {
        const c = useClustersStore();
        elevateCluster.mockRejectedValue(new Error('no link'));
        await c.setElevated('prod', false);
        expect(c.elevating.has('prod')).toBe(false);
        const t = useUiStore().toasts[0];
        expect(t?.tone).toBe('danger');
        expect(t?.text).toBe('Revert Prod: no link');
    });
});
