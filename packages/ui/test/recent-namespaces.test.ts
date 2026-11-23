import { describe, expect, it } from 'vitest';

import { moveRecent, parseRecent, pushRecent, withRecent } from '../src/lib/recent-namespaces.ts';

describe('recent namespaces', () => {
    it('pushes to the front, drops the 10th, and ignores empty sets', () => {
        let l: string[][] = [];
        for (let i = 1; i <= 10; i++) l = pushRecent(l, ['ns' + i]);
        expect(l).toHaveLength(9);
        expect(l[0]).toEqual(['ns10']);
        expect(l[8]).toEqual(['ns2']);
        expect(pushRecent(l, [])).toEqual(l);
    });

    it('moves a set already listed (in any order) to the front instead of repeating it', () => {
        const l = [['a'], ['b', 'c'], ['d']];
        expect(pushRecent(l, ['c', 'b'])).toEqual([['c', 'b'], ['a'], ['d']]);
        expect(pushRecent(l, ['a'])).toEqual(l);
        expect(pushRecent(l, ['b'])).toEqual([['b'], ['a'], ['b', 'c'], ['d']]); // a different set
    });

    it('keeps one list per cluster set, most recently used first', () => {
        let e = withRecent([], 'k1', ['a']);
        e = withRecent(e, 'k2', ['b']);
        e = withRecent(e, 'k1', ['a', 'b', 'c']);
        expect(e).toEqual([
            { key: 'k1', sets: [['a', 'b', 'c'], ['a']] },
            { key: 'k2', sets: [['b']] }
        ]);
        expect(withRecent(e, '', ['x'])).toEqual(e);
    });

    it('moves or copies a list to a new key', () => {
        const e = [
            { key: 'a,b', sets: [['x']] },
            { key: 'g:old', sets: [['y']] }
        ];
        expect(moveRecent(e, 'a,b', 'g:new')).toEqual([
            { key: 'g:new', sets: [['x']] },
            { key: 'g:old', sets: [['y']] }
        ]);
        expect(moveRecent(e, 'g:old', 'a,b', true)).toEqual([
            { key: 'a,b', sets: [['y']] },
            { key: 'g:old', sets: [['y']] }
        ]);
        expect(moveRecent(e, 'nope', 'a,b')).toEqual(e);
    });

    it('parses defensively', () => {
        expect(parseRecent(null)).toEqual([]);
        expect(
            parseRecent([
                { key: 'k', sets: [['a'], [], [1], 'x'] },
                { key: 1, sets: [] },
                { key: 'e', sets: [] }
            ])
        ).toEqual([{ key: 'k', sets: [['a']] }]);
    });
});
