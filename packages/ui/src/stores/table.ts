import type { ObjectRef, Sort, ViewQuery } from '@v8s/client';

import { defineStore } from 'pinia';
import { effectScope, ref, shallowReactive, watch } from 'vue';

export interface ToneChip {
    key: string;
    label: string;
    tone: number;
    color: string;
}

export const PODS_CHIPS: ToneChip[] = [
    { key: 'running', label: 'Running', tone: 1, color: 'var(--success)' },
    { key: 'failing', label: 'Failing', tone: 3, color: 'var(--danger)' },
    { key: 'pending', label: 'Pending', tone: 2, color: 'var(--warning)' },
    { key: 'completed', label: 'Completed', tone: 4, color: 'var(--info)' }
];

export const GENERIC_CHIPS: ToneChip[] = [
    { key: 'healthy', label: 'Healthy', tone: 1, color: 'var(--success)' },
    { key: 'failing', label: 'Failing', tone: 3, color: 'var(--danger)' },
    { key: 'warning', label: 'Warning', tone: 2, color: 'var(--warning)' }
];

export const chipsFor = (gvr: string): ToneChip[] => (gvr === 'v1/pods' ? PODS_CHIPS : GENERIC_CHIPS);

/** Builds a ViewQuery for a kind; `tones` are statusTone codes. */
export function buildQuery(gvr: string, text: string, namespaces: readonly string[], tones: readonly number[], sort: readonly Sort[]): ViewQuery {
    return {
        clusters: [],
        gvr,
        namespaces: [...namespaces],
        text,
        filters: tones.length ? [{ col: 'statusTone', op: 'in', value: tones.map(String) }] : [],
        sort: [...sort]
    };
}

/** Click: sole key cycling asc, desc, default. Shift-click: add or cycle a secondary key. */
export function nextSort(cur: readonly Sort[], col: string, additive: boolean): Sort[] {
    const idx = cur.findIndex(s => s.col === col);
    if (!additive) {
        if (idx === 0 && cur.length === 1) return cur[0].desc ? [] : [{ col, desc: true }];
        return [{ col, desc: false }];
    }
    if (idx < 0) return [...cur, { col, desc: false }];
    if (!cur[idx].desc) return cur.map((s, i) => (i === idx ? { col, desc: true } : s));
    return cur.filter((_, i) => i !== idx);
}

/**
 * Selection key: the object's identity (`cluster|namespace|name`, plus `|uid` when the ref carries one), never the wire row id,
 * which belongs to one subscription and is reused after a remount. Table rows carry no uid, so a row selection keys on the name;
 * an object read through `object.get` (the drawer) keys on the incarnation as well.
 */
export const selectionKey = (r: Pick<ObjectRef, 'cluster' | 'namespace' | 'name' | 'uid'>): string =>
    `${r.cluster}|${r.namespace ?? ''}|${r.name ?? ''}` + (r.uid ? `|${r.uid}` : '');

export type SelectionRefs = ReadonlyMap<string, ObjectRef>;

/** Per-kind UI state. Selection is by object identity; refs are snapshotted when a row is selected (a few bytes each, never rows). */
export interface TableState {
    text: string;
    namespaces: string[];
    tones: number[];
    sort: Sort[];
    selected: SelectionRefs;
    focusId: number;
    pendingOpen: { namespace: string; name: string } | null;
}

function fresh(): TableState {
    return shallowReactive<TableState>({ text: '', namespaces: [], tones: [], sort: [], selected: new Map(), focusId: 0, pendingOpen: null });
}

export function isFiltered(s: TableState): boolean {
    return !!s.text || s.namespaces.length > 0 || s.tones.length > 0;
}

export function stateQuery(gvr: string, s: TableState): ViewQuery {
    return buildQuery(gvr, s.text, s.namespaces, s.tones, s.sort);
}

export const useTableStore = defineStore('table', () => {
    const states = new Map<string, TableState>();
    /** The gvr whose page is on screen; the command bar, Esc chain and palette act on it. */
    const currentGvr = ref('');

    // Namespace filters are remembered per cluster set (lib/namespace-memory.ts), which listens here. The watchers live in a detached scope:
    // a state is created lazily from whichever component first asks for it, and must outlive that component.
    const scope = effectScope(true);
    const nsListeners: ((gvr: string) => void)[] = [];
    const onNamespacesChange = (fn: (gvr: string) => void) => void nsListeners.push(fn);

    function of(gvr: string): TableState {
        let s = states.get(gvr);
        if (!s) {
            const created = fresh();
            s = created;
            states.set(gvr, s);
            scope.run(() =>
                watch(
                    () => created.namespaces,
                    () => nsListeners.forEach(fn => fn(gvr))
                )
            );
        }
        return s;
    }

    /** Every kind's namespace filter, for persistence. */
    function namespaceFilters(): Record<string, string[]> {
        const out: Record<string, string[]> = {};
        for (const [gvr, s] of states) out[gvr] = s.namespaces;
        return out;
    }

    /** Replaces every kind's namespace filter with `filters` (kinds not listed are cleared). */
    function applyNamespaceFilters(filters: Readonly<Record<string, readonly string[]>>) {
        for (const [gvr, s] of states) if (!(gvr in filters) && s.namespaces.length) s.namespaces = [];
        for (const [gvr, list] of Object.entries(filters)) {
            const s = of(gvr);
            if (s.namespaces.join('\n') !== list.join('\n')) s.namespaces = [...list];
        }
    }

    function select(gvr: string, ref: ObjectRef) {
        const s = of(gvr);
        const n = new Map(s.selected);
        const k = selectionKey(ref);
        if (n.has(k)) n.delete(k);
        else n.set(k, ref);
        s.selected = n;
    }
    function setSelected(gvr: string, refs: Iterable<ObjectRef>) {
        of(gvr).selected = new Map([...refs].map(r => [selectionKey(r), r]));
    }
    function clearSelection(gvr: string) {
        const s = of(gvr);
        if (s.selected.size) s.selected = new Map();
    }
    /** Drops selection and keyboard focus: both point at rows of a subscription that is gone or changed. */
    function resetInteraction(gvr: string) {
        clearSelection(gvr);
        of(gvr).focusId = 0;
    }
    function toggleTone(gvr: string, tone: number) {
        const s = of(gvr);
        s.tones = s.tones.includes(tone) ? s.tones.filter(t => t !== tone) : [...s.tones, tone];
    }
    function sortBy(gvr: string, col: string, additive: boolean) {
        const s = of(gvr);
        s.sort = nextSort(s.sort, col, additive);
    }
    function clearFilters(gvr: string) {
        const s = of(gvr);
        s.text = '';
        s.namespaces = [];
        s.tones = [];
    }

    return {
        currentGvr,
        of,
        onNamespacesChange,
        namespaceFilters,
        applyNamespaceFilters,
        select,
        setSelected,
        clearSelection,
        resetInteraction,
        toggleTone,
        sortBy,
        clearFilters
    };
});
