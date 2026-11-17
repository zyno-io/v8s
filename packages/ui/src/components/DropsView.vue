<script setup lang="ts">
import type { DropGroup, HubbleDrops } from '@v8s/client';

import { useVirtualizer } from '@tanstack/vue-virtual';
import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue';
import { useRouter } from 'vue-router';

import { getAgent, kinds } from '@/lib/agent.ts';
import { openObject } from '@/lib/drawer.ts';
import { formatAge, formatCount } from '@/lib/format.ts';
import { errorText } from '@/lib/mutations.ts';
import {
    DEFAULT_DROP_WINDOW,
    DROP_WINDOWS,
    DROPS_REFRESH_MS,
    dropKey,
    dropNote,
    filterDrops,
    policyRef,
    sortDrops,
    totalDrops,
    workloadRef,
    workloadText,
    type DropSort
} from '@/lib/network.ts';
import { drawerQuery, gvrPath } from '@/lib/route.ts';
import { isPhone } from '@/lib/viewport.ts';
import { useClustersStore } from '@/stores/clusters.ts';

const router = useRouter();
const clusters = useClustersStore();

const windowSec = ref(DEFAULT_DROP_WINDOW);
const sort = ref<DropSort>('count');
const text = ref('');
const data = shallowRef<HubbleDrops | null>(null);
const loading = ref(false);
const error = ref('');
const selectedKey = ref('');
const now = ref(Date.now());

let seq = 0;
async function load() {
    const my = ++seq;
    loading.value = true;
    try {
        const d = await getAgent().call<HubbleDrops>('hubble.drops', { clusters: [], windowSec: windowSec.value });
        if (my !== seq) return;
        data.value = { windowSec: d.windowSec, groups: d.groups ?? [], errors: d.errors ?? [] };
        error.value = '';
        now.value = Date.now();
    } catch (e) {
        if (my === seq) error.value = errorText(e);
    } finally {
        if (my === seq) loading.value = false;
    }
}
watch(windowSec, () => void load());

// Auto-refresh every 30 s while the document is visible.
let timer: ReturnType<typeof setInterval> | undefined;
function onVisibility() {
    if (!document.hidden) void load();
}
onMounted(() => {
    void load();
    timer = setInterval(() => {
        if (!document.hidden) void load();
    }, DROPS_REFRESH_MS);
    document.addEventListener('visibilitychange', onVisibility);
});
onBeforeUnmount(() => {
    seq++;
    clearInterval(timer);
    document.removeEventListener('visibilitychange', onVisibility);
});

const shown = computed(() => sortDrops(filterDrops(data.value?.groups ?? [], text.value), sort.value));
const selected = computed(() => shown.value.find(g => dropKey(g) === selectedKey.value) ?? null);
const clusterName = (id: string) => clusters.nameById.get(id) ?? id;
const notes = computed(() => (data.value?.errors ?? []).map(e => dropNote(e, clusterName(e.cluster))));

// ---- virtualized rows ----
const scroller = ref<HTMLElement | null>(null);
const rowH = computed(() => (isPhone.value ? 72 : 32));
const virt = useVirtualizer(
    computed(() => ({ count: shown.value.length, getScrollElement: () => scroller.value, estimateSize: () => rowH.value, overscan: 10 }))
);
watch(rowH, () => virt.value.measure());
const rows = computed(() =>
    virt.value
        .getVirtualItems()
        .map(it => ({ top: it.start, g: shown.value[it.index]! }))
        .filter(r => r.g)
);

// ---- detail ----
const panel = ref<HTMLElement | null>(null);
async function select(g: DropGroup) {
    selectedKey.value = dropKey(g);
    await nextTick();
    panel.value?.focus();
}
function closePanel() {
    selectedKey.value = '';
}
function onKey(e: KeyboardEvent) {
    if (e.key === 'Escape' && selected.value) closePanel();
}
const policies = computed(() => {
    const g = selected.value;
    return g ? g.policies.map(p => ({ p, ref: policyRef(g.cluster, p, kinds.value) })) : [];
});
const srcRef = computed(() => (selected.value ? workloadRef(selected.value.cluster, selected.value.src) : null));
function openPolicy(i: number) {
    const r = policies.value[i]?.ref;
    if (r) void openObject(router, r);
}
function openFlows() {
    // openObject cannot carry the extra `fv` param, and FlowsPane reads it when it mounts, so build the same location here.
    const r = srcRef.value;
    if (r) void router.push({ path: gvrPath(r.gvr), query: { ...drawerQuery(r, 'flows'), fv: 'DROPPED' } });
}
const windowLabel = computed(() => DROP_WINDOWS.find(w => w.sec === windowSec.value)?.label ?? `${windowSec.value} s`);
const ago = (ms: number) => formatAge(ms, now.value);
</script>

