<script setup lang="ts">
import type { ProblemsEvidenceResult, Row } from '@v8s/client';

import { useVirtualizer } from '@tanstack/vue-virtual';
import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue';
import { useRouter } from 'vue-router';

import type { AggGroup, AggItem } from '@/lib/problems-aggregate.ts';
import type { Layout } from '@/lib/problems-groups.ts';
import type { ProblemsView } from '@/lib/problems-view.ts';
import type { EvState, ProblemRow, SnoozePreset } from '@/lib/problems.ts';

import ProblemsDetailSheet from '@/components/ProblemsDetailSheet.vue';
import ProblemsEvidence from '@/components/ProblemsEvidence.vue';
import ProtectedMark from '@/components/ProtectedMark.vue';
import { getAgent, kinds } from '@/lib/agent.ts';
import { clusterColor } from '@/lib/colors.ts';
import { openObject } from '@/lib/drawer.ts';
import { formatAge, OLD_AGE_MS } from '@/lib/format.ts';
import { setListNav, setPageKeyHandler } from '@/lib/keys.ts';
import { setAcked, subjectRef } from '@/lib/problems-actions.ts';
import { aggFlatIndexOfView, aggregateRows, buildAggFlat, groupClusters } from '@/lib/problems-aggregate.ts';
import { buildLayout, flatIndexOfView, itemAt, pixelOffset, viewRangeOfFlat } from '@/lib/problems-groups.ts';
import { aggregateKey, isRollup, makeRefresher, NO_PROBLEM_ID, readRow, severityTone, SNOOZE_PRESETS } from '@/lib/problems.ts';
import { isPhone, windowPad } from '@/lib/viewport.ts';
import { useClustersStore } from '@/stores/clusters.ts';
import { useProblemsStore } from '@/stores/problems.ts';
import { useTableStore } from '@/stores/table.ts';
import { useUiStore } from '@/stores/ui.ts';

const props = defineProps<{ view: ProblemsView }>();

const h = props.view.handle;
const store = useProblemsStore();
const clusters = useClustersStore();
const table = useTableStore();
const ui = useUiStore();
const router = useRouter();

const HEADER_H = 32;
const EVID_H = 232;
const scroller = ref<HTMLDivElement | null>(null);
const rowH = computed(() => (isPhone.value ? 72 : 32));
const headH = computed(() => (isPhone.value ? 0 : 32));

// ---- interaction state (small; never rows) ----
const expandedId = shallowRef(0); // wire id of the row whose evidence is open (desktop)
const sheetId = shallowRef(0); // wire id of the row shown in the full-screen sheet (phone)
const focusId = shallowRef(0);
const aggOpen = shallowRef<ReadonlySet<string>>(new Set());
const pending = shallowRef<ReadonlyMap<number, 'ack' | 'unack'>>(new Map());
const ackMenu = shallowRef<{ id: number; x: number; y: number } | null>(null);
const evState = shallowRef<EvState | null>(null);

// ---- row access (non-reactive reads after touching version) ----
function rowAtView(vi: number): Row | undefined {
    const k = vi - h.offset.value;
    const id = k >= 0 && k < h.ids.length ? h.ids[k] : 0;
    return id ? h.rows.get(id) : undefined;
}
function idAtView(vi: number): number {
    const k = vi - h.offset.value;
    return k >= 0 && k < h.ids.length ? h.ids[k] : 0;
}
function viewIndexOfId(id: number): number {
    if (!id) return -1;
    const k = h.ids.indexOf(id);
    return k < 0 ? -1 : h.offset.value + k;
}

// ---- the flat model: grouped layout, or the aggregated list ----
interface Model {
    count: number;
    layout: Layout | null;
    groups: AggGroup[];
    flat: AggItem[];
}

const model = computed<Model>(() => {
    void h.version.value;
    const cp = props.view.cols.value;
    const evVi = viewIndexOfId(expandedId.value);
    if (props.view.aggregateActive.value) {
        const total = h.total.value;
        const groups = aggregateRows(total, i => {
            const r = rowAtView(i);
            return r
                ? aggregateKey({
                      class: String(r[cp.class]),
                      kind: String(r[cp.kind]),
                      namespace: String(r[cp.namespace] ?? ''),
                      name: String(r[cp.name])
                  })
                : undefined;
        });
        const flat = buildAggFlat(groups, aggOpen.value, evVi >= 0 ? evVi : null);
        return { count: flat.length, layout: null, groups, flat };
    }
    const layout = buildLayout(props.view.groupSpecs.value, { headers: props.view.group.value !== 'none', expanded: evVi >= 0 ? evVi : null });
    return { count: layout.count, layout, groups: [], flat: [] };
});

