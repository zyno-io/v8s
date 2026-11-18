<script setup lang="ts">
import type { DrainOptions, DrainPod, DrainPreflightResult, JobFrame } from '@v8s/client';

import { RpcError } from '@v8s/client';
import { computed, onBeforeUnmount, onMounted, reactive, ref, shallowRef, watch } from 'vue';

import JobProgress from '@/components/JobProgress.vue';
import { newRequestId } from '@/lib/actions.ts';
import { getAgent } from '@/lib/agent.ts';
import { ask } from '@/lib/ask.ts';
import { cancelJob, CANCEL_CONFIRM, drainParams, isActive, jobLabel, preflightSummary, startJob } from '@/lib/jobs.ts';
import { errorText, isCancelled, newWriteDecision } from '@/lib/mutations.ts';
import { useClustersStore } from '@/stores/clusters.ts';

const props = defineProps<{ cluster: string; node: string; jobId?: string }>();
const emit = defineEmits<{ close: [] }>();

const MAX_ROWS = 200;
const clusters = useClustersStore();
const el = ref<HTMLDialogElement | null>(null);
let returnFocus: HTMLElement | null = null;

const step = ref<'options' | 'confirm' | 'progress'>(props.jobId ? 'progress' : 'options');
const opts = reactive({ ignoreDaemonSets: true, deleteEmptyDirData: false, force: false, grace: '', timeout: '300', disableEviction: false });
const typed = ref('');
const busy = ref(false);
const startErr = ref<{ message: string; hint?: string } | null>(null);

const options = computed<DrainOptions>(() => {
    const o: DrainOptions = {
        ignoreDaemonSets: opts.ignoreDaemonSets,
        deleteEmptyDirData: opts.deleteEmptyDirData,
        force: opts.force,
        disableEviction: opts.disableEviction
    };
    const g = opts.grace.trim();
    if (g !== '' && Number.isFinite(Number(g))) o.gracePeriodSeconds = Number(g);
    const t = Number(opts.timeout);
    if (opts.timeout.trim() !== '' && Number.isFinite(t) && t > 0) o.timeoutSeconds = t;
    return o;
});

// ---- preflight ----
const pre = shallowRef<DrainPreflightResult | null>(null);
const preErr = ref('');
const preLoading = ref(false);
let seq = 0;
let timer: ReturnType<typeof setTimeout> | undefined;

async function runPreflight() {
    const mine = ++seq;
    preLoading.value = true;
    try {
        const res = await getAgent().call<DrainPreflightResult>('drain.preflight', drainParams(props.cluster, props.node, options.value));
        if (mine !== seq) return;
        pre.value = res;
        preErr.value = '';
    } catch (e) {
        if (mine !== seq) return;
        preErr.value = errorText(e);
    } finally {
        if (mine === seq) preLoading.value = false;
    }
}

// A changed option is a new intent: a failed start must not reuse the old requestId.
let requestId = newRequestId();
const writeDecision = newWriteDecision();
watch(options, () => {
    requestId = newRequestId();
    startErr.value = null;
    if (step.value !== 'options') return;
    clearTimeout(timer);
    timer = setTimeout(runPreflight, 250);
});

const summary = computed(() => (pre.value ? preflightSummary(pre.value, options.value) : null));
const clusterName = computed(() => clusters.nameById.get(props.cluster) ?? props.cluster);
const isProt = computed(() => clusters.isProtected(props.cluster));
const needsTyped = computed(() => isProt.value || !!opts.disableEviction);
const typedOk = computed(() => !needsTyped.value || typed.value.trim() === props.node);
const startDisabled = computed(
    () => busy.value || !pre.value || preLoading.value || !!summary.value?.blockers.length || (step.value === 'confirm' ? !typedOk.value : false)
);
const startReason = computed(() => {
    if (!pre.value) return 'Waiting for the pre-flight check.';
    if (summary.value?.blockers.length) return summary.value.blockers.map(b => b.message).join(' ');
    return '';
});
const runsAs = computed(() => {
    if (!pre.value) return '';
    const wc = clusters.writeContextOf(props.cluster);
    const used = pre.value.usedContext;
    if (wc && used === wc) return `Runs as ${wc} (the linked write context)`;
    return `Runs as ${used || 'your current context'}`;
});

