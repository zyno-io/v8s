<script setup lang="ts">
import type { Kind, ViewHandle, ViewQuery } from '@v8s/client';

import { computed, effectScope, onBeforeUnmount, shallowRef, watch, type EffectScope } from 'vue';
import { useRouter } from 'vue-router';

import { getAgent, kinds } from '@/lib/agent.ts';
import { openObject } from '@/lib/drawer.ts';
import {
    aggregateOverview,
    baseFromColumns,
    cellStr,
    FLUX_KINDS,
    fluxKindOf,
    isStale,
    readCells,
    readyTagOf,
    toneName,
    truncateMessage,
    type FluxKind,
    type FluxTone
} from '@/lib/flux.ts';
import { formatAge, formatCount } from '@/lib/format.ts';
import { gvrPath } from '@/lib/route.ts';
import { useClustersStore } from '@/stores/clusters.ts';
import { buildQuery, useTableStore } from '@/stores/table.ts';

interface KindViews {
    kind: FluxKind;
    gvr: string;
    total: ViewHandle;
    ready: ViewHandle;
    notready: ViewHandle;
    suspended: ViewHandle;
    failing: ViewHandle;
    stale: ViewHandle;
    scope: EffectScope;
}

const router = useRouter();
const clusters = useClustersStore();
const table = useTableStore();
const agent = getAgent();

const FAIL_LIMIT = 15;
const MAX_FAILING = 30;
const DEPLOYMENTS = 'apps/v1/deployments';
const FLUX_NS = 'flux-system';

// Handles are held non-reactively; `tick` is the only reactive signal.
const views = new Map<string, KindViews>();
const tick = shallowRef(0);
const bump = () => tick.value++;

const byLastReconcile = [{ col: 'lastReconcile', desc: false }];
const q = (gvr: string, tones: number[], sort = byLastReconcile): ViewQuery => buildQuery(gvr, '', [], tones, sort);

function open(gvr: string, kind: FluxKind): KindViews {
    const sub = (query: ViewQuery, limit = 1) => agent.subscribeView(query, { offset: 0, limit });
    const total = sub(q(gvr, [], []));
    const suspendedQuery = q(gvr, [], []);
    suspendedQuery.filters = [{ col: 'suspended', op: 'eq', value: 'true' }];
    const v: KindViews = {
        kind,
        gvr,
        total,
        ready: sub(q(gvr, [1], [])),
        notready: sub(q(gvr, [3], [])),
        suspended: sub(suspendedQuery),
        failing: sub(q(gvr, [3]), FAIL_LIMIT),
        stale: sub(q(gvr, [1]), FAIL_LIMIT),
        scope: effectScope()
    };
    v.scope.run(() => {
        watch([total.total, v.ready.total, v.notready.total, v.suspended.total, v.failing.version, v.stale.version], bump);
    });
    return v;
}

function closeViews(v: KindViews) {
    v.scope.stop();
    for (const h of [v.total, v.ready, v.notready, v.suspended, v.failing, v.stale]) h.close();
}

const present = computed(() => {
    const out: { kind: FluxKind; gvr: string }[] = [];
    for (const fk of FLUX_KINDS) {
        const k = kinds.value.find((x: Kind) => fluxKindOf(x.gvr)?.id === fk.id);
        if (k) out.push({ kind: fk, gvr: k.gvr });
    }
    return out;
});

watch(
    present,
    list => {
        const want = new Map(list.map(p => [p.gvr, p.kind]));
        for (const [gvr, v] of views)
            if (!want.has(gvr)) {
                closeViews(v);
                views.delete(gvr);
            }
        for (const [gvr, kind] of want) if (!views.has(gvr)) views.set(gvr, open(gvr, kind));
        bump();
    },
    { immediate: true }
);

// Controllers: the flux-system Deployments, per cluster.
const controllers: ViewHandle = agent.subscribeView(buildQuery(DEPLOYMENTS, '', [FLUX_NS], [], [{ col: 'name', desc: false }]), {
    offset: 0,
    limit: 20
});
const ctrlTick = watch(() => controllers.version.value, bump);

onBeforeUnmount(() => {
    ctrlTick();
    for (const v of views.values()) closeViews(v);
    views.clear();
    controllers.close();
});

const nameOf = (id: string) => clusters.displayName(id);

const overview = computed(() => {
    void tick.value;
    const counts: Record<string, { total: number; ready: number; notready: number; suspended: number }> = {};
    for (const v of views.values())
        counts[v.kind.id] = {
            total: v.total.total.value,
            ready: v.ready.total.value,
            notready: v.notready.total.value,
            suspended: v.suspended.total.value
        };
    return aggregateOverview(counts);
});