type Kind = 'header' | 'row' | 'child' | 'evidence' | 'none';
interface Meta {
    kind: Kind;
    vi: number;
    group: number;
    key: string;
    count: number;
    collapsed: boolean;
}
function describe(m: Model, i: number): Meta {
    if (m.layout) {
        const it = itemAt(m.layout, i);
        if (!it) return { kind: 'none', vi: -1, group: -1, key: '', count: 0, collapsed: false };
        if (it.kind === 'header') return { kind: 'header', vi: -1, group: it.group, key: it.key, count: it.count, collapsed: it.collapsed };
        return { kind: it.kind, vi: it.viewIndex, group: it.group, key: '', count: 0, collapsed: false };
    }
    const it = m.flat[i];
    if (!it) return { kind: 'none', vi: -1, group: -1, key: '', count: 0, collapsed: false };
    return { kind: it.kind, vi: it.vi, group: it.g, key: m.groups[it.g]?.key ?? '', count: m.groups[it.g]?.n ?? 1, collapsed: false };
}
function sizeOf(kind: Kind): number {
    return kind === 'header' ? HEADER_H : kind === 'evidence' ? EVID_H : rowH.value;
}

const virtualizer = useVirtualizer<HTMLDivElement, Element>(
    computed(() => {
        const m = model.value;
        return {
            count: m.count,
            getScrollElement: () => scroller.value,
            estimateSize: (i: number) => sizeOf(describe(m, i).kind),
            overscan: isPhone.value ? 6 : 10,
            scrollMargin: headH.value,
            // a row scrolled to the start stays below the sticky header
            scrollPaddingStart: headH.value
        };
    })
);
// Sizes depend on the layout (headers, collapse, the evidence item), not only on the count.
watch(
    () => [model.value, isPhone.value],
    () => virtualizer.value.measure(),
    { flush: 'sync' }
);

const totalPx = computed(() => virtualizer.value.getTotalSize());

// ---- visible slice: one v-for over plain data, memoised ----
interface Item {
    i: number;
    start: number;
    size: number;
    kind: Kind;
    id: number;
    vi: number;
    r: ProblemRow | null;
    sk: boolean;
    mk: string;
    foc: boolean;
    exp: boolean;
    fade: boolean;
    age: string;
    key: string;
    count: number;
    collapsed: boolean;
    rollup: boolean;
    aggN: number;
    aggOpen: boolean;
    clusters: string[];
    label: string;
}

const tick = shallowRef(0);
let allOld = false;
let nowMs = Date.now();
const nameOf = (c: string) => clusters.nameById.get(c) ?? c;

const groupLabel = (key: string): string => {
    const g = props.view.group.value;
    if (g === 'cluster') return nameOf(key);
    if (g === 'namespace') return key || '(cluster-scoped)';
    return key;
};

/** The focused row is rendered: the list drops its own focus ring (app.scss `.scroller.rowfocus`). */
const focusInView = computed(() => slice.value.some(it => it.foc));

const slice = computed<Item[]>(() => {
    void tick.value;
    void h.version.value;
    void pending.value;
    const m = model.value;
    const cp = props.view.cols.value;
    nowMs = Date.now();
    let old = true;
    const out: Item[] = [];
    for (const v of virtualizer.value.getVirtualItems()) {
        const meta = describe(m, v.index);
        const it: Item = {
            i: v.index,
            start: v.start - headH.value,
            size: v.size,
            kind: meta.kind,
            id: 0,
            vi: meta.vi,
            r: null,
            sk: false,
            mk: '',
            foc: false,
            exp: false,
            fade: false,
            age: '',
            key: meta.key,
            count: meta.count,
            collapsed: meta.collapsed,
            rollup: false,
            aggN: 1,
            aggOpen: false,
            clusters: [],
            label: ''
        };
        if (meta.kind === 'header') {
            it.label = groupLabel(meta.key);
            it.mk = `h|${meta.key}|${meta.count}|${meta.collapsed}|${props.view.group.value}`;
        } else if (meta.kind === 'evidence') {
            it.id = idAtView(meta.vi);
            it.mk = 'e';
        } else if (meta.kind === 'row' || meta.kind === 'child') {
            const id = idAtView(meta.vi);
            const row = id ? h.rows.get(id) : undefined;
            it.id = id;
            if (row) {
                const r = readRow(cp, row);
                it.r = r;
                it.age = formatAge(r.since, nowMs);
                if (r.since && nowMs - r.since < OLD_AGE_MS) old = false;
                it.rollup = isRollup(r);
                const want = pending.value.get(id);
                it.fade = want === 'ack' || (r.acked && want !== 'unack');
                it.foc = id === focusId.value;
                it.exp = id === expandedId.value;
                if (m.layout === null && meta.kind === 'row') {
                    const g = m.groups[meta.group];
                    it.aggN = g.n;
                    it.aggOpen = g.n > 1 && aggOpen.value.has(g.key);
                    if (g.n > 1) it.clusters = groupClusters(g, i => rowAtView(i)?.[cp.cluster] as string | undefined);
                    else it.clusters = [r.cluster];
                } else it.clusters = [r.cluster];
                if (it.aggN > 1 && it.kind === 'row') {
                    it.exp = false;
                    if (it.aggOpen) it.foc = false;
                }
                it.mk = `${id}|${row[1]}|${it.foc}|${it.exp}|${it.fade}|${it.age}|${it.aggN}|${it.aggOpen}|${it.clusters.length}|${pending.value.get(id) ?? ''}`;
            } else {
                it.sk = true;
                it.mk = `s${v.index}`;
            }
        }
        out.push(it);
    }
    allOld = old;
    return out;
});

let slowCount = 0;
let timer: ReturnType<typeof setInterval> | undefined;

