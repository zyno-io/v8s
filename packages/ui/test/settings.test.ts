import { describe, expect, it } from 'vitest';

import {
    deleteGroupPatch,
    dismissedLinks,
    dismissLinkPatch,
    emptySettings,
    groupList,
    groupPatch,
    isProductionCluster,
    layoutFor,
    layoutPatch,
    mergePatch,
    moveStar,
    normalizeSettings,
    productionGroups,
    productionGroupsPatch,
    renameGroupPatch,
    toggleStar
} from '../src/lib/settings.ts';

describe('mergePatch (RFC 7386)', () => {
    it('merges objects, replaces arrays and scalars, deletes on null', () => {
        const doc = { a: { b: 1, c: 2 }, list: [1, 2], keep: true };
        expect(mergePatch(doc, { a: { b: null, d: 3 }, list: [9] })).toEqual({ a: { c: 2, d: 3 }, list: [9], keep: true });
    });

    it('does not mutate the target', () => {
        const doc = { a: { b: 1 } };
        mergePatch(doc, { a: { b: 2 } });
        expect(doc).toEqual({ a: { b: 1 } });
    });

    it('treats a non-object target as empty', () => {
        expect(mergePatch(5 as unknown, { x: 1 })).toEqual({ x: 1 });
        expect(mergePatch({ x: 1 }, 'str')).toBe('str');
    });
});

describe('settings helpers', () => {
    it('normalizes a partial document', () => {
        const s = normalizeSettings({ stars: ['v1/pods'] });
        expect(s.stars).toEqual(['v1/pods']);
        expect(s.groups).toEqual({});
        expect(s.autoSwitchWrite).toBe(true);
        expect(normalizeSettings(null)).toEqual(emptySettings());
    });

    it('toggles and reorders stars', () => {
        expect(toggleStar(['a'], 'b')).toEqual(['a', 'b']);
        expect(toggleStar(['a', 'b'], 'a')).toEqual(['b']);
        expect(moveStar(['a', 'b', 'c'], 0, 2)).toEqual(['b', 'c', 'a']);
        expect(moveStar(['a', 'b'], 5, 0)).toEqual(['a', 'b']);
    });

    it('stores column layouts per gvr and applies them through a merge patch', () => {
        let s = emptySettings();
        s = mergePatch(s, layoutPatch('v1/pods', { widths: { name: 300 }, hidden: ['node'] }));
        expect(layoutFor(s, 'v1/pods')).toEqual({ widths: { name: 300 }, hidden: ['node'] });
        s = mergePatch(s, layoutPatch('apps/v1/deployments', { hidden: ['images'] }));
        expect(layoutFor(s, 'v1/pods').hidden).toEqual(['node']);
        expect(layoutFor(s, 'apps/v1/deployments').hidden).toEqual(['images']);
        expect(layoutFor(s, 'nope')).toEqual({});
        // resetting replaces the hidden list (arrays replace) and clears widths (objects merge to empty)
        s = mergePatch(s, layoutPatch('v1/pods', {}));
        expect(layoutFor(s, 'v1/pods').hidden).toEqual([]);
    });

    it('builds group patches', () => {
        let s = emptySettings();
        s = mergePatch(s, groupPatch('production', ['a', 'b']));
        s = mergePatch(s, groupPatch('eu', ['b']));
        expect(groupList(s.groups)).toEqual([
            ['eu', ['b']],
            ['production', ['a', 'b']]
        ]);
        s = mergePatch(s, renameGroupPatch(s.groups, 'eu', 'europe'));
        expect(Object.keys(s.groups).sort()).toEqual(['europe', 'production']);
        expect(s.groups.europe).toEqual(['b']);
        s = mergePatch(s, deleteGroupPatch('production'));
        expect(Object.keys(s.groups)).toEqual(['europe']);
    });

    it('keeps the production-group list in problemPrefs, defaulting to "production"', () => {
        let s = emptySettings();
        expect(productionGroups(s)).toEqual(['production']);
        s = mergePatch(s, productionGroupsPatch(['prod', 'dr']));
        expect(productionGroups(s)).toEqual(['prod', 'dr']);
        s = mergePatch(s, productionGroupsPatch([]));
        expect(productionGroups(s)).toEqual([]);
        expect(isProductionCluster(['eu', 'prod'], ['prod'])).toBe(true);
        expect(isProductionCluster(['eu'], ['prod'])).toBe(false);
    });

    it('records dismissed link suggestions in acks without losing earlier ones', () => {
        let s = emptySettings();
        s = mergePatch(s, dismissLinkPatch('a', 'a-admin'));
        s = mergePatch(s, dismissLinkPatch('b', 'b-admin'));
        expect([...dismissedLinks(s)].sort()).toEqual(['a|a-admin', 'b|b-admin']);
    });
});

describe('bookmarks', () => {
    it('drops anything that is not an in-app path', () => {
        const s = normalizeSettings({
            bookmarks: [
                { name: 'ok', url: '/pods?c=a' },
                { name: 'abs', url: 'https://evil.example/' },
                { name: 'proto', url: '//evil.example/' },
                { name: 'js', url: 'javascript:alert(1)' },
                { name: 'none' } as never
            ]
        });
        expect(s.bookmarks.map(b => b.name)).toEqual(['ok']);
    });
});
