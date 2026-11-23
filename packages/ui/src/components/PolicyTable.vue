<script setup lang="ts">
import { computed } from 'vue';

import NamespaceMenu from '@/components/NamespaceMenu.vue';
import ResourceTable from '@/components/ResourceTable.vue';
import { planColumns } from '@/lib/columns.ts';
import { formatCount } from '@/lib/format.ts';
import { useResourceView } from '@/lib/resource-view.ts';
import { securityDecor } from '@/lib/security-decor.ts';
import { useClustersStore } from '@/stores/clusters.ts';
import { useSettingsStore } from '@/stores/settings.ts';
import { useTableStore } from '@/stores/table.ts';

const props = defineProps<{ gvr: string; title: string; namespaced: boolean }>();

const table = useTableStore();
const clusters = useClustersStore();
const settings = useSettingsStore();
const state = table.of(props.gvr);
const { handle } = useResourceView(props.gvr);
const decor = securityDecor(props.gvr);
const plan = computed(() =>
    planColumns(handle.columns.value, { gvr: props.gvr, showCluster: clusters.activeIds.length > 1, layout: settings.layout(props.gvr), decor })
);
const unavailable = computed(() => handle.columns.value !== null && handle.columns.value.length === 0);

function saveWidth(key: string, width: number) {
    const cur = settings.layout(props.gvr);
    void settings.saveLayout(props.gvr, { ...cur, widths: { ...cur.widths, [key]: width } }).catch(() => {});
}
</script>

<template>
    <div class="ptable">
        <div class="filters">
            <label class="field filter">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true">
                    <circle cx="7" cy="7" r="4.5" />
                    <path d="M10.5 10.5L14 14" />
                </svg>
                <input
                    v-model="state.text"
                    type="search"
                    placeholder="Filter by name"
                    :aria-label="`Filter ${title.toLowerCase()}`"
                    autocomplete="off"
                    spellcheck="false"
                />
            </label>
            <NamespaceMenu v-if="namespaced" :gvr="gvr" />
            <div class="grow"></div>
            <span class="muted count" role="status">{{ formatCount(handle.total.value) }} {{ title.toLowerCase() }}</span>
        </div>
        <div class="card table-card">
            <div v-if="unavailable" class="unavail" role="status" data-testid="unavailable">
                <p v-if="handle.error.value?.code === 'forbidden'">You are not allowed to list <b>{{ title }}</b> on the selected clusters.</p>
                <p v-else>None of the selected clusters serves <b>{{ title }}</b>.</p>
            </div>
            <ResourceTable
                v-else
                :handle="handle"
                :gvr="gvr"
                :plan="plan"
                :title="title"
                :namespaced="namespaced"
                :decor="decor"
                @resize="saveWidth"
            />
        </div>
    </div>
</template>

<style scoped>
.ptable {
    flex: 1 1 0;
    min-height: 0;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 10px;
}
.filters {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
}
.filter {
    width: 300px;
    max-width: 100%;
}
.filter input {
    flex: 1;
    min-width: 0;
    color: var(--text);
}
.count {
    font-size: 12px;
}
.table-card {
    flex: 1 1 0;
    min-height: 0;
    display: flex;
    flex-direction: column;
}
.unavail {
    padding: 40px 16px;
    text-align: center;
}
@media (max-width: 639.98px) {
    .filter {
        width: 100%;
        height: 40px;
    }
}
</style>