// ---- scroll anchor: rows re-sort live (lastChanged), so keep the open row, or else the first visible row, where it was ----
const anchor = { id: 0, top: 0 };
function pxOfFlat(m: Model, f: number): number {
    if (m.layout) return pixelOffset(m.layout, f, { header: HEADER_H, row: rowH.value, evidence: EVID_H });
    let px = 0;
    for (let i = 0; i < f && i < m.flat.length; i++) px += m.flat[i].kind === 'evidence' ? EVID_H : rowH.value;
    return px;
}
function trackAnchor() {
    const el = scroller.value;
    const r = virtualizer.value.range;
    if (!el || !r) return;
    const m = model.value;
    let f = -1;
    let id = 0;
    if (expandedId.value) {
        const ef = flatOfId(expandedId.value);
        if (ef >= r.startIndex && ef <= r.endIndex) {
            f = ef;
            id = expandedId.value;
        }
    }
    for (let j = r.startIndex; f < 0 && j <= r.endIndex && j < m.count; j++) {
        const meta = describe(m, j);
        const rid = meta.kind === 'row' || meta.kind === 'child' ? idAtView(meta.vi) : 0;
        if (rid) {
            f = j;
            id = rid;
        }
    }
    if (f < 0) {
        anchor.id = 0;
        return;
    }
    anchor.id = id;
    anchor.top = pxOfFlat(m, f) - el.scrollTop;
}
function holdAnchor() {
    const el = scroller.value;
    if (!el || !anchor.id) return;
    // At the very top new rows simply appear above; an open row is always held.
    if (el.scrollTop <= 0 && anchor.id !== expandedId.value) return;
    const f = flatOfId(anchor.id);
    if (f < 0) {
        anchor.id = 0;
        return;
    }
    const d = pxOfFlat(model.value, f) - el.scrollTop - anchor.top;
    if (Math.abs(d) >= 1) el.scrollTop += d;
}
watch(model, holdAnchor, { flush: 'post' });
watch(expandedId, () => void nextTick(trackAnchor));

// ---- windowing ----
let lastKey = '';
let lastKeyAt = 0;
function requestWindow(force = false) {
    if (props.view.aggregateActive.value) return;
    const m = model.value;
    const r = virtualizer.value.range;
    if (!m.layout || !r) return;
    const vr = viewRangeOfFlat(m.layout, r.startIndex, r.endIndex);
    if (!vr) return;
    const total = h.total.value;
    const pad = windowPad();
    const limit = pad * 2 + Math.min(vr.last - vr.first + 1, 300);
    const start = h.offset.value;
    const end = start + h.ids.length;
    const third = Math.floor(limit / 3);
    const outside = h.ids.length > 0 && (vr.first < start || vr.last >= end);
    const nearLow = start > 0 && vr.first - start < third;
    const nearHigh = end < total && end - vr.last < third;
    const empty = h.ids.length === 0 && total > 0;
    if (!force && !outside && !nearLow && !nearHigh && !empty) return;
    const desired = Math.max(0, Math.min(vr.first - pad, Math.max(0, total - limit)));
    const key = `${desired}:${limit}`;
    const now = performance.now();
    if (key === lastKey && now - lastKeyAt < 300) return;
    lastKey = key;
    lastKeyAt = now;
    h.update({ window: { offset: desired, limit } });
}
watch(
    () => {
        const r = virtualizer.value.range;
        return r ? r.startIndex * 1e7 + r.endIndex : -1;
    },
    () => requestWindow()
);
watch(
    () => [h.version.value, h.total.value],
    () => requestWindow()
);
watch(
    () => props.view.aggregateActive.value,
    on => {
        collapseAll();
        lastKey = '';
        if (!on) requestWindow(true);
    }
);
watch(isPhone, () => {
    collapseAll();
    lastKey = '';
    requestWindow(true);
});
// A new query starts at the top.
watch(
    () => JSON.stringify(props.view.query.value),
    () => {
        collapseAll();
        focusId.value = 0;
        anchor.id = 0;
        queryPending = true;
        if (scroller.value) scroller.value.scrollTop = 0;
    },
    { flush: 'post' }
);

// Resubscribed after a reconnect: wire ids restarted, so an open evidence panel, the focus and the anchor would point at another problem.
watch(
    () => h.epoch.value,
    () => {
        collapseAll();
        focusId.value = 0;
        anchor.id = 0;
        pending.value = new Map(); // keyed by wire id
    },
    { flush: 'sync' }
);

function collapseAll() {
    expandedId.value = 0;
    sheetId.value = 0;
    ackMenu.value = null;
}

// Drop interaction state for rows that are gone; settle optimistic acknowledgement.
watch(
    () => h.version.value,
    () => {
        queryPending = false;
        if (focusFirstNext && focusFirstRow() !== 'unloaded') focusFirstNext = false;
        if (expandedId.value && viewIndexOfId(expandedId.value) < 0 && h.ids.length) expandedId.value = 0;
        if (sheetId.value && !h.rows.has(sheetId.value) && h.ids.length) sheetId.value = 0;
        if (pending.value.size) {
            const cp = props.view.cols.value;
            const n = new Map(pending.value);
            for (const [id, want] of pending.value) {
                const row = h.rows.get(id);
                if (!row || (String(row[cp.acked]) === 'true') === (want === 'ack')) n.delete(id);
            }
            if (n.size !== pending.value.size) pending.value = n;
        }
    }
);

