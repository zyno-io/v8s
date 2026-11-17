<script setup lang="ts">
import type { KyvernoExceptionTemplateResult, ObjectRef } from '@v8s/client';

import { computed, defineAsyncComponent, onBeforeUnmount, ref, shallowRef, watch } from 'vue';

import type { ReportItem } from '@/lib/security-api.ts';
import type { ImageMatch, ReportRow } from '@/lib/security.ts';

import { getAgent, kinds } from '@/lib/agent.ts';
import { openCreateWith } from '@/lib/create-prefill.ts';
import { errorText } from '@/lib/mutations.ts';
import { exceptionPolicyGvr, policyReportGvr } from '@/lib/policy.ts';
import { fetchImages, readItems, reportGvrs, reportView, snapshotView } from '@/lib/security-api.ts';
import {
    containerImages,
    kindOfGvr,
    matchesWorkload,
    matchImages,
    sevLabel,
    sevTone,
    sourceLabel,
    workloadSearchTerm,
    workloadTargetOf
} from '@/lib/security.ts';
import { useIntegrationsStore } from '@/stores/integrations.ts';
import { useUiStore } from '@/stores/ui.ts';

const CveTable = defineAsyncComponent(() => import('@/components/CveTable.vue'));

const props = defineProps<{ target: ObjectRef; parsed: Record<string, unknown> | null }>();

const ui = useUiStore();
const integrations = useIntegrationsStore();

const wl = computed(() => workloadTargetOf(props.target.gvr, props.target.name ?? '', props.target.namespace, props.parsed));
const containers = computed(() => containerImages(props.parsed));

// ---- (a) image vulnerabilities: counts per container image from the shared, short-lived `security.images` cache ----
const matches = shallowRef<ImageMatch[]>([]);
const imagesState = ref<'loading' | 'ready' | 'error'>('loading');
const imagesError = ref('');
let epoch = 0;

async function loadImages() {
    const mine = epoch;
    if (!containers.value.length) {
        matches.value = [];
        imagesState.value = 'ready';
        return;
    }
    try {
        const list = await fetchImages([props.target.cluster]);
        if (mine !== epoch) return;
        matches.value = matchImages(containers.value, list);
        imagesState.value = 'ready';
    } catch (e) {
        if (mine !== epoch) return;
        imagesError.value = errorText(e);
        imagesState.value = 'error';
    }
}

// ---- reports of this workload, per section, from the scanner report views filtered by workload ----
type Section = 'vulns' | 'config' | 'secrets' | 'policy';
interface Listed {
    section: Section;
    item: ReportItem;
    label: string;
    sub: string;
}
const reports = shallowRef<Listed[]>([]);
const reportsLoading = ref(true);
const reportsError = ref('');
const openKey = ref('');

const TITLES: Record<Section, string> = {
    vulns: 'Image vulnerability reports',
    config: 'Configuration audit',
    secrets: 'Exposed secrets',
    policy: 'Policy results'
};
const ORDER: Section[] = ['vulns', 'config', 'secrets', 'policy'];