const detected = computed(() => present.value.length > 0);
const kindRows = computed(() => present.value.map(p => ({ ...p, c: overview.value.rows[p.kind.id] })).filter(r => r.c));

interface ObjRow {
    key: string;
    cluster: string;
    gvr: string;
    kind: string;
    namespace: string;
    name: string;
    tag: { text: string; tone: FluxTone };
    message: string;
    full: string;
    tone: number;
    last: number;
}

function collect(h: ViewHandle, v: KindViews): ObjRow[] {
    void h.version.value;
    const base = baseFromColumns(h.columns.value);
    const out: ObjRow[] = [];
    for (const id of h.ids) {
        const row = h.rows.get(id);
        if (!row) continue;
        const c = readCells(row, base);
        out.push({
            key: `${v.gvr}|${c.cluster}|${c.namespace}|${c.name}`,
            cluster: c.cluster,
            gvr: v.gvr,
            kind: v.kind.kind,
            namespace: c.namespace,
            name: c.name,
            tag: readyTagOf(c),
            message: truncateMessage(c.message, 110),
            full: c.message,
            tone: c.tone,
            last: c.lastReconcile
        });
    }
    return out;
}

const failing = computed(() => {
    void tick.value;
    const rows: ObjRow[] = [];
    for (const v of views.values()) rows.push(...collect(v.failing, v));
    rows.sort((a, b) => b.tone - a.tone || a.last - b.last);
    return rows.slice(0, MAX_FAILING);
});

const stale = computed(() => {
    void tick.value;
    const now = Date.now();
    const rows: (ObjRow & { interval: string })[] = [];
    for (const v of views.values()) {
        const base = baseFromColumns(v.stale.columns.value);
        void v.stale.version.value;
        for (const id of v.stale.ids) {
            const row = v.stale.rows.get(id);
            if (!row) continue;
            const interval = cellStr(row, base, 'interval');
            const c = readCells(row, base);
            if (!isStale(interval, c.lastReconcile, now)) continue;
            rows.push({
                key: `${v.gvr}|${c.cluster}|${c.namespace}|${c.name}`,
                cluster: c.cluster,
                gvr: v.gvr,
                kind: v.kind.kind,
                namespace: c.namespace,
                name: c.name,
                tag: readyTagOf(c),
                message: '',
                full: '',
                tone: c.tone,
                last: c.lastReconcile,
                interval
            });
        }
    }
    rows.sort((a, b) => a.last - b.last);
    return rows.slice(0, 15);
});

const ago = (ms: number) => formatAge(ms, Date.now());

interface Ctrl {
    key: string;
    cluster: string;
    name: string;
    ready: string;
    tag: { text: string; tone: FluxTone };
}
const controllerGroups = computed(() => {
    void tick.value;
    void controllers.version.value;
    const cols = controllers.columns.value;
    if (!cols) return [];
    const idx = (k: string) => cols.findIndex(c => c.key === k) + 2;
    const iC = idx('cluster');
    const iN = idx('name');
    const iR = idx('ready');
    const iT = idx('statusTone');
    const groups = new Map<string, Ctrl[]>();
    for (const id of controllers.ids) {
        const row = controllers.rows.get(id);
        if (!row) continue;
        const cluster = iC >= 2 ? String(row[iC]) : (clusters.activeIds[0] ?? '');
        const tone = toneName(Number(row[iT]));
        const text = tone === 'success' ? 'Running' : tone === 'danger' ? 'Down' : 'Degraded';
        const c: Ctrl = { key: `${cluster}|${row[iN]}`, cluster, name: String(row[iN]), ready: String(row[iR] ?? ''), tag: { text, tone } };
        const g = groups.get(cluster);
        if (g) g.push(c);
        else groups.set(cluster, [c]);
    }
    return [...groups].map(([cluster, items]) => ({ cluster, items }));
});

function go(gvr: string, tones: number[]) {
    table.clearFilters(gvr);
    table.of(gvr).tones = tones;
    void router.push(gvrPath(gvr));
}

const openRow = (r: ObjRow) => openObject(router, { cluster: r.cluster, gvr: r.gvr, namespace: r.namespace || undefined, name: r.name });
</script>

