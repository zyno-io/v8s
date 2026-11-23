<script setup lang="ts">
import type { ObjectRef } from '@v8s/client';

import { useVirtualizer } from '@tanstack/vue-virtual';
import { computed, onBeforeUnmount, ref, shallowRef, watch } from 'vue';

import type { ReportCol, ReportRow } from '@/lib/security.ts';

import { formatCount } from '@/lib/format.ts';
import { errorText } from '@/lib/mutations.ts';
import { reportDetail, rescanReport } from '@/lib/security-api.ts';
import {
    CHUNK_LIMIT,
    ChunkAccumulator,
    columnsFor,
    REPORT_KIND_LABEL,
    resultRank,
    rowsOf,
    sevLabel,
    sevRank,
    sevTone,
    resultTone,
    sourceLabel,
    viewRows,
    wantsMore
} from '@/lib/security.ts';
import { isPhone, ROW_H_DESKTOP, ROW_H_PHONE } from '@/lib/viewport.ts';
import { useUiStore } from '@/stores/ui.ts';

const props = defineProps<{
    target: ObjectRef;
    /** Fixed scroller height (px) instead of filling the parent. */
    height?: number;
    /** Policy results: shows a "Create exception" button on failing rows. */
    makeException?: (row: ReportRow) => void;
}>();
const emit = defineEmits<{ rescanned: []; loaded: [kind: string, total: number] }>();

const ui = useUiStore();
const acc = new ChunkAccumulator<ReportRow>();
const version = shallowRef(0);
const kind = ref('');
const source = ref('');
const title = ref('');
const summary = shallowRef<Record<string, number>>({});
const doc = shallowRef('');
const loading = ref(true);
const error = ref('');
const sevs = shallowRef<ReadonlySet<string>>(new Set());
const text = ref('');
const sort = ref<{ col: string; desc: boolean } | null>(null);
const copied = ref(false);

let epoch = 0;
let running = false;
let wanted = 0;

async function pump(all = false) {
    if (running) return;
    running = true;
    const mine = epoch;
    try {
        while (mine === epoch && !acc.done && (all || kind.value === '' || wantsMore(acc, wanted))) {
            const off = acc.nextOffset;
            const d = await reportDetail(props.target, off, CHUNK_LIMIT);
            if (mine !== epoch) return;
            if (!kind.value) {
                kind.value = d.kind;
                source.value = d.source;
                title.value = d.title ?? '';
                emit('loaded', d.kind, d.total);
            }
            summary.value = d.summary ?? summary.value;
            if (d.kind === 'document') {
                doc.value = d.doc ?? '';
                acc.done = true;
                break;
            }
            const rows = rowsOf(d);
            const r = acc.accept(d.offset ?? off, rows, d.total, d.resourceVersion);
            if (r === 'restart') {
                version.value++;
                continue; // the report was replaced mid-read: read it again from the top
            }
            if (r === 'stale') break;
            version.value++;
        }
        error.value = '';
    } catch (e) {
        if (mine === epoch) error.value = errorText(e);
    } finally {
        running = false;
        if (mine === epoch) loading.value = false;
    }
}

function start() {
    epoch++;
    running = false;
    acc.reset();
    kind.value = '';
    doc.value = '';
    summary.value = {};
    sevs.value = new Set();
    text.value = '';
    sort.value = null;
    error.value = '';
    loading.value = true;
    wanted = 0;
    version.value++;
    void pump();
}

watch(() => `${props.target.cluster}|${props.target.gvr}|${props.target.namespace ?? ''}|${props.target.name ?? ''}`, start, { immediate: true });
onBeforeUnmount(() => epoch++);

const cols = computed<ReportCol[]>(() => {
    const c = columnsFor(kind.value);
    return props.makeException && kind.value === 'policyResults' ? [...c, { key: 'action', label: '', width: '110px' }] : c;
});
const view = computed<ReportRow[]>(() => {
    void version.value;
    return viewRows(acc.rows, sevs.value, text.value, sort.value, cols.value);
});

const phone = isPhone;
const rowH = computed(() => (phone.value ? ROW_H_PHONE : ROW_H_DESKTOP));
const scroller = ref<HTMLDivElement | null>(null);
const virtualizer = useVirtualizer<HTMLDivElement, Element>(
    computed(() => ({
        count: view.value.length,
        getScrollElement: () => scroller.value,
        estimateSize: () => rowH.value,
        overscan: 12
    }))
);
const items = computed(() => virtualizer.value.getVirtualItems());
// Rows are placed from cached sizes: re-measure when the layout switches between phone and desktop row heights.
watch(rowH, () => virtualizer.value.measure());
watch(
    () => virtualizer.value.range?.endIndex,
    end => {
        wanted = end ?? 0;
        if (!acc.done && wantsMore(acc, wanted)) void pump();
    }
);

