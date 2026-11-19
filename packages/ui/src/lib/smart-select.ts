/*
 * Pure logic for SmartSelect.vue: filtering, keyboard index movement, highlight splitting, windowing and placement.
 * Kept free of Vue and the DOM so it unit-tests directly. Modeled on vue-foundation's VfSmartSelect
 * (https://github.com/zyno-io/vue-foundation, `@signal24/vue-foundation`, MIT), rewritten with no lodash or mark.js.
 */

export interface SelectOption<T = string | number | boolean | null> {
    value: T;
    label: string;
    /** Muted text after the label (searched too). */
    subtitle?: string;
    disabled?: boolean;
    /** Native tooltip of the row. */
    title?: string;
    /** `data-testid` of the row. */
    testid?: string;
}

/** More options than this and the list gets a search field. */
export const SEARCH_MIN_OPTIONS = 8;

/** Lowercased whitespace-separated terms; empty for a blank query. */
export function queryTerms(query: string): string[] {
    return query.toLowerCase().split(/\s+/).filter(Boolean);
}

/**
 * Indices of the options matching every term (case-insensitive substring of label or subtitle), label-prefix matches first,
 * otherwise in the original order. A blank query returns every index.
 */
export function filterOptions(options: readonly SelectOption<unknown>[], query: string): number[] {
    const terms = queryTerms(query);
    const out: number[] = [];
    if (!terms.length) {
        for (let i = 0; i < options.length; i++) out.push(i);
        return out;
    }
    const prefix: number[] = [];
    for (let i = 0; i < options.length; i++) {
        const o = options[i];
        const label = o.label.toLowerCase();
        const hay = o.subtitle ? label + ' ' + o.subtitle.toLowerCase() : label;
        if (!terms.every(t => hay.includes(t))) continue;
        (label.startsWith(terms[0]) ? prefix : out).push(i);
    }
    return prefix.concat(out);
}

/**
 * The next enabled position in `items` (indices into options) from `from`, moving by `step` (+1/-1 one at a time, larger for pages),
 * clamped at the ends (no wrap, like a native select). `from` of -1 means nothing is active. Returns -1 when nothing is enabled.
 */
export function moveActive(items: readonly number[], isDisabled: (optionIndex: number) => boolean, from: number, step: number): number {
    const n = items.length;
    if (!n) return -1;
    const dir = step < 0 ? -1 : 1;
    let pos = from < 0 ? (dir > 0 ? -1 : n) : from;
    pos = Math.max(-1, Math.min(n, pos + step));
    // Land on the nearest enabled row in the travel direction, then back off if that ran past the end.
    for (let p = pos; p >= 0 && p < n; p += dir) if (!isDisabled(items[p])) return p;
    for (let p = Math.max(0, Math.min(n - 1, pos)); p >= 0 && p < n; p -= dir) if (!isDisabled(items[p])) return p;
    return -1;
}

/** The first or last enabled position (Home / End). */
export function edgeActive(items: readonly number[], isDisabled: (optionIndex: number) => boolean, end: boolean): number {
    return end ? moveActive(items, isDisabled, items.length, -1) : moveActive(items, isDisabled, -1, 1);
}

/**
 * Type-ahead on a list with no search field: the next enabled position after `from` whose label starts with `buffer`
 * (wrapping once; a repeated single letter cycles). -1 when none.
 */
export function typeahead(items: readonly number[], options: readonly SelectOption<unknown>[], buffer: string, from: number): number {
    const b = buffer.toLowerCase();
    const n = items.length;
    if (!b || !n) return -1;
    const single = b.length > 1 && [...b].every(c => c === b[0]);
    const needle = single ? b[0] : b;
    // A growing buffer may keep the current row; a repeated letter moves on.
    const start = single || b.length === 1 ? from + 1 : Math.max(from, 0);
    for (let k = 0; k < n; k++) {
        const p = (((start + k) % n) + n) % n;
        const o = options[items[p]];
        if (!o.disabled && o.label.toLowerCase().startsWith(needle)) return p;
    }
    return -1;
}

