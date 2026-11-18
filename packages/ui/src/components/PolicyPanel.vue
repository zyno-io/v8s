<script setup lang="ts">
import type { KyvernoExceptionTemplateResult, KyvernoSetModeResult, ObjectRef, PolicyResult } from '@v8s/client';

import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue';

import type { Violation } from '@/lib/policy.ts';

import { getAgent, kinds } from '@/lib/agent.ts';
import { ask } from '@/lib/ask.ts';
import { clusterColor } from '@/lib/colors.ts';
import { openCreateWith } from '@/lib/create-prefill.ts';
import { labelFromGvr } from '@/lib/kinds.ts';
import { callMutating, errorText, isCancelled, pool } from '@/lib/mutations.ts';
import { canSetMode, isLegacyPolicyGvr, LEGACY_HINT, modeTag, policyReportGvr, setModeConfirm, violationsOf } from '@/lib/policy.ts';
import { colIdx, objectYaml, reportDetail, reportView, snapshotView } from '@/lib/security-api.ts';
import { sevLabel, sevTone } from '@/lib/security.ts';
import { useClustersStore } from '@/stores/clusters.ts';
import { useUiStore } from '@/stores/ui.ts';

const props = defineProps<{ target: ObjectRef }>();
const emit = defineEmits<{ close: [] }>();

const ui = useUiStore();
const clusters = useClustersStore();
const root = ref<HTMLElement | null>(null);

const SCAN_REPORTS = 40;
const MAX_VIOLATIONS = 200;

const legacy = computed(() => isLegacyPolicyGvr(props.target.gvr));
const settable = computed(() => canSetMode(props.target.gvr));
const kindName = computed(() => {
    return kinds.value.find(x => x.gvr === props.target.gvr)?.kind ?? labelFromGvr(props.target.gvr);
});
const clusterName = computed(() => clusters.nameById.get(props.target.cluster) ?? props.target.cluster);

// ---- summary (the policy's table row) and YAML (object.get, which also yields the uid) ----
const summary = shallowRef<Record<string, string>>({});
const uid = ref('');
const yaml = ref('');
const yamlOpen = ref(false);
const loadError = ref('');
let epoch = 0;

async function loadSummary() {
    const mine = epoch;
    try {
        const q = reportView(props.target.gvr, [{ col: 'name', op: 'eq', value: props.target.name ?? '' }], {
            clusters: [props.target.cluster],
            namespaces: props.target.namespace ? [props.target.namespace] : []
        });
        const snap = await snapshotView(q, 1);
        if (mine !== epoch) return;
        const r = snap.rows[0];
        const out: Record<string, string> = {};
        if (r)
            for (const k of ['mode', 'ready', 'rules', 'background', 'severity', 'message', 'readOnly']) {
                const i = colIdx(snap.columns, k);
                if (i >= 0) out[k] = String(r[i] ?? '');
            }
        summary.value = out;
    } catch {
        // the summary is a convenience: the YAML below still answers
    }
}

async function loadObject() {
    const mine = epoch;
    try {
        const p = await objectYaml(props.target);
        if (mine !== epoch) return;
        uid.value = p.uid;
        yaml.value = p.yaml;
        loadError.value = '';
    } catch (e) {
        if (mine === epoch) loadError.value = errorText(e);
    }
}

// ---- violations: failing policy reports, their policyResults filtered by this policy ----
const violations = shallowRef<Violation[]>([]);
const scanning = ref(false);
const scanned = ref<{ reports: number; limited: boolean } | null>(null);
const scanError = ref('');

async function scan() {
    const mine = epoch;
    const gvr = policyReportGvr(kinds.value);
    scanned.value = null;
    scanError.value = '';
    violations.value = [];
    if (!gvr) {
        scanError.value = 'No policy report kind (PolicyReport or openreports Report) is served by the selected clusters.';
        return;
    }
    scanning.value = true;
    try {
        const snap = await snapshotView(reportView(gvr, [], { clusters: [props.target.cluster], sort: [{ col: 'fail', desc: true }] }), SCAN_REPORTS);
        if (mine !== epoch) return;
        const ix = (k: string) => colIdx(snap.columns, k);
        const [nm, ns, sk, sn, fl, er] = ['name', 'namespace', 'scopeKind', 'scopeName', 'fail', 'error'].map(ix);
        const failing = snap.rows.filter(r => (Number(r[fl]) || 0) + (er >= 0 ? Number(r[er]) || 0 : 0) > 0);
        const out: Violation[] = [];
        const name = props.target.name ?? '';
        await pool(failing, 4, async r => {
            const namespace = ns >= 0 ? String(r[ns]) : '';
            const d = await reportDetail({ cluster: props.target.cluster, gvr, namespace: namespace || undefined, name: String(r[nm]) }, 0, 300);
            for (const result of violationsOf(name, d.results ?? []) as PolicyResult[])
                out.push({
                    result,
                    cluster: props.target.cluster,
                    kind: sk >= 0 ? String(r[sk]) : '',
                    namespace,
                    name: sn >= 0 ? String(r[sn]) : String(r[nm])
                });
        });
        if (mine !== epoch) return;
        violations.value = out.slice(0, MAX_VIOLATIONS);
        scanned.value = { reports: failing.length, limited: snap.total > snap.rows.length && failing.length === snap.rows.length };
    } catch (e) {
        if (mine === epoch) scanError.value = errorText(e);
    } finally {
        if (mine === epoch) scanning.value = false;
    }
}