interface Section {
    key: string;
    title: string;
    tone: string;
    pods: DrainPod[];
    note: string;
}
const sections = computed<Section[]>(() => {
    const r = pre.value;
    if (!r) return [];
    const o = options.value;
    return [
        { key: 'evict', title: 'Will be evicted', tone: 'success', pods: r.evict, note: '' },
        {
            key: 'blocked',
            title: 'Blocked by a PodDisruptionBudget',
            tone: 'warning',
            pods: r.blocked,
            note: o.disableEviction
                ? 'Eviction is disabled, so these are deleted regardless.'
                : 'The drain will wait and retry every 5 s until the timeout.'
        },
        {
            key: 'unmanaged',
            title: 'Unmanaged pods (lost for good)',
            tone: 'danger',
            pods: r.unmanaged,
            note: o.force
                ? 'Forced: these are deleted and nothing recreates them.'
                : 'Needs "Force", otherwise the drain fails before evicting anything.'
        },
        {
            key: 'emptydir',
            title: 'emptyDir data lost',
            tone: 'warning',
            pods: r.emptyDir,
            note: o.deleteEmptyDirData ? '' : 'Needs "Delete emptyDir data", otherwise the drain fails before evicting anything.'
        },
        {
            key: 'daemonsets',
            title: 'DaemonSets (skipped)',
            tone: 'neutral',
            pods: r.daemonSets,
            note: o.ignoreDaemonSets === false ? 'Not ignored: the drain fails while these exist.' : ''
        },
        { key: 'mirror', title: 'Mirror pods (skipped)', tone: 'neutral', pods: r.mirror, note: '' },
        { key: 'finished', title: 'Finished pods (deleted)', tone: 'neutral', pods: r.finished, note: '' }
    ].filter(s => s.pods?.length);
});
const expanded = ref<Record<string, boolean>>({});
const toggle = (k: string) => (expanded.value = { ...expanded.value, [k]: !expanded.value[k] });

// ---- job ----
const activeJobId = ref(props.jobId ?? '');
const job = shallowRef<JobFrame | null>(null);
watch(
    () => [getAgent().jobs.value, activeJobId.value] as const,
    ([list, id]) => {
        job.value = id ? (list.find(j => j.jobId === id) ?? null) : null;
    },
    { immediate: true }
);

async function start() {
    if (startDisabled.value) return;
    if (step.value === 'options' && opts.disableEviction) {
        step.value = 'confirm';
        typed.value = '';
        return;
    }
    busy.value = true;
    startErr.value = null;
    try {
        const res = await startJob(props.cluster, 'drain', drainParams(props.cluster, props.node, options.value), {
            requestId,
            decision: writeDecision,
            onRequestId: id => (requestId = id)
        });
        activeJobId.value = res.jobId;
        step.value = 'progress';
    } catch (e) {
        if (!isCancelled(e)) startErr.value = { message: errorText(e), hint: e instanceof RpcError ? e.hint : undefined };
    } finally {
        busy.value = false;
    }
}

async function cancel() {
    const j = job.value;
    if (!j) return;
    const ok = await ask({ title: `Cancel ${jobLabel(j)}?`, body: CANCEL_CONFIRM, confirmLabel: 'Cancel drain', danger: true });
    if (!ok) return;
    try {
        await cancelJob(j.jobId);
    } catch (e) {
        startErr.value = { message: errorText(e) };
    }
}

function back() {
    step.value = 'options';
    typed.value = '';
}

onMounted(() => {
    returnFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    if (!el.value?.open) el.value?.showModal();
    el.value?.querySelector<HTMLElement>('input, button.primary, button')?.focus();
    if (step.value === 'options') runPreflight();
});
onBeforeUnmount(() => {
    clearTimeout(timer);
    seq++;
    if (el.value?.open) el.value.close();
    returnFocus?.focus?.();
});
</script>