// ---- evidence (the one expanded row, or the phone sheet) ----
const activeId = computed(() => (isPhone.value ? sheetId.value : expandedId.value));
const activeRow = computed<ProblemRow | null>(() => {
    void h.version.value;
    const id = activeId.value;
    const row = id ? h.rows.get(id) : undefined;
    return row ? readRow(props.view.cols.value, row) : null;
});
const activeVer = computed(() => {
    void h.version.value;
    return h.rows.get(activeId.value)?.[1] ?? 0;
});

async function fetchEvidence(pid: string) {
    if (!pid) {
        evState.value = { pid: '', status: 'error', error: NO_PROBLEM_ID };
        return;
    }
    if (evState.value?.pid !== pid) evState.value = { pid, status: 'loading' };
    try {
        const data = await getAgent().call<ProblemsEvidenceResult>('problems.evidence', { id: pid });
        if (activeRow.value?.problemId === pid) evState.value = { pid, status: 'ok', data };
    } catch (e) {
        if (activeRow.value?.problemId !== pid) return;
        const code = (e as { code?: string })?.code;
        evState.value = {
            pid,
            status: 'error',
            error: code === 'not_found' ? 'This problem is gone.' : `Could not load evidence: ${e instanceof Error ? e.message : String(e)}`
        };
    }
}
const evRefresh = makeRefresher(() => {
    const r = activeRow.value;
    if (r) void fetchEvidence(r.problemId);
});
watch(
    () => [activeId.value, activeRow.value?.problemId ?? ''] as const,
    ([id, pid]) => {
        evRefresh.cancel();
        if (!id) {
            evState.value = null;
            return;
        }
        if (activeRow.value) void fetchEvidence(pid);
    },
    { immediate: true }
);
watch(activeVer, () => {
    if (activeId.value) evRefresh.schedule();
});
onBeforeUnmount(() => evRefresh.cancel());

// ---- actions ----
function pidOf(id: number): string {
    const row = h.rows.get(id);
    return row ? readRow(props.view.cols.value, row).problemId : '';
}

function setPending(ids: number[], want: 'ack' | 'unack' | null) {
    const n = new Map(pending.value);
    for (const id of ids) {
        if (want) n.set(id, want);
        else n.delete(id);
    }
    pending.value = n;
}

async function ack(ids: number[], preset: SnoozePreset | null) {
    const pids = ids.map(pidOf);
    if (pids.some(p => !p)) {
        ui.toast('danger', NO_PROBLEM_ID);
        return;
    }
    setPending(ids, 'ack');
    const results = await Promise.all(pids.map(p => setAcked(p, true, preset ?? undefined)));
    const failed = ids.filter((_, k) => !results[k]);
    if (failed.length) setPending(failed, null);
}
async function unack(ids: number[]) {
    const pids = ids.map(pidOf);
    if (pids.some(p => !p)) {
        ui.toast('danger', NO_PROBLEM_ID);
        return;
    }
    setPending(ids, 'unack');
    const results = await Promise.all(pids.map(p => setAcked(p, false)));
    const failed = ids.filter((_, k) => !results[k]);
    if (failed.length) setPending(failed, null);
}

/** Ids of the per-cluster rows behind a display item (one, or the whole aggregated group). */
function idsOf(it: Item): number[] {
    if (it.aggN > 1 && it.kind === 'row') return Array.from({ length: it.aggN }, (_, k) => idAtView(it.vi + k)).filter(Boolean);
    return it.id ? [it.id] : [];
}

function onAckEvidence(preset: SnoozePreset | null) {
    const id = activeId.value;
    if (id) void ack([id], preset);
    if (isPhone.value) sheetId.value = 0;
}
function onUnackEvidence() {
    const id = activeId.value;
    if (id) void unack([id]);
}

function openSubject(r: ProblemRow) {
    const ref = subjectRef(r, kinds.value, activeRow.value?.problemId === r.problemId && evState.value?.status === 'ok' ? evState.value.data : null);
    if (ref) void openObject(router, ref);
    else if (r.kind === 'Image') void router.push({ path: '/security', query: { image: r.name } });
}

function toggleExpand(it: Item) {
    if (!it.r || it.sk) return;
    focusId.value = it.id;
    if (isPhone.value) {
        sheetId.value = it.id;
        return;
    }
    if (it.kind === 'row' && it.aggN > 1) {
        const key = model.value.groups[describe(model.value, it.i).group]?.key;
        const n = new Set(aggOpen.value);
        if (!n.delete(key)) n.add(key);
        aggOpen.value = n;
        return;
    }
    expandedId.value = expandedId.value === it.id ? 0 : it.id;
}

function itemByIndex(i: number): Item | undefined {
    return slice.value.find(x => x.i === i);
}

