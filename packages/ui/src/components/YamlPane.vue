<script setup lang="ts">
import type { ObjectDryRunResult, ObjectPayload, ObjectRef } from '@v8s/client';

import { RpcError } from '@v8s/client';
import { computed, defineAsyncComponent, onBeforeUnmount, ref, shallowRef, watch } from 'vue';
import { useRouter } from 'vue-router';

import { checkAccess } from '@/lib/access.ts';
import { newRequestId } from '@/lib/actions.ts';
import { getAgent } from '@/lib/agent.ts';
import { errorText } from '@/lib/mutations.ts';
import { GITOPS_LABEL } from '@/lib/overview.ts';
import { useClustersStore } from '@/stores/clusters.ts';
import { useUiStore } from '@/stores/ui.ts';

// CodeMirror and the diff view are lazy chunks: nothing here loads until the YAML tab is opened.
const YamlEditor = defineAsyncComponent(() => import('./YamlEditor.vue'));
const DiffView = defineAsyncComponent(() => import('./DiffView.vue'));

const props = defineProps<{ target: ObjectRef; payload: ObjectPayload; deleted: boolean; frames: number }>();

const clusters = useClustersStore();
const ui = useUiStore();
const router = useRouter();

type Mode = 'view' | 'edit' | 'review';
const mode = ref<Mode>('view');
const editor = ref<{ getValue(): string; setValue(t: string): void; focus(): void } | null>(null);

const LARGE = 1024 * 1024;
const isSecret = computed(() => props.target.gvr === 'v1/secrets');
const revealed = ref(false);
const tooLarge = computed(() => props.payload.yaml.length > LARGE);
const hidden = computed(() => isSecret.value && !revealed.value);

// ---- the document the editor shows: live YAML in view mode, the opened version while editing ----
const openedRV = ref('');
const openedYaml = ref('');
const dirty = ref(false);
const editorDoc = computed(() => (mode.value === 'view' ? props.payload.yaml : openedYaml.value));
const changedOnServer = computed(() => mode.value !== 'view' && props.payload.resourceVersion !== openedRV.value);
const justUpdated = ref(false);

// A live frame in view mode refreshes the read-only editor itself; say so briefly.
watch(
    () => props.frames,
    () => {
        if (mode.value === 'view' && props.frames > 1) {
            justUpdated.value = true;
            setTimeout(() => (justUpdated.value = false), 4000);
        }
    }
);

// A different object resets everything.
watch(
    () => `${props.target.cluster}|${props.target.gvr}|${props.target.namespace}|${props.target.name}`,
    () => {
        mode.value = 'view';
        revealed.value = false;
        dirty.value = false;
        review.value = null;
        conflict.value = null;
    }
);

// Leaving the object, the tab or the page with unsaved edits asks first; the editor itself never loses work to a stray click.
const removeGuard = router.beforeEach((to, from) => {
    if (mode.value === 'view' || !dirty.value || to.fullPath === from.fullPath) return true;
    return window.confirm('Discard your unsaved YAML edits?');
});
onBeforeUnmount(removeGuard);

function startEdit() {
    openedRV.value = props.payload.resourceVersion;
    openedYaml.value = props.payload.yaml;
    dirty.value = false;
    mode.value = 'edit';
    queueMicrotask(() => editor.value?.focus());
}

function discard() {
    mode.value = 'view';
    review.value = null;
    conflict.value = null;
}

function reloadLive(live: ObjectPayload = props.payload) {
    openedRV.value = live.resourceVersion;
    openedYaml.value = live.yaml;
    editor.value?.setValue(live.yaml);
    dirty.value = false;
    conflict.value = null;
    review.value = null;
    mode.value = 'edit';
}

// ---- review: server dry run + ownership conflicts + who it will run as ----
interface Review {
    draft: string;
    dry: ObjectDryRunResult | null;
    failure: string;
    viaWrite: boolean;
    busy: boolean;
}
const review = shallowRef<Review | null>(null);
const conflict = shallowRef<ObjectPayload | null>(null);
const applying = ref(false);

const writeContext = computed(() => clusters.writeContextOf(props.target.cluster));

async function startReview() {
    const draft = editor.value?.getValue() ?? openedYaml.value;
    if (draft === openedYaml.value) {
        ui.toast('info', 'No changes to review');
        return;
    }
    review.value = { draft, dry: null, failure: '', viaWrite: false, busy: true };
    conflict.value = null;
    mode.value = 'review';
    try {
        const [dry, access] = await Promise.all([
            getAgent().call<ObjectDryRunResult>('object.dryRun', { ref: props.target, yaml: draft, mode: 'update' }),
            checkAccess([
                { cluster: props.target.cluster, verb: 'update', gvr: props.target.gvr, namespace: props.target.namespace, name: props.target.name }
            ]).catch(() => [])
        ]);
        if (review.value?.draft === draft) review.value = { draft, dry, failure: '', viaWrite: !!access[0]?.viaWrite, busy: false };
    } catch (e) {
        if (review.value?.draft === draft) review.value = { draft, dry: null, failure: errorText(e), viaWrite: false, busy: false };
    }
}

