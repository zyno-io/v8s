<script setup lang="ts">
import type { HubbleServiceMap } from '@v8s/client';

import { computed, defineAsyncComponent, onBeforeUnmount, ref, shallowRef, watch } from 'vue';
import { useRouter } from 'vue-router';

import SmartSelect from '@/components/SmartSelect.vue';
import { getAgent, getNamespacesView } from '@/lib/agent.ts';
import { openObject } from '@/lib/drawer.ts';
import { formatCount } from '@/lib/format.ts';
import { errorText } from '@/lib/mutations.ts';
import { clusterNamespaces, DEFAULT_MAP_WINDOW, MAP_WINDOWS, workloadRef } from '@/lib/network.ts';
import { drawerQuery, gvrPath } from '@/lib/route.ts';
import { classifyEdge, edgeKey, edgeLabel, edgeWord, isOpenable, nodeEdges, nodeLabel, sortEdges, summarize } from '@/lib/servicemap.ts';
import { listOptions } from '@/lib/smart-select.ts';
import { useClustersStore } from '@/stores/clusters.ts';
import { useIntegrationsStore } from '@/stores/integrations.ts';

// The canvas and its layout client load only when the map opens.
const ServiceMapGraph = defineAsyncComponent(() => import('./ServiceMapGraph.vue'));

const router = useRouter();
const clusters = useClustersStore();
const integrations = useIntegrationsStore();

const cluster = ref('');
const namespace = ref('');
const windowSec = ref(DEFAULT_MAP_WINDOW);
const picks = computed(() => integrations.hubbleClusters.map(id => ({ id, name: clusters.displayName(id) })));
watch(
    picks,
    p => {
        if (p.length && !p.some(x => x.id === cluster.value)) cluster.value = p[0]!.id;
    },
    { immediate: true }
);

const namespaces = shallowRef<string[]>([]);
function refreshNamespaces() {
    try {
        namespaces.value = clusterNamespaces(getNamespacesView(), cluster.value);
    } catch {
        namespaces.value = [];
    }
    if (!namespaces.value.includes(namespace.value)) namespace.value = namespaces.value.includes('default') ? 'default' : (namespaces.value[0] ?? '');
}
watch(cluster, refreshNamespaces, { immediate: true });
// The namespaces view fills in after connect and changes with the cluster; follow it (cheap: a few dozen rows per cluster).
watch(() => {
    try {
        return getNamespacesView().version.value;
    } catch {
        return 0;
    }
}, refreshNamespaces);

const data = shallowRef<(Pick<HubbleServiceMap, 'nodes' | 'edges'> & { truncated?: boolean; windowSec: number }) | null>(null);
const loading = ref(false);
const error = ref('');
const selectedId = ref<string | null>(null);
let seq = 0;

async function load() {
    if (!cluster.value || !namespace.value) return;
    const my = ++seq;
    loading.value = true;
    try {
        const d = await getAgent().call<HubbleServiceMap>('hubble.serviceMap', {
            cluster: cluster.value,
            namespace: namespace.value,
            windowSec: windowSec.value
        });
        if (my !== seq) return;
        data.value = { nodes: d.nodes ?? [], edges: d.edges ?? [], truncated: d.truncated, windowSec: d.windowSec };
        error.value = '';
        if (selectedId.value && !data.value.nodes.some(n => n.id === selectedId.value)) selectedId.value = null;
    } catch (e) {
        if (my === seq) error.value = errorText(e);
    } finally {
        if (my === seq) loading.value = false;
    }
}
watch([cluster, namespace, windowSec], () => {
    data.value = null;
    selectedId.value = null;
    void load();
});
if (namespace.value) void load();
onBeforeUnmount(() => seq++);

