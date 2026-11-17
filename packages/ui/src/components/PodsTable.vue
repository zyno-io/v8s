<script setup lang="ts">
import type { Cell, Row, ViewHandle } from '@v8s/client';

import { useVirtualizer } from '@tanstack/vue-virtual';
import { POD, TONE_NAMES } from '@v8s/client';
import { computed, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue';

import { clusterColor } from '@/lib/colors.ts';
import { formatAge, OLD_AGE_MS } from '@/lib/format.ts';
import { setPageKeyHandler } from '@/lib/keys.ts';
import { isPhone, ROW_H_DESKTOP, ROW_H_PHONE, windowPad } from '@/lib/viewport.ts';
import { useClustersStore } from '@/stores/clusters.ts';
import { SORT_LABELS, usePodsStore } from '@/stores/pods.ts';
import { useUiStore } from '@/stores/ui.ts';

const props = defineProps<{ handle: ViewHandle }>();

const pods = usePodsStore();
const ui = useUiStore();
const clusters = useClustersStore();

const HEADER_H = ROW_H_DESKTOP;
const EMPTY_ROW = Object.freeze([]) as unknown as Row;

const scroller = ref<HTMLDivElement | null>(null);
const rowH = computed(() => (isPhone.value ? ROW_H_PHONE : ROW_H_DESKTOP));
const headerH = computed(() => (isPhone.value ? 0 : HEADER_H));

// Fixed-height rows: a constant estimateSize and no measureElement, so scrolling never forces layout reads.
const virtualizer = useVirtualizer<HTMLDivElement, Element>(
    computed(() => ({
        count: props.handle.total.value,
        getScrollElement: () => scroller.value,
        estimateSize: () => rowH.value,
        overscan: isPhone.value ? 6 : 10,
        scrollMargin: headerH.value
    }))
);

const totalPx = computed(() => props.handle.total.value * rowH.value);

// ---- visible slice: one v-for, plain elements, memoised on id + version + selection + focus + age text ----

interface Item {
    i: number;
    id: number;
    row: Row;
    ver: number;
    sel: boolean;
    foc: boolean;
    age: string;
}

const tick = shallowRef(0);
let allOld = false;

const slice = computed<Item[]>(() => {
    void tick.value;
    const h = props.handle;
    void h.version.value;
    const off = h.offset.value;
    const ids = h.ids;
    const rows = h.rows;
    const sel = pods.selected;
    const foc = pods.focusId;
    const now = Date.now();
    let old = true;
    const out: Item[] = [];
    for (const vi of virtualizer.value.getVirtualItems()) {
        const i = vi.index;
        const k = i - off;
        const id = k >= 0 && k < ids.length ? ids[k] : 0;
        const row = id ? rows.get(id) : undefined;
        let age = '';
        if (row) {
            const created = row[POD.createdAt] as number;
            age = formatAge(created, now);
            if (now - created < OLD_AGE_MS) old = false;
        }
        out.push({ i, id, row: row ?? EMPTY_ROW, ver: row ? row[1] : -1, sel: id !== 0 && sel.has(id), foc: id !== 0 && id === foc, age });
    }
    allOld = old;
    return out;
});

// One shared 1 s tick (30 s when every visible age is over an hour); only the age text in the memo key changes.
let timer: ReturnType<typeof setInterval> | undefined;
let slowCount = 0;
onMounted(() => {
    timer = setInterval(() => {
        if (document.hidden) return;
        if (allOld && ++slowCount % 30 !== 0) return;
        tick.value++;
    }, 1000);
});
onBeforeUnmount(() => {
    clearInterval(timer);
    setPageKeyHandler(null);
});

// ---- windowing: hold ±pad rows around the viewport, re-request near an edge, anchor on the first visible row ----

let lastKey = '';
let lastKeyAt = 0;

function visibleRange(): { first: number; last: number } | null {
    const r = virtualizer.value.range;
    return r ? { first: r.startIndex, last: r.endIndex } : null;
}

function requestWindow(force = false) {
    const h = props.handle;
    const r = visibleRange();
    const total = h.total.value;
    const pad = windowPad();
    const first = r?.first ?? 0;
    const last = r?.last ?? 0;
    const limit = pad * 2 + Math.max(0, last - first + 1);
    const start = h.offset.value;
    const end = start + h.ids.length;
    const third = Math.floor(limit / 3);
    const outside = h.ids.length > 0 && (first < start || last >= end);
    const nearLow = start > 0 && first - start < third;
    const nearHigh = end < total && end - last < third;
    const empty = h.ids.length === 0 && total > 0;
    if (!force && !outside && !nearLow && !nearHigh && !empty) return;

    const desired = Math.max(0, Math.min(first - pad, Math.max(0, total - limit)));
    const k = first - start;
    const anchorId = k >= 0 && k < h.ids.length ? h.ids[k] : undefined;
    const window = anchorId ? { anchorId, offset: desired - first, limit } : { offset: desired, limit };
    const key = `${anchorId ?? 'abs'}:${window.offset}:${limit}`;
    const now = performance.now();
    if (key === lastKey && now - lastKeyAt < 300) return;
    lastKey = key;
    lastKeyAt = now;
    h.update({ window });
}

// Stable scroll: remember the first visible row's id and keep it where it was when inserts shift indexes.
const anchor = { id: 0, idx: 0 };

function trackAnchor() {
    const r = visibleRange();
    const h = props.handle;
    if (!r) return;
    const k = r.first - h.offset.value;
    if (k >= 0 && k < h.ids.length) {
        anchor.id = h.ids[k];
        anchor.idx = r.first;
    }
}

function holdAnchor() {
    const el = scroller.value;
    if (!el || !anchor.id || el.scrollTop <= 0) return;
    const h = props.handle;
    const k = h.ids.indexOf(anchor.id);
    if (k < 0) return;
    const newIdx = h.offset.value + k;
    const d = newIdx - anchor.idx;
    if (d !== 0) {
        anchor.idx = newIdx;
        el.scrollTop += d * rowH.value;
    }
}

watch(
    () => {
        const r = virtualizer.value.range;
        return r ? r.startIndex * 1e7 + r.endIndex : -1;
    },
    () => {
        trackAnchor();
        requestWindow();
    }
);

watch(
    () => props.handle.version.value,
    () => {
        holdAnchor();
        requestWindow();
        openPending();
    }
);

watch(
    () => props.handle.total.value,
    () => requestWindow()
);

watch(isPhone, () => {
    lastKey = '';
    requestWindow(true);
});

watch(
    () => pods.query,
    () => {
        anchor.id = 0;
        if (scroller.value) scroller.value.scrollTop = 0;
    },
    { flush: 'post' }
);

// ---- `:po ns/name` ----

function openPending() {
    const p = pods.pendingOpen;
    if (!p) return;
    for (const row of props.handle.rows.values()) {
        if (row[POD.name] === p.name && (!p.namespace || row[POD.namespace] === p.namespace)) {
            pods.pendingOpen = null;
            openRow(row[0]);
            return;
        }
    }
}

// ---- interaction ----

function openRow(id: number) {
    const row = props.handle.rows.get(id);
    if (!row) return;
    pods.focusId = id;
    ui.drawer = { id, name: row[POD.name] as string, cluster: row[POD.cluster] as string, namespace: row[POD.namespace] as string };
}

let longPressed = false;
let pressTimer: ReturnType<typeof setTimeout> | undefined;

function onPointerDown(e: PointerEvent) {
    if (!isPhone.value) return;
    const el = (e.target as HTMLElement).closest<HTMLElement>('[data-id]');
    if (!el) return;
    longPressed = false;
    pressTimer = setTimeout(() => {
        longPressed = true;
        pods.toggleSelected(Number(el.dataset.id));
    }, 500);
}
function cancelPress() {
    clearTimeout(pressTimer);
}

function onBodyClick(e: MouseEvent) {
    if (longPressed) {
        longPressed = false;
        return;
    }
    const t = e.target as HTMLElement;
    const el = t.closest<HTMLElement>('[data-id]');
    if (!el) return;
    const id = Number(el.dataset.id);
    if (!id) return;
    if (t instanceof HTMLInputElement) {
        pods.toggleSelected(id);
        return;
    }
    pods.focusId = id;
    if (isPhone.value || t.closest('[data-open]')) openRow(id);
}

function onDblClick(e: MouseEvent) {
    const id = Number((e.target as HTMLElement).closest<HTMLElement>('[data-id]')?.dataset.id);
    if (id) openRow(id);
}

function moveFocus(delta: number) {
    const h = props.handle;
    const ids = h.ids;
    if (!ids.length) return;
    let cur = pods.focusId ? ids.indexOf(pods.focusId) : -1;
    if (cur < 0) {
        const r = visibleRange();
        cur = Math.max(0, Math.min(ids.length - 1, (r?.first ?? 0) - h.offset.value));
    } else {
        cur = Math.max(0, Math.min(ids.length - 1, cur + delta));
    }
    pods.focusId = ids[cur];
    virtualizer.value.scrollToIndex(h.offset.value + cur, { align: 'auto' });
}

setPageKeyHandler(e => {
    switch (e.key) {
        case 'j':
        case 'ArrowDown':
            if (e.key === 'ArrowDown' && document.activeElement === scroller.value) return false;
            moveFocus(1);
            return true;
        case 'k':
        case 'ArrowUp':
            if (e.key === 'ArrowUp' && document.activeElement === scroller.value) return false;
            moveFocus(-1);
            return true;
        case 'x':
            if (pods.focusId) pods.toggleSelected(pods.focusId);
            return true;
        case 'Enter':
            if (pods.focusId && document.activeElement?.tagName !== 'BUTTON' && document.activeElement?.tagName !== 'A') {
                openRow(pods.focusId);
                return true;
            }
            return false;
    }
    return false;
});

// ---- header ----

const HEAD_ALL = [
    { label: 'Cluster', sort: 'cluster' },
    { label: 'Name', sort: 'name' },
    { label: 'Namespace', sort: 'namespace' },
    { label: 'Ready', sort: '' },
    { label: 'Status', sort: 'status' },
    { label: 'Restarts', sort: 'restarts' },
    { label: 'Node', sort: 'node' },
    { label: 'Age', sort: 'createdAt' }
];

const HEAD = computed(() => (showCluster.value ? HEAD_ALL : HEAD_ALL.filter(h => h.sort !== 'cluster')));

function sortInfo(col: string): { arrow: string; aria: 'ascending' | 'descending' | 'none' } {
    const i = pods.sort.findIndex(s => s.col === col);
    if (i < 0) return { arrow: '', aria: 'none' };
    const s = pods.sort[i];
    return { arrow: (s.desc ? '↓' : '↑') + (pods.sort.length > 1 ? i + 1 : ''), aria: s.desc ? 'descending' : 'ascending' };
}

const allSelected = computed(() => props.handle.ids.length > 0 && pods.selected.size >= props.handle.ids.length);
function toggleAll() {
    if (allSelected.value) pods.clearSelection();
    else pods.setSelected(props.handle.ids);
}

const n = (c: Cell) => c as number;
const s = (c: Cell) => c as string;
const toneClass = (row: Row) => 'tag ' + TONE_NAMES[n(row[POD.statusTone])];
const readyBad = (row: Row) => (n(row[POD.statusTone]) === 2 || n(row[POD.statusTone]) === 3) && n(row[POD.ready]) < n(row[POD.containers]);

defineExpose({ scroller });

// The cluster column/tag only earns its width when more than one cluster is connected.
const showCluster = computed(() => clusters.activeIds.length > 1);
const COLS_MULTI = '15px 112px minmax(0, 1.7fr) 128px 48px 168px 64px 148px 64px';
const COLS_SINGLE = '15px minmax(0, 1.7fr) 160px 56px 168px 72px minmax(120px, 1fr) 64px';

const emptyKind = computed(() => {
    if (!clusters.activeIds.length) return 'no-clusters';
    if (props.handle.total.value > 0) return '';
    if (props.handle.version.value === 0) return 'loading';
    if (pods.filtered) return 'filtered';
    if (clusters.states.some(st => st.phase === 'syncing' || st.phase === 'connecting')) return 'syncing';
    return 'none';
});

function clearFilters() {
    pods.text = '';
    pods.namespaces = [];
    pods.tones = [];
}
</script>

<template>
    <div
        ref="scroller"
        class="scroller"
        :class="{ phone: isPhone }"
        :style="{ '--row-h': rowH + 'px', '--cols': showCluster ? COLS_MULTI : COLS_SINGLE }"
        role="table"
        :aria-rowcount="handle.total.value + 1"
        aria-label="Pods"
        tabindex="0"
    >
        <div v-if="!isPhone" class="tr th" role="row">
            <span role="columnheader"
                ><input class="cb" type="checkbox" aria-label="Select all loaded rows" :checked="allSelected" @change="toggleAll"
            /></span>
            <span v-for="hd in HEAD" :key="hd.label" role="columnheader" :aria-sort="hd.sort ? sortInfo(hd.sort).aria : undefined" class="c">
                <button
                    v-if="hd.sort"
                    class="th-btn"
                    type="button"
                    :title="`Sort by ${SORT_LABELS[hd.sort]} (shift-click adds a secondary key)`"
                    @click="pods.sortBy(hd.sort, $event.shiftKey)"
                >
                    {{ hd.label }}<span aria-hidden="true">{{ sortInfo(hd.sort).arrow }}</span>
                </button>
                <template v-else>{{ hd.label }}</template>
            </span>
        </div>
        <div
            class="sizer"
            :style="{ height: totalPx + 'px' }"
            @click="onBodyClick"
            @dblclick="onDblClick"
            @pointerdown="onPointerDown"
            @pointerup="cancelPress"
            @pointercancel="cancelPress"
            @pointermove="cancelPress"
        >
            <template v-if="!isPhone">
                <div
                    v-for="it in slice"
                    :key="it.i"
                    v-memo="[it.id, it.ver, it.sel, it.foc, it.age, showCluster]"
                    class="tr row"
                    :class="{ sel: it.sel, focus: it.foc, sk: it.ver < 0 }"
                    :style="{ transform: 'translateY(' + it.i * ROW_H_DESKTOP + 'px)' }"
                    role="row"
                    :aria-rowindex="it.i + 2"
                    :aria-selected="it.sel"
                    :data-id="it.id"
                >
                    <template v-if="it.ver >= 0">
                        <span role="cell"><input class="cb" type="checkbox" tabindex="-1" aria-label="Select row" :checked="it.sel" /></span>
                        <span v-if="showCluster" class="c" role="cell"
                            ><span class="tag"
                                ><span class="dot" :style="{ background: clusterColor(s(it.row[POD.cluster])) }"></span
                                >{{ it.row[POD.cluster] }}</span
                            ></span
                        >
                        <span class="c name" role="cell" data-open>{{ it.row[POD.name] }}</span>
                        <span class="c muted" role="cell">{{ it.row[POD.namespace] }}</span>
                        <span class="c" :class="readyBad(it.row) ? 'bad' : 'dim'" role="cell"
                            >{{ it.row[POD.ready] }}/{{ it.row[POD.containers] }}</span
                        >
                        <span class="c" role="cell"><span :class="toneClass(it.row)">{{ it.row[POD.status] }}</span></span>
                        <span class="c" :class="n(it.row[POD.restarts]) > 5 ? 'bad' : 'dim'" role="cell">{{ it.row[POD.restarts] }}</span>
                        <span class="c mono muted" role="cell">{{ it.row[POD.node] || '—' }}</span>
                        <span class="c muted" role="cell">{{ it.age }}</span>
                    </template>
                    <template v-else>
                        <span></span>
                        <span v-if="showCluster" class="c"><i style="width: 70%"></i></span>
                        <span class="c"><i style="width: 80%"></i></span>
                        <span class="c"><i style="width: 50%"></i></span>
                        <span class="c"><i style="width: 60%"></i></span>
                        <span class="c"><i style="width: 70%"></i></span>
                        <span class="c"><i style="width: 40%"></i></span>
                        <span class="c"><i style="width: 70%"></i></span>
                        <span class="c"><i style="width: 60%"></i></span>
                    </template>
                </div>
            </template>
            <template v-else>
                <div
                    v-for="it in slice"
                    :key="it.i"
                    v-memo="[it.id, it.ver, it.sel, it.foc, it.age, showCluster]"
                    class="pcard"
                    :class="{ sel: it.sel, focus: it.foc, sk: it.ver < 0 }"
                    :style="{ transform: 'translateY(' + it.i * ROW_H_PHONE + 'px)' }"
                    role="row"
                    :aria-rowindex="it.i + 2"
                    :aria-selected="it.sel"
                    :data-id="it.id"
                >
                    <div v-if="it.ver >= 0" class="pc-main" role="cell">
                        <div class="pc-top">
                            <span class="c name">{{ it.row[POD.name] }}</span>
                            <span :class="toneClass(it.row)">{{ it.row[POD.status] }}</span>
                        </div>
                        <div class="sub">
                            <template v-if="showCluster"
                                ><span class="dot" :style="{ background: clusterColor(s(it.row[POD.cluster])) }"></span>{{ it.row[POD.cluster]
                                }}<span>·</span></template
                            >{{ it.row[POD.namespace] }}<span>·</span><span class="mono c">{{ it.row[POD.node] || '—' }}</span>
                        </div>
                        <div class="pc-meta">
                            <span>ready {{ it.row[POD.ready] }}/{{ it.row[POD.containers] }}</span><span>restarts {{ it.row[POD.restarts] }}</span
                            ><span>{{ it.age }}</span>
                        </div>
                    </div>
                    <div v-else class="pc-main"><i style="width: 70%"></i><i style="width: 45%"></i><i style="width: 55%"></i></div>
                </div>
            </template>
        </div>
        <div v-if="emptyKind" class="empty" role="status">
            <template v-if="emptyKind === 'no-clusters'">
                <p>No clusters connected.</p>
                <button class="btn" type="button" @click="clusters.open()">Choose clusters</button>
            </template>
            <template v-else-if="emptyKind === 'loading'"><p class="muted">Loading pods…</p></template>
            <template v-else-if="emptyKind === 'filtered'">
                <p>No pods match the current filters.</p>
                <button class="btn sm" type="button" @click="clearFilters">Clear filters</button>
            </template>
            <template v-else-if="emptyKind === 'syncing'"><p class="muted">Clusters are still syncing; rows stream in as they arrive.</p></template>
            <template v-else><p class="muted">No pods.</p></template>
        </div>
    </div>
</template>

<style scoped>
.bad {
    color: var(--danger);
}
.dim {
    color: var(--text2);
}
.empty {
    position: sticky;
    left: 0;
    top: 0;
    padding: 36px 16px;
    text-align: center;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 8px;
}
.empty p {
    margin: 0;
}
.pc-main {
    min-width: 0;
    flex-grow: 1;
    display: flex;
    flex-direction: column;
    gap: 4px;
}
.pc-top {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
    font-size: 14px;
}
.pc-top .name {
    flex-grow: 1;
}
.pc-meta {
    display: flex;
    gap: 12px;
    color: var(--text2);
}
.pcard .mono {
    font-size: 11px;
}
.pcard.sk i {
    display: block;
    height: 12px;
    border-radius: 6px;
    background: var(--s3);
}
</style>