function start() {
    epoch++;
    summary.value = {};
    uid.value = '';
    yaml.value = '';
    yamlOpen.value = false;
    violations.value = [];
    scanned.value = null;
    void loadSummary();
    void loadObject();
    void scan();
}
watch(() => `${props.target.cluster}|${props.target.gvr}|${props.target.namespace ?? ''}|${props.target.name}`, start, { immediate: true });
onBeforeUnmount(() => epoch++);
onMounted(async () => {
    await nextTick();
    root.value?.focus();
});

// ---- mode switch ----
const mode = computed(() => summary.value.mode ?? '');
const busy = ref(false);
const failCount = computed(() => (scanned.value ? violations.value.length : null));

async function setMode(next: 'Audit' | 'Enforce') {
    if (busy.value || !settable.value) return;
    const text = setModeConfirm({
        policy: props.target.name ?? '',
        cluster: clusterName.value,
        mode: next,
        current: mode.value,
        fails: failCount.value,
        partial: !!scanned.value?.limited || violations.value.length >= MAX_VIOLATIONS,
        protected: clusters.isProtected(props.target.cluster)
    });
    if (!(await ask(text))) return;
    busy.value = true;
    try {
        const res = await callMutating<KyvernoSetModeResult>(props.target.cluster, 'kyverno.setMode', {
            ref: { ...props.target, uid: uid.value || undefined },
            mode: next
        });
        ui.toast('success', `${props.target.name} is now ${res.mode}${res.usedContext ? ` (as ${res.usedContext})` : ''}`);
        summary.value = { ...summary.value, mode: res.mode };
        setTimeout(() => {
            void loadSummary();
            void loadObject();
        }, 1500);
    } catch (e) {
        if (!isCancelled(e)) ui.toast('danger', errorText(e));
    } finally {
        busy.value = false;
    }
}

// ---- exception ----
async function exception(v: Violation) {
    try {
        const res = await getAgent().call<KyvernoExceptionTemplateResult>('kyverno.exceptionTemplate', {
            policy: { ...props.target, uid: uid.value || undefined },
            rule: v.result.rule || undefined,
            resource: { kind: v.kind, namespace: v.namespace || undefined, name: v.name }
        });
        openCreateWith({ yaml: res.yaml, cluster: props.target.cluster }, () => (ui.createOpen = true));
    } catch (e) {
        ui.toast('danger', errorText(e));
    }
}

const tag = computed(() => modeTag(mode.value));
const facts = computed(() => [
    ['Mode', mode.value],
    ['Ready', summary.value.ready],
    ['Rules', summary.value.rules],
    ['Background', summary.value.background]
]);
</script>