const nodeById = computed(() => new Map((data.value?.nodes ?? []).map(n => [n.id, n])));
const edges = computed(() => sortEdges(data.value?.edges ?? []));
const totals = computed(() => summarize(edges.value));
const selected = computed(() => (selectedId.value ? (nodeById.value.get(selectedId.value) ?? null) : null));
const selectedEdges = computed(() => (selectedId.value ? nodeEdges(selectedId.value, edges.value) : []));
const nameOf = (id: string) => {
    const n = nodeById.value.get(id);
    return n ? (n.namespace && n.namespace !== namespace.value ? `${n.namespace}/${nodeLabel(n)}` : nodeLabel(n)) : id;
};
const wref = computed(() => (selected.value && isOpenable(selected.value) ? workloadRef(cluster.value, selected.value) : null));
const toggle = (id: string) => (selectedId.value = selectedId.value === id ? null : id);

function openWorkload() {
    if (wref.value) void openObject(router, wref.value);
}
function openFlows() {
    const r = wref.value;
    if (r) void router.push({ path: gvrPath(r.gvr), query: drawerQuery(r, 'flows') });
}
const clusterOptions = computed(() => picks.value.map(p => ({ value: p.id, label: p.name })));
const namespaceOptions = computed(() => listOptions(namespaces.value));
const windowOptions = MAP_WINDOWS.map(w => ({ value: w.sec, label: `Last ${w.label}` }));
const windowLabel = computed(() => MAP_WINDOWS.find(w => w.sec === windowSec.value)?.label ?? `${windowSec.value} s`);
</script>

<template>
    <section class="smap" data-testid="servicemap-view">
        <header class="head">
            <h1>Service map</h1>
            <span class="muted" role="status">
                <template v-if="data"
                    >{{ data.nodes.length }} workloads, {{ data.edges.length }} edges, {{ formatCount(totals.requests) }} requests, last
                    {{ windowLabel }}</template
                >
                <template v-else-if="loading">Loading…</template>
            </span>
            <span class="grow"></span>
            <label class="sel">
                <span class="sr-only">Cluster</span>
                <SmartSelect v-model="cluster" :options="clusterOptions" aria-label="Cluster" data-testid="map-cluster" />
            </label>
            <label class="sel">
                <span class="sr-only">Namespace</span>
                <SmartSelect v-model="namespace" :options="namespaceOptions" aria-label="Namespace" data-testid="map-namespace" />
            </label>
            <label class="sel">
                <span class="sr-only">Window</span>
                <SmartSelect v-model="windowSec" :options="windowOptions" aria-label="Window" data-testid="map-window" />
            </label>
            <button type="button" class="btn sm" :disabled="loading || !namespace" data-testid="map-refresh" @click="load">
                {{ loading ? 'Refreshing…' : 'Refresh' }}
            </button>
        </header>

        <p v-if="data?.truncated" class="notice" role="status" data-testid="map-truncated">
            More flows matched than were read, so the counters are lower bounds. Pick a shorter window or a smaller namespace.
        </p>
        <p v-if="error" class="notice danger" role="alert">
            Could not load the service map: {{ error }} <button type="button" class="btn sm" @click="load">Retry</button>
        </p>
        <div v-if="!picks.length" class="notice">No cluster with a reachable Hubble relay.</div>
        <div v-else-if="!namespace" class="notice">Pick a namespace to draw its service map.</div>

        <div v-if="picks.length && namespace" class="body">
            <div class="canvas">
                <ServiceMapGraph v-if="data" :graph="data" :selected-id="selectedId" @select="selectedId = $event" />
                <p v-else class="muted pad" role="status">{{ loading ? 'Loading the map…' : '' }}</p>
            </div>
            <aside v-if="selected" class="panel" role="complementary" aria-label="Workload details" data-testid="map-panel">
                <div class="ph">
                    <h2>{{ nodeLabel(selected) }}</h2>
                    <button type="button" class="btn sm ghost" @click="selectedId = null">Close</button>
                </div>
                <p class="muted">
                    {{ selected.namespace || 'reserved identity' }}<template v-if="selected.external"> · external to {{ namespace }}</template>
                </p>
                <div class="acts">
                    <button type="button" class="btn sm primary" :disabled="!wref" data-testid="map-open-flows" @click="openFlows">Open flows</button>
                    <button type="button" class="btn sm" :disabled="!wref" data-testid="map-open-workload" @click="openWorkload">
                        Open workload
                    </button>
                </div>
                <h3>Edges</h3>
                <ul class="elist">
                    <li v-for="e in selectedEdges" :key="edgeKey(e)">
                        <span class="mono">{{ e.from === selected.id ? 'to' : 'from' }} {{ nameOf(e.from === selected.id ? e.to : e.from) }}</span>
                        <span :class="{ bad: classifyEdge(e) !== 'ok' }">{{ edgeLabel(e) }}</span>
                    </li>
                </ul>
            </aside>
        </div>

        <section v-if="data && picks.length" class="listwrap" aria-label="Edges">
            <h2>Edges <span class="muted">sorted by drops, errors, requests</span></h2>
            <div class="scroll" tabindex="0" role="region" aria-label="Edges table">
                <table data-testid="map-edges">
                    <thead>
                        <tr>
                            <th scope="col">From</th>
                            <th scope="col">To</th>
                            <th scope="col" class="r">Requests</th>
                            <th scope="col" class="r">Errors</th>
                            <th scope="col" class="r">Drops</th>
                            <th scope="col">Status</th>
                        </tr>
                    </thead>
                    <tbody>
                        <tr v-for="e in edges.slice(0, 500)" :key="edgeKey(e)" :class="{ sel: selectedId === e.from || selectedId === e.to }">
                            <td>
                                <button type="button" class="btn sm ghost mono" @click="toggle(e.from)">{{ nameOf(e.from) }}</button>
                            </td>
                            <td>
                                <button type="button" class="btn sm ghost mono" @click="toggle(e.to)">{{ nameOf(e.to) }}</button>
                            </td>
                            <td class="r mono">{{ formatCount(e.requests) }}</td>
                            <td class="r mono">{{ formatCount(e.errors) }}</td>
                            <td class="r mono">{{ formatCount(e.drops) }}</td>
                            <td>
                                <span class="tag" :class="classifyEdge(e) === 'ok' ? 'success' : 'danger'">{{ edgeWord(classifyEdge(e)) }}</span>
                            </td>
                        </tr>
                    </tbody>
                </table>
                <p v-if="!edges.length" class="muted pad">No traffic in this window.</p>
            </div>
        </section>
    </section>
