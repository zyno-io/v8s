import type { Column } from '@v8s/client';

import type { ColumnLayout } from './settings.ts';

export type CellKind = 'cluster' | 'name' | 'text' | 'mono' | 'int' | 'age' | 'status' | 'tone' | 'ready' | 'usage';

/** One sub-column of the dense usage block (CPU, %R, %L, MEM, …). */
export interface UsageSub {
    key: string;
    /** Full label (`%CPU/L`): the sort button's aria-label. */
    label: string;
    /** What the header shows: the short label (`%L`) when the block also shows that resource's raw column, else the full one. */
    text: string;
    /** Unit suffix of a raw column (`m`, `Mi`): a muted 10px span that the sort arrow replaces, so nothing reflows. */
    suffix: string;
    idx: number;
    unit: string;
    metrics: boolean;
    res: 'cpu' | 'mem';
    width: number;
    /** Colour rule: `lim` warns at 90 % and alarms at 100 %, `node` warns at 90 %. */
    tone: 'lim' | 'node' | null;
    /** A percentage of the node: one decimal below 10 %. */
    pctNode: boolean;
    /** First sub-column of the memory group: the wider cpu-to-mem gap sits to its left (included in its grid track). */
    lead?: boolean;
}

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
    /** kind `usage`: the visible unit columns drawn as one block, with their grid (the block's cell has its own sub-grid). */
    subs?: UsageSub[];
    subGrid?: string;
}

/** Row indexes of the pod usage columns, for the tooltip and the phone card (-1 = not in the schema). */
export interface UsageIdx {
    cpuUse: number;
    cpuReq: number;
    cpuLim: number;
    cpuPctReq: number;
    cpuPctLim: number;
    cpuPctNode: number;
    memUse: number;
    memReq: number;
    memLim: number;
    memPctReq: number;
    memPctLim: number;
    memPctNode: number;
    node: number;
    /** Nodes: usage as a percentage of the node's own allocatable, and the allocatable columns (`cpu`, `memory`). */
    cpuPctAlloc: number;
    cpuAlloc: number;
    memPctAlloc: number;
    memAlloc: number;
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
    all: { key: string; label: string; idx: number; hideable: boolean; /** why it cannot be shown (metrics-server missing) */ note?: string }[];
    /** Keys the layout shows (the Columns menu's checkmarks), including default-hidden ones the user showed. */
    visibleKeys: Set<string>;
    /** Keys hidden until shown: Column.hidden plus the decor's. */
    defaultHidden: Set<string>;
    /** Keys of every metrics column of the schema (a sort on one of them needs usage even when the column is hidden). */
    metricKeys: string[];
    /** A metrics column is on screen (drives polling). */
    hasMetrics: boolean;
    usage: UsageIdx | null;
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
    /** Every active cluster reports no metrics API: metrics columns are dropped (listed disabled in the Columns menu). */
    metricsOff?: boolean;
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
        all: [],
        visibleKeys: new Set(),
        defaultHidden: new Set(),
        metricKeys: [],
        hasMetrics: false,
        usage: null
    };
    if (!columns?.length) return empty;
    // Visible = not hidden by the user, and either shown by default or shown by the user. (The layout used to replace the
    // defaults wholesale, so any resize made every default-hidden column appear.)
    const defaultHidden = new Set([...(opts.decor?.defaultHidden ?? []), ...columns.filter(c => c.hidden).map(c => c.key)]);
    const userHidden = new Set(opts.layout?.hidden ?? []);
    const userShown = new Set(opts.layout?.shown ?? []);
    const isVisible = (key: string) => !userHidden.has(key) && (!defaultHidden.has(key) || userShown.has(key));
    const metricsOff = !!opts.metricsOff;
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
        c => c.type !== 'tone' && c.key !== 'statusTone' && isVisible(c.key) && !(c.key === 'cluster' && !opts.showCluster)
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
    // The visible unit columns (contiguous in the schema) are drawn as one block at the first one's position.
    const unitCols = columns.filter(c => c.unit && isVisible(c.key) && !(metricsOff && c.metrics));
    const rawRes = new Set(unitCols.filter(c => !c.short).map(c => resOf(c.key)));
    let blockAt = -1;
    for (const c of columns) {
        const idx = columns.indexOf(c) + 2;
        const isTone = c.type === 'tone' || c.key === 'statusTone';
        if (c.unit) {
            if (!unitCols.includes(c) || blockAt >= 0) continue;
            blockAt = cols.length;
            const subs = unitCols.map(u => usageSub(u, columns.indexOf(u) + 2, rawRes.has(resOf(u.key))));
            let prev = '';
            const tracks = subs.map(u => {
                u.lead = !!prev && prev !== u.res;
                const t = u.width + (u.lead ? RES_GAP - SUB_GAP : 0);
                prev = u.res;
                return t;
            });
            const width = tracks.reduce((a, b) => a + b, 0) + SUB_GAP * (subs.length - 1);
            cols.push({
                key: 'usage',
                label: 'Usage',
                idx: subs[0].idx,
                kind: 'usage',
                align: 'right',
                width,
                grow: false,
                sortable: false,
                subs,
                subGrid: tracks.map(t => t + 'px').join(' ')
            });
            continue;
        }
        if (!isVisible(c.key) && c.key !== 'name') continue;
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
        if (!isVisible(e.key)) continue;
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
    const all: TablePlan['all'] = columns
        .map((c, i) => ({
            key: c.key,
            label: labelOf(c),
            idx: i + 2,
            hideable: c.key !== 'name' && c.key !== 'statusTone' && c.type !== 'tone' && !(c.key === 'cluster'),
            note: metricsOff && c.metrics ? 'metrics-server not installed' : undefined
        }))
        .filter(c => !(isPods && c.key === 'containers'))
        .concat(extra.map(e => ({ key: e.key, label: e.label, idx: e.idx, hideable: true, note: undefined })));
    const visibleKeys = new Set([...columns.map(c => c.key), ...extra.map(e => e.key)].filter(isVisible));
    const metricKeys = columns.filter(c => c.metrics).map(c => c.key);
    const hasMetrics = cols.some(c => c.subs?.some(u => u.metrics));
    return {
        cols,
        toneIdx,
        nameIdx,
        nsIdx,
        clusterIdx,
        ageIdx,
        hasTime,
        grid: gridTemplate(cols),
        minWidth: minWidthOf(cols),
        all,
        visibleKeys,
        defaultHidden,
        metricKeys,
        hasMetrics,
        usage: usageIdx(columns)
    };
}