async function loadReports() {
    const mine = epoch;
    reportsLoading.value = true;
    reportsError.value = '';
    const g = reportGvrs(kinds.value);
    const t = wl.value;
    const term = workloadSearchTerm(t);
    const base = { clusters: [props.target.cluster], namespaces: t.namespace ? [t.namespace] : [] };
    const byWorkload = (gvr: string) => snapshotView(reportView(gvr, [{ col: 'workload', op: 'contains', value: term }], base), 50);
    const jobs: { section: Section; gvr: string; run: () => ReturnType<typeof snapshotView> }[] = [];
    for (const gvr of [g.trivyVuln, g.kubescapeVuln]) if (gvr) jobs.push({ section: 'vulns', gvr, run: () => byWorkload(gvr) });
    for (const gvr of [g.trivyConfig, g.kubescapeConfig]) if (gvr) jobs.push({ section: 'config', gvr, run: () => byWorkload(gvr) });
    if (g.trivySecret) jobs.push({ section: 'secrets', gvr: g.trivySecret, run: () => byWorkload(g.trivySecret) });
    const pr = policyReportGvr(kinds.value);
    if (pr)
        jobs.push({
            section: 'policy',
            gvr: pr,
            run: () =>
                snapshotView(
                    reportView(
                        pr,
                        [
                            { col: 'scopeName', op: 'eq', value: t.name },
                            { col: 'scopeKind', op: 'eq', value: t.kind }
                        ],
                        base
                    ),
                    20
                )
        });
    const settled = await Promise.allSettled(jobs.map(j => j.run()));
    if (mine !== epoch) return;
    const out: Listed[] = [];
    let firstError = '';
    settled.forEach((s, i) => {
        const j = jobs[i];
        if (s.status === 'rejected') {
            firstError ||= errorText(s.reason);
            return;
        }
        for (const item of readItems(s.value, j.gvr, props.target.cluster)) {
            if (j.section !== 'policy' && !matchesWorkload(t, item.cells.workload ?? '')) continue;
            const source = j.gvr.startsWith('aquasecurity') ? 'trivy' : j.gvr.startsWith('spdx') ? 'kubescape' : 'kyverno';
            const c = item.cells;
            const counts =
                j.section === 'policy'
                    ? `${c.fail ?? 0} fail · ${c.warn ?? 0} warn · ${c.pass ?? 0} pass`
                    : ['critical', 'high', 'medium', 'low']
                          .filter(k => Number(c[k]) > 0)
                          .map(k => `${c[k]} ${k}`)
                          .join(' · ') || 'no findings';
            out.push({
                section: j.section,
                item,
                label:
                    j.section === 'vulns' && c.container
                        ? `${c.workload ?? ''} · ${c.container}`
                        : (c.workload ?? c.scopeName ?? item.ref.name ?? ''),
                sub: `${sourceLabel(source)} · ${counts}`
            });
        }
    });
    reports.value = out;
    reportsError.value = firstError;
    reportsLoading.value = false;
}

function load() {
    epoch++;
    imagesState.value = 'loading';
    reports.value = [];
    openKey.value = '';
    void loadImages();
    void loadReports();
}
watch(
    () => `${props.target.cluster}|${props.target.gvr}|${props.target.namespace}|${props.target.name}|${containers.value.map(c => c.image).join()}`,
    load,
    {
        immediate: true
    }
);
onBeforeUnmount(() => epoch++);

const sections = computed(() =>
    ORDER.map(s => ({ section: s, title: TITLES[s], list: reports.value.filter(r => r.section === s) })).filter(s => s.list.length)
);
const open = computed(() => reports.value.find(r => r.item.key === openKey.value) ?? null);
const toggle = (key: string) => (openKey.value = openKey.value === key ? '' : key);

// ---- exceptions for failing policy results ----
async function makeException(row: ReportRow) {
    const gvr = exceptionPolicyGvr(kinds.value);
    if (!gvr) {
        ui.toast('warning', 'No Kyverno policy kind is served, so an exception cannot be drafted.');
        return;
    }
    try {
        const res = await getAgent().call<KyvernoExceptionTemplateResult>('kyverno.exceptionTemplate', {
            policy: { cluster: props.target.cluster, gvr, name: row.id },
            rule: row.cells[0] || undefined,
            resource: { kind: kindOfGvr(props.target.gvr) || wl.value.kind, namespace: props.target.namespace, name: props.target.name ?? '' }
        });
        openCreateWith({ yaml: res.yaml, cluster: props.target.cluster }, () => (ui.createOpen = true));
    } catch (e) {
        ui.toast('danger', errorText(e));
    }
}

const exceptionFn = computed(() => (open.value?.section === 'policy' ? makeException : undefined));
const link = (m: ImageMatch, source: string) => ({ path: '/security', query: { image: m.found[0] ? m.found[0].image : m.image, source } });
const hasScanner = computed(() => integrations.detected.trivy || integrations.detected.kubescape || integrations.detected.kyverno);
const empty = computed(() => !reportsLoading.value && !reports.value.length && !matches.value.some(m => m.found.length));
</script>

