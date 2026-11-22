<script setup lang="ts">
import type { ImageVulnSummary } from '@v8s/client';

import { useVirtualizer } from '@tanstack/vue-virtual';
import { computed, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue';

import type { ImageSortKey, SourceFilter } from '@/lib/security.ts';
import type { SelectOption } from '@/lib/smart-select.ts';

import CheckMenu from '@/components/CheckMenu.vue';
import SmartSelect from '@/components/SmartSelect.vue';
import { clusterColor } from '@/lib/colors.ts';
import { formatCount } from '@/lib/format.ts';
import { errorText } from '@/lib/mutations.ts';
import { fetchImages, kubescapeScanNow } from '@/lib/security-api.ts';
import {
    DEFAULT_EXPOSURE_DEF,
    DEFAULT_SORT,
    EXPOSURE_UNKNOWN,
    EXPOSURES,
    exposureList,
    filterImages,
    isExposureUnknown,
    normExposureDef,
    normExposureFilter,
    NOT_EXPOSED,
    SEVERITIES,
    sevLabel,
    sevTone,
    shownExposure,
    sortImages,
    sourceLabel,
    totalsOf
} from '@/lib/security.ts';
import { isPhone, ROW_H_DESKTOP, ROW_H_PHONE } from '@/lib/viewport.ts';
import { useClustersStore } from '@/stores/clusters.ts';
import { useIntegrationsStore } from '@/stores/integrations.ts';

const props = defineProps<{ selected: string }>();
const emit = defineEmits<{ open: [image: ImageVulnSummary]; images: [list: readonly ImageVulnSummary[]] }>();

const clusters = useClustersStore();
const integrations = useIntegrationsStore();

// One request/response: a plain non-reactive array, replaced wholesale.
const images = shallowRef<readonly ImageVulnSummary[]>([]);
const loading = ref(true);
const error = ref('');
const text = ref('');
const source = ref<SourceFilter>('');
const onlyCritical = ref(false);
const sort = ref<{ key: ImageSortKey; desc: boolean }>({ ...DEFAULT_SORT });
const scanMenu = ref(false);
// "Exposed via": the definition (which kinds count as exposed, per viewer) and the filter over the shown kinds.
const EXPOSURE_DEF_KEY = 'v8s:vuln-exposure-def';
function loadExposureDef(): number {
    try {
        const v = Number(localStorage.getItem(EXPOSURE_DEF_KEY));
        return v ? normExposureDef(v) : DEFAULT_EXPOSURE_DEF;
    } catch {
        return DEFAULT_EXPOSURE_DEF;
    }
}
const exposureDef = ref(loadExposureDef());
const exposedVia = ref(0);
watch(exposureDef, def => {
    exposedVia.value = normExposureFilter(exposedVia.value, def);
    try {
        if (def === DEFAULT_EXPOSURE_DEF) localStorage.removeItem(EXPOSURE_DEF_KEY);
        else localStorage.setItem(EXPOSURE_DEF_KEY, String(def));
    } catch {
        // ignore: the definition just will not persist
    }
});
const defOptions = EXPOSURES.map(e => ({ bit: e.bit, label: e.label, hint: e.hint }));
const anyUnknown = computed(() => images.value.some(i => isExposureUnknown(i.exposure)));
const UNKNOWN_HINT = "A cluster's Services, EndpointSlices or Pods are still loading, or are not readable";
const filterOptions = computed(() => [
    ...EXPOSURES.filter(e => exposureDef.value & e.bit).map(e => ({ bit: e.bit, label: e.label })),
    { bit: NOT_EXPOSED, label: 'Not exposed' },
    ...(anyUnknown.value || exposedVia.value & EXPOSURE_UNKNOWN ? [{ bit: EXPOSURE_UNKNOWN, label: 'Unknown', hint: UNKNOWN_HINT }] : [])
]);
const filterLabel = computed(() => {
    const on = filterOptions.value.filter(o => exposedVia.value & o.bit);
    return !on.length ? 'any' : on.length === 1 ? on[0].label : `${on.length} selected`;
});
const shownOf = (i: ImageVulnSummary) => exposureList(shownExposure(i.exposure, exposureDef.value));
const exposureText = (i: ImageVulnSummary, long = false) => {
    const parts: string[] = shownOf(i).map(e => (long ? e.label : e.short));
    if (isExposureUnknown(i.exposure)) parts.push(parts.length ? 'unknown in some clusters' : 'unknown');
    return parts.join(', ') || 'not exposed';
};
const scanning = ref('');

let seq = 0;
// While a cluster's exposure is unknown (its stores are still loading) the list is re-read sooner than the 60 s refresh, a few times.
const UNKNOWN_RETRY_MS = 10_000;
const UNKNOWN_RETRIES = 6;
let unknownRetries = 0;
let retryTimer: ReturnType<typeof setTimeout> | undefined;
async function load(force = true) {
    const mine = ++seq;
    loading.value = true;
    try {
        const list = await fetchImages([...clusters.activeIds], force);
        if (mine !== seq) return;
        images.value = list;
        emit('images', list);
        error.value = '';
        clearTimeout(retryTimer);
        if (unknownRetries < UNKNOWN_RETRIES && list.some(i => isExposureUnknown(i.exposure))) {
            unknownRetries++;
            retryTimer = setTimeout(() => void load(), UNKNOWN_RETRY_MS);
        }
    } catch (e) {
        if (mine === seq) error.value = errorText(e);
    } finally {
        if (mine === seq) loading.value = false;
    }
}

let timer: ReturnType<typeof setInterval> | undefined;
const onVisible = () => {
    if (!document.hidden) void load();
};
onMounted(() => {
    // Refresh every 60 s while the page is visible; coming back to the tab refreshes at once.
    timer = setInterval(() => {
        if (!document.hidden) void load();
    }, 60_000);
    document.addEventListener('visibilitychange', onVisible);
});
onBeforeUnmount(() => {
    clearInterval(timer);
    clearTimeout(retryTimer);
    document.removeEventListener('visibilitychange', onVisible);
    seq++;
});
watch(
    () => clusters.activeIds.join('\n'),
    () => {
        unknownRetries = 0;
        void load();
    },
    { immediate: true }
);

const view = computed(() =>
    sortImages(
        filterImages(images.value, {
            text: text.value,
            source: source.value,
            onlyCritical: onlyCritical.value,
            exposedVia: exposedVia.value,
            exposureDef: exposureDef.value
        }),
        sort.value.key,
        sort.value.desc,
        exposureDef.value
    )
);
const totals = computed(() => totalsOf(view.value));
const sources = computed(() => [...new Set(images.value.map(i => i.source))].sort());
const sourceOptions = computed<SelectOption<SourceFilter>[]>(() => [
    { value: '', label: 'All sources' },
    ...sources.value.map(s => ({ value: s as SourceFilter, label: sourceLabel(s) }))
]);

function sortBy(key: ImageSortKey) {
    const cur = sort.value;
    sort.value = cur.key === key ? { key, desc: !cur.desc } : { key, desc: key !== 'image' && key !== 'source' };
}
const arrow = (key: ImageSortKey) => (sort.value.key === key ? (sort.value.desc ? ' ▼' : ' ▲') : '');
const ariaSort = (key: ImageSortKey) => (sort.value.key === key ? (sort.value.desc ? 'descending' : 'ascending') : 'none');

const rowH = computed(() => (isPhone.value ? ROW_H_PHONE : ROW_H_DESKTOP));
const scroller = ref<HTMLDivElement | null>(null);
const virtualizer = useVirtualizer<HTMLDivElement, Element>(
    computed(() => ({
        count: view.value.length,
        getScrollElement: () => scroller.value,
        estimateSize: () => rowH.value,
        overscan: isPhone.value ? 6 : 12
    }))
);
const items = computed(() => virtualizer.value.getVirtualItems());
// Rows are placed from cached sizes: re-measure when the layout switches between phone and desktop row heights.
watch(rowH, () => virtualizer.value.measure());
watch(
    () => [text.value, source.value, onlyCritical.value, exposedVia.value],
    () => scroller.value?.scrollTo({ top: 0 })
);

const nameOf = (c: string) => clusters.displayName(c);
const keyOf = (i: ImageVulnSummary) => `${i.image}|${i.source}`;
const isSel = (i: ImageVulnSummary) => !!props.selected && props.selected === keyOf(i);

const COLS: { key: ImageSortKey; label: string; right?: boolean }[] = [
    { key: 'image', label: 'Image' },
    { key: 'critical', label: 'Crit', right: true },
    { key: 'high', label: 'High', right: true },
    { key: 'medium', label: 'Med', right: true },
    { key: 'low', label: 'Low', right: true },
    { key: 'unknown', label: 'Unk', right: true },
    { key: 'workloads', label: 'Workloads', right: true },
    { key: 'exposure', label: 'Exposed via' },
    { key: 'reports', label: 'Reports', right: true },
    { key: 'clusters', label: 'Clusters' },
    { key: 'source', label: 'Source' }
];
const FULL: Record<string, string> = { Crit: 'Critical', Med: 'Medium', Unk: 'Unknown' };

async function scan(cluster: string) {
    scanMenu.value = false;
    scanning.value = cluster;
    try {
        if (await kubescapeScanNow(cluster)) setTimeout(() => void load(), 5000);
    } finally {
        scanning.value = '';
    }
}
const clusterList = computed(() => clusters.activeIds.map(id => ({ id, name: nameOf(id) })));
function onScan() {
    if (clusterList.value.length === 1) void scan(clusterList.value[0].id);
    else scanMenu.value = !scanMenu.value;
}
</script>

<template>
    <section class="images" data-testid="vuln-images">
        <div class="head">
            <h1>Vulnerabilities</h1>
            <span class="muted summary" role="status">
                {{ formatCount(view.length) }}{{ view.length !== images.length ? ` of ${formatCount(images.length)}` : '' }} image{{
                    images.length === 1 ? '' : 's'
                }}
            </span>
            <div class="grow"></div>
            <button class="btn" type="button" :disabled="loading" data-testid="refresh" @click="load()">
                {{ loading ? 'Refreshing…' : 'Refresh' }}
            </button>
            <div v-if="integrations.detected.kubescape" class="menuwrap" @keydown.esc.stop="scanMenu = false">
                <button
                    class="btn"
                    type="button"
                    :disabled="!!scanning || !clusterList.length"
                    :aria-expanded="scanMenu"
                    :aria-haspopup="clusterList.length > 1 ? 'menu' : undefined"
                    data-testid="scan-now"
                    @click="onScan"
                >
                    {{ scanning ? 'Scanning…' : 'Scan now' }}
                </button>
                <template v-if="scanMenu">
                    <div class="back" @mousedown="scanMenu = false"></div>
                    <div class="floating menu" role="menu" aria-label="Kubescape scan now">
                        <button v-for="c in clusterList" :key="c.id" class="mi" type="button" role="menuitem" @click="scan(c.id)">
                            Kubescape scan on {{ c.name }}
                        </button>
                    </div>
                </template>
            </div>
        </div>
        <div class="filters">
            <label class="field filter">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true">
                    <circle cx="7" cy="7" r="4.5" />
                    <path d="M10.5 10.5L14 14" />
                </svg>
                <input v-model="text" type="search" placeholder="Filter images" aria-label="Filter images" autocomplete="off" spellcheck="false" />
            </label>
            <SmartSelect v-if="sources.length > 1" v-model="source" :options="sourceOptions" aria-label="Scanner source" />
            <CheckMenu
                v-model="exposedVia"
                :options="filterOptions"
                label="Exposed via filter"
                heading="Show images exposed via"
                clearable
                btn-class="btn"
                testid="exposure-filter"
            >
                Exposed via: {{ filterLabel }}
                <svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true">
                    <path d="M4 6l4 4 4-4" />
                </svg>
            </CheckMenu>
            <CheckMenu
                v-if="isPhone"
                v-model="exposureDef"
                :options="defOptions"
                label="What counts as exposed"
                heading="Count as exposed"
                required
                btn-class="btn"
            >
                Exposure definition
            </CheckMenu>
            <label class="chip-wrap">
                <input v-model="onlyCritical" class="sr-only chip-input" type="checkbox" data-testid="only-critical" />
                <span class="chip" :class="{ on: onlyCritical }">Only critical</span>
            </label>
            <div class="grow"></div>
            <span class="tots muted" aria-label="Totals of the listed images">
                <span v-for="k in SEVERITIES" :key="k" class="tag" :class="totals[k] > 0 ? sevTone(k) : ''"
                    >{{ sevLabel(k) }} {{ formatCount(totals[k]) }}</span
                >
            </span>
        </div>
        <div class="card table-card">
            <div v-if="error" class="notice warn" role="alert">{{ error }} <button class="btn sm" type="button" @click="load()">Retry</button></div>
            <p v-else-if="loading && !images.length" class="empty muted" role="status">Loading images…</p>
            <p v-else-if="!images.length" class="empty muted" role="status">
                No scanner reports yet. Images appear once Trivy Operator or Kubescape has scanned a workload.
            </p>
            <p v-else-if="!view.length" class="empty muted" role="status">No image matches the filters.</p>
            <div
                v-else
                ref="scroller"
                class="scroller"
                :class="{ phone: isPhone }"
                role="table"
                aria-label="Images by severity"
                :aria-rowcount="view.length + 1"
                tabindex="0"
            >
                <div v-if="!isPhone" class="tr th" role="row">
                    <span
                        v-for="c in COLS"
                        :key="c.key"
                        class="c"
                        :class="{ r: c.right, thx: c.key === 'exposure' }"
                        role="columnheader"
                        :aria-sort="ariaSort(c.key)"
                    >
                        <button class="th-btn" type="button" :title="`Sort by ${(FULL[c.label] ?? c.label).toLowerCase()}`" @click="sortBy(c.key)">
                            {{ c.label }}{{ arrow(c.key) }}
                        </button>
                        <CheckMenu
                            v-if="c.key === 'exposure'"
                            v-model="exposureDef"
                            :options="defOptions"
                            label="What counts as exposed"
                            heading="Count as exposed"
                            note="Rows show - when none of these apply."
                            required
                            btn-class="th-cfg"
                            btn-title="Choose what counts as exposed"
                            testid="exposure-def"
                        >
                            <svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true">
                                <path d="M2 4h12M4 8h8M6 12h4" />
                            </svg>
                        </CheckMenu>
                    </span>
                </div>
                <div class="vp" :style="{ height: virtualizer.getTotalSize() + 'px' }">
                    <template v-for="vi in items" :key="vi.index">
                        <div
                            v-if="view[vi.index]"
                            class="tr row"
                            :class="{ on: isSel(view[vi.index]), mcard: isPhone }"
                            role="row"
                            tabindex="0"
                            :aria-label="`${view[vi.index].image}, ${view[vi.index].critical} critical, ${view[vi.index].high} high`"
                            :style="{ height: rowH + 'px', transform: `translateY(${vi.start}px)` }"
                            @click="emit('open', view[vi.index])"
                            @keydown.enter.prevent="emit('open', view[vi.index])"
                            @keydown.space.prevent="emit('open', view[vi.index])"
                        >
                            <template v-if="isPhone">
                                <div class="l1">
                                    <span class="mono img" :title="view[vi.index].image">{{ view[vi.index].image }}</span>
                                    <span class="tag">{{ sourceLabel(view[vi.index].source) }}</span>
                                </div>
                                <div class="l2">
                                    <span v-for="k in SEVERITIES" :key="k" class="tag" :class="view[vi.index][k] > 0 ? sevTone(k) : ''"
                                        >{{ sevLabel(k)[0] }} {{ view[vi.index][k] }}</span
                                    >
                                </div>
                                <div class="l3 muted">
                                    {{ view[vi.index].workloads }} workloads ·
                                    {{ exposureText(view[vi.index]) }}
                                    · {{ view[vi.index].reports }} reports · {{ view[vi.index].clusters.map(nameOf).join(', ') }}
                                </div>
                            </template>
                            <template v-else>
                                <span class="c mono img" role="cell" :title="view[vi.index].image">{{ view[vi.index].image }}</span>
                                <span v-for="k in SEVERITIES" :key="k" class="c r" role="cell"
                                    ><span v-if="view[vi.index][k] > 0" class="tag" :class="sevTone(k)">{{ view[vi.index][k] }}</span
                                    ><span v-else class="muted">0</span></span
                                >
                                <span class="c r" role="cell">{{ formatCount(view[vi.index].workloads) }}</span>
                                <span class="c ctags" role="cell" data-testid="exposure-cell" :title="exposureText(view[vi.index], true)">
                                    <span v-for="e in shownOf(view[vi.index])" :key="e.bit" class="tag" :title="e.hint">{{ e.short }}</span>
                                    <span v-if="isExposureUnknown(view[vi.index].exposure)" class="tag" :title="UNKNOWN_HINT">?</span>
                                    <span v-else-if="!shownOf(view[vi.index]).length" class="muted">-</span>
                                </span>
                                <span class="c r" role="cell">{{ formatCount(view[vi.index].reports) }}</span>
                                <span class="c ctags" role="cell">
                                    <span v-for="id in view[vi.index].clusters.slice(0, 2)" :key="id" class="tag" :class="clusters.tagTone(id)"
                                        ><span class="dot" :style="{ background: clusterColor(id) }"></span>{{ nameOf(id) }}</span
                                    >
                                    <span v-if="view[vi.index].clusters.length > 2" class="muted">+{{ view[vi.index].clusters.length - 2 }}</span>
                                </span>
                                <span class="c" role="cell"><span class="tag">{{ sourceLabel(view[vi.index].source) }}</span></span>
                            </template>
                        </div>
                    </template>
                </div>
            </div>
        </div>
    </section>
</template>

<style scoped>
.images {
    container-type: inline-size;
    flex: 1 1 0;
    min-width: 0;
    min-height: 0;
    display: flex;
    flex-direction: column;
    gap: 12px;
}
.head,
.filters {
    display: flex;
    align-items: center;
    gap: 10px;
    flex-wrap: wrap;
}
h1 {
    font-size: 19px;
    font-weight: 650;
    letter-spacing: -0.01em;
    margin: 0;
}
.summary {
    font-size: 13px;
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
.chip-wrap {
    display: inline-flex;
    position: relative;
}
.tots {
    display: flex;
    gap: 4px;
    flex-wrap: wrap;
}
.table-card {
    flex: 1 1 0;
    min-height: 0;
    display: flex;
    flex-direction: column;
}
.scroller {
    flex: 1 1 0;
    min-height: 0;
    overflow: auto;
    position: relative;
}
.vp {
    position: relative;
    min-width: 1130px;
}
.tr {
    display: grid;
    grid-template-columns: minmax(260px, 1fr) repeat(5, 56px) 84px 160px 72px 190px 90px;
    align-items: center;
    gap: 12px;
    padding: 0 16px;
    min-width: 1130px;
}
.row {
    position: absolute;
    top: 0;
    left: 0;
    right: 0;
    border-bottom: 1px solid var(--border);
    font-size: 13px;
    cursor: pointer;
}
.row:hover {
    background: var(--hover);
}
.row.on {
    background: var(--accent-soft);
}
.row:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
}
.th {
    position: sticky;
    top: 0;
    z-index: 2;
    height: 32px;
    background: var(--s2);
    border-bottom: 1px solid var(--border);
    font-size: 12px;
    font-weight: 600;
    color: var(--text2);
}
.th-btn {
    all: unset;
    cursor: pointer;
}
.th-btn:focus-visible {
    outline: 2px solid var(--focus);
}
.thx {
    display: flex;
    align-items: center;
    gap: 4px;
}
.thx :deep(.th-cfg) {
    all: unset;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 20px;
    height: 20px;
    border-radius: 4px;
    color: var(--text3);
    cursor: pointer;
}
.thx :deep(.th-cfg:hover),
.thx :deep(.th-cfg[aria-expanded='true']) {
    background: var(--hover);
    color: var(--text);
}
.thx :deep(.th-cfg:focus-visible) {
    outline: 2px solid var(--focus);
}
.c {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
.r {
    text-align: right;
}
.ctags {
    display: flex;
    gap: 4px;
    align-items: center;
}
.empty {
    padding: 40px 16px;
    text-align: center;
    margin: 0;
}
.notice {
    margin: 12px;
}
.menuwrap {
    position: relative;
}
.back {
    position: fixed;
    inset: 0;
    z-index: 20;
}
.menu {
    position: absolute;
    z-index: 21;
    right: 0;
    top: 36px;
    min-width: 240px;
    padding: 6px;
}
.mi {
    all: unset;
    box-sizing: border-box;
    display: block;
    width: 100%;
    padding: 6px 8px;
    border-radius: 5px;
    font-size: 13px;
    cursor: pointer;
}
.mi:hover,
.mi:focus-visible {
    background: var(--hover);
    outline: 0;
}
.mcard {
    display: flex;
    flex-direction: column;
    justify-content: center;
    align-items: stretch;
    gap: 3px;
    min-width: 0;
    padding: 0 12px;
}
.tr.mcard,
.vp:has(.mcard) {
    min-width: 0;
}
.l1,
.l2 {
    display: flex;
    gap: 6px;
    align-items: center;
}
.l1 .img {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
.l3 {
    font-size: 12px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
@container (max-width: 800px) {
    .tr:not(.mcard) {
        grid-template-columns: minmax(160px, 1fr) repeat(5, 40px) 70px 132px 84px;
        gap: 8px;
        padding: 0 10px;
        min-width: 0;
    }
    .tr:not(.mcard) > :nth-child(9),
    .tr:not(.mcard) > :nth-child(10) {
        display: none;
    }
    .tr:not(.mcard) > :nth-child(11) {
        grid-column: 9;
    }
    .vp {
        min-width: 0;
    }
}
@media (max-width: 639.98px) {
    .filter {
        width: 100%;
        height: 40px;
    }
}
@media (pointer: coarse) {
    .head .btn {
        min-height: 44px;
    }
    .mi {
        min-height: 44px;
        display: flex;
        align-items: center;
    }
}
</style>
