import { describe, expect, it } from 'vitest';

import {
    edgeActive,
    filterOptions,
    listOptions,
    moveActive,
    placePopup,
    queryTerms,
    scrollToRow,
    splitHighlight,
    typeahead,
    visibleRange,
    type SelectOption
} from '@/lib/smart-select.ts';

const opts: SelectOption<string>[] = [
    { value: 'a', label: 'default' },
    { value: 'b', label: 'kube-system' },
    { value: 'c', label: 'kube-public', disabled: true },
    { value: 'd', label: 'monitoring', subtitle: 'prometheus' },
    { value: 'e', label: 'my-default-app' }
];
const all = [0, 1, 2, 3, 4];
const dis = (i: number) => !!opts[i].disabled;

describe('filterOptions', () => {
    it('returns every index for a blank query', () => {
        expect(filterOptions(opts, '')).toEqual(all);
        expect(filterOptions(opts, '   ')).toEqual(all);
    });
    it('matches case-insensitive substrings, label prefixes first', () => {
        expect(filterOptions(opts, 'DEFAULT')).toEqual([0, 4]);
        expect(filterOptions(opts, 'def')).toEqual([0, 4]);
        expect(filterOptions(opts, 'app')).toEqual([4]);
    });
    it('requires every term and searches the subtitle', () => {
        expect(filterOptions(opts, 'kube sys')).toEqual([1]);
        expect(filterOptions(opts, 'prom')).toEqual([3]);
        expect(filterOptions(opts, 'kube zzz')).toEqual([]);
    });
    it('keeps the original order within each group', () => {
        expect(filterOptions(opts, 'e')).toEqual([0, 1, 2, 3, 4]);
        expect(filterOptions(opts, 'k')).toEqual([1, 2]);
    });
    it('splits terms on whitespace', () => {
        expect(queryTerms('  Kube   Sys ')).toEqual(['kube', 'sys']);
        expect(queryTerms('')).toEqual([]);
    });
});

describe('moveActive', () => {
    it('steps one at a time and clamps at the ends', () => {
        expect(moveActive(all, dis, 0, 1)).toBe(1);
        expect(moveActive(all, dis, 4, 1)).toBe(4);
        expect(moveActive(all, dis, 0, -1)).toBe(0);
    });
    it('skips disabled rows in the travel direction', () => {
        expect(moveActive(all, dis, 1, 1)).toBe(3);
        expect(moveActive(all, dis, 3, -1)).toBe(1);
    });
    it('starts from the first row going down and the last going up when nothing is active', () => {
        expect(moveActive(all, dis, -1, 1)).toBe(0);
        expect(moveActive(all, dis, -1, -1)).toBe(4);
    });
    it('pages by a larger step and lands on the last enabled row when it overshoots', () => {
        expect(moveActive(all, dis, 0, 8)).toBe(4);
        expect(moveActive(all, dis, 4, -8)).toBe(0);
    });
    it('stays on an enabled row when the rest below are disabled', () => {
        const items = [0, 2];
        expect(moveActive(items, dis, 0, 1)).toBe(0);
    });
    it('returns -1 for an empty or all-disabled list', () => {
        expect(moveActive([], dis, -1, 1)).toBe(-1);
        expect(moveActive([2], dis, -1, 1)).toBe(-1);
    });
});

describe('edgeActive', () => {
    it('finds the first and last enabled rows', () => {
        expect(edgeActive(all, dis, false)).toBe(0);
        expect(edgeActive(all, dis, true)).toBe(4);
        expect(edgeActive([2, 3], dis, false)).toBe(1);
        expect(edgeActive([3, 2], dis, true)).toBe(0);
    });
});

describe('typeahead buffer', () => {
    const o = listOptions(['Alpha', 'Beta', 'Gamma', 'Gizmo']);
    const idx = [0, 1, 2, 3];
    it('a two-letter buffer lands on the second-letter match, not just the first letter', () => {
        expect(typeahead(idx, o, 'g', -1)).toBe(2);
        expect(typeahead(idx, o, 'ga', 2)).toBe(2);
        expect(typeahead(idx, o, 'gi', 2)).toBe(3);
    });
});

describe('typeahead', () => {
    it('jumps to the next label with the prefix', () => {
        expect(typeahead(all, opts, 'k', -1)).toBe(1);
        expect(typeahead(all, opts, 'm', -1)).toBe(3);
        expect(typeahead(all, opts, 'mo', 3)).toBe(3);
        expect(typeahead(all, opts, 'my', 3)).toBe(4);
    });
    it('cycles on a repeated letter and skips disabled rows', () => {
        const o: SelectOption<string>[] = [
            { value: 'a', label: 'alpha' },
            { value: 'b', label: 'avocado' },
            { value: 'c', label: 'apple', disabled: true }
        ];
        expect(typeahead([0, 1, 2], o, 'a', 0)).toBe(1);
        expect(typeahead([0, 1, 2], o, 'a', 1)).toBe(0);
        expect(typeahead([0, 1, 2], o, 'aa', 0)).toBe(1);
    });
    it('returns -1 with no match or an empty buffer', () => {
        expect(typeahead(all, opts, 'zz', 0)).toBe(-1);
        expect(typeahead(all, opts, '', 0)).toBe(-1);
    });
});

