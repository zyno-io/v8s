<script setup lang="ts">
import type { ObjectRef } from '@v8s/client';

import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue';

import ActionBar from '@/components/ActionBar.vue';
import ClusterElevateMenu from '@/components/ClusterElevateMenu.vue';
import NamespaceMenu from '@/components/NamespaceMenu.vue';
import ResourceTable from '@/components/ResourceTable.vue';
import { getPodsAll, kinds } from '@/lib/agent.ts';
import { phaseColor } from '@/lib/colors.ts';
import { guessUsage, planColumns } from '@/lib/columns.ts';
import { formatCount } from '@/lib/format.ts';
import { kindLabel, labelFromGvr, resolveKind } from '@/lib/kinds.ts';
import { useResourceView } from '@/lib/resource-view.ts';
import { PODS_GVR } from '@/lib/route.ts';
import { keyAction } from '@/lib/row-keys.ts';
import { decorFor } from '@/lib/table-decor.ts';
import { isPhone, usePageVisible } from '@/lib/viewport.ts';
import { useVpaIndex, vpaWantKey } from '@/lib/vpa-status.ts';
import { useClustersStore } from '@/stores/clusters.ts';
import { useSettingsStore } from '@/stores/settings.ts';
import { isFiltered, useTableStore } from '@/stores/table.ts';
import { useUiStore } from '@/stores/ui.ts';

const props = defineProps<{ gvr: string }>();

const table = useTableStore();
const clusters = useClustersStore();
const settings = useSettingsStore();
const ui = useUiStore();
const state = table.of(props.gvr);
// Pods and nodes subscribe with a guess of the usage flag (known keys, saved layout, sort), so the first rows already carry usage.
const { handle, counts, chips, hasTone } = useResourceView(props.gvr, {
    usage:
        !(clusters.states.length > 0 && clusters.states.every(s => s.metrics === 'absent')) &&
        guessUsage(props.gvr, settings.layout(props.gvr), state.sort)
});
const podsAll = props.gvr === PODS_GVR ? getPodsAll() : null;

onMounted(() => (table.currentGvr = props.gvr));
onBeforeUnmount(() => {
    if (table.currentGvr === props.gvr) table.currentGvr = '';
});

const kind = computed(() => kinds.value.find(k => k.gvr === props.gvr));
const title = computed(() => (kind.value ? kindLabel(kind.value) : labelFromGvr(props.gvr)));
const namespaced = computed(() => kind.value?.namespaced ?? true);
const starred = computed(() => settings.isStarred(props.gvr));

const showCluster = computed(() => clusters.activeIds.length > 1);
const decor = decorFor(props.gvr);
// Every active cluster without a metrics API: the metrics columns are dropped (the Columns menu lists them disabled).
const metricsOff = computed(() => clusters.states.length > 0 && clusters.states.every(s => s.metrics === 'absent'));
// Pods: the VPA column shows only while some active cluster has a pod whose requests a VPA set (installed-but-unused stays hidden).
const vpaOff = computed(() => props.gvr === 'v1/pods' && !clusters.states.some(s => s.vpa));
const plan = computed(() =>
    planColumns(handle.columns.value, {
        gvr: props.gvr,
        showCluster: showCluster.value,
        layout: settings.layout(props.gvr),
        decor,
        metricsOff: metricsOff.value,
        vpaOff: vpaOff.value
    })
);
// Pods: the VPA list (a handful of rows, watched by the agent only while subscribed) colours the VPA tags by the VPA's status.
// Subscribed only while the VPA column is on screen.
const vpaWant = computed(() => {
    if (props.gvr !== 'v1/pods' || !plan.value.cols.some(c => c.kind === 'vpa')) return '';
    const g = resolveKind(kinds.value, 'VerticalPodAutoscaler', 'autoscaling.k8s.io')?.gvr;
    return g ? vpaWantKey(g, clusters.activeIds) : '';
});
const vpaIndex = useVpaIndex(vpaWant);

