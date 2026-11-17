/**
 * Grouping inside one virtualized list. The agent groups by filters only, so the UI sorts the single view by the group column
 * first and learns each group's size from a one-row count view. This module maps the flat list index to what to draw:
 * a group header, a row of the view (by view index) or the one evidence item inserted after the expanded row.
 * Pure; no Vue.
 */

export interface GroupSpec {
    key: string;
    count: number;
    collapsed: boolean;
}

export type FlatItem =
    | { kind: 'header'; group: number; key: string; count: number; collapsed: boolean }
    | { kind: 'row'; group: number; viewIndex: number }
    | { kind: 'evidence'; group: number; viewIndex: number };

interface LayoutGroup {
    /** Index in the input `groups` array. */
    src: number;
    key: string;
    count: number;
    collapsed: boolean;
    /** First flat index of the group (its header, or its first row without headers). */
    flatStart: number;
    /** View index of the group's first row. */
    viewStart: number;
    /** Flat items of the group: header + visible rows + the evidence item. */
    flatLen: number;
    /** Row offset (within the group) after which the evidence item sits, or -1. */
    evidenceAfter: number;
}

export interface Layout {
    /** Total flat items. */
    count: number;
    headers: boolean;
    groups: LayoutGroup[];
    /** Total rows across groups (the view's expected total). */
    rows: number;
}

export interface LayoutOptions {
    /** Draw a header row per group (grouped mode). Without headers collapsing is ignored. */
    headers?: boolean;
    /** View index of the expanded row: one evidence item follows it (ignored when its group is collapsed). */
    expanded?: number | null;
}

/** Empty groups are hidden. View indexes run through the groups in order, collapsed or not. */
export function buildLayout(groups: readonly GroupSpec[], opts: LayoutOptions = {}): Layout {
    const headers = !!opts.headers;
    const out: LayoutGroup[] = [];
    let flat = 0;
    let view = 0;
    const expanded = opts.expanded ?? -1;
    groups.forEach((g, src) => {
        const count = Math.max(0, Math.floor(g.count));
        if (count === 0) return;
        const collapsed = headers && g.collapsed;
        const evidenceAfter = !collapsed && expanded >= view && expanded < view + count ? expanded - view : -1;
        const flatLen = (headers ? 1 : 0) + (collapsed ? 0 : count) + (evidenceAfter >= 0 ? 1 : 0);
        out.push({ src, key: g.key, count, collapsed, flatStart: flat, viewStart: view, flatLen, evidenceAfter });
        flat += flatLen;
        view += count;
    });
    return { count: flat, headers, groups: out, rows: view };
}

function groupOfFlat(l: Layout, flat: number): number {
    let lo = 0;
    let hi = l.groups.length - 1;
    while (lo < hi) {
        const mid = (lo + hi + 1) >> 1;
        if (l.groups[mid].flatStart <= flat) lo = mid;
        else hi = mid - 1;
    }
    return lo;
}

/** What to draw at a flat index; undefined when out of range. */
export function itemAt(l: Layout, flat: number): FlatItem | undefined {
    if (flat < 0 || flat >= l.count || !l.groups.length) return undefined;
    const gi = groupOfFlat(l, flat);
    const g = l.groups[gi];
    let off = flat - g.flatStart;
    if (l.headers) {
        if (off === 0) return { kind: 'header', group: g.src, key: g.key, count: g.count, collapsed: g.collapsed };
        off--;
    }
    if (g.evidenceAfter >= 0) {
        if (off === g.evidenceAfter + 1) return { kind: 'evidence', group: g.src, viewIndex: g.viewStart + g.evidenceAfter };
        if (off > g.evidenceAfter + 1) off--;
    }
    return { kind: 'row', group: g.src, viewIndex: g.viewStart + off };
}

/** Flat index of a view row; -1 when its group is collapsed or the index is out of range. */
export function flatIndexOfView(l: Layout, viewIndex: number): number {
    if (viewIndex < 0 || viewIndex >= l.rows) return -1;
    let lo = 0;
    let hi = l.groups.length - 1;
    while (lo < hi) {
        const mid = (lo + hi + 1) >> 1;
        if (l.groups[mid].viewStart <= viewIndex) lo = mid;
        else hi = mid - 1;
    }
    const g = l.groups[lo];
    if (g.collapsed) return -1;
    const off = viewIndex - g.viewStart;
    return g.flatStart + (l.headers ? 1 : 0) + off + (g.evidenceAfter >= 0 && off > g.evidenceAfter ? 1 : 0);
}

