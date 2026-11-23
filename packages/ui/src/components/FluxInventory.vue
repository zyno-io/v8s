<script setup lang="ts">
import type { FluxInventoryResult, ObjectRef } from '@v8s/client';

import { useVirtualizer } from '@tanstack/vue-virtual';
import { computed, nextTick, onBeforeUnmount, ref, shallowRef, watch } from 'vue';
import { useRouter } from 'vue-router';

import type { FlatRow, InventoryTree } from '@/lib/flux-inventory.ts';

import { getAgent } from '@/lib/agent.ts';
import { openObject } from '@/lib/drawer.ts';
import { buildInventoryTree, flattenTree } from '@/lib/flux-inventory.ts';
import { toneClass } from '@/lib/tone.ts';

const ROW_H = 28;
const props = defineProps<{ target: ObjectRef; refreshKey?: number }>();
const router = useRouter();

const tree = shallowRef<InventoryTree | null>(null);
const revision = shallowRef('');
const loading = ref(false);
const error = ref('');
const filter = ref('');
const expanded = shallowRef<Set<string>>(new Set());
const cursor = ref(0);
const scroller = ref<HTMLElement | null>(null);
let seq = 0;
let first = true;

async function load() {
    const my = ++seq;
    loading.value = true;
    try {
        const res = await getAgent().call<FluxInventoryResult>('flux.inventory', { ref: props.target });
        if (my !== seq) return;
        const t = buildInventoryTree(res.entries ?? []);
        tree.value = t;
        revision.value = res.revision ?? '';
        error.value = '';
        if (first) {
            first = false;
            // Open everything for small inventories, otherwise only what needs attention.
            const open = new Set<string>();
            for (const g of t.groups) {
                if (t.summary.total <= 40 || g.tone === 2 || g.tone === 3) {
                    open.add(g.key);
                    for (const n of g.namespaces) if (t.summary.total <= 40 || n.tone === 2 || n.tone === 3) open.add(n.key);
                }
            }
            expanded.value = open;
        }
    } catch (e) {
        if (my !== seq) return;
        error.value = e instanceof Error ? e.message : String(e);
    } finally {
        if (my === seq) loading.value = false;
    }
}

watch(
    () => [props.target.cluster, props.target.gvr, props.target.namespace, props.target.name],
    () => {
        tree.value = null;
        first = true;
        expanded.value = new Set();
        void load();
    },
    { immediate: true }
);

let timer: ReturnType<typeof setTimeout> | undefined;
watch(
    () => props.refreshKey,
    () => {
        clearTimeout(timer);
        timer = setTimeout(() => void load(), 500);
    }
);
onBeforeUnmount(() => {
    clearTimeout(timer);
    seq++;
});

const rows = computed<FlatRow[]>(() => (tree.value ? flattenTree(tree.value, expanded.value, filter.value) : []));
watch(rows, r => {
    if (cursor.value >= r.length) cursor.value = Math.max(0, r.length - 1);
});

const virt = useVirtualizer(
    computed(() => ({
        count: rows.value.length,
        getScrollElement: () => scroller.value,
        estimateSize: () => ROW_H,
        overscan: 12
    }))
);
const items = computed(() => virt.value.getVirtualItems());
const totalSize = computed(() => virt.value.getTotalSize());

const summary = computed(() => tree.value?.summary);

function toggle(key: string) {
    const s = new Set(expanded.value);
    if (!s.delete(key)) s.add(key);
    expanded.value = s;
}
function expandAll() {
    expanded.value = new Set(tree.value?.branchKeys);
}
function collapseAll() {
    expanded.value = new Set();
}

function canOpen(r: FlatRow): boolean {
    return r.kind === 'leaf' && !!r.entry?.gvr && r.entry.live;
}
function activate(r: FlatRow) {
    if (r.kind !== 'leaf') return toggle(r.key);
    const e = r.entry;
    if (!e || !e.gvr || !e.live) return;
    void openObject(router, { cluster: props.target.cluster, gvr: e.gvr, namespace: e.namespace, name: e.name });
}
function onRowClick(i: number) {
    cursor.value = i;
    const r = rows.value[i];
    if (r) activate(r);
}

function moveTo(i: number) {
    const n = rows.value.length;
    if (!n) return;
    cursor.value = Math.min(n - 1, Math.max(0, i));
    virt.value.scrollToIndex(cursor.value);
}
function parentIndex(i: number): number {
    const d = rows.value[i]?.depth ?? 0;
    for (let j = i - 1; j >= 0; j--) if (rows.value[j]!.depth < d) return j;
    return i;
}
function onKey(ev: KeyboardEvent) {
    const r = rows.value[cursor.value];
    switch (ev.key) {
        case 'ArrowDown':
            moveTo(cursor.value + 1);
            break;
        case 'ArrowUp':
            moveTo(cursor.value - 1);
            break;
        case 'Home':
            moveTo(0);
            break;
        case 'End':
            moveTo(rows.value.length - 1);
            break;
        case 'ArrowRight':
            if (r && r.kind !== 'leaf') {
                if (!r.expanded) toggle(r.key);
                else moveTo(cursor.value + 1);
            }
            break;
        case 'ArrowLeft':
            if (r && r.kind !== 'leaf' && r.expanded && !filter.value) toggle(r.key);
            else moveTo(parentIndex(cursor.value));
            break;
        case 'Enter':
        case ' ':
            if (r) activate(r);
            break;
        default:
            return;
    }
    ev.preventDefault();
}