// Usage is polled by the agent only while this page needs it: a metrics column on screen or a sort by one (sorting needs a
// value for every row, not just the visible ones), and the tab not hidden for 30 s.
const pageVisible = usePageVisible(30_000);
const sortsMetric = computed(() => state.sort.some(s => plan.value.metricKeys.includes(s.col)));
const usageWanted = computed(() => !metricsOff.value && (plan.value.hasMetrics || sortsMetric.value) && pageVisible.value);
watch(usageWanted, v => handle.update({ usage: v }), { immediate: true });
const metricTags = computed(() =>
    usageWanted.value
        ? clusters.states
              .filter(s => s.metrics && s.metrics !== 'ok')
              .map(s => ({
                  id: s.id,
                  text: `${clusters.nameById.get(s.id) ?? s.id} · ${s.metrics === 'absent' ? 'no metrics' : s.metrics === 'forbidden' ? 'metrics not allowed' : 'metrics unavailable'}`,
                  title: s.metricsMessage ?? ''
              }))
        : []
);
const unavailable = computed(() => handle.columns.value !== null && handle.columns.value.length === 0);

const filtered = computed(() => isFiltered(state));
const summary = computed(() => {
    const n = clusters.activeIds.length;
    const scope = isPhone.value ? '' : ` in ${n} cluster${n === 1 ? '' : 's'}`;
    if (podsAll) {
        const base = `${formatCount(podsAll.total.value)}${scope}`;
        return filtered.value ? `${base} · ${formatCount(handle.total.value)} match` : base;
    }
    return filtered.value ? `${formatCount(handle.total.value)} match` : `${formatCount(handle.total.value)}${scope}`;
});

const syncTags = computed(() =>
    clusters.states.map(s => {
        const name = clusters.nameById.get(s.id) ?? s.id;
        const syncing = s.phase === 'syncing' || s.phase === 'connecting';
        const text = s.phase === 'ready' ? 'synced' : syncing ? `${s.phase} · ${formatCount(s.synced)} rows` : s.phase;
        const tone = s.phase === 'ready' ? '' : s.phase === 'unreachable' || s.phase === 'auth-required' ? 'danger' : 'warning';
        return {
            id: s.id,
            name,
            text,
            tone,
            color: phaseColor(s.phase),
            title: s.message ?? '',
            elevated: s.elevated === true,
            writeCtx: clusters.writeContextOf(s.id)
        };
    })
);

// Words are lowercased in the sentence; metric labels (%CPU/L, MEM) are names and stay as they are.
const sortName = (label: string) => (/^[%A-Z/]+$/.test(label) ? label : label.toLowerCase());
const colLabel = (key: string) => plan.value.all.find(c => c.key === key)?.label ?? key;
const sortLabel = computed(() =>
    state.sort.length
        ? 'Sorted by ' + state.sort.map(s => sortName(colLabel(s.col)) + (s.desc ? ' (desc)' : '')).join(', then ')
        : hasTone.value
          ? 'Sorted by status, then name'
          : 'Default order'
);

// ---- selection → bulk bar; focused row → drawer actions elsewhere ----
const selected = computed<ObjectRef[]>(() => [...state.selected.values()]);
// Bulk actions across clusters are desktop-only: too easy to fat-finger on a phone.
const crossCluster = computed(() => isPhone.value && new Set(selected.value.map(r => r.cluster)).size > 1);
watch(
    () => [state.text, state.namespaces, state.tones, state.sort],
    () => table.resetInteraction(props.gvr)
);
// A row key's action (Ctrl+D, r, Shift+F) is a one-shot request: never replay it after leaving this type.
keyAction.value = null;
watch(
    () => props.gvr,
    () => (keyAction.value = null)
);
onBeforeUnmount(() => (keyAction.value = null));
function onKeyActionEnd() {
    keyAction.value = null;
}
function onKeyActionDone() {
    keyAction.value = null;
    table.clearSelection(props.gvr);
}

