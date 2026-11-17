import { describe, expect, it } from 'vitest';

import { linkAllPatch, pairOptions } from '../src/lib/link-ops.ts';

const ctx = (...names: string[]) => names.map(n => ({ id: n, name: n }));

describe('linkAllPatch', () => {
    it('builds one links patch and collects the write contexts', () => {
        const { patch, writes } = linkAllPatch([
            { read: 'a-operator', write: 'a-admin' },
            { read: 'b', write: 'b-admin' }
        ]);
        expect(patch).toEqual({ links: { 'a-operator': 'a-admin', b: 'b-admin' } });
        expect([...writes]).toEqual(['a-admin', 'b-admin']);
    });

    it('is empty for no pairs', () => {
        expect(linkAllPatch([])).toEqual({ patch: { links: {} }, writes: new Set() });
    });
});

describe('pairOptions', () => {
    it('drops contexts already linked as read or write', () => {
        expect(pairOptions(ctx('a', 'b', 'c', 'd'), { a: 'b' }).map(c => c.id)).toEqual(['c', 'd']);
    });

    it('drops the context chosen on the other side', () => {
        expect(pairOptions(ctx('a', 'b', 'c'), {}, 'b').map(c => c.id)).toEqual(['a', 'c']);
    });
});