<template>
    <section class="drops" data-testid="drops-view" @keydown="onKey">
        <header class="head">
            <h1>Network drops</h1>
            <span class="muted" role="status">
                <template v-if="data"
                    >{{ formatCount(totalDrops(data.groups)) }} dropped flows in {{ data.groups.length }} groups, last {{ windowLabel }}</template
                >
                <template v-else-if="loading">Loading…</template>
            </span>
            <span class="grow"></span>
            <label class="sel">
                <span class="sr-only">Window</span>
                <select v-model.number="windowSec" class="field" aria-label="Window" data-testid="drops-window">
                    <option v-for="w in DROP_WINDOWS" :key="w.sec" :value="w.sec">Last {{ w.label }}</option>
                </select>
            </label>
            <label class="sel">
                <span class="sr-only">Sort</span>
                <select v-model="sort" class="field" aria-label="Sort" data-testid="drops-sort">
                    <option value="count">Most drops</option>
                    <option value="lastSeen">Most recent</option>
                </select>
            </label>
            <div class="field find">
                <input v-model="text" type="search" placeholder="Filter" aria-label="Filter drops" data-testid="drops-filter" />
            </div>
            <button type="button" class="btn sm" :disabled="loading" data-testid="drops-refresh" @click="load">
                {{ loading ? 'Refreshing…' : 'Refresh' }}
            </button>
        </header>

        <p v-for="n in notes" :key="n" class="notice" role="status" data-testid="drops-note">{{ n }}</p>
        <p v-if="error" class="notice danger" role="alert">
            Could not load drops: {{ error }} <button type="button" class="btn sm" @click="load">Retry</button>
        </p>

        <div class="body" :class="{ withPanel: !!selected }">
            <div class="tbl">
                <div v-if="!isPhone" class="thead" aria-hidden="true">
                    <span>Cluster</span><span>Source</span><span>Destination</span><span>Reason</span><span class="r">Count</span
                    ><span class="r">Last seen</span><span>Likely policies</span>
                </div>
                <div ref="scroller" class="scroller" role="list" aria-label="Dropped traffic groups" data-testid="drops-list">
                    <div class="sizer" :style="{ height: virt.getTotalSize() + 'px' }">
                        <button
                            v-for="r in rows"
                            :key="dropKey(r.g)"
                            type="button"
                            role="listitem"
                            class="drow"
                            :class="{ card: isPhone, sel: dropKey(r.g) === selectedKey }"
                            :style="{ transform: `translateY(${r.top}px)`, height: rowH + 'px' }"
                            :aria-pressed="dropKey(r.g) === selectedKey"
                            data-testid="drops-row"
                            @click="select(r.g)"
                        >
                            <template v-if="!isPhone">
                                <span class="t">{{ clusterName(r.g.cluster) }}</span>
                                <span class="t mono">{{ workloadText(r.g.src) }}</span>
                                <span class="t mono">{{ workloadText(r.g.dst) }}</span>
                                <span><span class="tag danger">{{ r.g.reason }}</span></span>
                                <span class="r mono">{{ formatCount(r.g.count) }}</span>
                                <span class="r muted">{{ ago(r.g.lastSeen) }}</span>
                                <span class="t chips"
                                    ><span v-for="p in r.g.policies.slice(0, 2)" :key="p.kind + p.name" class="chipx">{{ p.name }}</span
                                    ><span v-if="r.g.policies.length > 2" class="muted">+{{ r.g.policies.length - 2 }}</span></span
                                >
                            </template>
                            <template v-else>
                                <span class="l1"
                                    ><span class="tag danger">{{ r.g.reason }}</span><span class="mono">{{ formatCount(r.g.count) }}</span
                                    ><span class="muted">{{ ago(r.g.lastSeen) }} ago</span></span
                                >
                                <span class="t mono">{{ workloadText(r.g.src) }} &rarr; {{ workloadText(r.g.dst) }}</span>
                                <span class="t muted">{{ clusterName(r.g.cluster) }}</span>
                            </template>
                        </button>
                    </div>
                    <p v-if="data && !shown.length" class="muted empty" data-testid="drops-empty">
                        {{ data.groups.length ? 'No groups match the filter.' : `No dropped flows in the last ${windowLabel}.` }}
                    </p>
                </div>
            </div>

            <aside v-if="selected" ref="panel" class="panel" tabindex="-1" role="complementary" aria-label="Drop details" data-testid="drops-panel">
                <div class="ph">
                    <h2>Drop details</h2>
                    <button type="button" class="btn sm ghost" aria-label="Close details" @click="closePanel">Close</button>
                </div>
                <dl>
                    <dt>Cluster</dt>
                    <dd>{{ clusterName(selected.cluster) }}</dd>
                    <dt>Source</dt>
                    <dd class="mono">{{ workloadText(selected.src) }}</dd>
                    <dt>Destination</dt>
                    <dd class="mono">{{ workloadText(selected.dst) }}</dd>
                    <dt>Reason</dt>
                    <dd><span class="tag danger">{{ selected.reason }}</span></dd>
                    <dt>Dropped flows</dt>
                    <dd>{{ formatCount(selected.count) }} in the last {{ windowLabel }}</dd>
                    <dt>Last seen</dt>
                    <dd>{{ ago(selected.lastSeen) }} ago</dd>
                </dl>
                <h3>Likely policies</h3>
                <p v-if="!policies.length" class="muted">No matching policy found (default deny or an unmanaged rule).</p>
                <ul v-else class="pols">
                    <li v-for="(x, i) in policies" :key="x.p.kind + x.p.namespace + x.p.name">
                        <button
                            type="button"
                            class="btn sm"
                            :disabled="!x.ref"
                            :title="x.ref ? 'Open the policy' : `${x.p.kind} is not served by this cluster`"
                            @click="openPolicy(i)"
                        >
                            {{ x.p.name }}
                        </button>
                        <span class="muted">{{ x.p.kind }}{{ x.p.namespace ? ' in ' + x.p.namespace : '' }}</span>
                    </li>
                </ul>
                <div class="acts">
                    <button type="button" class="btn sm primary" :disabled="!srcRef" data-testid="drops-open-flows" @click="openFlows">
                        Open flows
                    </button>
                </div>
            </aside>
        </div>
    </section>