// ---- column layout: widths saved once per resize, visibility from the Columns menu ----
let saveTimer: ReturnType<typeof setTimeout> | undefined;
function saveWidth(key: string, width: number) {
    const cur = settings.layout(props.gvr);
    clearTimeout(saveTimer);
    saveTimer = setTimeout(() => void settings.saveLayout(props.gvr, { ...cur, widths: { ...cur.widths, [key]: width } }).catch(() => {}), 150);
}
function toggleColumn(key: string) {
    const cur = settings.layout(props.gvr);
    const hidden = new Set(cur.hidden ?? []);
    const shown = new Set(cur.shown ?? []);
    if (plan.value.visibleKeys.has(key)) {
        hidden.add(key);
        shown.delete(key);
    } else {
        hidden.delete(key);
        if (plan.value.defaultHidden.has(key)) shown.add(key); // a default-hidden column is shown by listing it, not by un-hiding it
    }
    void settings.saveLayout(props.gvr, { ...cur, hidden: [...hidden], shown: [...shown] }).catch(() => {});
}
function resetLayout() {
    void settings.saveLayout(props.gvr, {}).catch(() => {});
}
const colsOpen = ref(false);

function star() {
    void settings.toggleStarred(props.gvr).catch(e => ui.toast('danger', String(e)));
}
</script>

