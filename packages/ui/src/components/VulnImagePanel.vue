<script setup lang="ts">
import type { ImageVulnSummary, ObjectRef } from '@v8s/client';

import { computed, defineAsyncComponent, onBeforeUnmount, ref, shallowRef, watch } from 'vue';

import { kinds } from '@/lib/agent.ts';
import { formatCount } from '@/lib/format.ts';
import { errorText } from '@/lib/mutations.ts';
import { colIdx, reportGvrs, reportView, rescanReport, snapshotView } from '@/lib/security-api.ts';
import { SEVERITIES, sevLabel, sevTone, sourceLabel, splitImage } from '@/lib/security.ts';
import { isPhone } from '@/lib/viewport.ts';
import { useClustersStore } from '@/stores/clusters.ts';

const CveTable = defineAsyncComponent(() => import('@/components/CveTable.vue'));

const props = defineProps<{ image: string; source: string; summaries: ImageVulnSummary[]; report: ObjectRef | null }>();
const emit = defineEmits<{ openReport: [ref: ObjectRef | null]; close: [] }>();

const clusters = useClustersStore();

interface ReportItem {
    ref: ObjectRef;
    key: string;
    workload: string;
    kind: string;
    container: string;
    cluster: string;
    counts: number[];
}

const MAX = 300;
const items = shallowRef<ReportItem[]>([]);
const total = ref(0);
const loading = ref(true);
const error = ref('');
let epoch = 0;

const split = computed(() => splitImage(props.image));
const source = computed(() => props.source || props.summaries[0]?.source || 'trivy');
const gvr = computed(() => {
    const g = reportGvrs(kinds.value);
    return source.value === 'kubescape' ? g.kubescapeVuln : g.trivyVuln;
});

async function load() {
    const mine = ++epoch;
    error.value = '';
    if (!gvr.value) {
        items.value = [];
        loading.value = false;
        return;
    }
    loading.value = true;
    try {
        const q = reportView(
            gvr.value,
            [
                { col: 'repository', op: 'eq', value: split.value.repository },
                { col: 'tag', op: 'eq', value: split.value.tag }
            ],
            {
                sort: [
                    { col: 'critical', desc: true },
                    { col: 'high', desc: true },
                    { col: 'name', desc: false }
                ]
            }
        );
        const snap = await snapshotView(q, MAX);
        if (mine !== epoch) return;
        const ix = (k: string) => colIdx(snap.columns, k);
        const [cl, ns, nm, wl, kd, ct] = ['cluster', 'namespace', 'name', 'workload', 'kind', 'container'].map(ix);
        const sev = SEVERITIES.map(ix);
        const fallback = clusters.activeIds[0] ?? '';
        items.value = snap.rows.map(r => {
            const cluster = cl >= 0 ? String(r[cl]) : fallback;
            const name = String(r[nm]);
            const namespace = ns >= 0 ? String(r[ns]) || undefined : undefined;
            return {
                ref: { cluster, gvr: gvr.value, namespace, name },
                key: `${cluster}/${namespace ?? ''}/${name}`,
                workload: wl >= 0 ? String(r[wl]) : name,
                kind: kd >= 0 ? String(r[kd]) : '',
                container: ct >= 0 ? String(r[ct]) : '',
                cluster,
                counts: sev.map(i => (i >= 0 ? Number(r[i]) || 0 : 0))
            };
        });
        total.value = snap.total;
    } catch (e) {
        if (mine === epoch) error.value = errorText(e);
    } finally {
        if (mine === epoch) loading.value = false;
    }
}
watch(() => [props.image, source.value, gvr.value, clusters.activeIds.join()], load, { immediate: true });
onBeforeUnmount(() => epoch++);

async function rescan(it: ReportItem) {
    if (await rescanReport(it.ref, it.workload)) setTimeout(load, 1500);
}

const nameOf = (c: string) => clusters.displayName(c);
const sameRef = (a: ObjectRef, b: ObjectRef | null) => !!b && a.cluster === b.cluster && a.namespace === b.namespace && a.name === b.name;
const sums = computed(() => {
    const t = [0, 0, 0, 0, 0];
    for (const s of props.summaries) SEVERITIES.forEach((k, i) => (t[i] += s[k]));
    return t;
});
</script>

