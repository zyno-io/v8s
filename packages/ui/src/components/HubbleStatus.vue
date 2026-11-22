<script setup lang="ts">
import type { HubbleStatus } from '@v8s/client';

import { computed, onBeforeUnmount, onMounted, ref, shallowRef } from 'vue';

import { getAgent } from '@/lib/agent.ts';
import { formatCount } from '@/lib/format.ts';
import { errorText } from '@/lib/mutations.ts';
import { fillPct, formatUptime, hintsTitle, nodeTone, statusHints } from '@/lib/network.ts';
import { useClustersStore } from '@/stores/clusters.ts';
import { useIntegrationsStore } from '@/stores/integrations.ts';

const clusters = useClustersStore();
const integrations = useIntegrationsStore();

interface Entry {
    id: string;
    status: HubbleStatus | null;
    error: string;
}
const entries = shallowRef<Entry[]>([]);
const loading = ref(false);
let seq = 0;

async function load() {
    const my = ++seq;
    loading.value = true;
    const ids = clusters.activeIds;
    const out = await Promise.all(
        ids.map(async (id): Promise<Entry> => {
            try {
                return { id, status: await getAgent().call<HubbleStatus>('hubble.status', { cluster: id }), error: '' };
            } catch (e) {
                return { id, status: null, error: errorText(e) };
            }
        })
    );
    if (my !== seq) return;
    entries.value = out;
    loading.value = false;
    void integrations.refreshHubble(true);
}
onMounted(() => void load());
onBeforeUnmount(() => seq++);

const name = (id: string) => clusters.displayName(id);
const NODE_CAP = 400;
const nodesOf = (s: HubbleStatus) => (s.nodes ?? []).slice(0, NODE_CAP);
const relay = (s: HubbleStatus) => (s.relay ? `${s.relay.namespace}/${s.relay.service}${s.relay.port ? ':' + s.relay.port : ''}` : '');
const ordered = computed(() =>
    [...entries.value].sort((a, b) => Number(!!b.status?.available) - Number(!!a.status?.available) || a.id.localeCompare(b.id))
);
</script>

<template>
    <section class="hs" data-testid="hubble-status">
        <header class="head">
            <h1>Hubble status</h1>
            <span class="grow"></span>
            <button type="button" class="btn sm" :disabled="loading" data-testid="hubble-refresh" @click="load">
                {{ loading ? 'Refreshing…' : 'Refresh' }}
            </button>
        </header>
        <p v-if="loading && !entries.length" class="muted" role="status">Loading…</p>

        <article v-for="e in ordered" :key="e.id" class="card cl" :data-testid="`hubble-cluster-${e.id}`">
            <header class="ch">
                <h2>{{ name(e.id) }}</h2>
                <span class="tag" :class="e.status?.available ? 'success' : 'danger'">{{ e.status?.available ? 'Available' : 'Unavailable' }}</span>
                <span v-if="e.status?.version" class="muted mono">{{ e.status.version }}</span>
            </header>
            <p v-if="!e.status || !e.status.available" class="notice" :class="{ danger: !!e.error }">
                {{ e.error || e.status?.error || 'No reachable hubble-relay in this cluster.' }}
            </p>
            <template v-if="e.status && e.status.available">
                <dl class="kv">
                    <dt>Relay</dt>
                    <dd class="mono">{{ relay(e.status) || 'unknown' }}</dd>
                    <dt>Uptime</dt>
                    <dd>{{ formatUptime(e.status.uptimeSec) }}</dd>
                    <dt>Flows seen</dt>
                    <dd>{{ formatCount(e.status.seenFlows) }} ({{ Math.round(e.status.flowsPerSec) }}/s)</dd>
                    <dt>Flow buffer</dt>
                    <dd class="buf">
                        <span
                            class="bar"
                            role="meter"
                            :aria-valuenow="fillPct(e.status)"
                            aria-valuemin="0"
                            aria-valuemax="100"
                            :aria-label="`Flow buffer ${fillPct(e.status)}% full`"
                            ><span class="fill" :style="{ width: fillPct(e.status) + '%' }"></span
                        ></span>
                        <span class="mono"
                            >{{ formatCount(e.status.numFlows) }} / {{ formatCount(e.status.maxFlows) }} ({{ fillPct(e.status) }}%)</span
                        >
                    </dd>
                    <dt>Nodes</dt>
                    <dd>
                        <span class="tag success">{{ e.status.connectedNodes }} connected</span>
                        <span v-if="e.status.unavailableNodes" class="tag danger">{{ e.status.unavailableNodes }} unavailable</span>
                    </dd>
                </dl>
                <div v-if="statusHints(e.status).length" class="hints" data-testid="hubble-hints">
                    <strong>{{ hintsTitle(e.status) }}</strong>
                    <ul>
                        <li v-for="h in statusHints(e.status)" :key="h">{{ h }}</li>
                    </ul>
                </div>
                <div class="nodes" tabindex="0" role="region" :aria-label="`Nodes of ${name(e.id)}`">
                    <table>
                        <thead>
                            <tr>
                                <th scope="col">Node</th>
                                <th scope="col">State</th>
                                <th scope="col">Version</th>
                            </tr>
                        </thead>
                        <tbody>
                            <tr v-for="n in nodesOf(e.status)" :key="n.name">
                                <td class="mono">{{ n.name }}</td>
                                <td>
                                    <span class="tag" :class="nodeTone(n.state)">{{ n.state }}</span>
                                </td>
                                <td class="mono muted">{{ n.version }}</td>
                            </tr>
                        </tbody>
                    </table>
                    <p v-if="e.status.nodes.length > NODE_CAP" class="muted foot">Showing {{ NODE_CAP }} of {{ e.status.nodes.length }} nodes.</p>
                </div>
            </template>
        </article>
        <p v-if="!loading && !entries.length" class="notice">Connect a cluster to see its Hubble relay.</p>
    </section>
</template>

<style scoped>
.hs {
    flex-grow: 1;
    display: flex;
    flex-direction: column;
    gap: 12px;
    padding: 16px 20px;
    min-height: 0;
    overflow: auto;
}
.head {
    display: flex;
    align-items: center;
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
    font-size: 15px;
}
.cl {
    padding: 12px 14px;
    display: flex;
    flex-direction: column;
    gap: 10px;
}
.ch {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
}
.kv {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 6px 14px;
    margin: 0;
    font-size: 12.5px;
}
.kv dt {
    color: var(--text3);
}
.kv dd {
    margin: 0;
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
}
.buf .bar {
    width: 160px;
    height: 8px;
    border-radius: 4px;
    background: var(--s2);
    border: 1px solid var(--border);
    overflow: hidden;
}
.fill {
    display: block;
    height: 100%;
    background: var(--info);
}
.hints {
    padding: 8px 10px;
    border: 1px solid var(--warning-b);
    background: var(--warning-soft);
    border-radius: 8px;
    font-size: 12.5px;
}
.hints ul {
    margin: 4px 0 0;
    padding-left: 18px;
}
.nodes {
    max-height: 300px;
    overflow: auto;
    border: 1px solid var(--border);
    border-radius: 8px;
}
.nodes:focus-visible {
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
    height: 28px;
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
.foot {
    margin: 0;
    padding: 6px 10px;
}
@media (max-width: 639.98px) {
    .hs {
        padding: 12px;
    }
}
</style>