<template>
    <aside
        ref="root"
        class="panel floating"
        role="complementary"
        :aria-label="`${kindName} ${target.name}`"
        tabindex="-1"
        data-testid="policy-panel"
        @keydown.esc.stop="emit('close')"
    >
        <header class="shead">
            <div class="row">
                <b class="ttl" :title="target.name">{{ target.name }}</b>
                <span class="tag">{{ kindName }}</span>
                <span v-if="clusters.activeIds.length > 1" class="tag"
                    ><span class="dot" :style="{ background: clusterColor(clusterName) }"></span>{{ clusterName }}</span
                >
                <span v-if="tag" class="tag" :class="tag.tone" data-testid="policy-mode">{{ tag.text }}</span>
                <span v-if="legacy" class="tag" :title="LEGACY_HINT" data-testid="legacy-badge">Read-only</span>
                <div class="grow"></div>
                <button class="btn sm ghost" type="button" aria-label="Close policy details" @click="emit('close')">Close</button>
            </div>
            <p v-if="legacy" class="notice warn" role="note">{{ LEGACY_HINT }}</p>
        </header>
        <div class="body">
            <dl class="facts">
                <template v-for="f in facts" :key="f[0]">
                    <dt class="muted">{{ f[0] }}</dt>
                    <dd>{{ f[1] || '—' }}</dd>
                </template>
                <template v-if="summary.severity">
                    <dt class="muted">Severity</dt>
                    <dd>{{ summary.severity }}</dd>
                </template>
                <template v-if="summary.message">
                    <dt class="muted">Message</dt>
                    <dd>{{ summary.message }}</dd>
                </template>
            </dl>

            <div class="modes" role="group" aria-label="Policy mode">
                <button
                    class="btn"
                    type="button"
                    :disabled="legacy || !settable || busy || mode === 'Audit'"
                    :title="
                        legacy
                            ? LEGACY_HINT
                            : !settable
                              ? 'This kind has no Audit or Enforce mode'
                              : mode === 'Audit'
                                ? 'Already in Audit'
                                : 'Report violations without blocking'
                    "
                    data-testid="mode-audit"
                    @click="setMode('Audit')"
                >
                    Audit
                </button>
                <button
                    class="btn danger"
                    type="button"
                    :disabled="legacy || !settable || busy || mode === 'Enforce'"
                    :title="
                        legacy
                            ? LEGACY_HINT
                            : !settable
                              ? 'This kind has no Audit or Enforce mode'
                              : mode === 'Enforce'
                                ? 'Already Enforce'
                                : 'Block non-compliant admissions'
                    "
                    data-testid="mode-enforce"
                    @click="setMode('Enforce')"
                >
                    Enforce
                </button>
            </div>

            <h3>Violations</h3>
            <p class="muted small" role="status" aria-live="polite">
                <template v-if="scanning">Checking failing reports…</template>
                <template v-else-if="scanError">{{ scanError }}</template>
                <template v-else-if="scanned"
                    >{{ violations.length }} violation{{ violations.length === 1 ? '' : 's' }} in {{ scanned.reports }} failing report{{
                        scanned.reports === 1 ? '' : 's'
                    }}{{ scanned.limited ? ` (only the ${SCAN_REPORTS} reports with most failures were checked)` : '' }}</template
                >
            </p>
            <ul v-if="violations.length" class="vlist" aria-label="Violations">
                <li v-for="(v, i) in violations" :key="i" class="vrow">
                    <div class="vmain">
                        <span class="vres" :title="`${v.kind} ${v.namespace}/${v.name}`"
                            ><span class="muted">{{ v.kind }}</span> {{ v.namespace ? v.namespace + '/' : '' }}{{ v.name }}</span
                        >
                        <span class="vmsg muted" :title="v.result.message"
                            >{{ v.result.rule ? v.result.rule + ': ' : '' }}{{ v.result.message }}</span
                        >
                    </div>
                    <span class="tag" :class="v.result.severity ? sevTone(v.result.severity) : ''">{{
                        v.result.severity ? sevLabel(v.result.severity) : v.result.result
                    }}</span>
                    <button class="btn sm" type="button" :aria-label="`Create exception for ${v.name}`" @click="exception(v)">Exception</button>
                </li>
            </ul>

            <details class="yaml" :open="yamlOpen" @toggle="yamlOpen = ($event.target as HTMLDetailsElement).open">
                <summary>YAML (read-only)</summary>
                <p v-if="loadError" class="notice warn" role="alert">{{ loadError }}</p>
                <pre v-else-if="yamlOpen" class="mono" tabindex="0" data-testid="policy-yaml">{{ yaml || 'Loading…' }}</pre>
            </details>
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
    outline: 0;
}
.shead {
    padding: 12px 14px 10px;
    border-bottom: 1px solid var(--border);
    display: flex;
    flex-direction: column;
    gap: 8px;
}
.row {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
    min-width: 0;
}
.ttl {
    font-size: 15px;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
.body {
    flex: 1 1 0;
    min-height: 0;
    overflow: auto;
    padding: 12px 14px 16px;
    display: flex;
    flex-direction: column;
    gap: 10px;
}
.facts {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 4px 14px;
    margin: 0;
    font-size: 13px;
}
.facts dd {
    margin: 0;
    min-width: 0;
    overflow-wrap: anywhere;
}
.modes {
    display: flex;
    gap: 8px;
}
h3 {
    margin: 6px 0 0;
    font-size: 13px;
    font-weight: 600;
}
.small {
    font-size: 12px;
    margin: 0;
}
.vlist {
    list-style: none;
    margin: 0;
    padding: 0;
    border: 1px solid var(--border);
    border-radius: 8px;
    max-height: 340px;
    overflow: auto;
}
.vrow {
    display: flex;
    align-items: center;
    gap: 8px;
    height: 48px;
    padding: 0 10px;
    border-bottom: 1px solid var(--border);
    font-size: 13px;
}
.vrow:last-child {
    border-bottom: 0;
}
.vmain {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
}
.vres,
.vmsg {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
.vmsg {
    font-size: 12px;
}
.yaml summary {
    cursor: pointer;
    font-size: 13px;
    font-weight: 600;
    padding: 4px 0;
}
.yaml pre {
    margin: 6px 0 0;
    padding: 10px 12px;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--surface);
    max-height: 420px;
    overflow: auto;
    white-space: pre;
}
@media (pointer: coarse) {
    .modes .btn,
    .vrow .btn.sm {
        min-height: 44px;
    }
    .vrow {
        height: 56px;
    }
}
</style>
