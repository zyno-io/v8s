<script setup lang="ts">
import type { ViewHandle } from '@v8s/client';

import { CapacityGVR } from '@v8s/client';
import { computed, onBeforeUnmount, watch } from 'vue';
import { useRouter } from 'vue-router';

import ClusterElevateMenu from '@/components/ClusterElevateMenu.vue';
import { getAgent, kinds } from '@/lib/agent.ts';
import { capacityRow, capacityView } from '@/lib/capacity.ts';
import {
    countQuery,
    healthKinds,
    integrationsOf,
    NODES_GVR,
    phaseLabel,
    PODS_GVR,
    podOther,
    problemsQuery,
    SEVERITIES,
    type Severity
} from '@/lib/cluster-overview.ts';
import { clusterColor, phaseColor } from '@/lib/colors.ts';
import { formatCount } from '@/lib/format.ts';
import { gvrPath } from '@/lib/route.ts';
import { usePageVisible } from '@/lib/viewport.ts';
import { useClustersStore } from '@/stores/clusters.ts';
import { useIntegrationsStore } from '@/stores/integrations.ts';
import { useTableStore } from '@/stores/table.ts';

const props = defineProps<{ id: string }>();

const router = useRouter();
const clusters = useClustersStore();
const table = useTableStore();
const integrations = useIntegrationsStore();
const agent = getAgent();

// One-row count views (nodes, pods and problems are tier-0 data the agent already holds). Handles are non-reactive; their
// `total` shallowRefs drive the computed values below.
const open: ViewHandle[] = [];
const sub = (q: ReturnType<typeof countQuery>, limit = 0): ViewHandle => {
    const h = agent.subscribeView(q, { offset: 0, limit: limit || 1 });
    open.push(h);
    return h;
};

const nodes = sub(countQuery(props.id, NODES_GVR), 1);
const nodesReady = sub(countQuery(props.id, NODES_GVR, [1]));
const pods = {
    total: sub(countQuery(props.id, PODS_GVR)),
    running: sub(countQuery(props.id, PODS_GVR, [1])),
    failing: sub(countQuery(props.id, PODS_GVR, [3])),
    pending: sub(countQuery(props.id, PODS_GVR, [2])),
    completed: sub(countQuery(props.id, PODS_GVR, [4]))
};

// The capacity row (CPU and memory totals of the whole cluster) is computed by the agent. `usage` drives its node-metrics poll, so
// it is on only while this page is visible (a tab hidden for 30 s stops polling); requests and limits need no metrics-server.
const pageVisible = usePageVisible(30_000);
const capacity = agent.subscribeView(
    { clusters: [props.id], gvr: CapacityGVR, namespaces: [], text: '', filters: [], sort: [] },
    { offset: 0, limit: 1 },
    { usage: pageVisible.value }
);
open.push(capacity);
watch(pageVisible, v => capacity.update({ usage: v }));
const capRow = computed(() => {
    void capacity.version.value;
    const id = capacity.ids[0];
    return capacityRow(capacity.columns.value, id === undefined ? undefined : capacity.rows.get(id));
});
const cpuCap = computed(() => capacityView('cpu', capRow.value, state.value?.metrics));
const memCap = computed(() => capacityView('mem', capRow.value, state.value?.metrics));
const problems = Object.fromEntries(SEVERITIES.map(s => [s, sub(problemsQuery(props.id, s))])) as Record<Severity, ViewHandle>;

interface Health {
    label: string;
    gvr: string;
    total: ViewHandle;
    failing: ViewHandle;
}
const health = computed(() => healthKinds(kinds.value, props.id));
const healthViews = new Map<string, Health>();
const healthList = computed<Health[]>(() => {
    const h = health.value;
    const want: [string, string][] = [
        [h.kustomizations, 'Flux Kustomizations'],
        [h.helmReleases, 'Flux HelmReleases'],
        [h.helmSecrets, 'Helm releases']
    ];
    const out: Health[] = [];
    for (const [gvr, label] of want) {
        if (!gvr) continue;
        let v = healthViews.get(gvr);
        if (!v) {
            v = { label, gvr, total: sub(countQuery(props.id, gvr)), failing: sub(countQuery(props.id, gvr, [3])) };
            healthViews.set(gvr, v);
        }
        out.push(v);
    }
    return out;
});

onBeforeUnmount(() => {
    for (const h of open) h.close();
});

