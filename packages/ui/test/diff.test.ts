import { describe, expect, it } from 'vitest';

import { collapseContext, countChanges, diffLines } from '../src/lib/diff.ts';

describe('diffLines', () => {
    it('reports no changes for equal text', () => {
        const rows = diffLines('a\nb\nc', 'a\nb\nc');
        expect(rows.every(r => r.kind === 'same')).toBe(true);
        expect(countChanges(rows)).toBe(0);
    });

    it('pairs a replaced line into one changed row with both line numbers', () => {
        const rows = diffLines('a\nreplicas: 1\nc', 'a\nreplicas: 2\nc');
        expect(rows.map(r => r.kind)).toEqual(['same', 'chg', 'same']);
        expect(rows[1].l).toEqual({ n: 2, t: 'replicas: 1' });
        expect(rows[1].r).toEqual({ n: 2, t: 'replicas: 2' });
    });

    it('marks pure additions and deletions', () => {
        const rows = diffLines('a\nc', 'a\nb\nc');
        expect(rows.map(r => r.kind)).toEqual(['same', 'add', 'same']);
        expect(rows[1].l).toBeUndefined();
        expect(rows[1].r).toEqual({ n: 2, t: 'b' });
        const del = diffLines('a\nb\nc', 'a\nc');
        expect(del.map(r => r.kind)).toEqual(['same', 'del', 'same']);
    });

    it('handles empty sides', () => {
        expect(diffLines('', 'x').map(r => r.kind)).toEqual(['chg']);
        expect(countChanges(diffLines('a\nb', ''))).toBe(2);
    });

    it('keeps line numbers right across several hunks', () => {
        const left = ['1', '2', '3', '4', '5', '6'].join('\n');
        const right = ['1', 'two', '3', '4', '5', 'six', 'seven'].join('\n');
        const rows = diffLines(left, right);
        expect(rows.filter(r => r.kind !== 'same').map(r => [r.l?.n, r.r?.n])).toEqual([
            [2, 2],
            [6, 6],
            [undefined, 7]
        ]);
    });
});

describe('collapseContext', () => {
    it('keeps context around changes and skips the rest', () => {
        const left = Array.from({ length: 30 }, (_, i) => `l${i}`).join('\n');
        const right = left.replace('l15', 'changed');
        const blocks = collapseContext(diffLines(left, right), 3);
        expect(blocks.map(b => b.type)).toEqual(['skip', 'rows', 'skip']);
        const rows = blocks[1] as { type: 'rows'; rows: unknown[] };
        expect(rows.rows).toHaveLength(7);
    });
});
