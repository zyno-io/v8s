<script setup lang="ts">
import type { HelmRelease, JobFrame, ObjectRef, RpcError } from '@v8s/client';

import { computed, nextTick, onMounted, ref, watch } from 'vue';
import { useRouter } from 'vue-router';

import HoldButton from '@/components/HoldButton.vue';
import JobProgress from '@/components/JobProgress.vue';
import SmartSelect from '@/components/SmartSelect.vue';
import { checkAccess } from '@/lib/access.ts';
import { newRequestId } from '@/lib/actions.ts';
import { getAgent } from '@/lib/agent.ts';
import { openObject } from '@/lib/drawer.ts';
import { isPending, latestRevisionOptions } from '@/lib/helm.ts';
import { cancelJob, isActive, startJob } from '@/lib/jobs.ts';
import { callMutating, errorText, runAs } from '@/lib/mutations.ts';
import { listOptions, type SelectOption } from '@/lib/smart-select.ts';
import { isPhone } from '@/lib/viewport.ts';
import { useClustersStore } from '@/stores/clusters.ts';
import { useUiStore } from '@/stores/ui.ts';

const props = defineProps<{ target: ObjectRef; action: 'rollback' | 'uninstall' | 'retry'; release: HelmRelease; revision?: number }>();
const emit = defineEmits<{ close: []; done: [] }>();

const FLUX_GVR = 'helm.toolkit.fluxcd.io/v2/helmreleases';
type FluxAction = 'flux.reconcile' | 'flux.force' | 'flux.reset';

const router = useRouter();
const clusters = useClustersStore();
const ui = useUiStore();
const el = ref<HTMLDialogElement | null>(null);

const targets = computed(() => latestRevisionOptions(props.release.history).filter(o => o.revision !== props.release.revision));
const targetOptions = computed(() => targets.value.map(o => ({ value: o.revision, label: o.label })));
const CASCADE_OPTIONS = listOptions(['background', 'foreground', 'orphan'] as const);
const FLUX_ACTION_OPTIONS: SelectOption<FluxAction>[] = [
    { value: 'flux.reconcile', label: 'Reconcile' },
    { value: 'flux.force', label: 'Force' },
    { value: 'flux.reset', label: 'Reset failures' }
];
const revision = ref<number>(props.revision ?? targets.value[0]?.revision ?? 0);
const wait = ref(props.action !== 'uninstall');
const timeout = ref(300);
const cleanupOnFail = ref(false);
const forceReplace = ref(false);
const keepHistory = ref(false);
const cascade = ref<'background' | 'foreground' | 'orphan'>('background');
const markFailed = ref(isPending(props.release.status));
const typedName = ref('');
const typedNs = ref('');
const fluxAction = ref<FluxAction>('flux.reconcile');

const phase = ref<'form' | 'gitops' | 'force' | 'running'>('form');
const busy = ref(false);
const err = ref('');
const gitopsHint = ref('');
const jobId = ref('');

const isProt = computed(() => clusters.isProtected(props.target.cluster));
const viaWrite = ref(false);
const runsAs = computed(() => runAs(props.target.cluster, viaWrite.value));
const ns = computed(() => props.target.namespace ?? props.release.namespace);
const flux = computed(() => props.release.managedBy);
// A phone rolls back by holding the button (uninstall and protected clusters already ask for typed names).
const holdStart = computed(() => isPhone.value && props.action === 'rollback' && !isProt.value);
const title = computed(() => ({ rollback: 'Roll back', uninstall: 'Uninstall', retry: 'Retry' })[props.action]);
const job = computed<JobFrame | undefined>(() => getAgent().jobs.value.find(j => j.jobId === jobId.value));
const finished = computed(() => !!job.value && !isActive(job.value));
const timeoutOk = computed(() => Number.isInteger(Number(timeout.value)) && Number(timeout.value) >= 0);
// The release changed once the job settles: let the drawer reload.
watch(finished, f => f && emit('done'));
const canStart = computed(() => {
    if (busy.value || !timeoutOk.value) return false;
    if (props.action === 'rollback' && !revision.value) return false;
    // Protected clusters: every release-changing action types the release name (uninstall also the namespace), like ActionBar.
    if (isProt.value && typedName.value.trim() !== props.release.name) return false;
    if (props.action === 'uninstall') {
        if (typedName.value.trim() !== props.release.name) return false;
        if (isProt.value && typedNs.value.trim() !== ns.value) return false;
    }
    return true;
});

function params(force: boolean): Record<string, unknown> {
    const base = {
        cluster: props.target.cluster,
        namespace: ns.value,
        name: props.release.name,
        wait: wait.value,
        timeout: Number(timeout.value) || 300
    };
    let p: Record<string, unknown>;
    if (props.action === 'rollback') p = { ...base, revision: revision.value, cleanupOnFail: cleanupOnFail.value, forceReplace: forceReplace.value };
    else if (props.action === 'uninstall') p = { ...base, keepHistory: keepHistory.value, cascade: cascade.value };
    else p = { ...base, markFailed: markFailed.value };
    if (force) p.force = true;
    return p;
}

