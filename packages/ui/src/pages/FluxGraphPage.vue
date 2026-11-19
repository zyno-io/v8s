<script setup lang="ts">
import type { FluxGraphNode, FluxGraphResult } from '@v8s/client';

import { computed, onBeforeUnmount, ref, shallowRef, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';

import FluxGraph from '@/components/FluxGraph.vue';
import SmartSelect from '@/components/SmartSelect.vue';
import { getAgent } from '@/lib/agent.ts';
import { openObject } from '@/lib/drawer.ts';
import { isPhone } from '@/lib/viewport.ts';
import { useClustersStore } from '@/stores/clusters.ts';

const route = useRoute();
const router = useRouter();
const clusters = useClustersStore();

const KIND_GROUPS = [
    { id: 'kustomization', label: 'Kustomization', match: (k: string) => k === 'Kustomization' },
    { id: 'helmrelease', label: 'HelmRelease', match: (k: string) => k === 'HelmRelease' },
    { id: 'sources', label: 'Sources', match: (k: string) => k !== 'Kustomization' && k !== 'HelmRelease' }
];

const cluster = ref(String(route.query.c ?? ''));
const picks = computed(() => clusters.activeIds.map(id => ({ id, name: clusters.nameById.get(id) ?? id })));
const clusterOptions = computed(() => picks.value.map(p => ({ value: p.id, label: p.name })));
watch(
    picks,
    p => {
        if (!p.length) return;
        if (!p.some(x => x.id === cluster.value)) cluster.value = p[0]!.id;
    },
    { immediate: true }
);

const graph = shallowRef<FluxGraphResult | null>(null);
const loading = ref(false);
const error = ref('');
const kindsOn = ref<string[]>(KIND_GROUPS.map(g => g.id));
const failingOnly = ref(false);
let seq = 0;

async function load() {
    if (!cluster.value) return;
    const my = ++seq;
    loading.value = true;
    try {
        const g = await getAgent().call<FluxGraphResult>('flux.graph', { cluster: cluster.value });
        if (my !== seq) return;
        graph.value = { nodes: g.nodes ?? [], edges: g.edges ?? [] };
        error.value = '';
    } catch (e) {
        if (my !== seq) return;
        error.value = e instanceof Error ? e.message : String(e);
    } finally {
        if (my === seq) loading.value = false;
    }
}
watch(
    cluster,
    c => {
        graph.value = null;
        if (String(route.query.c ?? '') !== c) void router.replace({ query: { ...route.query, c } });
        void load();
    },
    { immediate: true }
);
onBeforeUnmount(() => seq++);

const counts = computed(() => {
    const g = graph.value;
    return { total: g?.nodes.length ?? 0, failing: g ? g.nodes.filter(n => n.tone === 3).length : 0 };
});

/** Kind chips first, then (optionally) failing nodes plus their direct neighbours. Edges keep only surviving endpoints. */
const shown = computed<FluxGraphResult | null>(() => {
    const g = graph.value;
    if (!g) return null;
    const on = KIND_GROUPS.filter(k => kindsOn.value.includes(k.id));
    let nodes = g.nodes.filter(n => on.some(k => k.match(n.kind)));
    let ids = new Set(nodes.map(n => n.id));
    let edges = g.edges.filter(e => ids.has(e.from) && ids.has(e.to));
    if (failingOnly.value) {
        const seed = new Set(nodes.filter(n => n.tone === 3).map(n => n.id));
        const near = new Set(seed);
        for (const e of edges) {
            if (seed.has(e.from)) near.add(e.to);
            if (seed.has(e.to)) near.add(e.from);
        }
        nodes = nodes.filter(n => near.has(n.id));
        ids = new Set(nodes.map(n => n.id));
        edges = edges.filter(e => ids.has(e.from) && ids.has(e.to));
    }
    return { nodes, edges };
});

function open(n: FluxGraphNode) {
    void openObject(router, { cluster: cluster.value, gvr: n.gvr, namespace: n.namespace || undefined, name: n.name });
}
</script>

<template>
    <main class="fgp" data-testid="flux-graph-page">
        <header class="head">
            <h1>Flux graph</h1>
            <span class="muted" role="status">
                <template v-if="graph"
                    >{{ counts.total }} objects · {{ counts.failing }} failing<template v-if="shown && shown.nodes.length !== counts.total">
                        · {{ shown.nodes.length }} shown</template
                    ></template
                >
                <template v-else-if="loading">Loading…</template>
            </span>
            <span class="grow"></span>
            <label class="sel">
                <span class="sr-only">Cluster</span>
                <SmartSelect v-model="cluster" :options="clusterOptions" aria-label="Cluster" />
            </label>
            <button class="btn sm" type="button" :disabled="loading || !cluster" @click="load">{{ loading ? 'Refreshing…' : 'Refresh' }}</button>
        </header>

        <div class="filters" role="group" aria-label="Kinds">
            <label v-for="k in KIND_GROUPS" :key="k.id">
                <input v-model="kindsOn" class="chip-input sr-only" type="checkbox" :value="k.id" />
                <span class="chip" :class="{ on: kindsOn.includes(k.id) }">{{ k.label }}</span>
            </label>
            <label>
                <input v-model="failingOnly" class="chip-input sr-only" type="checkbox" />
                <span class="chip" :class="{ on: failingOnly }">Failing only + neighbours</span>
            </label>
        </div>

        <p v-if="isPhone" class="muted hint">Drag to pan, pinch to zoom, or switch to List view.</p>

        <div v-if="!picks.length" class="notice">Connect a cluster to see its Flux objects.</div>
        <div v-else-if="error" class="notice danger" role="alert">
            Could not load the Flux graph: {{ error }}
            <button class="btn sm" type="button" @click="load">Retry</button>
        </div>
        <div v-else-if="graph && !graph.nodes.length" class="notice">No Flux objects found in this cluster.</div>
        <div v-else class="body">
            <FluxGraph v-if="shown" :graph="shown" @open="open" />
            <p v-else class="muted" role="status">Loading the graph…</p>
        </div>
    </main>
</template>

<style scoped>
.fgp {
    flex-grow: 1;
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding: 16px 20px;
    min-height: 0;
}
.head,
.filters {
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
.body {
    flex: 1;
    min-height: 360px;
}
.hint {
    margin: 0;
    font-size: 12px;
}
@media (max-width: 639.98px) {
    .fgp {
        padding: 12px;
    }
    .body {
        min-height: 60vh;
    }
}
</style>
