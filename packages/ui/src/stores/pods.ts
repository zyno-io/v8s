import type { Sort, ViewQuery } from '@v8s/client';

import { defineStore } from 'pinia';
import { computed, ref, shallowRef, watch } from 'vue';

export const TONE_CHIPS = [
    { key: 'running', label: 'Running', tone: 1, color: 'var(--success)' },
    { key: 'failing', label: 'Failing', tone: 3, color: 'var(--danger)' },
    { key: 'pending', label: 'Pending', tone: 2, color: 'var(--warning)' },
    { key: 'completed', label: 'Completed', tone: 4, color: 'var(--info)' }
] as const;

export const SORT_LABELS: Record<string, string> = {
    cluster: 'cluster',
    name: 'name',
    namespace: 'namespace',
    status: 'status',
    restarts: 'restarts',
    node: 'node',
    createdAt: 'age'
};

/** Builds the pods ViewQuery; `tones` is a list of statusTone codes. */
export function buildPodsQuery(text: string, namespaces: readonly string[], tones: readonly number[], sort: readonly Sort[]): ViewQuery {
    return {
        clusters: [],
        gvr: 'v1/pods',
        namespaces: [...namespaces],
        text,
        filters: tones.length ? [{ col: 'statusTone', op: 'in', value: tones.map(String) }] : [],
        sort: [...sort]
    };
}

/** Pods page UI state: filters, sort, selection and focus. Selection and focus are by row id, never by index. */
export const usePodsStore = defineStore('pods', () => {
    const text = ref('');
    const namespaces = shallowRef<string[]>([]);
    const tones = shallowRef<number[]>([]);
    const sort = shallowRef<Sort[]>([]);
    const selected = shallowRef<ReadonlySet<number>>(new Set());
    const focusId = ref(0);
    const pendingOpen = shallowRef<{ namespace: string; name: string } | null>(null);

    const query = computed(() => buildPodsQuery(text.value, namespaces.value, tones.value, sort.value));
    const filtered = computed(() => !!text.value || namespaces.value.length > 0 || tones.value.length > 0);

    watch(query, () => clearSelection());

    function toggleSelected(id: number) {
        const n = new Set(selected.value);
        if (!n.delete(id)) n.add(id);
        selected.value = n;
    }
    function setSelected(ids: Iterable<number>) {
        selected.value = new Set(ids);
    }
    function clearSelection() {
        if (selected.value.size) selected.value = new Set();
    }

    function toggleTone(tone: number) {
        tones.value = tones.value.includes(tone) ? tones.value.filter(t => t !== tone) : [...tones.value, tone];
    }

    /** Click: sole key cycling asc → desc → default. Shift-click: add/cycle a secondary key. */
    function sortBy(col: string, additive: boolean) {
        const cur = sort.value;
        const idx = cur.findIndex(s => s.col === col);
        if (!additive) {
            if (idx === 0 && cur.length === 1) sort.value = cur[0].desc ? [] : [{ col, desc: true }];
            else sort.value = [{ col, desc: false }];
            return;
        }
        if (idx < 0) sort.value = [...cur, { col, desc: false }];
        else if (!cur[idx].desc) sort.value = cur.map((s, i) => (i === idx ? { col, desc: true } : s));
        else sort.value = cur.filter((_, i) => i !== idx);
    }

    return {
        text,
        namespaces,
        tones,
        sort,
        selected,
        focusId,
        pendingOpen,
        query,
        filtered,
        toggleSelected,
        setSelected,
        clearSelection,
        toggleTone,
        sortBy
    };
});
