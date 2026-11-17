import { describe, expect, it } from 'vitest';

import { aggFlatIndexOfView, aggregateRows, buildAggFlat, groupClusters } from '@/lib/problems-aggregate.ts';

const rows = ['A', 'A', 'A', 'B', 'C', 'C'];
const clusters = ['c1', 'c2', 'c3', 'c1', 'c1', 'c2'];

describe('aggregateRows', () => {
    it('collapses adjacent equal keys', () => {
        const g = aggregateRows(rows.length, i => rows[i]);
        expect(g).toEqual([
            { key: 'A', first: 0, n: 3 },
            { key: 'B', first: 3, n: 1 },
            { key: 'C', first: 4, n: 2 }
        ]);
    });
    it('does not merge equal keys that are not adjacent', () => {
        expect(aggregateRows(3, i => ['A', 'B', 'A'][i]).map(g => g.n)).toEqual([1, 1, 1]);
    });
    it('never merges across an unloaded row', () => {
        const g = aggregateRows(3, i => (i === 1 ? undefined : 'A'));
        expect(g.map(x => x.n)).toEqual([1, 1, 1]);
    });
    it('handles an empty set', () => {
        expect(aggregateRows(0, () => 'x')).toEqual([]);
    });
    it('lists the distinct clusters of a group', () => {
        const g = aggregateRows(rows.length, i => rows[i]);
        expect(groupClusters(g[0], i => clusters[i])).toEqual(['c1', 'c2', 'c3']);
        expect(groupClusters(g[2], i => clusters[i])).toEqual(['c1', 'c2']);
    });
});

describe('buildAggFlat', () => {
    const groups = aggregateRows(rows.length, i => rows[i]);
    it('shows one row per group when nothing is expanded', () => {
        expect(buildAggFlat(groups, new Set(), null).map(i => i.kind)).toEqual(['row', 'row', 'row']);
    });
    it('expands a group into per-cluster children', () => {
        const flat = buildAggFlat(groups, new Set(['A']), null);
        expect(flat.map(i => i.kind + i.vi)).toEqual(['row0', 'child0', 'child1', 'child2', 'row3', 'row4']);
    });
    it('puts evidence after a single-cluster row', () => {
        const flat = buildAggFlat(groups, new Set(), 3);
        expect(flat.map(i => i.kind + i.vi)).toEqual(['row0', 'row3', 'evidence3', 'row4']);
    });
    it('puts evidence after an expanded child only', () => {
        const flat = buildAggFlat(groups, new Set(['C']), 5);
        expect(flat.map(i => i.kind + i.vi)).toEqual(['row0', 'row3', 'row4', 'child4', 'child5', 'evidence5']);
        // a collapsed multi-cluster group cannot show evidence
        expect(buildAggFlat(groups, new Set(), 5).some(i => i.kind === 'evidence')).toBe(false);
    });
    it('maps a view index back to its flat index', () => {
        const flat = buildAggFlat(groups, new Set(['A']), 1);
        expect(aggFlatIndexOfView(flat, 1)).toBe(2);
        expect(aggFlatIndexOfView(flat, 3)).toBe(5);
        expect(aggFlatIndexOfView(flat, 99)).toBe(-1);
    });
});
