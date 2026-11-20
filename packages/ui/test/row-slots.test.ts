import { describe, expect, it } from 'vitest';

import { RowSlots } from '../src/lib/row-slots.ts';

const range = (a: number, b: number) => Array.from({ length: b - a }, (_, i) => a + i);

describe('RowSlots', () => {
    it('keeps the slot of a row that stays visible and gives a scrolled-in row a freed slot', () => {
        const s = new RowSlots();
        const first = s.assign(range(0, 5));
        expect(first).toEqual([0, 1, 2, 3, 4]);
        const next = s.assign(range(2, 7));
        expect(next.slice(0, 3)).toEqual([2, 3, 4]); // rows 2..4 stay put
        expect(new Set(next.slice(3))).toEqual(new Set([0, 1])); // rows 5, 6 reuse the slots of 0, 1
    });

    it('a jump past the whole window reuses every slot', () => {
        const s = new RowSlots();
        s.assign(range(0, 30));
        expect(new Set(s.assign(range(5000, 5030)))).toEqual(new Set(range(0, 30)));
    });

    it('grows when the window grows and never hands out a slot twice', () => {
        const s = new RowSlots();
        s.assign(range(0, 3));
        const out = s.assign(range(1, 6));
        expect(new Set(out).size).toBe(5);
        expect(Math.max(...out)).toBe(4);
    });
});