const state = computed(() => clusters.stateById.get(props.id));
const name = computed(() => clusters.nameById.get(props.id) ?? props.id);
const phase = computed(() => state.value?.phase ?? 'connecting');
const version = computed(() => {
    // The first node's kubelet version: columns carry `version`; read it after the version ref moves.
    void nodes.version.value;
    const idx = (nodes.columns.value ?? []).findIndex(c => c.key === 'version');
    const id = nodes.ids[0];
    const row = id ? nodes.rows.get(id) : undefined;
    return idx >= 0 && row ? String(row[idx + 2] ?? '') : '';
});
const podCounts = computed(() => ({
    total: pods.total.total.value,
    running: pods.running.total.value,
    failing: pods.failing.total.value,
    pending: pods.pending.total.value,
    completed: pods.completed.total.value
}));
const problemTotal = computed(() => SEVERITIES.reduce((n, s) => n + problems[s].total.value, 0));
const found = computed(() => integrationsOf(kinds.value, props.id));
const detected = computed(() => {
    const out: string[] = [];
    const f = found.value;
    if (f.kyverno) out.push('Kyverno');
    if (f.trivy) out.push('Trivy');
    if (f.kubescape) out.push('Kubescape');
    if (integrations.hubbleClusters.includes(props.id)) out.push('Hubble');
    if (health.value.kustomizations || health.value.helmReleases) out.push('Flux');
    if (health.value.helmSecrets) out.push('Helm');
    return out;
});

const capCells = computed(() => [
    { key: 'cpu', label: 'CPU', v: cpuCap.value },
    { key: 'mem', label: 'Memory', v: memCap.value }
]);

function goPods(tones: number[]) {
    table.clearFilters(PODS_GVR);
    table.of(PODS_GVR).tones = tones;
    void router.push('/pods');
}
function goHealth(h: Health, tones: number[]) {
    table.clearFilters(h.gvr);
    table.of(h.gvr).tones = tones;
    void router.push(gvrPath(h.gvr));
}
</script>

<template>
    <section class="card cov" :aria-label="`Cluster ${name}`" :data-testid="`overview-${id}`">
        <header class="hd">
            <span class="dot" :style="{ background: clusterColor(id) }"></span>
            <h2>{{ name }}</h2>
            <span class="tag" :class="phase === 'ready' ? 'success' : phase === 'unreachable' || phase === 'auth-required' ? 'danger' : 'warning'">
                <span class="dot" :style="{ background: phaseColor(phase) }"></span>{{ phaseLabel(phase) }}
            </span>
            <span v-if="clusters.isElevated(id)" class="tag warning" :title="'Running as ' + clusters.writeContextOf(id)" data-testid="elevated-tag"
                >elevated</span
            >
            <ClusterElevateMenu :id="id" />
            <span v-if="version" class="muted mono">{{ version }}</span>
            <span v-if="state?.message" class="muted msg" :title="state.message">{{ state.message }}</span>
        </header>
        <div class="grid">
            <div class="cell">
                <div class="lbl">Nodes</div>
                <div class="big">
                    <router-link :to="gvrPath(NODES_GVR)">{{ formatCount(nodesReady.total.value) }}</router-link
                    ><span class="muted"> / {{ formatCount(nodes.total.value) }} ready</span>
                </div>
            </div>
            <div class="cell">
                <div class="lbl">Pods</div>
                <div class="big">
                    <a href="#" @click.prevent="goPods([])">{{ formatCount(podCounts.total) }}</a>
                </div>
                <div class="chips">
                    <a href="#" class="tag success" @click.prevent="goPods([1])">{{ formatCount(podCounts.running) }} running</a>
                    <a v-if="podCounts.failing" href="#" class="tag danger" @click.prevent="goPods([3])"
                        >{{ formatCount(podCounts.failing) }} failing</a
                    >
                    <a v-if="podCounts.pending" href="#" class="tag warning" @click.prevent="goPods([2])"
                        >{{ formatCount(podCounts.pending) }} pending</a
                    >
                    <a v-if="podCounts.completed" href="#" class="tag info" @click.prevent="goPods([4])"
                        >{{ formatCount(podCounts.completed) }} completed</a
                    >
                    <span v-if="podOther(podCounts)" class="tag">{{ formatCount(podOther(podCounts)) }} other</span>
                </div>
            </div>
            <div class="cell">
                <div class="lbl">Problems</div>
                <div class="big">
                    <router-link :to="{ path: '/problems', query: { cluster: id } }" :class="{ ok: !problemTotal }">{{
                        problemTotal ? formatCount(problemTotal) : 'None'
                    }}</router-link>
                </div>
                <div class="chips">
                    <span
                        v-for="s in SEVERITIES"
                        v-show="problems[s].total.value"
                        :key="s"
                        class="tag"
                        :class="s === 'warning' ? 'warning' : s === 'info' ? 'info' : 'danger'"
                    >
                        {{ formatCount(problems[s].total.value) }} {{ s }}
                    </span>
                </div>
            </div>
            <div class="cell">
                <div class="lbl">GitOps health</div>
                <p v-if="!healthList.length" class="muted none">No Flux or Helm detected.</p>
                <ul v-else class="hl">
                    <li v-for="h in healthList" :key="h.gvr">
                        <a href="#" @click.prevent="goHealth(h, [])">{{ h.label }}</a>
                        <span class="muted">{{ formatCount(h.total.total.value) }}</span>
                        <a v-if="h.failing.total.value" href="#" class="tag danger" @click.prevent="goHealth(h, [3])"
                            >{{ formatCount(h.failing.total.value) }} failing</a
                        >
                        <span v-else class="tag success">healthy</span>
                    </li>
                </ul>
            </div>
            <div class="cell">
                <div class="lbl">Integrations</div>
                <div class="chips">
                    <span v-for="d in detected" :key="d" class="tag">{{ d }}</span>
                    <span v-if="!detected.length" class="muted none">None detected.</span>
                </div>
            </div>
        </div>
        <div class="grid cap" :data-testid="`capacity-${id}`">
            <div v-for="r in capCells" :key="r.key" class="cell" :title="r.v.title" :data-testid="`cap-${r.key}`">
                <div class="lbl">{{ r.label }}</div>
                <div class="big" :class="r.v.tone">
                    {{ r.v.pct }}<template v-if="r.v.ok">%</template
                    ><span v-if="r.v.ok" class="muted use">{{ r.v.usage ? 'used' : 'requested' }}</span>
                </div>
                <div
                    class="bar"
                    role="img"
                    :aria-label="r.v.ok ? `${r.label} ${r.v.pct}% ${r.v.usage ? 'used' : 'requested'}` : `${r.label} unknown`"
                >
                    <i v-if="r.v.ok" :class="r.v.tone" :style="{ width: r.v.fill + '%' }"></i>
                    <b v-if="r.v.ok" :style="{ left: r.v.tick + '%' }"></b>
                </div>
                <template v-if="r.v.ok">
                    <div class="sub">{{ r.v.share }}</div>
                    <div class="sub muted">{{ r.v.amount }}</div>
                    <div v-if="r.v.note" class="sub muted">{{ r.v.note }}</div>
                </template>
            </div>
        </div>
    </section>