async function start(force = false) {
    if (busy.value) return;
    busy.value = true;
    err.value = '';
    try {
        const res = await startJob(props.target.cluster, `helm.${props.action}`, params(force));
        jobId.value = res.jobId;
        phase.value = 'running';
        emit('done');
    } catch (e) {
        const re = e as RpcError;
        if (re?.code === 'gitops_managed') {
            gitopsHint.value = re.hint ?? '';
            phase.value = 'gitops';
        } else {
            err.value = errorText(e);
            phase.value = 'form';
        }
    } finally {
        busy.value = false;
    }
}

async function useFlux() {
    const f = flux.value;
    if (!f || busy.value) return;
    busy.value = true;
    err.value = '';
    try {
        const ref: ObjectRef = { cluster: props.target.cluster, gvr: FLUX_GVR, namespace: f.namespace, name: f.name };
        await callMutating(props.target.cluster, 'object.action', { requestId: newRequestId(), ref, action: fluxAction.value });
        ui.toast('success', `${fluxAction.value.replace('flux.', '')} requested on HelmRelease ${f.name}`);
        emit('done');
        emit('close');
    } catch (e) {
        err.value = errorText(e);
    } finally {
        busy.value = false;
    }
}

function openFlux() {
    const f = flux.value;
    if (f) void openObject(router, { cluster: props.target.cluster, gvr: FLUX_GVR, namespace: f.namespace, name: f.name });
    emit('close');
}

async function cancel() {
    if (jobId.value) {
        try {
            await cancelJob(jobId.value);
        } catch (e) {
            err.value = errorText(e);
        }
    }
}

onMounted(async () => {
    await nextTick();
    el.value?.showModal();
    el.value?.querySelector<HTMLElement>('[data-select], input, button.primary, button.danger')?.focus();
    try {
        const [a] = await checkAccess([
            { cluster: props.target.cluster, verb: props.action === 'uninstall' ? 'delete' : 'create', gvr: 'v1/secrets', namespace: ns.value }
        ]);
        viaWrite.value = !!a?.viaWrite;
    } catch {
        // Unknown: the dialog keeps saying "your current context".
    }
});
</script>

