<script setup lang="ts">
import type { ObjectRef } from '@v8s/client';

import { HelmGVR } from '@v8s/client';
import { computed } from 'vue';
import { useRouter } from 'vue-router';

import { openObject } from '@/lib/drawer.ts';
import { remediationOf, retriesExhausted, retriesLabel } from '@/lib/flux-helmrelease.ts';

const props = defineProps<{ target: ObjectRef; parsed: Record<string, unknown> | null }>();
const router = useRouter();

const rem = computed(() => remediationOf(props.parsed));

const counters = computed(() => {
    const r = rem.value;
    return [
        { key: 'failures', label: 'Consecutive failures', count: r.failures, limit: null as number | null, strategy: '' },
        { key: 'install', label: 'Install failures', count: r.installFailures, limit: r.install.retries, strategy: r.install.strategy },
        { key: 'upgrade', label: 'Upgrade failures', count: r.upgradeFailures, limit: r.upgrade.retries, strategy: r.upgrade.strategy }
    ];
});

function condTone(type: string, status: string): string {
    const ok = status === 'True';
    if (type === 'Stalled') return ok ? 'danger' : 'success';
    if (type === 'Remediated') return ok ? 'warning' : 'neutral';
    return ok ? 'success' : 'danger';
}
function condWord(type: string, status: string): string {
    if (status === 'Unknown') return `${type} unknown`;
    return status === 'True' ? type : `Not ${type.toLowerCase()}`;
}
function histTone(status: string): string {
    const s = status.toLowerCase();
    if (s === 'deployed') return 'success';
    if (s === 'failed') return 'danger';
    if (s.startsWith('pending')) return 'warning';
    if (s === 'superseded') return 'info';
    return 'neutral';
}
const shortDigest = (d: string) => (d.length > 19 ? d.slice(0, 19) + '…' : d);
const shortTime = (t: string) => (t ? t.replace('T', ' ').replace(/\.\d+/, '').replace('Z', 'Z') : '—');

function openRelease() {
    const r = rem.value;
    void openObject(router, { cluster: props.target.cluster, gvr: HelmGVR, namespace: r.releaseNamespace, name: r.releaseName });
}
</script>

