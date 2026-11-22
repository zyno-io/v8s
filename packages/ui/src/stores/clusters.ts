import type { Cluster, ClusterState } from '@v8s/client';

import { defineStore } from 'pinia';
import { computed, ref, shallowRef, watch } from 'vue';

import { clearAccessCache } from '@/lib/access.ts';
import { getAgent, refreshKinds } from '@/lib/agent.ts';
import { clusterColor } from '@/lib/colors.ts';
import { KEY_SELECTION, parseSelection, readJson, resolveChecked, storeSelection, writeJson } from '@/lib/remember.ts';
import { useSettingsStore } from '@/stores/settings.ts';
import { useUiStore } from '@/stores/ui.ts';

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

    /** Clusters with an elevate/revert in flight (UI state only; the elevation itself is `ClusterState.elevated`). */
    const elevating = shallowRef<ReadonlySet<string>>(new Set());
    const isElevated = (id: string): boolean => stateById.value.get(id)?.elevated === true;
    /** Changes whenever the set of elevated clusters does: watch it to re-read what depends on the identity (access checks). */
    const elevatedKey = computed(() =>
        states.value
            .filter(s => s.elevated)
            .map(s => s.id)
            .join()
    );
    // Any change of who is elevated (this tab, another tab, an early state frame) invalidates cached access answers.
    watch(elevatedKey, clearAccessCache);
    const elevatedCount = computed(() => states.value.filter(s => s.elevated).length);
    /**
     * The name to show for a cluster's data and actions: its linked write context while elevated (everything on it is
     * loaded and written through that context), otherwise its own name. Lookups and colors stay keyed by the read id.
     */
    const displayById = computed(() => {
        const m = new Map(nameById.value);
        for (const s of states.value) {
            const w = s.elevated ? writeContextOf(s.id) : '';
            if (w) m.set(s.id, w);
        }
        return m;
    });
    const displayName = (id: string): string => displayById.value.get(id) ?? id;
    /** Cluster tags of an elevated cluster wear the warning tone and a warning dot, like its status chip (a grey hue reads as "disconnected"). */
    const tagTone = (id: string): string => (isElevated(id) ? 'warning' : '');
    const dotColor = (id: string): string => (isElevated(id) ? 'var(--warning)' : clusterColor(id));

    /** Runs the cluster as its linked write context (`on`) or back as its read context; its data reloads. */
    async function setElevated(id: string, on: boolean) {
        if (elevating.value.has(id)) return;
        elevating.value = new Set(elevating.value).add(id);
        try {
            await getAgent().elevateCluster(id, on);
            clearAccessCache();
        } catch (e) {
            const name = nameById.value.get(id) ?? id;
            useUiStore().toast('danger', `${on ? 'Elevate' : 'Revert'} ${name}: ${e instanceof Error ? e.message : String(e)}`);
        } finally {
            const n = new Set(elevating.value);
            n.delete(id);
            elevating.value = n;
        }
    }

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
            // Only when the selector was open: an auto-connect must not leave the next Esc/Cancel focusing the table.
            if (popoverOpen.value) focusAfterClose = 'table';
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
        elevating,
        isElevated,
        elevatedKey,
        elevatedCount,
        displayName,
        tagTone,
        dotColor,
        setElevated,
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