<template>
    <main class="page flux" data-testid="flux-overview">
        <header class="head">
            <div>
                <h1>Flux</h1>
                <p class="muted" aria-live="polite">
                    <template v-if="detected">
                        {{ formatCount(overview.totals.total) }} objects in {{ clusters.activeIds.length }} cluster{{
                            clusters.activeIds.length === 1 ? '' : 's'
                        }}<template v-if="overview.totals.notready"> · {{ formatCount(overview.totals.notready) }} not ready</template>
                    </template>
                    <template v-else>Flux is not detected in the selected clusters.</template>
                </p>
            </div>
            <router-link v-if="detected" class="btn" to="/flux/graph" data-testid="flux-graph-link">Dependency graph</router-link>
        </header>

        <div v-if="!detected" class="notice" role="status">
            No Flux CRDs were found (<span class="mono">source.toolkit.fluxcd.io</span>, <span class="mono">kustomize.toolkit.fluxcd.io</span>,
            <span class="mono">helm.toolkit.fluxcd.io</span>). Select a cluster that runs Flux.
        </div>

        <template v-else>
            <section class="card" aria-labelledby="flux-kinds">
                <h2 id="flux-kinds">Readiness by kind</h2>
                <table class="grid">
                    <thead>
                        <tr>
                            <th scope="col">Kind</th>
                            <th scope="col">Total</th>
                            <th scope="col">Ready</th>
                            <th scope="col">Not ready</th>
                            <th scope="col">Reconciling / unknown</th>
                            <th scope="col">Suspended</th>
                        </tr>
                    </thead>
                    <tbody>
                        <tr v-for="r in kindRows" :key="r.gvr">
                            <th scope="row">{{ r.kind.kind }}</th>
                            <td data-label="Total">
                                <a :href="'#'" class="cnt" :data-testid="`flux-count-${r.kind.id}-total`" @click.prevent="go(r.gvr, [])">{{
                                    formatCount(r.c!.total)
                                }}</a>
                            </td>
                            <td data-label="Ready">
                                <a href="#" class="cnt ok" :data-testid="`flux-count-${r.kind.id}-ready`" @click.prevent="go(r.gvr, [1])">{{
                                    formatCount(r.c!.ready)
                                }}</a>
                            </td>
                            <td data-label="Not ready">
                                <a
                                    href="#"
                                    class="cnt"
                                    :class="{ bad: r.c!.notready > 0 }"
                                    :data-testid="`flux-count-${r.kind.id}-notready`"
                                    @click.prevent="go(r.gvr, [3])"
                                    >{{ formatCount(r.c!.notready) }}</a
                                >
                            </td>
                            <td data-label="Reconciling / unknown">
                                <a
                                    href="#"
                                    class="cnt"
                                    :class="{ warn: r.c!.other > 0 }"
                                    :data-testid="`flux-count-${r.kind.id}-other`"
                                    @click.prevent="go(r.gvr, [2])"
                                    >{{ formatCount(r.c!.other) }}</a
                                >
                            </td>
                            <td data-label="Suspended">
                                <a href="#" class="cnt" :data-testid="`flux-count-${r.kind.id}-suspended`" @click.prevent="go(r.gvr, [0])">{{
                                    formatCount(r.c!.suspended)
                                }}</a>
                            </td>
                        </tr>
                    </tbody>
                </table>
            </section>

            <section class="card" aria-labelledby="flux-failing">
                <h2 id="flux-failing">Failing first</h2>
                <p v-if="!failing.length" class="muted empty">Nothing is failing.</p>
                <ul v-else class="list">
                    <li v-for="r in failing" :key="r.key">
                        <button
                            type="button"
                            class="item"
                            data-testid="flux-failing-row"
                            :aria-label="`${r.kind} ${r.namespace}/${r.name} on ${nameOf(r.cluster)}: ${r.tag.text}`"
                            @click="openRow(r)"
                        >
                            <span class="tag">{{ nameOf(r.cluster) }}</span>
                            <span class="kind muted">{{ r.kind }}</span>
                            <span class="obj mono">{{ r.namespace ? r.namespace + '/' : '' }}{{ r.name }}</span>
                            <span class="tag" :class="r.tag.tone">{{ r.tag.text }}</span>
                            <span class="msg muted" :title="r.full">{{ r.message }}</span>
                            <span class="age muted">{{ ago(r.last) }}</span>
                        </button>
                    </li>
                </ul>
            </section>

            <section v-if="stale.length" class="card" aria-labelledby="flux-stale">
                <h2 id="flux-stale">Not reconciled in 3&times; interval</h2>
                <ul class="list">
                    <li v-for="r in stale" :key="r.key">
                        <button type="button" class="item" data-testid="flux-stale-row" @click="openRow(r)">
                            <span class="tag">{{ nameOf(r.cluster) }}</span>
                            <span class="kind muted">{{ r.kind }}</span>
                            <span class="obj mono">{{ r.namespace ? r.namespace + '/' : '' }}{{ r.name }}</span>
                            <span class="tag warning">Last {{ ago(r.last) }} ago</span>
                            <span class="msg muted">every {{ r.interval }}</span>
                        </button>
                    </li>
                </ul>
            </section>

            <section v-if="controllerGroups.length" class="card" aria-labelledby="flux-ctrl">
                <h2 id="flux-ctrl">Controllers</h2>
                <div v-for="g in controllerGroups" :key="g.cluster" class="ctrl-group">
                    <h3>{{ nameOf(g.cluster) }}</h3>
                    <ul class="list">
                        <li v-for="c in g.items" :key="c.key" class="ctrl" data-testid="flux-controller">
                            <span class="obj mono">{{ c.name }}</span>
                            <span class="muted mono">{{ c.ready }}</span>
                            <span class="tag" :class="c.tag.tone">{{ c.tag.text }}</span>
                        </li>
                    </ul>
                </div>
            </section>
        </template>
    </main>