const activeId = computed(() => (rows.value[cursor.value] ? 'fi-' + cursor.value : undefined));
watch(filter, () => {
    cursor.value = 0;
    void nextTick(() => virt.value.scrollToIndex(0));
});

const live = computed(() => (loading.value ? 'Loading inventory' : summary.value ? `${summary.value.total} inventory entries` : ''));
</script>

<template>
    <div class="fi" data-testid="flux-inventory">
        <div class="hd">
            <span class="mono rev" :title="revision">{{ revision || '—' }}</span>
            <template v-if="summary">
                <span v-if="summary.failing" class="tag danger">{{ summary.failing }} failing</span>
                <span v-if="summary.warning" class="tag warning">{{ summary.warning }} warning</span>
                <span class="tag success">{{ summary.healthy }} healthy</span>
                <span v-if="summary.missing" class="tag warning">{{ summary.missing }} not found</span>
                <span v-if="summary.notWatched" class="tag">{{ summary.notWatched }} not watched</span>
                <span v-if="summary.other" class="tag info">{{ summary.other }} other</span>
            </template>
            <span class="grow"></span>
            <button class="btn sm" type="button" :disabled="loading" @click="load">{{ loading ? 'Refreshing…' : 'Refresh' }}</button>
        </div>
        <div class="bar">
            <input v-model="filter" class="field" type="search" placeholder="Filter kind, namespace, name, status" aria-label="Filter inventory" />
            <button class="btn sm" type="button" @click="expandAll">Expand all</button>
            <button class="btn sm" type="button" @click="collapseAll">Collapse all</button>
        </div>
        <p class="sr-only" role="status" aria-live="polite">{{ live }}</p>
        <div v-if="error" class="notice danger" role="alert">
            Could not load the inventory: {{ error }}
            <button class="btn sm" type="button" @click="load">Retry</button>
        </div>
        <p v-else-if="!tree && loading" class="muted" role="status">Loading inventory…</p>
        <p v-else-if="tree && !tree.summary.total" class="muted">This Kustomization has no inventory entries.</p>
        <p v-else-if="tree && !rows.length" class="muted">No entries match “{{ filter }}”.</p>
        <div
            v-if="tree && rows.length"
            ref="scroller"
            class="scroll"
            data-drawer-scroll
            role="tree"
            tabindex="0"
            aria-label="Kustomization inventory"
            :aria-activedescendant="activeId"
            @keydown="onKey"
        >
            <div class="sizer" :style="{ height: totalSize + 'px' }">
                <div
                    v-for="it in items"
                    :id="'fi-' + it.index"
                    :key="rows[it.index]!.key"
                    class="row"
                    :class="{ cur: it.index === cursor, leaf: rows[it.index]!.kind === 'leaf', open: canOpen(rows[it.index]!) }"
                    role="treeitem"
                    :aria-level="rows[it.index]!.depth + 1"
                    :aria-expanded="rows[it.index]!.kind === 'leaf' ? undefined : !!rows[it.index]!.expanded"
                    :aria-selected="it.index === cursor"
                    :style="{ height: ROW_H + 'px', transform: `translateY(${it.start}px)`, paddingLeft: 8 + rows[it.index]!.depth * 16 + 'px' }"
                    @click="onRowClick(it.index)"
                >
                    <span class="chev" aria-hidden="true">{{ rows[it.index]!.kind === 'leaf' ? '' : rows[it.index]!.expanded ? '▾' : '▸' }}</span>
                    <span class="lbl-t" :class="{ mono: rows[it.index]!.kind === 'leaf' }" :title="rows[it.index]!.label">{{
                        rows[it.index]!.label
                    }}</span>
                    <span class="grow"></span>
                    <span v-if="rows[it.index]!.status" class="tag" :class="toneClass(rows[it.index]!.tone)">{{ rows[it.index]!.status }}</span>
                    <span v-if="rows[it.index]!.kind !== 'leaf'" class="cnt muted">{{ rows[it.index]!.count }}</span>
                </div>
            </div>
        </div>
    </div>
</template>

<style scoped>
.fi {
    display: flex;
    flex-direction: column;
    gap: 8px;
    min-height: 0;
    height: 100%;
}
.hd,
.bar {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 6px;
}
.rev {
    max-width: 260px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--text2);
}
.bar .field {
    flex: 1;
    min-width: 140px;
}
.scroll {
    flex: 1;
    min-height: 160px;
    overflow: auto;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--surface);
}
.sizer {
    position: relative;
    width: 100%;
}
.row {
    position: absolute;
    top: 0;
    left: 0;
    right: 0;
    display: flex;
    align-items: center;
    gap: 6px;
    padding-right: 10px;
    font-size: 12.5px;
    cursor: default;
    box-sizing: border-box;
}
.row.open {
    cursor: pointer;
}
.row:hover {
    background: var(--hover);
}
.row.cur {
    background: var(--accent-soft);
}
.scroll:focus-visible .row.cur {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
}
.row:not(.leaf) {
    font-weight: 600;
}
.chev {
    width: 12px;
    flex: none;
    color: var(--text3);
}
.lbl-t {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
.cnt {
    min-width: 28px;
    text-align: right;
    font-weight: 400;
}
</style>