<template>
    <div class="sec" data-testid="security-pane">
        <p v-if="!hasScanner" class="muted">No security scanner or policy engine was detected in the selected clusters.</p>
        <template v-else>
            <section aria-labelledby="sec-img">
                <h3 id="sec-img">Images</h3>
                <p v-if="!containers.length" class="muted small">This object has no containers.</p>
                <p v-else-if="imagesState === 'loading'" class="muted small" role="status">Loading image scan results…</p>
                <p v-else-if="imagesState === 'error'" class="notice warn" role="alert">{{ imagesError }}</p>
                <ul v-else class="list" aria-label="Container images">
                    <li v-for="m in matches" :key="(m.init ? 'i:' : 'c:') + m.container" class="item">
                        <div class="main">
                            <span class="mono nm" :title="m.image">{{ m.image }}</span>
                            <span class="muted small">{{ m.init ? 'init container' : 'container' }} {{ m.container }}</span>
                        </div>
                        <div v-if="m.found.length" class="found">
                            <router-link
                                v-for="f in m.found"
                                :key="f.source"
                                class="fl"
                                :to="link(m, f.source)"
                                :aria-label="`Open ${f.image} in Vulnerabilities`"
                            >
                                <span class="tag">{{ sourceLabel(f.source) }}</span>
                                <span
                                    v-for="k in ['critical', 'high', 'medium', 'low'] as const"
                                    :key="k"
                                    class="tag"
                                    :class="f[k] > 0 ? sevTone(k) : ''"
                                    >{{ sevLabel(k)[0] }} {{ f[k] }}</span
                                >
                            </router-link>
                        </div>
                        <span v-else class="muted small">No scan results</span>
                    </li>
                </ul>
            </section>

            <p v-if="reportsLoading" class="muted small" role="status">Loading scanner reports…</p>
            <p v-else-if="reportsError && !reports.length" class="notice warn" role="alert">{{ reportsError }}</p>
            <p v-else-if="empty" class="muted" role="status">No scanner reports for this workload yet.</p>

            <section v-for="s in sections" :key="s.section" :aria-label="s.title">
                <h3>{{ s.title }}</h3>
                <ul class="list">
                    <li v-for="r in s.list" :key="r.item.key" class="item rep" :class="{ on: openKey === r.item.key }">
                        <div class="main">
                            <span class="nm" :title="r.label">{{ r.label }}</span>
                            <span class="muted small">{{ r.sub }}</span>
                        </div>
                        <button class="btn sm" type="button" :aria-expanded="openKey === r.item.key" @click="toggle(r.item.key)">
                            {{ openKey === r.item.key ? 'Hide' : s.section === 'vulns' ? 'CVEs' : s.section === 'policy' ? 'Results' : 'Open' }}
                        </button>
                    </li>
                </ul>
            </section>
            <CveTable v-if="open" :key="open.item.key" class="inline" :target="open.item.ref" :height="360" :make-exception="exceptionFn" />
        </template>
    </div>
</template>

<style scoped>
.sec {
    display: flex;
    flex-direction: column;
    gap: 14px;
    padding: 12px 16px 20px;
}
h3 {
    margin: 0 0 6px;
    font-size: 13px;
    font-weight: 600;
}
.small {
    font-size: 12px;
    margin: 0;
}
.list {
    list-style: none;
    margin: 0;
    padding: 0;
    border: 1px solid var(--border);
    border-radius: 8px;
    max-height: 288px;
    overflow: auto;
}
.item {
    display: flex;
    align-items: center;
    gap: 10px;
    height: 48px;
    padding: 0 10px;
    border-bottom: 1px solid var(--border);
}
.item:last-child {
    border-bottom: 0;
}
.item.on {
    background: var(--accent-soft);
}
.main {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
}
.nm {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 13px;
}
.found {
    display: flex;
    flex-direction: column;
    gap: 2px;
}
.fl {
    display: inline-flex;
    gap: 3px;
    text-decoration: none;
}
.fl:hover .tag:first-child {
    text-decoration: underline;
}
.fl:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: 1px;
    border-radius: 4px;
}
.inline {
    flex: none;
}
@media (pointer: coarse) {
    .item {
        height: 56px;
    }
    .item .btn.sm {
        min-height: 44px;
    }
}
</style>
