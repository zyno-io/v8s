<script setup lang="ts">
import type { ObjectDryRunResult, ObjectPayload, ObjectRef } from '@v8s/client';

import { RpcError } from '@v8s/client';
import { computed, defineAsyncComponent, onBeforeUnmount, ref, shallowRef, watch } from 'vue';
import { useRouter } from 'vue-router';

import { checkAccess } from '@/lib/access.ts';
import { newRequestId } from '@/lib/actions.ts';
import { getAgent } from '@/lib/agent.ts';
import { editRequest } from '@/lib/drawer.ts';
import { callMutating, errorText, isCancelled, newWriteDecision, runAs } from '@/lib/mutations.ts';
import { GITOPS_LABEL } from '@/lib/overview.ts';
import { PRETTY_MAX, prettyKind, prettyYaml } from '@/lib/pretty-yaml.ts';
import { KEY_YAML_PRETTY, KEY_YAML_SECRET_DECODED, KEY_YAML_WRAP, readFlag, readFlagDefaultOn, writeFlag } from '@/lib/remember.ts';
import { useUiStore } from '@/stores/ui.ts';

// CodeMirror and the diff view are lazy chunks: nothing here loads until the YAML tab is opened.
const YamlEditor = defineAsyncComponent(() => import('./YamlEditor.vue'));
const DiffView = defineAsyncComponent(() => import('./DiffView.vue'));

// `masked`: a Secret fetched without its values. Editing it asks the parent to reveal first (`reveal-edit`).
const props = defineProps<{ target: ObjectRef; payload: ObjectPayload; deleted: boolean; frames: number; masked?: boolean }>();
// `synced`: a revealed Secret was re-read (reload after a conflict); `applied`: an apply went through, so the parent should re-fetch.
const emit = defineEmits<{ 'reveal-edit': []; 'reveal-reload': []; synced: [payload: ObjectPayload]; applied: [] }>();

const ui = useUiStore();
const router = useRouter();

type Mode = 'view' | 'edit' | 'review';
const mode = ref<Mode>('view');
const editor = ref<{ getValue(): string; setValue(t: string): void; focus(): void } | null>(null);

const LARGE = 1024 * 1024;
const tooLarge = computed(() => props.payload.yaml.length > LARGE);

// ---- display options, remembered per browser ----
const wrap = ref(readFlag(KEY_YAML_WRAP));
const pretty = ref(readFlag(KEY_YAML_PRETTY));
// Secrets have their own toggle, "Decoded" (values as text), which is on by default.
const decoded = ref(readFlagDefaultOn(KEY_YAML_SECRET_DECODED));
watch(wrap, v => writeFlag(KEY_YAML_WRAP, v));
watch(pretty, v => writeFlag(KEY_YAML_PRETTY, v));
watch(decoded, v => writeFlag(KEY_YAML_SECRET_DECODED, v));

const kind = computed(() => prettyKind(props.target.gvr));
const isMaskedSecret = computed(() => kind.value === 'secret' && !!props.masked);
const prettyTooBig = computed(() => props.payload.yaml.length > PRETTY_MAX);
const toggleOn = computed(() => (kind.value === 'secret' ? decoded.value : pretty.value));
// A masked Secret has only placeholders: nothing to decode, so it shows as is.
const prettyOn = computed(() => toggleOn.value && !!kind.value && !prettyTooBig.value && !isMaskedSecret.value);
function toggleDisplay() {
    if (kind.value === 'secret') decoded.value = !decoded.value;
    else pretty.value = !pretty.value;
}

// The view-mode text with Pretty applied. `src` ties it to the YAML it was made from, so a newer payload never shows a stale rendering.
const prettyView = shallowRef<{ src: string; out: string } | null>(null);
let prettySeq = 0;
let prettyTimer: ReturnType<typeof setTimeout> | undefined;
const PRETTY_DEBOUNCE_MS = 100;
// Only the view needs it: edit and review work on the decoded document, made once on demand. A burst of live frames transforms once.
watch(
    [() => props.payload.yaml, prettyOn, kind, mode],
    (now, before) => {
        clearTimeout(prettyTimer);
        const my = ++prettySeq;
        const src = props.payload.yaml;
        if (mode.value !== 'view') return;
        if (!prettyOn.value || !kind.value) {
            prettyView.value = null;
            return;
        }
        const k = kind.value;
        const run = async () => {
            const out = await prettyYaml(src, k, { formatJson: true, decode: k === 'secret' });
            if (my === prettySeq) prettyView.value = out === null ? null : { src, out };
        };
        // The first rendering (toggle on, object opened, back to view mode) is immediate; later frames are debounced, and the last
        // rendering stays on screen meanwhile.
        if (prettyView.value && before && now[3] === before[3] && now[1] === before[1])
            prettyTimer = setTimeout(() => void run(), PRETTY_DEBOUNCE_MS);
        else void run();
    },
    { immediate: true }
);
onBeforeUnmount(() => clearTimeout(prettyTimer));
const viewText = computed(() => (prettyView.value ? prettyView.value.out : props.payload.yaml));

