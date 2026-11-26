<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue';

import FilterMenu from '@/components/FilterMenu.vue';
import { getNamespacesView } from '@/lib/agent.ts';
import { namespaceNames } from '@/lib/namespaces.ts';
import { recordRecentNamespaces, sameSet } from '@/lib/recent-namespaces.ts';
import { useTableStore } from '@/stores/table.ts';

// The filter is shared by every kind (stores/table.ts), so this menu shows and edits the same selection on every list.
const table = useTableStore();

const open = ref(false);
const all = ref<string[]>([]);

function readNamespaces() {
    all.value = namespaceNames(getNamespacesView(), table.namespaces);
}
// While open, follow the namespaces view: a cluster that connects (or a namespace created) shows up without reopening. Cheap: one row
// per namespace, and only while the menu is open.
watch(
    () => (open.value ? getNamespacesView().version.value : -1),
    v => v >= 0 && readNamespaces()
);

// The filter when the menu opened: closing it after a change makes the new set the most recent (keys 1–9), once, not per click.
let atOpen: readonly string[] = [];
function dismissed() {
    const now = table.namespaces;
    if (now.length && !sameSet(now, atOpen)) recordRecentNamespaces(now);
}
watch(open, o => {
    if (o) atOpen = [...table.namespaces];
    else dismissed();
});
onBeforeUnmount(() => open.value && dismissed());
</script>

<template>
    <FilterMenu
        v-model:open="open"
        label="Namespace"
        noun="namespaces"
        :options="all"
        :selected="table.namespaces"
        @change="list => (table.namespaces = list)"
    />
</template>
