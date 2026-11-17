import type { Column } from '@v8s/client';

import type { ColumnLayout } from './settings.ts';

export type CellKind = 'cluster' | 'name' | 'text' | 'mono' | 'int' | 'age' | 'status' | 'tone' | 'ready';

export interface ColPlan {
    key: string;
    label: string;
    /** Index into a Row (columns start at 2, after id and version). */
    idx: number;
    kind: CellKind;
    align: 'left' | 'right';
    /** Pixel width (the name column grows unless the user resized it). */
    width: number;
    grow: boolean;
    sortable: boolean;
}

export interface TablePlan {
    cols: ColPlan[];
    /** Row index of the tone column, or -1. */
    toneIdx: number;
    nameIdx: number;
    nsIdx: number;
    clusterIdx: number;
    /** Row index of the first `time` column named createdAt (or any time column), or -1. */
    ageIdx: number;
    hasTime: boolean;
    /** CSS `grid-template-columns` value, including the checkbox column. */
    grid: string;
    minWidth: number;
    /** Every column of the schema with its index, for the Columns menu (cluster included only if present). */
    all: { key: string; label: string; idx: number; hideable: boolean }[];
}

// `ready` first: for CRDs `Status` is usually a long message while `Ready` is True/False; pods and nodes have no text `ready`.
const STATUS_KEYS = ['ready', 'phase', 'state', 'synced', 'healthy', 'available', 'status'];
const MONO = /^(node|.*ip|images?|uid|volume|claim|schedule|selector|ports|taints|kernel|runtime|os|version|address|hosts|podselector)$/i;
export const CHECK_W = 15;
export const GAP = 12;
const PAD = 24;
const NAME_MIN = 160;

export const labelOf = (c: Column): string => c.label || c.key.replace(/([a-z])([A-Z])/g, '$1 $2').replace(/^./, s => s.toUpperCase());

export function defaultWidth(c: Column): number {
    if (c.width && c.width > 0) return c.width;
    switch (c.type) {
        case 'int':
            return 72;
        case 'time':
            return 80;
        case 'tone':
            return 90;
        default:
            return 140;
    }
}

/** Template for the grid with optional per-column pixel overrides (used live while dragging a resize handle). */
export function gridTemplate(cols: readonly ColPlan[], override?: { key: string; width: number }): string {
    const parts = [`${CHECK_W}px`];
    for (const c of cols) {
        const w = override && override.key === c.key ? override.width : c.width;
        parts.push(c.grow && !(override && override.key === c.key) ? `minmax(${NAME_MIN}px, 1.7fr)` : `${w}px`);
    }
    return parts.join(' ');
}

export function minWidthOf(cols: readonly ColPlan[]): number {
    let w = CHECK_W + PAD;
    for (const c of cols) w += (c.grow ? NAME_MIN : c.width) + GAP;
    return w;
}

export interface PlanOptions {
    gvr: string;
    showCluster: boolean;
    layout?: ColumnLayout;
    /** Kind-specific extras (see table-decor.ts): virtual columns and default-hidden keys. */
    decor?: { extraCols?: { key: string; label: string; width: number; mono?: boolean }[]; defaultHidden?: string[] } | null;
}

/**
 * Turns the schema from `view.subscribe` into a render plan: which columns show, how wide, how each cell renders. The tone
 * column is never shown on its own when a status-text column exists; that column's cell wears the tone as a tag instead.
 */
export function planColumns(columns: readonly Column[] | null, opts: PlanOptions): TablePlan {
    const empty: TablePlan = {
        cols: [],
        toneIdx: -1,
        nameIdx: -1,
        nsIdx: -1,
        clusterIdx: -1,
        ageIdx: -1,
        hasTime: false,
        grid: `${CHECK_W}px`,
        minWidth: 0,
        all: []
    };
    if (!columns?.length) return empty;
    const hidden = new Set(opts.layout?.hidden ?? opts.decor?.defaultHidden ?? []);
    const widths = opts.layout?.widths ?? {};
    const isPods = opts.gvr === 'v1/pods';

    let toneIdx = -1;
    let nameIdx = -1;
    let nsIdx = -1;
    let clusterIdx = -1;
    let ageIdx = -1;
    columns.forEach((c, i) => {
        const idx = i + 2;
        if (c.type === 'tone' || c.key === 'statusTone') toneIdx = idx;
        else if (c.key === 'name') nameIdx = idx;
        else if (c.key === 'namespace') nsIdx = idx;
        else if (c.key === 'cluster') clusterIdx = idx;
        else if (c.type === 'time' && (c.key === 'createdAt' || ageIdx < 0)) ageIdx = idx;
    });

    // Which visible column carries the status tag.
    const visible = columns.filter(
        c => c.type !== 'tone' && c.key !== 'statusTone' && !hidden.has(c.key) && !(c.key === 'cluster' && !opts.showCluster)
    );
    let statusKey = '';
    if (toneIdx >= 0) {
        for (const k of STATUS_KEYS) {
            if (visible.some(c => c.key === k && c.type !== 'int' && c.type !== 'time')) {
                statusKey = k;
                break;
            }
        }
    }

    const cols: ColPlan[] = [];
    let hasTime = false;
    for (const c of columns) {
        const idx = columns.indexOf(c) + 2;
        const isTone = c.type === 'tone' || c.key === 'statusTone';
        if (hidden.has(c.key) && c.key !== 'name') continue;
        if (c.key === 'cluster' && !opts.showCluster) continue;
        if (isPods && c.key === 'containers') continue; // folded into `ready` as "ready/containers"
        if (isTone && statusKey) continue;
        let kind: CellKind = 'text';
        if (c.key === 'cluster') kind = 'cluster';
        else if (c.key === 'name') kind = 'name';
        else if (isTone) kind = 'tone';
        else if (c.key === statusKey) kind = 'status';
        else if (isPods && c.key === 'ready') kind = 'ready';
        else if (c.type === 'time') kind = 'age';
        else if (c.type === 'int') kind = 'int';
        else if (MONO.test(c.key)) kind = 'mono';
        if (kind === 'age') hasTime = true;
        const user = widths[c.key];
        const width = user && user > 24 ? Math.round(user) : defaultWidth(c);
        cols.push({
            key: c.key,
            label: labelOf(c),
            idx,
            kind,
            align: c.align === 'right' || c.type === 'int' ? 'right' : 'left',
            width: kind === 'cluster' ? 112 : kind === 'ready' ? 56 : width,
            grow: kind === 'name' && !user,
            sortable: true
        });
    }
    const extra = (opts.decor?.extraCols ?? []).map((e, i) => ({ ...e, idx: -(i + 1) }));
    for (const e of extra) {
        if (hidden.has(e.key)) continue;
        const user = widths[e.key];
        cols.push({
            key: e.key,
            label: e.label,
            idx: e.idx,
            kind: e.mono ? 'mono' : 'text',
            align: 'left',
            width: user && user > 24 ? Math.round(user) : e.width,
            grow: false,
            sortable: false
        });
    }
    const all = columns
        .map((c, i) => ({
            key: c.key,
            label: labelOf(c),
            idx: i + 2,
            hideable: c.key !== 'name' && c.key !== 'statusTone' && c.type !== 'tone' && !(c.key === 'cluster')
        }))
        .filter(c => !(isPods && c.key === 'containers'))
        .concat(extra.map(e => ({ key: e.key, label: e.label, idx: e.idx, hideable: true })));
    return { cols, toneIdx, nameIdx, nsIdx, clusterIdx, ageIdx, hasTime, grid: gridTemplate(cols), minWidth: minWidthOf(cols), all };
}