<template>
    <main class="page">
        <div class="head">
            <h1>{{ title }}</h1>
            <button
                class="btn ghost sm starbtn"
                type="button"
                :aria-pressed="starred"
                :aria-label="starred ? `Unstar ${title}` : `Star ${title}`"
                :title="starred ? 'Unstar this type' : 'Star this type'"
                data-testid="star"
                @click="star"
            >
                <svg
                    width="14"
                    height="14"
                    viewBox="0 0 16 16"
                    :fill="starred ? 'currentColor' : 'none'"
                    stroke="currentColor"
                    stroke-width="1.5"
                    aria-hidden="true"
                >
                    <path d="M8 1.8l1.9 4 4.3.5-3.2 3 .9 4.3L8 11.4 4.1 13.6 5 9.3l-3.2-3 4.3-.5z" />
                </svg>
            </button>
            <span class="muted summary">{{ summary }}</span>
            <div class="grow"></div>
            <span v-for="t in metricTags" :key="'m' + t.id" class="tag sync warning" :title="t.title" data-testid="metrics-tag">{{ t.text }}</span>
            <span v-for="t in syncTags" :key="t.id" class="tag sync" :class="t.tone" :title="t.title"
                ><span class="dot" :style="{ background: t.color }"></span>{{ t.name }} {{ t.text }}<ClusterElevateMenu :id="t.id"
            /></span>
            <template v-for="t in syncTags" :key="'e' + t.id"
                ><span v-if="t.elevated" class="tag sync warning" :title="'Running as ' + t.writeCtx" data-testid="elevated-tag"
                    >{{ t.name }} elevated</span
                ></template
            >
            <div class="colwrap opt" @keydown.esc.stop="colsOpen && (colsOpen = false)">
                <button class="btn" type="button" :aria-expanded="colsOpen" aria-haspopup="dialog" @click="colsOpen = !colsOpen">Columns</button>
                <template v-if="colsOpen">
                    <div class="back" @mousedown="colsOpen = false"></div>
                    <div class="floating colmenu" role="dialog" aria-label="Columns">
                        <p v-if="metricsOff" class="cnote muted">CPU and memory usage need metrics-server, which is not installed.</p>
                        <label v-for="c in plan.all" :key="c.key" class="copt">
                            <input
                                class="cb"
                                type="checkbox"
                                :checked="plan.visibleKeys.has(c.key) && !c.note"
                                :disabled="!c.hideable || !!c.note"
                                @change="toggleColumn(c.key)"
                            />{{ c.label }}
                        </label>
                        <div class="cfoot"><button class="btn ghost sm" type="button" @click="resetLayout">Reset layout</button></div>
                    </div>
                </template>
            </div>
            <button class="btn primary opt" type="button" data-testid="create" @click="ui.createOpen = true">Create</button>
        </div>
        <div class="filters">
            <label class="field filter">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true">
                    <circle cx="7" cy="7" r="4.5" />
                    <path d="M10.5 10.5L14 14" />
                </svg>
                <input
                    id="pods-filter"
                    v-model="state.text"
                    type="search"
                    :placeholder="kind?.ips ? 'Filter by name or IP' : 'Filter by name'"
                    :aria-label="`Filter ${title.toLowerCase()}`"
                    autocomplete="off"
                    spellcheck="false"
                />
                <span class="kbd">/</span>
            </label>
            <NamespaceMenu v-if="namespaced" :gvr="gvr" />
            <label v-for="c in hasTone ? chips : []" :key="c.key" class="chip-wrap">
                <input class="sr-only chip-input" type="checkbox" :checked="state.tones.includes(c.tone)" @change="table.toggleTone(gvr, c.tone)" />
                <span class="chip" :class="{ on: state.tones.includes(c.tone) }">
                    <span class="dot" :style="{ background: c.color }"></span>{{ c.label }} <b>{{ formatCount(counts[c.key]?.total.value ?? 0) }}</b>
                </span>
            </label>
            <div class="grow"></div>
            <span class="muted sortlabel">{{ sortLabel }}</span>
        </div>
        <div class="card table-card">
            <div v-if="unavailable" class="unavail" role="status">
                <p>None of the selected clusters serves <b>{{ title }}</b>.</p>
                <p class="muted">Pick other clusters from the selector, or choose another type.</p>
            </div>
            <ResourceTable
                v-else
                :handle="handle"
                :gvr="gvr"
                :plan="plan"
                :title="title"
                :namespaced="namespaced"
                :decor="decor"
                :vpa-index="vpaIndex"
                key-actions
                @resize="saveWidth"
            />
            <ActionBar
                v-if="keyAction && keyAction.gvr === gvr"
                :key="keyAction.seq"
                :gvr="gvr"
                :targets="keyAction.targets"
                :kind-label="title"
                variant="key"
                :run="keyAction.action"
                @done="onKeyActionDone"
                @end="onKeyActionEnd"
            />
            <div v-if="selected.length" class="bulk" role="toolbar" aria-label="Bulk actions">
                <span class="selcount">{{ selected.length }} selected</span>
                <span v-if="crossCluster" class="muted legend" data-testid="bulk-desktop-only"
                    >Bulk actions across clusters need a larger screen.</span
                >
                <ActionBar v-else :gvr="gvr" :targets="selected" :kind-label="title" variant="bar" @done="table.clearSelection(gvr)" />
                <div class="grow"></div>
                <button class="btn sm ghost" type="button" @click="table.clearSelection(gvr)">Clear</button>
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
.starbtn {
    padding: 0 6px;
}
.starbtn[aria-pressed='true'] {
    color: var(--warning);
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
.unavail {
    padding: 40px 16px;
    text-align: center;
}
.unavail p {
    margin: 0 0 6px;
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
.colwrap {
    position: relative;
}
.back {
    position: fixed;
    inset: 0;
    z-index: 20;
}
.colmenu {
    position: absolute;
    z-index: 21;
    right: 0;
    top: 36px;
    width: 220px;
    max-height: 340px;
    overflow: auto;
    padding: 6px;
}
.copt {
    display: flex;
    align-items: center;
    gap: 8px;
    height: 28px;
    padding: 0 6px;
    border-radius: 5px;
    font-size: 13px;
    cursor: pointer;
}
.cnote {
    margin: 2px 6px 6px;
    font-size: 12px;
    line-height: 1.35;
}
.copt:hover {
    background: var(--hover);
}
.cfoot {
    border-top: 1px solid var(--border);
    margin-top: 4px;
    padding-top: 4px;
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
    .sortlabel {
        display: none;
    }
    .legend:not([data-testid]) {
        display: none;
    }
    .chip {
        height: 32px;
    }
}
</style>