export interface Segment {
    text: string;
    hit: boolean;
}

/** Splits `text` into runs, marking the parts the terms match (overlaps merged). Plain text only: callers render each run as text. */
export function splitHighlight(text: string, terms: readonly string[]): Segment[] {
    if (!terms.length || !text) return [{ text, hit: false }];
    const lower = text.toLowerCase();
    const ranges: [number, number][] = [];
    for (const t of terms) {
        if (!t) continue;
        for (let i = lower.indexOf(t); i >= 0; i = lower.indexOf(t, i + t.length)) ranges.push([i, i + t.length]);
    }
    if (!ranges.length) return [{ text, hit: false }];
    ranges.sort((a, b) => a[0] - b[0] || a[1] - b[1]);
    const merged: [number, number][] = [ranges[0]];
    for (const r of ranges.slice(1)) {
        const last = merged[merged.length - 1];
        if (r[0] <= last[1]) last[1] = Math.max(last[1], r[1]);
        else merged.push(r);
    }
    const out: Segment[] = [];
    let at = 0;
    for (const [s, e] of merged) {
        if (s > at) out.push({ text: text.slice(at, s), hit: false });
        out.push({ text: text.slice(s, e), hit: true });
        at = e;
    }
    if (at < text.length) out.push({ text: text.slice(at), hit: false });
    return out;
}

/** Rows to render for a fixed-height list scrolled to `scrollTop`: [start, end) with `overscan` rows each side. */
export function visibleRange(scrollTop: number, viewport: number, rowH: number, count: number, overscan = 4): [number, number] {
    const first = Math.floor(scrollTop / rowH);
    const last = Math.ceil((scrollTop + viewport) / rowH);
    return [Math.max(0, first - overscan), Math.min(count, last + overscan)];
}

/** Scroll offset that brings row `index` fully into view, or `scrollTop` unchanged when it already is. */
export function scrollToRow(index: number, scrollTop: number, viewport: number, rowH: number): number {
    const top = index * rowH;
    if (top < scrollTop) return top;
    if (top + rowH > scrollTop + viewport) return top + rowH - viewport;
    return scrollTop;
}

export interface Placement {
    left: number;
    width: number;
    /** Distance from the viewport top (opening down) or bottom (opening up). */
    top?: number;
    bottom?: number;
    maxHeight: number;
    above: boolean;
}

export interface PlacementInput {
    /** The trigger's viewport rect. */
    rect: { top: number; bottom: number; left: number; width: number };
    viewport: { width: number; height: number };
    /** Height the list wants (its content, capped). */
    wanted: number;
    minWidth?: number;
    margin?: number;
    gap?: number;
}

/** Viewport-relative placement: below the trigger, flipped above when there is not room below and there is more above. */
export function placePopup({ rect, viewport, wanted, minWidth = 180, margin = 8, gap = 4 }: PlacementInput): Placement {
    const below = viewport.height - rect.bottom - gap - margin;
    const above = rect.top - gap - margin;
    const flip = below < Math.min(wanted, 160) && above > below;
    const width = Math.min(Math.max(rect.width, minWidth), viewport.width - margin * 2);
    const left = Math.max(margin, Math.min(rect.left, viewport.width - width - margin));
    const room = Math.max(flip ? above : below, 0);
    const maxHeight = Math.min(wanted, Math.max(room, Math.min(wanted, 96)));
    return flip
        ? { left, width, bottom: viewport.height - rect.top + gap, maxHeight, above: true }
        : { left, width, top: rect.bottom + gap, maxHeight, above: false };
}

/** Options from a plain list; the label defaults to the value's string form. */
export function listOptions<T extends string | number>(values: readonly T[], label: (v: T) => string = v => String(v)): SelectOption<T>[] {
    return values.map(v => ({ value: v, label: label(v) }));
}