function onClick(e: MouseEvent) {
    const t = e.target as HTMLElement;
    const el = t.closest<HTMLElement>('[data-i]');
    if (!el) return;
    const it = itemByIndex(Number(el.dataset.i));
    if (!it) return;
    const act = t.closest<HTMLElement>('[data-act]')?.dataset.act;
    if (it.kind === 'header') {
        store.toggleCollapsed(`${props.view.group.value}:${it.key}`);
        return;
    }
    if (it.kind !== 'row' && it.kind !== 'child') return;
    if (!it.r) return;
    focusId.value = it.id;
    switch (act) {
        case 'open':
            openSubject(it.r);
            return;
        case 'ack':
            void ack(idsOf(it), null);
            return;
        case 'snooze': {
            const b = (t.closest('[data-act]') as HTMLElement).getBoundingClientRect();
            // Opens below the button, or above it when the menu (about 140 px) would leave the window.
            const h = 140;
            const y = b.bottom + 4 + h > window.innerHeight ? Math.max(8, b.top - 4 - h) : b.bottom + 4;
            ackMenu.value = { id: it.id, x: Math.min(b.right, window.innerWidth - 8), y };
            // Focus moves into the menu so Esc is handled there (its @keydown.esc.stop) and never reaches the page chain.
            void nextTick(() => document.querySelector<HTMLElement>('.amenu button')?.focus({ preventScroll: true }));
            return;
        }
        case 'restore':
            void unack(idsOf(it));
            return;
        default:
            toggleExpand(it);
    }
}

function pickSnooze(p: SnoozePreset | null) {
    const m = ackMenu.value;
    ackMenu.value = null;
    if (!m) return;
    const it = slice.value.find(x => x.id === m.id && (x.kind === 'row' || x.kind === 'child'));
    void ack(it ? idsOf(it) : [m.id], p);
}

// ---- keyboard ----
function flatOfId(id: number): number {
    const vi = viewIndexOfId(id);
    if (vi < 0) return -1;
    const m = model.value;
    return m.layout ? flatIndexOfView(m.layout, vi) : aggFlatIndexOfView(m.flat, vi);
}

function moveFocus(delta: number) {
    const m = model.value;
    let i = flatOfId(focusId.value);
    if (i < 0) i = (virtualizer.value.range?.startIndex ?? 0) - delta;
    for (let j = i + delta; j >= 0 && j < m.count; j += delta) {
        const meta = describe(m, j);
        if (meta.kind !== 'row' && meta.kind !== 'child') continue;
        const id = idAtView(meta.vi);
        if (!id) return;
        focusId.value = id;
        virtualizer.value.scrollToIndex(j, { align: 'auto' });
        return;
    }
}

// Enter in the search box (and Esc from the list that clears it) puts focus on the list and its row focus on the first result.
// While a search/sort change waits for its reply, the loaded window still holds the previous query's rows: focus the list now and
// the first row when the reply lands.
let queryPending = false;
let focusFirstNext = false;

/** Row focus to the first row of the results. 'unloaded': the window is not at the top yet (scrolled to the top; retried on the next reply). */
function focusFirstRow(): 'ok' | 'none' | 'unloaded' {
    const m = model.value;
    for (let j = 0; j < m.count; j++) {
        const meta = describe(m, j);
        if (meta.kind !== 'row' && meta.kind !== 'child') continue;
        const id = idAtView(meta.vi);
        if (!id) {
            if (scroller.value) scroller.value.scrollTop = 0;
            return 'unloaded';
        }
        focusId.value = id;
        // the first row: show the top of the list, its group header included
        if (scroller.value) scroller.value.scrollTop = 0;
        return 'ok';
    }
    return 'none';
}

function focusList(first: boolean): boolean {
    const el = scroller.value;
    if (!el) return false;
    if (queryPending) {
        focusFirstNext = true;
        el.focus({ preventScroll: true });
        return true;
    }
    if (!first && flatOfId(focusId.value) >= 0) {
        el.focus({ preventScroll: true });
        virtualizer.value.scrollToIndex(flatOfId(focusId.value), { align: 'auto' });
        return true;
    }
    const r = focusFirstRow();
    if (r === 'none') return false;
    if (r === 'unloaded') focusFirstNext = true;
    el.focus({ preventScroll: true });
    return true;
}

setListNav({
    focusList,
    owns: el => el instanceof Node && !!scroller.value?.contains(el),
    filterText: () => store.text,
    clearFilter: () => {
        store.text = '';
    }
});

function focusedItem(): Item | undefined {
    return slice.value.find(x => x.id === focusId.value && (x.kind === 'row' || x.kind === 'child'));
}

setPageKeyHandler(e => {
    switch (e.key) {
        case 'j':
        case 'ArrowDown':
            moveFocus(1);
            return true;
        case 'k':
        case 'ArrowUp':
            moveFocus(-1);
            return true;
        case 'Enter': {
            const it = focusedItem();
            const tag = document.activeElement?.tagName;
            if (!it || tag === 'BUTTON' || tag === 'A') return false;
            toggleExpand(it);
            return true;
        }
        case 'a': {
            const it = focusedItem();
            if (!it?.r) return false;
            void (it.r.acked ? unack(idsOf(it)) : ack(idsOf(it), null));
            return true;
        }
        case 'o': {
            const it = focusedItem();
            if (!it?.r) return false;
            openSubject(it.r);
            return true;
        }
    }
    return false;
});

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
    setListNav(null);
});