<template>
    <aside class="panel floating" role="complementary" :aria-label="`Image ${image}`" data-testid="image-panel">
        <header class="shead">
            <div class="row">
                <button v-if="report" class="btn sm ghost" type="button" data-testid="back-reports" @click="emit('openReport', null)">
                    ← Reports
                </button>
                <b class="ttl mono" :title="image">{{ image }}</b>
                <span v-for="s in summaries" :key="s.source" class="tag">{{ sourceLabel(s.source) }}</span>
                <div class="grow"></div>
                <button class="btn sm ghost" type="button" aria-label="Close image details" @click="emit('close')">Close</button>
            </div>
            <div class="row counts">
                <span v-for="(k, i) in SEVERITIES" :key="k" class="tag" :class="sums[i] > 0 ? sevTone(k) : ''"
                    >{{ sevLabel(k) }} {{ formatCount(sums[i]) }}</span
                >
            </div>
        </header>
        <div class="body">
            <CveTable v-if="report" :key="`${report.cluster}|${report.gvr}|${report.namespace}|${report.name}`" :target="report" @rescanned="load" />
            <template v-else>
                <p v-if="!gvr" class="muted pad">None of the selected clusters serves the {{ sourceLabel(source) }} report kind.</p>
                <div v-else-if="error" class="notice warn" role="alert">{{ error }}</div>
                <p v-else-if="loading && !items.length" class="muted pad" role="status">Loading reports…</p>
                <p v-else-if="!items.length" class="muted pad" role="status">No reports found for this image in the selected clusters.</p>
                <template v-else>
                    <p class="muted small pad" role="status">
                        {{ formatCount(total) }} report{{ total === 1 ? '' : 's'
                        }}{{ total > items.length ? ` · showing the first ${formatCount(items.length)}` : '' }}
                    </p>
                    <ul class="list" aria-label="Reports of this image">
                        <li v-for="it in items" :key="it.key" class="item" :class="{ on: sameRef(it.ref, report), mcard: isPhone }">
                            <button class="open" type="button" :aria-label="`Open report of ${it.workload}`" @click="emit('openReport', it.ref)">
                                <span class="wl" :title="`${it.kind} ${it.workload}`"
                                    ><span class="muted">{{ it.kind }}</span> {{ it.workload
                                    }}<span v-if="it.container" class="muted"> · {{ it.container }}</span></span
                                >
                                <span class="meta">
                                    <span v-if="clusters.activeIds.length > 1" class="tag" :class="clusters.tagTone(it.cluster)"
                                        ><span class="dot" :style="{ background: clusters.dotColor(it.cluster) }"></span
                                        >{{ nameOf(it.cluster) }}</span
                                    >
                                    <span v-if="it.ref.namespace" class="muted small">{{ it.ref.namespace }}</span>
                                    <span v-for="(k, i) in SEVERITIES.slice(0, 4)" :key="k" class="tag" :class="it.counts[i] > 0 ? sevTone(k) : ''"
                                        >{{ sevLabel(k)[0] }} {{ it.counts[i] }}</span
                                    >
                                </span>
                            </button>
                            <button
                                v-if="source === 'trivy'"
                                class="btn sm"
                                type="button"
                                title="Delete report to trigger a rescan"
                                :aria-label="`Rescan ${it.workload}`"
                                @click="rescan(it)"
                            >
                                Rescan
                            </button>
                        </li>
                    </ul>
                </template>
            </template>
        </div>
    </aside>
</template>

<style scoped>
.panel {
    display: flex;
    flex-direction: column;
    min-height: 0;
    min-width: 0;
    overflow: hidden;
}
.shead {
    padding: 12px 14px 8px;
    display: flex;
    flex-direction: column;
    gap: 8px;
    border-bottom: 1px solid var(--border);
}
.row {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
    flex-wrap: wrap;
}
.ttl {
    font-size: 14px;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
.body {
    flex: 1 1 0;
    min-height: 0;
    display: flex;
    flex-direction: column;
    padding: 10px 14px 12px;
    overflow: auto;
}
.pad {
    padding: 8px 0;
    margin: 0;
}
.small {
    font-size: 12px;
}
.list {
    list-style: none;
    margin: 0;
    padding: 0;
    border: 1px solid var(--border);
    border-radius: 8px;
}
.item {
    display: flex;
    align-items: center;
    gap: 8px;
    height: 36px;
    padding: 0 8px;
    border-bottom: 1px solid var(--border);
}
.item:last-child {
    border-bottom: 0;
}
.item.mcard {
    height: 72px;
}
.item.on {
    background: var(--accent-soft);
}
.open {
    all: unset;
    box-sizing: border-box;
    flex: 1;
    min-width: 0;
    display: flex;
    align-items: center;
    gap: 10px;
    height: 100%;
    cursor: pointer;
    font-size: 13px;
}
.open:hover .wl {
    text-decoration: underline;
}
.open:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
}
.mcard .open {
    flex-direction: column;
    align-items: flex-start;
    justify-content: center;
    gap: 4px;
}
.wl {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    flex: 1;
}
.mcard .wl {
    flex: none;
    max-width: 100%;
}
.meta {
    display: flex;
    align-items: center;
    gap: 6px;
    flex: none;
}
@media (pointer: coarse) {
    .item:not(.mcard) {
        height: 48px;
    }
    .item .btn.sm {
        min-height: 44px;
    }
}
</style>