<template>
    <dialog
        ref="el"
        class="dlg floating"
        aria-labelledby="helm-dlg-title"
        data-testid="helm-action-dialog"
        @cancel.prevent="emit('close')"
        @click.self="emit('close')"
    >
        <form method="dialog" @submit.prevent="phase === 'form' && canStart && !holdStart && start()">
            <h2 id="helm-dlg-title">{{ title }} {{ release.name }}</h2>
            <p class="muted small">
                <span class="mono">{{ ns }}</span> on {{ clusters.nameById.get(target.cluster) ?? target.cluster }} · revision
                {{ release.revision }} ({{ release.status }})
            </p>

            <template v-if="phase === 'form'">
                <p v-if="action === 'rollback'">Restores the manifest and values of an earlier revision as a new revision.</p>
                <p v-else-if="action === 'retry'">Re-runs the last upgrade from the stored chart and values.</p>
                <p v-else>Removes the release and the resources it created.</p>

                <label v-if="action === 'rollback'" class="fld"
                    >Roll back to revision
                    <SmartSelect v-model="revision" :options="targetOptions" block aria-label="Revision" data-testid="helm-revision" />
                </label>
                <label v-if="action === 'retry' && isPending(release.status)" class="chk"
                    ><input v-model="markFailed" type="checkbox" /> Mark the pending revision failed first
                    <span class="muted small">(Helm refuses a new operation while a revision is pending)</span></label
                >
                <label v-else-if="action === 'retry'" class="chk"
                    ><input v-model="markFailed" type="checkbox" /> Mark the last revision failed first</label
                >
                <template v-if="action === 'uninstall'">
                    <label class="chk"><input v-model="keepHistory" type="checkbox" /> Keep history</label>
                    <label class="fld"
                        >Delete propagation
                        <SmartSelect v-model="cascade" :options="CASCADE_OPTIONS" block aria-label="Cascade" />
                    </label>
                </template>
                <label class="chk"><input v-model="wait" type="checkbox" /> Wait until ready</label>
                <label class="fld"
                    >Timeout (seconds)
                    <span class="field"
                        ><input v-model.number="timeout" type="number" min="0" step="1" inputmode="numeric" aria-label="Timeout seconds"
                    /></span>
                </label>
                <label v-if="action === 'rollback'" class="chk"
                    ><input v-model="cleanupOnFail" type="checkbox" /> Clean up new resources on failure</label
                >
                <template v-if="action === 'rollback'">
                    <label class="chk"
                        ><input v-model="forceReplace" type="checkbox" data-testid="helm-force-replace" /> Force resource replacement</label
                    >
                    <p v-if="forceReplace" class="notice warn" role="note">
                        Force replacement deletes and recreates resources that cannot be patched, which can cause downtime.
                    </p>
                </template>
                <template v-if="action === 'uninstall' || isProt">
                    <label class="fld"
                        >{{ isProt ? 'This is a protected cluster. ' : '' }}Type <b class="mono">{{ release.name }}</b> to confirm
                        <span class="field"
                            ><input
                                v-model="typedName"
                                type="text"
                                autocomplete="off"
                                spellcheck="false"
                                aria-label="Release name"
                                data-testid="helm-confirm-name"
                        /></span>
                    </label>
                    <label v-if="isProt && action === 'uninstall'" class="fld"
                        >This is a protected cluster. Type the namespace <b class="mono">{{ ns }}</b>
                        <span class="field"
                            ><input
                                v-model="typedNs"
                                type="text"
                                autocomplete="off"
                                spellcheck="false"
                                aria-label="Namespace"
                                data-testid="helm-confirm-ns"
                        /></span>
                    </label>
                </template>
                <p class="runas" data-testid="runas">{{ runsAs }}</p>
                <p v-if="err" class="err" role="alert">{{ err }}</p>
                <div class="foot">
                    <button class="btn" type="button" @click="emit('close')">Cancel</button>
                    <HoldButton v-if="holdStart" label="Roll back" :danger="false" :disabled="!canStart" @confirm="start()" />
                    <button
                        v-else
                        class="btn"
                        :class="action === 'uninstall' ? 'danger' : 'primary'"
                        type="submit"
                        :disabled="!canStart"
                        data-testid="helm-start"
                    >
                        {{ busy ? 'Starting…' : title }}
                    </button>
                </div>
            </template>

            <template v-else-if="phase === 'gitops'">
                <div class="notice warn" role="alert" data-testid="helm-gitops-notice">
                    <div>
                        <b>Managed by a Flux HelmRelease.</b>
                        <template v-if="flux"> This release belongs to <span class="mono">{{ flux.namespace }}/{{ flux.name }}</span>.</template>
                        Flux would revert a direct Helm {{ action }}.
                        <div v-if="gitopsHint" class="hint">{{ gitopsHint }}</div>
                    </div>
                </div>
                <label v-if="action !== 'uninstall' && flux" class="fld"
                    >Flux action
                    <SmartSelect v-model="fluxAction" :options="FLUX_ACTION_OPTIONS" block aria-label="Flux action" />
                </label>
                <p v-if="err" class="err" role="alert">{{ err }}</p>
                <div class="foot">
                    <button class="btn" type="button" @click="emit('close')">Cancel</button>
                    <button class="btn" type="button" @click="phase = 'force'" data-testid="helm-force">Do it anyway</button>
                    <button v-if="action === 'uninstall'" class="btn primary" type="button" :disabled="!flux" @click="openFlux">
                        Open the HelmRelease
                    </button>
                    <button v-else class="btn primary" type="button" :disabled="!flux || busy" @click="useFlux">Use Flux instead</button>
                </div>
            </template>

            <template v-else-if="phase === 'force'">
                <div class="notice danger" role="alert">
                    <div>
                        <b>Override the Flux guard?</b> The {{ action }} runs directly through Helm, and Flux may undo it on its next reconcile.
                    </div>
                </div>
                <p v-if="err" class="err" role="alert">{{ err }}</p>
                <div class="foot">
                    <button class="btn" type="button" @click="phase = 'gitops'">Back</button>
                    <button class="btn danger" type="button" :disabled="busy" data-testid="helm-force-confirm" @click="start(true)">
                        {{ busy ? 'Starting…' : `Yes, ${action} anyway` }}
                    </button>
                </div>
            </template>

            <template v-else>
                <div data-testid="helm-job">
                    <JobProgress v-if="job" :job="job" />
                    <p v-else class="muted" role="status">Starting…</p>
                </div>
                <p v-if="err" class="err" role="alert">{{ err }}</p>
                <div class="foot">
                    <button v-if="job && !finished" class="btn danger" type="button" @click="cancel">Cancel job</button>
                    <button class="btn" :class="{ primary: finished }" type="button" @click="emit('close')">Close</button>
                </div>
            </template>
        </form>
    </dialog>
</template>

<style scoped>
.dlg {
    padding: 0;
    width: min(520px, calc(100vw - 24px));
    max-height: calc(100vh - 24px);
    color: var(--text);
    border-radius: 12px;
}
.dlg::backdrop {
    background: rgba(12, 13, 16, 0.45);
}
form {
    padding: 18px 18px 14px;
    display: flex;
    flex-direction: column;
    gap: 12px;
}
h2 {
    margin: 0;
    font-size: 16px;
    font-weight: 600;
}
p {
    margin: 0;
    font-size: 13px;
    color: var(--text2);
    word-break: break-word;
}
.small {
    font-size: 12px;
}
.fld {
    display: flex;
    flex-direction: column;
    gap: 6px;
    font-size: 13px;
}
.chk {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 13px;
    flex-wrap: wrap;
}
.err {
    color: var(--danger);
}
.runas {
    font-weight: 500;
    color: var(--text);
}
.hint {
    margin-top: 6px;
}
.foot {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    flex-wrap: wrap;
}
@media (pointer: coarse) {
    .chk input {
        width: 22px;
        height: 22px;
    }
    .foot .btn {
        min-height: 44px;
    }
}
</style>