<template>
    <dialog
        ref="el"
        class="dlg floating"
        data-testid="drain-dialog"
        aria-labelledby="drain-title"
        @cancel.prevent="emit('close')"
        @click.self="emit('close')"
    >
        <div class="body">
            <h2 id="drain-title">
                {{ step === 'confirm' ? `Confirm: delete pods on ${node} (${clusterName})` : `Drain ${node} on ${clusterName}` }}
                <span v-if="step === 'options' && pre?.cordoned" class="tag warning">already cordoned</span>
            </h2>

            <!-- Step 1: options and pre-flight -->
            <template v-if="step === 'options'">
                <p class="muted">
                    Cordons <b class="mono">{{ node }}</b> on <b>{{ clusterName }}</b>, then evicts its pods the way
                    <span class="mono">kubectl drain</span> does.
                </p>
                <p v-if="runsAs" class="runas" data-testid="runas">{{ runsAs }}</p>
                <p v-if="preErr" class="err" role="alert">{{ preErr }}</p>
                <p v-else-if="!pre" class="muted" role="status">Checking pods on the node…</p>

                <div v-if="pre" class="sections" :aria-busy="preLoading">
                    <section v-for="s in sections" :key="s.key" :data-testid="`drain-section-${s.key}`" :class="['sec', s.tone]">
                        <button type="button" class="sechead" :aria-expanded="!!expanded[s.key]" @click="toggle(s.key)">
                            <span class="chev" aria-hidden="true">{{ expanded[s.key] ? '▾' : '▸' }}</span>
                            <span class="stitle">{{ s.title }}</span>
                            <span class="tag" :class="s.tone">{{ s.pods.length }}</span>
                        </button>
                        <p v-if="s.note" class="snote">{{ s.note }}</p>
                        <ul v-if="expanded[s.key]" class="pods">
                            <li v-for="p in s.pods.slice(0, MAX_ROWS)" :key="p.namespace + '/' + p.name">
                                <span class="mono">{{ p.namespace }}/{{ p.name }}</span>
                                <span v-if="p.owner" class="muted">{{ p.owner }}</span>
                                <span v-if="p.pdb" class="tag" :class="p.pdb.disruptionsAllowed === 0 ? 'warning' : ''"
                                    >PDB {{ p.pdb.name }}: {{ p.pdb.disruptionsAllowed }} allowed</span
                                >
                                <span v-if="p.emptyDir" class="tag">emptyDir</span>
                            </li>
                            <li v-if="s.pods.length > MAX_ROWS" class="muted">and {{ s.pods.length - MAX_ROWS }} more</li>
                        </ul>
                    </section>
                    <p v-if="!sections.length" class="muted">No pods on this node.</p>
                </div>

                <fieldset class="opts">
                    <legend>Options</legend>
                    <label><input v-model="opts.ignoreDaemonSets" type="checkbox" /> Ignore DaemonSets</label>
                    <label><input v-model="opts.deleteEmptyDirData" type="checkbox" /> Delete emptyDir data</label>
                    <label><input v-model="opts.force" type="checkbox" /> Force (pods without a controller)</label>
                    <div class="nums">
                        <label
                            >Grace period (s)
                            <span class="field"
                                ><input
                                    v-model="opts.grace"
                                    type="number"
                                    min="0"
                                    inputmode="numeric"
                                    placeholder="pod's own"
                                    aria-label="Grace period seconds"
                            /></span>
                        </label>
                        <label
                            >Timeout (s)
                            <span class="field"
                                ><input v-model="opts.timeout" type="number" min="1" inputmode="numeric" aria-label="Timeout seconds"
                            /></span>
                        </label>
                    </div>
                    <label class="dangerous"
                        ><input v-model="opts.disableEviction" type="checkbox" /> Disable eviction (delete pods instead; PDBs are NOT honoured)</label
                    >
                </fieldset>

                <ul v-if="summary?.blockers.length" class="blockers" data-testid="drain-blockers" role="status">
                    <li v-for="b in summary.blockers" :key="b.option">{{ b.message }}</li>
                </ul>
                <p v-for="w in summary?.warnings ?? []" :key="w" class="notice warn">{{ w }}</p>

                <label v-if="isProt && !opts.disableEviction" class="phrase">
                    <span>Protected cluster <b>{{ clusterName }}</b>. Type <b class="mono">{{ node }}</b> to confirm</span>
                    <span class="field"><input v-model="typed" type="text" autocomplete="off" spellcheck="false" aria-label="Node name" /></span>
                </label>
            </template>

            <!-- Step 2: second confirmation for disable eviction -->
            <template v-else-if="step === 'confirm'">
                <p class="notice danger-n" role="alert">
                    <span
                        ><b>Eviction is disabled</b> on <b>{{ clusterName }}</b>. Pods on {{ node }} are deleted directly: PodDisruptionBudgets are
                        NOT honoured and the pods get no graceful handover. Workloads can lose availability.</span
                    >
                </p>
                <label class="phrase">
                    <span>Type <b class="mono">{{ node }}</b> on <b>{{ clusterName }}</b> to confirm</span>
                    <span class="field"><input v-model="typed" type="text" autocomplete="off" spellcheck="false" aria-label="Node name" /></span>
                </label>
            </template>

            <!-- Progress -->
            <template v-else>
                <div data-testid="drain-progress">
                    <JobProgress v-if="job" :job="job" />
                    <p v-else class="muted" role="status">Job {{ activeJobId }} is not known to the agent (it may have been cleaned up).</p>
                </div>
                <p v-if="job?.usedContext" class="muted">Ran as {{ job.usedContext }}</p>
            </template>

            <p v-if="startErr" class="err" role="alert">{{ startErr.message }}<span v-if="startErr.hint" class="hint"> {{ startErr.hint }}</span></p>

            <div class="foot">
                <template v-if="step === 'progress'">
                    <button v-if="job && isActive(job)" class="btn danger" type="button" data-testid="drain-cancel" @click="cancel">
                        Cancel drain
                    </button>
                    <button class="btn primary" type="button" @click="emit('close')">Close</button>
                </template>
                <template v-else>
                    <button v-if="step === 'confirm'" class="btn" type="button" @click="back">Back</button>
                    <button class="btn" type="button" @click="emit('close')">Cancel</button>
                    <button
                        class="btn"
                        :class="step === 'confirm' ? 'danger' : 'primary'"
                        type="button"
                        data-testid="drain-start"
                        :disabled="startDisabled || (step === 'options' && !opts.disableEviction && !typedOk)"
                        :title="startReason"
                        @click="start"
                    >
                        {{ busy ? 'Starting…' : step === 'confirm' ? 'Delete pods and drain' : opts.disableEviction ? 'Continue…' : 'Start drain' }}
                    </button>
                </template>
            </div>
        </div>
    </dialog>
