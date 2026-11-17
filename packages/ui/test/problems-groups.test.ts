import { describe, expect, it } from 'vitest';

import type { GroupSpec } from '@/lib/problems-groups.ts';

import { buildLayout, flatIndexOfHeader, flatIndexOfView, itemAt, pixelOffset, viewRangeOfFlat } from '@/lib/problems-groups.ts';

const g = (key: string, count: number, collapsed = false): GroupSpec => ({ key, count, collapsed });

function dump(l: ReturnType<typeof buildLayout>): string[] {
    const out: string[] = [];
    for (let i = 0; i < l.count; i++) {
        const it = itemAt(l, i)!;
        out.push(it.kind === 'header' ? `H:${it.key}` : it.kind === 'row' ? `r${it.viewIndex}` : `E${it.viewIndex}`);
    }
    return out;
}

describe('buildLayout', () => {
    it('lists headers and rows per group', () => {
        const l = buildLayout([g('a', 2), g('b', 3)], { headers: true });
        expect(l.count).toBe(7);
        expect(dump(l)).toEqual(['H:a', 'r0', 'r1', 'H:b', 'r2', 'r3', 'r4']);
        expect(l.rows).toBe(5);
    });

    it('hides empty groups', () => {
        const l = buildLayout([g('a', 0), g('b', 2), g('c', 0)], { headers: true });
        expect(dump(l)).toEqual(['H:b', 'r0', 'r1']);
    });

    it('collapsed groups keep only the header but still own their view indexes', () => {
        const l = buildLayout([g('a', 3, true), g('b', 2)], { headers: true });
        expect(dump(l)).toEqual(['H:a', 'H:b', 'r3', 'r4']);
        expect(flatIndexOfView(l, 1)).toBe(-1);
        expect(flatIndexOfView(l, 3)).toBe(2);
    });

    it('without headers it is a plain list and collapse is ignored', () => {
        const l = buildLayout([g('', 4, true)]);
        expect(dump(l)).toEqual(['r0', 'r1', 'r2', 'r3']);
    });

    it('inserts one evidence item after the expanded row', () => {
        const l = buildLayout([g('a', 2), g('b', 3)], { headers: true, expanded: 3 });
        expect(dump(l)).toEqual(['H:a', 'r0', 'r1', 'H:b', 'r2', 'r3', 'E3', 'r4']);
        expect(l.count).toBe(8);
    });

    it('expansion in an ungrouped list and on the last row', () => {
        expect(dump(buildLayout([g('', 3)], { expanded: 2 }))).toEqual(['r0', 'r1', 'r2', 'E2']);
        expect(dump(buildLayout([g('', 3)], { expanded: 0 }))).toEqual(['r0', 'E0', 'r1', 'r2']);
    });

    it('ignores an expansion inside a collapsed group or out of range', () => {
        expect(dump(buildLayout([g('a', 2, true)], { headers: true, expanded: 1 }))).toEqual(['H:a']);
        expect(dump(buildLayout([g('', 2)], { expanded: 5 }))).toEqual(['r0', 'r1']);
        expect(dump(buildLayout([g('', 2)], { expanded: null }))).toEqual(['r0', 'r1']);
    });

    it('round trips every view index through the flat list', () => {
        const groups = [g('a', 5), g('x', 0), g('b', 1), g('c', 4, true), g('d', 7)];
        for (const expanded of [null, 0, 4, 5, 6, 12, 16]) {
            const l = buildLayout(groups, { headers: true, expanded });
            for (let vi = 0; vi < l.rows; vi++) {
                const f = flatIndexOfView(l, vi);
                if (f < 0) {
                    const inCollapsed = vi >= 6 && vi < 10;
                    expect(inCollapsed).toBe(true);
                    continue;
                }
                expect(itemAt(l, f)).toMatchObject({ kind: 'row', viewIndex: vi });
            }
            // every flat index resolves, and out-of-range does not
            for (let f = 0; f < l.count; f++) expect(itemAt(l, f)).toBeDefined();
            expect(itemAt(l, l.count)).toBeUndefined();
            expect(itemAt(l, -1)).toBeUndefined();
        }
    });

    it('evidence flat index is the row flat index plus one', () => {
        const l = buildLayout([g('a', 3), g('b', 3)], { headers: true, expanded: 4 });
        const f = flatIndexOfView(l, 4);
        expect(itemAt(l, f + 1)).toMatchObject({ kind: 'evidence', viewIndex: 4 });
        expect(itemAt(l, f + 2)).toMatchObject({ kind: 'row', viewIndex: 5 });
    });

    it('finds headers by source group index', () => {
        const l = buildLayout([g('a', 0), g('b', 2), g('c', 1)], { headers: true });
        expect(flatIndexOfHeader(l, 0)).toBe(-1);
        expect(flatIndexOfHeader(l, 1)).toBe(0);
        expect(flatIndexOfHeader(l, 2)).toBe(3);
        expect(flatIndexOfHeader(buildLayout([g('a', 1)]), 0)).toBe(-1);
    });

    it('handles a large grouped list quickly and exactly', () => {
        const groups = Array.from({ length: 100 }, (_, i) => g('k' + i, 100 + (i % 7)));
        const l = buildLayout(groups, { headers: true, expanded: 5000 });
        let rows = 0;
        for (let f = 0; f < l.count; f += 1) if (itemAt(l, f)!.kind === 'row') rows++;
        expect(rows).toBe(l.rows);
    });
});

describe('viewRangeOfFlat', () => {
    const l = buildLayout([g('a', 3), g('b', 3, true), g('c', 3)], { headers: true });
    // H:a r0 r1 r2 H:b H:c r6 r7 r8
    it('skips headers at the ends', () => {
        expect(viewRangeOfFlat(l, 0, 2)).toEqual({ first: 0, last: 1 });
        expect(viewRangeOfFlat(l, 4, 7)).toEqual({ first: 6, last: 7 });
    });
    it('never spans a collapsed group: the run with the most visible rows wins (ties go to the earlier run)', () => {
        expect(viewRangeOfFlat(l, 2, 7)).toEqual({ first: 1, last: 2 });
        const big = buildLayout([g('a', 5), g('b', 3000, true), g('c', 100)], { headers: true, expanded: null });
        expect(viewRangeOfFlat(big, 1, 30)).toEqual({ first: 3005, last: 3027 });
    });
    it('uses the group position when the range is only headers', () => {
        expect(viewRangeOfFlat(l, 4, 4)).toEqual({ first: 3, last: 3 });
        expect(viewRangeOfFlat(l, 9, 12)).toBeNull();
    });
});

describe('pixelOffset', () => {
    const sz = { header: 32, row: 10, evidence: 100 };
    it('matches the sum of item sizes for every flat index', () => {
        for (const expanded of [null, 0, 2, 3, 6]) {
            const l = buildLayout([g('a', 3), g('b', 2, true), g('c', 4)], { headers: true, expanded });
            let px = 0;
            for (let f = 0; f < l.count; f++) {
                expect(pixelOffset(l, f, sz)).toBe(px);
                const it = itemAt(l, f)!;
                px += it.kind === 'header' ? sz.header : it.kind === 'evidence' ? sz.evidence : sz.row;
            }
        }
    });
    it('works without headers', () => {
        const l = buildLayout([g('', 5)], { expanded: 1 });
        expect(pixelOffset(l, 0, sz)).toBe(0);
        expect(pixelOffset(l, 2, sz)).toBe(20);
        expect(pixelOffset(l, 3, sz)).toBe(120);
    });
});