/** Flat index of a group's header (-1 without headers or for a hidden group). */
export function flatIndexOfHeader(l: Layout, srcGroup: number): number {
    if (!l.headers) return -1;
    return l.groups.find(g => g.src === srcGroup)?.flatStart ?? -1;
}

/** First and last row view index within `[lo, hi]` flat items (headers and evidence skipped); null when they hold no row. */
function rowSpan(l: Layout, lo: number, hi: number): { first: number; last: number } | null {
    let first = -1;
    let last = -1;
    // Only the two ends can be headers/evidence, so probing a few items each side is enough.
    for (let i = lo; i <= hi && i < lo + 3; i++) {
        const it = itemAt(l, i);
        if (it?.kind === 'row') {
            first = it.viewIndex;
            break;
        }
    }
    for (let i = hi; i >= lo && i > hi - 3; i--) {
        const it = itemAt(l, i);
        if (it?.kind === 'row') {
            last = it.viewIndex;
            break;
        }
    }
    if (first < 0 && last < 0) return null;
    return { first: first < 0 ? last : first, last: last < 0 ? first : last };
}

/**
 * The view indexes to hold for a flat range: the rows of the range's longest contiguous run. A collapsed group inside the range
 * breaks contiguity (its rows are skipped in the flat list but not in the view), so the run with the most visible rows wins and the
 * window never spans the hidden rows. Null when the range holds no row.
 */
export function viewRangeOfFlat(l: Layout, firstFlat: number, lastFlat: number): { first: number; last: number } | null {
    const a = Math.max(0, firstFlat);
    const b = Math.min(l.count - 1, lastFlat);
    if (b < a) return null;
    let best: { first: number; last: number; rows: number } | null = null;
    let cur: { first: number; last: number; rows: number } | null = null;
    const close = () => {
        if (cur && (!best || cur.rows > best.rows)) best = cur;
        cur = null;
    };
    for (const g of l.groups) {
        const gEnd = g.flatStart + g.flatLen - 1;
        if (gEnd < a) continue;
        if (g.flatStart > b) break;
        if (g.collapsed) {
            close();
            continue;
        }
        const span = rowSpan(l, Math.max(a, g.flatStart), Math.min(b, gEnd));
        if (!span) continue;
        const rows = span.last - span.first + 1;
        cur = cur ? { first: cur.first, last: span.last, rows: cur.rows + rows } : { first: span.first, last: span.last, rows };
    }
    close();
    if (best) {
        const r = best as { first: number; last: number };
        return { first: r.first, last: r.last };
    }
    // The range is all headers/evidence/collapsed groups: use the first item's group position.
    const it = itemAt(l, a);
    if (!it) return null;
    const g = l.groups.find(x => x.src === it.group);
    return g ? { first: g.viewStart, last: g.viewStart } : null;
}

export interface Sizes {
    header: number;
    row: number;
    evidence: number;
}

function groupPx(g: LayoutGroup, headers: boolean, sz: Sizes): number {
    return (headers ? sz.header : 0) + (g.collapsed ? 0 : g.count * sz.row) + (g.evidenceAfter >= 0 ? sz.evidence : 0);
}

/** Pixel offset of a flat item from the top of the list, for fixed item heights. */
export function pixelOffset(l: Layout, flat: number, sz: Sizes): number {
    if (flat <= 0 || !l.groups.length) return 0;
    const gi = groupOfFlat(l, Math.min(flat, l.count - 1));
    let px = 0;
    for (let k = 0; k < gi; k++) px += groupPx(l.groups[k], l.headers, sz);
    const g = l.groups[gi];
    let off = flat - g.flatStart;
    if (l.headers) {
        if (off <= 0) return px;
        px += sz.header;
        off--;
    }
    if (g.evidenceAfter >= 0) {
        if (off === g.evidenceAfter + 1) return px + off * sz.row;
        if (off > g.evidenceAfter + 1) return px + (off - 1) * sz.row + sz.evidence;
    }
    return px + off * sz.row;
}
