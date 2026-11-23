import { describe, expect, it } from 'vitest';

import { isScrollKey, redirectsScrollKey, scrollStep, SCROLL_KEYS, type DrawerSpot } from '../src/lib/drawer-keys.ts';

const ev = (key: string, mods: Partial<Record<'ctrlKey' | 'metaKey' | 'altKey' | 'shiftKey', boolean>> = {}) => ({
    key,
    ctrlKey: false,
    metaKey: false,
    altKey: false,
    shiftKey: false,
    ...mods
});

describe('isScrollKey', () => {
    it('accepts the eight scroll keys, bare', () => {
        for (const k of SCROLL_KEYS) expect(isScrollKey(ev(k))).toBe(true);
        expect(isScrollKey(ev('j'))).toBe(false);
        expect(isScrollKey(ev('Enter'))).toBe(false);
    });
    it('rejects any modifier', () => {
        for (const m of ['ctrlKey', 'metaKey', 'altKey', 'shiftKey'] as const) expect(isScrollKey(ev('ArrowDown', { [m]: true }))).toBe(false);
    });
});

describe('redirectsScrollKey', () => {
    it('on a tab button only vertical keys move into the scroller', () => {
        for (const k of ['ArrowUp', 'ArrowDown', 'PageUp', 'PageDown']) expect(redirectsScrollKey('tab', k)).toBe(true);
        for (const k of ['ArrowLeft', 'ArrowRight', 'Home', 'End']) expect(redirectsScrollKey('tab', k)).toBe(false);
    });
    it('everywhere else all eight redirect', () => {
        for (const spot of ['root', 'panel', 'body', 'control'] as DrawerSpot[])
            for (const k of SCROLL_KEYS) expect(redirectsScrollKey(spot, k)).toBe(true);
    });
});

describe('scrollStep', () => {
    it('lines, sideways, pages, start and end', () => {
        expect(scrollStep('ArrowUp', 400)).toEqual({ top: -40 });
        expect(scrollStep('ArrowDown', 400)).toEqual({ top: 40 });
        expect(scrollStep('ArrowLeft', 400)).toEqual({ left: -40 });
        expect(scrollStep('ArrowRight', 400)).toEqual({ left: 40 });
        expect(scrollStep('PageDown', 400)).toEqual({ top: 350 });
        expect(scrollStep('PageUp', 400)).toEqual({ top: -350 });
        expect(scrollStep('Home', 400)).toEqual({ to: 'start' });
        expect(scrollStep('End', 400)).toEqual({ to: 'end' });
    });
    it('a page is at least a line', () => {
        expect(scrollStep('PageDown', 20)).toEqual({ top: 40 });
    });
});