</template>

<style scoped>
.drops {
    flex-grow: 1;
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding: 16px 20px;
    min-height: 0;
}
.head {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
}
h1 {
    margin: 0;
    font-size: 19px;
    font-weight: 650;
    letter-spacing: -0.01em;
}
.grow {
    flex: 1;
}
.find {
    height: 30px;
    min-width: 160px;
}
.body {
    flex: 1;
    min-height: 360px;
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    gap: 12px;
}
.body.withPanel {
    grid-template-columns: minmax(0, 1fr) 340px;
}
.tbl {
    display: flex;
    flex-direction: column;
    min-height: 0;
    border: 1px solid var(--border);
    border-radius: 10px;
    background: var(--surface);
    overflow: hidden;
}
.thead,
.drow:not(.card) {
    display: grid;
    grid-template-columns: 110px minmax(120px, 1.2fr) minmax(120px, 1.2fr) 130px 70px 80px minmax(120px, 1fr);
    align-items: center;
    column-gap: 10px;
    padding: 0 12px;
}
.thead {
    height: 28px;
    flex: none;
    font-size: 11px;
    font-weight: 600;
    color: var(--text3);
    background: var(--s2);
    border-bottom: 1px solid var(--border);
}
.r {
    text-align: right;
}
.scroller {
    flex: 1;
    min-height: 0;
    overflow: auto;
    contain: strict;
}
.sizer {
    position: relative;
}
.drow {
    position: absolute;
    top: 0;
    left: 0;
    width: 100%;
    box-sizing: border-box;
    border: 0;
    border-bottom: 1px solid var(--border);
    background: none;
    color: var(--text);
    font: inherit;
    font-size: 12.5px;
    text-align: left;
    cursor: pointer;
    contain: layout paint;
}
.drow:hover {
    background: var(--s2);
}
.drow.sel {
    background: var(--accent-soft);
}
.drow:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
}
.drow > span {
    min-width: 0;
}
.t {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
.chips {
    display: flex;
    gap: 4px;
    align-items: center;
}
.chipx {
    padding: 1px 6px;
    border: 1px solid var(--border);
    border-radius: 999px;
    font-size: 11px;
    max-width: 110px;
    overflow: hidden;
    text-overflow: ellipsis;
}
.drow.card {
    display: flex;
    flex-direction: column;
    justify-content: center;
    gap: 2px;
    padding: 4px 12px;
}
.l1 {
    display: flex;
    align-items: center;
    gap: 8px;
}
.empty {
    padding: 16px;
    margin: 0;
}
.panel {
    border: 1px solid var(--border);
    border-radius: 10px;
    background: var(--surface);
    padding: 12px 14px;
    overflow: auto;
    outline: none;
}
.ph {
    display: flex;
    justify-content: space-between;
    align-items: center;
}
.panel h2,
.panel h3 {
    margin: 0;
    font-size: 14px;
}
.panel h3 {
    margin-top: 12px;
    font-size: 12px;
    color: var(--text3);
}
dl {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 4px 12px;
    font-size: 12.5px;
}
dt {
    color: var(--text3);
}
dd {
    margin: 0;
    overflow-wrap: anywhere;
}
.pols {
    list-style: none;
    padding: 0;
    margin: 6px 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
}
.pols li {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
}
.acts {
    margin-top: 12px;
}
@media (max-width: 899.98px) {
    .body.withPanel {
        grid-template-columns: minmax(0, 1fr);
        grid-template-rows: minmax(0, 1fr) auto;
    }
}
@media (max-width: 639.98px) {
    .drops {
        padding: 12px;
    }
    .panel {
        position: fixed;
        inset: auto 0 64px 0;
        max-height: 55dvh;
        z-index: 20;
        border-radius: 14px 14px 0 0;
        box-shadow: var(--float);
    }
}
@media (pointer: coarse) {
    .btn.sm {
        min-height: 44px;
    }
}
</style>
