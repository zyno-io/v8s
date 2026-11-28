import { describe, expect, it } from 'vitest';

import { emptySettings, mergePatch, normalizeSettings } from '../src/lib/settings.ts';
import { clampSidebarWidth, SIDEBAR_DEFAULT, SIDEBAR_MAX, SIDEBAR_MIN, sidebarPatch, sidebarPrefs } from '../src/lib/sidebar.ts';

describe('clampSidebarWidth', () => {
    it('keeps widths inside the bounds and rounds', () => {
        expect(clampSidebarWidth(300)).toBe(300);
        expect(clampSidebarWidth(100)).toBe(SIDEBAR_MIN);
        expect(clampSidebarWidth(9999)).toBe(SIDEBAR_MAX);
        expect(clampSidebarWidth(250.6)).toBe(251);
    });
    it('falls back to the default for anything that is not a finite number', () => {
        for (const v of [NaN, Infinity, '300', null, undefined, {}]) expect(clampSidebarWidth(v)).toBe(SIDEBAR_DEFAULT);
    });
});

describe('sidebarPrefs', () => {
    it('defaults to pinned at the default width', () => {
        expect(sidebarPrefs(undefined)).toEqual({ pinned: true, width: SIDEBAR_DEFAULT });
        expect(sidebarPrefs({})).toEqual({ pinned: true, width: SIDEBAR_DEFAULT });
    });
    it('reads stored values and sanitizes malformed ones', () => {
        expect(sidebarPrefs({ sidebarPinned: false, sidebarWidth: 300 })).toEqual({ pinned: false, width: 300 });
        expect(sidebarPrefs({ sidebarPinned: 'no', sidebarWidth: 5 })).toEqual({ pinned: true, width: SIDEBAR_MIN });
    });
});

describe('sidebarPatch', () => {
    it('patches only the given keys, clamping the width, and merges into uiPrefs', () => {
        expect(sidebarPatch({ pinned: false })).toEqual({ uiPrefs: { sidebarPinned: false } });
        expect(sidebarPatch({ width: 1000 })).toEqual({ uiPrefs: { sidebarWidth: SIDEBAR_MAX } });
        const doc = mergePatch(emptySettings(), sidebarPatch({ pinned: false }));
        const next = mergePatch(doc, sidebarPatch({ width: 260 }));
        expect(sidebarPrefs(next.uiPrefs)).toEqual({ pinned: false, width: 260 });
    });
    it('normalizeSettings keeps uiPrefs and fills it when missing', () => {
        expect(normalizeSettings({}).uiPrefs).toEqual({});
        expect(normalizeSettings({ uiPrefs: { sidebarWidth: 250 } }).uiPrefs).toEqual({ sidebarWidth: 250 });
    });
});
