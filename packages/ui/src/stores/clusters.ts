import type { Cluster, ClusterState } from '@v8s/client';

import { defineStore } from 'pinia';
import { computed, ref, shallowRef } from 'vue';

import { getAgent, refreshKinds } from '@/lib/agent.ts';
import { KEY_SELECTION, parseSelection, readJson, resolveChecked, storeSelection, writeJson } from '@/lib/remember.ts';
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

    /** Group name to its listed members: the groups in settings plus the server-set ones (hosted mode). Used to recognise an exact group match. */
    const groupMap = computed<Record<string, string[]>>(() => {
        const out: Record<string, string[]> = {};
        for (const c of list.value) for (const g of c.fixedGroups ?? []) (out[g] ??= []).push(c.id);
        for (const [g, ids] of Object.entries(useSettingsStore().groups)) out[g] = ids.filter(id => byId.value.has(id));
        return out;
    });

    async function load() {
        const res = await refreshList();
        // The latest selection this browser connected to (a group by name, so a changed membership is picked up); else the agent's.
        remembered.value = resolveChecked(parseSelection(readJson(KEY_SELECTION)), groupMap.value, new Set(byId.value.keys()), res.selected ?? []);
        checked.value = new Set(remembered.value);
        loaded.value = true;
        if (states.value.length) return;
        // A single cluster has nothing to choose: connect to it. Otherwise nothing connects until the user presses Connect.
        if (list.value.length === 1) await connect([list.value[0].id]);
        else popoverOpen.value = true;
        if (!states.value.length && error.value) popoverOpen.value = true;
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
            writeJson(KEY_SELECTION, storeSelection(ids, groupMap.value));
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
        groupMap,
        groupsOf,
        writeContextOf,
        isProtected,
        refreshList,
        takeFocusTarget,
        load,
        open,
        close,
        setChecked,
        toggle,
        connect
    };
});