</template>

<style scoped>
.cov {
    padding: 0;
}
.hd {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 12px 16px;
    border-bottom: 1px solid var(--border);
    min-width: 0;
}
h2 {
    margin: 0;
    font-size: 15px;
    font-weight: 650;
}
.msg {
    font-size: 12px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
.grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
    gap: 0;
}
.cell {
    padding: 12px 16px;
    display: flex;
    flex-direction: column;
    gap: 6px;
    min-width: 0;
}
.lbl {
    font-size: 11px;
    font-weight: 600;
    letter-spacing: 0.045em;
    text-transform: uppercase;
    color: var(--text3);
}
.big {
    font-size: 22px;
    font-weight: 650;
}
.big a {
    color: var(--text);
    text-decoration: none;
}
.big a:hover,
.big a:focus-visible {
    text-decoration: underline;
}
.big .ok {
    color: var(--success);
}
.big .muted {
    font-size: 13px;
    font-weight: 400;
}
.chips {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
}
.chips a {
    text-decoration: none;
}
.hl {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 4px;
    font-size: 13px;
}
.hl li {
    display: flex;
    gap: 8px;
    align-items: center;
}
.cap {
    grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
    border-top: 1px solid var(--border);
}
.cap .big {
    font-variant-numeric: tabular-nums;
}
.cap .big.warning {
    color: var(--warning);
}
.cap .big.danger {
    color: var(--danger);
}
.cap .use {
    margin-left: 6px;
}
.bar {
    position: relative;
    height: 6px;
    border-radius: 3px;
    background: var(--border);
    margin: 2px 0 4px;
}
.bar i {
    position: absolute;
    inset: 0 auto 0 0;
    border-radius: 3px;
    background: var(--text2);
}
.bar i.warning {
    background: var(--warning);
}
.bar i.danger {
    background: var(--danger);
}
.bar b {
    position: absolute;
    top: -2px;
    bottom: -2px;
    width: 2px;
    margin-left: -1px;
    border-radius: 1px;
    background: var(--text);
}
.sub {
    font-size: 12px;
    font-variant-numeric: tabular-nums;
}
.none {
    margin: 0;
    font-size: 13px;
}
</style>