<template>
    <div class="hrf" data-testid="helmrelease-flux">
        <div class="top">
            <span v-if="rem.suspended" class="tag">Suspended</span>
            <span v-if="rem.reconcilePending" class="tag info" role="status">Reconcile requested, waiting for the controller</span>
            <span class="grow"></span>
            <button class="btn sm" type="button" @click="openRelease">Open Helm release</button>
        </div>

        <section aria-label="Conditions">
            <h4 class="lbl">Conditions</h4>
            <p v-if="!rem.conditions.length" class="muted">No Ready, Stalled, Remediated or Released conditions yet.</p>
            <ul v-else class="conds">
                <li v-for="c in rem.conditions" :key="c.type">
                    <span class="tag" :class="condTone(c.type, c.status)">{{ condWord(c.type, c.status) }}</span>
                    <span v-if="c.reason" class="mono reason">{{ c.reason }}</span>
                    <span v-if="c.message" class="msg">{{ c.message }}</span>
                </li>
            </ul>
        </section>

        <section aria-label="Remediation">
            <h4 class="lbl">Remediation</h4>
            <table class="kv">
                <tbody>
                    <tr v-for="c in counters" :key="c.key">
                        <th scope="row">{{ c.label }}</th>
                        <td>
                            <span class="mono">{{ c.count }}</span>
                            <template v-if="c.key !== 'failures'">
                                <span class="muted"> of {{ retriesLabel(c.limit) }} retries</span>
                                <span class="muted"> · {{ c.strategy }}</span>
                                <span v-if="c.count && retriesExhausted(c.count, c.limit)" class="tag danger">Retries exhausted</span>
                                <span v-else-if="c.count" class="tag warning">Retrying</span>
                            </template>
                        </td>
                    </tr>
                </tbody>
            </table>
        </section>

        <section aria-label="Revisions">
            <h4 class="lbl">Revisions</h4>
            <table class="kv">
                <tbody>
                    <tr>
                        <th scope="row">Last attempted</th>
                        <td class="mono">{{ rem.lastAttemptedRevision || '—' }}</td>
                    </tr>
                    <tr>
                        <th scope="row">Last applied</th>
                        <td class="mono">{{ rem.lastAppliedRevision || '—' }}</td>
                    </tr>
                    <tr>
                        <th scope="row">Helm chart</th>
                        <td class="mono">{{ rem.helmChart || '—' }}</td>
                    </tr>
                    <tr>
                        <th scope="row">Last handled reconcile</th>
                        <td class="mono">{{ rem.lastHandledReconcileAt || '—' }}</td>
                    </tr>
                    <tr v-if="rem.requestedAt">
                        <th scope="row">Requested at</th>
                        <td class="mono">{{ rem.requestedAt }}</td>
                    </tr>
                </tbody>
            </table>
        </section>

        <section aria-label="History">
            <h4 class="lbl">History</h4>
            <p v-if="!rem.history.length" class="muted">No release history recorded.</p>
            <div v-else class="hist" tabindex="0" role="region" aria-label="Release history">
                <table>
                    <thead>
                        <tr>
                            <th scope="col">Rev</th>
                            <th scope="col">Status</th>
                            <th scope="col">Chart</th>
                            <th scope="col">App</th>
                            <th scope="col">First deployed</th>
                            <th scope="col">Last deployed</th>
                            <th scope="col">Digest</th>
                        </tr>
                    </thead>
                    <tbody>
                        <tr v-for="(h, i) in rem.history" :key="i">
                            <td class="mono">{{ h.version ?? '—' }}</td>
                            <td>
                                <span class="tag" :class="histTone(h.status)">{{ h.status || 'unknown' }}</span>
                            </td>
                            <td class="mono">{{ h.chartName }}{{ h.chartVersion ? '@' + h.chartVersion : '' }}</td>
                            <td class="mono">{{ h.appVersion || '—' }}</td>
                            <td class="mono">{{ shortTime(h.firstDeployed) }}</td>
                            <td class="mono">{{ shortTime(h.lastDeployed) }}</td>
                            <td class="mono" :title="h.digest">{{ shortDigest(h.digest) }}</td>
                        </tr>
                    </tbody>
                </table>
            </div>
        </section>
    </div>
</template>

<style scoped>
.hrf {
    display: flex;
    flex-direction: column;
    gap: 14px;
    font-size: 12.5px;
}
.top {
    display: flex;
    align-items: center;
    gap: 6px;
    flex-wrap: wrap;
}
h4 {
    margin: 0 0 6px;
}
.conds {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
}
.conds li {
    display: flex;
    align-items: baseline;
    flex-wrap: wrap;
    gap: 8px;
}
.reason {
    color: var(--text2);
}
.msg {
    flex-basis: 100%;
    color: var(--text2);
    overflow-wrap: anywhere;
}
.kv {
    border-collapse: collapse;
    width: 100%;
}
.kv th {
    width: 180px;
    padding: 4px 8px 4px 0;
    text-align: left;
    font-weight: 500;
    color: var(--text3);
    vertical-align: top;
}
.kv td {
    padding: 4px 0;
    overflow-wrap: anywhere;
}
.kv .tag {
    margin-left: 6px;
}
.hist {
    max-height: 288px;
    overflow: auto;
    border: 1px solid var(--border);
    border-radius: 8px;
}
.hist table {
    border-collapse: collapse;
    width: 100%;
}
.hist th,
.hist td {
    height: 28px;
    padding: 0 8px;
    text-align: left;
    white-space: nowrap;
    border-bottom: 1px solid var(--border);
}
.hist th {
    position: sticky;
    top: 0;
    background: var(--s2);
    font-size: 11px;
    font-weight: 600;
    color: var(--text3);
}
.hist tr:last-child td {
    border-bottom: 0;
}
</style>