/**
 * Whether a pods table will show usage before its schema has arrived: a metrics column is visible by default (or the user
 * showed one), or the sort is by one. Subscribing with this guess spares the first rows an encode without usage and the
 * dashes that follow. The page's own watch corrects it once the real plan is known.
 */
export const PODS_METRIC_KEYS = ['cpuUse', 'cpuPctReq', 'cpuPctLim', 'cpuPctNode', 'memUse', 'memPctReq', 'memPctLim', 'memPctNode'];
const PODS_METRIC_SHOWN = ['cpuUse', 'cpuPctReq', 'cpuPctLim', 'memUse', 'memPctReq', 'memPctLim'];
export function guessPodsUsage(layout: ColumnLayout | undefined, sort: readonly { col: string }[]): boolean {
    return guess(PODS_METRIC_KEYS, PODS_METRIC_SHOWN, layout, sort);
}

/** The same guess for nodes: all four metrics columns are shown by default. */
export const NODES_METRIC_KEYS = ['cpuUse', 'cpuPctAlloc', 'memUse', 'memPctAlloc'];
export function guessNodesUsage(layout: ColumnLayout | undefined, sort: readonly { col: string }[]): boolean {
    return guess(NODES_METRIC_KEYS, NODES_METRIC_KEYS, layout, sort);
}

/** The subscribe-time usage guess for a kind (false for kinds without usage). */
export function guessUsage(gvr: string, layout: ColumnLayout | undefined, sort: readonly { col: string }[]): boolean {
    if (gvr === 'v1/pods') return guessPodsUsage(layout, sort);
    if (gvr === 'v1/nodes') return guessNodesUsage(layout, sort);
    return false;
}

function guess(
    keys: readonly string[],
    shownByDefault: readonly string[],
    layout: ColumnLayout | undefined,
    sort: readonly { col: string }[]
): boolean {
    if (sort.some(s => keys.includes(s.col))) return true;
    const hidden = new Set(layout?.hidden ?? []);
    const shown = new Set(layout?.shown ?? []);
    return keys.some(k => !hidden.has(k) && (shownByDefault.includes(k) || shown.has(k)));
}

// ---- the dense usage block ----

/** Sub-column widths by unit, and the gaps: 6 px inside a resource group, 14 px where cpu meets mem. */
const SUB_W: Record<string, number> = { millicores: 44, MiB: 48, permille: 36 };
const SUB_GAP = 6;
const RES_GAP = 14;

const resOf = (key: string): 'cpu' | 'mem' => (key.startsWith('cpu') ? 'cpu' : 'mem');

function usageSub(c: Column, idx: number, shortOk: boolean): UsageSub {
    const label = labelOf(c);
    const raw = !c.short;
    const text = shortOk && c.short ? c.short : label;
    let width = SUB_W[c.unit ?? ''] ?? 40;
    if (c.short === 'alloc') width = c.unit === 'MiB' ? 56 : 52;
    if (!raw && text === label) width = Math.max(width, Math.ceil(text.length * 7.4) + 2); // a full label in a header
    return {
        key: c.key,
        label,
        text,
        suffix: raw ? (c.unit === 'MiB' ? 'Mi' : c.unit === 'millicores' ? 'm' : '') : '',
        idx,
        unit: c.unit ?? '',
        metrics: !!c.metrics,
        res: resOf(c.key),
        width,
        tone: c.key.endsWith('PctLim') ? 'lim' : /Pct(Node|Alloc)$/.test(c.key) ? 'node' : null,
        pctNode: /Pct(Node|Alloc)$/.test(c.key)
    };
}

function usageIdx(columns: readonly Column[]): UsageIdx | null {
    const at = (key: string) => {
        const i = columns.findIndex(c => c.key === key);
        return i < 0 ? -1 : i + 2;
    };
    const u: UsageIdx = {
        cpuUse: at('cpuUse'),
        cpuReq: at('cpuReq'),
        cpuLim: at('cpuLim'),
        cpuPctReq: at('cpuPctReq'),
        cpuPctLim: at('cpuPctLim'),
        cpuPctNode: at('cpuPctNode'),
        memUse: at('memUse'),
        memReq: at('memReq'),
        memLim: at('memLim'),
        memPctReq: at('memPctReq'),
        memPctLim: at('memPctLim'),
        memPctNode: at('memPctNode'),
        node: at('node'),
        cpuPctAlloc: at('cpuPctAlloc'),
        cpuAlloc: at('cpu'),
        memPctAlloc: at('memPctAlloc'),
        memAlloc: at('memory')
    };
    return u.cpuUse >= 0 && u.memUse >= 0 ? u : null;
}
