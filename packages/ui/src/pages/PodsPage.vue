<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted } from 'vue';

import NamespaceMenu from '@/components/NamespaceMenu.vue';
import PodsTable from '@/components/PodsTable.vue';
import { activePods } from '@/lib/active-view.ts';
import { getPodsAll } from '@/lib/agent.ts';
import { phaseColor } from '@/lib/colors.ts';
import { formatCount } from '@/lib/format.ts';
import { usePodsView } from '@/lib/pods-view.ts';
import { isPhone } from '@/lib/viewport.ts';
import { useClustersStore } from '@/stores/clusters.ts';
import { SORT_LABELS, TONE_CHIPS, usePodsStore } from '@/stores/pods.ts';

const pods = usePodsStore();
const clusters = useClustersStore();
const { handle, counts } = usePodsView();
const podsAll = getPodsAll();

onMounted(() => (activePods.value = handle));
onBeforeUnmount(() => {
    if (activePods.value === handle) activePods.value = null;
});

const summary = computed(() => {
    const total = podsAll.total.value;
    const n = clusters.activeIds.length;
    const base = `${formatCount(total)}${isPhone.value ? '' : ` across ${n} cluster${n === 1 ? '' : 's'}`}`;
    return pods.filtered ? `${base} · ${formatCount(handle.total.value)} match` : base;
});

const syncTags = computed(() =>
    clusters.states.map(s => {
        const name = clusters.nameById.get(s.id) ?? s.id;
        const syncing = s.phase === 'syncing' || s.phase === 'connecting';
        const text = s.phase === 'ready' ? 'synced' : syncing ? `${s.phase} · ${formatCount(s.synced)} rows` : s.phase;
        const tone = s.phase === 'ready' ? '' : s.phase === 'unreachable' || s.phase === 'auth-required' ? 'danger' : 'warning';
        return { id: s.id, name, text, tone, color: phaseColor(s.phase), title: s.message ?? '' };
    })
);

const sortLabel = computed(() =>
    pods.sort.length
        ? 'Sorted by ' + pods.sort.map(s => SORT_LABELS[s.col] + (s.desc ? ' (desc)' : '')).join(', then ')
        : 'Sorted by status, then name'
);
const selCount = computed(() => pods.selected.size);
</script>

<template>
    <main class="page">
        <div class="head">
            <h1>Pods</h1>
            <span class="muted summary">{{ summary }}</span>
            <div class="grow"></div>
            <span v-for="t in syncTags" :key="t.id" class="tag sync" :class="t.tone" :title="t.title"
                ><span class="dot" :style="{ background: t.color }"></span>{{ t.name }} {{ t.text }}</span
            >
            <button class="btn opt" type="button" disabled>Columns</button>
            <button class="btn primary opt" type="button" disabled>Create</button>
        </div>
        <div class="filters">
            <label class="field filter">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true">
                    <circle cx="7" cy="7" r="4.5" />
                    <path d="M10.5 10.5L14 14" />
                </svg>
                <input
                    id="pods-filter"
                    v-model="pods.text"
                    type="search"
                    placeholder="Filter by name"
                    aria-label="Filter pods"
                    autocomplete="off"
                    spellcheck="false"
                />
                <span class="kbd">/</span>
            </label>
            <NamespaceMenu :handle="handle" />
            <label v-for="c in TONE_CHIPS" :key="c.key" class="chip-wrap">
                <input class="sr-only chip-input" type="checkbox" :checked="pods.tones.includes(c.tone)" @change="pods.toggleTone(c.tone)" />
                <span class="chip" :class="{ on: pods.tones.includes(c.tone) }">
                    <span class="dot" :style="{ background: c.color }"></span>{{ c.label }} <b>{{ formatCount(counts[c.key].total.value) }}</b>
                </span>
            </label>
            <div class="grow"></div>
            <span class="muted sortlabel">{{ sortLabel }}</span>
        </div>
        <div class="card table-card">
            <PodsTable :handle="handle" />
            <div v-if="selCount" class="bulk" role="toolbar" aria-label="Bulk actions">
                <span class="selcount">{{ selCount }} selected</span>
                <button class="btn sm" type="button">Logs</button>
                <button class="btn sm" type="button">Shell</button>
                <button class="btn sm" type="button">Restart owner</button>
                <button class="btn sm danger" type="button">Delete</button>
                <div class="grow"></div>
                <button class="btn sm ghost" type="button" @click="pods.clearSelection()">Clear</button>
                <span class="muted legend">
                    <span><span class="kbd">j</span> <span class="kbd">k</span> move</span><span><span class="kbd">x</span> select</span
                    ><span><span class="kbd">↵</span> open</span>
                </span>
            </div>
        </div>
    </main>
</template>

<style scoped>
.page {
    flex: 1 1 0;
    min-width: 0;
    min-height: 0;
    display: flex;
    flex-direction: column;
    padding: 16px 20px 12px;
    gap: 12px;
}
.head {
    display: flex;
    align-items: center;
    gap: 12px;
    flex-wrap: wrap;
}
h1 {
    font-size: 19px;
    font-weight: 650;
    letter-spacing: -0.01em;
}
.summary {
    font-size: 13px;
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
.chip-wrap {
    display: inline-flex;
    position: relative;
}
.sortlabel {
    font-size: 12px;
}
.bulk {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 10px 12px;
    border-top: 1px solid var(--border);
    background: var(--bg);
    flex: none;
    flex-wrap: wrap;
}
.selcount {
    font-size: 13px;
    font-weight: 500;
}
.legend {
    font-size: 12px;
    display: flex;
    gap: 10px;
    align-items: center;
}
@media (max-width: 899.98px) {
    .sync,
    .opt {
        display: none;
    }
}
@media (max-width: 639.98px) {
    .page {
        padding: 12px 12px 8px;
    }
    .filter {
        width: 100%;
        height: 40px;
    }
    .sortlabel,
    .legend {
        display: none;
    }
    .chip {
        height: 32px;
    }
}
</style>
