import { describe, expect, it } from 'vitest';

import {
    connectionKey,
    matchGroup,
    moveEntry,
    touchEntry,
    parseEntries,
    parseSelection,
    renameGroupEntries,
    resolveChecked,
    restoreRoute,
    routeToRemember,
    storeSelection,
    upsertEntry
} from '../src/lib/remember.ts';

const groups = { prod: ['b', 'a'], dev: ['c'], empty: [] };

describe('groups and keys', () => {
    it('matches a group exactly, ignoring order', () => {
        expect(matchGroup(['a', 'b'], groups)).toBe('prod');
        expect(matchGroup(['a'], groups)).toBeNull();
        expect(matchGroup(['a', 'b', 'c'], groups)).toBeNull();
        expect(matchGroup([], groups)).toBeNull();
    });
    it('builds a key from the group or the sorted ids', () => {
        expect(connectionKey(['a', 'b'], groups)).toBe('g:prod');
        expect(connectionKey(['z', 'a'], groups)).toBe('a,z');
        expect(connectionKey([], groups)).toBe('');
    });
    it('stores the group name for an exact match', () => {
        expect(storeSelection(['a', 'b'], groups)).toEqual({ group: 'prod' });
        expect(storeSelection(['a', 'c'], groups)).toEqual({ ids: ['a', 'c'] });
    });
});

describe('resolveChecked', () => {
    const existing = new Set(['a', 'b', 'c']);
    it('uses the group current members', () => {
        expect(resolveChecked({ group: 'prod' }, { prod: ['a', 'c', 'gone'] }, existing, [])).toEqual(['a', 'c']);
    });
    it('falls back to stored ids filtered to existing', () => {
        expect(resolveChecked({ group: 'nope' }, groups, existing, ['b'])).toEqual(['b']);
        expect(resolveChecked({ ids: ['a', 'gone'] }, groups, existing, ['b'])).toEqual(['a']);
    });
    it('falls back to the agent selection when nothing is stored or nothing survives', () => {
        expect(resolveChecked(null, groups, existing, ['b', 'x'])).toEqual(['b']);
        expect(resolveChecked({ ids: ['gone'] }, groups, existing, ['c'])).toEqual(['c']);
    });
    it('parses selections defensively', () => {
        expect(parseSelection({ group: 'g' })).toEqual({ group: 'g' });
        expect(parseSelection({ ids: ['a'] })).toEqual({ ids: ['a'] });
        expect(parseSelection({ ids: [1] })).toBeNull();
        expect(parseSelection('x')).toBeNull();
        expect(parseSelection(null)).toBeNull();
    });
});

describe('namespace entries', () => {
    it('moves a key to the front and trims to the bound', () => {
        let e = upsertEntry([], 'a', ['1'], 3);
        e = upsertEntry(e, 'b', ['2'], 3);
        e = upsertEntry(e, 'c', ['3'], 3);
        e = upsertEntry(e, 'a', ['9'], 3);
        expect(e).toEqual([
            { key: 'a', ns: ['9'] },
            { key: 'c', ns: ['3'] },
            { key: 'b', ns: ['2'] }
        ]);
        e = upsertEntry(e, 'd', ['4'], 3);
        expect(e.map(x => x.key)).toEqual(['d', 'a', 'c']);
    });
    it('removes a key when its filter is cleared', () => {
        expect(upsertEntry([{ key: 'a', ns: ['1'] }], 'a', [])).toEqual([]);
    });
    it('parses stored entries defensively', () => {
        expect(parseEntries('x')).toEqual([]);
        expect(
            parseEntries([{ key: 'a', ns: ['1'] }, { key: 1, ns: ['x'] }, { key: 'b', ns: [] }, { key: 'c', ns: ['ok', 2] }, { key: 'd' }])
        ).toEqual([{ key: 'a', ns: ['1'] }]);
    });
    it('migrates per-kind filters from before the filter was shared: the Pods filter, else the first kind with one', () => {
        expect(
            parseEntries([
                { key: 'a', ns: { 'apps/v1/deployments': ['d'], 'v1/pods': ['p1', 'p2'] } },
                { key: 'b', ns: { 'v1/pods': [], 'v1/secrets': 'bad', 'apps/v1/deployments': ['d'] } },
                { key: 'c', ns: { 'v1/pods': [] } }
            ])
        ).toEqual([
            { key: 'a', ns: ['p1', 'p2'] },
            { key: 'b', ns: ['d'] }
        ]);
    });
});

describe('entry updates', () => {
    const e = (key: string, ns: string[]) => ({ key, ns });
    it('touches a key to the front without changing it', () => {
        const base = [e('a', ['1']), e('b', ['2'])];
        expect(touchEntry(base, 'b').map(x => x.key)).toEqual(['b', 'a']);
        expect(touchEntry(base, 'zz')).toEqual(base);
    });
    it('moves an entry to a new key, replacing what the new key held', () => {
        const base = [e('g:x', ['old']), e('a,b', ['1'])];
        expect(moveEntry(base, 'a,b', 'g:x', ['1'])).toEqual([e('g:x', ['1'])]);
        expect(moveEntry(base, 'a,b', 'g:y', [])).toEqual([e('g:x', ['old'])]);
    });
});

describe('route memory', () => {
    it('never remembers signedout', () => {
        expect(routeToRemember('/pods', { signedout: '1' })).toBe('/pods');
        expect(routeToRemember('/pods', { signedout: '1', q: 'x' })).toBe('/pods?q=x');
    });
    it('remembers path and query without drawer keys', () => {
        expect(routeToRemember('/pods', { q: 'x', c: 'k', n: 'p', ns: 'd', tab: 'logs' })).toBe('/pods?q=x');
        expect(routeToRemember('/r/v1/secrets', {})).toBe('/r/v1/secrets');
        expect(routeToRemember('/', {})).toBeNull();
        expect(routeToRemember('//evil', {})).toBeNull();
    });
    it('restores only on the first navigation to a bare /', () => {
        expect(restoreRoute(true, '/', {}, '/security?image=x')).toBe('/security?image=x');
        expect(restoreRoute(false, '/', {}, '/security')).toBeNull();
        expect(restoreRoute(true, '/pods', {}, '/security')).toBeNull();
        expect(restoreRoute(true, '/', { signedout: '1' }, '/security')).toBeNull();
        expect(restoreRoute(true, '/', {}, null)).toBeNull();
        expect(restoreRoute(true, '/', {}, '//evil.example')).toBeNull();
        expect(restoreRoute(true, '/', {}, '/')).toBeNull();
    });
});

describe('renameGroupEntries', () => {
    const e = (key: string, ns: string[]) => ({ key, ns });
    it("moves a renamed group's filter to the new name, and is a no-op without it", () => {
        const base = [e('a,b', ['1']), e('g:duo', ['2'])];
        expect(renameGroupEntries(base, 'duo', 'pair')).toEqual([e('g:pair', ['2']), e('a,b', ['1'])]);
        expect(renameGroupEntries(base, 'other', 'x')).toEqual(base);
    });
});