let pendingRequest: string | null = null;

async function apply(overwrite = false) {
    const r = review.value;
    if (!r || applying.value) return;
    applying.value = true;
    const live = overwrite ? conflict.value : null;
    // One id per intent: a retry after a dropped connection reuses it, so the agent cannot apply twice.
    pendingRequest ??= newRequestId();
    try {
        const res = await getAgent().call<{ resourceVersion: string; usedContext: string }>('object.apply', {
            requestId: pendingRequest,
            ref: props.target,
            yaml: r.draft,
            mode: 'update',
            resourceVersion: live ? live.resourceVersion : openedRV.value
        });
        pendingRequest = null;
        ui.toast('success', `Applied ${props.target.name}${res.usedContext ? ` as ${res.usedContext}` : ''}`);
        mode.value = 'view';
        review.value = null;
        conflict.value = null;
    } catch (e) {
        if (!(e instanceof RpcError && e.code === 'unavailable')) pendingRequest = null;
        if (e instanceof RpcError && e.code === 'conflict' && e.live) conflict.value = e.live;
        else ui.toast('danger', errorText(e));
    } finally {
        applying.value = false;
    }
}

const dryFailed = computed(() => !!review.value && (!!review.value.failure || (!!review.value.dry && !review.value.dry.ok)));
const canApply = computed(() => !!review.value && !review.value.busy && !dryFailed.value && !applying.value && !conflict.value);
const gitops = computed(() => props.payload.gitops);

async function copy() {
    try {
        await navigator.clipboard.writeText(props.payload.yaml);
        ui.toast('success', 'YAML copied');
    } catch {
        ui.toast('danger', 'Could not copy to the clipboard');
    }
}

defineExpose({ startEdit, mode });
</script>

