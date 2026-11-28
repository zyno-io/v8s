import type { FluxInventoryEntry } from '@v8s/client';

import { describe, expect, it } from 'vitest';

import { buildInventoryTree, entryHealth, flattenTree } from '../src/lib/flux-inventory.ts';

function e(name: string, over: Partial<FluxInventoryEntry> = {}): FluxInventoryEntry {
    return {
        id: `ns_${name}_apps_Deployment`,
        group: 'apps',
        version: 'v1',
        kind: 'Deployment',
        namespace: 'ns',
        name,
        gvr: 'apps/v1/deployments',
        live: true,
        tone: 1,
        status: 'Available',
        ...over
    };
}

describe('entryHealth', () => {
    it('maps missing and unwatched', () => {
        expect(entryHealth(e('a', { live: false }))).toEqual({ tone: 2, status: 'Not found' });
        expect(entryHealth(e('a', { live: false, gvr: undefined }))).toEqual({ tone: 0, status: 'Not watched' });
        expect(entryHealth(e('a'))).toEqual({ tone: 1, status: 'Available' });
    });
});

describe('buildInventoryTree', () => {
    const entries = [
        e('ok'),
        e('bad', { tone: 3, status: 'CrashLoop' }),
        e('gone', { live: false, tone: 0 }),
        e('other-ns', { namespace: 'zz' }),
        { id: 'x', group: '', version: 'v1', kind: 'Namespace', name: 'foo', live: false, tone: 0 } as FluxInventoryEntry
    ];
    const tree = buildInventoryTree(entries);

    it('groups by kind then namespace with roll-ups, worst first', () => {
        expect(tree.groups.map(g => g.label)).toEqual(['apps/Deployment', 'Namespace']);
        const g = tree.groups[0]!;
        expect(g.count).toBe(4);
        expect(g.tone).toBe(3);
        expect(g.status).toBe('1 failing');
        expect(g.namespaces.map(n => n.label)).toEqual(['ns', 'zz']);
        expect(g.namespaces[0]!.entries.map(x => x.name)).toEqual(['bad', 'gone', 'ok']);
        expect(tree.groups[1]!.namespaces[0]!.label).toBe('(cluster)');
        expect(tree.groups[1]!.tone).toBe(0);
    });

    it('summarizes', () => {
        expect(tree.summary).toEqual({ total: 5, failing: 1, warning: 0, healthy: 2, missing: 1, notWatched: 1, other: 0 });
    });

    it('flattens by expanded state', () => {
        expect(flattenTree(tree, new Set(), '').map(r => r.kind)).toEqual(['group', 'group']);
        const rows = flattenTree(tree, new Set(['g:apps/Deployment', 'n:apps/Deployment|ns']), '');
        expect(rows.map(r => r.key)).toEqual([
            'g:apps/Deployment',
            'n:apps/Deployment|ns',
            'l:ns_bad_apps_Deployment',
            'l:ns_gone_apps_Deployment',
            'l:ns_ok_apps_Deployment',
            'n:apps/Deployment|zz',
            'g:Namespace'
        ]);
        expect(rows[2]!.entry?.name).toBe('bad');
    });

    it('filter force-expands matches and prunes the rest', () => {
        const rows = flattenTree(tree, new Set(), 'crash');
        expect(rows.map(r => r.kind)).toEqual(['group', 'ns', 'leaf']);
        expect(rows[0]!.count).toBe(1);
        expect(flattenTree(tree, new Set(), 'nomatch')).toEqual([]);
    });

    it('handles thousands of entries', () => {
        const many = Array.from({ length: 20000 }, (_, i) => e('n' + i, { namespace: 'ns' + (i % 50), id: 'id' + i, tone: i % 100 === 0 ? 3 : 1 }));
        const t0 = performance.now();
        const t = buildInventoryTree(many);
        const rows = flattenTree(t, new Set(t.branchKeys), '');
        expect(rows.length).toBe(20000 + 50 + 1);
        expect(performance.now() - t0).toBeLessThan(1500);
    });
});