describe('splitHighlight', () => {
    it('returns the text whole without terms or matches', () => {
        expect(splitHighlight('kube-system', [])).toEqual([{ text: 'kube-system', hit: false }]);
        expect(splitHighlight('kube-system', ['zzz'])).toEqual([{ text: 'kube-system', hit: false }]);
    });
    it('marks matches case-insensitively, keeping the original casing', () => {
        expect(splitHighlight('Kube-System', ['sys'])).toEqual([
            { text: 'Kube-', hit: false },
            { text: 'Sys', hit: true },
            { text: 'tem', hit: false }
        ]);
    });
    it('finds every occurrence and merges overlapping and adjacent terms', () => {
        expect(splitHighlight('abab', ['ab'])).toEqual([{ text: 'abab', hit: true }]);
        expect(splitHighlight('abcd', ['ab', 'bc'])).toEqual([
            { text: 'abc', hit: true },
            { text: 'd', hit: false }
        ]);
    });
    it('keeps offsets right when lowercasing changes the length (İ)', () => {
        expect(splitHighlight('İstanbul-prod', ['prod'])).toEqual([
            { text: 'İstanbul-', hit: false },
            { text: 'prod', hit: true }
        ]);
    });
    it('treats regex metacharacters in a term literally', () => {
        expect(
            splitHighlight('a.b (x)', ['.', '(x)'])
                .filter(x => x.hit)
                .map(x => x.text)
        ).toEqual(['.', '(x)']);
    });
    it('never interprets markup: the text comes back verbatim', () => {
        const segs = splitHighlight('<img src=x onerror=alert(1)>', ['img']);
        expect(segs.map(s => s.text).join('')).toBe('<img src=x onerror=alert(1)>');
        expect(segs.find(s => s.hit)?.text).toBe('img');
    });
});

describe('windowing', () => {
    it('renders the rows in view plus overscan, clamped to the list', () => {
        expect(visibleRange(0, 288, 32, 1000, 4)).toEqual([0, 13]);
        expect(visibleRange(3200, 288, 32, 1000, 4)).toEqual([96, 113]);
        expect(visibleRange(31712, 288, 32, 1000, 4)).toEqual([987, 1000]);
        expect(visibleRange(0, 288, 32, 3, 4)).toEqual([0, 3]);
    });
    it('scrolls only as far as needed to reveal a row', () => {
        expect(scrollToRow(2, 0, 100, 32)).toBe(0);
        expect(scrollToRow(5, 0, 100, 32)).toBe(92);
        expect(scrollToRow(1, 200, 100, 32)).toBe(32);
    });
});

describe('placePopup', () => {
    const viewport = { width: 1000, height: 800 };
    it('opens below the trigger when there is room', () => {
        const p = placePopup({ rect: { top: 100, bottom: 132, left: 50, width: 120 }, viewport, wanted: 300 });
        expect(p.above).toBe(false);
        expect(p.top).toBe(136);
        expect(p.left).toBe(50);
        expect(p.width).toBe(180);
        expect(p.maxHeight).toBe(300);
    });
    it('flips above near the bottom of the viewport', () => {
        const p = placePopup({ rect: { top: 700, bottom: 732, left: 50, width: 200 }, viewport, wanted: 300 });
        expect(p.above).toBe(true);
        expect(p.bottom).toBe(800 - 700 + 4);
        expect(p.top).toBeUndefined();
        expect(p.maxHeight).toBe(300);
    });
    it('stays below when neither side fits but below has more room', () => {
        const p = placePopup({ rect: { top: 100, bottom: 132, left: 0, width: 200 }, viewport: { width: 1000, height: 260 }, wanted: 300 });
        expect(p.above).toBe(false);
        expect(p.maxHeight).toBeLessThanOrEqual(300);
        expect(p.maxHeight).toBeGreaterThan(90);
    });
    it('keeps the popup inside the viewport horizontally', () => {
        const p = placePopup({ rect: { top: 10, bottom: 42, left: 950, width: 40 }, viewport, wanted: 100 });
        expect(p.left + p.width).toBeLessThanOrEqual(1000 - 8);
        const narrow = placePopup({ rect: { top: 10, bottom: 42, left: 5, width: 40 }, viewport: { width: 200, height: 800 }, wanted: 100 });
        expect(narrow.width).toBeLessThanOrEqual(184);
        expect(narrow.left).toBeGreaterThanOrEqual(8);
    });
});

describe('listOptions', () => {
    it('maps values to options, with an optional label', () => {
        expect(listOptions(['a', 'b'])).toEqual([
            { value: 'a', label: 'a' },
            { value: 'b', label: 'b' }
        ]);
        expect(listOptions([1, 2], n => `Last ${n}`)[1]).toEqual({ value: 2, label: 'Last 2' });
    });
});