</template>

<style scoped>
.smap {
    flex-grow: 1;
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding: 16px 20px;
    min-height: 0;
    overflow: auto;
}
.head {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
}
.grow {
    flex: 1;
}
h1 {
    margin: 0;
    font-size: 19px;
    font-weight: 650;
    letter-spacing: -0.01em;
}
h2 {
    margin: 0;
    font-size: 14px;
}
h3 {
    margin: 12px 0 4px;
    font-size: 12px;
    color: var(--text3);
}
.body {
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    gap: 12px;
    height: 58vh;
    min-height: 360px;
    flex: none;
}
.body:has(.panel) {
    grid-template-columns: minmax(0, 1fr) 300px;
}
.canvas {
    min-height: 0;
    min-width: 0;
}
.pad {
    padding: 12px;
}
.panel {
    border: 1px solid var(--border);
    border-radius: 10px;
    background: var(--surface);
    padding: 12px 14px;
    overflow: auto;
}
.ph {
    display: flex;
    justify-content: space-between;
    align-items: center;
}
.acts {
    display: flex;
    gap: 6px;
    flex-wrap: wrap;
}
.elist {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
    font-size: 12px;
}
.elist li {
    display: flex;
    flex-direction: column;
}
.bad {
    color: var(--danger);
}
.listwrap {
    display: flex;
    flex-direction: column;
    gap: 6px;
}
.scroll {
    max-height: 320px;
    overflow: auto;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--surface);
}
.scroll:focus-visible {
    outline: 2px solid var(--focus);
}
table {
    border-collapse: collapse;
    width: 100%;
    font-size: 12.5px;
}
th,
td {
    padding: 0 10px;
    height: 32px;
    text-align: left;
    border-bottom: 1px solid var(--border);
}
th {
    position: sticky;
    top: 0;
    background: var(--s2);
    font-size: 11px;
    color: var(--text3);
}
.r {
    text-align: right;
}
tr.sel {
    background: var(--accent-soft);
}
@media (pointer: coarse) {
    .btn.sm {
        min-height: 44px;
    }
}
</style>