<template>
    <div class="ypane">
        <div v-if="deleted" class="notice danger" role="alert"><b>This object was deleted on the server.</b></div>

        <template v-if="hidden">
            <div class="notice" role="note">
                <div>
                    <b>Secret values are hidden.</b> Revealing loads the full object, including base64-encoded values, into this view.
                    <div class="row"><button class="btn sm" type="button" data-testid="reveal" @click="revealed = true">Reveal values</button></div>
                </div>
            </div>
        </template>
        <template v-else>
            <div v-show="mode !== 'review'" class="pane">
                <div class="toolbar">
                    <template v-if="mode === 'view'">
                        <span class="tag">read-only</span>
                        <span v-if="justUpdated" class="tag info" role="status">updated from server</span>
                        <span class="muted small">resourceVersion <span class="mono">{{ payload.resourceVersion }}</span></span>
                        <div class="grow"></div>
                        <button class="btn sm" type="button" @click="copy">Copy</button>
                        <button class="btn sm primary" type="button" :disabled="tooLarge || deleted" data-testid="edit-yaml" @click="startEdit">
                            Edit
                        </button>
                    </template>
                    <template v-else>
                        <span class="tag warning">editing</span>
                        <span class="muted small">opened at <span class="mono">{{ openedRV }}</span></span>
                        <div class="grow"></div>
                        <button class="btn sm" type="button" @click="discard">Discard</button>
                        <button class="btn sm primary" type="button" :disabled="!dirty" data-testid="review" @click="startReview">
                            Review changes
                        </button>
                    </template>
                </div>
                <div v-if="tooLarge" class="notice warn" role="note">This document is over 1 MB, so it opens read-only.</div>
                <div v-if="changedOnServer" class="notice warn" role="alert" data-testid="changed-notice">
                    <div>
                        <b>Changed on the server.</b> This object is now at <span class="mono">{{ payload.resourceVersion }}</span>; you opened
                        <span class="mono">{{ openedRV }}</span>.
                        <div class="row">
                            <button class="btn sm" type="button" @click="reloadLive()">Reload (discard my edits)</button>
                            <span class="muted small">or keep editing; Apply will tell you about any conflict.</span>
                        </div>
                    </div>
                </div>
                <div class="editor">
                    <YamlEditor
                        ref="editor"
                        :initial="editorDoc"
                        :read-only="mode === 'view' || tooLarge"
                        :label="`YAML of ${target.name}`"
                        @dirty="dirty = $event"
                    />
                </div>
            </div>

            <div v-if="mode === 'review' && review" class="pane review" data-testid="review-pane">
                <div class="toolbar">
                    <b>Review changes</b>
                    <span class="muted small">against live resourceVersion <span class="mono">{{ payload.resourceVersion }}</span></span>
                </div>
                <div class="scroll">
                    <div v-if="review.busy" class="notice" role="status">Running a server-side dry run…</div>
                    <div v-if="conflict" class="notice danger" role="alert" data-testid="conflict-notice">
                        <div>
                            <b>Someone changed this object while you were editing.</b> Opened <span class="mono">{{ openedRV }}</span>, now
                            <span class="mono">{{ conflict.resourceVersion }}</span>. Below: what changed on the server since you opened it, and your
                            changes against the live object.
                            <div class="row">
                                <button class="btn sm" type="button" data-testid="conflict-reload" @click="reloadLive(conflict)">
                                    Reload live version
                                </button>
                                <button
                                    class="btn sm danger"
                                    type="button"
                                    data-testid="conflict-overwrite"
                                    :disabled="applying"
                                    @click="apply(true)"
                                >
                                    Overwrite with mine
                                </button>
                            </div>
                        </div>
                    </div>
                    <div v-else-if="changedOnServer" class="notice warn" role="note">
                        The live object changed since you opened it (<span class="mono">{{ openedRV }}</span> to
                        <span class="mono">{{ payload.resourceVersion }}</span>); Apply may report a conflict.
                    </div>
                    <div v-if="gitops" class="notice warn" role="note">
                        <div>
                            <b>Managed by {{ GITOPS_LABEL[gitops.kind] ?? gitops.kind }}.</b>
                            {{ gitops.kind }} <span class="mono">{{ gitops.namespace ? gitops.namespace + '/' : '' }}{{ gitops.name }}</span> may
                            revert this change. Change it at the source, or suspend it first.
                        </div>
                    </div>
                    <div v-for="c in review.dry?.conflicts ?? []" :key="c.path + c.manager" class="notice warn" role="note">
                        <div>
                            <span class="mono">{{ c.path }}</span> is managed by <span class="mono">{{ c.manager }}</span>. It may revert this change.
                        </div>
                    </div>
                    <div v-for="w in review.dry?.warnings ?? []" :key="w" class="notice warn" role="note"><div>{{ w }}</div></div>
                    <div v-if="review.failure" class="notice danger" role="alert"><div>Dry run failed: {{ review.failure }}</div></div>
                    <div v-else-if="review.dry && !review.dry.ok" class="notice danger" role="alert">
                        <div><b>The server rejected this change.</b> {{ review.dry.error?.message }}</div>
                    </div>
                    <div v-else-if="review.dry" class="notice ok" role="status"><div>Server dry run passed.</div></div>
                    <DiffView
                        :left="conflict ? openedYaml : payload.yaml"
                        :right="conflict ? conflict.yaml : review.draft"
                        :left-label="conflict ? 'Opened' : 'Live'"
                        :right-label="conflict ? 'Live now' : 'Yours'"
                    />
                    <DiffView v-if="conflict" :left="conflict.yaml" :right="review.draft" left-label="Live now" right-label="Yours" />
                </div>
                <div class="foot">
                    <span class="runas small">
                        Runs as
                        <template v-if="review.viaWrite"
                            ><span class="tag warning">write</span
                            ><span class="mono">{{ writeContext || 'the linked write context' }}</span></template
                        >
                        <template v-else>your current context</template>
                    </span>
                    <div class="grow"></div>
                    <button class="btn" type="button" @click="mode = 'edit'">Back to editor</button>
                    <button class="btn primary" type="button" :disabled="!canApply" data-testid="apply" @click="apply(false)">
                        {{ applying ? 'Applying…' : 'Apply' }}
                    </button>
                </div>
            </div>
        </template>
    </div>
</template>

<style scoped>
.ypane {
    display: flex;
    flex-direction: column;
    gap: 10px;
    min-height: 0;
    flex: 1 1 0;
}
.pane {
    display: flex;
    flex-direction: column;
    gap: 10px;
    min-height: 0;
    flex: 1 1 0;
}
.toolbar {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
}
.small {
    font-size: 12px;
}
.editor {
    flex: 1 1 0;
    min-height: 220px;
}
.scroll {
    flex: 1 1 0;
    min-height: 0;
    overflow: auto;
    display: flex;
    flex-direction: column;
    gap: 10px;
}
.scroll > :deep(*) {
    flex: none;
}
.foot {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 10px 0 0;
    border-top: 1px solid var(--border);
}
.runas {
    color: var(--text2);
    display: flex;
    align-items: center;
    gap: 6px;
}
.row {
    display: flex;
    gap: 8px;
    margin-top: 8px;
    align-items: center;
    flex-wrap: wrap;
}
</style>