// ---- empty states ----
const filtered = computed(() => !!(store.families.length || store.text || store.severity !== 'all' || table.of('v8s/v1/problems').namespaces.length));
const emptyKind = computed(() => {
    if (!clusters.activeIds.length) return 'no-clusters';
    if (h.total.value > 0) return '';
    if (h.version.value === 0) return 'loading';
    if (filtered.value) return 'filtered';
    if (clusters.states.some(s => s.phase === 'syncing' || s.phase === 'connecting')) return 'syncing';
    return 'none';
});

const GRID = '76px 148px 150px minmax(0, 1.6fr) minmax(0, 1.7fr) 52px 70px';
const extra = computed(() => props.view.aggregateActive.value);

defineExpose({ scroller });
</script>

<template>
    <div
        ref="scroller"
        class="scroller"
        :class="{ phone: isPhone, rowfocus: focusInView }"
        :style="{ '--row-h': rowH + 'px', '--cols': GRID, '--minw': '900px' }"
        role="treegrid"
        aria-label="Problems"
        :aria-rowcount="model.count"
        tabindex="0"
        data-testid="problems-list"
        @scroll.passive="trackAnchor"
    >
        <div v-if="!isPhone" class="tr th" role="row">
            <span role="columnheader">Severity</span><span role="columnheader">Cluster</span><span role="columnheader">Class</span
            ><span role="columnheader">Object</span><span role="columnheader">What's wrong</span><span role="columnheader">Since</span
            ><span role="columnheader"><span class="sr-only">Actions</span></span>
        </div>
        <div class="sizer" :style="{ height: totalPx + 'px' }" @click="onClick">
            <template v-for="it in slice" :key="it.i">
                <!-- group header -->
                <div
                    v-if="it.kind === 'header'"
                    v-memo="[it.mk]"
                    class="grp"
                    :style="{ transform: 'translateY(' + it.start + 'px)' }"
                    role="row"
                    :aria-rowindex="it.i + 1"
                    :data-i="it.i"
                    data-testid="problems-group"
                >
                    <button
                        class="gbtn"
                        type="button"
                        :aria-expanded="!it.collapsed"
                        :aria-label="`${it.collapsed ? 'Expand' : 'Collapse'} ${it.label}`"
                    >
                        <svg
                            class="chev"
                            :class="{ open: !it.collapsed }"
                            width="12"
                            height="12"
                            viewBox="0 0 16 16"
                            fill="none"
                            stroke="currentColor"
                            stroke-width="1.8"
                            aria-hidden="true"
                        >
                            <path d="M6 3l5 5-5 5" />
                        </svg>
                        <span class="gname" :class="{ mono: props.view.group.value === 'class' }">{{ it.label }}</span>
                        <span class="muted gcount">{{ it.count }}</span>
                    </button>
                </div>
                <!-- the one open evidence item -->
                <div
                    v-else-if="it.kind === 'evidence'"
                    class="evi"
                    :style="{ transform: 'translateY(' + it.start + 'px)', height: EVID_H + 'px' }"
                    role="row"
                    :aria-rowindex="it.i + 1"
                >
                    <ProblemsEvidence
                        v-if="activeRow && expandedId === it.id"
                        :row="activeRow"
                        :state="evState"
                        @ack="onAckEvidence"
                        @unack="onUnackEvidence"
                    />
                </div>
                <!-- desktop row / child -->
                <div
                    v-else-if="!isPhone"
                    v-memo="[it.mk]"
                    class="tr row prow"
                    :class="{ focus: it.foc, sk: it.sk, faded: it.fade, child: it.kind === 'child', open: it.exp }"
                    :style="{ transform: 'translateY(' + it.start + 'px)' }"
                    role="row"
                    :aria-rowindex="it.i + 1"
                    :aria-expanded="it.r ? (it.kind === 'row' && it.aggN > 1 ? it.aggOpen : it.exp) : undefined"
                    :data-i="it.i"
                    data-testid="problem-row"
                >
                    <template v-if="it.r">
                        <span class="c" role="cell"><span class="tag" :class="severityTone(it.r.severity)">{{ it.r.severity }}</span></span>
                        <span class="c clus" role="cell">
                            <span v-for="c in it.clusters.slice(0, it.clusters.length > 2 ? 1 : 2)" :key="c" class="tag"
                                ><span class="dot" :style="{ background: clusterColor(c) }"></span>{{ nameOf(c) }}<ProtectedMark :cluster="c" /></span
                            ><span v-if="it.clusters.length > 2" class="tag">+{{ it.clusters.length - 1 }}</span>
                        </span>
                        <span class="c mono muted" role="cell">{{ it.r.class }}</span>
                        <span class="c obj" role="cell">
                            <button
                                class="chev-btn"
                                type="button"
                                data-act="expand"
                                :aria-label="it.aggN > 1 && it.kind === 'row' ? `Show ${it.aggN} clusters` : 'Show evidence'"
                                :aria-expanded="it.kind === 'row' && it.aggN > 1 ? it.aggOpen : it.exp"
                            >
                                <svg
                                    class="chev"
                                    :class="{ open: it.kind === 'row' && it.aggN > 1 ? it.aggOpen : it.exp }"
                                    width="12"
                                    height="12"
                                    viewBox="0 0 16 16"
                                    fill="none"
                                    stroke="currentColor"
                                    stroke-width="1.8"
                                    aria-hidden="true"
                                >
                                    <path d="M6 3l5 5-5 5" />
                                </svg>
                            </button>
                            <span class="muted">{{ it.r.kind }}&nbsp;</span>
                            <button
                                class="oname"
                                type="button"
                                data-act="open"
                                :title="it.r.namespace ? it.r.namespace + '/' + it.r.name : it.r.name"
                            >
                                <span v-if="it.r.namespace" class="muted nsp">{{ it.r.namespace }}/</span><b>{{ it.r.name }}</b>
                            </button>
                            <span v-if="it.rollup" class="tag info cnt" :title="`${it.r.count} objects`">{{ it.r.count }}</span>
                            <span v-if="extra && it.kind === 'row' && it.aggN > 1" class="tag release cnt">in {{ it.aggN }} clusters</span>
                        </span>
                        <span class="c msg" role="cell" :title="it.r.message || it.r.title"
                            >{{ it.r.title }}<span v-if="it.r.message" class="muted">&nbsp;{{ it.r.message }}</span></span
                        >
                        <span class="c muted" role="cell">{{ it.age }}</span>
                        <span class="c act" role="cell">
                            <button
                                v-if="it.r.acked && pending.get(it.id) !== 'unack'"
                                class="btn ghost sm rst"
                                type="button"
                                data-act="restore"
                                aria-label="Restore (unacknowledge)"
                            >
                                Restore
                            </button>
                            <template v-else-if="!it.fade">
                                <button
                                    class="btn ghost sm ib"
                                    type="button"
                                    data-act="ack"
                                    :aria-label="it.aggN > 1 && it.kind === 'row' ? 'Acknowledge in all clusters' : 'Acknowledge'"
                                    title="Acknowledge (a)"
                                >
                                    <svg
                                        width="13"
                                        height="13"
                                        viewBox="0 0 16 16"
                                        fill="none"
                                        stroke="currentColor"
                                        stroke-width="1.6"
                                        aria-hidden="true"
                                    >
                                        <path d="M3 8.5l3 3 7-7" />
                                    </svg>
                                </button>
                                <button
                                    class="btn ghost sm ib"
                                    type="button"
                                    data-act="snooze"
                                    aria-haspopup="menu"
                                    aria-label="Snooze"
                                    title="Snooze"
                                >
                                    <svg
                                        width="11"
                                        height="11"
                                        viewBox="0 0 16 16"
                                        fill="none"
                                        stroke="currentColor"
                                        stroke-width="1.8"
                                        aria-hidden="true"
                                    >
                                        <path d="M4 6l4 4 4-4" />
                                    </svg>
                                </button>
                            </template>
                        </span>
                    </template>
                    <template v-else>
                        <span v-for="n in 7" :key="n" class="c"><i :style="{ width: 35 + ((n * 17) % 45) + '%' }"></i></span>
                    </template>
                </div>
                <!-- phone card -->
                <div
                    v-else
                    v-memo="[it.mk]"
                    class="pcard prow"
                    :class="{ focus: it.foc, sk: it.sk, faded: it.fade }"
                    :style="{ transform: 'translateY(' + it.start + 'px)' }"
                    role="row"
                    :aria-rowindex="it.i + 1"
                    :data-i="it.i"
                    data-testid="problem-card"
                >
                    <div v-if="it.r" class="pc-main" role="cell">
                        <div class="pc-top">
                            <span class="tag" :class="severityTone(it.r.severity)">{{ it.r.severity }}</span>
                            <span class="c pname">{{ it.r.kind }} {{ it.r.name }}</span>
                            <span v-if="it.rollup" class="tag info">{{ it.r.count }}</span>
                            <span v-if="it.aggN > 1 && it.kind === 'row'" class="tag release">{{ it.aggN }} clusters</span>
                        </div>
                        <div class="c ptitle">{{ it.r.title }}</div>
                        <div class="sub">
                            <span class="dot" :style="{ background: clusterColor(it.r.cluster) }"></span>{{ nameOf(it.r.cluster)
                            }}<ProtectedMark :cluster="it.r.cluster" /><span>·</span>{{ it.age }}<span>·</span
                            ><span class="mono cls">{{ it.r.class }}</span>
                        </div>
                    </div>
                    <div v-else class="pc-main"><i style="width: 70%"></i><i style="width: 45%"></i><i style="width: 55%"></i></div>
                    <svg
                        v-if="it.r"
                        width="14"
                        height="14"
                        viewBox="0 0 16 16"
                        fill="none"
                        stroke="var(--text3)"
                        stroke-width="1.6"
                        aria-hidden="true"
                        style="flex: none"
                    >
                        <path d="M6 3l5 5-5 5" />
                    </svg>
                </div>
            </template>
        </div>
        <div v-if="emptyKind" class="empty" role="status" data-testid="problems-empty">
            <template v-if="emptyKind === 'no-clusters'">
                <p>No clusters connected.</p>
                <button class="btn" type="button" @click="clusters.open()">Choose clusters</button>
            </template>
            <template v-else-if="emptyKind === 'loading'"><p class="muted">Loading problems…</p></template>
            <template v-else-if="emptyKind === 'filtered'">
                <p>No problems match the current filters.</p>
                <button
                    class="btn sm"
                    type="button"
                    @click="
                        store.clearFilters();
                        table.of('v8s/v1/problems').namespaces = [];
                    "
                >
                    Clear filters
                </button>
            </template>
            <template v-else-if="emptyKind === 'syncing'"
                ><p class="muted">Clusters are still syncing; problems appear as checks complete.</p></template
            >
            <template v-else>
                <p class="ok"><span class="tag success">All clear</span></p>
                <p class="muted">No problems in the selected clusters.</p>
            </template>
        </div>
        <!-- Teleported: the list's `contain: layout paint` makes it the containing block of fixed descendants, so viewport
             coordinates would land relative to the list (and focusing the menu would scroll it into view). -->
        <Teleport v-if="ackMenu" to="body">
            <div class="back" @mousedown="ackMenu = null"></div>
            <div
                class="floating amenu"
                role="menu"
                aria-label="Acknowledge"
                :style="{ left: Math.max(8, ackMenu.x - 168) + 'px', top: ackMenu.y + 'px' }"
                @keydown.esc.stop="ackMenu = null"
            >
                <button class="mi" type="button" role="menuitem" @click="pickSnooze(null)">Acknowledge</button>
                <button v-for="p in SNOOZE_PRESETS" :key="p.id" class="mi" type="button" role="menuitem" @click="pickSnooze(p.id)">
                    {{ p.label }}
                </button>
            </div>
        </Teleport>
    </div>
    <ProblemsDetailSheet
        v-if="isPhone && sheetId && activeRow"
        :row="activeRow"
        :state="evState"
        @close="sheetId = 0"
        @ack="onAckEvidence"
        @unack="onUnackEvidence"
        @open="openSubject(activeRow)"
    />
