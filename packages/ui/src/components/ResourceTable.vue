<script setup lang="ts">
import type { ObjectRef, Row, ViewHandle } from '@v8s/client';

import { useVirtualizer } from '@tanstack/vue-virtual';
import { TONE_NAMES } from '@v8s/client';
import { computed, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue';
import { useRouter } from 'vue-router';

import type { ColPlan, TablePlan } from '@/lib/columns.ts';
import type { DrawerTab } from '@/lib/route.ts';
import type { CellContent, TableDecor } from '@/lib/table-decor.ts';

import { clusterColor } from '@/lib/colors.ts';
import { gridTemplate, minWidthOf } from '@/lib/columns.ts';
import { editRequest, openObject } from '@/lib/drawer.ts';
import { formatAge, OLD_AGE_MS } from '@/lib/format.ts';
import { setPageKeyHandler } from '@/lib/keys.ts';
import { parseDrawer, tabsFor } from '@/lib/route.ts';
import { isPhone, ROW_H_DESKTOP, ROW_H_PHONE, windowPad } from '@/lib/viewport.ts';
import { useClustersStore } from '@/stores/clusters.ts';
import { isFiltered, selectionKey, useTableStore } from '@/stores/table.ts';

const props = defineProps<{ handle: ViewHandle; gvr: string; plan: TablePlan; title: string; namespaced: boolean; decor?: TableDecor | null }>();
const emit = defineEmits<{ resize: [key: string, width: number] }>();

const table = useTableStore();
const clusters = useClustersStore();
const router = useRouter();
const ui = computed(() => table.of(props.gvr));

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

// ---- row refs (selection and actions are by id; the ref is a small snapshot taken at the moment of selection) ----

function refOf(row: Row): ObjectRef {
    const p = props.plan;
    return {
        cluster: p.clusterIdx >= 0 ? (row[p.clusterIdx] as string) : (clusters.activeIds[0] ?? ''),
        gvr: props.gvr,
        namespace: p.nsIdx >= 0 ? (row[p.nsIdx] as string) || undefined : undefined,
        name: row[p.nameIdx] as string
    };
}

// ---- visible slice: one v-for, plain elements, memoised on id + version + selection + focus + age text ----

interface Item {
    i: number;
    id: number;
    row: Row;
    ver: number;
    sel: boolean;
    foc: boolean;
    age: string;
    /** Decorated cell content per plan column (null = default rendering); only when the kind has a decor. */
    dc: (CellContent | null)[] | null;
    /** Decor memo key for state outside the row. */
    x: number | string;
}

const tick = shallowRef(0);
let allOld = false;
/** Captured when the slice is computed; age cells render from it so they only change together with the memo key. */
let nowMs = Date.now();

const slice = computed<Item[]>(() => {
    void tick.value;
    const decor = props.decor;
    if (decor?.version) void decor.version.value;
    const h = props.handle;
    void h.version.value;
    const off = h.offset.value;
    const ids = h.ids;
    const rows = h.rows;
    const sel = ui.value.selected;
    const foc = ui.value.focusId;
    const p = props.plan;
    nowMs = Date.now();
    let old = true;
    const out: Item[] = [];
    for (const vi of virtualizer.value.getVirtualItems()) {
        const i = vi.index;
        const k = i - off;
        const id = k >= 0 && k < ids.length ? ids[k] : 0;
        const row = id ? rows.get(id) : undefined;
        let age = '';
        if (row && p.hasTime) {
            for (const c of p.cols) {
                if (c.kind !== 'age') continue;
                const t = row[c.idx] as number;
                age += formatAge(t, nowMs) + '|';
                if (nowMs - t < OLD_AGE_MS) old = false;
            }
        }
        let dc: (CellContent | null)[] | null = null;
        let x: number | string = 0;
        if (decor && row) {
            if (decor.cell) dc = p.cols.map(c => decor.cell!(c, row));
            if (decor.rowKey) x = decor.rowKey(row);
        }
        out.push({
            i,
            id,
            row: row ?? EMPTY_ROW,
            ver: row ? row[1] : -1,
            sel: !!row && sel.has(selectionKey(refOf(row))),
            foc: id !== 0 && id === foc,
            age,
            dc,
            x
        });
    }
    allOld = old;
    return out;
});

// Lazy per-row detail (Helm chart versions): tell the decor which loaded rows are on screen, once scrolling settles.
let visibleTimer: ReturnType<typeof setTimeout> | undefined;
watch(
    () => [props.handle.version.value, virtualizer.value.range?.startIndex, virtualizer.value.range?.endIndex],
    () => {
        const d = props.decor;
        if (!d?.visible) return;
        clearTimeout(visibleTimer);
        visibleTimer = setTimeout(() => {
            const rows: Row[] = [];
            for (const it of slice.value) if (it.ver >= 0) rows.push(it.row);
            d.visible!(rows);
        }, 200);
    }
);
onBeforeUnmount(() => clearTimeout(visibleTimer));

// One shared 1 s tick (30 s when every visible age is over an hour); only the age text in the memo key changes.
let timer: ReturnType<typeof setInterval> | undefined;
let slowCount = 0;
onMounted(() => {
    timer = setInterval(() => {
        if (document.hidden || !props.plan.hasTime) return;
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
/** Range displayed when the anchor was last tracked (absolute row indexes). */
let lastRange: { first: number; last: number } | null = null;

function trackAnchor() {
    const r = visibleRange();
    const h = props.handle;
    if (!r) return;
    lastRange = { first: r.first, last: r.last };
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
    const newIdx = k < 0 ? -1 : h.offset.value + k;
    const prev = lastRange;
    // Shift only when the anchor is still in the window and landed inside the range displayed before the update.
    // Otherwise (re-sorted far away, or vanished) keep the scroll position and re-anchor on the first visible row.
    if (newIdx >= 0 && prev && newIdx >= prev.first && newIdx <= prev.last) {
        const d = newIdx - anchor.idx;
        if (d !== 0) {
            anchor.idx = newIdx;
            el.scrollTop += d * rowH.value;
        }
    } else {
        anchor.id = 0;
        trackAnchor();
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
    () => [ui.value.text, ui.value.namespaces, ui.value.tones, ui.value.sort],
    () => {
        anchor.id = 0;
        if (scroller.value) scroller.value.scrollTop = 0;
    },
    { flush: 'post' }
);

// ---- `:po ns/name` ----

function openPending() {
    const p = ui.value.pendingOpen;
    const pl = props.plan;
    if (!p || pl.nameIdx < 0) return;
    for (const row of props.handle.rows.values()) {
        if (row[pl.nameIdx] === p.name && (!p.namespace || pl.nsIdx < 0 || row[pl.nsIdx] === p.namespace)) {
            ui.value.pendingOpen = null;
            openRow(row[0]);
            return;
        }
    }
}

// ---- interaction ----

function openRow(id: number, tab?: DrawerTab) {
    const row = props.handle.rows.get(id);
    if (!row) return;
    ui.value.focusId = id;
    void openObject(router, refOf(row), tab);
}

let longPressed = false;
let pressTimer: ReturnType<typeof setTimeout> | undefined;

function toggle(id: number) {
    const row = props.handle.rows.get(id);
    if (row) table.select(props.gvr, refOf(row));
}

function onPointerDown(e: PointerEvent) {
    if (!isPhone.value) return;
    const el = (e.target as HTMLElement).closest<HTMLElement>('[data-id]');
    if (!el) return;
    longPressed = false;
    pressTimer = setTimeout(() => {
        longPressed = true;
        toggle(Number(el.dataset.id));
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
        toggle(id);
        return;
    }
    ui.value.focusId = id;
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
    let cur = ui.value.focusId ? ids.indexOf(ui.value.focusId) : -1;
    if (cur < 0) {
        const r = visibleRange();
        cur = Math.max(0, Math.min(ids.length - 1, (r?.first ?? 0) - h.offset.value));
    } else {
        cur = Math.max(0, Math.min(ids.length - 1, cur + delta));
    }
    ui.value.focusId = ids[cur];
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
            if (ui.value.focusId) toggle(ui.value.focusId);
            return true;
        case 'l':
        case 's': {
            // `l` opens the Logs tab, `s` the Shell tab of the focused row, where the kind has one.
            const tab: DrawerTab = e.key === 'l' ? 'logs' : 'shell';
            if (!ui.value.focusId || !tabsFor(props.gvr).includes(tab)) return false;
            openRow(ui.value.focusId, tab);
            return true;
        }
        case 'e':
        case 'd': {
            // `e` edits the YAML and `d` describes (the Overview tab) of the focused row, else of the object open in the drawer.
            const tab: DrawerTab = e.key === 'e' ? 'yaml' : 'overview';
            if (!tabsFor(props.gvr).includes(tab)) return false;
            if (e.key === 'e') editRequest.value = true;
            if (ui.value.focusId && props.handle.rows.has(ui.value.focusId)) {
                openRow(ui.value.focusId, tab);
                return true;
            }
            const open = parseDrawer(props.gvr, router.currentRoute.value.query);
            if (open) {
                void openObject(router, open.ref, tab, true);
                return true;
            }
            editRequest.value = false;
            return false;
        }
        case 'Enter':
            if (ui.value.focusId && document.activeElement?.tagName !== 'BUTTON' && document.activeElement?.tagName !== 'A') {
                openRow(ui.value.focusId);
                return true;
            }
            return false;
    }
    return false;
});

// ---- header ----

function sortInfo(col: string): { arrow: string; aria: 'ascending' | 'descending' | 'none' } {
    const sort = ui.value.sort;
    const i = sort.findIndex(s => s.col === col);
    if (i < 0) return { arrow: '', aria: 'none' };
    const s = sort[i];
    return { arrow: (s.desc ? '↓' : '↑') + (sort.length > 1 ? i + 1 : ''), aria: s.desc ? 'descending' : 'ascending' };
}

const allSelected = computed(() => props.handle.ids.length > 0 && ui.value.selected.size >= props.handle.ids.length);
function toggleAll() {
    if (allSelected.value) {
        table.clearSelection(props.gvr);
        return;
    }
    const refs: ObjectRef[] = [];
    for (const id of props.handle.ids) {
        const row = props.handle.rows.get(id);
        if (row) refs.push(refOf(row));
    }
    table.setSelected(props.gvr, refs);
}

// ---- column resize: one CSS variable on the scroller changes while dragging; the layout is saved once on release ----

function startResize(e: PointerEvent, c: ColPlan) {
    const el = scroller.value;
    const cell = (e.currentTarget as HTMLElement).parentElement;
    if (!el || !cell) return;
    e.preventDefault();
    e.stopPropagation();
    const startX = e.clientX;
    const startW = cell.getBoundingClientRect().width;
    let width = startW;
    const move = (ev: PointerEvent) => {
        width = Math.max(48, Math.round(startW + ev.clientX - startX));
        el.style.setProperty('--cols', gridTemplate(props.plan.cols, { key: c.key, width }));
        el.style.setProperty('--minw', Math.max(minWidthOf(props.plan.cols), minWidthOf(props.plan.cols) - c.width + width) + 'px');
    };
    const up = () => {
        window.removeEventListener('pointermove', move);
        window.removeEventListener('pointerup', up);
        if (width !== startW) emit('resize', c.key, width);
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
}

function onResizeKey(e: KeyboardEvent, c: ColPlan) {
    if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
    e.preventDefault();
    const cell = (e.currentTarget as HTMLElement).parentElement;
    const w = Math.round(cell?.getBoundingClientRect().width ?? c.width);
    emit('resize', c.key, Math.max(48, w + (e.key === 'ArrowRight' ? 16 : -16)));
}

const s = (c: unknown) => c as string;
const n = (c: unknown) => c as number;
const toneName = (row: Row) => (props.plan.toneIdx >= 0 ? TONE_NAMES[n(row[props.plan.toneIdx])] : 'neutral') ?? 'neutral';
const containersIdx = computed(() => {
    const i = (props.handle.columns.value ?? []).findIndex(c => c.key === 'containers');
    return i < 0 ? -1 : i + 2;
});
const readyBad = (row: Row, idx: number) => {
    const t = n(row[props.plan.toneIdx]);
    return (t === 2 || t === 3) && containersIdx.value >= 0 && n(row[idx]) < n(row[containersIdx.value]);
};

function cellClass(c: ColPlan, row: Row, dc?: CellContent | null) {
    return {
        name: c.kind === 'name',
        mono: dc?.mono ?? c.kind === 'mono',
        r: c.align === 'right',
        muted: dc?.muted ?? (c.kind === 'age' || c.key === 'namespace'),
        bad: c.idx >= 0 && ((c.key === 'restarts' && n(row[c.idx]) > 5) || (c.kind === 'ready' && readyBad(row, c.idx)))
    };
}

defineExpose({ scroller });

// The cluster column/tag only earns its width when more than one cluster is connected.
const showCluster = computed(() => props.plan.clusterIdx >= 0 && props.plan.cols.some(c => c.kind === 'cluster'));
const planKey = computed(() => props.plan.grid + props.plan.cols.length);

const emptyKind = computed(() => {
    if (!clusters.activeIds.length) return 'no-clusters';
    if (props.handle.total.value > 0) return '';
    if (props.handle.version.value === 0) return 'loading';
    if (isFiltered(ui.value)) return 'filtered';
    if (clusters.states.some(st => st.phase === 'syncing' || st.phase === 'connecting')) return 'syncing';
    return 'none';
});

/** Phone card: the first few text/int columns beyond the identity and status columns, as "label value". */
const cardMetrics = computed(() =>
    props.plan.cols.filter(c => ['text', 'mono', 'int', 'ready'].includes(c.kind) && c.key !== 'namespace').slice(0, 2)
);
const statusCol = computed(() => props.plan.cols.find(c => c.kind === 'status' || c.kind === 'tone'));
const ageCol = computed(() => props.plan.cols.find(c => c.kind === 'age'));

function metricText(c: ColPlan, row: Row): string {
    if (c.kind === 'ready') return `${row[c.idx]}/${containersIdx.value >= 0 ? row[containersIdx.value] : ''}`;
    return String(row[c.idx] ?? '');
}
</script>

<template>
    <div
        ref="scroller"
        class="scroller"
        :class="{ phone: isPhone }"
        :style="{ '--row-h': rowH + 'px', '--cols': plan.grid, '--minw': plan.minWidth + 'px' }"
        role="table"
        :aria-rowcount="handle.total.value + 1"
        :aria-label="title"
        tabindex="0"
    >
        <div v-if="!isPhone" :key="planKey" class="tr th" role="row">
            <span role="columnheader"
                ><input class="cb" type="checkbox" aria-label="Select all loaded rows" :checked="allSelected" @change="toggleAll"
            /></span>
            <span
                v-for="c in plan.cols"
                :key="c.key"
                role="columnheader"
                :aria-sort="c.sortable ? sortInfo(c.key).aria : undefined"
                class="c hd"
                :class="{ r: c.align === 'right' }"
            >
                <button
                    v-if="c.sortable"
                    class="th-btn"
                    type="button"
                    :title="`Sort by ${c.label.toLowerCase()} (shift-click adds a secondary key)`"
                    @click="table.sortBy(gvr, c.key, $event.shiftKey)"
                >
                    {{ c.label }}<span aria-hidden="true">{{ sortInfo(c.key).arrow }}</span>
                </button>
                <template v-else>{{ c.label }}</template>
                <span
                    class="rz"
                    role="separator"
                    tabindex="0"
                    :aria-label="`Resize ${c.label} column`"
                    aria-orientation="vertical"
                    @pointerdown="startResize($event, c)"
                    @keydown="onResizeKey($event, c)"
                ></span>
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
                    v-memo="[it.id, it.ver, it.sel, it.foc, it.age, it.x, planKey]"
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
                        <span
                            v-for="(c, ci) in plan.cols"
                            :key="c.key"
                            class="c"
                            :class="cellClass(c, it.row, it.dc?.[ci])"
                            role="cell"
                            :data-open="c.kind === 'name' ? '' : undefined"
                        >
                            <template v-if="it.dc?.[ci]"
                                ><span v-if="it.dc[ci]!.tag" class="tag" :class="it.dc[ci]!.tag!.tone" :title="it.dc[ci]!.title">{{
                                    it.dc[ci]!.tag!.text
                                }}</span
                                ><span v-if="it.dc[ci]!.text" :title="it.dc[ci]!.title">{{ it.dc[ci]!.text }}</span
                                ><template v-if="!it.dc[ci]!.tag && !it.dc[ci]!.text">—</template></template
                            >
                            <template v-else-if="c.kind === 'cluster'"
                                ><span class="tag"
                                    ><span class="dot" :style="{ background: clusterColor(s(it.row[c.idx])) }"></span>{{ it.row[c.idx] }}</span
                                ></template
                            >
                            <template v-else-if="c.kind === 'status' || c.kind === 'tone'"
                                ><span
                                    v-if="it.row[c.idx] !== '' && !(c.kind === 'tone' && n(it.row[c.idx]) === 0)"
                                    class="tag"
                                    :class="toneName(it.row)"
                                    >{{ c.kind === 'tone' ? toneName(it.row) : it.row[c.idx] }}</span
                                ><template v-else>—</template></template
                            >
                            <template v-else-if="c.kind === 'ready'"
                                >{{ it.row[c.idx] }}/{{ containersIdx >= 0 ? it.row[containersIdx] : '' }}</template
                            >
                            <template v-else-if="c.kind === 'age'">{{ formatAge(n(it.row[c.idx]), nowMs) }}</template>
                            <template v-else>{{
                                it.row[c.idx] === '' || it.row[c.idx] == null ? (c.kind === 'name' ? '' : '—') : it.row[c.idx]
                            }}</template>
                        </span>
                    </template>
                    <template v-else>
                        <span></span>
                        <span v-for="c in plan.cols" :key="c.key" class="c"><i :style="{ width: 40 + ((c.idx * 17) % 45) + '%' }"></i></span>
                    </template>
                </div>
            </template>
            <template v-else>
                <div
                    v-for="it in slice"
                    :key="it.i"
                    v-memo="[it.id, it.ver, it.sel, it.foc, it.age, it.x, planKey]"
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
                            <span class="c name">{{ it.row[plan.nameIdx] }}</span>
                            <span v-if="statusCol && it.row[statusCol.idx] !== ''" class="tag" :class="toneName(it.row)">{{
                                statusCol.kind === 'tone' ? toneName(it.row) : it.row[statusCol.idx]
                            }}</span>
                        </div>
                        <div class="sub">
                            <template v-if="showCluster"
                                ><span class="dot" :style="{ background: clusterColor(s(it.row[plan.clusterIdx])) }"></span
                                >{{ it.row[plan.clusterIdx] }}<span>·</span></template
                            ><template v-if="plan.nsIdx >= 0">{{ it.row[plan.nsIdx] }}<span>·</span></template
                            ><template v-if="ageCol">{{ formatAge(n(it.row[ageCol.idx]), nowMs) }}</template>
                        </div>
                        <div class="pc-meta">
                            <span v-for="c in cardMetrics" :key="c.key" class="c">{{ c.label.toLowerCase() }} {{ metricText(c, it.row) }}</span>
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
            <template v-else-if="emptyKind === 'loading'"><p class="muted">Loading {{ title.toLowerCase() }}…</p></template>
            <template v-else-if="emptyKind === 'filtered'">
                <p>No {{ title.toLowerCase() }} match the current filters.</p>
                <button class="btn sm" type="button" @click="table.clearFilters(gvr)">Clear filters</button>
            </template>
            <template v-else-if="emptyKind === 'syncing'"><p class="muted">Clusters are still syncing; rows stream in as they arrive.</p></template>
            <template v-else><p class="muted">No {{ title.toLowerCase() }}.</p></template>
        </div>
    </div>
</template>

<style scoped>
.bad {
    color: var(--danger);
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
    min-width: 0;
    overflow: hidden;
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
