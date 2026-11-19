import { describe, expect, it } from 'vitest';

import {
    compactFilters,
    connectionKey,
    matchGroup,
    parseEntries,
    parseSelection,
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
    it('compacts empty filters', () => {
        expect(compactFilters({ 'v1/pods': [], 'v1/secrets': ['x'] })).toEqual({ 'v1/secrets': ['x'] });
        expect(compactFilters({ 'v1/pods': [] })).toBeNull();
    });
    it('moves a key to the front and trims to the bound', () => {
        let e = upsertEntry([], 'a', { g: ['1'] }, 3);
        e = upsertEntry(e, 'b', { g: ['2'] }, 3);
        e = upsertEntry(e, 'c', { g: ['3'] }, 3);
        e = upsertEntry(e, 'a', { g: ['9'] }, 3);
        expect(e.map(x => x.key)).toEqual(['a', 'c', 'b']);
        e = upsertEntry(e, 'd', { g: ['4'] }, 3);
        expect(e.map(x => x.key)).toEqual(['d', 'a', 'c']);
    });
    it('removes a key when its filters are cleared', () => {
        expect(upsertEntry([{ key: 'a', ns: { g: ['1'] } }], 'a', null)).toEqual([]);
    });
    it('parses stored entries defensively', () => {
        expect(parseEntries('x')).toEqual([]);
        expect(parseEntries([{ key: 'a', ns: { g: ['1'], h: 'bad' } }, { key: 1 }, { key: 'b', ns: { g: [] } }])).toEqual([
            { key: 'a', ns: { g: ['1'] } }
        ]);
    });
});

describe('route memory', () => {
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