</template>

<style scoped>
.flux {
    padding: 20px;
    display: flex;
    flex-direction: column;
    gap: 16px;
    overflow: auto;
    flex-grow: 1;
}
.head {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 12px;
    flex-wrap: wrap;
}
h1 {
    font-size: 19px;
    font-weight: 650;
    letter-spacing: -0.01em;
}
h2 {
    font-size: 13px;
    font-weight: 650;
    padding: 12px 16px 8px;
}
h3 {
    font-size: 12px;
    font-weight: 600;
    padding: 8px 16px 2px;
    color: var(--text-muted, inherit);
}
.card {
    padding-bottom: 8px;
}
.empty {
    padding: 4px 16px 12px;
}
.grid {
    width: 100%;
    border-collapse: collapse;
    font-size: 13px;
}
.grid th,
.grid td {
    padding: 8px 16px;
    text-align: right;
    font-weight: 400;
}
.grid thead th {
    font-size: 12px;
    color: var(--text-muted, inherit);
    font-weight: 500;
}
.grid th:first-child {
    text-align: left;
    font-weight: 550;
}
.cnt {
    display: inline-block;
    min-width: 32px;
    padding: 4px 8px;
    border-radius: 6px;
    color: inherit;
    text-decoration: none;
    font-variant-numeric: tabular-nums;
}
.cnt:hover,
.cnt:focus-visible {
    background: var(--hover, rgba(127, 127, 127, 0.15));
}
.cnt.bad {
    color: var(--danger);
    font-weight: 650;
}
.cnt.warn {
    color: var(--warning);
}
.list {
    list-style: none;
    margin: 0;
    padding: 0;
}
.item,
.ctrl {
    display: grid;
    grid-template-columns: auto 110px minmax(0, 1.2fr) auto minmax(0, 2fr) 44px;
    gap: 10px;
    align-items: center;
    width: 100%;
    padding: 7px 16px;
    font-size: 13px;
    text-align: left;
    background: none;
    border: 0;
    color: inherit;
    cursor: pointer;
}
.ctrl {
    grid-template-columns: minmax(0, 1fr) 60px auto;
    cursor: default;
}
.item:hover,
.item:focus-visible {
    background: var(--hover, rgba(127, 127, 127, 0.12));
}
.obj,
.msg {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
.age {
    text-align: right;
}
@media (pointer: coarse) {
    .item,
    .cnt {
        min-height: 44px;
    }
}
@media (max-width: 639px) {
    .flux {
        padding: 12px;
    }
    .grid thead {
        display: none;
    }
    .grid tr {
        display: grid;
        grid-template-columns: 1fr 1fr;
        padding: 8px 16px;
        gap: 2px 12px;
        border-top: 1px solid var(--border, rgba(127, 127, 127, 0.2));
    }
    .grid th:first-child {
        grid-column: 1 / -1;
        padding: 4px 0;
    }
    .grid td {
        display: flex;
        justify-content: space-between;
        align-items: center;
        padding: 2px 0;
        text-align: left;
    }
    .grid td::before {
        content: attr(data-label);
        font-size: 11px;
        color: var(--text-muted, inherit);
    }
    .item {
        grid-template-columns: auto 1fr auto;
    }
    .kind,
    .age {
        display: none;
    }
    .msg {
        grid-column: 1 / -1;
    }
}
</style>