const chips = computed(() => {
    const keys = Object.keys(summary.value);
    const rank = kind.value === 'policyResults' ? resultRank : sevRank;
    return keys
        .sort((a, b) => rank(a) - rank(b))
        .map(k => ({ key: k, n: summary.value[k] ?? 0, tone: kind.value === 'policyResults' ? resultTone(k) : sevTone(k) }));
});
function toggleSev(k: string) {
    const n = new Set(sevs.value);
    if (!n.delete(k)) n.add(k);
    sevs.value = n;
}

function sortBy(key: string) {
    const cur = sort.value;
    sort.value = !cur || cur.col !== key ? { col: key, desc: false } : cur.desc ? null : { col: key, desc: true };
}
const arrow = (key: string) => (sort.value?.col === key ? (sort.value.desc ? ' ▼' : ' ▲') : '');
const ariaSort = (key: string) => (sort.value?.col === key ? (sort.value.desc ? 'descending' : 'ascending') : 'none');

const grid = computed(() => cols.value.map(c => c.width).join(' '));
const isTrivy = computed(() => props.target.gvr.startsWith('aquasecurity.github.io/'));
const status = computed(() => {
    if (loading.value && !acc.rows.length) return 'Loading report…';
    const f = view.value.length !== acc.rows.length ? ` · ${formatCount(view.value.length)} match` : '';
    return `${formatCount(acc.rows.length)} of ${formatCount(acc.total)} loaded${f}`;
});

async function rescan() {
    if (await rescanReport(props.target, title.value || (props.target.name ?? 'report'))) emit('rescanned');
}

async function copy() {
    try {
        await navigator.clipboard.writeText(doc.value);
        copied.value = true;
        setTimeout(() => (copied.value = false), 1500);
    } catch {
        ui.toast('danger', 'Could not copy to the clipboard');
    }
}

const cellOf = (r: ReportRow, i: number): string => (i === 0 ? r.id : (r.cells[i - 2] ?? ''));
const phoneLine2 = (r: ReportRow) => r.cells.slice(0, 3).filter(Boolean).join(' · ');
const phoneLine3 = (r: ReportRow) => r.cells[r.cells.length - 1] ?? '';
</script>

