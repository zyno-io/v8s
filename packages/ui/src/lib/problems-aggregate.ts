/**
 * Cross-cluster aggregation (UI side; the agent does not aggregate). The whole set is loaded sorted by
 * class, kind, namespace, name (then cluster), so equal problems are adjacent. Adjacent rows with the same identity collapse
 * into one display row; it expands to its per-cluster rows. Pure functions over row indexes (no row copies).
 */

export interface AggGroup {
    /** Identity key shared by the collapsed rows. */
    key: string;
    /** View index of the group's first row. */
    first: number;
    /** Number of per-cluster rows. */
    n: number;
}

/** Collapses adjacent equal keys. `keyAt(i)` is the identity of the i-th row of the view. */
export function aggregateRows(total: number, keyAt: (i: number) => string | undefined): AggGroup[] {
    const out: AggGroup[] = [];
    let cur: AggGroup | null = null;
    for (let i = 0; i < total; i++) {
        const k = keyAt(i);
        if (k === undefined) {
            cur = null; // a hole (row not loaded): never merge across it
            out.push({ key: `#${i}`, first: i, n: 1 });
            continue;
        }
        if (cur && cur.key === k) cur.n++;
        else {
            cur = { key: k, first: i, n: 1 };
            out.push(cur);
        }
    }
    return out;
}

export type AggItem =
    /** The display row of group `g` (its first per-cluster row is `vi`). */
    | { kind: 'row'; g: number; vi: number }
    /** A per-cluster row of an expanded group. */
    | { kind: 'child'; g: number; vi: number }
    /** The evidence item of the row `vi`. */
    | { kind: 'evidence'; g: number; vi: number };

/**
 * The flat list: one item per group, plus its per-cluster rows when expanded (more than one cluster), plus one evidence item
 * after the row whose evidence is open (`evidenceVi`: a single-cluster display row or a per-cluster child).
 */
export function buildAggFlat(groups: readonly AggGroup[], expanded: ReadonlySet<string>, evidenceVi: number | null): AggItem[] {
    const out: AggItem[] = [];
    groups.forEach((g, gi) => {
        out.push({ kind: 'row', g: gi, vi: g.first });
        if (g.n === 1) {
            if (evidenceVi === g.first) out.push({ kind: 'evidence', g: gi, vi: g.first });
            return;
        }
        if (!expanded.has(g.key)) return;
        for (let i = 0; i < g.n; i++) {
            const vi = g.first + i;
            out.push({ kind: 'child', g: gi, vi });
            if (evidenceVi === vi) out.push({ kind: 'evidence', g: gi, vi });
        }
    });
    return out;
}

/** Flat index of the display row of a view index (the group row, or the child when expanded); -1 if not found. */
export function aggFlatIndexOfView(flat: readonly AggItem[], vi: number): number {
    for (let i = 0; i < flat.length; i++) {
        const it = flat[i];
        if (it.kind === 'child' && it.vi === vi) return i;
        if (it.kind === 'row' && it.vi === vi) return i;
    }
    return -1;
}

/** Distinct clusters of a group, in row order. */
export function groupClusters(g: AggGroup, clusterAt: (i: number) => string | undefined): string[] {
    const out: string[] = [];
    for (let i = 0; i < g.n; i++) {
        const c = clusterAt(g.first + i);
        if (c !== undefined && !out.includes(c)) out.push(c);
    }
    return out;
}
