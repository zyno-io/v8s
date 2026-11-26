import type { Column, ViewHandle } from '@v8s/client';

import { computed, onScopeDispose, watch } from 'vue';

import { useClustersStore } from '@/stores/clusters.ts';
import { chipsFor, buildQuery, stateQuery, useTableStore } from '@/stores/table.ts';

import { getAgent } from './agent.ts';
import { windowPad } from './viewport.ts';

const hasTone = (cols: readonly Column[] | null) => !!cols?.some(c => c.type === 'tone' || c.key === 'statusTone');

/**
 * Subscribes one kind's list view plus a one-row count view per tone chip (only once the schema shows a tone column).
 * Count views never render rows; they only contribute `total`, so chips show live counts with nothing computed in the UI.
 */
export function useResourceView(gvr: string, opts?: { usage?: boolean }) {
    const agent = getAgent();
    const table = useTableStore();
    const state = table.of(gvr);
    const initialLimit = () => windowPad() * 2 + 60;
    // The shared namespace filter, or none for a cluster-scoped kind (known once the kinds load; namespaced until then).
    const namespaces = computed(() => table.namespacesOf(gvr));
    const query = computed(() => stateQuery(gvr, state, namespaces.value));

    const handle = agent.subscribeView(query.value, { offset: 0, limit: initialLimit() }, { usage: opts?.usage });
    const counts: Record<string, ViewHandle> = {};
    const chips = chipsFor(gvr);

    function ensureCounts() {
        if (Object.keys(counts).length) return;
        for (const c of chips)
            counts[c.key] = agent.subscribeView(buildQuery(gvr, state.text, namespaces.value, [c.tone], [], state.nodes), { offset: 0, limit: 1 });
    }
    watch(
        () => handle.columns.value,
        cols => {
            if (hasTone(cols)) ensureCounts();
        },
        { immediate: true }
    );

    // Row ids, selection and keyboard focus belong to one subscription state: reset them whenever the rows can change identity.
    watch(query, q => {
        table.resetInteraction(gvr);
        handle.update({ query: q, window: { offset: 0, limit: initialLimit() } });
    });
    watch(
        () => useClustersStore().activeIds.join('\n'),
        () => table.resetInteraction(gvr)
    );

    let timer: ReturnType<typeof setTimeout> | undefined;
    watch(
        () => [state.text, namespaces.value, state.nodes] as const,
        ([text, ns, nodes]) => {
            clearTimeout(timer);
            timer = setTimeout(() => {
                for (const c of chips) counts[c.key]?.update({ query: buildQuery(gvr, text, ns, [c.tone], [], nodes) });
            }, 150);
        }
    );

    onScopeDispose(() => {
        table.resetInteraction(gvr);
        clearTimeout(timer);
        handle.close();
        for (const c of Object.values(counts)) c.close();
    });

    return { handle, counts, chips, hasTone: computed(() => hasTone(handle.columns.value)) };
}