</template>

<style scoped>
.dlg {
    padding: 0;
    width: min(680px, calc(100vw - 24px));
    max-height: calc(100vh - 32px);
    color: var(--text);
    border-radius: 12px;
}
.dlg::backdrop {
    background: rgba(12, 13, 16, 0.45);
}
.body {
    padding: 18px 18px 14px;
    display: flex;
    flex-direction: column;
    gap: 12px;
}
h2 {
    margin: 0;
    font-size: 16px;
    font-weight: 600;
    display: flex;
    gap: 8px;
    align-items: center;
    flex-wrap: wrap;
}
p {
    margin: 0;
    font-size: 13px;
    color: var(--text2);
    word-break: break-word;
}
.runas {
    font-weight: 500;
    color: var(--text);
}
.err {
    color: var(--danger);
}
.hint {
    color: var(--text2);
}
.sections {
    display: flex;
    flex-direction: column;
    gap: 6px;
    max-height: 38vh;
    overflow: auto;
}
.sec {
    border: 1px solid var(--neutral-b);
    border-radius: 8px;
    padding: 4px 10px;
}
.sec.warning {
    border-color: var(--warning-b);
}
.sec.danger {
    border-color: var(--danger-b);
}
.sechead {
    all: unset;
    box-sizing: border-box;
    display: flex;
    gap: 8px;
    align-items: center;
    width: 100%;
    min-height: 30px;
    cursor: pointer;
    font-size: 13px;
    font-weight: 500;
}
.sechead:focus-visible {
    outline: 2px solid var(--accent, currentColor);
    outline-offset: 2px;
}
.stitle {
    flex: 1;
}
.snote {
    font-size: 12px;
    padding-bottom: 4px;
}
.pods {
    list-style: none;
    margin: 0 0 4px;
    padding: 0;
    max-height: 200px;
    overflow: auto;
    font-size: 12px;
}
.pods li {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;
    padding: 3px 0;
}
.opts {
    border: 1px solid var(--neutral-b);
    border-radius: 8px;
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 8px 12px 10px;
    font-size: 13px;
}
.opts label {
    display: flex;
    gap: 8px;
    align-items: center;
}
.nums {
    display: flex;
    gap: 16px;
    flex-wrap: wrap;
}
.nums label {
    flex-direction: column;
    align-items: flex-start;
    gap: 4px;
}
.dangerous {
    color: var(--danger);
}
.blockers {
    margin: 0;
    padding: 8px 10px 8px 26px;
    border: 1px solid var(--danger-b);
    background: var(--danger-soft);
    color: var(--danger);
    border-radius: 8px;
    font-size: 13px;
}
.danger-n {
    border-color: var(--danger-b);
    background: var(--danger-soft);
    color: var(--text);
}
.phrase {
    display: flex;
    flex-direction: column;
    gap: 6px;
    font-size: 13px;
}
.foot {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    flex-wrap: wrap;
}
@media (pointer: coarse) {
    .sechead,
    .opts label {
        min-height: 44px;
    }
    input[type='checkbox'] {
        width: 22px;
        height: 22px;
    }
}
</style>