// "Mask" drops the values: nothing decoded or revealed stays here.
watch(
    () => props.masked,
    masked => {
        if (!masked) return;
        openedYaml.value = '';
        liveForm.value = '';
        conflictForm.value = '';
        conflict.value = null;
        prettyView.value = null;
        decodedEdit.value = false;
    }
);

// A revealed Secret with Pretty on is edited in decoded form (values as text in stringData; the API server turns them back into data).
// JSON reformatting is never part of an edit document, because it would change the stored bytes.
const decodedEdit = ref(false);
const wantsDecoded = () => prettyOn.value && kind.value === 'secret' && !props.masked;
async function editForm(yaml: string, decode = decodedEdit.value): Promise<string> {
    if (!decode) return yaml;
    return (await prettyYaml(yaml, 'secret', { formatJson: false, decode: true })) ?? yaml;
}

// ---- the document the editor shows: live YAML in view mode, the opened version while editing ----
const openedRV = ref('');
const openedYaml = ref('');
const dirty = ref(false);
const editorDoc = computed(() => (mode.value === 'view' ? viewText.value : openedYaml.value));
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
        dirty.value = false;
        review.value = null;
        conflict.value = null;
        prettyView.value = null;
    }
);

// Leaving the object, the tab or the page with unsaved edits asks first; the editor itself never loses work to a stray click.
const removeGuard = router.beforeEach((to, from) => {
    if (mode.value === 'view' || !dirty.value || to.fullPath === from.fullPath) return true;
    return window.confirm('Discard your unsaved YAML edits?');
});
onBeforeUnmount(removeGuard);

async function startEdit() {
    const live = props.payload;
    // Checked here, not only on the button: "Reveal and edit" arrives with the revealed payload, which may be over the limit.
    if (mode.value !== 'view' || props.deleted || live.yaml.length > LARGE || isMaskedSecret.value) return;
    const decode = wantsDecoded();
    const text = await editForm(live.yaml, decode);
    if (mode.value !== 'view') return;
    // A Secret that was masked again (or re-fetched) while decoding must not open the values it no longer shows.
    if (kind.value === 'secret' && (props.masked || props.payload !== live)) return;
    decodedEdit.value = decode;
    liveForm.value = text;
    openedRV.value = live.resourceVersion;
    openedYaml.value = text;
    dirty.value = false;
    mode.value = 'edit';
    queueMicrotask(() => editor.value?.focus());
}

function editClicked() {
    if (isMaskedSecret.value) emit('reveal-edit');
    else void startEdit();
}

// The live document in the same form as the edit document, so the review diff compares like with like.
const liveForm = ref('');
watch(
    () => props.payload.yaml,
    async yaml => {
        if (mode.value === 'view') return;
        const f = await editForm(yaml);
        if (props.payload.yaml === yaml) liveForm.value = f;
    }
);

// The `e` shortcut asks for edit mode; honoured once the document is here and editable.
watch(
    editRequest,
    want => {
        if (!want) return;
        editRequest.value = false;
        if (mode.value === 'view' && !tooLarge.value && !props.deleted) editClicked();
    },
    { immediate: true }
);

function discard() {
    // The editor still holds the edits (it only reloads when its `initial` changes): put the live document back explicitly.
    editor.value?.setValue(viewText.value);
    dirty.value = false;
    mode.value = 'view';
    review.value = null;
    conflict.value = null;
}

