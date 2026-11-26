<script setup lang="ts">
import type { ViewHandle, ViewQuery } from '@v8s/client';

import { onBeforeUnmount, ref, shallowRef, watch } from 'vue';

import FilterMenu from '@/components/FilterMenu.vue';
import { getAgent } from '@/lib/agent.ts';
import { namespaceNames } from '@/lib/namespaces.ts';
import { PODS_GVR } from '@/lib/route.ts';
import { useTableStore } from '@/stores/table.ts';

// The Pods node filter. Options come from a `v1/nodes` view subscribed only while the menu is open, narrowed by the search text in the
// agent, one window of at most LIMIT names (sorted): a 5k-node fleet never ships every node, and typing reaches the rest.
const LIMIT = 500;
const nodesQuery = (text: string): ViewQuery => ({
    clusters: [],
    gvr: 'v1/nodes',
    namespaces: [],
    text,
    filters: [],
    sort: [{ col: 'name', desc: false }]
});

const table = useTableStore();
const state = table.of(PODS_GVR);

const open = ref(false);
const q = ref('');
const all = ref<string[]>([]);
const note = ref<string | undefined>();
const view = shallowRef<ViewHandle | null>(null);

function stop() {
    clearTimeout(timer);
    view.value?.close();
    view.value = null;
}
watch(open, o => {
    if (!o) return stop();
    clearTimeout(timer); // opening reset the search: the new view already has the empty query, so no update for it
    all.value = [...state.nodes];
    note.value = undefined;
    view.value = getAgent().subscribeView(nodesQuery(''), { offset: 0, limit: LIMIT });
});
// Node names (several clusters' nodes of one name are one option); the picks always listed, so a pick narrowed out by the search stays.
watch(
    () => [view.value?.version.value, view.value?.error.value] as const,
    () => {
        const h = view.value;
        if (!h) return;
        all.value = namespaceNames(h, state.nodes);
        note.value = h.error.value?.code === 'forbidden' ? 'Not allowed to list nodes' : undefined;
    }
);
let timer: ReturnType<typeof setTimeout> | undefined;
watch(q, t => {
    clearTimeout(timer);
    timer = setTimeout(() => view.value?.update({ query: nodesQuery(t.trim()), window: { offset: 0, limit: LIMIT } }), 120);
});
onBeforeUnmount(stop);
</script>

<template>
    <FilterMenu
        v-model:open="open"
        v-model:query="q"
        label="Node"
        noun="nodes"
        :options="all"
        :selected="state.nodes"
        :note="note"
        data-testid="node-menu"
        @change="list => (state.nodes = list)"
    />
</template>
