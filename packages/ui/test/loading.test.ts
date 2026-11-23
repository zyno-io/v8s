import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';

type St = { id: string; phase: string; synced: number; loading?: string[] };
const clusterStates = await vi.hoisted(async () => (await import('vue')).shallowRef<St[]>([]));
vi.mock('../src/lib/agent.ts', () => ({
    getAgent: () => ({ clusterStates }),
    refreshKinds: () => {}
}));
vi.mock('../src/lib/access.ts', () => ({ clearAccessCache: () => {} }));

import { emptyKindOf, kindLoading, loadingNames, type EmptyInput } from '../src/lib/loading.ts';
import { useClustersStore } from '../src/stores/clusters.ts';

const SECRETS = 'v1/secrets';

beforeEach(() => {
    setActivePinia(createPinia());
    clusterStates.value = [];
    useClustersStore().loaded = true;
});

describe('kindLoading', () => {
    it('is true for a listed gvr on a connected cluster', () => {
        expect(kindLoading({ phase: 'ready', loading: [SECRETS] }, SECRETS)).toBe(true);
        expect(kindLoading({ phase: 'syncing', loading: [SECRETS] }, SECRETS)).toBe(true);
        expect(kindLoading({ phase: 'ready', loading: [SECRETS] }, 'v1/configmaps')).toBe(false);
        expect(kindLoading({ phase: 'ready' }, SECRETS)).toBe(false);
    });
    it('is false while the cluster is unreachable or needs auth', () => {
        expect(kindLoading({ phase: 'unreachable', loading: [SECRETS] }, SECRETS)).toBe(false);
        expect(kindLoading({ phase: 'auth-required', loading: [SECRETS] }, SECRETS)).toBe(false);
    });
});

describe('emptyKindOf', () => {
    const base: EmptyInput = { clusters: 1, total: 0, version: 1, filtered: false, loading: 0, switching: false, syncing: false };
    it('keeps the order: no clusters, rows, first patch, then the reason', () => {
        expect(emptyKindOf({ ...base, clusters: 0 })).toBe('no-clusters');
        expect(emptyKindOf({ ...base, total: 3, loading: 1 })).toBe('');
        expect(emptyKindOf({ ...base, version: 0, loading: 1 })).toBe('loading');
        expect(emptyKindOf(base)).toBe('none');
        expect(emptyKindOf({ ...base, filtered: true })).toBe('filtered');
        expect(emptyKindOf({ ...base, syncing: true })).toBe('syncing');
    });
    it('a list still loading, or a switch in flight, outranks filtered and none', () => {
        expect(emptyKindOf({ ...base, loading: 1 })).toBe('syncing');
        expect(emptyKindOf({ ...base, loading: 1, filtered: true })).toBe('syncing');
        expect(emptyKindOf({ ...base, switching: true, filtered: true })).toBe('syncing');
    });
});

describe('loadingNames', () => {
    const name = (id: string) => id.toUpperCase();
    it('lists up to three, then counts the rest', () => {
        expect(loadingNames(['a'], name)).toBe('A');
        expect(loadingNames(['a', 'b'], name)).toBe('A and B');
        expect(loadingNames(['a', 'b', 'c'], name)).toBe('A, B and C');
        expect(loadingNames(['a', 'b', 'c', 'd', 'e'], name)).toBe('A, B, C and 2 more');
    });
});

describe('clusters store loadingOf', () => {
    it('returns the ids of the clusters loading the kind', () => {
        const c = useClustersStore();
        expect(c.loadingOf(SECRETS)).toEqual([]);
        clusterStates.value = [
            { id: 'a', phase: 'ready', synced: 0, loading: [SECRETS] },
            { id: 'b', phase: 'ready', synced: 0 },
            { id: 'c', phase: 'unreachable', synced: 0, loading: [SECRETS] },
            { id: 'd', phase: 'syncing', synced: 0, loading: ['v1/configmaps', SECRETS] }
        ];
        expect(c.loadingOf(SECRETS)).toEqual(['a', 'd']);
        expect(c.loadingOf('v1/configmaps')).toEqual(['d']);
    });
});