</template>

<style scoped>
.grp {
    position: absolute;
    left: 0;
    right: 0;
    top: 0;
    height: 32px;
    display: flex;
    align-items: center;
    background: var(--s2);
    border-bottom: 1px solid var(--border);
    font-size: 12px;
    font-weight: 600;
    color: var(--text2);
    contain: layout paint;
    min-width: var(--minw);
}
.scroller.phone .grp {
    min-width: 0;
}
.gbtn {
    all: unset;
    box-sizing: border-box;
    display: flex;
    align-items: center;
    gap: 8px;
    height: 100%;
    width: 100%;
    padding: 0 12px;
    cursor: pointer;
}
.gbtn:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
}
.gcount {
    font-weight: 400;
}
.chev {
    transition: transform 0.12s;
    flex: none;
}
.chev.open {
    transform: rotate(90deg);
}
.evi {
    position: absolute;
    left: 0;
    right: 0;
    top: 0;
    border-bottom: 1px solid var(--border);
    contain: layout paint;
    min-width: var(--minw);
    overflow: hidden;
}
.scroller.phone .evi {
    min-width: 0;
}
.prow.faded {
    opacity: 0.5;
    transition: opacity 0.15s;
}
.prow.open {
    background: var(--hover);
}
.prow.child {
    background: var(--bg);
}
.prow.child .obj {
    padding-left: 22px;
}
.clus {
    display: flex;
    gap: 4px;
    min-width: 0;
    overflow: hidden;
}
.obj {
    display: flex;
    align-items: center;
    gap: 2px;
    min-width: 0;
}
.nsp {
    font-weight: 400;
}
.oname {
    all: unset;
    cursor: pointer;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
.oname:hover b {
    text-decoration: underline;
}
.oname:focus-visible,
.chev-btn:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: 1px;
    border-radius: 4px;
}
.chev-btn {
    all: unset;
    cursor: pointer;
    width: 20px;
    height: 20px;
    display: grid;
    place-items: center;
    flex: none;
    color: var(--text3);
}
.cnt {
    margin-left: 6px;
    flex: none;
}
.msg {
    color: var(--text2);
}
.act {
    display: flex;
    gap: 0;
    justify-content: flex-end;
}
.ib {
    padding: 0 4px;
    height: 22px;
}
.rst {
    height: 22px;
    padding: 0 6px;
}
.sk i,
.pcard.sk i {
    display: block;
    height: 12px;
    border-radius: 6px;
    background: var(--s3);
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
.back {
    position: fixed;
    inset: 0;
    z-index: 30;
}
.amenu {
    position: fixed;
    z-index: 31;
    min-width: 168px;
    padding: 4px;
    display: flex;
    flex-direction: column;
}
.mi {
    all: unset;
    box-sizing: border-box;
    height: 30px;
    padding: 0 10px;
    display: flex;
    align-items: center;
    border-radius: 5px;
    font-size: 13px;
    cursor: pointer;
}
.mi:hover,
.mi:focus-visible {
    background: var(--hover);
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
    gap: 6px;
    min-width: 0;
}
.pname {
    font-size: 14px;
    font-weight: 600;
    min-width: 0;
    flex-grow: 1;
}
.ptitle {
    color: var(--text2);
    font-size: 12px;
}
.pcard .sub {
    font-size: 11px;
}
.cls {
    font-size: 11px;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
}
@media (pointer: coarse) {
    .chev-btn,
    .ib,
    .rst {
        min-width: 44px;
        min-height: 44px;
    }
}
</style>