async function reloadLive(live: ObjectPayload = props.payload) {
    // The server copy of a Secret came back masked (the reveal failed): re-reveal through the parent, which calls this again.
    if (conflictMasked.value && live === conflict.value) {
        emit('reveal-reload');
        return;
    }
    conflictMasked.value = false;
    const text = await editForm(live.yaml);
    openedRV.value = live.resourceVersion;
    openedYaml.value = text;
    editor.value?.setValue(text);
    dirty.value = false;
    conflict.value = null;
    if (kind.value === 'secret' && !props.masked && live !== props.payload) emit('synced', live);
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
const conflictForm = ref('');
const conflictMasked = ref(false);
const applying = ref(false);
// The write-context answer from the dry run carries into the apply, so the user is asked once.
const writeDecision = newWriteDecision();

async function startReview() {
    const draft = editor.value?.getValue() ?? openedYaml.value;
    if (draft === openedYaml.value) {
        ui.toast('info', 'No changes to review');
        return;
    }
    writeDecision.accepted = false;
    liveForm.value = await editForm(props.payload.yaml);
    review.value = { draft, dry: null, failure: '', viaWrite: false, busy: true };
    conflict.value = null;
    mode.value = 'review';
    try {
        const [dry, access] = await Promise.all([
            callMutating<ObjectDryRunResult>(
                props.target.cluster,
                'object.dryRun',
                { ref: props.target, yaml: draft, mode: 'update' },
                { decision: writeDecision }
            ),
            checkAccess([
                { cluster: props.target.cluster, verb: 'update', gvr: props.target.gvr, namespace: props.target.namespace, name: props.target.name }
            ]).catch(() => [])
        ]);
        if (review.value?.draft === draft) review.value = { draft, dry, failure: '', viaWrite: !!access[0]?.viaWrite, busy: false };
    } catch (e) {
        if (isCancelled(e)) {
            // The user declined the write context: back to the editor, nothing to report.
            if (review.value?.draft === draft) {
                review.value = null;
                mode.value = 'edit';
            }
        } else if (review.value?.draft === draft) review.value = { draft, dry: null, failure: errorText(e), viaWrite: false, busy: false };
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
        const res = await callMutating<{ resourceVersion: string; usedContext: string }>(
            props.target.cluster,
            'object.apply',
            {
                requestId: pendingRequest,
                ref: props.target,
                yaml: r.draft,
                mode: 'update',
                resourceVersion: live ? live.resourceVersion : openedRV.value
            },
            { decision: writeDecision, onRequestId: id => (pendingRequest = id) }
        );
        pendingRequest = null;
        ui.toast('success', `Applied ${props.target.name}${res.usedContext ? ` as ${res.usedContext}` : ''}`);
        mode.value = 'view';
        review.value = null;
        conflict.value = null;
        if (kind.value === 'secret') emit('applied');
    } catch (e) {
        if (!(e instanceof RpcError && e.code === 'unavailable')) pendingRequest = null;
        if (e instanceof RpcError && e.code === 'conflict' && e.live) {
            // The agent's conflict payload has masked Secret values; compare against the revealed server copy instead.
            let live = e.live;
            conflictMasked.value = false;
            if (kind.value === 'secret' && !props.masked) {
                try {
                    live = await getAgent().call<ObjectPayload>('object.get', {
                        cluster: props.target.cluster,
                        gvr: props.target.gvr,
                        namespace: props.target.namespace,
                        name: props.target.name,
                        reveal: true
                    });
                } catch {
                    conflictMasked.value = true;
                }
            }
            conflictForm.value = await editForm(live.yaml);
            conflict.value = live;
        } else if (!isCancelled(e)) ui.toast('danger', errorText(e));
    } finally {
        applying.value = false;
    }
}

const dryFailed = computed(() => !!review.value && (!!review.value.failure || (!!review.value.dry && !review.value.dry.ok)));
const canApply = computed(() => !!review.value && !review.value.busy && !dryFailed.value && !applying.value && !conflict.value);
const gitops = computed(() => props.payload.gitops);

async function copy() {
    try {
        await navigator.clipboard.writeText(viewText.value);
        ui.toast('success', 'YAML copied');
    } catch {
        ui.toast('danger', 'Could not copy to the clipboard');
    }
}

defineExpose({ startEdit, reloadLive, mode });
</script>

<template>
    <div class="ypane">
        <div v-if="deleted" class="notice danger" role="alert"><b>This object was deleted on the server.</b></div>

        <div v-show="mode !== 'review'" class="pane">
            <div class="toolbar">
                <template v-if="mode === 'view'">
                    <slot name="status"></slot>
                    <span v-if="justUpdated" class="tag info" role="status">updated from server</span>
                    <span class="muted small" title="resourceVersion">rv <span class="mono">{{ payload.resourceVersion }}</span></span>
                    <div class="grow"></div>
                    <span v-if="kind && prettyTooBig" class="muted small">
                        {{ kind === 'secret' ? 'Decoded' : 'Pretty' }} is skipped for documents over 256 KB
                    </span>
                    <button
                        v-if="kind"
                        class="btn sm"
                        type="button"
                        :aria-pressed="toggleOn"
                        :data-testid="kind === 'secret' ? 'yaml-decoded' : 'yaml-pretty'"
                        :title="
                            kind === 'secret'
                                ? 'Show values as text (stringData), with JSON indented'
                                : 'Indent JSON values and show multi-line values as blocks'
                        "
                        @click="toggleDisplay"
                    >
                        {{ kind === 'secret' ? 'Decoded' : 'Pretty' }}
                    </button>
                    <button class="btn sm" type="button" :aria-pressed="wrap" data-testid="yaml-wrap" @click="wrap = !wrap">Wrap</button>
                    <button class="btn sm" type="button" @click="copy">Copy</button>
                    <slot name="actions"></slot>
                    <button class="btn sm primary" type="button" :disabled="tooLarge || deleted" data-testid="edit-yaml" @click="editClicked">
                        {{ isMaskedSecret ? 'Reveal and edit' : 'Edit' }}
                    </button>
                </template>
                <template v-else>
                    <slot name="status"></slot>
                    <span class="tag warning">editing</span>
                    <span class="muted small">opened at <span class="mono">{{ openedRV }}</span></span>
                    <span v-if="prettyOn && kind === 'configmap'" class="muted small" data-testid="edit-hint">Editing the raw YAML</span>
                    <span v-else-if="decodedEdit" class="muted small" data-testid="edit-hint">Editing decoded values</span>
                    <div class="grow"></div>
                    <button class="btn sm" type="button" :aria-pressed="wrap" data-testid="yaml-wrap" @click="wrap = !wrap">Wrap</button>
                    <slot name="actions"></slot>
                    <button class="btn sm" type="button" @click="discard">Discard</button>
                    <button class="btn sm primary" type="button" :disabled="!dirty" data-testid="review" @click="startReview">Review changes</button>
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
                    :wrap="wrap"
                    :label="`YAML of ${target.name}`"
                    @dirty="dirty = $event"
                />
            </div>
        </div>

        <div v-if="mode === 'review' && review" class="pane review" data-testid="review-pane">
            <div class="toolbar">
                <slot name="status"></slot>
                <b>Review changes</b>
                <span class="muted small">against live resourceVersion <span class="mono">{{ payload.resourceVersion }}</span></span>
            </div>
            <div class="scroll">
                <div v-if="review.busy" class="notice" role="status">Running a server-side dry run…</div>
                <div v-if="conflict && conflictMasked" class="notice warn" role="note" data-testid="conflict-masked">
                    The server copy's values could not be revealed, so they show as masked below. Reload to continue from the live object.
                </div>
                <div v-if="conflict" class="notice danger" role="alert" data-testid="conflict-notice">
                    <div>
                        <b>Someone changed this object while you were editing.</b> Opened <span class="mono">{{ openedRV }}</span>, now
                        <span class="mono">{{ conflict.resourceVersion }}</span>. Below: what changed on the server since you opened it, and your
                        changes against the live object.
                        <div class="row">
                            <button class="btn sm" type="button" data-testid="conflict-reload" @click="reloadLive(conflict)">
                                Discard my edits and reload
                            </button>
                            <button class="btn sm danger" type="button" data-testid="conflict-overwrite" :disabled="applying" @click="apply(true)">
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
                        {{ gitops.kind }} <span class="mono">{{ gitops.namespace ? gitops.namespace + '/' : '' }}{{ gitops.name }}</span> may revert
                        this change. Change it at the source, or suspend it first.
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
                    :left="conflict ? openedYaml : liveForm"
                    :right="conflict ? conflictForm : review.draft"
                    :left-label="conflict ? 'Opened' : 'Live'"
                    :right-label="conflict ? 'Live now' : 'Yours'"
                />
                <DiffView v-if="conflict" :left="conflictForm" :right="review.draft" left-label="Live now" right-label="Yours" />
            </div>
            <div class="foot">
                <span class="runas small">
                    <span v-if="review.viaWrite" class="tag warning">write</span>
                    {{ runAs(target.cluster, review.viaWrite) }}
                </span>
                <div class="grow"></div>
                <button class="btn" type="button" @click="mode = 'edit'">Back to editor</button>
                <button class="btn primary" type="button" :disabled="!canApply" data-testid="apply" @click="apply(false)">
                    {{ applying ? 'Applying…' : 'Apply' }}
                </button>
            </div>
        </div>
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
.btn[aria-pressed='true'] {
    background: var(--accent-soft);
    border-color: var(--focus);
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
