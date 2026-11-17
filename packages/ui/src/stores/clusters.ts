import type { Cluster, ClusterState } from '@v8s/client';

import { defineStore } from 'pinia';
import { computed, ref, shallowRef } from 'vue';

import { getAgent } from '@/lib/agent.ts';

export const useClustersStore = defineStore('clusters', () => {
    const list = shallowRef<Cluster[]>([]);
    /** Remembered selection reported by the agent; pre-checked, not connected. */
    const remembered = shallowRef<string[]>([]);
    const checked = shallowRef<ReadonlySet<string>>(new Set());
    const popoverOpen = ref(false);
    const loaded = ref(false);
    const connecting = ref(false);
    const error = ref('');
    /** Where focus goes when the popover closes: back to its trigger (Esc/Cancel), or into the table after a successful Connect. */
    let focusAfterClose: 'trigger' | 'table' = 'trigger';
    const takeFocusTarget = () => {
        const t = focusAfterClose;
        focusAfterClose = 'trigger';
        return t;
    };

    const states = computed<ClusterState[]>(() => (loaded.value ? getAgent().clusterStates.value : []));
    const stateById = computed(() => new Map(states.value.map(s => [s.id, s])));
    const activeIds = computed(() => states.value.map(s => s.id));
    const nameById = computed(() => new Map(list.value.map(c => [c.id, c.name])));

    async function load() {
        const res = await getAgent().listClusters();
        list.value = res.clusters ?? [];
        remembered.value = res.selected ?? [];
        checked.value = new Set(remembered.value);
        loaded.value = true;
        // Nothing connects until the user presses Connect: show the selector with the remembered selection pre-checked.
        if (!states.value.length) popoverOpen.value = true;
    }

    function open() {
        checked.value = new Set(activeIds.value.length ? activeIds.value : remembered.value);
        popoverOpen.value = true;
    }

    function close() {
        popoverOpen.value = false;
    }

    function setChecked(ids: Iterable<string>) {
        checked.value = new Set(ids);
    }

    function toggle(id: string) {
        const n = new Set(checked.value);
        if (!n.delete(id)) n.add(id);
        checked.value = n;
    }

    async function connect(ids: string[]) {
        connecting.value = true;
        error.value = '';
        try {
            await getAgent().selectClusters(ids);
            remembered.value = ids;
            focusAfterClose = 'table';
            popoverOpen.value = false;
        } catch (e) {
            error.value = e instanceof Error ? e.message : String(e);
        } finally {
            connecting.value = false;
        }
    }

    return {
        list,
        remembered,
        checked,
        popoverOpen,
        loaded,
        connecting,
        error,
        states,
        stateById,
        activeIds,
        nameById,
        takeFocusTarget,
        load,
        open,
        close,
        setChecked,
        toggle,
        connect
    };
});
