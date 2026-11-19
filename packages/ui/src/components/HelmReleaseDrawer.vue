<script setup lang="ts">
import type { HelmDiffResult, HelmRelease, ObjectRef } from '@v8s/client';

import { HelmGVR } from '@v8s/client';
import { computed, defineAsyncComponent, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue';
import { useRouter } from 'vue-router';

import DiffView from '@/components/DiffView.vue';
import HelmActionDialog from '@/components/HelmActionDialog.vue';
import SmartSelect from '@/components/SmartSelect.vue';
import { getAgent } from '@/lib/agent.ts';
import { openObject } from '@/lib/drawer.ts';
import { canRetry, canRollbackTo, helmStatusTone, latestRevisionOptions } from '@/lib/helm.ts';
import { errorText } from '@/lib/mutations.ts';
import { unifiedToSides } from '@/lib/unified.ts';

const YamlEditor = defineAsyncComponent(() => import('@/components/YamlEditor.vue'));

const props = defineProps<{ target: ObjectRef }>();

const TABS = ['overview', 'history', 'values', 'manifest', 'notes', 'diff'] as const;
type Tab = (typeof TABS)[number];
const TAB_LABEL: Record<Tab, string> = {
    overview: 'Overview',
    history: 'History',
    values: 'Values',
    manifest: 'Manifest',
    notes: 'Notes',
    diff: 'Diff'
};
const FLUX_GVR = 'helm.toolkit.fluxcd.io/v2/helmreleases';
const REFRESH_MIN_MS = 10_000;

const router = useRouter();
const tab = ref<Tab>('overview');
/** The latest revision (header, history, actions) and the one whose values/manifest/notes are shown. */
const latest = shallowRef<HelmRelease | null>(null);
const shown = shallowRef<HelmRelease | null>(null);
const loading = ref(false);
const error = ref('');
const selected = ref(0); // 0 = latest
const valuesMode = ref<'user' | 'computed'>('user');
const dialog = ref<{ action: 'rollback' | 'uninstall' | 'retry'; revision?: number } | null>(null);
const picked = ref<number | null>(null);
const ticked = ref<number[]>([]);
const diffFrom = ref(0);
const diffTo = ref(0);
const diff = shallowRef<HelmDiffResult | null>(null);
const diffBusy = ref(false);
const diffErr = ref('');
let lastLoad = 0;
let seq = 0;

const rel = computed(() => latest.value);
const cur = computed(() => shown.value ?? latest.value);
const revOptions = computed(() => latestRevisionOptions(latest.value?.history));
const revChoices = computed(() => revOptions.value.map(o => ({ value: o.revision, label: o.label })));
const diffChoices = computed(() => revOptions.value.map(o => ({ value: o.revision, label: `${o.revision} · ${o.label.split(' · ')[1]}` })));
const rollbackTargets = computed(() => revOptions.value.filter(o => canRollbackTo(latest.value?.history, latest.value?.revision ?? 0, o.revision)));
const sortedHistory = computed(() => [...(latest.value?.history ?? [])].sort((a, b) => b.revision - a.revision));
const sides = (d: string) => unifiedToSides(d);
const valuesText = computed(() => (valuesMode.value === 'user' ? cur.value?.values : cur.value?.computedValues) ?? '');
const fluxName = computed(() => rel.value?.managedBy);

const fmt = (ms: number) => (ms ? new Date(ms).toLocaleString() : '—');

async function load(revision = 0) {
    const id = ++seq;
    loading.value = true;
    error.value = '';
    try {
        const p = { cluster: props.target.cluster, namespace: props.target.namespace ?? '', name: props.target.name };
        const r = await getAgent().call<HelmRelease>('helm.release', revision ? { ...p, revision } : p);
        if (id !== seq) return;
        if (!revision) {
            latest.value = r;
            if (!selected.value || selected.value === r.revision) shown.value = r;
        } else {
            shown.value = r;
        }
        lastLoad = Date.now();
    } catch (e) {
        if (id === seq) error.value = errorText(e);
    } finally {
        if (id === seq) loading.value = false;
    }
}

async function reload() {
    // Reloads the latest revision and, when another revision is selected, that one too.
    await load(0);
    if (selected.value && selected.value !== latest.value?.revision) await load(selected.value);
}

function resetDiff() {
    const l = latest.value;
    if (!l) return;
    const prev = [...l.history].map(h => h.revision).filter(r => r < l.revision);
    diffTo.value = l.revision;
    diffFrom.value = prev.length ? Math.max(...prev) : l.revision;
    diff.value = null;
}

async function runDiff() {
    if (!diffFrom.value || !diffTo.value) return;
    diffBusy.value = true;
    diffErr.value = '';
    try {
        diff.value = await getAgent().call<HelmDiffResult>('helm.diff', {
            ref: { cluster: props.target.cluster, gvr: HelmGVR, namespace: props.target.namespace ?? '', name: props.target.name },
            fromRevision: diffFrom.value,
            toRevision: diffTo.value
        });
    } catch (e) {
        diffErr.value = errorText(e);
        diff.value = null;
    } finally {
        diffBusy.value = false;
    }
}

function selectRevision(r: number) {
    selected.value = r;
    void load(r === latest.value?.revision ? 0 : r);
}

function toggleTick(r: number) {
    const i = ticked.value.indexOf(r);
    if (i >= 0) ticked.value = ticked.value.filter(x => x !== r);
    else ticked.value = [...ticked.value, r].slice(-2);
}
function compare() {
    if (ticked.value.length !== 2) return;
    const [a, b] = [...ticked.value].sort((x, y) => x - y);
    diffFrom.value = a;
    diffTo.value = b;
    setTab('diff');
    void runDiff();
}

function setTab(t: Tab) {
    tab.value = t;
    if (t === 'diff' && !diff.value && !diffBusy.value && diffFrom.value !== diffTo.value) void runDiff();
}
function onTabKey(e: KeyboardEvent) {
    const i = TABS.indexOf(tab.value);
    let n = i;
    if (e.key === 'ArrowRight') n = (i + 1) % TABS.length;
    else if (e.key === 'ArrowLeft') n = (i + TABS.length - 1) % TABS.length;
    else if (e.key === 'Home') n = 0;
    else if (e.key === 'End') n = TABS.length - 1;
    else return;
    e.preventDefault();
    setTab(TABS[n]);
    void nextTick(() => document.getElementById(`helm-tab-${TABS[n]}`)?.focus());
}

function openFlux() {
    const f = fluxName.value;
    if (f) void openObject(router, { cluster: props.target.cluster, gvr: FLUX_GVR, namespace: f.namespace, name: f.name });
}

function onFocusIn() {
    if (document.visibilityState === 'visible' && Date.now() - lastLoad >= REFRESH_MIN_MS && !loading.value) void reload();
}
function onVisible() {
    if (document.visibilityState === 'visible') onFocusIn();
}

watch(
    () => `${props.target.cluster}|${props.target.namespace}|${props.target.name}`,
    () => {
        latest.value = shown.value = null;
        selected.value = 0;
        diff.value = null;
        picked.value = null;
        ticked.value = [];
        void load(0);
    }
);
watch(latest, (l, old) => {
    if (l && (!old || old.revision !== l.revision || !diffTo.value)) resetDiff();
});

onMounted(() => {
    void load(0);
    window.addEventListener('focus', onFocusIn);
    document.addEventListener('visibilitychange', onVisible);
});
onBeforeUnmount(() => {
    window.removeEventListener('focus', onFocusIn);
    document.removeEventListener('visibilitychange', onVisible);
});
</script>

<template>
    <section class="hd" data-testid="helm-drawer" :aria-label="`Helm release ${target.name}`" :aria-busy="loading">
        <header>
            <div class="title">
                <h2 class="c">{{ target.name }}</h2>
                <template v-if="rel">
                    <span class="tag" :class="helmStatusTone(rel.status)" data-testid="helm-status">{{ rel.status }}</span>
                    <span class="muted small">revision {{ rel.revision }}</span>
                </template>
                <button v-if="fluxName" class="tag link" type="button" data-testid="helm-flux" @click="openFlux">
                    managed by Flux · {{ fluxName.namespace }}/{{ fluxName.name }}
                </button>
                <div class="grow"></div>
                <button class="btn sm ghost" type="button" :disabled="loading" @click="reload">{{ loading ? 'Loading…' : 'Refresh' }}</button>
            </div>
            <div v-if="rel" class="sub muted small">
                <span class="mono">{{ rel.chart }}<template v-if="rel.chartVersion">-{{ rel.chartVersion }}</template></span>
                <span v-if="rel.appVersion">app <span class="mono">{{ rel.appVersion }}</span></span>
                <span>{{ target.namespace }}</span>
            </div>
            <div v-if="rel" class="actions">
                <button
                    class="btn sm"
                    type="button"
                    :disabled="!rollbackTargets.length"
                    data-testid="helm-action-rollback"
                    @click="dialog = { action: 'rollback', revision: picked ?? undefined }"
                >
                    Roll back…
                </button>
                <button
                    v-if="canRetry(rel.status)"
                    class="btn sm"
                    type="button"
                    data-testid="helm-action-retry"
                    @click="dialog = { action: 'retry' }"
                >
                    Retry
                </button>
                <button class="btn sm danger" type="button" data-testid="helm-action-uninstall" @click="dialog = { action: 'uninstall' }">
                    Uninstall…
                </button>
            </div>
            <label v-if="rel && revOptions.length > 1" class="revsel small"
                >Revision
                <SmartSelect
                    :model-value="selected || rel.revision"
                    :options="revChoices"
                    aria-label="Revision to show"
                    data-testid="helm-revision-select"
                    @update:model-value="selectRevision"
                />
            </label>
        </header>

        <div class="tabs" role="tablist" aria-label="Release sections" @keydown="onTabKey">
            <button
                v-for="t in TABS"
                :id="`helm-tab-${t}`"
                :key="t"
                class="tab"
                :class="{ on: tab === t }"
                type="button"
                role="tab"
                :aria-selected="tab === t"
                :aria-controls="`helm-panel-${t}`"
                :tabindex="tab === t ? 0 : -1"
                :data-testid="`helm-tab-${t}`"
                @click="setTab(t)"
            >
                {{ TAB_LABEL[t] }}
            </button>
        </div>

        <div :id="`helm-panel-${tab}`" class="panel" role="tabpanel" :aria-labelledby="`helm-tab-${tab}`">
            <p v-if="error" class="notice danger" role="alert">{{ error }}</p>
            <p v-else-if="!rel" class="muted" role="status">Loading release…</p>
            <template v-else-if="cur">
                <!-- Overview -->
                <template v-if="tab === 'overview'">
                    <dl class="grid">
                        <dt class="lbl">Description</dt>
                        <dd>{{ cur.description || '—' }}</dd>
                        <dt class="lbl">Updated</dt>
                        <dd>{{ fmt(cur.updated) }}</dd>
                        <dt class="lbl">Chart</dt>
                        <dd class="mono">{{ cur.chart }} {{ cur.chartVersion }}</dd>
                        <dt class="lbl">App version</dt>
                        <dd class="mono">{{ cur.appVersion || '—' }}</dd>
                    </dl>
                    <h3>Hooks</h3>
                    <p v-if="!cur.hooks?.length" class="muted small">No hooks.</p>
                    <table v-else class="tbl">
                        <thead>
                            <tr>
                                <th>Name</th>
                                <th>Kind</th>
                                <th>Events</th>
                                <th class="r">Weight</th>
                            </tr>
                        </thead>
                        <tbody>
                            <tr v-for="h in cur.hooks" :key="h.name + h.kind">
                                <td class="mono">{{ h.name }}</td>
                                <td>{{ h.kind }}</td>
                                <td class="mono">{{ h.events.join(', ') }}</td>
                                <td class="r">{{ h.weight }}</td>
                            </tr>
                        </tbody>
                    </table>
                </template>

                <!-- History -->
                <template v-else-if="tab === 'history'">
                    <div class="bar">
                        <button
                            class="btn sm"
                            type="button"
                            :disabled="picked == null"
                            @click="dialog = { action: 'rollback', revision: picked ?? undefined }"
                        >
                            Roll back to this revision…
                        </button>
                        <button class="btn sm" type="button" :disabled="ticked.length !== 2" data-testid="helm-compare" @click="compare">
                            Compare
                        </button>
                        <span class="muted small">Pick one revision to roll back to; tick two to compare.</span>
                    </div>
                    <div class="hist" role="group" aria-label="Revisions">
                        <table class="tbl">
                            <thead>
                                <tr>
                                    <th class="c1"><span class="sr">Compare</span></th>
                                    <th>Revision</th>
                                    <th>Status</th>
                                    <th>Chart</th>
                                    <th>App</th>
                                    <th>Updated</th>
                                    <th>Description</th>
                                </tr>
                            </thead>
                            <tbody>
                                <tr
                                    v-for="h in sortedHistory"
                                    :key="h.revision"
                                    data-testid="helm-history-row"
                                    :class="{ on: picked === h.revision }"
                                    :aria-selected="picked === h.revision"
                                    @click="picked = canRollbackTo(rel.history, rel.revision, h.revision) ? h.revision : null"
                                >
                                    <td class="c1" @click.stop>
                                        <input
                                            type="checkbox"
                                            :checked="ticked.includes(h.revision)"
                                            :aria-label="`Compare revision ${h.revision}`"
                                            @change="toggleTick(h.revision)"
                                        />
                                    </td>
                                    <td class="mono">{{ h.revision }}</td>
                                    <td><span class="tag" :class="helmStatusTone(h.status)">{{ h.status }}</span></td>
                                    <td class="mono">{{ h.chart || '—' }}</td>
                                    <td class="mono">{{ h.appVersion || '—' }}</td>
                                    <td>{{ fmt(h.updated) }}</td>
                                    <td class="desc">{{ h.description }}</td>
                                </tr>
                            </tbody>
                        </table>
                    </div>
                </template>

                <!-- Values -->
                <template v-else-if="tab === 'values'">
                    <div class="seg" role="group" aria-label="Values source">
                        <button
                            class="btn sm"
                            :class="{ primary: valuesMode === 'user' }"
                            type="button"
                            :aria-pressed="valuesMode === 'user'"
                            @click="valuesMode = 'user'"
                        >
                            User supplied
                        </button>
                        <button
                            class="btn sm"
                            :class="{ primary: valuesMode === 'computed' }"
                            type="button"
                            :aria-pressed="valuesMode === 'computed'"
                            @click="valuesMode = 'computed'"
                        >
                            Computed
                        </button>
                    </div>
                    <p v-if="!valuesText.trim()" class="muted small">No values.</p>
                    <div v-else class="editor">
                        <YamlEditor
                            :key="`${cur.revision}-${valuesMode}`"
                            :initial="valuesText"
                            read-only
                            :label="`${valuesMode} values of ${target.name}`"
                        />
                    </div>
                </template>

                <!-- Manifest -->
                <div v-else-if="tab === 'manifest'" class="editor">
                    <YamlEditor :key="`m-${cur.revision}`" :initial="cur.manifest" read-only :label="`Manifest of ${target.name}`" />
                </div>

                <!-- Notes -->
                <template v-else-if="tab === 'notes'">
                    <pre v-if="cur.notes" class="notes mono">{{ cur.notes }}</pre>
                    <p v-else class="muted small">This chart has no notes.</p>
                </template>

                <!-- Diff -->
                <template v-else>
                    <div class="bar">
                        <label class="small"
                            >From <SmartSelect v-model="diffFrom" :options="diffChoices" aria-label="From revision" @change="diff = null"
                        /></label>
                        <label class="small"
                            >To <SmartSelect v-model="diffTo" :options="diffChoices" aria-label="To revision" @change="diff = null"
                        /></label>
                        <button class="btn sm primary" type="button" :disabled="diffBusy || diffFrom === diffTo" @click="runDiff">
                            {{ diffBusy ? 'Comparing…' : 'Compare' }}
                        </button>
                    </div>
                    <p v-if="diffErr" class="notice danger" role="alert">{{ diffErr }}</p>
                    <p v-else-if="diffFrom === diffTo" class="muted small">Pick two different revisions.</p>
                    <template v-if="diff">
                        <h3>Values</h3>
                        <div data-testid="helm-diff-values">
                            <p v-if="!diff.values.trim()" class="muted small">No differences.</p>
                            <DiffView
                                v-else
                                :left="sides(diff.values).left"
                                :right="sides(diff.values).right"
                                :left-label="`Revision ${diffFrom}`"
                                :right-label="`Revision ${diffTo}`"
                            />
                        </div>
                        <h3>Manifest</h3>
                        <div data-testid="helm-diff-manifest">
                            <p v-if="!diff.manifest.trim()" class="muted small">No differences.</p>
                            <DiffView
                                v-else
                                :left="sides(diff.manifest).left"
                                :right="sides(diff.manifest).right"
                                :left-label="`Revision ${diffFrom}`"
                                :right-label="`Revision ${diffTo}`"
                            />
                        </div>
                    </template>
                </template>
            </template>
        </div>

        <HelmActionDialog
            v-if="dialog && rel"
            :target="target"
            :action="dialog.action"
            :release="rel"
            :revision="dialog.revision"
            @close="dialog = null"
            @done="reload"
        />
    </section>
</template>

<style scoped>
.hd {
    display: flex;
    flex-direction: column;
    min-height: 0;
    flex: 1 1 0;
}
header {
    padding: 10px 16px 8px;
    display: flex;
    flex-direction: column;
    gap: 6px;
}
.title,
.sub,
.actions,
.bar {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
    flex-wrap: wrap;
}
.grow {
    flex: 1;
}
.small {
    font-size: 12px;
}
h2 {
    margin: 0;
    font-size: 17px;
    font-weight: 600;
    min-width: 0;
}
h3 {
    margin: 8px 0 0;
    font-size: 13px;
    font-weight: 600;
}
.tag.link {
    cursor: pointer;
    font: inherit;
}
.revsel {
    display: flex;
    gap: 8px;
    align-items: center;
}
.tabs {
    display: flex;
    gap: 2px;
    padding: 0 12px;
    border-bottom: 1px solid var(--border);
    overflow-x: auto;
}
.tab {
    all: unset;
    box-sizing: border-box;
    padding: 8px 10px;
    font-size: 13px;
    font-weight: 500;
    color: var(--text2);
    cursor: pointer;
    border-bottom: 2px solid transparent;
    white-space: nowrap;
}
.tab.on {
    color: var(--text);
    border-bottom-color: var(--accent);
}
.tab:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
}
.panel {
    flex: 1 1 0;
    min-height: 0;
    overflow: auto;
    padding: 14px 16px;
    display: flex;
    flex-direction: column;
    gap: 12px;
}
.panel > * {
    flex: none;
}
.panel > .editor {
    flex: 1 1 0;
    min-height: 280px;
}
.editor {
    border: 1px solid var(--border);
    border-radius: 8px;
    overflow: hidden;
    flex: 1 1 0;
    min-height: 280px;
}
.grid {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 6px 14px;
    margin: 0;
    font-size: 13px;
}
.grid dd {
    margin: 0;
    min-width: 0;
    word-break: break-word;
}
.tbl {
    width: 100%;
    border-collapse: collapse;
    font-size: 12px;
}
.tbl th {
    text-align: left;
    font-size: 11px;
    font-weight: 600;
    color: var(--text3);
    padding: 4px 8px;
    position: sticky;
    top: 0;
    background: var(--surface);
}
.tbl td {
    padding: 5px 8px;
    border-top: 1px solid var(--border);
    vertical-align: top;
}
.tbl .r {
    text-align: right;
}
.hist {
    max-height: 320px;
    overflow: auto;
    border: 1px solid var(--border);
    border-radius: 8px;
}
.hist tbody tr {
    cursor: pointer;
}
.hist tbody tr.on {
    background: var(--accent-soft);
}
.c1 {
    width: 28px;
}
.desc {
    word-break: break-word;
}
.sr {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
}
.seg {
    display: flex;
    gap: 4px;
}
.notes {
    margin: 0;
    white-space: pre-wrap;
    word-break: break-word;
    font-size: 12px;
}
@media (pointer: coarse) {
    .btn,
    .tab {
        min-height: 44px;
    }
    .tbl td input {
        width: 22px;
        height: 22px;
    }
}
</style>
