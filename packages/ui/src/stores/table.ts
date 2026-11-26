import type { ObjectRef, Sort, ViewQuery } from '@v8s/client';

import { ProblemsGVR } from '@v8s/client';
import { defineStore } from 'pinia';
import { ref, shallowReactive, shallowRef } from 'vue';

import { kinds } from '@/lib/agent.ts';
import { PODS_GVR } from '@/lib/route.ts';

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

/** Builds a ViewQuery for a kind; `tones` are statusTone codes, `nodes` node names (Pods only: the agent filters its `node` column). */
export function buildQuery(
    gvr: string,
    text: string,
    namespaces: readonly string[],
    tones: readonly number[],
    sort: readonly Sort[],
    nodes: readonly string[] = []
): ViewQuery {
    const filters: ViewQuery['filters'] = [];
    if (tones.length) filters.push({ col: 'statusTone', op: 'in', value: tones.map(String) });
    if (nodes.length && gvr === PODS_GVR) filters.push({ col: 'node', op: 'in', value: [...nodes] });
    return { clusters: [], gvr, namespaces: [...namespaces], text, filters, sort: [...sort] };
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

/**
 * Per-kind UI state. Selection is by object identity; refs are snapshotted when a row is selected (a few bytes each, never rows).
 * The namespace filter is not here: it is one selection shared by every kind (`useTableStore().namespaces`).
 */
export interface TableState {
    text: string;
    tones: number[];
    /** Node names (the Pods node filter; unused by other kinds). Not remembered across sessions, like the text and tones. */
    nodes: string[];
    sort: Sort[];
    selected: SelectionRefs;
    focusId: number;
    pendingOpen: { namespace: string; name: string } | null;
}

function fresh(): TableState {
    return shallowReactive<TableState>({ text: '', tones: [], nodes: [], sort: [], selected: new Map(), focusId: 0, pendingOpen: null });
}

const NO_NAMESPACES: readonly string[] = Object.freeze([]);

/** Whether the shared namespace filter applies to `gvr`: not to cluster-scoped kinds, nor to Problems (which shows no namespace menu). */
export function takesNamespaces(gvr: string): boolean {
    return gvr !== ProblemsGVR && kinds.value.find(k => k.gvr === gvr)?.namespaced !== false;
}

/** `namespaces` is the filter in effect for the kind (see `useTableStore().namespacesOf`). */
export function isFiltered(s: TableState, namespaces: readonly string[]): boolean {
    return !!s.text || namespaces.length > 0 || s.tones.length > 0 || s.nodes.length > 0;
}

export function stateQuery(gvr: string, s: TableState, namespaces: readonly string[]): ViewQuery {
    return buildQuery(gvr, s.text, namespaces, s.tones, s.sort, s.nodes);
}

export const useTableStore = defineStore('table', () => {
    const states = new Map<string, TableState>();
    /** The gvr whose page is on screen; the command bar, Esc chain and palette act on it. */
    const currentGvr = ref('');

    /**
     * The namespace filter: one selection for every kind, so switching kinds (sidebar, `:<type>`, palette, overview links) keeps it.
     * Remembered per cluster set (lib/namespace-memory.ts). Assign a new array to change it; an unchanged filter keeps its identity so
     * views are not re-queried.
     */
    const namespaces = shallowRef<string[]>([]);

    function of(gvr: string): TableState {
        let s = states.get(gvr);
        if (!s) {
            s = fresh();
            states.set(gvr, s);
        }
        return s;
    }

    /** The namespace filter in effect for `gvr`: the shared one, or none for kinds it does not apply to. Stable identity. */
    function namespacesOf(gvr: string): readonly string[] {
        return takesNamespaces(gvr) ? namespaces.value : NO_NAMESPACES;
    }

    /** Sets the shared filter; no-op (same array) when the list is unchanged. */
    function setNamespaces(list: readonly string[]) {
        if (list.join('\n') !== namespaces.value.join('\n')) namespaces.value = [...list];
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
    /** Clears the kind's text, status and node filters; the shared namespace filter stays (it is not this kind's to drop). */
    function clearKindFilters(gvr: string) {
        const s = of(gvr);
        s.text = '';
        s.tones = [];
        if (s.nodes.length) s.nodes = [];
    }
    /** "Clear filters": the kind's own filters and the namespace filter in effect for it. */
    function clearFilters(gvr: string) {
        clearKindFilters(gvr);
        if (namespacesOf(gvr).length) namespaces.value = [];
    }

    return {
        currentGvr,
        namespaces,
        of,
        namespacesOf,
        setNamespaces,
        select,
        setSelected,
        clearSelection,
        resetInteraction,
        toggleTone,
        sortBy,
        clearKindFilters,
        clearFilters
    };
});
