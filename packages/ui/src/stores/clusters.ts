import type { Cluster, ClusterState } from '@v8s/client';

import { defineStore } from 'pinia';
import { computed, ref, shallowRef } from 'vue';

import { getAgent, refreshKinds } from '@/lib/agent.ts';
import { useSettingsStore } from '@/stores/settings.ts';

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
    /** At least one selected cluster is connected (listing or listed): search has something to look through. */
    const anyConnected = computed(() => states.value.some(s => s.phase === 'syncing' || s.phase === 'ready' || s.phase === 'degraded'));
    const nameById = computed(() => new Map(list.value.map(c => [c.id, c.name])));
    const byId = computed(() => new Map(list.value.map(c => [c.id, c])));

    const groupsOf = (id: string): readonly string[] => byId.value.get(id)?.groups ?? [];
    const writeContextOf = (id: string): string => byId.value.get(id)?.writeContext ?? '';
    /**
     * Protected clusters ask for a typed confirmation. The flag is keyed by the listed (read) cluster id; the write context linked to it
     * is protected too, and so is a cluster whose own write context is protected, so no linked id can sidestep the flag.
     */
    const isProtected = (id: string): boolean => {
        const s = useSettingsStore();
        const set = s.protectedClusters;
        return set.includes(id) || set.includes(s.links[id] ?? '') || set.some(p => s.links[p] === id);
    };

    /** Re-reads the list (groups and linked write contexts come from settings, so call this after changing them). */
    async function refreshList() {
        const res = await getAgent().listClusters();
        list.value = res.clusters ?? [];
        return res;
    }

    async function load() {
        const res = await refreshList();
        remembered.value = res.selected ?? [];
        checked.value = new Set(remembered.value);
        loaded.value = true;
        // Nothing connects until the user presses Connect: show the selector with the remembered selection pre-checked.
        if (!states.value.length) popoverOpen.value = true;
    }

    /** After a reconnect: a restarted agent (or an evicted hosted engine) has no live clusters; connect the remembered selection again. */
    async function restoreAfterReconnect() {
        await refreshList();
        // The agent pushes its cluster states right after Hello, so they are in by the time this response is.
        if (!getAgent().clusterStates.value.length && remembered.value.length) await getAgent().selectClusters(remembered.value);
    }

    function open() {
        // A context linked as another one's write context is not listed, so it must not stay checked.
        const ids = activeIds.value.length ? activeIds.value : remembered.value;
        checked.value = new Set(list.value.length ? ids.filter(id => nameById.value.has(id)) : ids);
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
            refreshKinds(0);
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
        anyConnected,
        nameById,
        byId,
        groupsOf,
        writeContextOf,
        isProtected,
        refreshList,
        restoreAfterReconnect,
        takeFocusTarget,
        load,
        open,
        close,
        setChecked,
        toggle,
        connect
    };
});