<template>
    <section class="cve" :class="{ fixed: !!height }" :style="height ? { height: height + 'px' } : undefined" data-testid="cve-table">
        <header class="shead">
            <div class="top">
                <b class="ttl">{{ title || target.name }}</b>
                <span v-if="source" class="tag">{{ sourceLabel(source) }}</span>
                <span v-if="kind" class="tag">{{ REPORT_KIND_LABEL[kind] ?? kind }}</span>
                <span v-if="kind && kind !== 'document'" class="muted small">{{ formatCount(acc.total) }} total</span>
                <div class="grow"></div>
                <button v-if="isTrivy" class="btn sm" type="button" title="Delete report to trigger a rescan" data-testid="rescan" @click="rescan">
                    Rescan
                </button>
                <button v-if="kind === 'document'" class="btn sm" type="button" data-testid="copy-doc" @click="copy">
                    {{ copied ? 'Copied' : 'Copy' }}
                </button>
            </div>
            <div v-if="kind && kind !== 'document'" class="filters">
                <label class="field filter">
                    <input
                        v-model="text"
                        type="search"
                        placeholder="Filter loaded rows"
                        aria-label="Filter report rows"
                        autocomplete="off"
                        spellcheck="false"
                    />
                </label>
                <label v-for="c in chips" :key="c.key" class="chip-wrap">
                    <input class="sr-only chip-input" type="checkbox" :checked="sevs.has(c.key)" @change="toggleSev(c.key)" />
                    <span class="chip" :class="{ on: sevs.has(c.key) }">
                        <span class="tag" :class="c.tone">{{ sevLabel(c.key) }}</span> <b>{{ formatCount(c.n) }}</b>
                    </span>
                </label>
                <button v-if="!acc.done && !loading" class="btn sm" type="button" data-testid="load-all" @click="pump(true)">Load all</button>
            </div>
            <div class="muted small" aria-live="polite" role="status">{{ error ? '' : kind === 'document' ? '' : status }}</div>
        </header>
        <div v-if="error" class="notice warn" role="alert">{{ error }}</div>
        <pre v-else-if="kind === 'document'" class="doc mono" tabindex="0" :data-drawer-scroll="height ? undefined : ''" data-testid="report-doc">{{
            doc
        }}</pre>
        <p v-else-if="kind && !acc.rows.length && !loading" class="muted empty">This report has no rows.</p>
        <div
            v-else
            ref="scroller"
            class="scroller"
            :data-drawer-scroll="height ? undefined : ''"
            role="table"
            :aria-rowcount="view.length + 1"
            :aria-label="REPORT_KIND_LABEL[kind] ?? 'Report'"
            tabindex="0"
        >
            <div v-if="!phone && kind" class="tr th" role="row" :style="{ gridTemplateColumns: grid }">
                <span v-for="(c, i) in cols" :key="c.key" class="c" role="columnheader" :aria-sort="c.label ? ariaSort(c.key) : undefined">
                    <button v-if="c.label" class="th-btn" type="button" @click="sortBy(c.key)">{{ c.label }}{{ arrow(c.key) }}</button>
                    <span v-else class="sr-only">Actions {{ i }}</span>
                </span>
            </div>
            <div class="vp" :style="{ height: virtualizer.getTotalSize() + 'px' }">
                <template v-for="vi in items" :key="vi.index">
                    <div
                        v-if="view[vi.index]"
                        class="tr"
                        :class="{ mcard: phone }"
                        role="row"
                        :style="{ height: rowH + 'px', transform: `translateY(${vi.start}px)`, gridTemplateColumns: phone ? undefined : grid }"
                    >
                        <template v-if="phone">
                            <div class="l1">
                                <a v-if="view[vi.index].href" class="mono" :href="view[vi.index].href" target="_blank" rel="noopener noreferrer">{{
                                    view[vi.index].id
                                }}</a>
                                <span v-else class="mono">{{ view[vi.index].id }}</span>
                                <span class="tag" :class="view[vi.index].tone">{{ sevLabel(view[vi.index].sev) }}</span>
                                <button
                                    v-if="makeException && kind === 'policyResults' && view[vi.index].sev === 'fail'"
                                    class="btn sm"
                                    type="button"
                                    @click="makeException(view[vi.index])"
                                >
                                    Exception
                                </button>
                            </div>
                            <div class="l2 muted">{{ phoneLine2(view[vi.index]) }}</div>
                            <div class="l3">{{ phoneLine3(view[vi.index]) }}</div>
                        </template>
                        <template v-else>
                            <template v-for="(c, i) in cols" :key="c.key">
                                <span v-if="c.key === 'action'" class="c">
                                    <button
                                        v-if="view[vi.index].sev === 'fail' && makeException"
                                        class="btn sm"
                                        type="button"
                                        :aria-label="`Create exception for ${view[vi.index].id}`"
                                        @click="makeException(view[vi.index])"
                                    >
                                        Exception
                                    </button>
                                </span>
                                <span v-else-if="i === 0" class="c" :class="{ mono: c.mono }" :title="view[vi.index].id">
                                    <a v-if="view[vi.index].href" :href="view[vi.index].href" target="_blank" rel="noopener noreferrer">{{
                                        view[vi.index].id
                                    }}</a>
                                    <template v-else>{{ view[vi.index].id }}</template>
                                </span>
                                <span v-else-if="i === 1" class="c"
                                    ><span class="tag" :class="view[vi.index].tone">{{ sevLabel(view[vi.index].sev) }}</span></span
                                >
                                <span
                                    v-else
                                    class="c"
                                    :class="{ mono: c.mono, fix: c.key === 'fixed' && view[vi.index].hi }"
                                    :title="cellOf(view[vi.index], i)"
                                    >{{ cellOf(view[vi.index], i) }}</span
                                >
                            </template>
                        </template>
                    </div>
                </template>
            </div>
        </div>
    </section>
</template>

<style scoped>
.cve {
    display: flex;
    flex-direction: column;
    min-height: 0;
    flex: 1 1 0;
    gap: 8px;
}
.cve.fixed {
    flex: none;
}
.shead {
    display: flex;
    flex-direction: column;
    gap: 8px;
}
.top,
.filters {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
}
.ttl {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
.small {
    font-size: 12px;
}
.filter {
    width: 240px;
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
.scroller {
    flex: 1 1 0;
    min-height: 0;
    overflow: auto;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--surface);
    position: relative;
}
.vp {
    position: relative;
    min-width: 760px;
}
.tr {
    display: grid;
    align-items: center;
    gap: 12px;
    padding: 0 12px;
    min-width: 760px;
}
.vp .tr {
    position: absolute;
    top: 0;
    left: 0;
    right: 0;
    border-bottom: 1px solid var(--border);
    font-size: 13px;
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
.c {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
.c a,
.l1 a {
    color: var(--link);
}
.fix {
    color: var(--success);
    font-weight: 600;
}
.doc {
    flex: 1 1 0;
    min-height: 200px;
    margin: 0;
    overflow: auto;
    padding: 10px 12px;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--surface);
    white-space: pre;
}
.empty {
    padding: 24px 8px;
    text-align: center;
}
.mcard {
    display: flex;
    flex-direction: column;
    justify-content: center;
    align-items: stretch;
    gap: 2px;
    min-width: 0;
}
.l1 {
    display: flex;
    gap: 8px;
    align-items: center;
}
.l2,
.l3 {
    font-size: 12px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
@media (max-width: 639.98px) {
    .vp,
    .tr {
        min-width: 0;
    }
}
@media (pointer: coarse) {
    .top .btn.sm,
    .filters .btn.sm {
        min-height: 44px;
    }
}
</style>
