import { describe, expect, it } from 'vitest';

import { splitRole, suggestLinks } from '../src/lib/link-suggest.ts';

const ctx = (...names: string[]) => names.map(n => ({ id: n, name: n }));

describe('splitRole', () => {
    it('recognizes role suffixes, longest first', () => {
        expect(splitRole('prod-us-cluster-admin')).toEqual({ base: 'prod-us', role: 'cluster-admin', kind: 'write' });
        expect(splitRole('prod-us-operator')).toEqual({ base: 'prod-us', role: 'operator', kind: 'read' });
        expect(splitRole('staging')).toEqual({ base: 'staging', role: '', kind: 'none' });
        expect(splitRole('admin')).toEqual({ base: 'admin', role: '', kind: 'none' });
    });
});

describe('suggestLinks', () => {
    it('pairs a read-role context with a write-role context of the same base', () => {
        expect(suggestLinks(ctx('edge-operator', 'edge-cluster-admin', 'other'), {})).toEqual([
            { read: 'edge-operator', write: 'edge-cluster-admin', base: 'edge' }
        ]);
    });

    it('treats the bare base name as the read side', () => {
        expect(suggestLinks(ctx('prod-us', 'prod-us-admin'), {})).toEqual([{ read: 'prod-us', write: 'prod-us-admin', base: 'prod-us' }]);
    });

    it('does not pair two reads, two writes, or ambiguous groups', () => {
        expect(suggestLinks(ctx('x-operator', 'x-readonly'), {})).toEqual([]);
        expect(suggestLinks(ctx('x-admin', 'x-cluster-admin'), {})).toEqual([]);
        expect(suggestLinks(ctx('x', 'x-operator', 'x-admin'), {})).toEqual([]);
    });

    it('skips pairs that are linked already or dismissed', () => {
        const list = ctx('a-operator', 'a-admin', 'b', 'b-admin');
        expect(suggestLinks(list, { 'a-operator': 'a-admin' }).map(s => s.read)).toEqual(['b']);
        expect(suggestLinks(list, {}, new Set(['b|b-admin'])).map(s => s.read)).toEqual(['a-operator']);
    });

    it('is case-insensitive on the base', () => {
        expect(suggestLinks(ctx('Prod-operator', 'prod-admin'), {})).toHaveLength(1);
    });
});
